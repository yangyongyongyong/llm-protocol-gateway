package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func workBuddyNumberPtr(value float64) *workBuddyNumber {
	number := workBuddyNumber(value)
	return &number
}

// pinWorkBuddyClock freezes the quota reader's clock for one test.
func pinWorkBuddyClock(t *testing.T, at time.Time) {
	t.Helper()
	original := workBuddyNow
	workBuddyNow = func() time.Time { return at }
	t.Cleanup(func() { workBuddyNow = original })
}

func TestParseWorkBuddyClock(t *testing.T) {
	// WorkBuddy writes its billing dates as Beijing wall clock with no zone.
	at, ok := parseWorkBuddyClock("2026-10-13 00:39:27")
	if !ok {
		t.Fatal("the plain clock form must read")
	}
	if want := time.Date(2026, 10, 12, 16, 39, 27, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("clock read as %s, want %s", at.UTC(), want)
	}
	if _, ok = parseWorkBuddyClock("2026-11-06T15:39:00Z"); !ok {
		t.Fatal("an RFC3339 date must read too")
	}
	for _, junk := range []string{"", "   ", "soon", "2026-13-45 99:99:99"} {
		if _, ok = parseWorkBuddyClock(junk); ok {
			t.Fatalf("%q must not read as a date", junk)
		}
	}
}

func TestWorkBuddyCreditsExpiryPicksTheSoonestBatch(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	in := func(d time.Duration) string { return now.Add(d).In(beijingZone).Format(workBuddyBeijingClock) }
	paid := "TCACA_code_002_AkiJS3ZHF5"
	extra := "TCACA_code_005_maRGyrHhw1"
	packages := []workBuddyPackage{
		// The daily slice and a capacity type 4 batch are replaced for everyone
		// at midnight: they tell the user nothing to act on.
		{PackageCode: workBuddyDailyPackage, Remain: workBuddyNumberPtr(100), ExpiredTime: in(time.Hour)},
		{PackageCode: paid, CapacityType: workBuddyNumber(workBuddySliceCapacityType), Remain: workBuddyNumberPtr(50), ExpiredTime: in(2 * time.Hour)},
		// Already gone.
		{PackageCode: paid, Remain: workBuddyNumberPtr(9), ExpiredTime: in(-time.Hour)},
		// Nothing left to lose.
		{PackageCode: paid, Remain: workBuddyNumberPtr(0), ExpiredTime: in(time.Hour)},
		// The summary never counted this one.
		{PackageCode: "TCACA_code_099_unknown", Remain: workBuddyNumberPtr(7), ExpiredTime: in(3 * time.Hour)},
		// The two that count: the earlier wins, and a tie adds up.
		{PackageCode: paid, RemainPrecise: workBuddyNumberPtr(18.04), ExpiredTime: in(17 * 24 * time.Hour)},
		{PackageCode: extra, Remain: workBuddyNumberPtr(31.96), ExpiredTime: in(17 * 24 * time.Hour)},
	}
	counted := map[string]bool{paid: true, extra: true, workBuddyDailyPackage: true}
	at, amount := workBuddyCreditsExpiry(packages, counted, now)
	if at.IsZero() {
		t.Fatal("expected an expiry")
	}
	if want := now.Add(17 * 24 * time.Hour); at.Sub(want) > time.Minute || want.Sub(at) > time.Minute {
		t.Fatalf("expiry = %s, want about %s", at, want)
	}
	if amount < 49.99 || amount > 50.01 {
		t.Fatalf("amount = %v, want the two batches that end together (50)", amount)
	}
}

