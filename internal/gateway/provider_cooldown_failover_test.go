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

// TestFailoverHonoursUpstreamRetryAfter drives a real failover attempt against an
// upstream that answers 429 with Retry-After: the header must reach the cooldown,
// not just the body. Before this, every 429 rested the soft default minute.
func TestFailoverHonoursUpstreamRetryAfter(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`))
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "cooldown-probe", Name: "cooldown-probe", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static",
		}, {
			// 备选刻意与首选同协议：这条测试要钉的是「Retry-After 有没有进冷缩」，
			// 不是协议选择。两边协议相同也能避免测试把失败归因到协议转换。
			ID: "cooldown-backup", Name: "cooldown-backup", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static",
		}},
		Routes: []domain.Route{
			{
				ID: "cooldown-probe-route", Name: "cooldown-probe-route", ProviderID: "cooldown-probe",
				OutputProtocol: domain.ProtocolClaude, Enabled: true,
			},
			// A chain of two so the attempt can be deferred: without a backup the
			// gateway has nowhere to fail over and commits the error at once.
			{
				ID: "cooldown-backup-route", Name: "cooldown-backup-route", ProviderID: "cooldown-backup",
				OutputProtocol: domain.ProtocolClaude, Enabled: true,
			},
		},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, err := server.router.RouteByID("cooldown-probe-route")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	key := domain.APIKey{ID: "cooldown-key", RouteID: route.ID, FallbackProviderIDs: []string{"cooldown-backup"}}

	for i := 0; i < providerSoftFailureThreshold; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		rec := httptest.NewRecorder()
		status, _, _, _, _, execErr := server.executeProtocolFlowWithFailover(
			rec, req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
			"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
			domain.ProtocolClaude, true, key, true,
		)
		if status != http.StatusTooManyRequests {
			t.Fatalf("attempt %d status = %d err=%v, want 429", i+1, status, execErr)
		}
	}
	// 每次请求会打两次上游：首选 429 之后切到备选，备选也回 429（同一个
	// httptest 服务器）。以前备选会因为「跨协议缺锁定模型」被跳过，所以只有三次；
	// 那条跳过规则已经拆掉——模型会按备选自己的目录兜底，链路该真的走完。
	wantHits := int32(providerSoftFailureThreshold) * 2
	if atomic.LoadInt32(&hits) != wantHits {
		t.Fatalf("upstream hits = %d, want %d (each request tries both nodes)", hits, wantHits)
	}
	st, ok := server.providerUnavailableSnapshot("cooldown-probe")
	if !ok {
		t.Fatal("expected provider marked unavailable after repeated 429")
	}
	if left := time.Until(st.nextRetry); left < 4*time.Minute || left > 5*time.Minute {
		t.Fatalf("cooldown = %v left, want ~5m from the upstream Retry-After", left)
	}
}

// The 400 quota-exhausted shape is what OpenAI-compatible relays answer once a
// plan runs dry. It must take the provider out of rotation as a hard mark even
// with a backup chain in front of it (the live check that found this).
func TestFailoverHardMarkOnQuotaExhausted400(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"insufficient_quota","message":"You exceeded your current quota"}}`))
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "quota-probe", Name: "quota-probe", Protocol: domain.ProtocolOpenAIChat, BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static"},
			{ID: "quota-backup", Name: "quota-backup", Protocol: domain.ProtocolOpenAIChat, BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static"},
		},
		Routes: []domain.Route{
			{ID: "quota-probe-route", Name: "quota-probe-route", ProviderID: "quota-probe", OutputProtocol: domain.ProtocolClaude, Enabled: true},
			{ID: "quota-backup-route", Name: "quota-backup-route", ProviderID: "quota-backup", OutputProtocol: domain.ProtocolClaude, Enabled: true},
		},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, err := server.router.RouteByID("quota-probe-route")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	key := domain.APIKey{ID: "quota-key", RouteID: route.ID, FallbackProviderIDs: []string{"quota-backup"}}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	status, _, _, _, _, _ := server.executeProtocolFlowWithFailover(
		httptest.NewRecorder(), req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, key, true,
	)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (both accounts spent)", status)
	}
	for _, id := range []string{"quota-probe", "quota-backup"} {
		st, ok := server.providerUnavailableSnapshot(id)
		if !ok || !st.hard {
			t.Fatalf("%s: expected hard unavailable, got %#v ok=%v", id, st, ok)
		}
	}
}

// TestFailoverBusyDoesNotRestTheAccount pins the 2026-10-29 incident: Qoder
// answers 10605 ("the model is busy") for every account while the model is
// queued. Counted as a soft failure, three of them cooled a healthy account
// down, the recovery loop walked it back into the same queue, and the console
// showed accounts flipping between 备4 and 备8 with no traffic change. A busy
// model must advance the chain and rest nobody.
func TestFailoverBusyDoesNotRestTheAccount(t *testing.T) {
	queueBody := `{"code":"10605","message":"{\"isQueued\":false,\"modelKey\":\"m\",\"queueCount\":0,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"serviceAvailable\":false,\"subCode\":\"RESOURCE_LIMIT\",\"waitTime\":0}","subCode":"RESOURCE_LIMIT"}`
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(queueBody))
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "busy-probe", Name: "busy-probe", Protocol: domain.ProtocolOpenAIChat, BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static"},
			{ID: "busy-backup", Name: "busy-backup", Protocol: domain.ProtocolOpenAIChat, BaseURL: upstream.URL, AuthType: "api_key", APIKeySource: "static"},
		},
		Routes: []domain.Route{
			{ID: "busy-probe-route", Name: "busy-probe-route", ProviderID: "busy-probe", OutputProtocol: domain.ProtocolClaude, Enabled: true},
			{ID: "busy-backup-route", Name: "busy-backup-route", ProviderID: "busy-backup", OutputProtocol: domain.ProtocolClaude, Enabled: true},
		},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	route, err := server.router.RouteByID("busy-probe-route")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	key := domain.APIKey{ID: "busy-key", RouteID: route.ID, FallbackProviderIDs: []string{"busy-backup"}}
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	status, _, _, _, _, _ := server.executeProtocolFlowWithFailover(
		httptest.NewRecorder(), req, route, domain.RouteDecision{RouteID: route.ID, ProviderID: route.ProviderID},
		"m", map[string]any{"model": "m", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, key, true,
	)
	// Both accounts answered "busy", so the client still gets the refusal.
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	// The chain tried the backup too: a busy model is worth another account.
	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Fatalf("upstream hits = %d, want at least 2 (backup tried)", got)
	}
	for _, id := range []string{"busy-probe", "busy-backup"} {
		if st, ok := server.providerUnavailableSnapshot(id); ok {
			t.Fatalf("%s: busy must not rest the account, got %#v", id, st)
		}
	}
}
