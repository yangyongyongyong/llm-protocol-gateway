package gateway

// WorkBuddy's credit dates.
//
// Two clocks run on a WorkBuddy account, and the console used to show neither.
// A personal account's credits come in packages that each end on their own day,
// so "2 320 / 3 300 used" says nothing about the 18.04 credits that go away on
// November 6 whether they are spent or not. A team account has no packages at
// all — its meter runs on a cycle that starts and resets on a date WorkBuddy
// names — so what it has left resets then.
//
// The package list is a second door: WorkBuddy's billing API refuses a detail
// read unless the request names the package codes its own client sends, so they
// are listed here as the client lists them. The daily slice is left out of the
// answer on purpose: its credits are replaced every midnight on every account,
// so "these expire tomorrow" would tell the user nothing they can act on.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	workBuddySummaryPath       = "/billing/meter/get-user-resource-summary"
	workBuddyEnterprisePath    = "/billing/meter/get-enterprise-user-usage"
	workBuddyPaidPackagesPath  = "/billing/meter/get-user-resource-paid-packages"
	workBuddyFreePackagesPath  = "/billing/meter/get-user-resource-free-packages"
	workBuddyPackagePageSize   = 200
	workBuddyPackageMaxPages   = 10
	workBuddySliceCapacityType = 4
	// workBuddyBeijingClock is how WorkBuddy's billing API writes a moment:
	// Beijing wall clock, no zone.
	workBuddyBeijingClock = "2006-01-02 15:04:05"
)

// The package codes WorkBuddy's client asks its meters with. A detail read
// without them answers 10001 "PackageCodes required".
const workBuddyDailyPackage = "TCACA_code_001_PqouKr6QWV"

var workBuddyPaidPackages = []string{
	"TCACA_code_002_AkiJS3ZHF5", "TCACA_code_005_maRGyrHhw1", "TCACA_code_003_FAnt7lcmRT",
	"TCACA_code_023_4xbGhMrE6q", "TCACA_code_026_BaESVICNoi", "TCACA_code_027_0FCGVA6vSa",
	"TCACA_code_009_0XmEQc2xOf", "TCACA_code_038_OhvqZtiPKr", "TCACA_code_036_lupO5WgNdG",
}

var workBuddyFreePackages = []string{
	workBuddyDailyPackage,
	"TCACA_code_008_cfWoLwvjU4", "TCACA_code_035_ArVxJcGDsm", "TCACA_code_006_DbXS0lrypC",
	"TCACA_code_039_KRcQj7wUat", "TCACA_code_040_mi9rCYg46x", "TCACA_code_007_nzdH5h4Nl0",
	"TCACA_code_028_NtpWi0jzXs", "TCACA_code_037_WxOD3MpI2o", "TCACA_code_029_6wCGEWquYy",
	"TCACA_code_030_BjSt89qTvr",
}

// workBuddyNow is the clock the quota reads use, a var so a test can pin it.
//
// A personal account's credits come in dated batches, and one that is already
// out of time is dropped from the window. So an end-to-end read judged against
// the wall clock turns red on the day its fixture's expiry passes, with nothing
// about the code changed: a fixture ending 2026-11-06 read green until that day
// and red from 2026-11-10 on, blocking every release in between.
var workBuddyNow = time.Now

// workBuddyPackage is one credit package as the billing meter lists it.
type workBuddyPackage struct {
	PackageCode  string           `json:"PackageCode"`
	CapacityType workBuddyNumber  `json:"CapacityType"`
	Remain       *workBuddyNumber `json:"CycleCapacityRemain"`
	// RemainPrecise is the same figure to more places; WorkBuddy sends one, the
	// other, or both, and its client reads the precise one when it is there.
	RemainPrecise *workBuddyNumber `json:"CycleCapacityRemainPrecise"`
	// DeductionEndTime is milliseconds; ExpiredTime and CycleEndTime are the
	// same moment written as Beijing wall clock.
	DeductionEndTime workBuddyNumber `json:"DeductionEndTime"`
	ExpiredTime      string          `json:"ExpiredTime"`
	CycleEndTime     string          `json:"CycleEndTime"`
}

func (p workBuddyPackage) left() float64 {
	if p.RemainPrecise != nil {
		return float64(*p.RemainPrecise)
	}
	if p.Remain != nil {
		return float64(*p.Remain)
	}
	return 0
}

// endsAt is when the package's credits are lost: its own deduction end, else its
// expiry, else the cycle's end — the order WorkBuddy's client reads them in.
func (p workBuddyPackage) endsAt() time.Time {
	if millis := int64(p.DeductionEndTime); millis > 0 {
		return time.UnixMilli(millis)
	}
	for _, value := range []string{p.ExpiredTime, p.CycleEndTime} {
		if at, ok := parseWorkBuddyClock(value); ok {
			return at
		}
	}
	return time.Time{}
}

// parseWorkBuddyClock reads a date WorkBuddy's billing API sends: a Beijing wall
// clock without a zone ("2026-10-13 00:39:27"), or a full RFC3339.
func parseWorkBuddyClock(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	if at, err := time.ParseInLocation(workBuddyBeijingClock, trimmed, beijingZone); err == nil {
		return at, true
	}
	if at, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return at, true
	}
	return time.Time{}, false
}

