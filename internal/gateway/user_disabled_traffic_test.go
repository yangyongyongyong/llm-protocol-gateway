package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// newDisabledUserServer 复刻事故现场的形态：用户 "shouze" 被授予 hj-claude，
// 名下有一把已经在用的 Key，然后管理员在「用户管理」里把这个账号禁用。
//
// 禁用之前，Enabled 只在两条控制台登录路径上被读（校验会话 cookie 与登录
// 密码），请求链路完全不看它——于是禁用之后这把 Key 还能一直打，管理员以为
// 已经把人拦住了，实际上一分钱没省。
func newDisabledUserServer(t *testing.T) *Server {
	t.Helper()
	state := domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "hj-claude", Name: "hj-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://hj.example.com"},
			{ID: "备用-claude", Name: "备用-claude", Protocol: domain.ProtocolClaude, BaseURL: "https://backup.example.com"},
		},
		Routes: []domain.Route{
			{ID: "hj-claude-claude", ProviderID: "hj-claude", OutputProtocol: domain.ProtocolClaude, Enabled: true},
		},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	server.userStore = newMemoryUserStore(domain.User{
		ID:                 "shouze",
		Username:           "shouze",
		Role:               domain.UserRoleUser,
		RoleID:             domain.RoleIDUser,
		AllowedResourceIDs: []string{"hj-claude", "备用-claude"},
		Enabled:            false, // 已被禁用
	})
	return server
}

// 禁用账号的 Key 一把都不能再服务，哪怕授权表还是完好的。
func TestDisabledUserOwnsNoProviders(t *testing.T) {
	server := newDisabledUserServer(t)
	if allowed := server.allowedProviderIDsForUser("shouze"); len(allowed) != 0 {
		t.Fatalf("a disabled account must resolve to no providers, got %v", allowed)
	}
	key := domain.APIKey{ID: "key-1", OwnerUserID: "shouze"}
	if server.providerAllowedForKeyOwner(key, "hj-claude") {
		t.Fatal("a disabled account's key must not reach a provider it was granted")
	}
	// 整条 failover 链都要被滤掉：留着一个备选等于没禁用。
	chain := apiKeyProviderChain("hj-claude", []string{"备用-claude"})
	if got := server.allowedProviderChainForKey(key, chain); len(got) != 0 {
		t.Fatalf("disabled account's chain must be empty, got %v", got)
	}
	// 注意：这里不断言 keysVisibleTo。禁用账号登不进控制台（校验会话时就被
	// 挡掉了），那条路径根本走不到它身上；真正要保证的是上面这些请求时的闸门。
	// Key 行本身也刻意保持 enabled——管理员在密钥页仍能看到它、能删除它，
	// 而「禁用这个人」与「禁用这把钥匙」是两件事。
}

// 重新启用后必须立刻恢复：禁用是可逆的运维动作，不是删除。
func TestReEnabledUserGetsProvidersBack(t *testing.T) {
	server := newDisabledUserServer(t)
	user, err := server.userStore.UserByID("shouze")
	if err != nil {
		t.Fatal(err)
	}
	user.Enabled = true
	if err := server.userStore.UpdateUser(user); err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{ID: "key-1", OwnerUserID: "shouze"}
	if !server.providerAllowedForKeyOwner(key, "hj-claude") {
		t.Fatal("re-enabled account must reach its granted providers again")
	}
}

// 拒绝必须说清楚是「账号被禁用」，不能报成「Provider 未分配」：后者的提示会
// 把运维直接引到一份其实完全正常的授权表上。
func TestDisabledAccountRefusalSaysWhatHappened(t *testing.T) {
	server := newDisabledUserServer(t)
	key := domain.APIKey{
		ID:          "key-1",
		OwnerUserID: "shouze",
		RouteID:     "hj-claude-claude",
	}
	if !server.keyOwnerDisabled(key) {
		t.Fatal("keyOwnerDisabled must recognise the disabled owner")
	}

	route, err := server.router.RouteByID("hj-claude-claude")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	status, _, _, _, _, err := server.executeProtocolFlowWithFailover(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		route, domain.RouteDecision{ProviderID: "hj-claude"},
		"claude-3", map[string]any{}, domain.ProtocolClaude, false, key, true,
	)
	if err == nil {
		t.Fatal("a disabled account's request must be refused")
	}
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
	if !errors.Is(err, errProviderNotAssigned) {
		t.Fatalf("error must map to a permission denial, got %v", err)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("refusal must name the disabled account, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("refusal must not blame the grant table, got %q", err.Error())
	}
}

// 管理员自己的 Key（无 OwnerUserID）不受影响：禁用是用来拦普通账号的，
// 不能因为某处 resolve 出一个空 owner 就把管理员一起挡住。
func TestAdminKeysAreNotAffectedByDisabledCheck(t *testing.T) {
	server := newDisabledUserServer(t)
	for _, key := range []domain.APIKey{
		{ID: "admin"},
		{ID: "legacy-admin", OwnerUserID: legacyAdminUserID},
	} {
		if server.keyOwnerDisabled(key) {
			t.Fatalf("key %q must not be treated as a disabled account", key.ID)
		}
		if !server.providerAllowedForKeyOwner(key, "hj-claude") {
			t.Fatalf("admin key %q must stay unrestricted", key.ID)
		}
	}
}

// 删掉的用户不是"被禁用"：它没有行，owner 查不到，行为应保持原样（授权集
// 为空 → 拒绝），但错误文案不能冒充成「账号被禁用」。
func TestDeletedOwnerIsNotReportedAsDisabled(t *testing.T) {
	server := newDisabledUserServer(t)
	if server.keyOwnerDisabled(domain.APIKey{ID: "k", OwnerUserID: "已删除的人"}) {
		t.Fatal("an unknown owner must not be reported as disabled")
	}
}
