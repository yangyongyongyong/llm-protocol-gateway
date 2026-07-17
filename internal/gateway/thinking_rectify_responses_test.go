package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestResponsesToClaudeThinkingRectifyRetry proves the Responses<->Claude
// conversion path (proxyClaudeToResponses -> proxyConvertedThroughClaude) now
// engages the thinking-signature rectifier on the "cannot be modified" 400 and
// transparently retries once with thinking blocks stripped, instead of
// forwarding the 400 to the client.
func TestResponsesToClaudeThinkingRectifyRetry(t *testing.T) {
	var calls int32
	var secondBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		buf := make([]byte, r.ContentLength)
		_, _ = io.ReadFull(r.Body, buf)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.3.content.16: ` + "`thinking`" + ` or ` + "`redacted_thinking`" + ` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response."}}`))
			return
		}
		secondBody = buf
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	provider := domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
		BaseURL: upstream.URL, APIKeySource: "literal:test-secret",
	}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}})
	server := NewServer(router, monitor.NewStore())

	block := map[string]any{"type": "thinking", "thinking": "secret reasoning", "signature": "sig-abc"}
	enc, ok := encodeAnthropicThinkingBlock(block)
	if !ok {
		t.Fatal("encode thinking block failed")
	}
	// Interleave thinking with a tool_use so the thinking block is NOT trailing
	// (otherwise dropTrailingAssistantThinking would remove it pre-flight).
	responsesReq := map[string]any{
		"model": "claude-sonnet-5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "reasoning", "encrypted_content": enc, "summary": []any{}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "do_thing", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
			map[string]any{"type": "message", "role": "user", "content": "continue"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	status, _, _, err := server.proxyClaudeToResponses(rec, req, provider, "claude-sonnet-5", responsesReq, false)
	if err != nil {
		t.Fatalf("proxyClaudeToResponses error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200 after rectify retry, got %d (body=%s)", status, rec.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 upstream calls (original + rectified retry), got %d", got)
	}
	if secondBody == nil {
		t.Fatal("no second (rectified) upstream request captured")
	}
	if strings.Contains(string(secondBody), `"type":"thinking"`) || strings.Contains(string(secondBody), `"type":"redacted_thinking"`) {
		t.Fatalf("rectified retry still contains thinking blocks: %s", secondBody)
	}
	if strings.Contains(string(secondBody), "sig-abc") {
		t.Fatalf("rectified retry still contains thinking signature: %s", secondBody)
	}
}
