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

// Zhipu (智谱 / bigmodel) coding-plan quota query.
//
// Ported from cc-switch (farion1231/cc-switch,
// src-tauri/src/services/coding_plan.rs). Unlike the Claude/Cursor/ChatGPT
// usage probes in this package, Zhipu is an *API-key* provider, so the quota
// call authenticates with the provider's plain api_key (NO "Bearer " prefix).
//
// Personal plan:  GET {base}/api/monitor/usage/quota/limit
// Team plan:      GET https://open.bigmodel.cn/api/monitor/usage/quota/limit?type=2
//   - headers bigmodel-organization / bigmodel-project
//
// Both share the same response shape, so parsing is shared.
const zhipuQuotaPath = "/api/monitor/usage/quota/limit"

// isZhipuBaseURL reports whether a provider's BaseURL points at Zhipu/bigmodel
// (either open.bigmodel.cn or the api.z.ai international host).
func isZhipuBaseURL(baseURL string) bool {
	u := strings.ToLower(baseURL)
	return strings.Contains(u, "bigmodel.cn") || strings.Contains(u, "z.ai")
}

// ZhipuUsageBucket is one rolling-window quota tier (utilization percentage +
// reset time), matching the shape used by ClaudeOAuthUsageBucket.
type ZhipuUsageBucket struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// ZhipuUsageReport is the client-safe snapshot for a Zhipu coding-plan provider.
type ZhipuUsageReport struct {
	Available bool `json:"available"`
	// Unsupported means this API key is not on a coding-plan product (e.g. 按量
	// 付费). It is NOT an auth/routing failure — forwarding still works.
	Unsupported bool              `json:"unsupported,omitempty"`
	Error       string            `json:"error,omitempty"`
	FetchedAt   string            `json:"fetchedAt,omitempty"`
	Level       string            `json:"level,omitempty"` // plan level from data.level
	FiveHour    *ZhipuUsageBucket `json:"five_hour,omitempty"`
	Weekly      *ZhipuUsageBucket `json:"weekly,omitempty"`
}

func isZhipuNoCodingPlanMessage(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(msg, "coding plan") ||
		strings.Contains(msg, "不存在coding") ||
		strings.Contains(m, "no coding plan") ||
		strings.Contains(msg, "当前用户不存在")
}

// zhipuQuotaBase resolves the host to hit for a personal-plan query. Zhipu ships
// as two presets (open.bigmodel.cn and api.z.ai) sharing the same quota path.
func zhipuQuotaBase(baseURL string) string {
	if strings.Contains(strings.ToLower(baseURL), "bigmodel.cn") {
		return "https://open.bigmodel.cn"
	}
	return "https://api.z.ai"
}

// fetchZhipuUsage queries the personal-plan quota endpoint.
func fetchZhipuUsage(ctx context.Context, baseURL, apiKey string) (ZhipuUsageReport, error) {
	url := zhipuQuotaBase(baseURL) + zhipuQuotaPath
	return zhipuQuotaRequest(ctx, url, apiKey, "", "")
}

// fetchZhipuTeamUsage queries the team-plan quota endpoint. Team plan only
// exists on the CN site; org + project IDs are required.
func fetchZhipuTeamUsage(ctx context.Context, apiKey, organizationID, projectID string) (ZhipuUsageReport, error) {
	apiKey = strings.TrimSpace(apiKey)
	organizationID = strings.TrimSpace(organizationID)
	projectID = strings.TrimSpace(projectID)
	if apiKey == "" || organizationID == "" || projectID == "" {
		return ZhipuUsageReport{Available: false, Error: "Zhipu team plan needs the API key + organization ID + project ID"}, nil
	}
	url := "https://open.bigmodel.cn" + zhipuQuotaPath + "?type=2"
	return zhipuQuotaRequest(ctx, url, apiKey, organizationID, projectID)
}

