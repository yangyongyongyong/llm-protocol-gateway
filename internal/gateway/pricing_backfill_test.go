package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestHandleBackfillPricesAndStatsRefresh 验证：
// 1) POST /__prices/backfill 仅允许管理员调用；
// 2) 未计价的历史请求在配置单价并触发补算后，request_logs、usage_daily_buckets、内存统计桶与统计缓存同步更新；
// 3) 已计价的请求在后续改价并再次补算时绝不被重写。
func TestHandleBackfillPricesAndStatsRefresh(t *testing.T) {
	server, router, db := pricedServer(t)
	key := router.State().APIKeys[0]
	now := time.Now()
	today := now.Local().Format("2006-01-02")

	// 先在未配置价格时记录 1 条请求：priced 应为 false，cost_micros=0。
	server.RecordRequestLogForTest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), now, key, "r1", "p1", "gpt-x", "/v1/chat/completions", http.StatusOK, TokenUsage{
		InputTokens:  1_000_000,
		OutputTokens: 500_000,
	})
	server.FlushRequestLogs()
	if err := server.logs.FlushUsage(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 非本地管理员（无登录 Cookie 且非 loopback）应被拒绝。
	unauthReq := httptest.NewRequest(http.MethodPost, "/__prices/backfill", strings.NewReader(`{}`))
	unauthReq.Host = "gw.example.com"
	unauthReq.RemoteAddr = "203.0.113.10:12345"
	unauthRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized && unauthRec.Code != http.StatusForbidden {
		t.Fatalf("expected 401/403 for unauthenticated backfill, got %d", unauthRec.Code)
	}

	// 先拉一次 /__request-stats 填充 5s 缓存，确认此时未计价且已预聚合出 byProviderModel。
	statsReq1 := asLocalAdmin(httptest.NewRequest(http.MethodGet, "/__request-stats?from="+today+"&to="+today, nil))
	statsRec1 := httptest.NewRecorder()
	server.Handler().ServeHTTP(statsRec1, statsReq1)
	if statsRec1.Code != http.StatusOK {
		t.Fatalf("stats 1 failed: %d %s", statsRec1.Code, statsRec1.Body.String())
	}
	var snap1 monitor.UsageStatsSnapshot
	if err := json.Unmarshal(statsRec1.Body.Bytes(), &snap1); err != nil {
		t.Fatal(err)
	}
	if snap1.Range == nil || snap1.Range.Total.PricedCount != 0 || len(snap1.Range.ByProviderModel) != 1 {
		t.Fatalf("expected 1 unpriced byProviderModel before backfill, got %#v", snap1.Range)
	}

	// 保存全局单价：输入 $2/MTok，输出 $6/MTok -> 1M 输入 + 0.5M 输出 = $5 = 5,000,000 微美元。
	putPrice(t, server, domain.ModelPrice{
		ProviderID:       "*",
		ModelID:          "gpt-x",
		InputUSDPerMTok:  2,
		OutputUSDPerMTok: 6,
	})

	// 执行补算。
	bfReq := asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices/backfill", strings.NewReader(`{"from":"`+today+`","to":"`+today+`"}`)))
	bfRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bfRec, bfReq)
	if bfRec.Code != http.StatusOK {
		t.Fatalf("backfill failed: %d %s", bfRec.Code, bfRec.Body.String())
	}
	var bfResp struct {
		Updated    int64  `json:"updated"`
		CostMicros int64  `json:"costMicros"`
		From       string `json:"from"`
		To         string `json:"to"`
	}
	if err := json.Unmarshal(bfRec.Body.Bytes(), &bfResp); err != nil {
		t.Fatal(err)
	}
	if bfResp.Updated != 1 || bfResp.CostMicros != 5_000_000 {
		t.Fatalf("unexpected backfill response: %+v", bfResp)
	}

	// 立即再次查询 /__request-stats，应已失效缓存并展示最新费用。
	statsReq2 := asLocalAdmin(httptest.NewRequest(http.MethodGet, "/__request-stats?from="+today+"&to="+today, nil))
	statsRec2 := httptest.NewRecorder()
	server.Handler().ServeHTTP(statsRec2, statsReq2)
	if statsRec2.Code != http.StatusOK {
		t.Fatalf("stats 2 failed: %d", statsRec2.Code)
	}
	var snap2 monitor.UsageStatsSnapshot
	if err := json.Unmarshal(statsRec2.Body.Bytes(), &snap2); err != nil {
		t.Fatal(err)
	}
	if snap2.Range == nil || snap2.Range.Total.CostMicros != 5_000_000 || snap2.Range.Total.PricedCount != 1 {
		t.Fatalf("expected total cost 5000000 after backfill, got %#v", snap2.Range)
	}
	if len(snap2.Range.ByProviderModel) != 1 || snap2.Range.ByProviderModel[0].CostMicros != 5_000_000 || snap2.Range.ByProviderModel[0].PricedCount != 1 {
		t.Fatalf("expected byProviderModel cost 5000000 after backfill, got %#v", snap2.Range.ByProviderModel)
	}

	// 改价后再次补算：之前已 priced=1 的记录绝对不能被重写。
	putPrice(t, server, domain.ModelPrice{
		ProviderID:       "*",
		ModelID:          "gpt-x",
		InputUSDPerMTok:  100,
		OutputUSDPerMTok: 100,
	})
	bfReq2 := asLocalAdmin(httptest.NewRequest(http.MethodPost, "/__prices/backfill", strings.NewReader(`{}`)))
	bfRec2 := httptest.NewRecorder()
	server.Handler().ServeHTTP(bfRec2, bfReq2)
	if bfRec2.Code != http.StatusOK {
		t.Fatalf("second backfill failed: %d", bfRec2.Code)
	}
	var bfResp2 struct {
		Updated    int64 `json:"updated"`
		CostMicros int64 `json:"costMicros"`
	}
	if err := json.Unmarshal(bfRec2.Body.Bytes(), &bfResp2); err != nil {
		t.Fatal(err)
	}
	if bfResp2.Updated != 0 || bfResp2.CostMicros != 0 {
		t.Fatalf("already-priced row must not be overwritten, got %+v", bfResp2)
	}
	_ = db
}

