package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// routeGateUpstream is an upstream that counts what reaches it. Reaching it at
// all is what "the gateway spent somebody's account" means here.
func routeGateUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"up-1","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)
	return upstream, &calls
}

func routeGateState(upstreamURL string) domain.GatewayState {
	return domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude,
			BaseURL: upstreamURL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "sk-upstream-secret",
		}},
		Models: []domain.Model{{ID: "claude-x", ProviderID: "p1", InMenu: true}},
		Routes: []domain.Route{
			{ID: "r1", Name: "R1", ProviderID: "p1", OutputProtocol: domain.ProtocolClaude, Enabled: true},
			{ID: "r2", Name: "R2", ProviderID: "p1", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
		},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "console", Key: "sk-console-key-1", RouteID: "r1", Enabled: true, StreamEnabled: true}},
	}
}

// 控制台「对话测试」(testRoute) 让协议入口跳过 API Key 校验，靠的是
// X-Gateway-Route-Id + X-Gateway-Internal-Test 两个头。这两个头是客户端随便写的，
// 而控制台接口（Claude / Gemini / Chat 的转换入口）本来对不带 key 的请求一律 401：
// 任何人只要知道一条路由 id（/__state 里就有），带上这两个头就能跳过校验、直接
// 用 Provider 自己的上游凭据发请求，既不记 key 额度也不归因到任何 key。
// 现在这个捷径只认本进程签发的短时 HMAC 令牌；普通请求头无法伪造。
func TestClientCannotClaimTheRouteTestShortcut(t *testing.T) {
	upstream, upstreamCalls := routeGateUpstream(t)
	server := NewServer(NewRouter(routeGateState(upstream.URL)), monitor.NewStore())
	server.SetAdminAuthStore(newMemoryAdminAuthStore())
	handler := server.Handler()

	for _, target := range []string{"/anthropic/v1/messages", "/v1beta/models/claude-x:generateContent"} {
		t.Run(target, func(t *testing.T) {
			before := upstreamCalls.Load()
			body := `{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			if strings.HasPrefix(target, "/v1beta/") {
				body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
			}
			request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
			request.Host = "gateway.example.com"
			request.RemoteAddr = "203.0.113.70:443"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(headerGatewayRouteID, "r1")
			request.Header.Set(headerGatewayInternalTest, "1")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if got := upstreamCalls.Load(); got != before {
				t.Fatalf("a client with no API key reached the provider %d time(s): status=%d body=%s", got-before, recorder.Code, recorder.Body.String())
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// 反过来：带的 key 照常工作（上面的 401 不是因为把整条链路关了）。
func TestAnAPIKeyIsAccepted(t *testing.T) {
	upstream, _ := routeGateUpstream(t)
	server := NewServer(NewRouter(routeGateState(upstream.URL)), monitor.NewStore())
	server.SetAdminAuthStore(newMemoryAdminAuthStore())
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	request.Host = "gateway.example.com"
	request.RemoteAddr = "203.0.113.70:443"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", "sk-console-key-1")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	// 带 key 的请求必须走通。这个 fixture 的 provider 讲 Claude，请求也走 Claude
	// 端点——按「这次请求的协议」判定，两端一致就是直通。
	//
	// 这里曾经断言的是 502「protocol conversion is not implemented」：那时判定看的是
	// route 上的 OutputProtocol（fixture 里是 openai_chat），Claude→Claude 被当成需要
	// 转换、又找不到转换器。那是 bug，不是契约。
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a keyed Claude request (body %s)", recorder.Code, recorder.Body.String())
	}
}

// internalTestRouteID is the only door into the route-test shortcut. The route
// id must be paired with a fresh signature made by this process: a client can
// copy both headers, but it cannot mint the HMAC.
func TestInternalTestRouteIDRequiresSignedServerToken(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		routeID   string
		token     string
		wantOK    bool
		wantRoute string
	}{
		{name: "route header only", routeID: "r1", token: "", wantOK: false},
		{name: "unsigned legacy value", routeID: "r1", token: "1", wantOK: false},
		{name: "token for another route", routeID: "r1", token: routeTestToken("r2", now), wantOK: false},
		{name: "expired token", routeID: "r1", token: routeTestToken("r1", now.Add(-routeTestTokenTTL-time.Minute)), wantOK: false},
		{name: "fresh signed token", routeID: "r1", token: routeTestToken("r1", now), wantOK: true, wantRoute: "r1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			if tc.routeID != "" {
				request.Header.Set(headerGatewayRouteID, tc.routeID)
			}
			if tc.token != "" {
				request.Header.Set(headerGatewayInternalTest, tc.token)
			}
			routeID, ok := internalTestRouteID(request)
			if ok != tc.wantOK || routeID != tc.wantRoute {
				t.Fatalf("internalTestRouteID = (%q, %v), want (%q, %v)", routeID, ok, tc.wantRoute, tc.wantOK)
			}
		})
	}
}

// X-Gateway-Internal-Test 曾经兼着一个「跳过协议不符检查」的开关。那个检查本身
// 已经拆掉了：一把 Key 本来就该同时被 Claude Code 和 Codex 用，每条端点自带协议，
// 按「这次请求走的是哪条端点」判定直通还是转换，而不是拿 route 上那个
// 输出协议去拒绝另一条端点。
//
// 这条测试盯的是新口径：同一个 Key 绑在 chat 输出的路由上，发 Claude 端点的请求
// 也必须被正常服务，而不需要任何特殊请求头。
func TestOneKeyServesEveryProtocolEndpoint(t *testing.T) {
	upstream, _ := routeGateUpstream(t)
	state := routeGateState(upstream.URL)
	// k1 绑到 chat 输出的 r2：按旧口径，往 Claude 端点发就是「协议不符」。
	state.APIKeys[0].RouteID = "r2"
	server := NewServer(NewRouter(state), monitor.NewStore())
	server.SetAdminAuthStore(newMemoryAdminAuthStore())
	body := `{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

	// 不带任何特殊头：Claude 端点也必须能用。
	plain := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	plain.Host = "gateway.example.com"
	plain.RemoteAddr = "203.0.113.70:443"
	plain.Header.Set("Content-Type", "application/json")
	plain.Header.Set("x-api-key", "sk-console-key-1")
	plainRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("同一把 Key 应当能走 Claude 端点，status = %d (body %s)", plainRecorder.Code, plainRecorder.Body.String())
	}

	// 伪造的内部测试头不能改变任何东西（它早已不参与协议判定）。
	forged := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	forged.Host = "gateway.example.com"
	forged.RemoteAddr = "203.0.113.70:443"
	forged.Header.Set("Content-Type", "application/json")
	forged.Header.Set("x-api-key", "sk-console-key-1")
	forged.Header.Set(headerGatewayInternalTest, "1")
	forgedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(forgedRecorder, forged)
	if forgedRecorder.Code != http.StatusOK {
		t.Fatalf("带上内部测试头结果应当一样，status = %d (body %s)", forgedRecorder.Code, forgedRecorder.Body.String())
	}
}

// 普通用户的 Key 只能用管理员当前授权的 Provider。这条规则由
// executeProtocolFlowWithFailover 每次请求复核，而 count_tokens 不走那条路：
// 撤销授权后，旧 Key 仍然能拿 token 计数，花的是上游账号的额度。
func TestCountTokensRefusesRevokedProviderForNormalUserKey(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{{ID: "revoked-p", Name: "revoked-p", Protocol: domain.ProtocolClaude,
			BaseURL: upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "sk-a"}},
		Routes: []domain.Route{{ID: "revoked-route", ProviderID: "revoked-p", OutputProtocol: domain.ProtocolClaude, Enabled: true}},
		APIKeys: []domain.APIKey{{ID: "user-key", Name: "user-key", Key: "sk-user-key-token", RouteID: "revoked-route",
			OwnerUserID: "user-1", Enabled: true}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	// 管理员把这个用户的 Provider 都撤了：用户已无任何授权。
	server.userStore = newMemoryUserStore(domain.User{ID: "user-1", Username: "alice", Role: domain.UserRoleUser, Enabled: true})

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "sk-user-key-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleClaudeCountTokens(rec, req)

	if hits.Load() > 0 {
		t.Fatalf("count_tokens reached upstream %d time(s) after the owner's grant was revoked (status %d)", hits.Load(), rec.Code)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 after the grant was revoked (body %s)", rec.Code, rec.Body.String())
	}
}