func TestWorkBuddyQuotaWindowsCarryTheirOwnDate(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	paid := "TCACA_code_002_AkiJS3ZHF5"
	summary := workBuddySummary{IsPaidUser: true}
	summary.Packages = append(summary.Packages, struct {
		PackageCode        string
		CycleTotalCapacity workBuddyNumber
		CycleUsedCapacity  workBuddyNumber
	}{PackageCode: paid, CycleTotalCapacity: workBuddyNumber(300), CycleUsedCapacity: workBuddyNumber(100)})
	plan, windows := workBuddyQuotaWindows(summary, []workBuddyPackage{
		{PackageCode: paid, RemainPrecise: workBuddyNumberPtr(18.04), ExpiredTime: now.Add(17 * 24 * time.Hour).In(beijingZone).Format(workBuddyBeijingClock)},
	}, workBuddyEnterpriseCycle{}, now)
	if plan != "Pro" || len(windows) != 1 {
		t.Fatalf("plan=%q windows=%+v", plan, windows)
	}
	if windows[0].RunsOutAt == "" || windows[0].RunsOut != 18.04 {
		t.Fatalf("the batch date is missing: %+v", windows[0])
	}
	if _, err := time.Parse(time.RFC3339, windows[0].RunsOutAt); err != nil {
		t.Fatalf("RunsOutAt is not RFC3339: %q", windows[0].RunsOutAt)
	}

	// A team account answers the summary with no packages; its cycle reset comes
	// from the enterprise meter instead.
	enterprise := workBuddyEnterpriseCycle{Credit: workBuddyNumber(40), LimitNum: workBuddyNumber(100), Resets: "2026-11-01 00:00:00"}
	plan, windows = workBuddyQuotaWindows(workBuddySummary{}, nil, enterprise, now)
	if plan != "Enterprise" || len(windows) != 1 || windows[0].Used != 40 {
		t.Fatalf("enterprise shape: plan=%q windows=%+v", plan, windows)
	}
	if windows[0].ResetsAt == "" || windows[0].RunsOutAt != "" {
		t.Fatalf("an enterprise window resets, it does not expire in batches: %+v", windows[0])
	}

	// Nothing counted by either meter: no window, and no invented date.
	plan, windows = workBuddyQuotaWindows(workBuddySummary{}, nil, workBuddyEnterpriseCycle{}, now)
	if plan != "Free" || windows != nil {
		t.Fatalf("empty account: plan=%q windows=%+v", plan, windows)
	}
}

