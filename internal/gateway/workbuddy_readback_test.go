package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// readbackServer fakes the report channel and the streak oracle. newRewardsServer
// already points the site globals at its fake host, so the cleanup it returns is the
// only thing this helper needs to hand back.
func readbackServer(t *testing.T, streakDays string) func() {
	t.Helper()
	_, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		switch {
		case strings.Contains(path, "/report"):
			return http.StatusOK, `{"code":0}`
		case strings.HasSuffix(path, "/activity/growth/streak"):
			return http.StatusOK, `{"code":0,"data":{"streak":{"days":` + streakDays + `}}}`
		}
		return http.StatusOK, `{"code":0}`
	})
	return cleanup
}

// A report that came back 200 but left the streak at zero is the silently-dropped
// shape, and it has to be visible: every later step depends on that number.
func TestReportActivityVerifiedFlagsAZeroStreak(t *testing.T) {
	cleanup := readbackServer(t, `"0"`)
	defer cleanup()
	gateway := &Server{}
	readback, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-1",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !readback.Reported {
		t.Fatal("the report was not recorded as sent")
	}
	if readback.StreakDays != 0 {
		t.Fatalf("streak = %d", readback.StreakDays)
	}
	if !strings.Contains(readback.Note, "疑似 200 未计分") {
		t.Fatalf("note = %q, want the silent-drop warning", readback.Note)
	}
}

// Once a baseline exists, a second pass that did not move the number says so — and
// an advancing one is reported as advanced.
func TestReportActivityVerifiedComparesAgainstTheBaseline(t *testing.T) {
	gateway := &Server{}

	cleanup := readbackServer(t, `"5"`)
	first, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-2",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	cleanup()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.HasBaseline {
		t.Fatal("the first pass invented a baseline")
	}
	if first.StreakDays != 5 {
		t.Fatalf("streak = %d, want 5", first.StreakDays)
	}

	cleanup = readbackServer(t, `"5"`)
	second, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-2",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	cleanup()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.HasBaseline || second.Advanced {
		t.Fatalf("second = %+v, want no advance", second)
	}
	if !strings.Contains(second.Note, "本次未推进") {
		t.Fatalf("note = %q", second.Note)
	}

	cleanup = readbackServer(t, `"6"`)
	third, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-2",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	cleanup()
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if !third.Advanced || third.Previous != 5 || third.StreakDays != 6 {
		t.Fatalf("third = %+v, want 5 -> 6", third)
	}
	if third.Note != "" {
		t.Fatalf("an advancing streak left a note: %q", third.Note)
	}
	if days, ok := workBuddyStreakBaselineOf("provider-rb-2"); !ok || days != 6 {
		t.Fatalf("baseline = %d ok=%v, want 6", days, ok)
	}
}

// The report failing is an error; the oracle failing is not — the report still went
// through and the caller has nothing to retry.
func TestReportActivityVerifiedSeparatesReportFromOracle(t *testing.T) {
	gateway := &Server{}

	refusing, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		if strings.Contains(path, "/report") {
			return http.StatusForbidden, `{"code":403,"msg":"refused"}`
		}
		return http.StatusOK, `{"code":0,"data":{"streak":{"days":3}}}`
	})
	_ = refusing
	if _, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-3",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"}); err == nil {
		cleanup()
		t.Fatal("a refused report was not returned as an error")
	}
	cleanup()

	oracleDown, cleanup2 := newRewardsServer(t, func(method, path string) (int, string) {
		if strings.Contains(path, "/report") {
			return http.StatusOK, `{"code":0}`
		}
		return http.StatusNotFound, `{"code":404,"msg":"Route Not Found"}`
	})
	_ = oracleDown
	defer cleanup2()
	readback, err := gateway.workBuddyReportActivityVerified(context.Background(), "provider-rb-4",
		workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if err != nil {
		t.Fatalf("an unavailable oracle failed the step: %v", err)
	}
	if !readback.Reported {
		t.Fatal("the report was not recorded as sent")
	}
	if !strings.Contains(readback.Note, "无法回读") {
		t.Fatalf("note = %q, want the read-back failure note", readback.Note)
	}
}
