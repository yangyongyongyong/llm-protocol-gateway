package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// newPeerTestServer 起一个能接受对端注册与透传的最小网关。
// peerID 固定，方便断言"链路里出现自己就是环"。
func newPeerTestServer(t *testing.T, providers ...domain.Provider) *Server {
	t.Helper()
	server := NewServer(NewRouter(domain.GatewayState{Providers: providers}), monitor.NewStore())
	server.SetGatewayID(gwID)
	server.userStore = newMemoryUserStore()
	// 生产里同一个 *store.Store 同时实现 UserStore 与 RoleStore；测试里也要
	// 两边都挂上，否则授权读写会落到两个互不相干的 map 上。
	server.roles = newMemoryRoleStore()
	return server
}

// peerRegister 走完整 HTTP 路径注册一个下游，返回响应。
func peerRegister(t *testing.T, server *Server, payload map[string]any) (*httptest.ResponseRecorder, peerRegisterResponse) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	// 默认来源：本机，落在"只允许私网"的默认规则内。
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	var decoded peerRegisterResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode register response: %v (%s)", err, recorder.Body.String())
		}
	}
	return recorder, decoded
}

func peerRegisterPayload(overrides map[string]any) map[string]any {
	payload := map[string]any{
		"gatewayId": otherID,
		"name":      "国内网关",
		"username":  "sub-gw",
		"password":  "a-strong-password",
	}
	for key, value := range overrides {
		payload[key] = value
	}
	return payload
}

