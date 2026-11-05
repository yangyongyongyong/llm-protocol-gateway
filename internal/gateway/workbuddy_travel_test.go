package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// travelServer fakes the buddy routes and records what was asked for.
func travelServer(t *testing.T, handler func(method, path string) (int, string)) (*rewardsServer, func()) {
	t.Helper()
	return newRewardsServer(t, handler)
}

// No buddy means nothing to travel with: this pass stays out of the adoption chain's
// way instead of sending a depart for a buddy that does not exist.
func TestWorkBuddyTravelPassWithoutABuddy(t *testing.T) {
	_, cleanup := travelServer(t, func(method, path string) (int, string) {
		if strings.HasSuffix(path, "/buddy/info") {
			return http.StatusOK, `{"code":0,"data":{"buddy":null}}`
		}
		return http.StatusOK, `{"code":0,"data":{}}`
	})
	defer cleanup()
	credit, notes := workBuddyTravelPass(context.Background(), workBuddyCNSite,
		domain.WorkBuddyCredential{AccessToken: "t"})
	if credit != 0 {
		t.Fatalf("credit=%v, want nothing earned", credit)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "还没有猫") {
		t.Fatalf("notes=%v, want the no-buddy note", notes)
	}
}

// An arrived buddy pays out — but only with a record id: `record_id: 0` is not a claim.
func TestWorkBuddyTravelPassClaimsOnlyWithARecordID(t *testing.T) {
	state := &travelState{}
	_, cleanup := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/buddy/info"):
			return http.StatusOK, `{"code":0,"data":{"buddy":{"name":"momo"}}}`
		case strings.HasSuffix(path, "/travel/status"):
			return http.StatusOK, `{"code":0,"data":{"state":"arrived","record_id":"77"}}`
		case strings.HasSuffix(path, "/travel/claim"):
			state.claimed = true
			return http.StatusOK, `{"code":0,"data":{"reward_credit":12}}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup()
	credit, notes := workBuddyTravelPass(context.Background(), workBuddyCNSite,
		domain.WorkBuddyCredential{AccessToken: "t"})
	if !state.claimed {
		t.Fatalf("an arrived buddy was not claimed: notes=%v", notes)
	}
	if credit != 12 {
		t.Fatalf("credit = %v, want 12", credit)
	}

	// arrived with no record id: nothing is sent.
	state2 := &travelState{}
	_, cleanup2 := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/buddy/info"):
			return http.StatusOK, `{"code":0,"data":{"buddy":{"name":"momo"}}}`
		case strings.HasSuffix(path, "/travel/status"):
			return http.StatusOK, `{"code":0,"data":{"state":"arrived","record_id":0}}`
		case strings.HasSuffix(path, "/travel/claim"):
			state2.claimed = true
			return http.StatusOK, `{"code":0,"data":{"reward_credit":12}}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup2()
	credit, notes = workBuddyTravelPass(context.Background(), workBuddyCNSite,
		domain.WorkBuddyCredential{AccessToken: "t"})
	if state2.claimed || credit != 0 {
		t.Fatalf("a claim went out without a record id (credit=%v notes=%v)", credit, notes)
	}
	if len(notes) == 0 {
		t.Fatal("the skipped claim left no note")
	}
}

// An idle buddy is sent out, unless the day's allowance is already spent.
func TestWorkBuddyTravelPassDepartsWhenAllowed(t *testing.T) {
	state := &travelState{}
	_, cleanup := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/buddy/info"):
			return http.StatusOK, `{"code":0,"data":{"buddy":{"name":"momo"}}}`
		case strings.HasSuffix(path, "/travel/status"):
			return http.StatusOK, `{"code":0,"data":{"state":"idle","daily_limit_reached":false}}`
		case strings.HasSuffix(path, "/travel/depart"):
			state.departed = true
			return http.StatusOK, `{"code":0}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup()
	workBuddyTravelPass(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if !state.departed {
		t.Fatal("an idle buddy was not sent out")
	}

	// The day's allowance is spent: no depart, and no note either — that is an answer.
	state2 := &travelState{}
	_, cleanup2 := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/buddy/info"):
			return http.StatusOK, `{"code":0,"data":{"buddy":{"name":"momo"}}}`
		case strings.HasSuffix(path, "/travel/status"):
			return http.StatusOK, `{"code":0,"data":{"state":"idle","daily_limit_reached":true}}`
		case strings.HasSuffix(path, "/travel/depart"):
			state2.departed = true
			return http.StatusOK, `{"code":0}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup2()
	workBuddyTravelPass(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if state2.departed {
		t.Fatal("a depart went out after the daily limit")
	}
}

// A traveling buddy is waited for, not re-sent.
func TestWorkBuddyTravelPassWaitsWhileTraveling(t *testing.T) {
	state := &travelState{}
	_, cleanup := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/buddy/info"):
			return http.StatusOK, `{"code":0,"data":{"buddy":{"name":"momo"}}}`
		case strings.HasSuffix(path, "/travel/status"):
			return http.StatusOK, `{"code":0,"data":{"state":"traveling"}}`
		case strings.HasSuffix(path, "/travel/depart"):
			state.departed = true
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup()
	workBuddyTravelPass(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if state.departed {
		t.Fatal("a traveling buddy was sent out again")
	}
}

// Makeup cards are spent newest-first and bounded, because the closest missed day is
// the one that keeps the current run alive.
func TestWorkBuddyRepairStreakUsesTheNewestDays(t *testing.T) {
	used := &travelState{}
	_, cleanup := travelServer(t, func(method, path string) (int, string) {
		switch {
		case strings.HasSuffix(path, "/activity/growth/streak"):
			return http.StatusOK, `{"code":0,"data":{"streak":{"days":5},"makeup_cards":{"balance":"4"}}}`
		case strings.HasSuffix(path, "/activity/growth/heatmap"):
			return http.StatusOK, `{"code":0,"data":{"missed_dates":["2026-10-30","2026-11-01","2026-11-02"]}}`
		case strings.HasSuffix(path, "/makeup-cards/use"):
			used.dates = append(used.dates, path)
			return http.StatusOK, `{"code":0}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup()
	count, notes := workBuddyRepairStreak(context.Background(), workBuddyCNSite,
		domain.WorkBuddyCredential{AccessToken: "t"}, 2)
	if count != 2 || len(notes) != 0 {
		t.Fatalf("count=%d notes=%v", count, notes)
	}
	if len(used.dates) != 2 {
		t.Fatalf("makeup calls = %d, want 2", len(used.dates))
	}

	// No cards means no work and no upstream call.
	_, cleanup2 := travelServer(t, func(method, path string) (int, string) {
		if strings.HasSuffix(path, "/activity/growth/streak") {
			return http.StatusOK, `{"code":0,"data":{"streak":{"days":5},"makeup_cards":{"balance":0}}}`
		}
		return http.StatusOK, `{"code":0}`
	})
	defer cleanup2()
	if count, _ := workBuddyRepairStreak(context.Background(), workBuddyCNSite,
		domain.WorkBuddyCredential{AccessToken: "t"}, 3); count != 0 {
		t.Fatalf("cards were used with a zero balance: %d", count)
	}
}

// travelState records the calls a pass made.
type travelState struct {
	claimed  bool
	departed bool
	dates    []string
}
