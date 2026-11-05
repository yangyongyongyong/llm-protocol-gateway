package gateway

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The free model's own daily window.
//
// A model served at x0.00 costs the plan nothing, yet the upstream refuses it long
// before any credit is touched:
//
//	429 usage exceeds frequency limit, but don't worry, your usage will reset at
//	2026-10-20 19:47:41 UTC+8, alternatively, you can switch to the other models
//
// That is a separate allowance — per account, per 24 hours, about 1e8 tokens,
// counted as prompt + cache-read + completion (measured and documented in
// magpie-community/plugins packages/workbuddy README §The free model's daily
// window; `deepseek-v4.1-flash-sg` is the paid id of the same model and shares
// the window, so switching ids does not escape it).
//
// Two consequences matter for a gateway, and both were wrong before this file:
//
//  1. A 429 is not the account failing. Before, the generic classifier read 429 as
//     a soft throttle and painted the provider 异常 after a few hits. The window
//     refills on its own; the account is perfectly healthy.
//  2. The reset instant is in the body. There is no need to keep a token ledger
//     and guess: the vendor states exactly when the window reopens, so the model is
//     rested until then and no request is wasted on a refusal we already know.

// workBuddyFrequencyWindowPattern matches the free-model window refusal.
var workBuddyFrequencyWindowPattern = regexp.MustCompile(`(?i)usage exceeds frequency limit`)

// workBuddyFrequencyResetPattern reads the reset instant the vendor states:
//
//	... your usage will reset at 2026-10-20 19:47:41 UTC+8 ...
var workBuddyFrequencyResetPattern = regexp.MustCompile(
	`(?i)reset\s+at\s+(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(?:\s*UTC\s*([+-]?\d{1,2}))?`)

// workBuddyIsFreeWindowRefusal reports whether a body is the free-model window
// refusal rather than a generic throttle.
func workBuddyIsFreeWindowRefusal(body []byte) bool {
	return len(body) > 0 && workBuddyFrequencyWindowPattern.Match(body)
}

// workBuddyFrequencyWindowReset reads the instant the window reopens, or reports
// that the body did not state one.
//
// The vendor writes the offset separately ("UTC+8"), so it is applied by hand
// rather than trusted to a layout. A body with no offset is treated as UTC — the
// conservative reading, since it rests the model no longer than the vendor said.
func workBuddyFrequencyWindowReset(body []byte, now time.Time) (time.Time, bool) {
	if !workBuddyIsFreeWindowRefusal(body) {
		return time.Time{}, false
	}
	match := workBuddyFrequencyResetPattern.FindSubmatch(body)
	if match == nil {
		return time.Time{}, false
	}
	stamp := strings.TrimSpace(string(match[1])) + " " + strings.TrimSpace(string(match[2]))
	parsed, err := time.Parse("2006-01-02 15:04:05", stamp)
	if err != nil {
		return time.Time{}, false
	}
	offsetHours := 0
	if raw := strings.TrimSpace(string(match[3])); raw != "" {
		// strconv rather than the code parser: an offset carries a leading '+'
		// ("UTC+8"), which a bare decimal parser rejects.
		offset, parseErr := strconv.Atoi(raw)
		if parseErr != nil || offset < -12 || offset > 14 {
			return time.Time{}, false
		}
		offsetHours = offset
	}
	location := time.FixedZone("workbuddy", offsetHours*3600)
	instant := time.Date(parsed.Year(), parsed.Month(), parsed.Day(),
		parsed.Hour(), parsed.Minute(), parsed.Second(), 0, location)
	// A reset in the past is not a rest: the vendor's clock and ours drift, and
	// resting on a stale stamp would take a healthy model out of rotation.
	if !instant.After(now) {
		return time.Time{}, false
	}
	return instant, true
}

// workBuddyFreeWindowRestCap bounds how long one stated reset may rest a model.
// The window is 24 hours; anything the vendor says beyond that is our clock
// disagreeing, not a real rest.
const workBuddyFreeWindowRestCap = 24 * time.Hour

// workBuddyFrequencyRest is the rest to record for a free-window refusal: until
// the stated reset, bounded, and never shorter than the model-level default so a
// body without a usable stamp still buys some quiet.
func workBuddyFrequencyRest(body []byte, now time.Time) time.Duration {
	if reset, ok := workBuddyFrequencyWindowReset(body, now); ok {
		rest := reset.Sub(now)
		if rest > workBuddyFreeWindowRestCap {
			return workBuddyFreeWindowRestCap
		}
		return rest
	}
	return workBuddyModelRestBusy
}