// 注册成功：返回一次性凭据，并把下游建成 users 表里一行 kind=peer 的账号。
func TestPeerRegisterCreatesPeerAccount(t *testing.T) {
	server := newPeerTestServer(t)
	recorder, response := peerRegister(t, server, peerRegisterPayload(nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(response.Token, peerTokenPrefix) {
		t.Fatalf("token %q must carry the peer prefix", response.Token)
	}
	if response.GatewayID != otherID {
		t.Fatalf("gatewayId = %q", response.GatewayID)
	}
	user, err := server.userStore.UserByID(response.UserID)
	if err != nil {
		t.Fatalf("peer account not created: %v", err)
	}
	if !user.IsPeer() || user.GatewayID != otherID {
		t.Fatalf("account is not a peer: %+v", user)
	}
	if !user.Enabled {
		t.Fatal("a freshly registered peer must be enabled")
	}
	// 凭据要真的能用。
	me := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	me.Header.Set("Authorization", "Bearer "+response.Token)
	meRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(meRecorder, me)
	if meRecorder.Code != http.StatusOK {
		t.Fatalf("the issued token must authenticate, got %d: %s", meRecorder.Code, meRecorder.Body.String())
	}
}

// 凭据在这里是「可以拿去用」的真值，所以它必须能被对端凭据鉴权原样认出来：
// 存的就是明文本身，比较时现算摘要（与 APIKeyByToken 同一口径）。这条测试
// 把双方的约定钉死——存的和比的必须是同一种形态。
func TestPeerTokenStoredVerbatimAndComparable(t *testing.T) {
	server := newPeerTestServer(t)
	_, response := peerRegister(t, server, peerRegisterPayload(nil))
	stored := ""
	for _, key := range server.router.State().APIKeys {
		if strings.HasPrefix(key.Key, peerTokenPrefix) {
			stored = key.Key
		}
	}
	if stored == "" {
		t.Fatal("the peer key was not persisted")
	}
	if stored != response.Token {
		t.Fatalf("stored credential %q must be the token handed to the peer", stored)
	}
	if peerTokenHash(stored) == stored {
		t.Fatal("the stored credential must not already be a digest")
	}
}

// 同一个网关重复注册是幂等的：账号不重复建，凭据换一把（旧的立刻失效）。
func TestPeerReRegisterRotatesToken(t *testing.T) {
	server := newPeerTestServer(t)
	_, first := peerRegister(t, server, peerRegisterPayload(nil))
	recorder, second := peerRegister(t, server, peerRegisterPayload(nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("re-register status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if second.UserID != first.UserID {
		t.Fatalf("re-register created a second account: %q vs %q", second.UserID, first.UserID)
	}
	if second.Token == first.Token {
		t.Fatal("re-register must rotate the credential")
	}
	// 旧凭据必须已经失效。
	old := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	old.Header.Set("Authorization", "Bearer "+first.Token)
	oldRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(oldRecorder, old)
	if oldRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("the previous token must stop working, got %d", oldRecorder.Code)
	}
	// 新凭据可用。
	fresh := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	fresh.Header.Set("Authorization", "Bearer "+second.Token)
	freshRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(freshRecorder, fresh)
	if freshRecorder.Code != http.StatusOK {
		t.Fatalf("the new token must work, got %d", freshRecorder.Code)
	}
}

// 密码不对就不许顶替别人的注册。网关 ID 是公开信息（会显示在界面上），
// 只凭它换走凭据等于让任何人接管一个下游。
func TestPeerReRegisterRejectsWrongPassword(t *testing.T) {
	server := newPeerTestServer(t)
	_, first := peerRegister(t, server, peerRegisterPayload(nil))
	recorder, _ := peerRegister(t, server, peerRegisterPayload(map[string]any{"password": "another-password"}))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 原凭据必须仍然有效：拒绝的是冒名者，不能顺手把真正的下游踢下线。
	me := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	me.Header.Set("Authorization", "Bearer "+first.Token)
	meRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(meRecorder, me)
	if meRecorder.Code != http.StatusOK {
		t.Fatalf("the original token must keep working, got %d", meRecorder.Code)
	}
}

// 用户名被本地账号占用时不能注册：否则对端会混进一个真人账号。
func TestPeerRegisterRejectsUsernameTakenByLocalUser(t *testing.T) {
	server := newPeerTestServer(t)
	server.userStore = newMemoryUserStore(domain.User{
		ID: "shouze", Username: "sub-gw", Role: domain.UserRoleUser, RoleID: domain.RoleIDUser, Enabled: true,
	})
	recorder, _ := peerRegister(t, server, peerRegisterPayload(nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// 公网来源默认可以注册：下游从公网直接连上来是正常用法，来源不是安全边界，
// 用户名+密码才是。
func TestPeerRegisterAcceptsPublicSourceByDefault(t *testing.T) {
	server := newPeerTestServer(t)
	body, _ := json.Marshal(peerRegisterPayload(nil))
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.9:4444"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// 配了白名单之后它就是权威的：名单外的公网来源仍然被拒。
func TestPeerRegisterRefusedWhenWhitelistExcludesSource(t *testing.T) {
	server := newPeerTestServer(t)
	server.router.SetPeerRegistrationAllowCIDRs("10.0.0.0/8")
	body, _ := json.Marshal(peerRegisterPayload(nil))
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.9:4444"
	// 伪造转发头也没用：requestClientIP 对公网对端忽略这些头。
	request.Header.Set("X-Forwarded-For", "10.1.2.3")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// 白名单里的来源照常放行。
	inside := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
	inside.Header.Set("Content-Type", "application/json")
	inside.RemoteAddr = "10.1.2.3:5555"
	insideRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(insideRecorder, inside)
	if insideRecorder.Code != http.StatusOK {
		t.Fatalf("whitelisted source status=%d body=%s", insideRecorder.Code, insideRecorder.Body.String())
	}
}

// 公网放行之后，注册入口不能再是可以无限次猜密码的地方：同一来源连续失败
// 到阈值就 429。
func TestPeerRegisterThrottlesRepeatedCredentialFailures(t *testing.T) {
	server := newPeerTestServer(t)
	_, first := peerRegister(t, server, peerRegisterPayload(nil))
	if first.Token == "" {
		t.Fatal("the first registration must succeed")
	}
	body, _ := json.Marshal(peerRegisterPayload(map[string]any{"password": "wrong-password"}))
	var lastCode int
	for attempt := 0; attempt <= authMaxFailures+2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "127.0.0.1:40000"
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		lastCode = recorder.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("after repeated failures status=%d, want 429", lastCode)
	}
	// 正确的凭据这时同样被挡：限流是按来源整段生效，不给攻击者留一条快路。
	body, _ = json.Marshal(peerRegisterPayload(nil))
	request := httptest.NewRequest(http.MethodPost, peerEndpointPrefix+"register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled source with correct credentials = %d, want 429", recorder.Code)
	}
}

// 网关 ID 形状不对要早拒：说明对面根本没实现这一侧。
func TestPeerRegisterValidatesGatewayID(t *testing.T) {
	server := newPeerTestServer(t)
	for _, bad := range []string{"", "gw_", "not-a-gateway", "gw_zzzzzzzzzzzzzzzz", "gw_0123456789abcde"} {
		recorder, _ := peerRegister(t, server, peerRegisterPayload(map[string]any{"gatewayId": bad}))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("gatewayId %q: status=%d body=%s", bad, recorder.Code, recorder.Body.String())
		}
	}
}

// 对端入口只认对端凭据：控制台的会话 cookie、普通 API 密钥都不能进来。
func TestPeerEndpointsRejectConsoleAndNormalKeys(t *testing.T) {
	server := newPeerTestServer(t)
	// 一个普通（非 peer）密钥。
	if _, err := server.router.AddAPIKey(domain.APIKey{ID: "local-key", Name: "local", Key: "sk-gw-ordinary"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		header string
	}{
		{"no credential", ""},
		{"ordinary api key", "Bearer sk-gw-ordinary"},
		{"peer prefix but unknown", "Bearer " + peerTokenPrefix + "deadbeef"},
		{"malformed", "Bearer not-a-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			request.RemoteAddr = "127.0.0.1:40000"
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// 禁用对端账号 = 停它的流量。这是「用户管理页里禁用一个人」的同一套机制，
// 也正是把它做成 users 表的收益：不需要另写一条禁用路径。
func TestDisabledPeerLosesAccess(t *testing.T) {
	server := newPeerTestServer(t)
	_, response := peerRegister(t, server, peerRegisterPayload(nil))
	user, err := server.userStore.UserByID(response.UserID)
	if err != nil {
		t.Fatal(err)
	}
	user.Enabled = false
	if err := server.userStore.UpdateUser(user); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	request.Header.Set("Authorization", "Bearer "+response.Token)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled peer must be refused, got %d", recorder.Code)
	}
}

// 吊销后凭据立刻失效，但用户行留着（授权还在，重新注册能恢复）。
func TestRevokedPeerTokenStopsWorking(t *testing.T) {
	server := newPeerTestServer(t)
	_, response := peerRegister(t, server, peerRegisterPayload(nil))
	if err := server.router.RemovePeerKeys(response.UserID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"me", nil)
	request.Header.Set("Authorization", "Bearer "+response.Token)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked credential must stop working, got %d", recorder.Code)
	}
	if _, err := server.userStore.UserByID(response.UserID); err != nil {
		t.Fatalf("the account must survive revocation: %v", err)
	}
}

// 对端能看到哪些 provider，完全由上游的授权集决定，和它请求的清单无关。
func TestPeerProvidersFollowGrants(t *testing.T) {
	server := newPeerTestServer(t,
		domain.Provider{ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://hj.example.com"},
		domain.Provider{ID: "other-claude", Name: "other-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://other.example.com"},
	)
	_, response := peerRegister(t, server, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{"hj-claude", "不存在的"},
	}))
	providers := peerProviders(t, server, response.Token)
	if len(providers) != 1 || providers[0].ID != "hj-claude" {
		t.Fatalf("providers = %+v, want only the granted one", providers)
	}
	// 管理员追加授权后立刻可见：没有第二套权限判断。
	if err := server.roles.SetResourceGrants(response.UserID, []string{"hj-claude", "other-claude"}); err != nil {
		t.Fatal(err)
	}
	providers = peerProviders(t, server, response.Token)
	if len(providers) != 2 {
		t.Fatalf("a new grant must be visible immediately, got %+v", providers)
	}
}

func peerProviders(t *testing.T, server *Server, token string) []peerProviderView {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, peerEndpointPrefix+"providers", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Providers []peerProviderView `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	return payload.Providers
}
