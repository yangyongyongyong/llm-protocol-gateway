package gateway

// The daily check-in loop.
//
// Which subscriptions have one is a question the vendor answers, not us:
// checkinVendorOf lists the ones whose event the gateway can press (Qoder on
// both sites, WorkBuddy's Chinese build), and every other subscription gets no
// switch in the console at all. WorkBuddy AI is missing from the list because
// that build ships without the event, not because nobody wrote it.
//
// Cadence, taken from what the vendors allow:
//   - one account is checked in once a Beijing day, which is the day they count
//     in; UTC+8 is hardcoded so a gateway on a US VPS and its user in Shenzhen
//     agree about whether today is done, and no time zone database is needed;
//   - the accounts are looked at every minute, but nothing is asked until an
//     account is actually due: the day turned, or a failure came back for;
//   - Qoder is re-read half-hourly even after a success, because its campaigns
//     page can put a new grant up inside the same day;
//   - a check-in that got no answer is tried again after half an hour; one whose
//     request never left the machine — a laptop just woken, its network not back
//     — after one minute, doubled for each attempt that also failed, so the
//     account is checked in as soon as it can be instead of waiting out thirty
//     minutes of "签到失败".

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitoring"
)

const (
	// checkinTick is how often the loop looks for an account that is due.
	checkinTick = time.Minute
	// checkinStartDelay lets the gateway settle before the first look: a restart
	// in the middle of the day should not compete with the console opening.
	checkinStartDelay = 2 * time.Minute
	// checkinLook is how often an account that is settled is read again — only
	// for Qoder, whose campaigns can change inside a day.
	checkinLook = 30 * time.Minute
	// checkinRetry is how long a check-in that got no answer waits before it is
	// tried again the same day.
	checkinRetry = 30 * time.Minute
	// checkinOfflineFirst is the first wait for a request that never reached the
	// vendor; it doubles from there.
	checkinOfflineFirst = time.Minute
	// checkinOfflineCap counts the doublings: 1, 2, 4, 8, 16, 30 minutes.
	checkinOfflineCap   = 5
	checkinRequestLimit = 2 * time.Minute
)

// The two check-ins this gateway can press.
const (
	checkinVendorQoder     = "qoder"
	checkinVendorWorkBuddy = "workbuddy"
)

// beijingZone is the day the vendors count in — UTC+8 all year, with no leap
// rules to get wrong and no tzdata read on Windows.
var beijingZone = time.FixedZone("CST", 8*60*60)

// checkinDay is the Beijing day ("2006-01-02") t falls on. A record of that Day
// is today's.
func checkinDay(t time.Time) string { return t.In(beijingZone).Format("2006-01-02") }

// CheckinStore persists what came of a check-in. One row per provider: the
// console asks "is today's in", and the vendor decides what a day is.
type CheckinStore interface {
	SaveCheckin(record domain.CheckinRecord) error
	ListCheckins() ([]domain.CheckinRecord, error)
	CheckinByProvider(providerID string) (domain.CheckinRecord, bool, error)
}

func (s *Server) checkinStoreReady() bool { return s.checkinStore != nil }

// checkinVendorOf names the daily check-in a subscription has, and false when
// its vendor runs none — which is the common case, and why the console shows the
// switch only for these.
func checkinVendorOf(provider domain.Provider) (string, bool) {
	switch strings.TrimSpace(provider.AuthType) {
	case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
		return checkinVendorQoder, true
	case domain.AuthTypeWorkBuddy:
		return checkinVendorWorkBuddy, true
	}
	return "", false
}

// checkinUsable says whether a provider may be checked in at all: its vendor has
// the event and the account is signed in and not paused.
func checkinUsable(provider domain.Provider) bool {
	if _, ok := checkinVendorOf(provider); !ok {
		return false
	}
	switch strings.TrimSpace(provider.AuthType) {
	case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
		return provider.QoderPAT != nil && !provider.QoderPAT.Disconnected
	case domain.AuthTypeWorkBuddy:
		return provider.WorkBuddy != nil && strings.TrimSpace(provider.WorkBuddy.AccessToken) != ""
	}
	return false
}

