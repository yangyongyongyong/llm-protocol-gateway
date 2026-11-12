package gateway

import (
	"container/list"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// OpenCode Zen's free tier.
//
// Zen bills most of its models against an API key, but serves a set of free
// ones (-free, and big-pickle / grok-code, whose ids do not say so) to anyone
// at all — with two catches. The request has to carry OpenCode's own client
// identity ("OpenCode's free tier can only be used from within OpenCode",
// answered 403 otherwise), it has to stream, and it has to offer two tools
// named bash and read, each with at least one parameter. Since mid-October
// 2026 a free model also has to be asked at OpenCode's inference API rather
// than at Zen's own root, or it answers 403 even when the request is OpenCode's
// own word for word.
//
// So this provider kind is a plain OpenAI-Chat relay whose free models get
// dressed on the way out. A key the operator did paste is still used, and only
// the models the free tier serves are dressed, which leaves the paid ones
// billing normally against that key.

const (
	// openCodeZenFreeBase is Zen's OpenAI-compatible root; the free rewrite
	// moves it to /inference (openCodeZenInferenceURL).
	openCodeZenFreeBase = "https://opencode.ai/zen/v1"
	// openCodeZenAnonymousKey is what OpenCode itself asks Zen with when no
	// one is signed in to it; the free models answer that key.
	openCodeZenAnonymousKey = "public"
	// openCodeZenUserAgent is OpenCode 1.19.0's User-Agent, its ai-sdk and Bun
	// runtime included, as captured from the real client. Zen's free tier turns
	// away anything older than 1.18.0.
	openCodeZenUserAgent = "opencode/1.19.0 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"
	// openCodeZenToolNote is what a stubbed tool says about itself: the client
	// that sent this request does not have it, so calling it would be a mistake.
	openCodeZenToolNote = "Not available in this session. Never call this tool."
)

// openCodeZenFreeIDs are Zen's free models whose ids do not end in -free.
var openCodeZenFreeIDs = map[string]bool{"big-pickle": true, "grok-code": true}

// openCodeZenToolSchemas are the parameters OpenCode gives bash and read, sent
// verbatim when the client offered neither. Zen rejects either offered with no
// parameters (403 FreeTierError), so a bare {"properties":{}} stub is not
// enough; these are OpenCode's own.
var openCodeZenToolSchemas = map[string]string{
	"bash": `{"type":"object","properties":{"command":{"type":"string","description":"The command to execute"},"timeout":{"type":"integer","description":"Optional timeout in milliseconds"},"workdir":{"type":"string","description":"The working directory to run the command in"}},"required":["command"]}`,
	"read": `{"type":"object","properties":{"filePath":{"type":"string","description":"The absolute path to the file or directory to read"},"offset":{"type":"integer","description":"The line number to start reading from (1-indexed)"},"limit":{"type":"integer","description":"The maximum number of lines to read"}},"required":["filePath"]}`,
}

func isOpenCodeZenProvider(provider domain.Provider) bool {
	return provider.AuthType == domain.AuthTypeOpenCodeZenFree
}

// openCodeZenFreeFor is the one question the dressing asks of a request: is
// this a Zen provider asking a model the free tier serves.
func openCodeZenFreeFor(provider domain.Provider, model string) bool {
	return isOpenCodeZenProvider(provider) && openCodeZenFreeModel(model)
}

// openCodeZenFreeModel says whether a request has to be dressed for the free
// tier: the id names a -free model, or is one Zen prices at nothing under
// another spelling.
func openCodeZenFreeModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if strings.HasSuffix(model, "-free") || openCodeZenFreeIDs[model] {
		return true
	}
	// A model asked through a route alias ("<provider>/<model>") still names a
	// free model by its last segment.
	if i := strings.LastIndex(model, "/"); i >= 0 {
		tail := model[i+1:]
		return strings.HasSuffix(tail, "-free") || openCodeZenFreeIDs[tail]
	}
	return false
}

// chatBodyModel reads the model a chat body asks for. The dressing
// decides on it, and every call site has the body to hand, so this is how the
// model reaches a header-setting function that never sees one.
func chatBodyModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return payload.Model
}

// openCodeZenInferenceURL moves a Zen URL to the inference API the free tier
// is served from: the same path under /inference instead of /zen. Anything
// that is not Zen's own host, or is Zen's paid Go deployment, is returned
// unchanged.
func openCodeZenInferenceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return raw
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "opencode.ai" && !strings.HasSuffix(host, ".opencode.ai") {
		return raw
	}
	rest, ok := strings.CutPrefix(parsed.Path, "/zen")
	if !ok || rest != "" && rest[0] != '/' || rest == "/go" || strings.HasPrefix(rest, "/go/") {
		return raw
	}
	parsed.Path = "/inference" + rest
	return parsed.String()
}

