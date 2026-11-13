package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 一台声明 openai_chat、但实测也支持 claude 的上游：客户端发 Claude 请求时，
// 调度应先用 Claude 直通；直通失败且还没写客户端，再退回 OpenAI Chat 转换。
func TestNativeProtocolPassThroughFallsBackToConversion(t *testing.T) {
	var claudeHits, chatHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/messages":
			atomic.AddInt32(&claudeHits, 1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"not found"}}`))
		case "/chat/completions":
			atomic.AddInt32(&chatHits, 1)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "multi", Name: "multi", Protocol: domain.ProtocolOpenAIChat,
			ProtocolSupport: []string{"openai_chat", "claude"},
			BaseURL:         upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "static",
			Models: []domain.Model{{ID: "m", ProviderID: "multi", Protocol: domain.ProtocolOpenAIChat}},
		}},
		Routes: []domain.Route{{
			ID: "multi-route", Name: "multi-route", ProviderID: "multi",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, _ := server.router.RouteByID("multi-route")
	key := domain.APIKey{ID: "k", RouteID: route.ID}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	rec := httptest.NewRecorder()
	status, _, body, decision, _, err := server.executeProtocolFlowWithFailover(
		rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, key, true,
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v body=%s", status, err, body)
	}
	if atomic.LoadInt32(&claudeHits) != 1 || atomic.LoadInt32(&chatHits) != 1 {
		t.Fatalf("hits: claude=%d chat=%d, want one native try then one conversion", claudeHits, chatHits)
	}
	if decision.Action != "convert" {
		t.Fatalf("final action = %q, want convert after fallback", decision.Action)
	}
	found := false
	for _, entry := range server.logs.ListApp(50) {
		if entry.Message == "native protocol pass-through failed, retrying conversion" {
			found = true
			if !strings.Contains(entry.Context, "provider=multi") {
				t.Fatalf("log context = %q", entry.Context)
			}
		}
	}
	if !found {
		t.Fatal("缺少直通失败退回转换的日志")
	}
}

// 直通成功时不要再走转换：上游只应被 /messages 打一次，/chat/completions 为零。
func TestNativeProtocolPassThroughSucceedsWithoutConversion(t *testing.T) {
	var claudeHits, chatHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/messages":
			atomic.AddInt32(&claudeHits, 1)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		case "/chat/completions":
			atomic.AddInt32(&chatHits, 1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "multi", Name: "multi", Protocol: domain.ProtocolOpenAIChat,
			ProtocolSupport: []string{"openai_chat", "claude"},
			BaseURL:         upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "static",
			Models: []domain.Model{{ID: "m", ProviderID: "multi", Protocol: domain.ProtocolOpenAIChat}},
		}},
		Routes: []domain.Route{{
			ID: "multi-route", Name: "multi-route", ProviderID: "multi",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, _ := server.router.RouteByID("multi-route")
	key := domain.APIKey{ID: "k", RouteID: route.ID}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	rec := httptest.NewRecorder()
	status, _, body, decision, _, err := server.executeProtocolFlowWithFailover(
		rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, key, true,
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v body=%s", status, err, body)
	}
	if atomic.LoadInt32(&claudeHits) != 1 || atomic.LoadInt32(&chatHits) != 0 {
		t.Fatalf("hits: claude=%d chat=%d, want native only", claudeHits, chatHits)
	}
	if decision.Action != "pass_through" {
		t.Fatalf("action = %q, want pass_through", decision.Action)
	}
}

func TestNativeProtocolForProvider(t *testing.T) {
	provider := domain.Provider{
		Protocol:        domain.ProtocolOpenAIChat,
		ProtocolSupport: []string{"openai_chat", "claude"},
		AuthType:        domain.AuthTypeAPIKey,
	}
	if protocol, ok := nativeProtocolForProvider(provider, domain.ProtocolClaude); !ok || protocol != domain.ProtocolClaude {
		t.Fatalf("native = %q/%v, want claude", protocol, ok)
	}
	if _, ok := nativeProtocolForProvider(provider, domain.ProtocolOpenAIChat); ok {
		t.Fatal("声明协议已一致时不该覆盖")
	}
	// 对端网关也要参与：上游把多协议能力透传下来后，下游应能按客户端协议
	// 选择信封里的协议。
	provider.AuthType = domain.AuthTypePeer
	if protocol, ok := nativeProtocolForProvider(provider, domain.ProtocolClaude); !ok || protocol != domain.ProtocolClaude {
		t.Fatalf("peer native = %q/%v, want claude", protocol, ok)
	}
	// 订阅类上游的「多协议」不是同一条 baseUrl 派生多条路径的形态，不覆盖。
	provider.AuthType = domain.AuthTypeClaudeOAuth
	if _, ok := nativeProtocolForProvider(provider, domain.ProtocolClaude); ok {
		t.Fatal("OAuth 订阅不参与协议覆盖")
	}
}
