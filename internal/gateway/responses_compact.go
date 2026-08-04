// POST /openai/v1/responses/compact — OpenAI's real Responses API exposes a
// standalone "compact this context window" endpoint, and Codex CLI calls it
// automatically on long sessions ("remote compaction"). We never implemented
// it, so any Codex client routed through this gateway hit a 404 mid-session:
// "Error running remote compact task: unexpected status 404 Not Found".
//
// Disabling Codex's own remote_compaction_v2 feature flag does not reliably
// prevent the attempt (verified: the flag was off in a real user's
// config.toml and the 404 still happened) — Codex appears to decide whether a
// provider is "official-OpenAI-like" independently of that flag, and once it
// decides yes, it calls this endpoint regardless. So the robust fix lives
// here: implement the endpoint for real.
//
// We do not need OpenAI's real (encrypted, OpenAI-only-readable) compaction
// format. Codex will only ever send our own compaction token back to THIS
// gateway (never to real OpenAI), so we define our own opaque encoding and
// only need to recognize it ourselves — see expandCompactionItems below.
package gateway

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// compactionBlobPrefix marks our own opaque compaction encoding, distinct
// from anything a real OpenAI-issued encrypted_content blob could look like,
// so we never mistake foreign content for ours.
const compactionBlobPrefix = "lpg1:"

// compactionSystemInstructions guides the summarization sub-call. Kept
// deliberately simple for v1 — good enough to stop the 404, not tuned.
const compactionSystemInstructions = "You are compacting a long conversation history to free up context space for a coding agent. Produce a concise but complete summary that preserves: the user's original goals and requirements, key decisions made so far, the current state of any in-progress task, and any unfinished TODO items. Do not repeat large code blocks verbatim unless essential. Respond with the summary text only, no preamble."

// encodeCompactionBlob wraps a plain-text summary into our own opaque token.
// Not real encryption — round-trips only through this gateway, never to real
// OpenAI, so opacity (not confidentiality) is all that's required.
func encodeCompactionBlob(summary string) string {
	payload, _ := json.Marshal(map[string]string{"summary": summary})
	return compactionBlobPrefix + base64.StdEncoding.EncodeToString(payload)
}

// decodeCompactionBlob reverses encodeCompactionBlob. Returns ok=false for
// anything that isn't our own encoding (wrong prefix, bad base64/JSON) so
// callers can fall back gracefully instead of erroring.
func decodeCompactionBlob(blob string) (string, bool) {
	blob = strings.TrimSpace(blob)
	if !strings.HasPrefix(blob, compactionBlobPrefix) {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(blob, compactionBlobPrefix))
	if err != nil {
		return "", false
	}
	var decoded struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", false
	}
	return decoded.Summary, true
}

// expandCompactionItems replaces any {"type":"compaction","encrypted_content":...}
// item (produced by our own handleOpenAIResponsesCompact, carried forward by
// the client into a later request) with a plain message item, before any
// protocol conversion or forwarding happens. This must run first, at the very
// top of every Responses-protocol entry point that reads req["input"] — once
// applied, convert_responses_claude.go / convert_openai_responses_chat.go /
// stripResponsesInputReasoning never need to know compaction items exist.
//
// A foreign/undecodable blob (wrong prefix, corrupt) is not an error: we just
// carry the raw string forward as opaque text, mirroring how
// responsesAgentMessagePlainText already treats an unrecognized encrypted_content.
func expandCompactionItems(items []any) []any {
	if len(items) == 0 {
		return items
	}
	out := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(strings.ToLower(stringValue(item["type"]))) != "compaction" {
			out = append(out, raw)
			continue
		}
		blob := stringValue(item["encrypted_content"])
		summary, ok := decodeCompactionBlob(blob)
		if !ok {
			summary = blob
		}
		out = append(out, map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": "[Compacted context from earlier in this conversation]\n" + summary,
			}},
		})
	}
	return out
}

// flattenInputToTranscript renders a Responses input array as plain text —
// one line per message/tool-call/tool-result — for the summarization
// sub-call. See handleOpenAIResponsesCompact for why raw items aren't
// forwarded as-is (missing `tools` schema context breaks the upstream's
// tool-call parsing). reasoning items are dropped: internal chain-of-thought
// isn't useful in a summary and some providers reject echoing it back anyway.
func flattenInputToTranscript(items []any) string {
	lines := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "reasoning":
			continue
		case "function_call":
			name := stringValue(item["name"])
			args := truncateForTranscript(stringValue(item["arguments"]), transcriptFieldMaxLen)
			lines = append(lines, "[tool call] "+name+"("+args+")")
		case "function_call_output":
			output := truncateForTranscript(responsesContentToString(item["output"]), transcriptFieldMaxLen)
			lines = append(lines, "[tool result] "+output)
		default:
			role := stringValue(item["role"])
			if role == "" {
				role = "user"
			}
			text := strings.TrimSpace(responsesContentToString(item["content"]))
			if text == "" {
				continue
			}
			lines = append(lines, "["+role+"] "+truncateForTranscript(text, transcriptFieldMaxLen))
		}
	}
	return strings.Join(lines, "\n\n")
}

// transcriptFieldMaxLen caps any single field (tool args/output/message text)
// in the flattened transcript, so one oversized tool result can't blow the
// whole summarization request past the model's own context window.
const transcriptFieldMaxLen = 4000

