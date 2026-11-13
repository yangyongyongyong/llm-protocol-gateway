package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 注册上游时自动建的路由只是一条 provider 绑定，不再持久化任何
// auto/pass_through/convert 模式。Provider 选择完全由 Key 的调度规则决定。
func TestEnsurePeerUpstreamRouteCreatesProviderBinding(t *testing.T) {
	server := newPeerTestServer(t, domain.Provider{
		ID: "peer-member", Name: "peer-member", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypePeer,
		BaseURL:  "https://parent.example.com",
	})

	if err := server.ensurePeerUpstreamRoute(server.router.State().Providers[0]); err != nil {
		t.Fatalf("ensurePeerUpstreamRoute: %v", err)
	}

	routes := server.router.State().Routes
	if len(routes) != 1 {
		t.Fatalf("应当只建一条路由，实际 %d 条", len(routes))
	}
	if routes[0].ProviderID != "peer-member" {
		t.Fatalf("路由指向的 provider = %q", routes[0].ProviderID)
	}
	if routes[0].OutputProtocol != domain.ProtocolOpenAIChat {
		t.Fatalf("路由的默认输出协议 = %q", routes[0].OutputProtocol)
	}
	if !routes[0].Enabled {
		t.Fatal("自动建的路由必须启用")
	}
}

// 当前 provider 不支持客户端协议时，才走转换。这里用只声明 Chat 的
// provider 验证 Responses -> Chat 的决策，不允许再通过 mode 强制直通。
func TestPeerChatOnlyRouteConvertsResponsesToChat(t *testing.T) {
	server := newPeerTestServer(t, domain.Provider{
		ID: "chat-only", Name: "chat-only", Protocol: domain.ProtocolOpenAIChat,
		AuthType:        domain.AuthTypePeer,
		BaseURL:         "https://parent.example.com",
		ProtocolSupport: []string{"openai_chat"},
		Models: []domain.Model{{
			ID: "m1", ProviderID: "chat-only", Protocol: domain.ProtocolOpenAIChat, InMenu: true,
		}},
	})
	server.router.AddRoute(domain.Route{
		ID: "r-auto", Name: "auto", ProviderID: "chat-only",
		OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true,
	})

	decision, err := server.router.DecideForProtocol("r-auto", domain.ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("DecideForProtocol: %v", err)
	}
	if decision.Action != "convert" {
		t.Fatalf("上游只认 Chat、客户端发 Responses 时必须转换，实际 action=%q", decision.Action)
	}
	if decision.ConversionLabel != "OpenAI Responses -> OpenAI Chat" {
		t.Fatalf("协议流 = %q", decision.ConversionLabel)
	}
}

// Provider 顺序只能由 Key 的调度决定。即使后面的 provider 与客户端协议相同，
// 也不能为了少一次转换把它提到前面的 provider 之前。
func TestProviderOrderDoesNotPreferNativeProtocolAcrossProviders(t *testing.T) {
	order := make(chan string, 3)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order <- "primary"
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"primary down"}}`))
	}))
	defer primary.Close()
	fallbackOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order <- "fallback-one"
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"fallback one down"}}`))
	}))
	defer fallbackOne.Close()
	fallbackTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order <- "fallback-two"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer fallbackTwo.Close()

	route := domain.Route{
		ID: "r-order", Name: "r-order", ProviderID: "p-primary",
		OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true,
	}
	key := domain.APIKey{
		ID: "k-order", Name: "k-order", Key: "sk-gw-order", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{"p-fallback-one", "p-fallback-two"},
	}
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-primary", Name: "primary", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey, BaseURL: primary.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy"},
			{ID: "p-fallback-one", Name: "fallback-one", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey, BaseURL: fallbackOne.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy"},
			{ID: "p-fallback-two", Name: "fallback-two", Protocol: domain.ProtocolOpenAIChat, AuthType: domain.AuthTypeAPIKey, BaseURL: fallbackTwo.URL + "/v1", APIKeySource: "sk-c", HealthStatus: "healthy"},
		},
		Routes:  []domain.Route{route},
		APIKeys: []domain.APIKey{key},
	}), monitor.NewStore())

	req := reqWithBody(t, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	rec := httptest.NewRecorder()
	status, _, _, _, _, err := server.executeProtocolFlowWithFailover(
		rec, req, route,
		domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolOpenAIChat, true, key, true,
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	want := []string{"primary", "fallback-one", "fallback-two"}
	got := make([]string, 0, len(want))
	for range want {
		got = append(got, <-order)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("尝试顺序 = %v, want %v", got, want)
		}
	}
}
