package gateway

// Qoder's request body, and the conversation identity inside it.
//
// Two things in this body decide whether the upstream prompt cache ever hits,
// and both are ids rather than content:
//
//   - session_id keys the cache. It belongs to the whole conversation, so it
//     must not change between turns — a fresh id per request means every turn
//     pays full price for a prefix the previous one had already sent.
//   - business.id / chat_record_id name one question. Billing and the usage
//     panel group an answer's tool steps by it, so it must change when the user
//     asks something new but not between the steps of one answer.
//
// Neither may be derived from anything that drifts between turns: a system
// message carrying the working directory or the time would change the key on
// every request, which is exactly how a cache-hit rate of 98% becomes 0%.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	// qoderSessionType and qoderChatTask are what Qoder's own client names its
	// request with; the endpoint routes on them, so a body naming another
	// session type is not the same request.
	qoderSessionType = "qodercli"
	qoderChatTask    = "FREE_INPUT"

	// qoderBusinessProduct is the product the turn is billed and grouped under.
	qoderBusinessProduct = "cli"

	// A conversation's window id lasts as long as the conversation does: six
	// hours idle, and no more than this many conversations remembered at once.
	qoderConversationTTL = 6 * time.Hour
	qoderConversationMax = 4096
	// qoderConversationGate is how much of a message goes into a conversation
	// key. Long enough to tell two prompts apart, short enough that a pasted
	// file does not become the key.
	qoderConversationGate = 400
	qoderConversationHead = 200

	// qoderToolResultKeep is how many characters of an old tool result survive
	// on each side. Everything older than the last result is cut to it, and the
	// marker that replaces the middle is a fixed string: the bytes a truncated
	// result sends must be identical on every later turn, or the prefix cache
	// misses on the very messages meant to be stable.
	qoderToolResultKeep   = 2000
	qoderToolResultMargin = 200
	qoderToolResultMarker = "\n[[qoder-proxy: 已省略中间内容；需要完整内容请重新执行该命令]]\n"
)

// qoderConversation is one question's upstream identity.
type qoderConversation struct {
	// windowID keys the upstream prompt cache: the whole conversation, stable
	// between turns.
	windowID string
	// turnID names this one question.
	turnID string
	// beginAt is when the question was first seen, in milliseconds. A retry or
	// a tool step re-sends the same turn, so it reuses this rather than
	// reporting a new start every time.
	beginAt int64
	at      time.Time
}

var qoderConversations = struct {
	mu      sync.Mutex
	entries map[string]qoderConversation
	order   []string
}{entries: map[string]qoderConversation{}}

// qoderBodyContext is what a request adds to the body besides its messages: the
// client's own conversation id when it sent one, the account's user type, and
// the site the provider belongs to (which decides how its tool history travels).
type qoderBodyContext struct {
	SessionKey string
	UserType   string
	Site       qoderSite
}

// qoderStructuredToolHistoryMode mirrors the vendor client's switch: "auto"
// (the default) lets the model decide, "on" forces structured tool turns, "off"
// forces the text envelope.
var qoderStructuredToolHistoryMode = "auto"

// qoderStructuredToolModels are the models known to accept a structured tool
// history. Everything else is sent the text envelope: several models answer
// `role:"tool"` turns with a provider_error and lose the whole turn.
var qoderStructuredToolModels = []string{"qwen", "glm", "gm4", "gm5", "deepseek", "dfmodel"}

