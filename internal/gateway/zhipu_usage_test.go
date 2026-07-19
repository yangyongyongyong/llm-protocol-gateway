package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseZhipuBody(t *testing.T, raw string) ZhipuUsageReport {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return zhipuUsageFromBody(body)
}

func TestZhipuNewPlanTwoTiersByUnit(t *testing.T) {
	// New plan: two TOKENS_LIMIT, classified by unit (3 = 5h, 6 = weekly).
	// Order in the array is intentionally weekly-first to prove independence.
	report := parseZhipuBody(t, `{
		"success": true,
		"data": {
			"level": "pro",
			"limits": [
				{"type":"TOKENS_LIMIT","percentage":53.0,"unit":6,"nextResetTime":2000000000000},
				{"type":"TOKENS_LIMIT","percentage":44.0,"unit":3,"nextResetTime":1000000000000},
				{"type":"TIME_LIMIT","percentage":7.0}
			]
		}
	}`)
	if !report.Available {
		t.Fatalf("expected available, got error %q", report.Error)
	}
	if report.Level != "pro" {
		t.Fatalf("level = %q, want pro", report.Level)
	}
	if report.FiveHour == nil || report.FiveHour.Utilization != 44.0 {
		t.Fatalf("five_hour = %+v, want 44.0", report.FiveHour)
	}
	if report.Weekly == nil || report.Weekly.Utilization != 53.0 {
		t.Fatalf("weekly = %+v, want 53.0", report.Weekly)
	}
}

func TestZhipuOldPlanSingleTierFallsBackToFiveHour(t *testing.T) {
	report := parseZhipuBody(t, `{
		"data": {
			"limits": [
				{"type":"TOKENS_LIMIT","percentage":2.0,"nextResetTime":1774967594803},
				{"type":"TIME_LIMIT","percentage":0.0}
			]
		}
	}`)
	if report.FiveHour == nil || report.FiveHour.Utilization != 2.0 {
		t.Fatalf("five_hour = %+v, want 2.0", report.FiveHour)
	}
	if report.Weekly != nil {
		t.Fatalf("weekly = %+v, want nil", report.Weekly)
	}
}

func TestZhipuMissingResetIsFiveHourWhenWeeklyHasReset(t *testing.T) {
	// 5h bucket at 0% may have no nextResetTime; must not be misclassified as weekly.
	report := parseZhipuBody(t, `{
		"data": {
			"limits": [
				{"type":"TOKENS_LIMIT","percentage":25.0,"nextResetTime":2000000000000},
				{"type":"TOKENS_LIMIT","percentage":0.0}
			]
		}
	}`)
	if report.FiveHour == nil || report.FiveHour.Utilization != 0.0 {
		t.Fatalf("five_hour = %+v, want 0.0 (no-reset entry)", report.FiveHour)
	}
	if report.Weekly == nil || report.Weekly.Utilization != 25.0 {
		t.Fatalf("weekly = %+v, want 25.0", report.Weekly)
	}
}

func TestZhipuBusinessError(t *testing.T) {
	report := parseZhipuBody(t, `{"success": false, "msg": "invalid key"}`)
	if report.Available {
		t.Fatalf("expected not available")
	}
	if report.Error != "API error: invalid key" {
		t.Fatalf("error = %q", report.Error)
	}
}

func TestZhipuNoCodingPlanIsUnsupportedNotFailure(t *testing.T) {
	report := parseZhipuBody(t, `{"success": false, "msg": "当前用户不存在coding plan"}`)
	if report.Available {
		t.Fatalf("expected not available")
	}
	if !report.Unsupported {
		t.Fatalf("expected unsupported=true for non-coding-plan account, got %+v", report)
	}
	if report.Error == "" || strings.Contains(report.Error, "API error:") {
		t.Fatalf("expected soft unsupported message, got %q", report.Error)
	}
}

func TestZhipuTeamRequiresIDs(t *testing.T) {
	r, err := fetchZhipuTeamUsage(t.Context(), "key", "", "proj")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.Available || r.Error == "" {
		t.Fatalf("expected guidance error, got %+v", r)
	}
}
