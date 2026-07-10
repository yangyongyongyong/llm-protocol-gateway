package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestUsageDailyApplyAndLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.Local)
	last := &monitor.RequestLog{
		Time:         now,
		APIKeyID:     "k1",
		APIKeyName:   "main",
		ProviderID:   "p1",
		Model:        "claude-opus-4-8",
		Status:       200,
		InputTokens:  100,
		OutputTokens: 10,
		CacheTokens:  40,
	}
	if err := s.ApplyUsageDelta(monitor.UsagePersistDelta{
		Day: "2026-07-10", KeyID: "k1", KeyName: "main", ProviderID: "p1", Model: "claude-opus-4-8",
		StatusClass: "2xx", InputTokens: 100, OutputTokens: 10, CacheTokens: 40, LatencyMs: 50, TTFTMs: 20,
		LastRequest: last,
	}); err != nil {
		t.Fatal(err)
	}

	days, loadedLast, err := s.LoadUsageSince(now.AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	day, ok := days["2026-07-10"]
	if !ok {
		t.Fatal("missing day bucket")
	}
	if day.Total.RequestCount != 1 || day.Total.InputTokens != 100 || day.Total.CacheTokens != 40 {
		t.Fatalf("total=%+v", day.Total)
	}
	if day.ByAPIKey["k1"].RequestCount != 1 {
		t.Fatalf("api key=%+v", day.ByAPIKey["k1"])
	}
	if day.ByModel["claude-opus-4-8"].RequestCount != 1 {
		t.Fatalf("model=%+v", day.ByModel["claude-opus-4-8"])
	}
	if day.Status2xx != 1 || day.LatencySum != 50 || day.TTFTCount != 1 {
		t.Fatalf("status/latency=%+v", day)
	}
	if loadedLast == nil || loadedLast.APIKeyID != "k1" {
		t.Fatalf("last=%+v", loadedLast)
	}
}

func TestUsageDailyPrune(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.ApplyUsageDelta(monitor.UsagePersistDelta{
		Day: "2026-07-01", KeyID: "k1", KeyName: "main", ProviderID: "p1",
		StatusClass: "2xx", InputTokens: 1, OutputTokens: 1,
	}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	if err := s.PruneUsageBefore(cutoff); err != nil {
		t.Fatal(err)
	}
	days, _, err := s.LoadUsageSince(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 0 {
		t.Fatalf("expected prune, got %+v", days)
	}
}
