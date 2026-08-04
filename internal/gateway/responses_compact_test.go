package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestEncodeDecodeCompactionBlobRoundTrip(t *testing.T) {
	t.Parallel()
	blob := encodeCompactionBlob("the user wants X, we decided Y, TODO: Z")
	if !strings.HasPrefix(blob, compactionBlobPrefix) {
		t.Fatalf("expected blob to carry our prefix, got %q", blob)
	}
	summary, ok := decodeCompactionBlob(blob)
	if !ok {
		t.Fatal("expected decode to succeed on our own blob")
	}
	if summary != "the user wants X, we decided Y, TODO: Z" {
		t.Fatalf("round-trip mismatch: %q", summary)
	}
}

func TestDecodeCompactionBlobRejectsForeignContent(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "not-ours", "lpg1:not-valid-base64!!!", "lpg1:" + "aGVsbG8="} {
		if _, ok := decodeCompactionBlob(bad); ok {
			t.Fatalf("decodeCompactionBlob(%q) should not succeed", bad)
		}
	}
}

func TestExpandCompactionItemsReplacesOurOwnToken(t *testing.T) {
	t.Parallel()
	blob := encodeCompactionBlob("earlier summary text")
	items := []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "compaction", "encrypted_content": blob},
	}
	out := expandCompactionItems(items)
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out))
	}
	// First item untouched.
	first, _ := out[0].(map[string]any)
	if stringValue(first["type"]) != "message" || stringValue(first["role"]) != "user" {
		t.Fatalf("first item should be untouched: %+v", first)
	}
	// Second item expanded into a plain message carrying the decoded summary.
	second, ok := out[1].(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", out[1])
	}
	if stringValue(second["type"]) != "message" {
		t.Fatalf("compaction item should expand to type=message, got %+v", second)
	}
	blocks, _ := second["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block, got %+v", second["content"])
	}
	block, _ := blocks[0].(map[string]any)
	if !strings.Contains(stringValue(block["text"]), "earlier summary text") {
		t.Fatalf("expanded text missing decoded summary: %+v", block)
	}
}

func TestExpandCompactionItemsFallsBackOnForeignBlob(t *testing.T) {
	t.Parallel()
	items := []any{
		map[string]any{"type": "compaction", "encrypted_content": "some-real-openai-ciphertext"},
	}
	out := expandCompactionItems(items)
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	item, _ := out[0].(map[string]any)
	blocks, _ := item["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	if !strings.Contains(stringValue(block["text"]), "some-real-openai-ciphertext") {
		t.Fatalf("foreign blob should be carried forward as opaque text: %+v", block)
	}
}

func TestExpandCompactionItemsPassesThroughNonCompactionItems(t *testing.T) {
	t.Parallel()
	items := []any{
		map[string]any{"type": "function_call", "name": "shell"},
		"not-even-a-map",
	}
	out := expandCompactionItems(items)
	if len(out) != 2 {
		t.Fatalf("expected pass-through of all items, got %d", len(out))
	}
	if out[1] != "not-even-a-map" {
		t.Fatalf("non-map item should be passed through unchanged, got %+v", out[1])
	}
}