// checkinResult is one account's answer, before it is stamped with the day it
// belongs to.
type checkinResult struct {
	Outcome string
	Credit  float64
	Streak  int
	Message string
	// Until ends the window a success was read inside; zero means the answer
	// holds for the rest of the Beijing day.
	Until      time.Time
	Offline    bool
	AccountKey string
	// loginRefused marks a failure the account's own token caused, as opposed to
	// the endpoint refusing today's claim: only the former is worth a refresh.
	loginRefused bool
}

// StartDailyCheckin presses the check-in of every subscription that asked for it
// on its card. It ticks every minute and asks nothing until an account is due,
// so a gateway with no check-in enabled costs one provider listing per minute.
func (s *Server) StartDailyCheckin(ctx context.Context) {
	if !s.checkinStoreReady() {
		return
	}
	go func() {
		timer := time.NewTimer(checkinStartDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.runDueCheckins(ctx)
			timer.Reset(checkinTick)
		}
	}()
}

// runDueCheckins checks in every account that is due, each in its own goroutine:
// a vendor that answers slowly must not hold the loop, or another account's
// check-in, behind it.
func (s *Server) runDueCheckins(ctx context.Context) {
	records, err := s.checkinStore.ListCheckins()
	if err != nil {
		slog.Warn("check-in: read records failed", "error", err)
		return
	}
	byID := map[string]domain.CheckinRecord{}
	for _, record := range records {
		byID[record.ProviderID] = record
	}
	now := time.Now()
	for _, provider := range s.router.State().Providers {
		if !provider.CheckinEnabled || provider.Disabled || !checkinUsable(provider) {
			continue
		}
		vendor, _ := checkinVendorOf(provider)
		accountKey, keyErr := checkinAccountKey(provider)
		if keyErr != nil {
			// Without an account id the record could not be attributed to anyone,
			// so nothing is asked; the card says why.
			s.recordCheckin(provider, domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: keyErr.Error()}, now)
			continue
		}
		if checkinSettled(byID[provider.ID], vendor, accountKey, checkinDay(now), now, false) {
			continue
		}
		go s.checkinProviderNow(ctx, provider.ID, false)
	}
}

// checkinProviderNow presses one account's check-in. soon is a press from the
// console — the answer of a failed attempt is then worth nothing, and a disabled
// provider is still asked, because the user just told us to.
func (s *Server) checkinProviderNow(ctx context.Context, providerID string, soon bool) (domain.Provider, domain.CheckinRecord, bool) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, domain.CheckinRecord{}, false
	}
	vendor, ok := checkinVendorOf(provider)
	if !ok {
		return provider, domain.CheckinRecord{}, false
	}
	if !s.claimCheckin(providerID) {
		// One check-in at a time per account: a refresh token is spent once, and
		// the loop and a press must not both spend it.
		return provider, domain.CheckinRecord{}, false
	}
	defer s.releaseCheckin(providerID)

	now := time.Now()
	requestCtx, cancel := context.WithTimeout(ctx, checkinRequestLimit)
	defer cancel()
	result, refreshed, runErr := s.runCheckin(requestCtx, vendor, provider, now)
	if runErr != nil {
		result = checkinResult{Outcome: domain.CheckinFailed, Message: runErr.Error(), Offline: checkinUnanswered(runErr), AccountKey: result.AccountKey}
	}
	if refreshed.ID != "" && credentialMoved(provider, refreshed) {
		// The check-in may have rotated the account's token, which is spent and
		// so has to be kept even when the check-in itself says nothing new.
		if saveErr := s.persistProviderOAuth(refreshed); saveErr != nil {
			slog.Warn("check-in: save rotated credential failed", "provider", provider.ID, "error", saveErr)
		}
	}
	if refreshed.ID != "" {
		provider = refreshed
	}
	record := domain.CheckinRecord{
		ProviderID: provider.ID,
		AccountKey: result.AccountKey,
		Day:        checkinDay(now),
		At:         now.UTC().Format(time.RFC3339),
		Outcome:    result.Outcome,
		Credit:     result.Credit,
		Streak:     result.Streak,
		Message:    result.Message,
		Offline:    result.Offline,
	}
	if !result.Until.IsZero() {
		record.Until = result.Until.UTC().Format(time.RFC3339)
	}
	if previous, found, err := s.checkinStore.CheckinByProvider(provider.ID); err == nil && found &&
		previous.Day == record.Day && previous.Offline && record.Offline {
		// A streak of attempts that never left the machine is what backs the
		// minute-by-minute retry off, so it has to survive the rewrite.
		record.Tries = previous.Tries + 1
	}
	s.recordCheckin(provider, record, now)
	// A failure the vendor describes as temporary gets two more attempts later today:
	// one attempt that lands on "请稍后重试" costs the whole day, and a lost day breaks
	// the streak (workbuddy_checkin_retry.go).
	if workBuddyCheckinTransient(record) {
		s.queueWorkBuddyCheckinRetry(provider.ID)
	}
	return provider, record, true
}

