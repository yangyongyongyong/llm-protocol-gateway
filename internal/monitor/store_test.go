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

func usageEvent(now time.Time, model string, input, output, cache int64) UsageEvent {
	return UsageEvent{
		Time:         now,
		APIKeyID:     "k1",
		APIKeyName:   "Main",
		ProviderID:   "anthropic",
		Model:        model,
		Status:       200,
		InputTokens:  input,
		OutputTokens: output,
		CacheTokens:  cache,
		LatencyMs:    120,
		TTFTMs:       80,
	}
}

func TestApplyUsageEventSyncModelRanking(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	store := NewStore()
	store.ApplyUsageEventSync(usageEvent(now, "claude-opus-4-8", 100, 50, 10))
	store.ApplyUsageEventSync(usageEvent(now, "claude-opus-4-8", 20, 10, 0))
	store.ApplyUsageEventSync(usageEvent(now, "your-model", 999, 999, 999))
	store.ApplyUsageEventSync(usageEvent(now, "gpt-4.1", 200, 80, 0))

	snapshot := store.UsageStats(now)
	if len(snapshot.Today.ByModel) != 2 {
		t.Fatalf("expected 2 real models, got %d: %+v", len(snapshot.Today.ByModel), snapshot.Today.ByModel)
	}
	if snapshot.Today.ByModel[0].Model != "gpt-4.1" {
		t.Fatalf("expected gpt-4.1 first, got %q", snapshot.Today.ByModel[0].Model)
	}
	opus := snapshot.Today.ByModel[1]
	if opus.Model != "claude-opus-4-8" || opus.RequestCount != 2 || opus.InputTokens != 120 {
		t.Fatalf("unexpected opus stats: %+v", opus)
	}
}

func TestAggregateByModelRanksByTokenUsage(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	logs := []RequestLog{
		{Time: now, Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 50, CacheTokens: 10},
		{Time: now, Model: "claude-opus-4-8", InputTokens: 20, OutputTokens: 10},
		{Time: now, Model: "gpt-4.1", InputTokens: 200, OutputTokens: 80},
		{Time: now, Model: "", InputTokens: 1, OutputTokens: 1},
	}

	stats := aggregateByModel(logs, now.Add(-time.Hour))
	if len(stats) != 2 {
		t.Fatalf("expected 2 real models (empty omitted), got %d: %+v", len(stats), stats)
	}
	if stats[0].Model != "gpt-4.1" {
		t.Fatalf("expected gpt-4.1 first by tokens, got %q", stats[0].Model)
	}
	if stats[1].Model != "claude-opus-4-8" {
		t.Fatalf("expected claude-opus-4-8 second, got %q", stats[1].Model)
	}
	if stats[1].RequestCount != 2 || modelUsageTotalTokens(stats[1]) != 180 {
		t.Fatalf("unexpected opus stats: %+v", stats[1])
	}
}

func TestAggregateByModelCollapsesAliasArrowForm(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	logs := []RequestLog{
		{Time: now, Model: "luca-claude-sonnet-5 -> claude-opus-4-8", InputTokens: 10, OutputTokens: 5},
		{Time: now, Model: "claude-opus-4-8", InputTokens: 20, OutputTokens: 10},
		{Time: now, Model: "luca-claude-sonnet-5 -> claude-sonnet-5", InputTokens: 7, OutputTokens: 3},
	}
	stats := aggregateByModel(logs, now.Add(-time.Hour))
	if len(stats) != 2 {
		t.Fatalf("expected 2 real models after collapse, got %d: %+v", len(stats), stats)
	}
	byModel := map[string]ModelDayStats{}
	for _, item := range stats {
		byModel[item.Model] = item
	}
	opus := byModel["claude-opus-4-8"]
	if opus.RequestCount != 2 || opus.InputTokens != 30 || opus.OutputTokens != 15 {
		t.Fatalf("unexpected collapsed opus stats: %+v", opus)
	}
	sonnet := byModel["claude-sonnet-5"]
	if sonnet.RequestCount != 1 || sonnet.InputTokens != 7 {
		t.Fatalf("unexpected sonnet stats: %+v", sonnet)
	}
}

func TestCanonicalModelForUsage(t *testing.T) {
	if got := CanonicalModelForUsage("luca-claude-sonnet-5 -> claude-opus-4-8"); got != "claude-opus-4-8" {
		t.Fatalf("got %q", got)
	}
	if got := CanonicalModelForUsage("claude-opus-4-8"); got != "claude-opus-4-8" {
		t.Fatalf("got %q", got)
	}
	if got := CanonicalModelForUsage(""); got != "_unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestUsageStatsIncludesByProvider(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, APIKeyID: "k1", APIKeyName: "Main", ProviderID: "anthropic",
		Model: "claude-opus-4-8", Status: 200, InputTokens: 100, OutputTokens: 50,
	})
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, APIKeyID: "k1", APIKeyName: "Main", ProviderID: "openai",
		Model: "gpt-4.1", Status: 200, InputTokens: 40, OutputTokens: 20,
	})
	snapshot := store.UsageStats(now)
	if len(snapshot.Today.ByProvider) != 2 {
		t.Fatalf("today byProvider=%d", len(snapshot.Today.ByProvider))
	}
	if len(snapshot.Month.ByProvider) != 2 {
		t.Fatalf("month byProvider=%d", len(snapshot.Month.ByProvider))
	}
	if len(snapshot.Today.ByModel) != 2 {
		t.Fatalf("today byModel=%d", len(snapshot.Today.ByModel))
	}
	if snapshot.Range == nil || len(snapshot.Range.ByModel) != 2 {
		t.Fatalf("range byModel missing: %+v", snapshot.Range)
	}
	if snapshot.Today.Total.RequestCount != 2 {
		t.Fatalf("today total requests=%d", snapshot.Today.Total.RequestCount)
	}
}