// TestHandleOpenAIResponsesCompactEndToEnd reproduces the production incident:
// Codex CLI calling POST /openai/v1/responses/compact and hitting a 404
// because the endpoint didn't exist. Wires a real Server+Router with a fake
// upstream standing in for whatever real provider the route points at, sends
// a compact request, and asserts the response is a well-formed
// response.compaction object with exactly one compaction output item whose
// encrypted_content decodes back to a real summary.
func TestHandleOpenAIResponsesCompactEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Fake upstream "summarizes" by just acknowledging it saw the instructions.
		if stringValue(req["instructions"]) == "" {
			t.Error("expected instructions to be forwarded to the upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_upstream_1",
			"object": "response",
			"output": []any{
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "output_text", "text": "User wants a login page; decided to use JWT; TODO: add refresh tokens."},
					},
				},
			},
			"usage": map[string]any{"input_tokens": 42, "output_tokens": 17},
		})
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID:           "p1",
		Name:         "p1",
		Protocol:     domain.ProtocolOpenAIResponses,
		AuthType:     domain.AuthTypeAPIKey,
		BaseURL:      upstream.URL + "/v1/responses",
		APIKeySource: "sk-upstream-test",
		HealthStatus: "healthy",
	}
	route := domain.Route{
		ID:             "r1",
		Name:           "r1",
		ProviderID:     provider.ID,
		OutputProtocol: domain.ProtocolOpenAIResponses,
		Mode:           domain.RouteModeAuto,
		Enabled:        true,
	}
	apiKey := domain.APIKey{
		ID:            "k1",
		Name:          "k1",
		Key:           "sk-gw-test-compact-key",
		RouteID:       route.ID,
		Enabled:       true,
		StreamEnabled: true,
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	server := NewServer(router, monitor.NewStore())
	handler := server.Handler()

	reqBody := map[string]any{
		"model": "gpt-5.5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "please build me a login page"},
		},
	}
	buf, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", strings.NewReader(string(buf)))
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v (%s)", err, rec.Body.String())
	}
	if stringValue(resp["object"]) != "response.compaction" {
		t.Fatalf(`expected object="response.compaction", got %+v`, resp["object"])
	}
	output, ok := resp["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected exactly one output item (Codex hard-fails otherwise), got %+v", resp["output"])
	}
	item, _ := output[0].(map[string]any)
	if stringValue(item["type"]) != "compaction" {
		t.Fatalf(`expected output[0].type="compaction", got %+v`, item)
	}
	summary, ok := decodeCompactionBlob(stringValue(item["encrypted_content"]))
	if !ok {
		t.Fatalf("encrypted_content should decode via our own scheme: %+v", item["encrypted_content"])
	}
	if !strings.Contains(summary, "JWT") {
		t.Fatalf("expected the upstream's summary to be carried through, got %q", summary)
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatal("expected a usage object in the response")
	}

	// Feed the compaction output back into a normal /responses call and
	// confirm expandCompactionItems unpacks it instead of forwarding the
	// opaque token upstream untouched.
	var sawExpanded bool
	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if items, ok := req["input"].([]any); ok {
			for _, raw := range items {
				m, _ := raw.(map[string]any)
				if strings.Contains(stringValue(m["type"]), "message") {
					if blocks, ok := m["content"].([]any); ok {
						for _, b := range blocks {
							block, _ := b.(map[string]any)
							if strings.Contains(stringValue(block["text"]), "JWT") {
								sawExpanded = true
							}
						}
					}
				}
				if stringValue(m["type"]) == "compaction" {
					t.Error("a raw compaction item reached the upstream — expandCompactionItems did not run")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_upstream_2", "object": "response",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
			"usage":  map[string]any{"input_tokens": 5, "output_tokens": 1},
		})
	})

	followUp := map[string]any{
		"model": "gpt-5.5",
		"input": []any{item, map[string]any{"type": "message", "role": "user", "content": "continue"}},
	}
	followUpBuf, _ := json.Marshal(followUp)
	req2 := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(string(followUpBuf)))
	req2.Header.Set("Authorization", "Bearer "+apiKey.Key)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("follow-up request failed: %d %s", rec2.Code, rec2.Body.String())
	}
	if !sawExpanded {
		t.Fatal("expected the compaction token to be expanded into plain text before reaching the upstream")
	}
}

