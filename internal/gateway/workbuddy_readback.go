package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Checking that an activity report actually counted.
//
// WorkBuddy answers 200 to a report it silently drops. The reference implementation
// treats that as a known failure mode rather than a theory: the mini-program events
// are rolled back, and an account can report all day while its streak never moves
// ("200 但未计分", misakano7545/workbuddy2api-panel scheduler/benefit.go and
// scheduler/scheduler.go checkActivityStreak).
//
// A gateway that only looks at the HTTP status therefore reports success forever
// while the account earns nothing. This file closes that loop: report, then read the
// streak oracle and say whether the number moved.
//
// It is deliberately cheap and honest:
//
//   - the baseline is per provider and in memory. A process restart forgets it, and
//     the first pass after that reports "no baseline" instead of inventing a verdict;
//   - "unchanged" is a note, not a failure. The streak only advances once a day, so
//     calling the same keep-alive twice legitimately shows no movement;
//   - a zero streak right after a report is the interesting case: the account has
//     never had a counted day, which is what a silently dropped report looks like.

// workBuddyStreakBaselines remembers the last streak day count seen per provider.
var workBuddyStreakBaselines sync.Map // providerID -> workBuddyStreakBaseline

type workBuddyStreakBaseline struct {
	days int
	at   time.Time
}

// workBuddyActivityReadback is what a verified report came to.
type workBuddyActivityReadback struct {
	Reported    bool   `json:"reported"`
	StreakDays  int    `json:"streakDays"`
	Previous    int    `json:"previous,omitempty"`
	HasBaseline bool   `json:"hasBaseline"`
	Advanced    bool   `json:"advanced"`
	Note        string `json:"note,omitempty"`
}

// workBuddyReportActivityVerified reports activity and then asks the streak oracle
// whether the number moved.
//
// A failure to report is returned as an error (the caller wants to surface it); a
// report that succeeded but did not count comes back in Note, because the account did
// nothing wrong and nothing should be retried in a loop.
func (s *Server) workBuddyReportActivityVerified(
	ctx context.Context,
	providerID string,
	site workBuddySite,
	credential domain.WorkBuddyCredential,
) (workBuddyActivityReadback, error) {
	result := workBuddyActivityReadback{}
	if err := workBuddyReportActivity(ctx, site, credential); err != nil {
		return result, err
	}
	result.Reported = true

	streak, err := workBuddyStreakNow(ctx, site, credential)
	if err != nil {
		// The report itself went through; the oracle being unavailable is not a
		// reason to call the whole step a failure.
		result.Note = "无法回读连登天数：" + workBuddyShortError(err)
		return result, nil
	}
	result.StreakDays = streak.Days
	if cached, ok := workBuddyStreakBaselines.Load(providerID); ok {
		if baseline, typed := cached.(workBuddyStreakBaseline); typed {
			result.HasBaseline = true
			result.Previous = baseline.days
			result.Advanced = streak.Days > baseline.days
		}
	}
	workBuddyStreakBaselines.Store(providerID, workBuddyStreakBaseline{days: streak.Days, at: time.Now()})

	switch {
	case streak.Days <= 0:
		// The shape a silently dropped report leaves behind: the account has never
		// had a counted day, so every later step (streak tiers, first_buddy) is stuck.
		result.Note = "上报已返回成功，但连登为 0：疑似 200 未计分（事件被回滚）"
	case result.HasBaseline && !result.Advanced:
		result.Note = fmt.Sprintf("连登仍为 %d 天（本次未推进）", streak.Days)
	}
	return result, nil
}

// workBuddyStreakBaselineOf reads one provider's recorded streak, for tests and the
// console.
func workBuddyStreakBaselineOf(providerID string) (int, bool) {
	cached, ok := workBuddyStreakBaselines.Load(providerID)
	if !ok {
		return 0, false
	}
	baseline, typed := cached.(workBuddyStreakBaseline)
	if !typed {
		return 0, false
	}
	return baseline.days, true
}
