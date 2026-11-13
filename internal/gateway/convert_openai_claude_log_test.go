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

// Chat→Claude 成功的非流式回包必须随返回值交回调用方：记录 2xx 正文时落库的
// 就是这里返回的字节，返回 nil 会让日志里永远显示「未落盘」。
func TestOpenAIChatToClaudeProxyReturnsNonStreamBodyForLog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"gpt-5",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	provider := openAIChatLogProvider(upstream.URL)
	server := NewServer(NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}}), monitor.NewStore())
	claudeReq := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 64,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	status, _, body, err := server.proxyOpenAIToClaudeMessages(recorder, request, provider, "gpt-5", claudeReq, false)
	if err != nil {
		t.Fatalf("proxyOpenAIToClaudeMessages: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s)", status, recorder.Body.String())
	}
	if len(body) == 0 {
		t.Fatal("proxy returned no response body for a successful turn")
	}
	if string(body) != recorder.Body.String() {
		t.Fatalf("logged body differs from what the client got:\nlogged: %s\nclient: %s", body, recorder.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("body is not the Claude answer: %v (%s)", err, body)
	}
	if !strings.Contains(string(body), `"hello"`) {
		t.Fatalf("body lost the answer text: %s", body)
	}
}

// 流式同理：转换后的 SSE 边写边透传，日志副本取自同一批字节。
func TestOpenAIChatToClaudeProxyReturnsStreamBodyForLog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Answer."},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	provider := openAIChatLogProvider(upstream.URL)
	server := NewServer(NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}}), monitor.NewStore())
	claudeReq := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 64,
		"stream":     true,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	status, _, body, err := server.proxyOpenAIToClaudeMessages(recorder, request, provider, "gpt-5", claudeReq, false)
	if err != nil {
		t.Fatalf("proxyOpenAIToClaudeMessages: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s)", status, recorder.Body.String())
	}
	if len(body) == 0 {
		t.Fatal("proxy returned no response body for a successful stream")
	}
	if !strings.Contains(string(body), "Answer.") || !strings.Contains(string(body), "message_stop") {
		t.Fatalf("logged stream is not the converted Claude stream: %s", body)
	}
}

// 端到端：Claude 客户端打到 Chat 上游，开着「记录 2xx 成功正文」时，落库的
// 日志里必须真的有转换后的 Claude 回包 —— 这正是截图里显示「未落盘」的那条路径。
func TestClaudeEndpointLogsChatProviderResponseBody(t *testing.T) {
	cases := []struct {
		name    string
		stream  bool
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "non-stream", stream: false, want: "hello",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"gpt-5",` +
					`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
					`"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
			},
		},
		{
			name: "stream", stream: true, want: "message_stop",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Answer."},"finish_reason":null}]}`+"\n\n")
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(tc.handler)
			t.Cleanup(upstream.Close)

			provider := openAIChatLogProvider(upstream.URL)
			router := NewRouter(domain.GatewayState{
				Providers: []domain.Provider{provider},
				Routes: []domain.Route{{
					ID: "r1", Name: "R1", ProviderID: provider.ID,
					OutputProtocol: domain.ProtocolClaude, Enabled: true,
				}},
				APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "sk-gw", RouteID: "r1", Enabled: true, StreamEnabled: true}},
			})
			server := NewServer(router, monitor.NewStore())
			server.SetLog2xxBodies(true)

			body, err := json.Marshal(map[string]any{
				"model": "claude-sonnet-4-5", "max_tokens": 32, "stream": tc.stream,
				"messages": []map[string]any{{"role": "user", "content": "hi"}},
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(body)))
			request.Header.Set("x-api-key", "sk-gw")
			request.Header.Set("anthropic-version", "2023-06-01")
			recorder := httptest.NewRecorder()
			server.handleClaudeMessages(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
			}
			server.FlushRequestLogs()

			logs := server.logs.List(1)
			if len(logs) != 1 {
				t.Fatalf("expected 1 log entry, got %d", len(logs))
			}
			if logs[0].ResponseBody == "" {
				t.Fatalf("2xx response body was not logged (client got %d bytes)", recorder.Body.Len())
			}
			if !strings.Contains(logs[0].ResponseBody, tc.want) {
				t.Fatalf("logged body is not the converted answer: %s", logs[0].ResponseBody)
			}
		})
	}
}

func openAIChatLogProvider(baseURL string) domain.Provider {
	return domain.Provider{
		ID:           "p_log",
		Name:         "log",
		Protocol:     domain.ProtocolOpenAIChat,
		BaseURL:      baseURL + "/v1",
		AuthType:     "api_key",
		APIKeySource: "literal:test-secret",
	}
}
