package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitoring"
)

func TestMonitoringSourceProviders(t *testing.T) {
	expiry := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	state := domain.GatewayState{
		Providers: []domain.Provider{
			{
				ID: "p-claude", Name: "Claude 主号", AuthType: domain.AuthTypeClaudeOAuth,
				ClaudeOAuth: &domain.ClaudeOAuthCredential{AccessToken: "a", RefreshToken: "r", ExpiresAt: expiry.Format(time.RFC3339)},
			},
			{
				ID: "p-glm", Name: "智谱", Protocol: "openai_chat",
				BaseURL: "https://open.bigmodel.cn/api/paas/v4", AuthType: domain.AuthTypeAPIKey,
			},
			{ID: "p-gone", Name: "已删除", Deleted: true},
		},
	}
	server := NewServer(NewRouter(state), nil)
	source := newMonitoringSource(server)

	providers, err := source.Providers(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("deleted provider leaked into the snapshot: %+v", providers)
	}
	claude := providers[0]
	if claude.Vendor != "claude" || !claude.Capabilities.OAuth || !claude.Capabilities.Quota {
		t.Fatalf("claude capabilities wrong: %+v", claude)
	}
	if !claude.ExpiresAt.Equal(expiry) {
		t.Fatalf("expiry = %s, want %s", claude.ExpiresAt, expiry)
	}
	glm := providers[1]
	if glm.Vendor != "zhipu" {
		t.Fatalf("glm vendor = %q", glm.Vendor)
	}
	if glm.Capabilities.Quota || glm.Capabilities.OAuth || glm.Capabilities.Models {
		t.Fatalf("API-key vendor invented capabilities: %+v", glm.Capabilities)
	}
	if !glm.ExpiresAt.IsZero() {
		t.Fatalf("API-key vendor has an expiry: %s", glm.ExpiresAt)
	}
}

func TestMonitoringSourceKeysUsesWindowUsage(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
	state := domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "k1", Name: "主密钥", QuotaWindow: "day", QuotaTokens: 1000, QuotaCostMicros: 5_000_000},
		{ID: "k2", Name: "无限额"},
	}}
	server := NewServer(NewRouter(state), nil)
	source := newMonitoringSource(server)

	keys, err := source.Keys(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d", len(keys))
	}
	if keys[0].TokenLimit != 1000 || keys[0].CostLimitMicros != 5_000_000 || keys[0].QuotaWindow != "day" {
		t.Fatalf("limits not carried: %+v", keys[0])
	}
	if keys[0].TokenUsed != 0 || keys[0].CostUsedMicros != 0 {
		t.Fatalf("usage invented without a log store: %+v", keys[0])
	}
	if keys[1].TokenLimit != 0 || keys[1].CostLimitMicros != 0 {
		t.Fatalf("unlimited key gained a limit: %+v", keys[1])
	}
}

func TestClaudeQuotaWindowsKeepBucketsApart(t *testing.T) {
	fiveHourReset := "2026-10-26T14:40:00Z"
	weeklyReset := "2026-10-31T04:00:00.123456789Z"
	report := ClaudeOAuthUsageReport{
		Available: true,
		FiveHour:  &ClaudeOAuthUsageBucket{Utilization: 0, ResetsAt: fiveHourReset},
		SevenDay:  &ClaudeOAuthUsageBucket{Utilization: 51, ResetsAt: weeklyReset},
		// Sonnet window outranks the weekly one here only to prove the reset follows
		// the tightest bucket, not the first one present.
		SevenDaySonnet: &ClaudeOAuthUsageBucket{Utilization: 120, ResetsAt: weeklyReset},
	}
	windows := claudeQuotaWindows(report)
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(windows), windows)
	}
	if windows[0].Key != "five_hour" || windows[0].UsedPercent != 0 {
		t.Fatalf("five-hour window must come first and keep 0%%: %+v", windows[0])
	}
	if windows[2].Key != "seven_day_sonnet" || windows[2].UsedPercent != 100 {
		t.Fatalf("used percent must be clamped to 0..100: %+v", windows[2])
	}
	if !windows[1].ResetsAt.Equal(time.Date(2026, 10, 31, 4, 0, 0, 123456789, time.UTC)) {
		t.Fatalf("nanosecond resets_at lost: %s", windows[1].ResetsAt)
	}

	reset := claudeWindowReset(report)
	if !reset.Equal(time.Date(2026, 10, 31, 4, 0, 0, 123456789, time.UTC)) {
		t.Fatalf("reset of the tightest window = %s", reset)
	}
	if !claudeWindowReset(ClaudeOAuthUsageReport{}).IsZero() {
		t.Fatal("an empty report must not invent a reset time")
	}
	// 最紧的窗口没有 resets_at 时，退回到其它有时间的窗口，而不是丢掉重置信息。
	fallback := claudeWindowReset(ClaudeOAuthUsageReport{
		SevenDay: &ClaudeOAuthUsageBucket{Utilization: 90},
		FiveHour: &ClaudeOAuthUsageBucket{Utilization: 10, ResetsAt: fiveHourReset},
	})
	if !fallback.Equal(time.Date(2026, 10, 26, 14, 40, 0, 0, time.UTC)) {
		t.Fatalf("fallback reset = %s", fallback)
	}
}