// credentialMoved says whether a check-in left the account with a different
// token, which is the only thing worth writing back.
func credentialMoved(before, after domain.Provider) bool {
	if before.QoderPAT != nil && after.QoderPAT != nil {
		if after.QoderPAT.DeviceToken != before.QoderPAT.DeviceToken ||
			after.QoderPAT.AccessToken != before.QoderPAT.AccessToken {
			return true
		}
	}
	if before.WorkBuddy != nil && after.WorkBuddy != nil {
		return after.WorkBuddy.AccessToken != before.WorkBuddy.AccessToken
	}
	return false
}

// runCheckin asks one vendor for the account's day. The provider it hands back
// carries the credential as it now stands, rotated token and all.
func (s *Server) runCheckin(ctx context.Context, vendor string, provider domain.Provider, now time.Time) (checkinResult, domain.Provider, error) {
	switch vendor {
	case checkinVendorQoder:
		site := qoderSiteOf(provider)
		if provider.QoderPAT == nil {
			return checkinResult{}, provider, errors.New("Qoder 尚未登录")
		}
		result, credential := qoderCheckin(ctx, s, provider, site, *provider.QoderPAT, now)
		updated := provider
		if credential.DeviceToken != provider.QoderPAT.DeviceToken || credential.AccessToken != provider.QoderPAT.AccessToken {
			latest, err := s.router.SetProviderQoderPAT(provider.ID, credential)
			if err == nil {
				updated = latest
			} else {
				updated.QoderPAT = &credential
			}
		}
		return result, updated, nil
	case checkinVendorWorkBuddy:
		site, ok := workBuddySiteForAuthType(provider.AuthType)
		if !ok || provider.WorkBuddy == nil {
			return checkinResult{}, provider, errors.New("WorkBuddy 尚未登录")
		}
		// The check-in is pressed with the plan's bearer, so it has to be live
		// first; a refresh here is the same one any chat would pay for.
		refreshed, err := s.ensureFreshWorkBuddyToken(provider)
		if err != nil {
			return checkinResult{}, provider, err
		}
		if refreshed.WorkBuddy == nil {
			return checkinResult{}, refreshed, errors.New("WorkBuddy 尚未登录")
		}
		stale := *refreshed.WorkBuddy
		result, credential := workBuddyCheckin(ctx, site, stale, func() (domain.WorkBuddyCredential, error) {
			updated, refreshErr := s.forceRefreshWorkBuddyToken(provider.ID)
			if refreshErr != nil {
				return stale, refreshErr
			}
			if updated.WorkBuddy == nil {
				return stale, errors.New("WorkBuddy 登录已失效，请在控制台重新登录")
			}
			return *updated.WorkBuddy, nil
		})
		if credential.AccessToken != stale.AccessToken {
			refreshed.WorkBuddy = &credential
		}
		return result, refreshed, nil
	}
	return checkinResult{}, provider, fmt.Errorf("未知的签到厂商 %q", vendor)
}

