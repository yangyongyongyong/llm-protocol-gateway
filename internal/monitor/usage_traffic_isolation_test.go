package monitor

import (
	"testing"
	"time"
)

// The per-day traffic series must be scoped to the caller's own API keys.
// A leak here would show one tenant another tenant's byte volumes.
func TestDailyTrafficIsolatedPerUserKeys(t *testing.T) {
	store := NewStore()
	now := time.Now()
	day := now.Local().Format("2006-01-02")

	// Two tenants, very different traffic volumes.
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, APIKeyID: "key-mine", APIKeyName: "mine",
		Status: 200, RxBytes: 1000, TxBytes: 100,
	})
	store.ApplyUsageEventSync(UsageEvent{
		Time: now, APIKeyID: "key-other", APIKeyName: "other",
		Status: 200, RxBytes: 9_000_000, TxBytes: 800_000,
	})

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.Add(24 * time.Hour)

	mine := store.UsageStatsRangeForKeys(now, from, to, []string{"key-mine"})
	var mineDay *DailyRequestPoint
	for i := range mine.Daily {
		if mine.Daily[i].Date == day {
			mineDay = &mine.Daily[i]
		}
	}
	if mineDay == nil {
		t.Fatalf("missing today in per-user daily: %+v", mine.Daily)
	}
	if mineDay.RxBytes != 1000 || mineDay.TxBytes != 100 {
		t.Fatalf("per-user traffic leaked or wrong: rx=%d tx=%d (want 1000/100)", mineDay.RxBytes, mineDay.TxBytes)
	}

	// Admin view aggregates everyone.
	all := store.UsageStatsRange(now, from, to)
	var allDay *DailyRequestPoint
	for i := range all.Daily {
		if all.Daily[i].Date == day {
			allDay = &all.Daily[i]
		}
	}
	if allDay == nil {
		t.Fatal("missing today in admin daily")
	}
	if allDay.RxBytes != 9_001_000 || allDay.TxBytes != 800_100 {
		t.Fatalf("admin totals wrong: rx=%d tx=%d (want 9001000/800100)", allDay.RxBytes, allDay.TxBytes)
	}

	// An empty key set (user owning no keys) must see zero, not everything.
	none := store.UsageStatsRangeForKeys(now, from, to, nil)
	for _, p := range none.Daily {
		if p.RxBytes != 0 || p.TxBytes != 0 {
			t.Fatalf("user with no keys must see zero traffic, got %+v", p)
		}
	}
}
