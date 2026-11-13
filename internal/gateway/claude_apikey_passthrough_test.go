package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestClaudeAPIKeyPassThroughAppendsMessagesAndBeta verifies the real
// pass-through path for a Claude API-key provider:
//  1. BaseURL without /messages gets /messages appended (regression: new-api
//     relays answered 404 "Invalid URL (POST /v1)").
//  2. The client's anthropic-beta header is forwarded, and the 1M-context
//     beta flag is injected for 1M-capable models (regression: relays rejected
//     with "1m 上下文已经全量可用，请启用 1m 上下文后重试").
func TestClaudeAPIKeyPassThroughAppendsMessagesAndBeta(t *testing.T) {
	var gotPath string
	var gotBeta string
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBeta = r.Header.Get("anthropic-beta")
		gotAuth = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			// BaseURL deliberately lacks /messages, mirroring https://…/v1.
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			AuthType: domain.AuthTypeAPIKey, BaseURL: upstream.URL + "/v1",
			APIKeySource: "literal:sk-test", AuthHeader: "x-api-key",
		}},
		Routes: []domain.Route{{
			ID: "r1", Name: "R1", ProviderID: "p1",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
		APIKeys: []domain.APIKey{{
			ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true,
		}},
	})
	server := NewServer(router, monitor.NewStore())

	body := `{"model":"claude-opus-5","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", "sk-gw")
	request.Header.Set("anthropic-version", "2023-06-01")
	// Client asked for an unrelated beta flag; it must survive the merge.
	request.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	recorder := httptest.NewRecorder()
	server.handleClaudeMessages(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages (base URL must gain /messages)", gotPath)
	}
	if gotAuth != "sk-test" {
		t.Fatalf("upstream x-api-key = %q", gotAuth)
	}
	if !strings.Contains(gotBeta, claudeContext1MBeta) {
		t.Fatalf("anthropic-beta %q must contain the 1M-context flag %q for claude-opus-5", gotBeta, claudeContext1MBeta)
	}
	if !strings.Contains(gotBeta, "interleaved-thinking-2025-05-14") {
		t.Fatalf("client's anthropic-beta flag must be forwarded, got %q", gotBeta)
	}

	// Sanity: non-1M model must NOT get the 1M flag injected.
	gotBeta = ""
	body2 := `{"model":"claude-3-5-haiku-20241022","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	request2 := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body2))
	request2.Header.Set("x-api-key", "sk-gw")
	request2.Header.Set("anthropic-version", "2023-06-01")
	recorder2 := httptest.NewRecorder()
	server.handleClaudeMessages(recorder2, request2)
	if recorder2.Code != http.StatusOK {
		t.Fatalf("second request status = %d", recorder2.Code)
	}
	if strings.Contains(gotBeta, claudeContext1MBeta) {
		t.Fatalf("non-1M model must not receive the 1M flag, got %q", gotBeta)
	}

	// The response must be valid Claude JSON the client can parse.
	var parsed map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
}