// applyOpenCodeZenHeaders dresses a request for the free tier. It only ever
// touches a Zen provider asking a free model, so a paid model with the
// operator's own key goes out exactly as any other relay's would.
func applyOpenCodeZenHeaders(request *http.Request, provider domain.Provider, body []byte) {
	if request == nil || !isOpenCodeZenProvider(provider) || !openCodeZenFreeModel(chatBodyModel(body)) {
		return
	}
	// One session id per conversation, the way OpenCode keeps one per session:
	// Zen routes and prompt-caches by it, so a fresh id per request would make
	// every turn of one conversation a cold start. A client that names its own
	// session keeps it; otherwise the first user turn identifies the
	// conversation, which every later turn repeats.
	seed := strings.TrimSpace(request.Header.Get("x-opencode-session"))
	if !openCodeZenRealSession(seed) {
		if named := strings.TrimSpace(request.Header.Get("X-Session-Id")); openCodeZenRealSession(named) {
			seed = named
		}
	}
	if !openCodeZenRealSession(seed) {
		seed = provider.ID + "|" + openCodeZenFirstUserText(body)
	}
	session := openCodeZenSessionID(seed)
	request.Header.Set("User-Agent", openCodeZenUserAgent)
	request.Header.Set("x-opencode-session", session)
	request.Header.Set("x-opencode-session-id", session)
	request.Header.Set("x-opencode-request", openCodeZenNewID("msg", false))
	request.Header.Set("x-opencode-client", "cli")
	// OpenCode's project outside a git repository.
	request.Header.Set("x-opencode-project", "global")
	// Zen answers 403 FreeTierError to a free request carrying a session
	// affinity header, which other clients of this gateway may have sent.
	request.Header.Del("x-session-affinity")
	request.Header.Del("X-Session-Id")
}

// isOpenCodeGoProvider reports whether this provider points at OpenCode Go's
// paid deployment (…/zen/go/…).
//
// Go serves Claude on the same root as OpenAI (`/v1/messages`), unlike the
// `/anthropic` sibling layout most relays use. That endpoint also authenticates
// with `x-api-key` instead of the bearer the OpenAI routes accept, and refuses a
// request that carries no routing session header.
func isOpenCodeGoProvider(provider domain.Provider) bool {
	if provider.AuthType != domain.AuthTypeAPIKey {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil || parsed.Scheme == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "opencode.ai" && !strings.HasSuffix(host, ".opencode.ai") {
		return false
	}
	path := strings.TrimRight(strings.ToLower(parsed.Path), "/")
	return path == "/zen/go" || strings.HasPrefix(path, "/zen/go/")
}

// applyOpenCodeGoClaudeHeaders dresses a Claude-protocol request for OpenCode Go:
// the endpoint wants the key in `x-api-key` and a stable `x-opencode-session`,
// otherwise it answers 401 or "MissingSessionID" even though the route exists.
func applyOpenCodeGoClaudeHeaders(request *http.Request, provider domain.Provider, body []byte) {
	if request == nil || !isOpenCodeGoProvider(provider) {
		return
	}
	token := strings.TrimSpace(resolveProviderAuth(provider))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	if token != "" {
		request.Header.Set("x-api-key", token)
	}
	seed := strings.TrimSpace(request.Header.Get("x-opencode-session"))
	if !openCodeZenRealSession(seed) {
		seed = provider.ID + "|" + openCodeZenFirstUserText(body)
	}
	session := openCodeZenSessionID(seed)
	request.Header.Set("x-opencode-session", session)
	request.Header.Set("x-opencode-session-id", session)
	request.Header.Set("User-Agent", openCodeZenUserAgent)
}

// resolveOpenCodeZenURL points a free-model request at the inference API. It is
// called from the chat URL resolver, so every path that asks Zen gets it.
func resolveOpenCodeZenURL(provider domain.Provider, model, upstreamURL string) string {
	if !isOpenCodeZenProvider(provider) || !openCodeZenFreeModel(model) {
		return upstreamURL
	}
	return openCodeZenInferenceURL(upstreamURL)
}

// prepareOpenCodeZenChatBody shapes a chat body for the free tier: it must
// stream, and it must offer bash and read. The second result says the request
// is a free-tier one at all — a caller that asked for a single JSON object
// needs it to know the stream it now has to rebuild is coming. Idempotent, so
// a caller that already shaped the body is unaffected.
func prepareOpenCodeZenChatBody(provider domain.Provider, body []byte) ([]byte, bool) {
	if !isOpenCodeZenProvider(provider) {
		return body, false
	}
	model := chatBodyModel(body)
	if !openCodeZenFreeModel(model) {
		return body, false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body, true
	}
	if stream, _ := payload["stream"].(bool); !stream {
		payload["stream"] = true
	}
	openCodeZenAddTools(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, true
	}
	return encoded, true
}

// openCodeZenAddTools offers the two tools the free tier wants to see, adding a
// stub for one the request is missing. A tool the client sent under another
// case (Claude Code's Bash and Read) counts as sent: renaming it would mean
// renaming its calls back on the way in, and the gate only asks that a tool of
// that name exists.
func openCodeZenAddTools(payload map[string]any) bool {
	tools, _ := payload["tools"].([]any)
	have := map[string]bool{}
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if tool == nil {
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			if fn, _ := tool["function"].(map[string]any); fn != nil {
				name, _ = fn["name"].(string)
			}
		}
		have[strings.ToLower(strings.TrimSpace(name))] = true
	}
	added := false
	for _, need := range []string{"bash", "read"} {
		if have[need] {
			continue
		}
		var schema any
		if json.Unmarshal([]byte(openCodeZenToolSchemas[need]), &schema) != nil {
			continue
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        need,
				"description": openCodeZenToolNote,
				"parameters":  schema,
			},
		})
		added = true
	}
	if added {
		payload["tools"] = tools
	}
	return added
}

