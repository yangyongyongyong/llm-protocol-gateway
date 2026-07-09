package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	cursorOAuthUsageURL           = "https://api2.cursor.sh/auth/usage"
	cursorOAuthPeriodUsageURL     = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	cursorOAuthUsageConnectHeader = "1"
)

// CursorOAuthUsageBucket is one display row for Cursor subscription usage.
type CursorOAuthUsageBucket struct {
	Label       string  `json:"label"`
	Utilization float64 `json:"utilization"` // 0-100 used percent
	Detail      string  `json:"detail,omitempty"`
	ResetsAt    string  `json:"resetsAt,omitempty"` // RFC3339 when known
}

// CursorOAuthUsageReport is the client-safe usage snapshot for a cursor_oauth provider.
type CursorOAuthUsageReport struct {
	Available bool                     `json:"available"`
	Error     string                   `json:"error,omitempty"`
	FetchedAt string                   `json:"fetchedAt,omitempty"`
	PlanName  string                   `json:"planName,omitempty"`
	Message   string                   `json:"message,omitempty"`
	Buckets   []CursorOAuthUsageBucket `json:"buckets,omitempty"`
}

type cursorPeriodUsageResponse struct {
	BillingCycleStart string `json:"billingCycleStart"`
	BillingCycleEnd   string `json:"billingCycleEnd"`
	DisplayMessage    string `json:"displayMessage"`
	AutoModelSelectedDisplayMessage  string `json:"autoModelSelectedDisplayMessage"`
	NamedModelSelectedDisplayMessage string `json:"namedModelSelectedDisplayMessage"`
	PlanUsage *struct {
		TotalSpend       int     `json:"totalSpend"`
		IncludedSpend    int     `json:"includedSpend"`
		BonusSpend       int     `json:"bonusSpend"`
		Limit            int     `json:"limit"`
		AutoPercentUsed  float64 `json:"autoPercentUsed"`
		APIPercentUsed   float64 `json:"apiPercentUsed"`
		TotalPercentUsed float64 `json:"totalPercentUsed"`
	} `json:"planUsage"`
}

type cursorAuthUsageResponse struct {
	StartOfMonth string `json:"startOfMonth"`
	GPT4         *struct {
		NumRequests     int  `json:"numRequests"`
		NumRequestsTotal int `json:"numRequestsTotal"`
		MaxRequestUsage *int `json:"maxRequestUsage"`
	} `json:"gpt-4"`
}

func parseCursorMillisTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var millis int64
	if _, err := fmt.Sscanf(raw, "%d", &millis); err != nil || millis <= 0 {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
		return ""
	}
	// Cursor sometimes returns seconds; treat small values as seconds.
	if millis < 1_000_000_000_000 {
		millis *= 1000
	}
	return time.UnixMilli(millis).UTC().Format(time.RFC3339)
}

func formatCursorCents(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func fetchCursorPeriodUsage(ctx context.Context, accessToken string) (cursorPeriodUsageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorOAuthPeriodUsageURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return cursorPeriodUsageResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", cursorOAuthUsageConnectHeader)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cursorPeriodUsageResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return cursorPeriodUsageResponse{}, fmt.Errorf("cursor period usage failed: %s", message)
	}
	var parsed cursorPeriodUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return cursorPeriodUsageResponse{}, fmt.Errorf("failed to parse cursor period usage: %w", err)
	}
	return parsed, nil
}

func fetchCursorAuthUsage(ctx context.Context, accessToken string) (cursorAuthUsageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cursorOAuthUsageURL, nil)
	if err != nil {
		return cursorAuthUsageResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cursorAuthUsageResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return cursorAuthUsageResponse{}, fmt.Errorf("cursor auth usage failed: %s", message)
	}
	var parsed cursorAuthUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return cursorAuthUsageResponse{}, fmt.Errorf("failed to parse cursor auth usage: %w", err)
	}
	return parsed, nil
}

