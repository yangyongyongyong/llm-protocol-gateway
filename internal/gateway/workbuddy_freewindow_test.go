package gateway

import (
	"net/http"
	"testing"
	"time"
)

// The vendor's own wording, as captured in the reference README.
const workBuddyFrequencyBody = `{"error":"usage exceeds frequency limit, but don't worry, ` +
	`your usage will reset at 2026-10-20 19:47:41 UTC+8, alternatively, you can switch to the other models to continue using it."}`

// A spent free window is not the account failing. Before this rule a 429 here went
// down the soft-throttle path and rested the whole provider.
func TestFreeWindowRefusalDoesNotBlameTheAccount(t *testing.T) {
	class, reason, ok := classifyWorkBuddyFailure(http.StatusTooManyRequests, []byte(workBuddyFrequencyBody))
	if !ok {
		t.Fatal("the free-window refusal was not classified")
	}
	if class != failoverBusy {
		t.Fatalf("class = %v (%s), want busy", class, reason)
	}
	if reason != "wb_free_model_window" {
		t.Fatalf("reason = %q", reason)
	}
}

// A generic 429 keeps the generic handling: only the stated window earns the
// "account is fine" verdict.
func TestGenericThrottleIsNotReadAsAFreeWindow(t *testing.T) {
	_, _, ok := classifyWorkBuddyFailure(http.StatusTooManyRequests, []byte(`{"error":"rate limit exceeded"}`))
	if ok {
		t.Fatal("a generic 429 was classified by the free-window rule")
	}
}

// The reset instant is stated in the body, with its own offset.
func TestFrequencyWindowResetIsParsedWithItsOffset(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	reset, ok := workBuddyFrequencyWindowReset([]byte(workBuddyFrequencyBody), now)
	if !ok {
		t.Fatal("the stated reset was not parsed")
	}
	// 19:47:41 at UTC+8 is 11:47:41 UTC.
	want := time.Date(2026, 10, 20, 11, 47, 41, 0, time.UTC)
	if !reset.Equal(want) {
		t.Fatalf("reset = %s, want %s", reset.UTC(), want)
	}
}

// A stamp without an offset is read as UTC, which never rests longer than stated.
func TestFrequencyWindowResetWithoutOffsetIsUTC(t *testing.T) {
	body := []byte(`usage exceeds frequency limit, reset at 2026-10-20 19:47:41`)
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	reset, ok := workBuddyFrequencyWindowReset(body, now)
	if !ok {
		t.Fatal("a stamp without an offset was rejected")
	}
	want := time.Date(2026, 10, 20, 19, 47, 41, 0, time.UTC)
	if !reset.Equal(want) {
		t.Fatalf("reset = %s, want %s", reset.UTC(), want)
	}
}

// A reset in the past is clock drift, not a rest.
func TestFrequencyWindowResetIgnoresStampInThePast(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	if _, ok := workBuddyFrequencyWindowReset([]byte(workBuddyFrequencyBody), now); ok {
		t.Fatal("a stale reset was treated as a rest")
	}
}

// The rest is bounded by the window length even if the stamp is far away.
func TestFrequencyRestIsBoundedAndHasAFallback(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	if rest := workBuddyFrequencyRest([]byte(workBuddyFrequencyBody), now); rest != 107*time.Minute+41*time.Second {
		t.Fatalf("rest = %s, want the stated reset minus now", rest)
	}
	faraway := []byte(`usage exceeds frequency limit, reset at 2026-12-31 00:00:00 UTC+8`)
	if rest := workBuddyFrequencyRest(faraway, now); rest != workBuddyFreeWindowRestCap {
		t.Fatalf("rest = %s, want it capped at %s", rest, workBuddyFreeWindowRestCap)
	}
	noStamp := []byte(`usage exceeds frequency limit`)
	if rest := workBuddyFrequencyRest(noStamp, now); rest != workBuddyModelRestBusy {
		t.Fatalf("rest = %s, want the short fallback", rest)
	}
}

// Recording a free-window refusal rests that one model until the stated reset, and
// leaves the account alone.
func TestNoteWorkBuddyModelRestRestsTheModelUntilTheReset(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	if !noteWorkBuddyModelRest("provider-fw", "deepseek-v4.1-flash", http.StatusTooManyRequests,
		[]byte(workBuddyFrequencyBody), now) {
		t.Fatal("the free-window refusal did not rest the model")
	}
	state := workBuddyModelStateOf("provider-fw", "deepseek-v4.1-flash", now)
	if !state.Rested {
		t.Fatal("the model is not resting")
	}
	if !state.RestTill.Equal(time.Date(2026, 10, 20, 11, 47, 41, 0, time.UTC)) {
		t.Fatalf("rest till %s, want the stated reset", state.RestTill.UTC())
	}
	// Once the window reopens the model is usable again without any bookkeeping.
	if workBuddyModelRested("provider-fw", "deepseek-v4.1-flash", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("the model was still rested after the window reopened")
	}
}
