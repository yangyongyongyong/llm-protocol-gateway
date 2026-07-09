package gateway

import "testing"

func TestParseCursorMillisTimestamp(t *testing.T) {
	got := parseCursorMillisTimestamp("1785736990000")
	if got == "" {
		t.Fatalf("expected rfc3339 timestamp")
	}
	if !stringsHasPrefix(got, "2026-") {
		t.Fatalf("unexpected timestamp %q", got)
	}
}

func TestFormatCursorCents(t *testing.T) {
	if formatCursorCents(2000) != "$20" {
		t.Fatalf("got %q", formatCursorCents(2000))
	}
	if formatCursorCents(3701) != "$37.01" {
		t.Fatalf("got %q", formatCursorCents(3701))
	}
}

func TestBuildCursorUsageReportFromPeriod(t *testing.T) {
	report := buildCursorUsageReportFromPeriod(cursorPeriodUsageResponse{
		BillingCycleEnd: "1785736990000",
		DisplayMessage:  "You've hit your usage limit",
		PlanUsage: &struct {
			TotalSpend       int     `json:"totalSpend"`
			IncludedSpend    int     `json:"includedSpend"`
			BonusSpend       int     `json:"bonusSpend"`
			Limit            int     `json:"limit"`
			AutoPercentUsed  float64 `json:"autoPercentUsed"`
			APIPercentUsed   float64 `json:"apiPercentUsed"`
			TotalPercentUsed float64 `json:"totalPercentUsed"`
		}{
			TotalSpend:       3701,
			IncludedSpend:    2000,
			BonusSpend:       1701,
			Limit:            2000,
			AutoPercentUsed:  24.1,
			APIPercentUsed:   1.6,
			TotalPercentUsed: 18.9,
		},
	})
	if !report.Available || len(report.Buckets) != 3 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Buckets[0].Utilization != 18.9 {
		t.Fatalf("total utilization = %v", report.Buckets[0].Utilization)
	}
}

func TestBuildCursorUsageReportFromAuth(t *testing.T) {
	limit := 500
	report := buildCursorUsageReportFromAuth(cursorAuthUsageResponse{
		StartOfMonth: "2026-07-03T06:03:10.000Z",
		GPT4: &struct {
			NumRequests      int  `json:"numRequests"`
			NumRequestsTotal int  `json:"numRequestsTotal"`
			MaxRequestUsage  *int `json:"maxRequestUsage"`
		}{
			NumRequests:     150,
			MaxRequestUsage: &limit,
		},
	})
	if !report.Available || len(report.Buckets) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Buckets[0].Utilization != 30 {
		t.Fatalf("utilization = %v", report.Buckets[0].Utilization)
	}
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