func zhipuQuotaRequest(ctx context.Context, url, apiKey, organizationID, projectID string) (ZhipuUsageReport, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ZhipuUsageReport{Available: false, Error: "missing api key"}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ZhipuUsageReport{}, err
	}
	req.Header.Set("Authorization", apiKey) // NOTE: Zhipu does NOT use a "Bearer " prefix.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
	if organizationID != "" {
		req.Header.Set("bigmodel-organization", organizationID)
	}
	if projectID != "" {
		req.Header.Set("bigmodel-project", projectID)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Transient transport failure: let caller retry / keep last good value.
		return ZhipuUsageReport{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ZhipuUsageReport{Available: false, Error: fmt.Sprintf("Authentication failed (HTTP %d)", resp.StatusCode)}, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ZhipuUsageReport{}, err // transient read failure
	}
	// Prefer structured JSON parse even on non-2xx: Zhipu returns business
	// errors like "当前用户不存在coding plan" as HTTP 500 with {success:false}.
	if len(body) > 0 && body[0] == '{' {
		var parsed map[string]any
		if json.Unmarshal(body, &parsed) == nil {
			report := zhipuUsageFromBody(parsed)
			if report.Available || report.Unsupported || report.Error != "" {
				return report, nil
			}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ZhipuUsageReport{Available: false, Error: fmt.Sprintf("API error (HTTP %d): %s", resp.StatusCode, string(body))}, nil
	}
	return ZhipuUsageReport{Available: false, Error: "Failed to parse response"}, nil
}

// zhipuUsageFromBody parses the (shared) personal/team response body.
func zhipuUsageFromBody(body map[string]any) ZhipuUsageReport {
	report := ZhipuUsageReport{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	if ok, present := body["success"].(bool); present && !ok {
		msg := stringValue(body["msg"])
		if msg == "" {
			msg = "Unknown error"
		}
		if isZhipuNoCodingPlanMessage(msg) {
			// Pay-as-you-go / non-coding-plan keys: quota UI does not apply.
			report.Unsupported = true
			report.Error = "非智谱编程套餐（按量付费账号无此额度）"
			return report
		}
		report.Error = "API error: " + msg
		return report
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		report.Error = "Missing 'data' field in response"
		return report
	}
	report.Level = stringValue(data["level"])
	report.FiveHour, report.Weekly = parseZhipuTokenTiers(data)
	report.Available = true
	return report
}

// parseZhipuTokenTiers classifies TOKENS_LIMIT entries into the 5-hour and
// weekly buckets. Classification is by the explicit `unit` field (3 = 5h,
// 6 = weekly); entries with an unknown/missing unit fall back to filling the
// still-empty slot, preferring five_hour for entries without a reset time.
func parseZhipuTokenTiers(data map[string]any) (fiveHour, weekly *ZhipuUsageBucket) {
	var unclassified []zhipuEntry

	limits, _ := data["limits"].([]any)
	for _, raw := range limits {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if !strings.EqualFold(stringValue(item["type"]), "TOKENS_LIMIT") {
			continue
		}
		pct, _ := item["percentage"].(float64)
		resetMS, hasReset := item["nextResetTime"].(float64)
		e := zhipuEntry{resetMS: resetMS, hasReset: hasReset, percentage: pct}

		bucket := &ZhipuUsageBucket{Utilization: pct}
		if hasReset {
			if iso := millisToISO8601(int64(resetMS)); iso != "" {
				bucket.ResetsAt = iso
			}
		}
		switch zhipuUnit(item) {
		case 3:
			if fiveHour == nil {
				fiveHour = bucket
				continue
			}
		case 6:
			if weekly == nil {
				weekly = bucket
				continue
			}
		}
		unclassified = append(unclassified, e)
	}

	// Fallback: entries without a reset first, then by ascending reset.
	sortZhipuEntries(unclassified)
	for _, e := range unclassified {
		bucket := &ZhipuUsageBucket{Utilization: e.percentage}
		if e.hasReset {
			if iso := millisToISO8601(int64(e.resetMS)); iso != "" {
				bucket.ResetsAt = iso
			}
		}
		if fiveHour == nil {
			fiveHour = bucket
		} else if weekly == nil {
			weekly = bucket
		}
	}
	return fiveHour, weekly
}

func zhipuUnit(item map[string]any) int {
	if u, ok := item["unit"].(float64); ok {
		return int(u)
	}
	return 0
}

// sortZhipuEntries: entries with no reset time sort first; the rest ascending
// by reset time (matches cc-switch's key (reset.is_some(), reset)).
type zhipuEntry struct {
	resetMS    float64
	hasReset   bool
	percentage float64
}

func sortZhipuEntries(entries []zhipuEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			a, b := entries[j-1], entries[j]
			aKey := zhipuSortKey(a.hasReset, a.resetMS)
			bKey := zhipuSortKey(b.hasReset, b.resetMS)
			if aKey <= bKey {
				break
			}
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

func zhipuSortKey(hasReset bool, resetMS float64) float64 {
	if !hasReset {
		return -1e18 // sort "no reset" first
	}
	return resetMS
}

func millisToISO8601(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