// recordCheckin writes what came of a day and logs the lines worth knowing:
// credits in, or a failure the loop will keep trying. An answer that changed is
// also pushed into the event stream — the console shows check-ins beside
// renewals and probes — while a retry of the same answer stays out of it.
func (s *Server) recordCheckin(provider domain.Provider, record domain.CheckinRecord, now time.Time) {
	if record.AccountKey == "" {
		record.AccountKey = provider.ID
	}
	if record.ProviderID == "" {
		record.ProviderID = provider.ID
	}
	if record.Day == "" {
		record.Day = checkinDay(now)
	}
	if record.At == "" {
		record.At = now.UTC().Format(time.RFC3339)
	}
	previous, hadPrevious, err := s.checkinStore.CheckinByProvider(record.ProviderID)
	if err != nil {
		hadPrevious = false
	}
	if err := s.checkinStore.SaveCheckin(record); err != nil {
		slog.Warn("check-in: save record failed", "provider", provider.ID, "error", err)
		return
	}
	if checkinEventWorthy(previous, hadPrevious, record) {
		s.recordCheckinEvent(provider, record, now)
	}
	switch {
	case record.Outcome == domain.CheckinClaimed && record.Streak > 0:
		slog.Info("check-in: credits claimed", "provider", provider.Name, "credit", record.Credit, "streak", record.Streak)
	case record.Outcome == domain.CheckinClaimed:
		slog.Info("check-in: credits claimed", "provider", provider.Name, "credit", record.Credit)
	case record.Outcome == domain.CheckinFailed:
		slog.Warn("check-in: failed", "provider", provider.Name, "reason", record.Message)
	}
}

// checkinEventWorthy says whether an answer is news: the first one, a new day,
// a different account, a different outcome, a new grant, or a failure that now
// looks different. An offline retry that fails the same way is not — Go's
// transport errors change wording between attempts and would otherwise fill the
// stream with the same outage.
func checkinEventWorthy(previous domain.CheckinRecord, hadPrevious bool, next domain.CheckinRecord) bool {
	if !hadPrevious {
		return true
	}
	if previous.Day != next.Day || previous.AccountKey != next.AccountKey {
		return true
	}
	if previous.Outcome != next.Outcome {
		return true
	}
	switch next.Outcome {
	case domain.CheckinClaimed:
		return previous.Credit != next.Credit
	case domain.CheckinFailed:
		if previous.Offline != next.Offline {
			return true
		}
		return !next.Offline && previous.Message != next.Message
	}
	return false
}

// recordCheckinEvent puts one answer into the event stream as an info event.
// Failures stay info: checkin_failed is the rule that decides when a run of
// them is worth a page.
func (s *Server) recordCheckinEvent(provider domain.Provider, record domain.CheckinRecord, now time.Time) {
	if s.monitoringStore == nil {
		return
	}
	detail := map[string]any{
		"providerId": record.ProviderID,
		"outcome":    record.Outcome,
		"day":        record.Day,
	}
	if vendor, ok := checkinVendorOf(provider); ok {
		detail["vendor"] = vendor
	}
	if record.Credit > 0 {
		detail["credit"] = record.Credit
	}
	if record.Streak > 0 {
		detail["streak"] = record.Streak
	}
	if record.Until != "" {
		detail["validUntil"] = record.Until
	}
	if record.Message != "" {
		detail["message"] = record.Message
	}
	if record.Offline {
		detail["offline"] = true
		detail["tries"] = record.Tries
	}
	at := now.UTC()
	if _, err := s.monitoringStore.InsertMonitorEvent(monitoring.Event{
		Time: at, Kind: monitoring.EventKindCheckin, Severity: monitoring.SeverityInfo,
		TargetType: monitoring.TargetProvider, TargetID: record.ProviderID, TargetName: provider.Name,
		Title: checkinEventTitle(provider, record), Status: monitoring.StatusInfo, Detail: detail,
		FirstSeen: at, LastSeen: at,
	}); err != nil {
		slog.Warn("monitoring check-in event failed", "provider", record.ProviderID, "error", err)
	}
}

// checkinEventTitle names the answer the way the card does: credits in, the day
// already done, or the reason it failed.
func checkinEventTitle(provider domain.Provider, record domain.CheckinRecord) string {
	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = record.ProviderID
	}
	switch record.Outcome {
	case domain.CheckinClaimed:
		if record.Credit > 0 {
			return fmt.Sprintf("%s 签到成功 +%g 积分", name, record.Credit)
		}
		return name + " 签到成功"
	case domain.CheckinDone:
		return name + " 今日已签到"
	case domain.CheckinFailed:
		return name + " 签到失败" + checkinReasonSuffix(record.Message)
	case domain.CheckinIneligible:
		return name + " 不参与每日签到"
	case domain.CheckinInactive:
		return name + " 暂无签到活动"
	case domain.CheckinOwnApp:
		return name + " 需在官方 App 签到"
	}
	return name + " 签到结果：" + record.Outcome
}

