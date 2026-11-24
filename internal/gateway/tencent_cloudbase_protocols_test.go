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

// 直连 CloudBase aiProxy 时，唯一真实的上游协议是 OpenAI Chat。Provider 必须
// 声明 openai_chat，三种客户端协议再由 Gateway 转换；否则 Responses 请求会把
// input 原样发给云函数，立刻得到 "messages is required"。
func TestTencentCloudBaseProxyProviderServesThreeGatewayProtocols(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		upstreamBodies = append(upstreamBodies, payload)
		if _, ok := payload["messages"].([]any); !ok {
			t.Fatalf("aiProxy must receive Chat messages, got %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"hy3",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()

	tests := []struct {
		name     string
		protocol domain.Protocol
		path     string
		body     string
		authName string
		auth     string
		handle   func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name:     "openai chat",
			protocol: domain.ProtocolOpenAIChat,
			path:     "/v1/chat/completions",
			body:     `{"model":"hy3","stream":false,"messages":[{"role":"user","content":"只回复 OK"}]}`,
			authName: "Authorization",
			auth:     "Bearer sk-gw",
			handle:   func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleOpenAIChat(w, r) },
		},
		{
			name:     "openai responses",
			protocol: domain.ProtocolOpenAIResponses,
			path:     "/openai/v1/responses",
			body:     `{"model":"hy3","stream":false,"input":"只回复 OK"}`,
			authName: "Authorization",
			auth:     "Bearer sk-gw",
			handle:   func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleOpenAIResponses(w, r) },
		},
		{
			name:     "claude",
			protocol: domain.ProtocolClaude,
			path:     "/anthropic/v1/messages",
			body:     `{"model":"hy3","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"只回复 OK"}]}`,
			authName: "x-api-key",
			auth:     "sk-gw",
			handle:   func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleClaudeMessages(w, r) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := domain.Provider{
				ID:           "cloudbase-proxy",
				Name:         "CloudBase proxy",
				Protocol:     domain.ProtocolOpenAIChat,
				BaseURL:      upstream.URL + "/v1/functions/aiProxy",
				AuthType:     domain.AuthTypeAPIKey,
				AuthHeader:   "Authorization",
				APIKeySource: "literal:jwt-test",
				DefaultModel: "hy3",
				Models:       []domain.Model{{ID: "hy3", ProviderID: "cloudbase-proxy", Protocol: domain.ProtocolOpenAIChat}},
			}
			router := NewRouter(domain.GatewayState{
				Providers: []domain.Provider{provider},
				Routes: []domain.Route{{
					ID: "r1", Name: "R1", ProviderID: provider.ID, OutputProtocol: tc.protocol, Enabled: true,
				}},
				APIKeys: []domain.APIKey{{
					ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true,
				}},
			})
			server := NewServer(router, monitor.NewStore())
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(tc.authName, tc.auth)
			if tc.protocol == domain.ProtocolClaude {
				request.Header.Set("anthropic-version", "2023-06-01")
			}
			recorder := httptest.NewRecorder()
			tc.handle(server, recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "OK") {
				t.Fatalf("response lost model text: %s", recorder.Body.String())
			}
		})
	}
	if len(upstreamBodies) != len(tests) {
		t.Fatalf("upstream calls = %d, want %d", len(upstreamBodies), len(tests))
	}
}
