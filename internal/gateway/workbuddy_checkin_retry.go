package gateway

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Retrying a check-in that failed for a reason that passes.
//
// The upstream's check-in answers "请稍后重试" when its billing side is busy, and that
// settles in minutes, not seconds. A single daily attempt that lands on such an answer
// costs the whole day — and a lost day breaks the streak, which is what later tiers and
// redemptions are measured against. The reference implementation hit exactly this and
// fixed it by queueing the failed account for two more attempts, 10 and 40 minutes later
// (workbuddy2api-panel 1adfa52: "签到遇到「请稍后重试」不再直接放弃").
//
// The rules that make this safe are the ones that fix it there:
//
//   - only a **transient** failure is retried. A business refusal (an enterprise account
//     that cannot check in, a bad parameter) fails identically every time, and retrying it
//     is two more requests a day for nothing;
//   - the day's own record is consulted first: the user may have checked in from the app,
//     or an earlier retry may have succeeded;
//   - the goroutine exits when the process gives up on the request context, and the
//     account's own check-in lock still serialises it against a manual press.
//
// The window is fixed rather than exponential because the upstream's answer names a
// settling time, not a rate limit: two attempts spread over under an hour is enough, and
// a longer tail would keep firing into the next day's schedule.

const (
	// workBuddyCheckinRetryFirstDelay / Second are the waits before the two extra
	// attempts. Ten minutes clears the "busy right now" answer; forty clears a longer
	// billing maintenance window without reaching the next scheduled run.
	workBuddyCheckinRetryFirstDelay  = 10 * time.Minute
	workBuddyCheckinRetrySecondDelay = 40 * time.Minute
	// workBuddyCheckinRetryTotal bounds the whole retry, so a stuck goroutine cannot
	// outlive the day it belongs to.
	workBuddyCheckinRetryTotal = 2 * time.Hour
)

// workBuddyCheckinRetries tracks the accounts with a retry in flight.
var workBuddyCheckinRetries sync.Map // providerID -> struct{}

// workBuddyCheckinTransientHints are the words the vendor uses when the failure is its
// own temporary state rather than the account's.
var workBuddyCheckinTransientHints = []string{
	"请稍后重试", "稍后再试", "请稍候",
	"try again later", "please retry", "temporarily unavailable",
	"请求处理中", "processing", "system busy", "服务繁忙",
}

// workBuddyCheckinTransient reports whether one failed check-in is worth another attempt.
//
// An offline failure is deliberately excluded: the gateway already has a minute-by-minute
// retry for a check-in that never left the machine (`record.Tries`, `Offline`), and
// queueing those here would double the traffic for the same condition.
func workBuddyCheckinTransient(record domain.CheckinRecord) bool {
	if record.Offline {
		return false
	}
	switch record.Outcome {
	case domain.CheckinFailed:
	case "":
		// No outcome recorded is not evidence of a transient failure.
		return false
	default:
		// done / claimed / inactive / ineligible / own_app are answers, not failures.
		return false
	}
	message := strings.ToLower(strings.TrimSpace(record.Message))
	if message == "" {
		return false
	}
	for _, hint := range workBuddyCheckinTransientHints {
		if strings.Contains(message, strings.ToLower(hint)) {
			return true
		}
	}
	// A 5xx from the vendor's own gateway is transient by nature.
	for _, marker := range []string{"http 500", "http 502", "http 503", "http 504", "status code: 5"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// queueWorkBuddyCheckinRetry schedules the two extra attempts for one account.
//
// A second queue for the same account is a no-op: the first one already covers the day,
// and two goroutines waking at the same minute would race for the check-in lock and log
// two failures.
func (s *Server) queueWorkBuddyCheckinRetry(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return
	}
	if _, running := workBuddyCheckinRetries.LoadOrStore(providerID, struct{}{}); running {
		return
	}
	go func() {
		defer workBuddyCheckinRetries.Delete(providerID)
		ctx, cancel := context.WithTimeout(context.Background(), workBuddyCheckinRetryTotal)
		defer cancel()
		for _, delay := range []time.Duration{workBuddyCheckinRetryFirstDelay, workBuddyCheckinRetrySecondDelay} {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return
			}
			// The day may already be done by now — the app, an earlier retry, or the
			// user pressing the button.
			if s.workBuddyCheckinDoneToday(providerID) {
				return
			}
			provider, record, ran := s.checkinProviderNow(ctx, providerID, true)
			if !ran {
				// Another check-in holds the lock, or the provider is gone. Neither is
				// worth a third attempt from here.
				if provider.ID == "" {
					return
				}
				continue
			}
			if !workBuddyCheckinTransient(record) {
				// Succeeded, or failed for a reason another attempt cannot fix.
				return
			}
			if s.logs != nil {
				s.logs.AddApp("info", "check-in retry", "provider="+providerID+" outcome="+string(record.Outcome))
			}
		}
	}()
}

// workBuddyCheckinDoneToday reports whether today's check-in is already settled.
func (s *Server) workBuddyCheckinDoneToday(providerID string) bool {
	record, found, err := s.checkinStore.CheckinByProvider(providerID)
	if err != nil || !found {
		return false
	}
	if record.Day != checkinDay(time.Now()) {
		return false
	}
	switch record.Outcome {
	case domain.CheckinDone, domain.CheckinClaimed:
		return true
	}
	return false
}