// workBuddyPackagesCached keeps each account's batch list for a while. The
// console re-reads a card every few minutes, and a batch's end date is a day,
// not a minute: asking WorkBuddy's billing for the whole list that often is only
// traffic. The credits bar itself stays live — at worst the expiry line's amount
// is this stale.
const workBuddyPackagesTTL = 10 * time.Minute

var workBuddyPackagesCache sync.Map // provider id → {at, packages}

type workBuddyPackagesEntry struct {
	at       time.Time
	packages []workBuddyPackage
}

// workBuddyBatches returns the account's batches, asking the meter only when the
// cached list is older than workBuddyPackagesTTL.
func workBuddyBatches(ctx context.Context, site workBuddySite, providerID, base string, headers map[string]string, now time.Time) []workBuddyPackage {
	if cached, ok := workBuddyPackagesCache.Load(providerID); ok {
		if entry, typed := cached.(workBuddyPackagesEntry); typed && now.Sub(entry.at) < workBuddyPackagesTTL {
			return entry.packages
		}
	}
	packages := workBuddyPackagesOf(ctx, site, base, headers, now)
	if len(packages) > 0 {
		// An empty answer is not worth remembering: it is as likely to be a
		// refused door as an account with no batches.
		workBuddyPackagesCache.Store(providerID, workBuddyPackagesEntry{at: now, packages: packages})
	}
	return packages
}

// workBuddyPackagesOf lists every package of the account that is live or used up
// (status 0 and 3), paid and free, as WorkBuddy's own client asks for them. A
// door that answers nothing costs the account no expiry line.
func workBuddyPackagesOf(ctx context.Context, site workBuddySite, base string, headers map[string]string, now time.Time) []workBuddyPackage {
	day := now.In(beijingZone).Format("2006-01-02")
	asks := []struct {
		path string
		body map[string]any
	}{
		{workBuddyPaidPackagesPath, map[string]any{"PackageCodes": workBuddyPaidPackages, "NeedRenewInfo": true}},
		{workBuddyFreePackagesPath, map[string]any{
			"PackageCodes":         workBuddyFreePackages,
			"SlicePeriodStartTime": day + " 00:00:00",
			"SlicePeriodEndTime":   day + " 23:59:59",
		}},
	}
	var out []workBuddyPackage
	for _, ask := range asks {
		for page := 1; page <= workBuddyPackageMaxPages; page++ {
			var list struct {
				Accounts []workBuddyPackage `json:"Accounts"`
			}
			body := map[string]any{"PageNumber": page, "PageSize": workBuddyPackagePageSize, "Status": []int{0, 3}}
			for key, value := range ask.body {
				body[key] = value
			}
			if err := workBuddyCall(ctx, site, http.MethodPost, base+ask.path, headers, body, &list); err != nil {
				break
			}
			out = append(out, list.Accounts...)
			if len(list.Accounts) < workBuddyPackagePageSize {
				break
			}
		}
	}
	return out
}

// workBuddyCreditsExpiry is the soonest moment credits the summary counts are
// lost, and how many of them — the day to spend them, which is not the day the
// window resets. Packages the summary never counted, the daily slice, and
// anything already out of time are left out; two batches ending together add up.
func workBuddyCreditsExpiry(packages []workBuddyPackage, counted map[string]bool, now time.Time) (time.Time, float64) {
	var earliest time.Time
	var amount float64
	for _, item := range packages {
		if !counted[item.PackageCode] || item.PackageCode == workBuddyDailyPackage {
			continue
		}
		if int(float64(item.CapacityType)) == workBuddySliceCapacityType {
			continue
		}
		left := item.left()
		end := item.endsAt()
		if left <= 0 || end.IsZero() || !end.After(now) {
			continue
		}
		switch {
		case earliest.IsZero() || end.Before(earliest):
			earliest, amount = end, left
		case end.Equal(earliest):
			amount += left
		}
	}
	return earliest, amount
}

// workBuddyEnterpriseCycle is a team account's cycle: what it has spent, what it
// was granted, and when that grant resets.
type workBuddyEnterpriseCycle struct {
	Credit   workBuddyNumber `json:"credit"`
	LimitNum workBuddyNumber `json:"limitNum"`
	Resets   string          `json:"cycleResetTime"`
}

// workBuddySummary is the personal meter: the plan's credits as the sum of its
// packages, which ones they are, and whether the account pays for them.
type workBuddySummary struct {
	Packages []struct {
		PackageCode        string
		CycleTotalCapacity workBuddyNumber
		CycleUsedCapacity  workBuddyNumber
	} `json:"Packages"`
	IsPaidUser bool `json:"IsPaidUser"`
}

func (summary workBuddySummary) totals() (total, used float64, counted map[string]bool) {
	counted = map[string]bool{}
	for _, pkg := range summary.Packages {
		total += float64(pkg.CycleTotalCapacity)
		used += float64(pkg.CycleUsedCapacity)
		counted[pkg.PackageCode] = true
	}
	return total, used, counted
}