// WorkBuddy revokes an access token when the same account signs in elsewhere,
// and the stored expiry — the refresh token's month — never shows it. The quota
// reader has to answer that 401 with one refresh and retry, keeping the token.
func TestWorkBuddyQuotaRefreshesARevokedToken(t *testing.T) {
	summaryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workBuddySummaryPath:
			summaryCalls++
			if strings.Contains(r.Header.Get("Authorization"), "stale") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("<html>gateway</html>"))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"IsPaidUser":true,"Packages":[{"PackageCode":"tc-x","CycleTotalCapacity":"100","CycleUsedCapacity":"40"}]}}`))
		case workBuddyPaidPackagesPath, workBuddyFreePackagesPath:
			_, _ = w.Write([]byte(`{"code":0,"data":{"Accounts":[]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	site, _ := workBuddySiteForAuthType(domain.AuthTypeWorkBuddy)
	site.endpoint = func() string { return server.URL }

	refreshes := 0
	quota, credential := workBuddyQuotaFor(context.Background(), site,
		domain.WorkBuddyCredential{AccessToken: "stale", UserID: "u-revoked"}, func() (domain.WorkBuddyCredential, error) {
			refreshes++
			return domain.WorkBuddyCredential{AccessToken: "fresh", UserID: "u-revoked"}, nil
		})
	if refreshes != 1 || summaryCalls != 2 {
		t.Fatalf("refreshes=%d summaryCalls=%d, want exactly one refresh and a retry", refreshes, summaryCalls)
	}
	if credential.AccessToken != "fresh" {
		t.Fatalf("credential = %+v, want the rotated token", credential)
	}
	if quota.Error != "" || quota.Plan != "Pro" {
		t.Fatalf("quota = %+v", quota)
	}
}

// The package door is only opened for an account whose credits come in batches,
// and only with the codes WorkBuddy's client names — a detail read without them
// answers 10001.
func TestWorkBuddyQuotaAsksThePackageDoorOnlyWhenItCounts(t *testing.T) {
	// The paid batch below ends 2026-11-06 15:39 Beijing. Read against the wall
	// clock, this test went red the day that date passed — pin the clock to the
	// morning of it so the fixture means the same thing every run.
	pinWorkBuddyClock(t, time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC))
	cases := []struct {
		name        string
		summary     string
		enterprise  string
		wantPackage bool
		wantPlan    string
	}{
		{"personal", `{"IsPaidUser":true,"Packages":[{"PackageCode":"TCACA_code_002_AkiJS3ZHF5","CycleTotalCapacity":"300","CycleUsedCapacity":"100"}]}`, "", true, "Pro"},
		{"team", `{"IsPaidUser":true,"Packages":[]}`, `{"credit":"40","limitNum":"100","cycleResetTime":"2026-11-01 00:00:00"}`, false, "Enterprise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked := []string{}
			var paidBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				body, _ := io.ReadAll(r.Body)
				switch r.URL.Path {
				case workBuddySummaryPath:
					_, _ = w.Write([]byte(`{"code":0,"data":` + tc.summary + `}`))
				case workBuddyPaidPackagesPath:
					_ = json.Unmarshal(body, &paidBody)
					_, _ = w.Write([]byte(`{"code":0,"data":{"Accounts":[{"PackageCode":"TCACA_code_002_AkiJS3ZHF5","CycleCapacityRemainPrecise":"18.04","ExpiredTime":"2026-11-06 15:39:00"}]}}`))
				case workBuddyFreePackagesPath:
					_, _ = w.Write([]byte(`{"code":0,"data":{"Accounts":[]}}`))
				case workBuddyEnterprisePath:
					_, _ = w.Write([]byte(`{"code":0,"data":` + tc.enterprise + `}`))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			site, _ := workBuddySiteForAuthType(domain.AuthTypeWorkBuddy)
			site.endpoint = func() string { return server.URL }
			quota, _ := workBuddyQuotaFor(context.Background(), site, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}, nil)
			if quota.Error != "" {
				t.Fatalf("quota error: %s", quota.Error)
			}
			if quota.Plan != tc.wantPlan {
				t.Fatalf("plan = %q, want %q", quota.Plan, tc.wantPlan)
			}
			if strings.Join(asked, ",") == "" {
				t.Fatal("no meter was asked")
			}
			gotPackages := false
			for _, path := range asked {
				if path == workBuddyPaidPackagesPath {
					gotPackages = true
				}
			}
			if gotPackages != tc.wantPackage {
				t.Fatalf("package door asked = %v, want %v (%v)", gotPackages, tc.wantPackage, asked)
			}
			if tc.wantPackage {
				codes, _ := paidBody["PackageCodes"].([]any)
				if len(codes) != len(workBuddyPaidPackages) {
					t.Fatalf("the paid read must name its codes, got %v", paidBody["PackageCodes"])
				}
				if len(quota.Windows) != 1 || quota.Windows[0].RunsOut != 18.04 {
					t.Fatalf("windows = %+v", quota.Windows)
				}
				// "2026-11-06 15:39" is Beijing wall clock; the card gets UTC.
				at, err := time.Parse(time.RFC3339, quota.Windows[0].RunsOutAt)
				if err != nil || at.UTC().Hour() != 7 {
					t.Fatalf("RunsOutAt = %q, want 2026-11-06T07:39:00Z", quota.Windows[0].RunsOutAt)
				}
			} else if len(quota.Windows) != 1 || quota.Windows[0].ResetsAt == "" {
				t.Fatalf("team windows = %+v", quota.Windows)
			}
		})
	}
}