func buildCursorUsageReportFromPeriod(period cursorPeriodUsageResponse) CursorOAuthUsageReport {
	if period.PlanUsage == nil {
		return CursorOAuthUsageReport{Available: false, Error: "period usage missing planUsage"}
	}
	plan := period.PlanUsage
	resetsAt := parseCursorMillisTimestamp(period.BillingCycleEnd)
	includedDetail := fmt.Sprintf("含额已用 %s / %s", formatCursorCents(plan.IncludedSpend), formatCursorCents(plan.Limit))
	if plan.BonusSpend > 0 {
		includedDetail += fmt.Sprintf(" · 赠送 %s", formatCursorCents(plan.BonusSpend))
	}
	buckets := []CursorOAuthUsageBucket{
		{
			Label:       "总用量",
			Utilization: clampPercent(plan.TotalPercentUsed),
			Detail:      includedDetail,
			ResetsAt:    resetsAt,
		},
		{
			Label:       "Auto 模型",
			Utilization: clampPercent(plan.AutoPercentUsed),
			Detail:      strings.TrimSpace(period.AutoModelSelectedDisplayMessage),
			ResetsAt:    resetsAt,
		},
		{
			Label:       "API 模型",
			Utilization: clampPercent(plan.APIPercentUsed),
			Detail:      strings.TrimSpace(period.NamedModelSelectedDisplayMessage),
			ResetsAt:    resetsAt,
		},
	}
	message := strings.TrimSpace(period.DisplayMessage)
	if message == "" {
		message = strings.TrimSpace(period.AutoModelSelectedDisplayMessage)
	}
	return CursorOAuthUsageReport{
		Available: true,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		PlanName:  "Cursor",
		Message:   message,
		Buckets:   buckets,
	}
}

func buildCursorUsageReportFromAuth(auth cursorAuthUsageResponse) CursorOAuthUsageReport {
	if auth.GPT4 == nil || auth.GPT4.MaxRequestUsage == nil || *auth.GPT4.MaxRequestUsage <= 0 {
		return CursorOAuthUsageReport{Available: false, Error: "auth usage missing request quota"}
	}
	limit := *auth.GPT4.MaxRequestUsage
	used := auth.GPT4.NumRequests
	if auth.GPT4.NumRequestsTotal > used {
		used = auth.GPT4.NumRequestsTotal
	}
	utilization := clampPercent(float64(used) / float64(limit) * 100)
	resetsAt := ""
	if strings.TrimSpace(auth.StartOfMonth) != "" {
		if t, err := time.Parse(time.RFC3339, auth.StartOfMonth); err == nil {
			// Approximate next reset as one month after startOfMonth.
			resetsAt = t.AddDate(0, 1, 0).UTC().Format(time.RFC3339)
		} else if t, err := time.Parse(time.RFC3339Nano, auth.StartOfMonth); err == nil {
			resetsAt = t.AddDate(0, 1, 0).UTC().Format(time.RFC3339)
		}
	}
	return CursorOAuthUsageReport{
		Available: true,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		PlanName:  "Cursor",
		Buckets: []CursorOAuthUsageBucket{{
			Label:       "Fast 请求",
			Utilization: utilization,
			Detail:      fmt.Sprintf("%d / %d", used, limit),
			ResetsAt:    resetsAt,
		}},
	}
}

func fetchCursorOAuthUsage(ctx context.Context, accessToken string) (CursorOAuthUsageReport, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return CursorOAuthUsageReport{Available: false, Error: "missing oauth access token"}, nil
	}

	period, periodErr := fetchCursorPeriodUsage(ctx, accessToken)
	if periodErr == nil && period.PlanUsage != nil {
		return buildCursorUsageReportFromPeriod(period), nil
	}

	auth, authErr := fetchCursorAuthUsage(ctx, accessToken)
	if authErr == nil {
		report := buildCursorUsageReportFromAuth(auth)
		if report.Available {
			return report, nil
		}
		if periodErr != nil {
			return CursorOAuthUsageReport{Available: false, Error: periodErr.Error()}, nil
		}
		return report, nil
	}

	if periodErr != nil {
		return CursorOAuthUsageReport{Available: false, Error: periodErr.Error()}, nil
	}
	return CursorOAuthUsageReport{Available: false, Error: authErr.Error()}, nil
}