func TestUsageStatsRangeDailyAndStatus(t *testing.T) {
	store := NewStore()
	day := time.Date(2026, 7, 8, 12, 0, 0, 0, time.Local)
	from := time.Date(2026, 7, 8, 0, 0, 0, 0, time.Local)
	to := from.Add(24 * time.Hour)

	store.ApplyUsageEventSync(UsageEvent{
		Time: day, ProviderID: "p1", Model: "gpt-4.1", Status: 200,
		InputTokens: 10, OutputTokens: 5, LatencyMs: 100, TTFTMs: 50,
	})
	store.ApplyUsageEventSync(UsageEvent{
		Time: day, ProviderID: "p1", Model: "gpt-4.1", Status: 500,
		InputTokens: 20, OutputTokens: 10, LatencyMs: 300, TTFTMs: 0,
	})

	snapshot := store.UsageStatsRange(day, from, to)
	if len(snapshot.Daily) != 1 || snapshot.Daily[0].RequestCount != 2 {
		t.Fatalf("daily=%+v", snapshot.Daily)
	}
	if snapshot.Daily[0].AvgLatencyMs != 200 {
		t.Fatalf("avg latency=%d", snapshot.Daily[0].AvgLatencyMs)
	}
	if snapshot.Daily[0].AvgTTFTMs != 50 {
		t.Fatalf("avg ttft=%d", snapshot.Daily[0].AvgTTFTMs)
	}
	statusByClass := map[string]int64{}
	for _, bucket := range snapshot.Status {
		statusByClass[bucket.Class] = bucket.RequestCount
	}
	if statusByClass["2xx"] != 1 || statusByClass["5xx"] != 1 {
		t.Fatalf("status=%+v", snapshot.Status)
	}
}

func TestUsageStatsLastRequest(t *testing.T) {
	store := NewStore()
	early := time.Date(2026, 7, 8, 10, 0, 0, 0, time.Local)
	late := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	store.ApplyUsageEventSync(UsageEvent{Time: early, Model: "gpt-4.1", Status: 200})
	store.ApplyUsageEventSync(UsageEvent{
		Time: late, APIKeyID: "k1", APIKeyName: "Main", ProviderID: "openai",
		Model: "claude-opus-4-8", Status: 200, InputTokens: 42,
	})

	snapshot := store.UsageStats(late)
	if snapshot.Today.LastRequest == nil {
		t.Fatal("expected lastRequest")
	}
	if snapshot.Today.LastRequest.Model != "claude-opus-4-8" || snapshot.Today.LastRequest.InputTokens != 42 {
		t.Fatalf("lastRequest=%+v", snapshot.Today.LastRequest)
	}
}

func TestApplyUsageEventSyncNormalizesExclusiveInput(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	store := NewStore()
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, Model: "claude-opus-4-8", Status: 200,
		InputTokens: 17900000, OutputTokens: 1, CacheTokens: 188800000,
	})
	snapshot := store.UsageStats(now)
	if snapshot.Today.Total.InputTokens != 17900000+188800000 {
		t.Fatalf("expected normalized inclusive input, got %d", snapshot.Today.Total.InputTokens)
	}
	if snapshot.Today.Total.CacheTokens != 188800000 {
		t.Fatalf("unexpected cache total: %d", snapshot.Today.Total.CacheTokens)
	}
	rate := CacheHitRate(snapshot.Today.Total.InputTokens, snapshot.Today.Total.CacheTokens)
	want := 188800000.0 / (17900000.0 + 188800000.0) * 100
	if rate < want-0.01 || rate > want+0.01 {
		t.Fatalf("expected hit rate ~%.2f%%, got %.2f%%", want, rate)
	}
}

func TestApplyUsageEventSyncOpenAIInclusiveInput(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.Local)
	store := NewStore()
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, Model: "gpt-4.1", Status: 200,
		InputTokens: 120, OutputTokens: 8, CacheTokens: 96,
	})
	snapshot := store.UsageStats(now)
	if snapshot.Today.Total.InputTokens != 120 {
		t.Fatalf("expected inclusive input unchanged, got %d", snapshot.Today.Total.InputTokens)
	}
	rate := CacheHitRate(snapshot.Today.Total.InputTokens, snapshot.Today.Total.CacheTokens)
	if rate < 79.9 || rate > 80.1 {
		t.Fatalf("expected ~80%% hit rate, got %.2f%%", rate)
	}
}
