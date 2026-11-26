package monitor

import (
	"testing"
	"time"
)

// Cost is summed by hand in a dozen merge loops (per day, per provider, per
// model, per user, per protocol, per key, per trend point). One missed loop reads
// as a day whose parts add up to less than its total, so this test pins every
// view that the console reads money from.
func TestUsageCostThreadsThroughEveryView(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	store := NewStore()
	yesterday := now.AddDate(0, 0, -1)
	events := []UsageEvent{
		{Time: yesterday, APIKeyID: "k1", APIKeyName: "key", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "openai_chat", Status: 200, InputTokens: 1000, CacheTokens: 400, CacheWriteTokens: 100,
			OutputTokens: 200, CostMicros: 7000, Priced: true, LatencyMs: 50},
		{Time: now, APIKeyID: "k1", APIKeyName: "key", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "openai_chat", Status: 200, InputTokens: 500, OutputTokens: 100, CostMicros: 3000, Priced: true, LatencyMs: 50},
		{Time: now, APIKeyID: "k2", APIKeyName: "key two", UserID: "u2", ProviderID: "p2", Model: "m2",
			OutputProtocol: "claude", Status: 503, InputTokens: 10, CostMicros: 5, Priced: true, LatencyMs: 5},
		{Time: now, APIKeyID: "k3", APIKeyName: "key three", UserID: "u3", ProviderID: "p3", Model: "m3",
			OutputProtocol: "openai_responses", Status: 200, InputTokens: 700, OutputTokens: 90, Priced: false, LatencyMs: 50},
	}
	for _, event := range events {
		store.ApplyUsageEventSync(event)
	}

	month := store.PeriodStats(now, now.AddDate(0, 0, -3), "month")
	if got := month.Total.CostMicros; got != 10005 {
		t.Fatalf("period total cost: got %d, want 10005", got)
	}
	if got := month.Total.CacheWriteTokens; got != 100 {
		t.Fatalf("period total cache writes: got %d", got)
	}
	// Coverage is the other half of the ledger. Three of the four requests had a
	// price row, so this period is not cheap — it is mostly unpriced, and only a
	// priced count can say so.
	if month.Total.RequestCount != 4 || month.Total.PricedCount != 3 {
		t.Fatalf("period coverage: priced %d of %d, want 3 of 4", month.Total.PricedCount, month.Total.RequestCount)
	}
	if priced := pricedOfProvider(month.ByProvider, "p3"); priced != 0 {
		t.Fatalf("unpriced provider must report no priced requests, got %d", priced)
	}
	for _, tc := range []struct {
		name string
		got  int64
		want int64
	}{
		{"byProvider p1", costOfProvider(month.ByProvider, "p1"), 10000},
		{"byProvider p2", costOfProvider(month.ByProvider, "p2"), 5},
		{"byModel m1", costOfModel(month.ByModel, "m1"), 10000},
		{"byUser u1", costOfUser(month.ByUser, "u1"), 10000},
		{"byProtocol openai_chat", costOfProtocol(month.ByProtocol, "openai_chat"), 10000},
		{"byAPIKey k1", costOfKey(month.ByAPIKey, "k1"), 10000},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s cost: got %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	today := store.TodayStats(now)
	if got := today.Total.CostMicros; got != 3005 {
		t.Fatalf("today cost: got %d, want 3005 (only today's two requests)", got)
	}

	// The trend series is what the chart plots; a missing loop there shows up as
	// a day with traffic and no money.
	usage := store.UsageStatsRange(now, now.AddDate(0, 0, -3), now.Add(24*time.Hour))
	var trendCost, trendPriced int64
	for _, point := range usage.Daily {
		trendCost += point.CostMicros
		trendPriced += point.PricedCount
	}
	if trendCost != 10005 {
		t.Fatalf("trend series cost: got %d, want 10005 (%#v)", trendCost, usage.Daily)
	}
	if trendPriced != 3 {
		t.Fatalf("trend series priced count: got %d, want 3 (%#v)", trendPriced, usage.Daily)
	}
}

func pricedOfProvider(stats []ProviderDayStats, id string) int64 {
	for _, item := range stats {
		if item.ProviderID == id {
			return item.PricedCount
		}
	}
	return -1
}

func costOfProvider(stats []ProviderDayStats, id string) int64 {
	for _, item := range stats {
		if item.ProviderID == id {
			return item.CostMicros
		}
	}
	return -1
}

func costOfModel(stats []ModelDayStats, id string) int64 {
	for _, item := range stats {
		if item.Model == id {
			return item.CostMicros
		}
	}
	return -1
}

func costOfUser(stats []UserDayStats, id string) int64 {
	for _, item := range stats {
		if item.UserID == id {
			return item.CostMicros
		}
	}
	return -1
}

func costOfProtocol(stats []ProtocolDayStats, id string) int64 {
	for _, item := range stats {
		if item.Protocol == id {
			return item.CostMicros
		}
	}
	return -1
}

func costOfKey(stats []APIKeyDayStats, id string) int64 {
	for _, item := range stats {
		if item.APIKeyID == id {
			return item.CostMicros
		}
	}
	return -1
}

// Two views rebuild their rows field by field instead of summing in place: the
// alias-collapse merge and the per-key (role=user) drill-down. Both once dropped
// cost and cache writes, which showed up as a leaderboard row reading 未计价 while
// the provider row for the same traffic had money.
func TestUsageCostSurvivesModelMergeAndKeyViews(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)

	merged := MergeModelDayStats([]ModelDayStats{
		{Model: "gpt-x-preview", RequestCount: 1, InputTokens: 1000, OutputTokens: 200, CacheWriteTokens: 40, CostMicros: 7000, PricedCount: 1},
		{Model: "gpt-x", RequestCount: 1, InputTokens: 500, OutputTokens: 100, CacheWriteTokens: 10, CostMicros: 3000, PricedCount: 1},
	}, func(model string) string { return "gpt-x" })
	if len(merged) != 1 {
		t.Fatalf("alias collapse expected one row, got %#v", merged)
	}
	row := merged[0]
	if row.CostMicros != 10000 || row.CacheWriteTokens != 50 || row.PricedCount != 2 || row.RequestCount != 2 {
		t.Fatalf("merge lost cost/cache writes/priced count: %#v", row)
	}

	store := NewStore()
	for _, event := range []UsageEvent{
		{Time: now, APIKeyID: "k1", APIKeyName: "key", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "openai_chat", Status: 200, InputTokens: 1000, OutputTokens: 200, CacheWriteTokens: 40,
			CostMicros: 7000, Priced: true},
		{Time: now, APIKeyID: "k1", APIKeyName: "key", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "openai_chat", Status: 200, InputTokens: 500, OutputTokens: 100, CacheWriteTokens: 10,
			CostMicros: 3000, Priced: true},
	} {
		store.ApplyUsageEventSync(event)
	}
	snapshot := store.UsageStatsRangeForKeys(now, now.AddDate(0, 0, -1), now.Add(24*time.Hour), []string{"k1"})
	period := snapshot.Range
	if got := costOfProvider(period.ByProvider, "p1"); got != 10000 {
		t.Fatalf("per-key view provider cost: got %d, want 10000 (%#v)", got, period.ByProvider)
	}
	if got := costOfModel(period.ByModel, "m1"); got != 10000 {
		t.Fatalf("per-key view model cost: got %d, want 10000 (%#v)", got, period.ByModel)
	}
	if got := costOfProtocol(period.ByProtocol, "openai_chat"); got != 10000 {
		t.Fatalf("per-key view protocol cost: got %d, want 10000 (%#v)", got, period.ByProtocol)
	}
	for _, item := range period.ByModel {
		if item.Model == "m1" && item.PricedCount != 2 {
			t.Fatalf("per-key view lost priced count: %#v", item)
		}
	}
}

