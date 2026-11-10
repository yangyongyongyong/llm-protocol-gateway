package gateway

import (
	"testing"
)

// The per-model weekly allowance the Claude app shows as 「仅限 Fable」 is only in
// the limits array: seven_day_opus / seven_day_sonnet come back null on current
// accounts (measured against api/oauth/usage). A reader that knows only the old
// buckets draws two bars where the app draws three.
func TestParseClaudeOAuthUsageLimitsReadsScopedWeeklyWindow(t *testing.T) {
	raw := []any{
		map[string]any{
			"kind": "session", "group": "session", "percent": 0.0,
			"resets_at": "2026-11-10T06:59:59.773406+00:00", "scope": nil, "is_active": false,
		},
		map[string]any{
			"kind": "weekly_all", "group": "weekly", "percent": 25.0,
			"resets_at": "2026-11-13T19:59:59.773426+00:00", "scope": nil, "is_active": true,
		},
		map[string]any{
			"kind": "weekly_scoped", "group": "weekly", "percent": 0.0,
			"resets_at": "2026-11-13T20:00:00+00:00",
			"scope":     map[string]any{"model": map[string]any{"id": nil, "display_name": "Fable"}, "surface": nil},
			"is_active": false,
		},
	}
	shares := []any{
		map[string]any{"limit_index": 2.0, "used_percent_of_weekly": 0.0, "allowance_percent_of_weekly": 50.0},
	}

	limits := parseClaudeOAuthUsageLimits(raw, shares)
	if len(limits) != 3 {
		t.Fatalf("limits = %#v, want all three windows", limits)
	}
	if limits[0].Kind != "session" || limits[0].Percent != 0 {
		t.Fatalf("session window = %#v", limits[0])
	}
	if limits[1].Kind != "weekly_all" || limits[1].Percent != 25 || !limits[1].Active {
		t.Fatalf("weekly_all window = %#v", limits[1])
	}
	scoped := limits[2]
	if scoped.Kind != "weekly_scoped" || scoped.Label != "Fable" {
		t.Fatalf("scoped window = %#v, want the Fable label", scoped)
	}
	if scoped.AllowancePercentOfWeekly != 50 {
		t.Fatalf("allowance = %v, want 50 from weekly_scoped_shares", scoped.AllowancePercentOfWeekly)
	}
}

// Without a display name the scope falls back to the model id, so a new scoped
// window still gets a usable label instead of an empty one.
func TestParseClaudeOAuthUsageLimitsFallsBackToModelID(t *testing.T) {
	raw := []any{
		map[string]any{
			"kind": "weekly_scoped", "percent": 5.0,
			"scope": map[string]any{"model": map[string]any{"id": "claude-mythos-5"}, "surface": nil},
		},
	}
	limits := parseClaudeOAuthUsageLimits(raw, nil)
	if len(limits) != 1 || limits[0].Label != "claude-mythos-5" {
		t.Fatalf("limits = %#v, want the model id as the label", limits)
	}
}

// Entries without a percentage are not windows; skipping them keeps the panel
// from rendering a bar that stands for nothing.
func TestParseClaudeOAuthUsageLimitsSkipsEntriesWithoutPercent(t *testing.T) {
	raw := []any{
		map[string]any{"kind": "session"},
		map[string]any{"kind": "weekly_all", "percent": 12.0},
	}
	limits := parseClaudeOAuthUsageLimits(raw, nil)
	if len(limits) != 1 || limits[0].Kind != "weekly_all" {
		t.Fatalf("limits = %#v, want only the entry carrying a percent", limits)
	}
}

// The quota windows keep the old keys intact (keepalive and cooldown read them)
// and append the limit windows under their own keys.
func TestClaudeQuotaWindowsAppendLimitWindowsWithoutDisturbingOldKeys(t *testing.T) {
	report := ClaudeOAuthUsageReport{
		Available: true,
		FiveHour:  &ClaudeOAuthUsageBucket{Utilization: 0, ResetsAt: "2026-11-10T06:59:59Z"},
		SevenDay:  &ClaudeOAuthUsageBucket{Utilization: 25, ResetsAt: "2026-11-13T19:59:59Z"},
		Limits: []ClaudeOAuthUsageLimit{
			{Kind: "session", Percent: 0},
			{Kind: "weekly_all", Percent: 25},
			{Kind: "weekly_scoped", Percent: 0, Label: "Fable"},
		},
	}

	windows := claudeQuotaWindows(report)
	keys := make([]string, 0, len(windows))
	for _, window := range windows {
		keys = append(keys, window.Key)
	}
	want := []string{"five_hour", "seven_day", "limit_session", "limit_weekly_all", "limit_weekly_scoped_fable"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
	for index := range want {
		if keys[index] != want[index] {
			t.Fatalf("keys = %#v, want %#v", keys, want)
		}
	}
}
