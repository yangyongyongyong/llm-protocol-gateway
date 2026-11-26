package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestUsageDailyProviderModelAndBackfill 验证 provider_model 与 key_provider_model
// 的持久化加载、Schema 升级回填及按当前价格补算未计价请求（且不覆盖已计价历史）。
func TestUsageDailyProviderModelAndBackfill(t *testing.T) {
	t.Parallel()
	db, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ts1 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.Local)
	ts2 := time.Date(2026, 11, 26, 11, 0, 0, 0, time.Local)
	dayKey := ts1.Local().Format("2006-01-02")

	// 插入两条请求日志：第一条已计价（cost=100），第二条未计价（priced=0）。
	if err := db.AppendRequestLogs([]monitor.RequestLog{
		{
			Time: ts1, APIKeyID: "k1", APIKeyName: "main", ProviderID: "p1", Model: "m1",
			ProtocolFlow: "OpenAI Chat", Status: 200, InputTokens: 1000, OutputTokens: 200,
			CostMicros: 100, Priced: true,
		},
		{
			Time: ts2, APIKeyID: "k1", APIKeyName: "main", ProviderID: "p1", Model: "m1",
			ProtocolFlow: "OpenAI Chat", Status: 200, InputTokens: 2000, OutputTokens: 500,
			CostMicros: 0, Priced: false,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 写入对应的日桶：第一条已计价（100），第二条未计价（0）。
	if err := db.ApplyUsageDeltas([]monitor.UsagePersistDelta{
		{
			Day: dayKey, KeyID: "k1", KeyName: "main", UserID: "admin", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", StatusClass: "2xx", InputTokens: 1000, OutputTokens: 200,
			CostMicros: 100, PricedCount: 1,
		},
		{
			Day: dayKey, KeyID: "k1", KeyName: "main", UserID: "admin", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", StatusClass: "2xx", InputTokens: 2000, OutputTokens: 500,
			CostMicros: 0, PricedCount: 0,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 验证 LoadUsageSince 能正确读回 provider_model 与 key_provider_model。
	days, _, err := db.LoadUsageSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	day := days[dayKey]
	pmKey := monitor.ProviderModelBucketKey("p1", "m1")
	if got := day.ByProviderModel[pmKey]; got.RequestCount != 2 || got.CostMicros != 100 || got.PricedCount != 1 {
		t.Fatalf("unexpected loaded provider_model: %+v", got)
	}
	if got := day.ByKey["k1"].ByProviderModel[pmKey]; got.RequestCount != 2 || got.CostMicros != 100 || got.PricedCount != 1 {
		t.Fatalf("unexpected loaded key_provider_model: %+v", got)
	}

	// 模拟按新价格补算区间内 priced=0 的请求（第二条应补算为 500，第一条保持 100 不变）。
	from := time.Date(2026, 11, 26, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 1)
	res, err := db.BackfillUnpricedCosts(from, to, func(row UnpricedRequestRow) PricedRequestResolution {
		if row.Model != "m1" {
			return PricedRequestResolution{Priced: false}
		}
		return PricedRequestResolution{
			Day:            row.Time.Local().Format("2006-01-02"),
			KeyID:          "k1",
			UserID:         "admin",
			ProviderID:     "p1",
			ModelBucket:    "m1",
			OutputProtocol: "OpenAI Chat",
			CostMicros:     500,
			Priced:         true,
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.CostMicros != 500 {
		t.Fatalf("unexpected backfill result: %+v", res)
	}

	// 再次补算应为 0（第一条已 priced=1，第二条刚更新为 priced=1）。
	res2, err := db.BackfillUnpricedCosts(from, to, func(row UnpricedRequestRow) PricedRequestResolution {
		return PricedRequestResolution{CostMicros: 9999, Priced: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Updated != 0 || res2.CostMicros != 0 {
		t.Fatalf("second backfill must be no-op, got %+v", res2)
	}

	// 重新加载日桶，验证各维度都精确累加了 500 与 priced_count=1（总计 600、2/2）。
	daysAfter, _, err := db.LoadUsageSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	after := daysAfter[dayKey]
	if after.Total.CostMicros != 600 || after.Total.PricedCount != 2 || after.Total.RequestCount != 2 {
		t.Fatalf("unexpected total after backfill: %+v", after.Total)
	}
	if got := after.ByProviderModel[pmKey]; got.CostMicros != 600 || got.PricedCount != 2 || got.RequestCount != 2 {
		t.Fatalf("unexpected provider_model after backfill: %+v", got)
	}
	if got := after.ByKey["k1"].ByProviderModel[pmKey]; got.CostMicros != 600 || got.PricedCount != 2 {
		t.Fatalf("unexpected key_provider_model after backfill: %+v", got)
	}

	// 验证 BackfillUsageKeyDimensions 能保留已有 total 并在缺失时重建 provider_model / key_provider_model。
	if _, err := db.db.Exec(`DELETE FROM usage_daily_buckets WHERE bucket_type IN ('provider_model', 'key_provider_model')`); err != nil {
		t.Fatal(err)
	}
	if err := db.BackfillUsageKeyDimensions(from, []monitor.UsagePersistDelta{
		{
			Day: dayKey, KeyID: "k1", KeyName: "main", UserID: "admin", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", StatusClass: "2xx", InputTokens: 1000, OutputTokens: 200,
			CostMicros: 100, PricedCount: 1,
		},
		{
			Day: dayKey, KeyID: "k1", KeyName: "main", UserID: "admin", ProviderID: "p1", Model: "m1",
			OutputProtocol: "OpenAI Chat", StatusClass: "2xx", InputTokens: 2000, OutputTokens: 500,
			CostMicros: 500, PricedCount: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	daysRebuilt, _, err := db.LoadUsageSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := daysRebuilt[dayKey].ByProviderModel[pmKey]; got.CostMicros != 600 || got.PricedCount != 2 {
		t.Fatalf("BackfillUsageKeyDimensions did not rebuild provider_model: %+v", got)
	}
	if got := daysRebuilt[dayKey].ByKey["k1"].ByProviderModel[pmKey]; got.CostMicros != 600 || got.PricedCount != 2 {
		t.Fatalf("BackfillUsageKeyDimensions did not rebuild key_provider_model: %+v", got)
	}
}
