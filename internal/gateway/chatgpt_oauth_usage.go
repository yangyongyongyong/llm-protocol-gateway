package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const chatgptOAuthUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// ChatGPTOAuthUsageBucket is one display row for ChatGPT/Codex quota.
type ChatGPTOAuthUsageBucket struct {
	Label       string  `json:"label"`
	Utilization float64 `json:"utilization"` // 0-100 used percent
	Detail      string  `json:"detail,omitempty"`
	ResetsAt    string  `json:"resetsAt,omitempty"` // RFC3339 when known
}

// ChatGPTOAuthUsageReport is the client-safe usage snapshot for chatgpt_oauth.
type ChatGPTOAuthUsageReport struct {
	Available bool                       `json:"available"`
	Error     string                     `json:"error,omitempty"`
	FetchedAt string                     `json:"fetchedAt,omitempty"`
	PlanName  string                     `json:"planName,omitempty"`
	Message   string                     `json:"message,omitempty"`
	Buckets   []ChatGPTOAuthUsageBucket  `json:"buckets,omitempty"`
}

type chatgptWhamUsageResponse struct {
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Allowed      bool `json:"allowed"`
		LimitReached bool `json:"limit_reached"`
		PrimaryWindow *struct {
			UsedPercent         float64 `json:"used_percent"`
			LimitWindowSeconds  int64   `json:"limit_window_seconds"`
			ResetAfterSeconds   int64   `json:"reset_after_seconds"`
			ResetAt             int64   `json:"reset_at"`
		} `json:"primary_window"`
		SecondaryWindow *struct {
			UsedPercent         float64 `json:"used_percent"`
			LimitWindowSeconds  int64   `json:"limit_window_seconds"`
			ResetAfterSeconds   int64   `json:"reset_after_seconds"`
			ResetAt             int64   `json:"reset_at"`
		} `json:"secondary_window"`
	} `json:"rate_limit"`
	Credits *struct {
		HasCredits          bool    `json:"has_credits"`
		Unlimited           bool    `json:"unlimited"`
		OverageLimitReached bool    `json:"overage_limit_reached"`
		Balance             *float64 `json:"balance"`
	} `json:"credits"`
}

func formatChatGPTWindowLabel(seconds int64, fallback string) string {
	switch {
	case seconds <= 0:
		return fallback
	case seconds%(24*3600) == 0:
		days := seconds / (24 * 3600)
		if days == 1 {
			return "1 天额度"
		}
		return fmt.Sprintf("%d 天额度", days)
	case seconds%3600 == 0:
		hours := seconds / 3600
		if hours == 1 {
			return "1 小时额度"
		}
		return fmt.Sprintf("%d 小时额度", hours)
	default:
		return fallback
	}
}

func formatChatGPTWindowDetail(seconds int64) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds%(24*3600) == 0:
		days := seconds / (24 * 3600)
		if days == 1 {
			return "统计窗口：1 天"
		}
		return fmt.Sprintf("统计窗口：%d 天", days)
	case seconds%3600 == 0:
		hours := seconds / 3600
		if hours == 1 {
			return "统计窗口：1 小时"
		}
		return fmt.Sprintf("统计窗口：%d 小时", hours)
	case seconds%60 == 0:
		return fmt.Sprintf("统计窗口：%d 分钟", seconds/60)
	default:
		return fmt.Sprintf("统计窗口：%d 秒", seconds)
	}
}

func chatgptResetAtRFC3339(resetAtUnix, resetAfterSeconds int64) string {
	if resetAtUnix > 0 {
		return time.Unix(resetAtUnix, 0).UTC().Format(time.RFC3339)
	}
	if resetAfterSeconds > 0 {
		return time.Now().UTC().Add(time.Duration(resetAfterSeconds) * time.Second).Format(time.RFC3339)
	}
	return ""
}

func buildChatGPTOAuthUsageReport(raw chatgptWhamUsageResponse) ChatGPTOAuthUsageReport {
	plan := strings.TrimSpace(raw.PlanType)
	if plan == "" {
		plan = "unknown"
	}
	report := ChatGPTOAuthUsageReport{
		Available: true,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		PlanName:  plan,
	}
	if raw.RateLimit == nil {
		report.Message = "未返回 rate_limit"
		return report
	}
	if raw.RateLimit.LimitReached || !raw.RateLimit.Allowed {
		report.Message = "额度已用尽或暂时不可用"
	} else {
		report.Message = "可用"
	}

	buckets := make([]ChatGPTOAuthUsageBucket, 0, 3)
	if w := raw.RateLimit.PrimaryWindow; w != nil {
		buckets = append(buckets, ChatGPTOAuthUsageBucket{
			Label:       formatChatGPTWindowLabel(w.LimitWindowSeconds, "主窗口额度"),
			Utilization: clampPercent(w.UsedPercent),
			Detail:      formatChatGPTWindowDetail(w.LimitWindowSeconds),
			ResetsAt:    chatgptResetAtRFC3339(w.ResetAt, w.ResetAfterSeconds),
		})
	}
	if w := raw.RateLimit.SecondaryWindow; w != nil {
		buckets = append(buckets, ChatGPTOAuthUsageBucket{
			Label:       formatChatGPTWindowLabel(w.LimitWindowSeconds, "次窗口额度"),
			Utilization: clampPercent(w.UsedPercent),
			Detail:      formatChatGPTWindowDetail(w.LimitWindowSeconds),
			ResetsAt:    chatgptResetAtRFC3339(w.ResetAt, w.ResetAfterSeconds),
		})
	}
	if raw.Credits != nil {
		detail := "无额外 credits"
		if raw.Credits.Unlimited {
			detail = "credits 无限"
		} else if raw.Credits.HasCredits {
			if raw.Credits.Balance != nil {
				detail = fmt.Sprintf("credits 余额 %.2f", *raw.Credits.Balance)
			} else {
				detail = "有 credits"
			}
		}
		buckets = append(buckets, ChatGPTOAuthUsageBucket{
			Label:       "Credits",
			Utilization: 0,
			Detail:      detail,
		})
	}
	report.Buckets = buckets
	return report
}

func fetchChatGPTOAuthUsage(ctx context.Context, provider domain.Provider) (ChatGPTOAuthUsageReport, error) {
	if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.AccessToken) == "" {
		return ChatGPTOAuthUsageReport{Available: false, Error: "missing oauth access token"}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatgptOAuthUsageURL, nil)
	if err != nil {
		return ChatGPTOAuthUsageReport{}, err
	}
	applyChatGPTCodexHeaders(req, provider)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ChatGPTOAuthUsageReport{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return ChatGPTOAuthUsageReport{Available: false, Error: msg}, nil
	}
	var raw chatgptWhamUsageResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return ChatGPTOAuthUsageReport{}, fmt.Errorf("failed to parse chatgpt oauth usage: %w", err)
	}
	return buildChatGPTOAuthUsageReport(raw), nil
}
