package monitor

import (
	"testing"
	"time"
)

func TestAggregateByProvider(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	logs := []RequestLog{
		{Time: now, ProviderID: "p1", InputTokens: 10, OutputTokens: 5},
		{Time: now, ProviderID: "p1", InputTokens: 20, OutputTokens: 10},
		{Time: now, ProviderID: "p2", InputTokens: 3, OutputTokens: 1},
		{Time: now, ProviderID: "", InputTokens: 1, OutputTokens: 1},
	}

	stats := aggregateByProvider(logs, now.Add(-time.Hour))
	if len(stats) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(stats))
	}
	byID := map[string]ProviderDayStats{}
	for _, item := range stats {
		byID[item.ProviderID] = item
	}
	p1 := byID["p1"]
	if p1.RequestCount != 2 || p1.InputTokens != 30 || p1.OutputTokens != 15 {
		t.Fatalf("unexpected p1 stats: %+v", p1)
	}
	p2 := byID["p2"]
	if p2.RequestCount != 1 || p2.InputTokens != 3 {
		t.Fatalf("unexpected p2 stats: %+v", p2)
	}
	unknown := byID["_unknown"]
	if unknown.RequestCount != 1 || unknown.InputTokens != 1 {
		t.Fatalf("unexpected unknown stats: %+v", unknown)
	}
}

func TestUsageStatsIncludesByProvider(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	logs := []RequestLog{
		{Time: now, ProviderID: "anthropic", APIKeyID: "k1", APIKeyName: "Main", InputTokens: 100, OutputTokens: 50},
		{Time: now, ProviderID: "openai", APIKeyID: "k1", APIKeyName: "Main", InputTokens: 40, OutputTokens: 20},
	}
	snapshot := store.UsageStats(logs, now)
	if len(snapshot.Today.ByProvider) != 2 {
		t.Fatalf("today byProvider=%d", len(snapshot.Today.ByProvider))
	}
	if len(snapshot.Month.ByProvider) != 2 {
		t.Fatalf("month byProvider=%d", len(snapshot.Month.ByProvider))
	}
}