// TestFlattenInputToTranscript reproduces the shape of a real Codex history
// (message/function_call/function_call_output/reasoning items) and checks
// the flattened output: tool calls/results render as readable text,
// reasoning is dropped, and long fields get truncated.
func TestFlattenInputToTranscript(t *testing.T) {
	t.Parallel()
	items := []any{
		map[string]any{"type": "message", "role": "user", "content": "please read config.go"},
		map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "I should read the file first"}}},
		map[string]any{"type": "function_call", "name": "read_file", "arguments": `{"path":"config.go"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "package main\n..."},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Here's what I found."}}},
		map[string]any{"type": "function_call", "name": "noisy", "arguments": strings.Repeat("x", transcriptFieldMaxLen+500)},
	}
	transcript := flattenInputToTranscript(items)
	if strings.Contains(transcript, "I should read the file first") {
		t.Fatal("reasoning items must be dropped from the transcript")
	}
	if !strings.Contains(transcript, `[tool call] read_file({"path":"config.go"})`) {
		t.Fatalf("expected a readable tool-call line, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "[tool result] package main") {
		t.Fatalf("expected a readable tool-result line, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "[user] please read config.go") || !strings.Contains(transcript, "[assistant] Here's what I found.") {
		t.Fatalf("expected plain message lines, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "…(truncated)") {
		t.Fatal("expected the oversized tool-call arguments to be truncated")
	}
}

// TestHandleOpenAIResponsesCompactFlattensToolCallsWithoutToolsSchema
// reproduces the real production incident: Codex sends a compact request
// whose input contains function_call/function_call_output/reasoning items
// (needing the original request's `tools` schema to convert faithfully) —
// forwarding them as-is to a real upstream 502'd with "failed to parse tool
// arguments: invalid character ':' ...". The fix flattens history to plain
// text before the sub-call, so the upstream never sees a raw function_call
// item and never needs `tools` context it wasn't given.
func TestHandleOpenAIResponsesCompactFlattensToolCallsWithoutToolsSchema(t *testing.T) {
	var sawFunctionCallItem, sawToolsField bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["tools"]; ok {
			sawToolsField = true
		}
		if items, ok := req["input"].([]any); ok {
			for _, raw := range items {
				m, _ := raw.(map[string]any)
				if stringValue(m["type"]) == "function_call" || stringValue(m["type"]) == "function_call_output" {
					sawFunctionCallItem = true
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_upstream_3", "object": "response",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "summary ok"}}}},
			"usage":  map[string]any{"input_tokens": 10, "output_tokens": 2},
		})
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "p2", Name: "p2", Protocol: domain.ProtocolOpenAIResponses, AuthType: domain.AuthTypeAPIKey,
		BaseURL: upstream.URL + "/v1/responses", APIKeySource: "sk-upstream-test", HealthStatus: "healthy",
	}
	route := domain.Route{ID: "r2", Name: "r2", ProviderID: provider.ID, OutputProtocol: domain.ProtocolOpenAIResponses, Mode: domain.RouteModeAuto, Enabled: true}
	apiKey := domain.APIKey{ID: "k2", Name: "k2", Key: "sk-gw-test-compact-toolcalls", RouteID: route.ID, Enabled: true, StreamEnabled: true}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}, Routes: []domain.Route{route}, APIKeys: []domain.APIKey{apiKey}})
	server := NewServer(router, monitor.NewStore())
	handler := server.Handler()

	// Real-shaped history: a tool call + its output + a reasoning item + a
	// `tools` schema at the top level (present in the real request, but our
	// synthetic sub-call must not depend on it).
	reqBody := map[string]any{
		"model": "gpt-5.5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "read config.go please"},
			map[string]any{"type": "reasoning", "summary": []any{}},
			map[string]any{"type": "function_call", "name": "read_file", "arguments": `{"path":"config.go"}`, "call_id": "call_1"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "package main"},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "read_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		},
	}
	buf, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", strings.NewReader(string(buf)))
	req.Header.Set("Authorization", "Bearer "+apiKey.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if sawFunctionCallItem {
		t.Fatal("a raw function_call/function_call_output item reached the upstream — should have been flattened to plain text")
	}
	if sawToolsField {
		t.Fatal("the synthetic summarization request should not carry a tools schema at all")
	}
}