// checkinReasonSuffix keeps the title readable: the full reason stays in
// detail.message for the card's tooltip.
func checkinReasonSuffix(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if runes := []rune(message); len(runes) > 60 {
		message = string(runes[:60]) + "…"
	}
	return "：" + message
}

// checkinSettled says whether record answers for accountKey on day: it is that
// account's, of that day, and not a failure due to be tried again (now; soon, as
// on a press, nothing is settled).
func checkinSettled(record domain.CheckinRecord, vendor, accountKey, day string, now time.Time, soon bool) bool {
	if accountKey == "" || record.AccountKey != accountKey {
		return false
	}
	if record.Day != day {
		return false
	}
	if record.Outcome == domain.CheckinFailed {
		if soon {
			return false
		}
		at, err := time.Parse(time.RFC3339, record.At)
		if err != nil || now.Sub(at) >= checkinRetryAfter(record) {
			return false
		}
		return true
	}
	if soon {
		return false
	}
	if vendor != checkinVendorQoder {
		return true
	}
	// Qoder's campaigns page can put a new grant up inside the same day, so a
	// settled answer is only trusted for checkinLook, and a success not at all
	// once its campaign window has ended.
	at, err := time.Parse(time.RFC3339, record.At)
	if err != nil || now.Sub(at) >= checkinLook {
		return false
	}
	if record.CheckedIn() && record.Until != "" {
		until, parseErr := time.Parse(time.RFC3339, record.Until)
		if parseErr != nil || !now.Before(until) {
			return false
		}
	}
	return true
}

// checkinRetryAfter is how long a failure waits to be tried again: half an hour,
// or for one that never reached the vendor, a minute doubled for each attempt
// before it that also failed.
func checkinRetryAfter(record domain.CheckinRecord) time.Duration {
	if !record.Offline {
		return checkinRetry
	}
	tries := record.Tries
	if tries > checkinOfflineCap {
		tries = checkinOfflineCap
	}
	wait := checkinOfflineFirst << tries
	if wait > checkinRetry {
		return checkinRetry
	}
	return wait
}

// checkinAccountKey is the account a provider currently points at. A check-in
// record belongs to it, not to the provider row: signing a card in to a second
// account must not inherit the first account's "today is done".
func checkinAccountKey(provider domain.Provider) (string, error) {
	switch strings.TrimSpace(provider.AuthType) {
	case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
		if provider.QoderPAT == nil {
			return "", errors.New("尚未登录，无法记录签到结果属于哪个账号")
		}
		if key := qoderCheckinAccountKey(qoderSiteOf(provider), *provider.QoderPAT); key != "" {
			return key, nil
		}
		// A connection that never reached the account endpoints has no uid; its
		// provider row is the only thing that can tie an answer to this account.
		return provider.ID, nil
	case domain.AuthTypeWorkBuddy:
		if provider.WorkBuddy == nil {
			return "", errors.New("尚未登录，无法记录签到结果属于哪个账号")
		}
		if uid := strings.TrimSpace(provider.WorkBuddy.UserID); uid != "" {
			return strings.TrimSpace(provider.AuthType) + "|" + uid, nil
		}
		return provider.ID, nil
	}
	return "", errors.New("这个订阅没有每日签到")
}

// checkinUnanswered says whether err came from a request that never got a reply:
// no name lookup, no connection, or one closed in time. Those are worth retrying
// in a minute; an answer that says "no" is not.
func checkinUnanswered(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return err != nil && unanswered.MatchString(err.Error())
}

// unanswered is the words Go's client and a proxy use for a connection that was
// never made.
var unanswered = regexp.MustCompile(`\b(connection refused|no such host|i/o timeout|network is unreachable|no route to host|TLS handshake|EOF)\b`)

// claimCheckin keeps one check-in per provider in flight: the loop and a press
// must not ask the same account twice at once, because a refresh token is spent
// on the first use.
func (s *Server) claimCheckin(providerID string) bool {
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	if s.checkinRunning == nil {
		s.checkinRunning = map[string]bool{}
	}
	if s.checkinRunning[providerID] {
		return false
	}
	s.checkinRunning[providerID] = true
	return true
}

func (s *Server) releaseCheckin(providerID string) {
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	delete(s.checkinRunning, providerID)
}