// rewriteOpenCodeZenUpstreamStreamedBody rebuilds the forced stream into one
// chat.completion for a client that never asked to stream. The dressing is
// invisible to that client: it gets the single object it asked for.
func rewriteOpenCodeZenUpstreamStreamedBody(response *http.Response, model string) error {
	return forcedStreamAsCompletion(response, model, workBuddyStreamAsCompletion)
}

// openCodeZenKeep is how many conversations keep one session id. Past this the
// least recently used are forgotten; a forgotten conversation just starts a
// fresh session, which costs its prompt cache and nothing else.
const openCodeZenKeep = 1000

var openCodeZenSessions = struct {
	sync.Mutex
	bySeed map[string]*list.Element
	order  *list.List // openCodeZenKept, least recently used first
}{bySeed: map[string]*list.Element{}, order: list.New()}

type openCodeZenKept struct{ seed, id string }

func openCodeZenSessionID(seed string) string {
	if openCodeZenRealSession(seed) {
		// A client that names its own session (OpenCode's, or anything that
		// copied its shape) is asked with that name, so its conversation keeps
		// the id it started with.
		return seed
	}
	if seed == "" {
		return openCodeZenNewID("ses", true)
	}
	id := openCodeZenNewID("ses", true) // made first: it takes no lock held here
	openCodeZenSessions.Lock()
	defer openCodeZenSessions.Unlock()
	if e, ok := openCodeZenSessions.bySeed[seed]; ok {
		openCodeZenSessions.order.MoveToBack(e)
		return e.Value.(openCodeZenKept).id
	}
	if openCodeZenSessions.order.Len() >= openCodeZenKeep {
		oldest := openCodeZenSessions.order.Front()
		openCodeZenSessions.order.Remove(oldest)
		delete(openCodeZenSessions.bySeed, oldest.Value.(openCodeZenKept).seed)
	}
	openCodeZenSessions.bySeed[seed] = openCodeZenSessions.order.PushBack(openCodeZenKept{seed, id})
	return id
}

// openCodeZenIDRe is the shape OpenCode gives its identifiers: a three-letter
// prefix, twelve hex digits of the time it was made, and fourteen characters
// of noise.
var openCodeZenIDRe = regexp.MustCompile(`^[a-z]{3}_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// openCodeZenRealSession says whether id is one OpenCode could have made: of
// its shape, with a small counter in the low bits of its time. Zen's free tier
// checks this and answers 403 FreeTierError to a made-up id — a hash cut to
// the same shape among them — so a session is never synthesized any other way.
func openCodeZenRealSession(id string) bool {
	if !strings.HasPrefix(id, "ses_") || !openCodeZenIDRe.MatchString(id) {
		return false
	}
	low, err := strconv.ParseUint(id[13:16], 16, 16)
	return err == nil && 0xfff-low < 16
}

// openCodeZenCounter is bumped within a millisecond so two ids made in the
// same one differ, the way OpenCode's own counter does.
var openCodeZenCounter struct {
	sync.Mutex
	last, count int64
}

// openCodeZenNewID makes an id the way OpenCode makes one: 12 hex digits of the
// clock in milliseconds times 0x1000 plus a per-millisecond count — bit-turned
// for a session, so the newest sorts first — then 14 base62 characters.
func openCodeZenNewID(prefix string, descending bool) string {
	ms := time.Now().UnixMilli()
	openCodeZenCounter.Lock()
	if ms != openCodeZenCounter.last {
		openCodeZenCounter.last, openCodeZenCounter.count = ms, 0
	}
	openCodeZenCounter.count++
	current := uint64(ms)*0x1000 + uint64(openCodeZenCounter.count)
	openCodeZenCounter.Unlock()
	if descending {
		current = ^current
	}
	var noise [14]byte
	if _, err := rand.Read(noise[:]); err != nil {
		return prefix
	}
	id := []byte(fmt.Sprintf("%s_%012x", prefix, current&0xffffffffffff))
	for _, c := range noise {
		id = append(id, openCodeZenBase62[int(c)%len(openCodeZenBase62)])
	}
	return string(id)
}

const openCodeZenBase62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func openCodeZenFirstUserText(body []byte) string {
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	for _, message := range payload.Messages {
		if !strings.EqualFold(message.Role, "user") {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			return text
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				if part.Text != "" {
					return part.Text
				}
			}
		}
		return ""
	}
	return ""
}