func truncateForTranscript(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

// bufferedResponseWriter is a minimal http.ResponseWriter that never touches
// a real client connection — used for the internal summarization sub-call
// inside handleOpenAIResponsesCompact, which must run through the normal
// executeProtocolFlowWithFailover pipeline (for fallback-provider support)
// without leaking its raw completion straight to the actual HTTP client.
type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header), status: http.StatusOK}
}

func (w *bufferedResponseWriter) Header() http.Header         { return w.header }
func (w *bufferedResponseWriter) WriteHeader(status int)      { w.status = status }
func (w *bufferedResponseWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func newResponsesID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "resp_" + time.Now().Format("20060102T150405.000000000")
	}
	return "resp_" + hex.EncodeToString(buf)
}

// handleOpenAIResponsesCompact implements OpenAI's standalone compaction
// endpoint. Real request: {model, input, instructions?, previous_response_id?}.
// Real response is a response.compaction object whose output array must
// contain exactly one {type:"compaction",...} item — Codex hard-fails
// otherwise (see near_empty_response_dump.go's doc comment, which already
// documents this "expected exactly one compaction output item" requirement).
//
// previous_response_id is intentionally not given real (stateful) semantics:
// this gateway is stateless per-request, and the input array already carries
// whatever history the client wants preserved.
func (s *Server) handleOpenAIResponsesCompact(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req map[string]any
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	} else {
		req = map[string]any{}
	}

	var route domain.Route
	var matchedKey domain.APIKey
	var gatewayKeyMatched bool
	if token := extractConsumerAPIKey(r); token != "" {
		if key, ok := s.router.APIKeyByToken(token); ok {
			matchedKey = key
			gatewayKeyMatched = true
			if key.RouteID != "" {
				route, err = s.router.RouteByID(key.RouteID)
			}
			if s.apiKeyStore != nil {
				s.apiKeyToucher.Touch(key.ID)
			}
		}
	}
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !gatewayKeyMatched {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	if route.OutputProtocol != domain.ProtocolOpenAIResponses {
		writeOpenAIError(w, http.StatusBadRequest, "this api key's route is not configured for OpenAI Responses")
		return
	}
	decision, err := s.router.Decide(route.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	decision = s.decisionForAPIKey(route, matchedKey, decision)

	requestModel, _ := req["model"].(string)
	model, logModel := resolveConsumerModel(s.router, route, matchedKey, gatewayKeyMatched, requestModel)

	items, _ := req["input"].([]any)
	items = expandCompactionItems(items)

	instructions := compactionSystemInstructions
	if extra := strings.TrimSpace(stringValue(req["instructions"])); extra != "" {
		instructions = compactionSystemInstructions + "\n\n" + extra
	}
	// Flatten the whole history into one plain-text transcript instead of
	// forwarding the raw function_call/function_call_output/reasoning items:
	// those need the original request's `tools` schema to convert correctly,
	// which we don't have reason to reconstruct for a pure summarization
	// call. Forwarding them bare caused a real upstream 502 ("failed to parse
	// tool arguments: invalid character ':' ...") the first time this shipped
	// — the upstream's tool-call parser choked without that context. Plain
	// text sidesteps the whole tool-conversion path; a summary doesn't need
	// replayable tool calls, just readable content.
	syntheticReq := map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": "Conversation transcript to compact:\n\n" + flattenInputToTranscript(items),
			}},
		}},
		"instructions": instructions,
		"stream":       false,
	}

	bufWriter := newBufferedResponseWriter()
	status, usage, upstreamBody, _, _, err := s.executeProtocolFlowWithFailover(
		bufWriter, r, route, decision, model, syntheticReq, domain.ProtocolOpenAIResponses, gatewayKeyMatched, matchedKey, gatewayKeyMatched,
	)
	if err != nil || status >= 400 {
		message := "compaction upstream call failed"
		if err != nil {
			message = err.Error()
		} else if len(upstreamBody) > 0 {
			message = summarizeUpstreamHTTPError(status, upstreamBody)
		}
		s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "compact", decision.ConversionLabel, r.URL.Path, http.StatusBadGateway, usage, body, []byte(message))
		s.logs.AddApp("error", "responses compact upstream call failed", message)
		writeOpenAIError(w, http.StatusBadGateway, message)
		return
	}

	var upstreamPayload map[string]any
	_ = json.Unmarshal(upstreamBody, &upstreamPayload)
	summary := strings.TrimSpace(responsesOutputText(upstreamPayload["output"]))
	if summary == "" {
		summary = "(compaction produced no summary text)"
	}

	usageJSON := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheTokens > 0 {
		usageJSON["input_tokens_details"] = map[string]any{"cached_tokens": usage.CacheTokens}
	}
	result := map[string]any{
		"id":         newResponsesID(),
		"object":     "response.compaction",
		"created_at": time.Now().Unix(),
		"output": []any{map[string]any{
			"type":              "compaction",
			"encrypted_content": encodeCompactionBlob(summary),
		}},
		"usage": usageJSON,
	}
	resultBody, _ := json.Marshal(result)
	s.recordRequestLogFromRequest(r, started, matchedKey, gatewayKeyMatched, route.ID, decision.ProviderID, logModel, "compact", decision.ConversionLabel, r.URL.Path, http.StatusOK, usage, body, resultBody)
	s.logs.AddApp("info", "handled responses compact request", "route="+route.ID+" model="+logModel)
	writeJSON(w, http.StatusOK, result)
}
