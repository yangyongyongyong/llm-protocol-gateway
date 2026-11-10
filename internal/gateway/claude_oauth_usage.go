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

// ClaudeOAuthUsageLimit is one entry of the usage response's `limits` array.
//
// This is where Anthropic now reports the per-model weekly allowance the app
// shows as 「仅限 Fable」. It used to be a `seven_day_<model>` bucket, but those
// fields come back null on current accounts (measured: seven_day_opus and
// seven_day_sonnet both null while limits carried the Fable window), so a
// reader that only knows the old bucket names shows two bars where the app
// shows three.
type ClaudeOAuthUsageLimit struct {
	// Kind is the wire name: session, weekly_all, weekly_scoped.
	Kind string `json:"kind"`
	// Group is session or weekly — the section the app files it under.
	Group string `json:"group,omitempty"`
	// Percent is how much of this window is spent, 0-100.
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt,omitempty"`
	// Label names the scoped model ("Fable"), empty for unrestricted windows.
	Label string `json:"label,omitempty"`
	// AllowancePercentOfWeekly is the share of the weekly limit this scope may
	// use (weekly_scoped_shares.allowance_percent_of_weekly), 0 when unknown.
	AllowancePercentOfWeekly float64 `json:"allowancePercentOfWeekly,omitempty"`
	// Active marks the window the account is currently spending against.
	Active bool `json:"active,omitempty"`
}

// ClaudeOAuthUsageReport is the client-safe usage snapshot for a claude_oauth provider.
type ClaudeOAuthUsageReport struct {
	Available      bool                    `json:"available"`
	Error          string                  `json:"error,omitempty"`
	FetchedAt      string                  `json:"fetchedAt,omitempty"`
	FiveHour       *ClaudeOAuthUsageBucket `json:"five_hour,omitempty"`
	SevenDay       *ClaudeOAuthUsageBucket `json:"seven_day,omitempty"`
	SevenDayOpus   *ClaudeOAuthUsageBucket `json:"seven_day_opus,omitempty"`
	SevenDaySonnet *ClaudeOAuthUsageBucket `json:"seven_day_sonnet,omitempty"`
	// SevenDayOverageIncluded is the "extra usage over the plan" window. It is
	// the one that actually turns requests away once the plan is spent, so a
	// report that leaves it out hides the limit an operator is looking for.
	SevenDayOverageIncluded *ClaudeOAuthUsageBucket `json:"seven_day_overage_included,omitempty"`
	// Limits is the current, ordered list of windows (session → weekly_all →
	// weekly_scoped). Rendered in this order so the panel reads like the app.
	Limits     []ClaudeOAuthUsageLimit `json:"limits,omitempty"`
	ExtraUsage map[string]any          `json:"extra_usage,omitempty"`
}

// parseClaudeOAuthUsageLimits reads the limits array, folding in the per-scope
// allowance from weekly_scoped_shares (which refers back by limit_index).
func parseClaudeOAuthUsageLimits(raw any, shares any) []ClaudeOAuthUsageLimit {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	allowances := map[int]float64{}
	if list, ok := shares.([]any); ok {
		for _, entry := range list {
			share, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			index, ok := share["limit_index"].(float64)
			if !ok {
				continue
			}
			if percent, ok := share["allowance_percent_of_weekly"].(float64); ok {
				allowances[int(index)] = percent
			}
		}
	}
	out := make([]ClaudeOAuthUsageLimit, 0, len(items))
	for index, entry := range items {
		item, ok := entry.(map[string]any)
		if !ok || item == nil {
			continue
		}
		percent, ok := item["percent"].(float64)
		if !ok {
			continue
		}
		limit := ClaudeOAuthUsageLimit{
			Kind:     strings.TrimSpace(stringValue(item["kind"])),
			Group:    strings.TrimSpace(stringValue(item["group"])),
			Percent:  percent,
			ResetsAt: strings.TrimSpace(stringValue(item["resets_at"])),
			Active:   boolValue(item["is_active"]),
		}
		if scope, ok := item["scope"].(map[string]any); ok {
			if model, ok := scope["model"].(map[string]any); ok {
				limit.Label = strings.TrimSpace(stringValue(model["display_name"]))
				if limit.Label == "" {
					limit.Label = strings.TrimSpace(stringValue(model["id"]))
				}
			}
		}
		if allowance, ok := allowances[index]; ok {
			limit.AllowancePercentOfWeekly = allowance
		}
		out = append(out, limit)
	}
	return out
}

// boolValue reads a JSON boolean leniently.
func boolValue(raw any) bool {
	if value, ok := raw.(bool); ok {
		return value
	}
	return false
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
	request.Header.Set("User-Agent", "claude-code/"+claudeOAuthBillingVersion)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("anthropic-beta", claudeOAuthBetaHeader)

	client := &http.Client{Timeout: 20 * time.Second}
	response, err := guardUpstreamIdleClient(client, request)
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
		Available:      true,
		FetchedAt:      time.Now().UTC().Format(time.RFC3339),
		FiveHour:       parseClaudeOAuthUsageBucket(payload["five_hour"]),
		SevenDay:       parseClaudeOAuthUsageBucket(payload["seven_day"]),
		SevenDayOpus:   parseClaudeOAuthUsageBucket(payload["seven_day_opus"]),
		SevenDaySonnet: parseClaudeOAuthUsageBucket(payload["seven_day_sonnet"]),

		SevenDayOverageIncluded: parseClaudeOAuthUsageBucket(payload["seven_day_overage_included"]),
		Limits:                  parseClaudeOAuthUsageLimits(payload["limits"], payload["weekly_scoped_shares"]),
	}
	if extraUsage, ok := payload["extra_usage"].(map[string]any); ok && len(extraUsage) > 0 {
		report.ExtraUsage = extraUsage
	}
	if report.FiveHour == nil && report.SevenDay == nil && report.SevenDayOpus == nil && report.SevenDaySonnet == nil &&
		report.SevenDayOverageIncluded == nil && len(report.Limits) == 0 {
		report.Available = false
		report.Error = "usage response did not include known buckets"
	}
	return report, nil
}