// workBuddyQuotaWindows turns what the meters and the package list said into the
// card's windows. Split out of the fetch so the dates can be tested without a
// live account.
func workBuddyQuotaWindows(summary workBuddySummary, packages []workBuddyPackage, enterprise workBuddyEnterpriseCycle, now time.Time) (string, []workBuddyQuotaWin) {
	plan := "Free"
	if summary.IsPaidUser {
		plan = "Pro"
	}
	total, used, counted := summary.totals()
	if total > 0 {
		window := workBuddyQuotaWin{Name: "Credits", Used: 100 * used / total, Amount: used, Limit: total, Unit: "credits"}
		if at, amount := workBuddyCreditsExpiry(packages, counted, now); !at.IsZero() {
			window.RunsOutAt = at.UTC().Format(time.RFC3339)
			window.RunsOut = amount
		}
		return plan, []workBuddyQuotaWin{window}
	}
	if limit := float64(enterprise.LimitNum); limit > 0 {
		window := workBuddyQuotaWin{Name: "Credits", Used: 100 * float64(enterprise.Credit) / limit,
			Amount: float64(enterprise.Credit), Limit: limit, Unit: "credits"}
		if at, ok := parseWorkBuddyClock(enterprise.Resets); ok {
			window.ResetsAt = at.UTC().Format(time.RFC3339)
		}
		return "Enterprise", []workBuddyQuotaWin{window}
	}
	return plan, nil
}

// workBuddyTokenRefresher trades the provider's current credential for a new
// one. It is how the quota reader repairs an access token WorkBuddy revoked
// early: the recorded expiry is the refresh token's, so it never predicts this.
type workBuddyTokenRefresher func() (domain.WorkBuddyCredential, error)

// workBuddyQuotaFor reads an account's credit allowance from its resource
// summary, and from the enterprise meter when the summary grants no packages
// (a team account's credits only live there) — with whichever date each of the
// two meters carries. A 401 from any of those calls triggers one refresh and a
// retry; the credential that ended up being used is returned so the caller can
// keep the rotated token.
func workBuddyQuotaFor(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, refresh workBuddyTokenRefresher) (workBuddyQuota, domain.WorkBuddyCredential) {
	now := workBuddyNow()
	quota := workBuddyQuota{User: credential.AccountLabel, Windows: []workBuddyQuotaWin{}, FetchedAt: now.UTC().Format(time.RFC3339)}
	headers := workBuddyAccountHeaders(site, credential)
	base := site.api()
	call := func(path string, dst any) error {
		err := workBuddyCall(ctx, site, http.MethodPost, base+path, headers, map[string]any{}, dst)
		if err == nil || !workBuddyUnauthorized(err) || refresh == nil {
			return err
		}
		updated, refreshErr := refresh()
		if refreshErr != nil {
			return refreshErr
		}
		credential = updated
		headers = workBuddyAccountHeaders(site, credential)
		return workBuddyCall(ctx, site, http.MethodPost, base+path, headers, map[string]any{}, dst)
	}
	var summary workBuddySummary
	if err := call(workBuddySummaryPath, &summary); err != nil {
		quota.Error = err.Error()
		return quota, credential
	}
	total, _, _ := summary.totals()
	var windows []workBuddyQuotaWin
	if total > 0 {
		// Only a personal account's credits come in dated batches.
		key := site.authType + "|" + credential.UserID
		batches := workBuddyBatches(ctx, site, key, base, headers, now)
		quota.Plan, windows = workBuddyQuotaWindows(summary, batches, workBuddyEnterpriseCycle{}, now)
	} else {
		// A team account answers the summary with no packages at all; its credits
		// only live in the enterprise meter.
		var enterprise workBuddyEnterpriseCycle
		_ = call(workBuddyEnterprisePath, &enterprise)
		quota.Plan, windows = workBuddyQuotaWindows(summary, nil, enterprise, now)
	}
	quota.Windows = windows
	return quota, credential
}

// workBuddyQuota reads a provider's allowance with the token its stored
// credential carries, and repairs that token if WorkBuddy refuses it: a refresh
// is spent only when a 401 says the access token is dead, which WorkBuddy does
// whenever the same account signs in elsewhere.
func (s *Server) workBuddyQuota(ctx context.Context, provider domain.Provider) workBuddyQuota {
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok || provider.WorkBuddy == nil {
		return workBuddyQuota{Windows: []workBuddyQuotaWin{}, Error: "WorkBuddy 尚未登录"}
	}
	current := *provider.WorkBuddy
	quota, _ := workBuddyQuotaFor(ctx, site, current, func() (domain.WorkBuddyCredential, error) {
		updated, err := s.forceRefreshWorkBuddyToken(provider.ID)
		if err != nil {
			return current, err
		}
		if updated.WorkBuddy == nil {
			return current, errors.New("WorkBuddy 登录已失效，请在控制台重新登录")
		}
		return *updated.WorkBuddy, nil
	})
	return quota
}