// qoderStructuredToolHistory is whether this request's history goes out as real
// tool turns. Either every call id is intact and the model is one that reads
// them, or the whole history falls back to text — a half-structured history is
// the one shape no model accepts.
func qoderStructuredToolHistory(site qoderSite, model string, info qoderModelInfo, chat map[string]any) bool {
	if !qoderToolIDsComplete(chat) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(qoderStructuredToolHistoryMode)) {
	case "off", "false", "no", "disable", "disabled", "0":
		return false
	case "on", "true", "yes", "enable", "enabled", "1":
		return true
	}
	if site.AuthType != domain.AuthTypeQoderCN {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(info.Key))
	name := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range qoderStructuredToolModels {
		if strings.HasPrefix(key, prefix) || strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// qoderToolIDsComplete reports whether every tool turn can be paired with the
// call it answers. One missing id makes the structured form unreadable, and a
// model handed an unpaired result answers with a provider_error.
func qoderToolIDsComplete(chat map[string]any) bool {
	for _, item := range messageList(chat) {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(qoderStringOf(message["role"])) {
		case "tool":
			if strings.TrimSpace(qoderStringOf(message["tool_call_id"])) == "" {
				return false
			}
		case "assistant":
			declared, ok := message["tool_calls"].([]any)
			if !ok {
				continue
			}
			for _, entry := range declared {
				call, ok := entry.(map[string]any)
				if !ok {
					return false
				}
				if strings.TrimSpace(qoderStringOf(call["id"])) == "" {
					return false
				}
			}
		}
	}
	return true
}

// qoderTextualAssistantContent renders an assistant turn's calls the way the
// vendor client writes them into a conversation's history: the turn's own text,
// then the marker, then the calls as a JSON array. A model repeating that shape
// is what the stream's echo recovery reads back (see qoder_tool_echo.go).
func qoderTextualAssistantContent(message map[string]any, text string) string {
	declared, ok := message["tool_calls"].([]any)
	if !ok || len(declared) == 0 {
		return text
	}
	calls := make([]map[string]any, 0, len(declared))
	for _, entry := range declared {
		call, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		function, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(qoderStringOf(function["name"]))
		if name == "" {
			continue
		}
		arguments := function["arguments"]
		switch value := arguments.(type) {
		case string:
		default:
			if arguments == nil {
				arguments = "{}"
			} else if raw, err := qoderCompactJSON(value); err == nil {
				arguments = string(raw)
			} else {
				arguments = "{}"
			}
		}
		calls = append(calls, map[string]any{"name": name, "arguments": arguments})
	}
	if len(calls) == 0 {
		return text
	}
	encoded, err := qoderCompactJSON(calls)
	if err != nil {
		return text
	}
	return text + "\n\n" + qoderEchoMarkerText + "\n" + string(encoded)
}

// qoderTextualToolResult wraps a tool's output in the vendor client's envelope,
// naming the tool it came from when the call is known.
func qoderTextualToolResult(name, body string) string {
	label := qoderToolResultOpen
	if name != "" {
		label += " (" + name + ")"
	}
	return label + "]\n" + body
}

// qoderDigest is a stable hex digest for a conversation key.
func qoderDigest(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// qoderHead cuts a message down to what a conversation key may carry: whole
// runes, never half a character.
func qoderHead(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// qoderConversationIdentity is the ids one request goes out with. A client that
// names its own conversation keeps that name as the window; one that names
// nothing gets a window derived from the model and the conversation's first
// prompt, so the same conversation keeps the same window whichever turn of it
// arrives first.
func qoderConversationIdentity(sessionKey, model, system string, chat map[string]any) qoderConversation {
	window := qoderHead(sessionKey, qoderConversationGate)
	if window == "" {
		window = "win:" + qoderDigest(model+"\x00"+qoderHead(qoderFirstUserText(chat), qoderConversationGate))
	}
	// The system prompt is part of the question's key, not the window's: a
	// client that rewrites it between turns (a cwd, a clock) would otherwise
	// walk the whole conversation onto a new window every time.
	turn := "turn:" + qoderDigest(model+"\x00"+qoderHead(system, qoderConversationHead)+"\x00"+qoderHead(qoderLastUserText(chat), qoderConversationGate))
	key := turn
	if sessionKey != "" {
		key = window + "|" + turn
	}
	now := time.Now()
	if cached, ok := qoderConversationLookup(key, now); ok {
		cached.windowID = window
		cached.at = now
		return cached
	}
	conversation := qoderConversation{
		windowID: window,
		turnID:   newUUIDV4(),
		beginAt:  now.UnixMilli(),
		at:       now,
	}
	qoderConversationStore(key, conversation)
	return conversation
}

func qoderConversationLookup(key string, now time.Time) (qoderConversation, bool) {
	qoderConversations.mu.Lock()
	defer qoderConversations.mu.Unlock()
	entry, ok := qoderConversations.entries[key]
	if !ok || now.Sub(entry.at) > qoderConversationTTL {
		return qoderConversation{}, false
	}
	return entry, true
}

func qoderConversationStore(key string, conversation qoderConversation) {
	qoderConversations.mu.Lock()
	defer qoderConversations.mu.Unlock()
	if _, seen := qoderConversations.entries[key]; !seen {
		qoderConversations.order = append(qoderConversations.order, key)
	}
	qoderConversations.entries[key] = conversation
	// Oldest first: the map is only ever appended to, so the head of the order
	// is the stalest conversation.
	for len(qoderConversations.order) > qoderConversationMax {
		oldest := qoderConversations.order[0]
		qoderConversations.order = qoderConversations.order[1:]
		delete(qoderConversations.entries, oldest)
	}
}

// qoderFirstUserText is the conversation's opening prompt: the first thing the
// user asked, which does not change as the conversation grows.
func qoderFirstUserText(chat map[string]any) string {
	for _, item := range messageList(chat) {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role := strings.ToLower(qoderStringOf(message["role"])); role == "user" {
			if text := qoderTextOf(message["content"]); text != "" {
				return text
			}
		}
	}
	return ""
}

// qoderLastUserText is the question this request asks.
func qoderLastUserText(chat map[string]any) string {
	last := ""
	for _, item := range messageList(chat) {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role := strings.ToLower(qoderStringOf(message["role"])); role == "user" {
			if text := qoderTextOf(message["content"]); text != "" {
				last = text
			}
		}
	}
	return last
}

// qoderModelConfig is the model's own configuration as Qoder's client sends it.
// The endpoint reads the window and the vision/reasoning flags from here rather
// than from the request's parameters.
func qoderModelConfig(info qoderModelInfo, window int) map[string]any {
	maxInput := window
	if maxInput <= 0 {
		maxInput = info.MaxInputTokens
	}
	if maxInput <= 0 {
		maxInput = info.largestContext()
	}
	return map[string]any{
		"key":              info.Key,
		"display_name":     info.DisplayName,
		"model":            "",
		"format":           "openai",
		"is_vl":            info.Images,
		"is_reasoning":     info.Thinks,
		"api_key":          "",
		"url":              "",
		"source":           "system",
		"max_input_tokens": maxInput,
	}
}

// qoderRequestBody assembles the body one chat request goes out as. Every key a
// Qoder client writes is written here: the endpoint validates the shape of the
// request it is handed, and a body carrying only the keys a plain OpenAI
// gateway would send is not the shape its own client produces.
func qoderRequestBody(model string, chat map[string]any, info qoderModelInfo, context qoderBodyContext) ([]byte, error) {
	// Older tool results are trimmed before anything else reads the turns: the
	// bytes that reach the endpoint have to be the same on every later turn, or
	// the prefix cache misses on exactly the messages meant to be stable.
	chat = qoderShrinkToolTurns(chat)
	system := qoderSystemText(chat)
	tools := qoderToolsOf(chat)
	if len(tools) > 0 {
		// Qoder answers a request that offers tools with prose unless it is told
		// the reply has to be a call; the vendor's own system prompt says so.
		system += "\nYou must call an available function in this response."
	}
	prompt := qoderHead(qoderLastUserText(chat), 30)
	conversation := qoderConversationIdentity(context.SessionKey, model, system, chat)
	window := qoderWindowFor(chat, info, qoderRequestedMaxTokens(chat))
	modelConfig := qoderModelConfig(info, window)
	userType := context.UserType
	if userType == "" {
		userType = qoderDefaultUserType
	}
	thinking, effort := qoderEffort(qoderAskedEffort(chat), info)
	params := map[string]any{"enable_thinking": thinking, "max_tokens": qoderRequestedMaxTokens(chat)}
	if effort != "" {
		params["reasoning_effort"] = effort
	}
	// A model that cannot read structured tool turns gets the vendor client's
	// text envelope instead; both carry the same calls, in a shape the model
	// will actually answer.
	structured := qoderStructuredToolHistory(context.Site, model, info, chat)
	messages := append([]any{map[string]any{"role": "system", "content": system}}, qoderMessagesOf(chat, structured)...)
	body := map[string]any{
		"request_id":     newUUIDV4(),
		"request_set_id": newUUIDV4(),
		"chat_record_id": conversation.turnID,
		"stream":         true,
		"chat_task":      qoderChatTask,
		"chat_context": map[string]any{
			"chatPrompt": "",
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     modelConfig,
				"originalContent": map[string]any{"type": "text", "text": qoderLastUserText(chat)},
			},
			"features": []any{},
			"text":     map[string]any{"type": "text", "text": qoderLastUserText(chat)},
		},
		"image_urls":       nil,
		"is_reply":         true,
		"is_retry":         false,
		"session_id":       conversation.windowID,
		"code_language":    "",
		"source":           1,
		"version":          "3",
		"chat_prompt":      "",
		"parameters":       params,
		"aliyun_user_type": userType,
		"session_type":     qoderSessionType,
		"agent_id":         "agent_common",
		"task_id":          "common",
		"model_config":     modelConfig,
		"messages":         messages,
		"business": map[string]any{
			"product":   qoderBusinessProduct,
			"version":   qoderCOSYVersion,
			"type":      "agent",
			"id":        conversation.turnID,
			"name":      prompt,
			"begin_at":  conversation.beginAt,
			"stage":     "start",
			"model_key": info.Key,
		},
	}
	if len(tools) > 0 {
		body["tools"] = tools
	} else {
		body["tools"] = []any{}
	}
	return qoderCompactJSON(body)
}

// qoderShrinkToolResult cuts the middle out of an old tool result. The result a
// turn is answering stays whole; everything before it is trimmed to the same
// fixed bytes on every later turn, so a conversation's prefix stops growing
// with the size of files it has already read.
func qoderShrinkToolResult(text string) string {
	if len(text) <= qoderToolResultKeep*2+qoderToolResultMargin {
		return text
	}
	runes := []rune(text)
	if len(runes) <= qoderToolResultKeep*2+qoderToolResultMargin {
		return text
	}
	// A whole JSON document this short is kept: half an object is worse than a
	// long one, and it is still inside what the endpoint accepts.
	if len(runes) <= qoderToolResultKeep*4 && json.Valid([]byte(text)) {
		return text
	}
	head := string(runes[:qoderToolResultKeep])
	if at := strings.LastIndex(head, "\n"); at > qoderToolResultKeep/2 {
		head = head[:at]
	}
	tail := string(runes[len(runes)-qoderToolResultKeep:])
	if at := strings.Index(tail, "\n"); at >= 0 && at < qoderToolResultKeep/2 {
		tail = tail[at+1:]
	}
	return head + qoderToolResultMarker + tail
}

// qoderLastToolResultIndex is the tool turn the request is answering, which is
// the one result that must reach the endpoint whole.
func qoderLastToolResultIndex(chat map[string]any) int {
	last := -1
	for index, item := range messageList(chat) {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(qoderStringOf(message["role"]), "tool") {
			last = index
		}
	}
	return last
}

// qoderShrinkToolTurns applies the trim to every tool turn but the last.
func qoderShrinkToolTurns(chat map[string]any) map[string]any {
	last := qoderLastToolResultIndex(chat)
	if last < 0 {
		return chat
	}
	messages := messageList(chat)
	trimmed := make([]any, len(messages))
	copy(trimmed, messages)
	changed := false
	for index, item := range messages {
		if index == last {
			continue
		}
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if !strings.EqualFold(qoderStringOf(message["role"]), "tool") {
			continue
		}
		text, ok := message["content"].(string)
		if !ok {
			continue
		}
		shortened := qoderShrinkToolResult(text)
		if shortened == text {
			continue
		}
		replacement := make(map[string]any, len(message))
		for key, value := range message {
			replacement[key] = value
		}
		replacement["content"] = shortened
		trimmed[index] = replacement
		changed = true
	}
	if !changed {
		return chat
	}
	copied := make(map[string]any, len(chat))
	for key, value := range chat {
		copied[key] = value
	}
	copied["messages"] = trimmed
	return copied
}