// A day rebuilt from request logs must carry the same money as one built live,
// otherwise a restart would quietly change the console's totals.
func TestAggregateDailyCarriesCost(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	points := AggregateDaily([]RequestLog{
		{Time: now, InputTokens: 10, CacheTokens: 4, CacheWriteTokens: 2, OutputTokens: 1, CostMicros: 100, Priced: true},
		{Time: now.Add(time.Hour), InputTokens: 20, OutputTokens: 3, CostMicros: 50, Priced: true},
		// A request with no price row: it is volume without money.
		{Time: now.Add(2 * time.Hour), InputTokens: 30, OutputTokens: 4},
	}, now, now.Add(24*time.Hour))
	if len(points) == 0 {
		t.Fatal("no daily points")
	}
	day := points[0]
	if day.Date != now.Format("2006-01-02") {
		t.Fatalf("expected the first point to be the request day, got %#v", day)
	}
	if day.CostMicros != 150 || day.CacheWriteTokens != 2 {
		t.Fatalf("daily point lost cost or cache writes: %#v", day)
	}
	if day.RequestCount != 3 || day.PricedCount != 2 {
		t.Fatalf("daily point coverage: priced %d of %d, want 2 of 3", day.PricedCount, day.RequestCount)
	}
}

// The log-derived Today card is used when the permanent aggregate is behind it.
func TestTodayStatsFromLogsCarriesCost(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	snapshot := NewStore().TodayStatsFromLogs([]RequestLog{
		{Time: now, APIKeyID: "k1", APIKeyName: "key", ProviderID: "p1", Model: "m1", Status: 200,
			InputTokens: 10, CacheWriteTokens: 3, CostMicros: 40, Priced: true},
		{Time: now, APIKeyID: "k1", APIKeyName: "key", ProviderID: "p1", Model: "m1", Status: 200,
			InputTokens: 5, CostMicros: 10, Priced: true},
		{Time: now, APIKeyID: "k1", APIKeyName: "key", ProviderID: "p1", Model: "m1", Status: 200,
			InputTokens: 7},
	}, now)
	if got := snapshot.Total.CostMicros; got != 50 {
		t.Fatalf("today total cost: got %d", got)
	}
	if got := snapshot.Total.CacheWriteTokens; got != 3 {
		t.Fatalf("today cache writes: got %d", got)
	}
	if snapshot.Total.RequestCount != 3 || snapshot.Total.PricedCount != 2 {
		t.Fatalf("today coverage: priced %d of %d, want 2 of 3", snapshot.Total.PricedCount, snapshot.Total.RequestCount)
	}
	if len(snapshot.ByAPIKey) != 1 || snapshot.ByAPIKey[0].CostMicros != 50 {
		t.Fatalf("per-key cost: %#v", snapshot.ByAPIKey)
	}
	if len(snapshot.ByProvider) != 1 || snapshot.ByProvider[0].CostMicros != 50 {
		t.Fatalf("per-provider cost: %#v", snapshot.ByProvider)
	}
	if got := snapshot.ByProvider[0].PricedCount; got != 2 {
		t.Fatalf("per-provider priced count: got %d, want 2", got)
	}
}

