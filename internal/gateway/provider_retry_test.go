package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 同一个节点先原地重试，次数由 Key 配置。这正是「第一个明明还能用、偶尔出错
// 就切走」要解决的事：瞬时抖动换账号没用，只会白推一格。
func TestSameProviderRetriesStayOnTheNode(t *testing.T) {
	t.Parallel()
	var firstNodeHits, secondNodeHits int32
	// 第一个节点一直 503（可重试），第二个节点成功。
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstNodeHits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"try later"}}`))
	}))
	defer flaky.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondNodeHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer healthy.Close()

	route := domain.Route{ID: "r-retry", Name: "r-retry", ProviderID: "p-flaky", OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModeAuto, Enabled: true}
	key := domain.APIKey{
		ID: "k-retry", Name: "k-retry", Key: "sk-gw-retry", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
		FallbackProviderIDs: []string{"p-healthy"},
		SameProviderRetries: 2,
	}
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-flaky", Name: "flaky", Protocol: domain.ProtocolOpenAIChat, AuthType: "api_key", BaseURL: flaky.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy"},
			{ID: "p-healthy", Name: "healthy", Protocol: domain.ProtocolOpenAIChat, AuthType: "api_key", BaseURL: healthy.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy"},
		},
		Routes:  []domain.Route{route},
		APIKeys: []domain.APIKey{key},
	}), monitor.NewStore())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		nil)
	req = req.WithContext(req.Context())
	rec := httptest.NewRecorder()
	status, _, _, _, _, err := server.executeProtocolFlowWithFailover(
		rec, reqWithBody(t, req), route,
		domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolOpenAIChat, true, key, true,
	)
	_ = err
	if status != http.StatusOK {
		t.Fatalf("最终应当由健康的节点服务，status = %d", status)
	}
	// 首选被打 1 + 2 次重试 = 3 次，然后才轮到备选。
	if got := atomic.LoadInt32(&firstNodeHits); got != 3 {
		t.Fatalf("首选命中 %d 次，想要 3 次（首次 + 2 次重试）", got)
	}
	if got := atomic.LoadInt32(&secondNodeHits); got != 1 {
		t.Fatalf("备选命中 %d 次，想要 1 次", got)
	}
}

// 重试次数为 0（默认）时行为与升级前一致：一失败就换下一个节点。
func TestSameProviderRetriesZeroFailsOverImmediately(t *testing.T) {
	t.Parallel()
	var firstNodeHits int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstNodeHits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer flaky.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer healthy.Close()

	route := domain.Route{ID: "r-norety", Name: "r-norety", ProviderID: "p-a", OutputProtocol: domain.ProtocolOpenAIChat, Mode: domain.RouteModeAuto, Enabled: true}
	key := domain.APIKey{ID: "k-norety", Name: "k", Key: "sk-gw-norety", RouteID: route.ID, Enabled: true, StreamEnabled: true, FallbackProviderIDs: []string{"p-b"}}
	server := NewServer(NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p-a", Name: "a", Protocol: domain.ProtocolOpenAIChat, AuthType: "api_key", BaseURL: flaky.URL + "/v1", APIKeySource: "sk-a", HealthStatus: "healthy"},
			{ID: "p-b", Name: "b", Protocol: domain.ProtocolOpenAIChat, AuthType: "api_key", BaseURL: healthy.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy"},
		},
		Routes:  []domain.Route{route},
		APIKeys: []domain.APIKey{key},
	}), monitor.NewStore())

	req := reqWithBody(t, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	rec := httptest.NewRecorder()
	status, _, _, _, _, _ := server.executeProtocolFlowWithFailover(
		rec, req, route,
		domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolOpenAIChat, true, key, true,
	)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got := atomic.LoadInt32(&firstNodeHits); got != 1 {
		t.Fatalf("默认不重试，首选应当只被打 1 次，实际 %d", got)
	}
}

func reqWithBody(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	return r.WithContext(r.Context())
}

// 越界值收敛。
func TestClampSameProviderRetries(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{0, 0}, {-1, 0}, {1, 1}, {3, 3}, {99, maxSameProviderRetries},
	}
	for _, tc := range cases {
		if got := clampSameProviderRetries(tc.in); got != tc.want {
			t.Fatalf("clampSameProviderRetries(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	_ = time.Second
}
