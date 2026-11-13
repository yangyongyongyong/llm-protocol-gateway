package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// WorkBuddy 的 credit 卡片必须沿着 /__peer/providers → 子端成员快照一路保留，
// 否则下游只能看到一条普通 peer provider，和父端卡片完全不像。
func TestPeerProviderCardCarriesWorkBuddyCredits(t *testing.T) {
	provider := domain.Provider{
		ID: "wb-main", Name: "WorkBuddy 订阅", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://copilot.tencent.com/v2", AuthType: domain.AuthTypeWorkBuddy,
		APIKeySource: "literal:secret",
		WorkBuddy: &domain.WorkBuddyCredential{
			AccessToken: "access", RefreshToken: "refresh", AccountLabel: "陌", UserID: "user-123456789", Connected: true,
		},
		Models: []domain.Model{{ID: "deepseek-v4.1-flash", ProviderID: "wb-main", InMenu: true}},
	}
	parent := newPeerTestServer(t, provider)
	// 上游卡片的“总量”必须包含父网关所有用户，而不只是本次注册的下游。
	for _, event := range []monitor.UsageEvent{
		{Time: time.Now(), APIKeyID: "user-a", UserID: "user-a", ProviderID: provider.ID, Model: "deepseek-v4.1-flash", Status: 200, InputTokens: 100, OutputTokens: 20},
		{Time: time.Now(), APIKeyID: "user-b", UserID: "user-b", ProviderID: provider.ID, Model: "deepseek-v4.1-flash", Status: 200, InputTokens: 50, OutputTokens: 5},
	} {
		parent.logs.ApplyUsageEventSync(event)
	}
	parent.oauthUsageCache.setForAccount(
		oauthUsageCacheKey("workbuddy", provider.ID),
		oauthUsageAccountID(provider),
		workBuddyQuota{
			Plan: "Business Pro", FetchedAt: "2026-11-12T07:00:00Z",
			Windows: []workBuddyQuotaWin{{
				Name: "Credits", Used: 37.5, Amount: 1250, Limit: 3333, Unit: "credits",
				ResetsAt: "2026-12-01T00:00:00Z",
			}},
		},
	)
	_, registration := peerRegister(t, parent, peerRegisterPayload(map[string]any{
		"requestedProviderIds": []string{provider.ID},
	}))
	parentHTTP := httptest.NewServer(parent.Handler())
	defer parentHTTP.Close()

	granted, err := newPeerClient().providers(t.Context(), parentHTTP.URL, registration.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0].Card == nil {
		t.Fatalf("peer provider card missing: %+v", granted)
	}
	card := granted[0].Card
	if card.AuthType != domain.AuthTypeWorkBuddy || card.Vendor != "workbuddy" || card.AccountLabel != "陌" || !card.Connected {
		t.Fatalf("card metadata = %+v", card)
	}
	if card.TodayTokens == nil || *card.TodayTokens != 175 {
		t.Fatalf("card today tokens = %v, want parent-wide 175", card.TodayTokens)
	}
	if card.Usage == nil || card.Usage.Plan != "Business Pro" || len(card.Usage.Buckets) != 1 {
		t.Fatalf("card usage = %+v", card.Usage)
	}
	bucket := card.Usage.Buckets[0]
	if bucket.Utilization != 37.5 || bucket.Note != "1250 / 3333 credits" || bucket.ResetsAt == "" {
		t.Fatalf("credit bucket = %+v", bucket)
	}

	// 子端落 Provider 时把同一份卡片快照存进 PeerGrants，刷新页面即可直接渲染。
	child := newPeerTestServer(t)
	childRegistration := peerRegistrationFromResponse(parentHTTP.URL, registration)
	rows, err := child.createPeerUpstreamProviders(httptest.NewRequest(http.MethodPost, "/__peer-upstreams", nil), "parent", domain.ProtocolOpenAIChat, childRegistration, granted)
	if err != nil || len(rows) != 1 {
		t.Fatalf("create child provider: %v rows=%+v", err, rows)
	}
	if rows[0].PeerGrants == nil || rows[0].PeerGrants.Card == nil || rows[0].PeerGrants.Card.Usage == nil {
		t.Fatalf("child provider lost inherited card: %+v", rows[0].PeerGrants)
	}
}
