package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/store"
)

// TestDownstreamGatewayUsagePersistsAndFiltersByKey 验证下游网关维度会持久化，
// 且普通用户按 Key 过滤时不会看到别人 Key 产生的下游流量。
func TestDownstreamGatewayUsagePersistsAndFiltersByKey(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, now.Location()).AddDate(0, 0, -40)
	logs := monitor.NewStore()
	logs.SetUsageDailyStore(db)
	for _, event := range []monitor.UsageEvent{
		{Time: at, APIKeyID: "key-a", APIKeyName: "Key A", UserID: "peer-a", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 100, OutputTokens: 10, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
		{Time: at.Add(time.Second), APIKeyID: "key-b", APIKeyName: "Key B", UserID: "peer-b", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 200, OutputTokens: 20, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
		{Time: at.Add(2 * time.Second), APIKeyID: "key-a", APIKeyName: "Key A", UserID: "peer-a", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 300, OutputTokens: 30, PeerGatewayID: "gw_upstream", PeerDirection: "egress"},
	} {
		logs.ApplyUsageEventSync(event)
	}

	days, last, err := db.LoadUsageSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	dayKey := at.Format("2006-01-02")
	day := days[dayKey]
	if day.ByDownstreamGateway["gw_down_a"].RequestCount != 2 {
		t.Fatalf("global downstream bucket not persisted: %+v", day.ByDownstreamGateway)
	}
	if day.ByKey["key-a"].ByDownstreamGateway["gw_down_a"].InputTokens != 100 {
		t.Fatalf("per-key downstream bucket not persisted: %+v", day.ByKey["key-a"].ByDownstreamGateway)
	}
	if _, ok := day.ByDownstreamGateway["gw_upstream"]; ok {
		t.Fatalf("egress traffic leaked into downstream bucket: %+v", day.ByDownstreamGateway)
	}

	restarted := monitor.NewStore()
	restarted.BootstrapUsageDays(days, last)
	from := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	admin := restarted.UsageStatsRange(now, from, from.AddDate(0, 0, 1))
	if admin.Range == nil || len(admin.Range.ByDownstreamGateway) != 1 || admin.Range.ByDownstreamGateway[0].GatewayID != "gw_down_a" || admin.Range.ByDownstreamGateway[0].RequestCount != 2 {
		t.Fatalf("admin downstream stats lost after restart: %+v", admin.Range)
	}
	user := restarted.UsageStatsRangeForKeys(now, from, from.AddDate(0, 0, 1), []string{"key-a"})
	if user.Range == nil || len(user.Range.ByDownstreamGateway) != 1 || user.Range.ByDownstreamGateway[0].RequestCount != 1 || user.Range.ByDownstreamGateway[0].InputTokens != 100 {
		t.Fatalf("per-user downstream stats leaked or lost: %+v", user.Range)
	}
}

// TestUsageSchemaV4BackfillsDownstreamGatewayFromLogs 模拟 v3 数据库升级：
// 永久总量保持不变，但下游网关维度会从保留的请求日志中回放出来。
func TestUsageSchemaV4BackfillsDownstreamGatewayFromLogs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	at := now.Add(-time.Hour)
	logs := monitor.NewStore()
	logs.SetUsageDailyStore(db)
	for _, event := range []monitor.UsageEvent{
		{Time: at, APIKeyID: "key-a", APIKeyName: "Key A", UserID: "peer-a", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 10, OutputTokens: 1},
		{Time: at.Add(time.Second), APIKeyID: "key-b", APIKeyName: "Key B", UserID: "peer-b", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 20, OutputTokens: 2},
	} {
		logs.ApplyUsageEventSync(event)
	}
	if err := db.AppendRequestLogs([]monitor.RequestLog{
		{Time: at, APIKeyID: "key-a", APIKeyName: "Key A", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 10, OutputTokens: 1, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
		{Time: at.Add(time.Second), APIKeyID: "key-b", APIKeyName: "Key B", ProviderID: "provider", Model: "model", Status: 200, InputTokens: 20, OutputTokens: 2, PeerGatewayID: "gw_down_a", PeerDirection: "ingress"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(usageBucketSchemaSettingKey, "3"); err != nil {
		t.Fatal(err)
	}

	server := NewServer(NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "key-a", Name: "Key A", OwnerUserID: "peer-a", Enabled: true},
		{ID: "key-b", Name: "Key B", OwnerUserID: "peer-b", Enabled: true},
	}}), monitor.NewStore(), db)
	server.RebuildUsageStats()

	from := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	snapshot := server.logs.UsageStatsRange(now, from, from.AddDate(0, 0, 1))
	if snapshot.Range == nil || snapshot.Range.Total.RequestCount != 2 {
		t.Fatalf("schema backfill changed permanent total: %+v", snapshot.Range)
	}
	if len(snapshot.Range.ByDownstreamGateway) != 1 {
		t.Fatalf("downstream dimension not backfilled: %+v", snapshot.Range)
	}
	got := snapshot.Range.ByDownstreamGateway[0]
	if got.GatewayID != "gw_down_a" || got.RequestCount != 2 || got.InputTokens != 30 || got.OutputTokens != 3 {
		t.Fatalf("unexpected backfilled downstream stats: %+v", got)
	}
	if version := db.Setting(usageBucketSchemaSettingKey); version != usageBucketSchemaVersion {
		t.Fatalf("schema version = %q, want %q", version, usageBucketSchemaVersion)
	}
}

