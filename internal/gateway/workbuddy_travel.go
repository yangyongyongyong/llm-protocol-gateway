package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The buddy's travel loop, and the two streak helpers beside it.
//
// Same family as the gift / streak / lottery pass this gateway already runs: cheap
// GETs and POSTs on the growth host that earn credits without a conversation
// (misakano7545/workbuddy2api-panel internal/upstream/travel.go and streak_extras.go).
//
//	travel   GET  /activity/growth/buddy/info              is there a buddy?
//	         GET  /activity/growth/buddy/travel/status     idle / traveling / arrived
//	         POST /activity/growth/buddy/travel/depart     {location_id} 1-4
//	         POST /activity/growth/buddy/travel/claim      {record_id}
//	makeup   GET  /activity/growth/heatmap                which days were missed
//	         POST /activity/growth/makeup-cards/use        {target_date}
//
// Three rules the reference measured, all of which this file follows:
//
//   - **`record_id` is required for a claim and 0 is not a claim.** A status that says
//     "arrived" with no record id is a state to skip, not a request to send.
//   - **A day's travel is limited by the server**, so `daily_limit_reached` is read and
//     honoured rather than retried against.
//   - **The makeup card is spent before the streak is evaluated**, because repairing a
//     missed day is what keeps the streak alive; redeeming tiers first would give up a
//     days-long streak for one day's credit.
//
// All four rewards are locations; the reference notes the payout is the same whichever
// is chosen, so a fixed one is used and the choice is not invented per account.

const (
	workBuddyBuddyInfoPath    = "/activity/growth/buddy/info"
	workBuddyTravelStatusPath = "/activity/growth/buddy/travel/status"
	workBuddyTravelDepartPath = "/activity/growth/buddy/travel/depart"
	workBuddyTravelClaimPath  = "/activity/growth/buddy/travel/claim"
	workBuddyHeatmapPath      = "/activity/growth/heatmap"
	workBuddyMakeupUsePath    = "/activity/growth/makeup-cards/use"

	// workBuddyTravelLocation is the location the pass sends the buddy to. The
	// reference states all four pay the same, so one fixed choice keeps the behaviour
	// explainable instead of picking one per account.
	workBuddyTravelLocation = 4
)

// workBuddyBuddy is whether the account has a buddy at all.
type workBuddyBuddy struct {
	Exists bool
	Name   string
}

// workBuddyTravelState is one travel status answer.
type workBuddyTravelState struct {
	State             string
	DailyLimitReached bool
	RecordID          int64
	RewardCredit      float64
}

// workBuddyBuddyOf asks whether a buddy exists.
//
// The vendor answers `data.buddy` as null before the first adoption, so absence is a
// normal state rather than an error.
func workBuddyBuddyOf(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (workBuddyBuddy, error) {
	var data struct {
		Buddy *struct {
			Name string `json:"name"`
		} `json:"buddy"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodGet,
		workBuddyBuddyInfoPath, nil, &data); err != nil {
		return workBuddyBuddy{}, err
	}
	if data.Buddy == nil {
		return workBuddyBuddy{}, nil
	}
	return workBuddyBuddy{Exists: true, Name: data.Buddy.Name}, nil
}

// workBuddyTravelStatusNow reads where the buddy is.
func workBuddyTravelStatusNow(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (workBuddyTravelState, error) {
	var data struct {
		State             string `json:"state"`
		DailyLimitReached any    `json:"daily_limit_reached"`
		RecordID          any    `json:"record_id"`
		RewardCredit      any    `json:"reward_credit"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodGet,
		workBuddyTravelStatusPath, nil, &data); err != nil {
		return workBuddyTravelState{}, err
	}
	return workBuddyTravelState{
		State:             strings.ToLower(strings.TrimSpace(data.State)),
		DailyLimitReached: workBuddyAnyBool(data.DailyLimitReached),
		RecordID:          int64(workBuddyAnyFloat(data.RecordID)),
		RewardCredit:      workBuddyAnyFloat(data.RewardCredit),
	}, nil
}

// workBuddyTravelDepart sends the buddy out.
func workBuddyTravelDepart(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, locationID int) error {
	body := map[string]any{"location_id": locationID}
	return workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyTravelDepartPath, body, nil)
}