// TestUsageByProviderModelAndKeyIsolation 验证 Provider×Model 联合维度在总览、
// 区间、按 Key 隔离视图、别名归并及日志回退重算路径上的准确性。
func TestUsageByProviderModelAndKeyIsolation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1)
	store := NewStore()
	events := []UsageEvent{
		{Time: yesterday, APIKeyID: "k1", APIKeyName: "key-1", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", Status: 200, InputTokens: 1000, OutputTokens: 200, CostMicros: 5000, Priced: true, LatencyMs: 30},
		{Time: now, APIKeyID: "k1", APIKeyName: "key-1", UserID: "u1", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", Status: 200, InputTokens: 500, OutputTokens: 100, CostMicros: 2500, Priced: true, LatencyMs: 20},
		{Time: now, APIKeyID: "k1", APIKeyName: "key-1", UserID: "u1", ProviderID: "p2", Model: "m1",
			OutputProtocol: "OpenAI Chat", Status: 200, InputTokens: 300, OutputTokens: 50, CostMicros: 0, Priced: false, LatencyMs: 20},
		{Time: now, APIKeyID: "k2", APIKeyName: "key-2", UserID: "u2", ProviderID: "p1", Model: "m2",
			OutputProtocol: "Claude", Status: 200, InputTokens: 400, OutputTokens: 100, CostMicros: 9000, Priced: true, LatencyMs: 20},
	}
	for _, ev := range events {
		store.ApplyUsageEventSync(ev)
	}

	full := store.UsageStatsRange(now, yesterday, now.Add(24*time.Hour))
	if full.Range == nil || len(full.Range.ByProviderModel) != 3 {
		t.Fatalf("expected 3 provider_model rows, got %#v", full.Range)
	}
	// 默认按成本降序：p1/m2(9000) -> p1/m1(7500) -> p2/m1(0)
	if full.Range.ByProviderModel[0].ProviderID != "p1" || full.Range.ByProviderModel[0].Model != "m2" || full.Range.ByProviderModel[0].CostMicros != 9000 {
		t.Fatalf("unexpected top provider_model row: %#v", full.Range.ByProviderModel[0])
	}
	if full.Range.ByProviderModel[1].ProviderID != "p1" || full.Range.ByProviderModel[1].Model != "m1" || full.Range.ByProviderModel[1].CostMicros != 7500 || full.Range.ByProviderModel[1].PricedCount != 2 {
		t.Fatalf("unexpected second provider_model row: %#v", full.Range.ByProviderModel[1])
	}
	if full.Range.ByProviderModel[2].ProviderID != "p2" || full.Range.ByProviderModel[2].Model != "m1" || full.Range.ByProviderModel[2].PricedCount != 0 {
		t.Fatalf("unexpected unpriced provider_model row: %#v", full.Range.ByProviderModel[2])
	}

	// 普通用户仅可见 k1：不能混入 k2 的 p1/m2。
	k1Range := store.UsageStatsRangeForKeys(now, yesterday, now.Add(24*time.Hour), []string{"k1"})
	if k1Range.Range == nil || len(k1Range.Range.ByProviderModel) != 2 {
		t.Fatalf("expected 2 provider_model rows for k1, got %#v", k1Range.Range)
	}
	for _, row := range k1Range.Range.ByProviderModel {
		if row.Model == "m2" {
			t.Fatalf("k1 range leaked k2 model m2: %#v", row)
		}
	}

	// 别名归并只能合并同一 Provider 下，绝不能跨 Provider 合并。
	merged := MergeProviderModelDayStats([]ProviderModelDayStats{
		{ProviderID: "p1", Model: "qoder-cn/dfmodel", RequestCount: 2, InputTokens: 100, CostMicros: 200, PricedCount: 2},
		{ProviderID: "p1", Model: "dfmodel", RequestCount: 1, InputTokens: 50, CostMicros: 100, PricedCount: 1},
		{ProviderID: "p2", Model: "dfmodel", RequestCount: 3, InputTokens: 300, CostMicros: 900, PricedCount: 3},
	}, func(m string) string {
		if m == "qoder-cn/dfmodel" {
			return "dfmodel"
		}
		return m
	})
	if len(merged) != 2 {
		t.Fatalf("expected 2 rows after per-provider canonical merge, got %#v", merged)
	}
	if merged[0].ProviderID != "p2" || merged[0].CostMicros != 900 || merged[1].ProviderID != "p1" || merged[1].RequestCount != 3 || merged[1].CostMicros != 300 {
		t.Fatalf("unexpected merged rows: %#v", merged)
	}

	// 日志回退聚合也要返回同样的 ByProviderModel。
	fromLogs := AggregateLogsInRange([]RequestLog{
		{Time: now, ProviderID: "p1", Model: "m1", InputTokens: 10, OutputTokens: 5, CostMicros: 42, Priced: true},
		{Time: now, ProviderID: "p2", Model: "m1", InputTokens: 20, OutputTokens: 5, Priced: false},
	}, yesterday, now.Add(24*time.Hour), "test")
	if len(fromLogs.ByProviderModel) != 2 || fromLogs.ByProviderModel[0].CostMicros != 42 {
		t.Fatalf("AggregateLogsInRange missing ByProviderModel: %#v", fromLogs.ByProviderModel)
	}
}