// TestFillUsageDownstreamGatewayNames 验证下游用量会用 peer 账号的当前显示名
// 展示，未知或已删除网关保留稳定 ID，不把名称写死进历史聚合。
func TestFillUsageDownstreamGatewayNames(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.userStore = newMemoryUserStore(domain.User{
		ID: "peer-a", Username: "sub-gw-a", DisplayName: "上海研发网关", Kind: domain.UserKindPeer,
		GatewayID: "gw_down_a", Enabled: true,
	})
	snapshot := &monitor.UsageStatsSnapshot{
		Today: monitor.TodayStatsSnapshot{ByDownstreamGateway: []monitor.DownstreamGatewayDayStats{
			{GatewayID: "gw_down_a", RequestCount: 1},
			{GatewayID: "gw_deleted", RequestCount: 2},
		}},
		Month: monitor.PeriodStatsSnapshot{ByDownstreamGateway: []monitor.DownstreamGatewayDayStats{{GatewayID: "gw_down_a", RequestCount: 3}}},
		Range: &monitor.PeriodStatsSnapshot{ByDownstreamGateway: []monitor.DownstreamGatewayDayStats{{GatewayID: "gw_down_a", RequestCount: 4}}},
	}
	server.fillUsageDownstreamGatewayNames(snapshot)
	if got := snapshot.Today.ByDownstreamGateway[0].GatewayName; got != "上海研发网关" {
		t.Fatalf("today gateway name = %q", got)
	}
	if got := snapshot.Today.ByDownstreamGateway[1].GatewayName; got != "" {
		t.Fatalf("deleted gateway name = %q, want empty fallback", got)
	}
	if snapshot.Month.ByDownstreamGateway[0].GatewayName != "上海研发网关" || snapshot.Range.ByDownstreamGateway[0].GatewayName != "上海研发网关" {
		t.Fatalf("month/range names not filled: month=%+v range=%+v", snapshot.Month.ByDownstreamGateway, snapshot.Range.ByDownstreamGateway)
	}
}

// TestUserRequestStatsIncludesOwnedDownstreamGateway 验证普通用户的用量页会
// 带上归属给自己的下游网关，但不会看到未归属或其他人的下游流量。
func TestUserRequestStatsIncludesOwnedDownstreamGateway(t *testing.T) {
	now := time.Now().Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	logs := monitor.NewStore()
	logs.ApplyUsageEventSync(monitor.UsageEvent{
		Time: now, APIKeyID: "owned-peer-key", APIKeyName: "Owned peer", UserID: "peer-owned",
		ProviderID: "provider", Model: "model", Status: 200, InputTokens: 120, OutputTokens: 30,
		PeerGatewayID: "gw_owned", PeerDirection: "ingress",
	})
	logs.ApplyUsageEventSync(monitor.UsageEvent{
		Time: now, APIKeyID: "unowned-peer-key", APIKeyName: "Unowned peer", UserID: "peer-unowned",
		ProviderID: "provider", Model: "model", Status: 200, InputTokens: 500, OutputTokens: 50,
		PeerGatewayID: "gw_unowned", PeerDirection: "ingress",
	})
	server := NewServer(NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "owned-peer-key", Name: "Owned peer", OwnerUserID: "peer-owned", Enabled: true},
		{ID: "unowned-peer-key", Name: "Unowned peer", OwnerUserID: "peer-unowned", Enabled: true},
	}}), logs)
	server.userStore = newMemoryUserStore(
		domain.User{ID: "peer-owned", Username: "owned-peer", Kind: domain.UserKindPeer, GatewayID: "gw_owned", OwnerUserID: "owner-a", Enabled: true},
		domain.User{ID: "peer-unowned", Username: "unowned-peer", Kind: domain.UserKindPeer, GatewayID: "gw_unowned", Enabled: true},
	)
	req := httptest.NewRequest(http.MethodGet, "/__request-stats?from="+today.Format("2006-01-02")+"&to="+today.Format("2006-01-02"), nil)
	req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, sessionIdentity{UserID: "owner-a", Role: domain.UserRoleUser}))
	rec := httptest.NewRecorder()
	server.handleRequestStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var snapshot monitor.UsageStatsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Today.ByDownstreamGateway) != 1 || snapshot.Today.ByDownstreamGateway[0].GatewayID != "gw_owned" {
		t.Fatalf("owned downstream not isolated correctly: %+v", snapshot.Today.ByDownstreamGateway)
	}
	if snapshot.Range == nil || len(snapshot.Range.ByDownstreamGateway) != 1 || snapshot.Range.ByDownstreamGateway[0].InputTokens != 120 {
		t.Fatalf("range downstream not isolated correctly: %+v", snapshot.Range)
	}
	if snapshot.Today.ByDownstreamGateway[0].GatewayName != "owned-peer" {
		t.Fatalf("gateway display name = %q", snapshot.Today.ByDownstreamGateway[0].GatewayName)
	}
}