// TestNormalizeUsageModelIdentitiesFoldsByProviderModel 验证同一 Provider 下的别名模型会归并到一起。
func TestNormalizeUsageModelIdentitiesFoldsByProviderModel(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID:     "p1",
		Models: []domain.Model{{ID: "dfmodel"}},
	}}})
	server := NewServer(router, monitor.NewStore(), nil)
	snapshot := &monitor.UsageStatsSnapshot{
		Range: &monitor.PeriodStatsSnapshot{
			ByProviderModel: []monitor.ProviderModelDayStats{
				{ProviderID: "p1", Model: "qoder-cn/dfmodel", RequestCount: 2, CostMicros: 300, PricedCount: 2},
				{ProviderID: "p1", Model: "dfmodel", RequestCount: 1, CostMicros: 150, PricedCount: 1},
				{ProviderID: "p2", Model: "dfmodel", RequestCount: 4, CostMicros: 800, PricedCount: 4},
			},
		},
	}
	server.normalizeUsageModelIdentities(snapshot)
	if len(snapshot.Range.ByProviderModel) != 2 {
		t.Fatalf("expected 2 provider_model rows after folding, got %#v", snapshot.Range.ByProviderModel)
	}
	if snapshot.Range.ByProviderModel[0].ProviderID != "p2" || snapshot.Range.ByProviderModel[1].ProviderID != "p1" || snapshot.Range.ByProviderModel[1].CostMicros != 450 {
		t.Fatalf("unexpected folded ByProviderModel: %#v", snapshot.Range.ByProviderModel)
	}
}
