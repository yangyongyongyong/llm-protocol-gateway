package monitor

import (
	"testing"
	"time"
)

// TestUsageStatsTracksDownstreamGatewayByIngressOnly 验证下游网关只统计
// ingress，并且普通用户按 Key 隔离时只能看到自己 Key 带来的下游流量。
func TestUsageStatsTracksDownstreamGatewayByIngressOnly(t *testing.T) {
	now := time.Date(2026, 11, 13, 18, 0, 0, 0, time.Local)
	at := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, now.Location())
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	store := NewStore()
	for _, event := range []UsageEvent{
		{Time: at, APIKeyID: "key-a", APIKeyName: "Key A", UserID: "owner-a", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 100, OutputTokens: 10, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
		{Time: at.Add(time.Second), APIKeyID: "key-b", APIKeyName: "Key B", UserID: "owner-b", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 200, OutputTokens: 20, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
		{Time: at.Add(2 * time.Second), APIKeyID: "key-c", APIKeyName: "Key C", UserID: "owner-c", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 7, OutputTokens: 2, PeerGatewayID: "gw_down_b", PeerDirection: "ingress"},
		{Time: at.Add(3 * time.Second), APIKeyID: "key-a", APIKeyName: "Key A", UserID: "owner-a", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 300, OutputTokens: 30, PeerGatewayID: "gw_upstream", PeerDirection: "egress"},
	} {
		store.ApplyUsageEventSync(event)
	}

	admin := store.UsageStatsRange(now, dayStart, dayStart.AddDate(0, 0, 1))
	assertDownstreamStats(t, admin.Today.ByDownstreamGateway, map[string][3]int64{
		"gw_down_a": {2, 300, 30},
		"gw_down_b": {1, 7, 2},
	})
	if admin.Range == nil {
		t.Fatal("admin range snapshot is nil")
	}
	assertDownstreamStats(t, admin.Range.ByDownstreamGateway, map[string][3]int64{
		"gw_down_a": {2, 300, 30},
		"gw_down_b": {1, 7, 2},
	})

	user := store.UsageStatsRangeForKeys(now, dayStart, dayStart.AddDate(0, 0, 1), []string{"key-a"})
	assertDownstreamStats(t, user.Today.ByDownstreamGateway, map[string][3]int64{
		"gw_down_a": {1, 100, 10},
	})
	if user.Range == nil {
		t.Fatal("user range snapshot is nil")
	}
	assertDownstreamStats(t, user.Range.ByDownstreamGateway, map[string][3]int64{
		"gw_down_a": {1, 100, 10},
	})
}

// assertDownstreamStats 用 gateway -> 请求数/输入/输出 的期望值校验下游维度。
func assertDownstreamStats(t *testing.T, rows []DownstreamGatewayDayStats, want map[string][3]int64) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("downstream rows = %+v, want %d buckets", rows, len(want))
	}
	for _, row := range rows {
		expected, ok := want[row.GatewayID]
		if !ok {
			t.Fatalf("unexpected downstream gateway %q in %+v", row.GatewayID, rows)
		}
		got := [3]int64{row.RequestCount, row.InputTokens, row.OutputTokens}
		if got != expected {
			t.Fatalf("downstream %s = %v, want %v", row.GatewayID, got, expected)
		}
	}
}
