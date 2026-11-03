package gateway

// WorkBuddy's daily check-in (签到).
//
// While its event runs, a WorkBuddy (China) account is given a few credits a day
// for pressing 签到 in the app, more on a streak. The gateway presses it the way
// WorkBuddy does: ask the event's status, and claim only while the event is on
// and today's is not in yet. The streak the card shows is the vendor's own count
// — this gateway keeps no tally of its own, so a day checked in by hand in the
// app still reads as a streak here.
//
// Only the Chinese build has the event; WorkBuddy AI ships without it, which is
// why its subscription gets no switch.
//
// One answer is worth knowing about: WorkBuddy's gateway answers the check-in
// alone with a bare 401 for accounts whose credits read fine. Its own app signs
// the check-in with a device token from its security SDK, which nothing outside
// the app can make. That is not a failure to retry all day — it is the day's
// answer, and the card says to check in in the WorkBuddy app.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	workBuddyCheckinStatusPath = "/v2/billing/meter/checkin-activity-status"
	workBuddyCheckinClaimPath  = "/v2/billing/meter/daily-checkin"
	// workBuddyResourceSummaryPath has no /v2 prefix: WorkBuddy asks its own
	// credits at /billing/meter/… on desktop and web alike. It doubles as the
	// probe that tells a refused check-in apart from a refused sign-in.
	workBuddyResourceSummaryPath = "/billing/meter/get-user-resource-summary"
)

// WorkBuddy's answers to a claim that did not go through.
const (
	workBuddyCheckinAlready    = 1001 // today's is in already
	workBuddyCheckinIneligible = 1002 // the account can't take part
	workBuddyCheckinEnded      = 1003 // the event is over
)

// workBuddyCheckinStatus is the event as an account sees it.
type workBuddyCheckinStatus struct {
	Active         bool            `json:"active"`
	TodayCheckedIn bool            `json:"today_checked_in"`
	StreakDays     workBuddyNumber `json:"streak_days"`
	TodayCredit    workBuddyNumber `json:"today_credit"`
}

// workBuddyCheckinClaim is what a claim gave.
type workBuddyCheckinClaim struct {
	Credit     workBuddyNumber `json:"credit"`
	StreakDays workBuddyNumber `json:"streak_days"`
	Message    string          `json:"message"`
}

func workBuddyCheckinAccountKey(site workBuddySite, credential domain.WorkBuddyCredential) string {
	uid := strings.TrimSpace(credential.UserID)
	if uid == "" {
		return ""
	}
	return site.authType + "|" + uid
}

// workBuddyCheckin presses a's check-in for the day. A refused sign-in is
// repaired once with refresh: WorkBuddy revokes an access token when the same
// account signs in elsewhere, and the stored expiry never predicts that. The
// credential that ended up being used is returned so the caller can keep the
// rotated token.
func workBuddyCheckin(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, refresh workBuddyTokenRefresher) (checkinResult, domain.WorkBuddyCredential) {
	result := workBuddyCheckinOnce(ctx, site, credential)
	if result.loginRefused && refresh != nil {
		if updated, err := refresh(); err == nil {
			credential = updated
			result = workBuddyCheckinOnce(ctx, site, credential)
		}
	}
	return result, credential
}

func workBuddyCheckinOnce(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) checkinResult {
	result := checkinResult{AccountKey: workBuddyCheckinAccountKey(site, credential)}
	headers := workBuddyAccountHeaders(site, credential)
	var status workBuddyCheckinStatus
	if err := workBuddyCall(ctx, site, http.MethodPost, site.api()+workBuddyCheckinStatusPath, headers, map[string]any{}, &status); err != nil {
		return workBuddyCheckinRefused(ctx, site, credential, headers, err)
	}
	switch {
	case !status.Active:
		result.Outcome = domain.CheckinInactive
		return result
	case status.TodayCheckedIn:
		result.Outcome = domain.CheckinDone
		result.Credit = float64(status.TodayCredit)
		result.Streak = int(status.StreakDays)
		return result
	}
	var claim workBuddyCheckinClaim
	if err := workBuddyCall(ctx, site, http.MethodPost, site.api()+workBuddyCheckinClaimPath, headers, map[string]any{}, &claim); err != nil {
		refused := workBuddyCheckinRefused(ctx, site, credential, headers, err)
		if refused.Outcome == domain.CheckinDone {
			// The event said today was not in yet, so the streak the status
			// carried is the one to show even when the claim answers "already".
			refused.Streak = int(status.StreakDays)
		}
		return refused
	}
	result.Outcome = domain.CheckinClaimed
	result.Credit = float64(claim.Credit)
	result.Streak = int(claim.StreakDays)
	result.Message = claim.Message
	return result
}

// workBuddyCheckinRefused is what an error from the event's API comes to: one of
// its answers for the day, or a failure to try again later.
func workBuddyCheckinRefused(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, headers map[string]string, err error) checkinResult {
	if !workBuddyUnauthorized(err) {
		if code, ok := workBuddyErrorCode(err); ok {
			switch code {
			case workBuddyCheckinAlready:
				return checkinResult{Outcome: domain.CheckinDone, AccountKey: workBuddyCheckinAccountKey(site, credential)}
			case workBuddyCheckinIneligible:
				return checkinResult{Outcome: domain.CheckinIneligible, Message: err.Error(), AccountKey: workBuddyCheckinAccountKey(site, credential)}
			case workBuddyCheckinEnded:
				return checkinResult{Outcome: domain.CheckinInactive, Message: err.Error(), AccountKey: workBuddyCheckinAccountKey(site, credential)}
			}
		}
		return checkinResult{Outcome: domain.CheckinFailed, Message: err.Error(), Offline: checkinUnanswered(err), AccountKey: workBuddyCheckinAccountKey(site, credential)}
	}
	// A 401 says nothing on its own. Read the account's credits with the same
	// headers: when those read, the sign-in is good and it is the check-in alone
	// that wants WorkBuddy's own app.
	key := workBuddyCheckinAccountKey(site, credential)
	if probeErr := workBuddyCall(ctx, site, http.MethodPost, site.api()+workBuddySummaryPath, headers, map[string]any{}, nil); probeErr == nil {
		return checkinResult{Outcome: domain.CheckinOwnApp, AccountKey: key,
			Message: "WorkBuddy 用 401 挡下了签到，但这个账号的额度读得出来：它的签到只认 WorkBuddy 客户端，请在 WorkBuddy 应用里点签到"}
	}
	return checkinResult{Outcome: domain.CheckinFailed, AccountKey: key, loginRefused: true,
		Message: "WorkBuddy 拒绝了这个账号的登录（401），请重新登录"}
}

// workBuddyUnauthorized says whether err is WorkBuddy's bare 401, with or
// without a message: its gateway answers a token it refuses with an HTML page,
// so the code is not always in an envelope.
func workBuddyUnauthorized(err error) bool {
	var target *workBuddyError
	if errors.As(err, &target) {
		return target.status == http.StatusUnauthorized || target.code == http.StatusUnauthorized
	}
	return err != nil && strings.Contains(err.Error(), "HTTP 401")
}
