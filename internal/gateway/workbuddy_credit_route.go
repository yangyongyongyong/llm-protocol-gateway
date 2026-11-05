package gateway

import (
	"os"
	"sort"
	"strings"
	"time"
)

// Draining accounts before they expire, and keeping the nearly-empty ones out of
// rotation.
//
// The reference implementation treats credits as a routing input, not just as a
// number on a card: an account whose credits expire soonest is preferred (a credit
// that expires unused is lost), and an account under a floor is not spent at all
// (`credit_floor`, `prefer_expiring` in misakano7545/workbuddy2api-panel
// internal/pool/pick.go).
//
// This gateway's routing unit is the provider and its modes are the
// administrator's (order / rotate / least_used / quota / spread), so the two are
// added as a **preference layer** applied after the mode has ordered the chain,
// never before: the mode still decides the shape, this decides who among equals is
// asked first.
//
// Both are off by default. That is deliberate: 59 providers serve live traffic
// here, and silently reordering which account answers a request is not something a
// release should do on its own. Enable with
//
//	WORKBUDDY_PREFER_EXPIRING=1    drain the soonest-expiring account first
//	WORKBUDDY_CREDIT_FLOOR=<n>     skip accounts with fewer than n credits left
//
// Only cached package data is consulted. The routing path must not spend an
// upstream call to decide where to send a request; an account with no cached
// figures keeps its position rather than being guessed at.

const (
	// workBuddyPreferExpiringEnv turns on the expiry preference.
	workBuddyPreferExpiringEnv = "WORKBUDDY_PREFER_EXPIRING"
	// workBuddyCreditFloorEnv is the number of credits below which an account is
	// skipped while another can serve the request.
	workBuddyCreditFloorEnv = "WORKBUDDY_CREDIT_FLOOR"
)

// workBuddyCreditView is what routing knows about one account's credits.
type workBuddyCreditView struct {
	// Known is false when no cached package data exists; such an account is never
	// reordered on credit grounds.
	Known     bool
	Credits   float64
	ExpiresAt time.Time
}

// workBuddyPreferExpiringEnabled reports whether the expiry preference is on.
func workBuddyPreferExpiringEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(workBuddyPreferExpiringEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// workBuddyCreditFloor reads the configured floor, or 0 (disabled).
func workBuddyCreditFloor() float64 {
	raw := strings.TrimSpace(os.Getenv(workBuddyCreditFloorEnv))
	if raw == "" {
		return 0
	}
	value, ok := workBuddyJSONNumber(raw)
	if !ok || value <= 0 {
		return 0
	}
	return value
}

// workBuddyCreditViewOf builds one account's view from the cached package list.
//
// "Credits left" is the sum of what the batches have left, which is the number the
// console already shows. The expiry is the soonest end among the batches that still
// have something left: the moment the account starts losing value.
func workBuddyCreditViewOf(providerID string, now time.Time) workBuddyCreditView {
	cached, ok := workBuddyPackagesCache.Load(providerID)
	if !ok {
		return workBuddyCreditView{}
	}
	entry, typed := cached.(workBuddyPackagesEntry)
	if !typed || len(entry.packages) == 0 {
		return workBuddyCreditView{}
	}
	if now.Sub(entry.at) > workBuddyPackagesTTL {
		// Stale enough to be misleading: treat as unknown rather than routing on
		// figures that may have moved.
		return workBuddyCreditView{}
	}
	view := workBuddyCreditView{Known: true}
	for _, batch := range entry.packages {
		remaining := batch.left()
		if remaining <= 0 {
			continue
		}
		view.Credits += remaining
		if ends := batch.endsAt(); !ends.IsZero() {
			if view.ExpiresAt.IsZero() || ends.Before(view.ExpiresAt) {
				view.ExpiresAt = ends
			}
		}
	}
	return view
}

// workBuddyPreferDrained reorders a chain by credit urgency.
//
// Rules, in order:
//
//  1. an account at or below the floor goes last, because spending it can only
//     produce refusals — unless every account qualifies, in which case the order
//     is left alone (a chain that refuses everything is still better tried than
//     skipped);
//  2. among the rest, an account whose credits expire sooner comes first;
//  3. accounts with no cached figures keep their relative position, and sort after
//     the ones with known figures — an unknown account is not evidence of a healthy
//     one, but neither is it evidence of an empty one.
//
// The sort is stable, so the administrator's ranking still decides ties.
func workBuddyPreferDrained(chain []string, views map[string]workBuddyCreditView, floor float64, now time.Time) []string {
	if len(chain) < 2 {
		return append([]string(nil), chain...)
	}
	preferExpiring := workBuddyPreferExpiringEnabled()

	belowFloor := make(map[string]bool, len(chain))
	anyAboveFloor := false
	for _, providerID := range chain {
		view, ok := views[providerID]
		if !ok || !view.Known || floor <= 0 {
			continue
		}
		if view.Credits <= floor {
			belowFloor[providerID] = true
			continue
		}
		anyAboveFloor = true
	}
	if !anyAboveFloor {
		// Nothing is above the floor: ordering by floor would empty the chain.
		belowFloor = map[string]bool{}
	}

	type ranked struct {
		providerID string
		tier       int // 0 = above floor with known expiry, 1 = known but no expiry, 2 = unknown, 3 = below floor
		expiresAt  time.Time
		credits    float64
	}
	entries := make([]ranked, 0, len(chain))
	for _, providerID := range chain {
		view, ok := views[providerID]
		entry := ranked{providerID: providerID, tier: 2}
		switch {
		case belowFloor[providerID]:
			entry.tier = 3
		case ok && view.Known && !view.ExpiresAt.IsZero():
			entry.tier = 0
			entry.expiresAt = view.ExpiresAt
			entry.credits = view.Credits
		case ok && view.Known:
			entry.tier = 1
			entry.credits = view.Credits
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(left, right int) bool {
		a, b := entries[left], entries[right]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if preferExpiring && a.tier == 0 && !a.expiresAt.Equal(b.expiresAt) {
			return a.expiresAt.Before(b.expiresAt)
		}
		// Before the expiry question, a richer account is the better bet: it can
		// absorb a burst without running dry mid-request.
		if a.tier <= 1 && a.credits != b.credits {
			return a.credits > b.credits
		}
		return false
	})

	ordered := make([]string, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry.providerID)
	}
	return ordered
}

// applyWorkBuddyCreditOrder is the server-side step: it reads the cached views for
// the WorkBuddy providers in the chain and lets the pure ranking reorder them.
// Providers of other kinds are simply absent from the view map and keep their
// relative position.
func (s *Server) applyWorkBuddyCreditOrder(chain []string) []string {
	if len(chain) < 2 || s.router == nil {
		return chain
	}
	now := time.Now()
	views := make(map[string]workBuddyCreditView, len(chain))
	for _, providerID := range chain {
		provider, err := s.router.ProviderByID(providerID)
		if err != nil || !isWorkBuddyProvider(provider) {
			continue
		}
		views[providerID] = workBuddyCreditViewOf(providerID, now)
	}
	if len(views) == 0 {
		return chain
	}
	return workBuddyPreferDrained(chain, views, workBuddyCreditFloor(), now)
}
