package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func newAlertTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendLog(t *testing.T, s *Store, when time.Time, keyID, keyName, ip string) {
	t.Helper()
	if err := s.AppendRequestLog(monitor.RequestLog{
		Time:       when,
		APIKeyID:   keyID,
		APIKeyName: keyName,
		ClientIP:   ip,
		Status:     200,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMultiIPKeysHitsAboveThreshold(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		appendLog(t, s, now, "leaked", "泄露的密钥", ip)
	}

	hits, err := s.DetectMultiIPKeys(now.Add(-10*time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].APIKeyID != "leaked" || hits[0].IPCount != 3 || hits[0].RequestCount != 3 {
		t.Fatalf("unexpected hit: %+v", hits[0])
	}
	if hits[0].APIKeyName != "泄露的密钥" {
		t.Fatalf("expected key name to be carried, got %q", hits[0].APIKeyName)
	}
	if len(hits[0].IPs) != 3 {
		t.Fatalf("expected 3 IPs, got %v", hits[0].IPs)
	}
}

func TestDetectMultiIPKeysBelowThreshold(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()
	// Same key, same IP, many requests: normal heavy use, not a leak.
	for i := 0; i < 5; i++ {
		appendLog(t, s, now, "normal", "正常密钥", "1.1.1.1")
	}

	hits, err := s.DetectMultiIPKeys(now.Add(-10*time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
}

// Unauthenticated traffic and console route tests carry no api_key_id. Folding
// them together would synthesize a bogus shared key, so they must be excluded.
func TestDetectMultiIPKeysExcludesEmptyKeyAndIP(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		appendLog(t, s, now, "", "Route Test", ip)
	}
	// A real key whose rows carry no client IP must not be flagged either.
	for i := 0; i < 4; i++ {
		appendLog(t, s, now, "noip", "无 IP 密钥", "")
	}

	hits, err := s.DetectMultiIPKeys(now.Add(-10*time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
}

func TestDetectMultiIPKeysIgnoresRowsOutsideWindow(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	appendLog(t, s, old, "k1", "key", "1.1.1.1")
	appendLog(t, s, old, "k1", "key", "2.2.2.2")
	appendLog(t, s, now, "k1", "key", "3.3.3.3")

	hits, err := s.DetectMultiIPKeys(now.Add(-10*time.Minute), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("only one IP is inside the window; expected no hits, got %+v", hits)
	}
}

func TestAlertRoundTripAndStatusFilter(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()

	id, err := s.InsertAlert(monitor.Alert{
		Time: now, Rule: "apikey_multi_ip", Severity: "warn",
		APIKeyID: "k1", APIKeyName: "main",
		IPs: []string{"1.1.1.1", "2.2.2.2"}, IPCount: 2,
		WindowMinutes: 10, RequestCount: 7, Status: "unread",
	})
	if err != nil {
		t.Fatal(err)
	}

	alert, found, err := s.AlertByID(id)
	if err != nil || !found {
		t.Fatalf("AlertByID failed: err=%v found=%v", err, found)
	}
	if len(alert.IPs) != 2 || alert.IPs[0] != "1.1.1.1" {
		t.Fatalf("IPs did not survive the round trip: %v", alert.IPs)
	}
	if alert.IPCount != 2 || alert.RequestCount != 7 || alert.WindowMinutes != 10 {
		t.Fatalf("unexpected alert: %+v", alert)
	}

	if err := s.UpdateAlertStatus(id, "ignored"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAlertPush(id, "failed", "boom"); err != nil {
		t.Fatal(err)
	}

	page, err := s.QueryAlerts(monitor.AlertQuery{Status: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected 1 ignored alert, got total=%d items=%d", page.Total, len(page.Items))
	}
	if page.Items[0].PushStatus != "failed" || page.Items[0].PushError != "boom" {
		t.Fatalf("push state not persisted: %+v", page.Items[0])
	}
	if page.Counts.All != 1 || page.Counts.Ignored != 1 || page.Counts.Unread != 0 {
		t.Fatalf("unexpected counts: %+v", page.Counts)
	}

	// Filtering by a different status must return nothing but keep global counts.
	unread, err := s.QueryAlerts(monitor.AlertQuery{Status: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	if unread.Total != 0 {
		t.Fatalf("expected 0 unread, got %d", unread.Total)
	}
	if unread.Counts.All != 1 {
		t.Fatalf("counts must be unfiltered, got %+v", unread.Counts)
	}

	if err := s.UpdateAlertStatus(id, "bogus"); err == nil {
		t.Fatal("expected invalid status to be rejected")
	}
}

func TestLatestAlertForKeyPicksNewest(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	now := time.Now()

	if _, err := s.InsertAlert(monitor.Alert{
		Time: now.Add(-time.Hour), Rule: "apikey_multi_ip", APIKeyID: "k1",
		IPs: []string{"1.1.1.1"}, IPCount: 1, Status: "unread",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertAlert(monitor.Alert{
		Time: now, Rule: "apikey_multi_ip", APIKeyID: "k1",
		IPs: []string{"9.9.9.9"}, IPCount: 1, Status: "unread",
	}); err != nil {
		t.Fatal(err)
	}

	latest, found, err := s.LatestAlertForKey("apikey_multi_ip", "k1")
	if err != nil || !found {
		t.Fatalf("LatestAlertForKey failed: err=%v found=%v", err, found)
	}
	if len(latest.IPs) != 1 || latest.IPs[0] != "9.9.9.9" {
		t.Fatalf("expected newest alert, got %v", latest.IPs)
	}

	if _, found, err := s.LatestAlertForKey("apikey_multi_ip", "never-alerted"); err != nil || found {
		t.Fatalf("expected no alert for unknown key: err=%v found=%v", err, found)
	}
}

// Alerts must serialize with a non-null ips array so the console can call
// .join() without a null guard.
func TestAlertIPsSerializeAsArrayWhenEmpty(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	id, err := s.InsertAlert(monitor.Alert{
		Time: time.Now(), Rule: "apikey_multi_ip", APIKeyID: "k1", Status: "unread",
	})
	if err != nil {
		t.Fatal(err)
	}
	alert, _, err := s.AlertByID(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(alert)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ips":[]`) {
		t.Fatalf("expected empty ips array in %s", raw)
	}
}
