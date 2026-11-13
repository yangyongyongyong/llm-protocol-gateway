package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 一张 5 节点的链（首选 a + 备选 b/c/d/e）。把 b 提为首选、同时把 a 放回
// b 原来的位次，是控制台「设为首选」现在提交的形态（见 web/src/lib.tsx 的
// promoteApiKeyProvider）。这条测试钉住后端随后做的那步归一化不会把节点吞掉：
// 它只该剔除「等于新首选」的条目，总数一个不少。
func TestAPIKeyPromoteKeepsEveryChainNode(t *testing.T) {
	t.Parallel()
	newPromoteRouter := func(t *testing.T) *Router {
		t.Helper()
		return NewRouter(domain.GatewayState{
			Providers: []domain.Provider{
				{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat},
				{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat},
				{ID: "c", Name: "c", Protocol: domain.ProtocolOpenAIChat},
				{ID: "d", Name: "d", Protocol: domain.ProtocolOpenAIChat},
				{ID: "e", Name: "e", Protocol: domain.ProtocolOpenAIChat},
			},
			Routes: []domain.Route{
				{ID: "r-a", Name: "r-a", ProviderID: "a", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
				{ID: "r-b", Name: "r-b", ProviderID: "b", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
			},
		})
	}

	router := newPromoteRouter(t)
	key, err := router.AddAPIKey(domain.APIKey{
		Name: "chain", RouteID: "r-a",
		FallbackProviderIDs: []string{"b", "c", "d", "e"},
	})
	if err != nil {
		t.Fatalf("add key: %v", err)
	}

	// 设为首选：绑定换到 b 的路由，a 站回 b 的位置。
	updated, err := router.UpdateAPIKey(key.ID, domain.APIKey{
		Name:                   "chain",
		RouteID:                "r-b",
		FallbackProviderIDs:    []string{"a", "c", "d", "e"},
		FallbackModelOverrides: map[string]string{},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if updated.RouteID != "r-b" {
		t.Fatalf("RouteID = %q, want r-b", updated.RouteID)
	}
	want := []string{"a", "c", "d", "e"}
	if len(updated.FallbackProviderIDs) != len(want) {
		t.Fatalf("fallbacks = %v, want %v: promoting must not drop a node", updated.FallbackProviderIDs, want)
	}
	for i, id := range want {
		if updated.FallbackProviderIDs[i] != id {
			t.Fatalf("fallbacks = %v, want %v (order preserved, old primary back in place)", updated.FallbackProviderIDs, want)
		}
	}

	// 对照：只改 routeId 的旧写法（不提备选列表）会把原首选挤丢——这正是
	// 「设为首选少一个节点」的复现，归一化只认新首选、不替我们补回旧的。
	router = newPromoteRouter(t)
	key, err = router.AddAPIKey(domain.APIKey{
		Name: "chain", RouteID: "r-a",
		FallbackProviderIDs: []string{"b", "c", "d", "e"},
	})
	if err != nil {
		t.Fatalf("add key: %v", err)
	}
	stale, err := router.UpdateAPIKey(key.ID, domain.APIKey{Name: "chain", RouteID: "r-b"})
	if err != nil {
		t.Fatalf("route-only patch: %v", err)
	}
	if len(stale.FallbackProviderIDs) != 3 {
		t.Fatalf("the old route-only patch left %d fallbacks, want 3 — if this changed, revisit the console fix: %v",
			len(stale.FallbackProviderIDs), stale.FallbackProviderIDs)
	}
}

// 交换首选时，主模型与备选固定模型必须一起跟随 Provider 搬运；后端归一化
// 不能把新主模型清空，也不能把旧主模型进入备选后对应的条目丢掉。
func TestAPIKeyPromoteKeepsPrimaryAndFallbackModelOverrides(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat},
			{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat},
		},
		Routes: []domain.Route{
			{ID: "r-a", Name: "r-a", ProviderID: "a", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
			{ID: "r-b", Name: "r-b", ProviderID: "b", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
		},
	})
	key, err := router.AddAPIKey(domain.APIKey{
		Name: "models", RouteID: "r-a", ModelOverride: "m-a",
		FallbackProviderIDs: []string{"b"}, FallbackModelOverrides: map[string]string{"b": "m-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := router.UpdateAPIKey(key.ID, domain.APIKey{
		Name: "models", RouteID: "r-b", ModelOverride: "m-b",
		FallbackProviderIDs: []string{"a"}, FallbackModelOverrides: map[string]string{"a": "m-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ModelOverride != "m-b" || updated.FallbackModelOverrides["a"] != "m-a" {
		t.Fatalf("model overrides lost on promote: model=%q fallbacks=%v", updated.ModelOverride, updated.FallbackModelOverrides)
	}
}

// 旧首选变成了备选时，Key 级 activeProviderId 也必须清空，否则调度会继续把
// 旧节点当 sticky 首选，用户看到的就是“改了首选但请求还在旧 Provider”。
func TestAPIKeyPromoteClearsStickyActiveProvider(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "a", Name: "a", Protocol: domain.ProtocolOpenAIChat},
			{ID: "b", Name: "b", Protocol: domain.ProtocolOpenAIChat},
		},
		Routes: []domain.Route{
			{ID: "r-a", Name: "r-a", ProviderID: "a", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
			{ID: "r-b", Name: "r-b", ProviderID: "b", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true},
		},
	})
	key, err := router.AddAPIKey(domain.APIKey{
		Name: "sticky", RouteID: "r-a", ActiveProviderID: "b", FallbackProviderIDs: []string{"b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := router.UpdateAPIKey(key.ID, domain.APIKey{
		Name: "sticky", RouteID: "r-b", FallbackProviderIDs: []string{"a"}, FallbackModelOverrides: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ActiveProviderID != "" {
		t.Fatalf("promote kept stale active provider %q", updated.ActiveProviderID)
	}
}
