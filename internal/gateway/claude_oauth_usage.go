package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const claudeOAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"

// ClaudeOAuthUsageBucket mirrors Anthropic's undocumented OAuth usage bucket.
type ClaudeOAuthUsageBucket struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// ClaudeOAuthUsageReport is the client-safe usage snapshot for a claude_oauth provider.
type ClaudeOAuthUsageReport struct {
	Available  bool                              `json:"available"`
	Error      string                            `json:"error,omitempty"`
	FetchedAt  string                            `json:"fetchedAt,omitempty"`
	FiveHour   *ClaudeOAuthUsageBucket           `json:"five_hour,omitempty"`
	SevenDay   *ClaudeOAuthUsageBucket           `json:"seven_day,omitempty"`
	SevenDayOpus   *ClaudeOAuthUsageBucket       `json:"seven_day_opus,omitempty"`
	SevenDaySonnet *ClaudeOAuthUsageBucket       `json:"seven_day_sonnet,omitempty"`
	ExtraUsage map[string]any                    `json:"extra_usage,omitempty"`
}

func parseClaudeOAuthUsageBucket(raw any) *ClaudeOAuthUsageBucket {
	item, ok := raw.(map[string]any)
	if !ok || item == nil {
		return nil
	}
	utilization, ok := item["utilization"].(float64)
	if !ok {
		return nil
	}
	bucket := &ClaudeOAuthUsageBucket{Utilization: utilization}
	if resetsAt := stringValue(item["resets_at"]); resetsAt != "" {
		bucket.ResetsAt = resetsAt
	}
	return bucket
}

func fetchClaudeOAuthUsage(ctx context.Context, accessToken string) (ClaudeOAuthUsageReport, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return ClaudeOAuthUsageReport{Available: false, Error: "missing oauth access token"}, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeOAuthUsageURL, nil)
	if err != nil {
		return ClaudeOAuthUsageReport{}, err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "claude-code/2.0.32")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("anthropic-beta", claudeOAuthBetaHeader)

	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return ClaudeOAuthUsageReport{}, err
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		return ClaudeOAuthUsageReport{Available: false, Error: message}, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ClaudeOAuthUsageReport{}, fmt.Errorf("failed to parse claude oauth usage response: %w", err)
	}

	report := ClaudeOAuthUsageReport{
		Available: true,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		FiveHour:  parseClaudeOAuthUsageBucket(payload["five_hour"]),
		SevenDay:  parseClaudeOAuthUsageBucket(payload["seven_day"]),
		SevenDayOpus:   parseClaudeOAuthUsageBucket(payload["seven_day_opus"]),
		SevenDaySonnet: parseClaudeOAuthUsageBucket(payload["seven_day_sonnet"]),
	}
	if extraUsage, ok := payload["extra_usage"].(map[string]any); ok && len(extraUsage) > 0 {
		report.ExtraUsage = extraUsage
	}
	if report.FiveHour == nil && report.SevenDay == nil && report.SevenDayOpus == nil && report.SevenDaySonnet == nil {
		report.Available = false
		report.Error = "usage response did not include known buckets"
	}
	return report, nil
}