// workBuddyTravelClaim collects what the trip earned.
func workBuddyTravelClaim(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, recordID int64) (float64, error) {
	var data struct {
		Credit any `json:"reward_credit"`
	}
	body := map[string]any{"record_id": recordID}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyTravelClaimPath, body, &data); err != nil {
		return 0, err
	}
	credit := workBuddyAnyFloat(data.Credit)
	if credit <= 0 {
		return 0, nil
	}
	return credit, nil
}

// workBuddyMissedDays lists the days a makeup card could repair.
func workBuddyMissedDays(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) ([]string, error) {
	var data struct {
		Missed []string `json:"missed_dates"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodGet,
		workBuddyHeatmapPath, nil, &data); err != nil {
		return nil, err
	}
	return data.Missed, nil
}

// workBuddyUseMakeupCard repairs one missed day and reports whether the card was
// accepted.
func workBuddyUseMakeupCard(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, targetDate string) (bool, error) {
	body := map[string]any{"target_date": targetDate}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyMakeupUsePath, body, nil); err != nil {
		return false, err
	}
	return true, nil
}

// workBuddyTravelPass runs one day's travel step.
//
// Order matters and follows the state machine: no buddy → nothing to do here (the
// adoption chain owns that), arrived → claim, idle with allowance → depart,
// traveling → wait. A claim without a record id is skipped rather than sent, because
// `record_id: 0` is not a claim.
func workBuddyTravelPass(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (float64, []string) {
	credit := 0.0
	var notes []string

	buddy, err := workBuddyBuddyOf(ctx, site, credential)
	if err != nil {
		return credit, []string{"buddy info: " + workBuddyShortError(err)}
	}
	if !buddy.Exists {
		// The first_buddy task adopts one; this pass has nothing to travel with.
		//
		// Said out loud rather than silently: "no buddy" and "the buddy routes are
		// unreachable" both spend nothing, and an operator looking at a card that never
		// moves needs to tell them apart.
		return credit, []string{"travel: 账号还没有猫（由 first_buddy 任务领养）"}
	}

	state, err := workBuddyTravelStatusNow(ctx, site, credential)
	if err != nil {
		return credit, []string{"travel status: " + workBuddyShortError(err)}
	}

	switch state.State {
	case "arrived":
		if state.RecordID <= 0 {
			return credit, []string{"travel claim: arrived without a record id"}
		}
		claimed, claimErr := workBuddyTravelClaim(ctx, site, credential, state.RecordID)
		if claimErr != nil {
			return credit, []string{"travel claim: " + workBuddyShortError(claimErr)}
		}
		credit += claimed
	case "idle", "":
		if state.DailyLimitReached {
			return credit, nil
		}
		if departErr := workBuddyTravelDepart(ctx, site, credential, workBuddyTravelLocation); departErr != nil {
			notes = append(notes, "travel depart: "+workBuddyShortError(departErr))
		}
	case "traveling", "travelling":
		// Nothing to do until it lands.
		notes = append(notes, "travel: 出行中，等到站后领奖")
	default:
		notes = append(notes, "travel state: "+state.State)
	}
	return credit, notes
}

// workBuddyRepairStreak spends makeup cards on missed days, newest first.
//
// Newest first because the closest missed day is the one that keeps the current run
// alive; repairing an older gap buys nothing if today is already broken.
func workBuddyRepairStreak(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, maxCards int) (int, []string) {
	if maxCards <= 0 {
		return 0, nil
	}
	streak, err := workBuddyStreakNow(ctx, site, credential)
	if err != nil {
		return 0, []string{"streak: " + workBuddyShortError(err)}
	}
	if streak.MakeupBalance <= 0 {
		return 0, nil
	}
	budget := streak.MakeupBalance
	if budget > maxCards {
		budget = maxCards
	}
	missed, err := workBuddyMissedDays(ctx, site, credential)
	if err != nil || len(missed) == 0 {
		return 0, nil
	}
	if len(missed) > budget {
		missed = missed[len(missed)-budget:]
	}
	used := 0
	var notes []string
	for index := len(missed) - 1; index >= 0; index-- {
		ok, useErr := workBuddyUseMakeupCard(ctx, site, credential, missed[index])
		if useErr != nil {
			notes = append(notes, "makeup: "+workBuddyShortError(useErr))
			break
		}
		if ok {
			used++
		}
	}
	return used, notes
}
