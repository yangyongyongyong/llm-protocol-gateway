package gateway

import (
	"testing"
	"time"
)

// The floor takes a nearly-empty account out of rotation, but never empties the
// chain: if every account is below it, the order is left as the administrator
// wrote it.
func TestWorkBuddyPreferDrainedHoldsBackNearEmptyAccounts(t *testing.T) {
	now := time.Now()
	chain := []string{"a", "b", "c"}
	views := map[string]workBuddyCreditView{
		"a": {Known: true, Credits: 5, ExpiresAt: now.Add(time.Hour)},
		"b": {Known: true, Credits: 500, ExpiresAt: now.Add(2 * time.Hour)},
		"c": {Known: true, Credits: 400, ExpiresAt: now.Add(3 * time.Hour)},
	}
	got := workBuddyPreferDrained(chain, views, 10, now)
	if got[len(got)-1] != "a" {
		t.Fatalf("order = %v, want the account at the floor last", got)
	}
	if len(got) != 3 {
		t.Fatalf("order = %v, the chain lost an account", got)
	}

	// Every account below the floor: nothing is filtered, the order survives.
	allLow := map[string]workBuddyCreditView{
		"a": {Known: true, Credits: 1},
		"b": {Known: true, Credits: 2},
	}
	got = workBuddyPreferDrained(chain[:2], allLow, 10, now)
	if len(got) != 2 {
		t.Fatalf("order = %v, want the whole chain kept", got)
	}
}

// With the preference on, the account losing value soonest is asked first; without
// it, the richer account wins. Both are stable for ties.
func TestWorkBuddyPreferDrainedOrderingRules(t *testing.T) {
	now := time.Now()
	chain := []string{"soon-but-poor", "late-but-rich"}
	views := map[string]workBuddyCreditView{
		"soon-but-poor": {Known: true, Credits: 100, ExpiresAt: now.Add(time.Hour)},
		"late-but-rich": {Known: true, Credits: 900, ExpiresAt: now.Add(30 * 24 * time.Hour)},
	}

	t.Setenv(workBuddyPreferExpiringEnv, "1")
	if got := workBuddyPreferDrained(chain, views, 0, now); got[0] != "soon-but-poor" {
		t.Fatalf("order = %v, want the soonest-expiring first", got)
	}

	t.Setenv(workBuddyPreferExpiringEnv, "")
	if got := workBuddyPreferDrained(chain, views, 0, now); got[0] != "late-but-rich" {
		t.Fatalf("order = %v, want the richer account first when the preference is off", got)
	}
}

// An account with no cached figures is neither promoted nor demoted past the ones
// with figures, and keeps its position among the unknowns.
func TestWorkBuddyPreferDrainedLeavesUnknownAccountsAlone(t *testing.T) {
	now := time.Now()
	chain := []string{"unknown-1", "known", "unknown-2"}
	views := map[string]workBuddyCreditView{
		"known": {Known: true, Credits: 500, ExpiresAt: now.Add(time.Hour)},
	}
	got := workBuddyPreferDrained(chain, views, 0, now)
	if got[0] != "known" {
		t.Fatalf("order = %v, want the known account first", got)
	}
	if got[1] != "unknown-1" || got[2] != "unknown-2" {
		t.Fatalf("order = %v, unknown accounts lost their relative order", got)
	}
}

// A single-provider chain is returned untouched, and never nil.
func TestWorkBuddyPreferDrainedSingleProvider(t *testing.T) {
	got := workBuddyPreferDrained([]string{"only"}, nil, 100, time.Now())
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("got %v", got)
	}
}

// The view sums what the batches have left and takes the soonest end among the ones
// that still hold something.
func TestWorkBuddyCreditViewOfReadsCachedPackages(t *testing.T) {
	now := time.Now()
	workBuddyPackagesCache.Store("provider-view", workBuddyPackagesEntry{
		at: now,
		packages: []workBuddyPackage{
			{Remain: workBuddyNumberPtr(60), DeductionEndTime: workBuddyMillis(now.Add(2 * time.Hour))},
			{Remain: workBuddyNumberPtr(50), DeductionEndTime: workBuddyMillis(now.Add(30 * time.Minute))},
			// Fully spent: contributes nothing and must not set the expiry.
			{Remain: workBuddyNumberPtr(0), DeductionEndTime: workBuddyMillis(now.Add(-time.Hour))},
		},
	})
	view := workBuddyCreditViewOf("provider-view", now)
	if !view.Known {
		t.Fatal("the view is not known")
	}
	if view.Credits != 110 {
		t.Fatalf("credits = %v, want 110", view.Credits)
	}
	want := now.Add(30 * time.Minute).UTC().Truncate(time.Second)
	if !view.ExpiresAt.UTC().Truncate(time.Second).Equal(want) {
		t.Fatalf("expiry = %s, want %s", view.ExpiresAt.UTC(), want)
	}
}

// Stale cache is misleading, not useful: it is reported as unknown.
func TestWorkBuddyCreditViewOfIgnoresStaleCache(t *testing.T) {
	now := time.Now()
	workBuddyPackagesCache.Store("provider-stale", workBuddyPackagesEntry{
		at:       now.Add(-2 * workBuddyPackagesTTL),
		packages: []workBuddyPackage{{Remain: workBuddyNumberPtr(100)}},
	})
	if view := workBuddyCreditViewOf("provider-stale", now); view.Known {
		t.Fatalf("stale cache was used: %+v", view)
	}
	if view := workBuddyCreditViewOf("provider-absent", now); view.Known {
		t.Fatalf("absent provider reported as known: %+v", view)
	}
}

// The floor is a number or off; anything unparsable means off.
func TestWorkBuddyCreditFloorEnv(t *testing.T) {
	for _, raw := range []string{"", "0", "-5", "abc"} {
		t.Setenv(workBuddyCreditFloorEnv, raw)
		if got := workBuddyCreditFloor(); got != 0 {
			t.Fatalf("floor for %q = %v, want 0", raw, got)
		}
	}
	t.Setenv(workBuddyCreditFloorEnv, "12.5")
	if got := workBuddyCreditFloor(); got != 12.5 {
		t.Fatalf("floor = %v, want 12.5", got)
	}
}

// workBuddyMillis builds a package fixture's end time in the shape the meter sends:
// milliseconds since the epoch.
func workBuddyMillis(at time.Time) workBuddyNumber {
	return workBuddyNumber(at.UnixMilli())
}
