package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitoring"
)

func insertLegacyAlert(t *testing.T, s *Store, when time.Time, rule, severity, keyID, keyName, status string) int64 {
	t.Helper()
	result, err := s.db.Exec(`INSERT INTO alerts
		(time, rule, severity, api_key_id, api_key_name, ips, ip_count, window_minutes,
		 request_count, status, push_status, push_error, concurrent_at)
		VALUES (?, ?, ?, ?, ?, '["1.1.1.1","2.2.2.2"]', 2, 10, 42, ?, 'sent', '', '')`,
		when.UTC().Format(time.RFC3339Nano), rule, severity, keyID, keyName, status)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.reader().QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMigrationImportsCheckinsAndAlertsIdempotently(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(`INSERT INTO checkins
		(provider_id, account_key, day, at, outcome, credit, streak, msg, valid_until, offline, tries)
		VALUES ('p1', 'site|uid', '2026-10-22', ?, 'ok', 12.5, 3, 'done', '', 0, 1)`,
		now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	legacyID := insertLegacyAlert(t, s, now.Add(-48*time.Hour), "apikey_multi_ip", "warn", "k1", "K1", "unread")

	for i := 0; i < 2; i++ {
		if err := s.MigrateMonitoringV1(); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM checkin_history`); got != 1 {
		t.Fatalf("checkin_history rows = %d, want 1", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_events`); got != 1 {
		t.Fatalf("monitor_events rows = %d, want 1", got)
	}
	var eventID int64
	var ruleKey, severity, status, reason, targetType, targetID, pushStatus, detailRaw string
	if err := s.reader().QueryRow(`SELECT id, rule_key, severity, status, resolution_reason,
			target_type, target_id, push_status, detail_json
		FROM monitor_events LIMIT 1`).Scan(&eventID, &ruleKey, &severity, &status, &reason,
		&targetType, &targetID, &pushStatus, &detailRaw); err != nil {
		t.Fatal(err)
	}
	if ruleKey != "multi_ip" || severity != "p1" || status != monitoring.StatusResolved {
		t.Fatalf("mapped event = rule %q severity %q status %q", ruleKey, severity, status)
	}
	if reason != monitoring.ResolutionLegacy || targetType != "api_key" || targetID != "k1" {
		t.Fatalf("event target/reason = %q/%q/%q", targetType, targetID, reason)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(detailRaw), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["legacyAlertId"] != float64(legacyID) || detail["legacyStatus"] != "unread" {
		t.Fatalf("detail = %#v", detail)
	}
	if _, ok := detail["legacyRule"]; ok {
		t.Fatalf("known rule must not carry legacyRule: %#v", detail)
	}
	if detail["ipCount"] != float64(2) {
		t.Fatalf("ipCount = %#v", detail["ipCount"])
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_alert_states`); got != 0 {
		t.Fatalf("legacy alerts must not create alert states, got %d", got)
	}
}

func TestMigrationCreatesLegacyTelegramChannelOnce(t *testing.T) {
	s := openMonitoringTestStore(t)
	settings := domain.DefaultAlertSettings()
	settings.Telegram.Enabled = true
	settings.Telegram.BotToken = "bot-token"
	settings.Telegram.ChatID = "chat-id"
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("alerts", string(raw)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.MigrateMonitoringV1(); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	channels := monitoring.NewConfig(s).Channels()
	count := 0
	for _, channel := range channels {
		if channel.ID != "legacy-telegram" {
			continue
		}
		count++
		if channel.Type != "telegram" || channel.Config.BotToken != "bot-token" || channel.Config.ChatID != "chat-id" {
			t.Fatalf("legacy channel = %+v", channel)
		}
		if !channel.Enabled || channel.MinSeverity != monitoring.SeverityP1 {
			t.Fatalf("legacy channel defaults = %+v", channel)
		}
	}
	if count != 1 {
		t.Fatalf("legacy-telegram channels = %d, want 1", count)
	}
}

func TestMigrationImportsAlertsIncrementally(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	insertLegacyAlert(t, s, now, "apikey_multi_ip", "warn", "k1", "K1", "unread")
	insertLegacyAlert(t, s, now, "apikey_concurrent_ip", "error", "k2", "K2", "read")
	if err := s.MigrateMonitoringV1(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_events`); got != 2 {
		t.Fatalf("events after first run = %d, want 2", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_alert_states`); got != 0 {
		t.Fatalf("alert states = %d, want 0", got)
	}
	newID := insertLegacyAlert(t, s, now.Add(time.Hour), "apikey_multi_ip", "warn", "k3", "K3", "ignored")
	if err := s.MigrateMonitoringV1(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_events`); got != 3 {
		t.Fatalf("events after second run = %d, want 3", got)
	}
	var concurrentSeverity string
	if err := s.reader().QueryRow(`SELECT severity FROM monitor_events WHERE target_id = 'k2'`).Scan(&concurrentSeverity); err != nil {
		t.Fatal(err)
	}
	if concurrentSeverity != "p0" {
		t.Fatalf("error severity mapped to %q, want p0", concurrentSeverity)
	}
	if marker := monitoring.NewConfig(s).MigrationMarker(); marker.LegacyMaxAlertID != newID {
		t.Fatalf("legacyMaxAlertId = %d, want %d", marker.LegacyMaxAlertID, newID)
	}
}

func TestSaveCheckinWritesHistory(t *testing.T) {
	s := openMonitoringTestStore(t)
	// ListCheckinHistory 只回看最近 N 天：固定日期会在 N 天后失效，
	// 用当前日期生成，保证测试不随时间过期。
	now := time.Now().UTC()
	if err := s.SaveCheckin(domain.CheckinRecord{
		ProviderID: "p1", AccountKey: "site|uid", Day: now.Format("2006-01-02"),
		At: now.Format(time.RFC3339),
		Outcome: "ok", Credit: 5, Streak: 2, Message: "done", Tries: 1,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := s.ListCheckinHistory("p1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Credit != 5 || history[0].Message != "done" {
		t.Fatalf("history = %+v", history)
	}
}

func TestMonitorRetentionDeletesOldRows(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	oldEvent, err := s.InsertMonitorEvent(monitoring.Event{
		Time: now.AddDate(0, 0, -91), Kind: monitoring.EventKindAlert, Severity: monitoring.SeverityP1,
		TargetType: "provider", TargetID: "p1", Title: "old", Status: monitoring.StatusResolved,
		FirstSeen: now.AddDate(0, 0, -91), LastSeen: now.AddDate(0, 0, -91),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMonitorEvent(monitoring.Event{
		Time: now.AddDate(0, 0, -89), Kind: monitoring.EventKindAlert, Severity: monitoring.SeverityP1,
		TargetType: "provider", TargetID: "p1", Title: "new", Status: monitoring.StatusResolved,
		FirstSeen: now.AddDate(0, 0, -89), LastSeen: now.AddDate(0, 0, -89),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMonitorEventPush(monitoring.EventPush{
		EventID: oldEvent, ChannelID: "ch1", Status: monitoring.PushSent, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProviderMetricSample(monitoring.MetricSample{
		ProviderID: "p1", Hour: now.AddDate(0, 0, -31).UTC().Truncate(time.Hour).Format(time.RFC3339Nano),
		ObservedAt: now.AddDate(0, 0, -31),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProviderMetricSample(monitoring.MetricSample{
		ProviderID: "p1", Hour: now.AddDate(0, 0, -29).UTC().Truncate(time.Hour).Format(time.RFC3339Nano),
		ObservedAt: now.AddDate(0, 0, -29),
	}); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-06-01", "2026-10-01"} {
		if _, err := s.db.Exec(`INSERT INTO checkin_history (provider_id, day, at) VALUES ('p1', ?, '')`, day); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneMonitoringData(now); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_events`); got != 1 {
		t.Fatalf("events after prune = %d, want 1", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM monitor_event_pushes`); got != 0 {
		t.Fatalf("orphan pushes after prune = %d, want 0", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM provider_metric_samples`); got != 1 {
		t.Fatalf("samples after prune = %d, want 1", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM checkin_history`); got != 1 {
		t.Fatalf("checkin history after prune = %d, want 1", got)
	}
}
