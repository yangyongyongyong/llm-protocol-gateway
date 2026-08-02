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

func appendLogWithLatency(t *testing.T, s *Store, when time.Time, keyID, ip string, latencyMS int64) {
	t.Helper()
	if err := s.AppendRequestLog(monitor.RequestLog{
		Time: when, APIKeyID: keyID, APIKeyName: keyID,
		ClientIP: ip, Status: 200, LatencyMillis: latencyMS,
	}); err != nil {
		t.Fatal(err)
	}
}

// Three IPs whose requests all overlap the same instant must be detected.
func TestDetectConcurrentIPKeysOverlapping(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	base := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	// All three are in flight for 10s starting within 1s of each other.
	appendLogWithLatency(t, s, base, "k1", "1.1.1.1", 10000)
	appendLogWithLatency(t, s, base.Add(300*time.Millisecond), "k1", "2.2.2.2", 10000)
	appendLogWithLatency(t, s, base.Add(600*time.Millisecond), "k1", "3.3.3.3", 10000)

	hits, err := s.DetectConcurrentIPKeys(base.Add(-time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d: %+v", len(hits), hits)
	}
	if hits[0].IPCount != 3 {
		t.Fatalf("expected peak concurrency 3, got %d", hits[0].IPCount)
	}
	if hits[0].At.IsZero() {
		t.Fatal("expected the overlap instant to be recorded")
	}
}

// Sequential requests from different IPs (one person switching networks) must
// NOT trip the overlap rule — this is the whole point of the rule.
func TestDetectConcurrentIPKeysSequentialNotFlagged(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)
	// Each finishes (100ms) long before the next begins (1 minute apart).
	appendLogWithLatency(t, s, base, "k1", "1.1.1.1", 100)
	appendLogWithLatency(t, s, base.Add(time.Minute), "k1", "2.2.2.2", 100)
	appendLogWithLatency(t, s, base.Add(2*time.Minute), "k1", "3.3.3.3", 100)

	hits, err := s.DetectConcurrentIPKeys(base.Add(-time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("sequential requests must not be flagged, got %+v", hits)
	}
}

// Only two of three overlap at any instant: below a threshold of 3.
func TestDetectConcurrentIPKeysBelowThreshold(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	base := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	appendLogWithLatency(t, s, base, "k1", "1.1.1.1", 1000)
	appendLogWithLatency(t, s, base.Add(500*time.Millisecond), "k1", "2.2.2.2", 1000)
	// Starts after the first two have finished.
	appendLogWithLatency(t, s, base.Add(5*time.Second), "k1", "3.3.3.3", 1000)

	hits, err := s.DetectConcurrentIPKeys(base.Add(-time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("peak concurrency is 2, expected no hits, got %+v", hits)
	}
	// Lowering the threshold to 2 should surface it.
	hits, err = s.DetectConcurrentIPKeys(base.Add(-time.Minute), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].IPCount != 2 {
		t.Fatalf("expected one hit with 2 concurrent IPs, got %+v", hits)
	}
}

// Many concurrent requests from a SINGLE IP are normal parallel tool calls.
func TestDetectConcurrentIPKeysSameIPNotFlagged(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	base := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	for i := 0; i < 6; i++ {
		appendLogWithLatency(t, s, base.Add(time.Duration(i)*10*time.Millisecond), "k1", "1.1.1.1", 10000)
	}
	hits, err := s.DetectConcurrentIPKeys(base.Add(-time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("single-IP concurrency must not be flagged, got %+v", hits)
	}
}

func TestDetectConcurrentIPKeysExcludesEmptyKeyAndIP(t *testing.T) {
	t.Parallel()
	s := newAlertTestStore(t)
	base := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	for i, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		appendLogWithLatency(t, s, base.Add(time.Duration(i)*time.Millisecond), "", ip, 10000)
	}
	for i := 0; i < 4; i++ {
		appendLogWithLatency(t, s, base.Add(time.Duration(i)*time.Millisecond), "noip", "", 10000)
	}
	hits, err := s.DetectConcurrentIPKeys(base.Add(-time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
}