func TestMonitoringSourceMultiIPMapsHits(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), nil)
	source := newMonitoringSource(server)
	if _, err := source.MultiIP(context.Background(), time.Now()); err == nil {
		t.Fatal("expected a data error when the store cannot detect multi-IP use")
	}
	if _, err := source.ConcurrentIP(context.Background(), time.Now()); err == nil {
		t.Fatal("expected a data error when the store cannot detect concurrent IPs")
	}
}

func TestMonitoringSourceQuotaUnsupportedIsNil(t *testing.T) {
	state := domain.GatewayState{Providers: []domain.Provider{{
		ID: "p-glm", Name: "智谱", BaseURL: "https://open.bigmodel.cn/api/paas/v4", AuthType: domain.AuthTypeAPIKey,
	}}}
	server := NewServer(NewRouter(state), nil)
	source := newMonitoringSource(server)
	snapshot, err := source.Quota(context.Background(), monitoring.ProviderObservation{ID: "p-glm", Vendor: "zhipu"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("expected no quota snapshot for an unsupported vendor, got %+v", snapshot)
	}
	missing, err := source.Quota(context.Background(), monitoring.ProviderObservation{ID: "gone"})
	if err != nil || missing != nil {
		t.Fatalf("unknown provider should be silent, got %+v / %v", missing, err)
	}
}

func TestQuotaFromWindowsPicksWorstWindow(t *testing.T) {
	now := time.Now()
	snapshot := quotaFromWindows("qoder", []quotaWindowView{
		{UsedPercent: 20, Limit: 500},
		{UsedPercent: 80, Limit: 100},
		{UsedPercent: 99, Limit: 0},
	}, time.Time{}, now)
	if snapshot == nil || snapshot.Limit != 100 {
		t.Fatalf("wrong window chosen: %+v", snapshot)
	}
	if snapshot.Ratio < 0.19 || snapshot.Ratio > 0.21 {
		t.Fatalf("ratio = %f, want about 0.2", snapshot.Ratio)
	}
	if snapshot.Remaining < 19.9 || snapshot.Remaining > 20.1 {
		t.Fatalf("remaining = %f", snapshot.Remaining)
	}
	if !snapshot.Available() {
		t.Fatal("a partially available window must be available")
	}

	empty := quotaFromWindows("qoder", []quotaWindowView{{UsedPercent: 10, Limit: 0}}, time.Time{}, now)
	if empty.Ratio != -1 || empty.Available() {
		t.Fatalf("no usable window should be unavailable, got %+v", empty)
	}
}

func TestWorkBuddyCreditsDeadline(t *testing.T) {
	windows := []workBuddyQuotaWin{
		{Name: "Credits", Limit: 100, RunsOut: 10, RunsOutAt: "2026-10-30T00:00:00Z"},
		{Name: "Credits", Limit: 100, RunsOut: 0, RunsOutAt: "2026-10-24T00:00:00Z"},
		{Name: "Credits", Limit: 100, RunsOut: 5, RunsOutAt: "2026-10-26T00:00:00Z"},
	}
	deadline := workBuddyCreditsDeadline(windows)
	want := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
	if !workBuddyCreditsDeadline(nil).IsZero() {
		t.Fatal("no dated batch must yield a zero deadline")
	}
}

func TestProviderVendorKey(t *testing.T) {
	cases := []struct {
		provider domain.Provider
		want     string
	}{
		{domain.Provider{AuthType: domain.AuthTypeQoderCN}, "qoder"},
		{domain.Provider{AuthType: domain.AuthTypeWorkBuddyAI}, "workbuddy"},
		{domain.Provider{AuthType: domain.AuthTypeZCodeBigModel}, "zcode"},
		{domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "https://api.anthropic.com"}, "anthropic"},
		{domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "HTTPS://API.DEEPSEEK.COM/v1"}, "deepseek"},
		{domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "https://api.minimaxi.com/v1"}, "minimax"},
		{domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "https://opencode.ai/zen/go/v1"}, "opencode"},
		{domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "https://relay.example.com/v1"}, ""},
	}
	for _, tc := range cases {
		if got := providerVendorKey(tc.provider); got != tc.want {
			t.Fatalf("%s/%s: vendor = %q, want %q", tc.provider.AuthType, tc.provider.BaseURL, got, tc.want)
		}
	}
}

func TestClassifyQuotaError(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"unexpected status 401": "auth_failed",
		"Qoder 尚未登录":            "auth_failed",
		"unexpected status 429": "rate_limited",
		"Get \"https://x/y\": context deadline exceeded": "network",
		"boom": "unknown",
	}
	for input, want := range cases {
		var err error
		if input != "" {
			err = errString(input)
		}
		if got := classifyQuotaError(err); got != want {
			t.Fatalf("classify(%q) = %q, want %q", input, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestProviderCredentialExpiry(t *testing.T) {
	raw := "2026-11-01T00:00:00Z"
	want, _ := time.Parse(time.RFC3339, raw)
	provider := domain.Provider{Trae: &domain.TraeCredential{ExpiresAt: raw}}
	if got := providerCredentialExpiry(provider); !got.Equal(want) {
		t.Fatalf("trae expiry = %s", got)
	}
	zcode := domain.Provider{AuthType: domain.AuthTypeZCode, ZCode: &domain.ZCodeCredential{APIKey: "k"}}
	if got := providerCredentialExpiry(zcode); !got.IsZero() {
		t.Fatalf("zcode must not report an expiry, got %s", got)
	}
}
