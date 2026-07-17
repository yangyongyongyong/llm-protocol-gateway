package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestIsSelfCheckPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/__providers/p1/self-check/health", true},
		{http.MethodPost, "/__providers/p1/self-check/chat", true},
		{http.MethodGet, "/__providers/p1/self-check/health", false},
		{http.MethodPost, "/__providers/p1/self-register", false},
		{http.MethodPost, "/__providers/p1/self-register-token", false},
	}
	for _, tc := range cases {
		if got := isSelfCheckPath(tc.method, tc.path); got != tc.want {
			t.Fatalf("isSelfCheckPath(%s, %s) = %v want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// newSelfCheckTestServer wires a real gateway Server + Router with one
// self-registered provider (token pre-issued) and returns the raw token
// alongside the HTTP handler, for exercising the self-check endpoints
// end-to-end through server.Handler() (proving the session-middleware bypass
// too, via a non-loopback Host).
func newSelfCheckTestServer(t *testing.T, provider domain.Provider) (http.Handler, string, *Router) {
	t.Helper()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := router.SetProviderSelfRegistrationToken(provider.ID, domain.ProviderSelfRegistration{TokenHash: hash, TokenPreview: preview, CreatedAt: nowRFC3339()}); err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}
	return server.Handler(), raw, router
}

func selfCheckRequest(handler http.Handler, path, token string, body map[string]any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Host = "gateway.example.com" // non-loopback: proves the session-auth bypass
	req.RemoteAddr = "203.0.113.5:443"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestProviderSelfCheckHealthAuthAndConnectivity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-shared-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5"}]}`))
	}))
	defer upstream.Close()

	handler, token, _ := newSelfCheckTestServer(t, domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL + "/v1/chat/completions", APIKeySource: "literal:test-shared-secret",
	})

	// 缺 token -> 401
	if rec := selfCheckRequest(handler, "/__providers/p1/self-check/health", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 错误 token -> 401
	if rec := selfCheckRequest(handler, "/__providers/p1/self-check/health", "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 正确 token，真实连通性检查 -> 200 + success true
	rec := selfCheckRequest(handler, "/__providers/p1/self-check/health", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Status  int  `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success || resp.Status != 200 {
		t.Fatalf("expected success, got %+v body=%s", resp, rec.Body.String())
	}
}

func TestProviderSelfCheckHealthReportsUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	handler, token, _ := newSelfCheckTestServer(t, domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL + "/v1/chat/completions", APIKeySource: "literal:wrong-secret",
	})
	rec := selfCheckRequest(handler, "/__providers/p1/self-check/health", token, nil)
	if rec.Code != http.StatusOK { // 探测本身成功送达 handler；失败体现在 success=false
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Success {
		t.Fatalf("expected success=false for a 401 upstream, got %s", rec.Body.String())
	}
}

// TestProviderSelfCheckChatAllThreeProtocols proves the chat self-check (and
// the underlying testProviderChat generalization) works for all three
// supported protocols on plain api_key providers, always using the fixed
// "2+2等于几" prompt regardless of request body, and that a caller-supplied
// prompt is ignored.
func TestProviderSelfCheckChatAllThreeProtocols(t *testing.T) {
	cases := []struct {
		name     string
		protocol domain.Protocol
		path     string
		checkReq func(t *testing.T, body []byte)
		respBody string
	}{
		{
			name: "openai_chat", protocol: domain.ProtocolOpenAIChat, path: "/v1/chat/completions",
			checkReq: func(t *testing.T, body []byte) {
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				messages, _ := payload["messages"].([]any)
				if len(messages) == 0 {
					t.Fatalf("expected messages array, got %s", body)
				}
				last := messages[len(messages)-1].(map[string]any)
				if last["content"] != selfCheckChatPrompt {
					t.Fatalf("expected fixed prompt %q, got %v", selfCheckChatPrompt, last["content"])
				}
			},
			respBody: `{"choices":[{"message":{"role":"assistant","content":"4"}}]}`,
		},
		{
			name: "claude", protocol: domain.ProtocolClaude, path: "/v1/messages",
			checkReq: func(t *testing.T, body []byte) {
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				messages, _ := payload["messages"].([]any)
				if len(messages) == 0 {
					t.Fatalf("expected messages array, got %s", body)
				}
				last := messages[0].(map[string]any)
				if last["content"] != selfCheckChatPrompt {
					t.Fatalf("expected fixed prompt %q, got %v", selfCheckChatPrompt, last["content"])
				}
			},
			respBody: `{"content":[{"type":"text","text":"4"}],"stop_reason":"end_turn"}`,
		},
		{
			name: "openai_responses", protocol: domain.ProtocolOpenAIResponses, path: "/v1/responses",
			checkReq: func(t *testing.T, body []byte) {
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				input, _ := payload["input"].([]any)
				if len(input) == 0 {
					t.Fatalf("expected input array, got %s", body)
				}
				last := input[len(input)-1].(map[string]any)
				if last["content"] != selfCheckChatPrompt {
					t.Fatalf("expected fixed prompt %q, got %v", selfCheckChatPrompt, last["content"])
				}
			},
			respBody: `{"output":[{"type":"message","content":[{"type":"output_text","text":"4"}]}],"status":"completed"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var capturedBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(body)
				capturedBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.respBody))
			}))
			defer upstream.Close()

			handler, token, _ := newSelfCheckTestServer(t, domain.Provider{
				ID: "p1", Name: "P1", Protocol: tc.protocol,
				BaseURL: upstream.URL + tc.path, APIKeySource: "literal:test-secret",
			})

			// 客户端故意传一个不同的 prompt，验证服务端强制用固定内容，不被覆盖。
			rec := selfCheckRequest(handler, "/__providers/p1/self-check/chat", token, map[string]any{"userPrompt": "帮我写诗"})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Success bool `json:"success"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !resp.Success {
				t.Fatalf("expected success, got %s", rec.Body.String())
			}
			if capturedBody == nil {
				t.Fatalf("upstream never received a request")
			}
			tc.checkReq(t, capturedBody)
		})
	}
}
