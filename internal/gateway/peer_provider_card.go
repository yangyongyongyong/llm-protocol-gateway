package gateway

import (
	"fmt"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// peerProviderCardFor 生成可以安全传给下游的 Provider 卡片快照。
// 这里刻意只读内存缓存，不在下游拉清单时访问 WorkBuddy/Anthropic 等厂商：
// 卡片一致性不应该把父网关变成额度接口放大器的上游。
func (s *Server) peerProviderCardFor(provider domain.Provider, todayTokens *int64) *domain.PeerProviderCard {
	authType := strings.TrimSpace(provider.AuthType)
	if authType == "" {
		authType = domain.AuthTypeAPIKey
	}
	return &domain.PeerProviderCard{
		AuthType:     authType,
		Vendor:       providerVendorKey(provider),
		AccountLabel: peerProviderAccountLabel(provider),
		Connected:    peerProviderConnected(provider),
		Usage:        s.peerProviderUsageFor(provider),
		TodayTokens:  todayTokens,
	}
}

// peerProviderAccountLabel 返回上游卡片的账号标识。只挑非秘密字段，
// 与前端 providerAccountLabel 的展示口径保持一致。
func peerProviderAccountLabel(provider domain.Provider) string {
	pick := func(label, id string) string {
		label = strings.TrimSpace(label)
		id = strings.TrimSpace(id)
		tail := ""
		if id != "" {
			id = strings.ReplaceAll(id, "-", "")
			if len(id) > 8 {
				id = id[:8]
			}
			tail = "#" + id
		}
		if label != "" && !peerGenericAccountLabel(label) {
			if tail != "" && len([]rune(label)) <= 3 && strings.Trim(label, "0123456789") == "" {
				return tail
			}
			return label
		}
		return tail
	}
	switch {
	case provider.WorkBuddy != nil:
		return pick(provider.WorkBuddy.AccountLabel, provider.WorkBuddy.UserID)
	case provider.QoderPAT != nil:
		return pick(firstNonEmpty(provider.QoderPAT.Email, provider.QoderPAT.AccountLabel), provider.QoderPAT.UID)
	case provider.ClaudeOAuth != nil:
		return pick(provider.ClaudeOAuth.AccountLabel, "")
	case provider.CursorOAuth != nil:
		return pick(provider.CursorOAuth.AccountLabel, "")
	case provider.ChatGPTOAuth != nil:
		return pick(provider.ChatGPTOAuth.AccountLabel, provider.ChatGPTOAuth.ChatGPTAccountID)
	case provider.ZCode != nil:
		return pick(provider.ZCode.AccountLabel, "")
	case provider.Trae != nil:
		return pick(provider.Trae.AccountLabel, provider.Trae.UserID)
	default:
		return ""
	}
}

// peerGenericAccountLabel 过滤“昵称就是品牌名”这种没有区分度的标签。
func peerGenericAccountLabel(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "cursor", "qoder", "workbuddy", "trae", "zcode", "anthropic", "openai":
		return true
	default:
		return false
	}
}

// peerProviderConnected 表示上游凭据当前处于已连接状态。对 API Key 类
// Provider 没有“未连接”概念，保持 false，界面不会多画一个连接徽章。
func peerProviderConnected(provider domain.Provider) bool {
	switch {
	case provider.WorkBuddy != nil:
		return strings.TrimSpace(provider.WorkBuddy.AccessToken) != "" && strings.TrimSpace(provider.WorkBuddy.RefreshToken) != ""
	case provider.ClaudeOAuth != nil:
		return strings.TrimSpace(provider.ClaudeOAuth.AccessToken) != "" && strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) != ""
	case provider.CursorOAuth != nil:
		return strings.TrimSpace(provider.CursorOAuth.AccessToken) != "" && strings.TrimSpace(provider.CursorOAuth.RefreshToken) != ""
	case provider.ChatGPTOAuth != nil:
		return strings.TrimSpace(provider.ChatGPTOAuth.AccessToken) != "" && strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) != ""
	case provider.QoderPAT != nil:
		return strings.TrimSpace(provider.QoderPAT.AccessToken) != "" && !provider.QoderPAT.Disconnected
	case provider.ZCode != nil:
		return strings.TrimSpace(provider.ZCode.APIKey) != "" && !provider.ZCode.Disconnected
	case provider.Trae != nil:
		return strings.TrimSpace(provider.Trae.AccessToken) != "" && !provider.Trae.Disconnected
	default:
		return false
	}
}

// peerProviderUsageFor 从父网关已有的额度缓存生成只读卡片数据。缓存未 warm
// 时返回 nil，下游继续显示 Provider 本体，不会把“没取到”画成 0%。
func (s *Server) peerProviderUsageFor(provider domain.Provider) *domain.PeerProviderUsage {
	usage := func(title, fetchedAt string, buckets []domain.PeerProviderUsageBucket) *domain.PeerProviderUsage {
		if len(buckets) == 0 && fetchedAt == "" {
			return nil
		}
		return &domain.PeerProviderUsage{Title: title, FetchedAt: fetchedAt, Buckets: buckets}
	}
	cached := func(prefix string) (any, bool) {
		if s == nil || s.oauthUsageCache == nil {
			return nil, false
		}
		return s.oauthUsageCache.getAllowStale(s.oauthUsageCacheKeyFor(prefix, provider.ID))
	}

	switch {
	case provider.WorkBuddy != nil:
		value, ok := cached("workbuddy")
		report, typeOK := value.(workBuddyQuota)
		if !ok || !typeOK || report.Error != "" {
			return nil
		}
		buckets := make([]domain.PeerProviderUsageBucket, 0, len(report.Windows))
		for _, window := range report.Windows {
			note := fmt.Sprintf("%.0f / %.0f %s", window.Amount, window.Limit, window.Unit)
			if window.RunsOut > 0 {
				note += fmt.Sprintf(" · %.0f %s 先到期", window.RunsOut, window.Unit)
			}
			buckets = append(buckets, domain.PeerProviderUsageBucket{
				Label: window.Name, Utilization: window.Used, Note: note, ResetsAt: window.ResetsAt,
			})
		}
		result := usage("WorkBuddy 套餐额度", report.FetchedAt, buckets)
		if result != nil {
			result.Plan = report.Plan
		}
		return result

	case isQoderProvider(provider):
		value, ok := cached("qoder")
		quota, typeOK := value.(qoderQuota)
		if !ok || !typeOK {
			return nil
		}
		buckets := make([]domain.PeerProviderUsageBucket, 0, len(quota.Windows))
		for _, window := range quota.Windows {
			note := ""
			if window.Limit > 0 {
				note = fmt.Sprintf("%.0f / %.0f %s", window.Amount, window.Limit, window.Unit)
			}
			buckets = append(buckets, domain.PeerProviderUsageBucket{
				Label: window.Name, Utilization: window.Used, Note: note, ResetsAt: window.ResetsAt,
			})
		}
		result := usage("Qoder 积分", quota.FetchedAt, buckets)
		if result != nil {
			result.Plan = quota.Plan
			result.Message = quota.Error
		}
		return result

	case provider.ZCode != nil || isZhipuBaseURL(provider.BaseURL):
		value, ok := cached("zhipu")
		report, typeOK := value.(ZhipuUsageReport)
		if !ok || !typeOK || !report.Available {
			return nil
		}
		var buckets []domain.PeerProviderUsageBucket
		if report.FiveHour != nil {
			buckets = append(buckets, domain.PeerProviderUsageBucket{Label: "5 小时额度", Utilization: report.FiveHour.Utilization, ResetsAt: report.FiveHour.ResetsAt})
		}
		if report.Weekly != nil {
			buckets = append(buckets, domain.PeerProviderUsageBucket{Label: "每周额度", Utilization: report.Weekly.Utilization, ResetsAt: report.Weekly.ResetsAt})
		}
		result := usage("智谱编程套餐额度", report.FetchedAt, buckets)
		if result != nil {
			result.Plan = report.Level
		}
		return result

	case isDeepSeekBaseURL(provider.BaseURL):
		value, ok := cached("deepseek")
		report, typeOK := value.(DeepSeekBalanceReport)
		if !ok || !typeOK || !report.Available || report.Unsupported {
			return nil
		}
		parts := make([]string, 0, len(report.BalanceInfos))
		for _, info := range report.BalanceInfos {
			parts = append(parts, strings.TrimSpace(info.Currency+" "+info.TotalBalance))
		}
		return &domain.PeerProviderUsage{
			Title: "DeepSeek 账户余额", FetchedAt: report.FetchedAt, Balance: strings.Join(parts, " · "),
		}

	case isOpenCodeGoProvider(provider):
		value, ok := cached("opencode-go")
		report, typeOK := value.(OpenCodeGoUsageReport)
		if !ok || !typeOK || !report.Available {
			return nil
		}
		var buckets []domain.PeerProviderUsageBucket
		for _, item := range []struct {
			label  string
			bucket *OpenCodeGoUsageBucket
		}{{"5 小时额度", report.FiveHour}, {"周额度", report.Weekly}, {"月额度", report.Monthly}} {
			if item.bucket != nil {
				buckets = append(buckets, domain.PeerProviderUsageBucket{Label: item.label, Utilization: item.bucket.Utilization, ResetsAt: item.bucket.ResetsAt})
			}
		}
		return usage("OpenCode Go 套餐额度", report.FetchedAt, buckets)

	case provider.ClaudeOAuth != nil:
		value, ok := cached("claude")
		report, typeOK := value.(ClaudeOAuthUsageReport)
		if !ok || !typeOK || !report.Available {
			return nil
		}
		var buckets []domain.PeerProviderUsageBucket
		if len(report.Limits) > 0 {
			for _, limit := range report.Limits {
				label := "7 天额度"
				if limit.Kind == "session" {
					label = "5 小时额度"
				} else if limit.Label != "" {
					label = "7 天 " + limit.Label
				}
				buckets = append(buckets, domain.PeerProviderUsageBucket{Label: label, Utilization: limit.Percent, ResetsAt: limit.ResetsAt})
			}
		} else {
			if report.FiveHour != nil {
				buckets = append(buckets, domain.PeerProviderUsageBucket{Label: "5 小时额度", Utilization: report.FiveHour.Utilization, ResetsAt: report.FiveHour.ResetsAt})
			}
			if report.SevenDay != nil {
				buckets = append(buckets, domain.PeerProviderUsageBucket{Label: "7 天额度", Utilization: report.SevenDay.Utilization, ResetsAt: report.SevenDay.ResetsAt})
			}
		}
		return usage("Claude 订阅额度", report.FetchedAt, buckets)

	case provider.ChatGPTOAuth != nil:
		value, ok := cached("chatgpt")
		report, typeOK := value.(ChatGPTOAuthUsageReport)
		if !ok || !typeOK || !report.Available {
			return nil
		}
		var buckets []domain.PeerProviderUsageBucket
		for _, bucket := range report.Buckets {
			buckets = append(buckets, domain.PeerProviderUsageBucket{Label: bucket.Label, Utilization: bucket.Utilization, Note: bucket.Detail, ResetsAt: bucket.ResetsAt})
		}
		result := usage("ChatGPT Codex 额度", report.FetchedAt, buckets)
		if result != nil {
			result.Plan = report.PlanName
			result.Message = report.Message
		}
		return result

	case provider.CursorOAuth != nil:
		value, ok := cached("cursor")
		report, typeOK := value.(CursorOAuthUsageReport)
		if !ok || !typeOK || !report.Available {
			return nil
		}
		var buckets []domain.PeerProviderUsageBucket
		for _, bucket := range report.Buckets {
			buckets = append(buckets, domain.PeerProviderUsageBucket{Label: bucket.Label, Utilization: bucket.Utilization, Note: bucket.Detail, ResetsAt: bucket.ResetsAt})
		}
		result := usage("Cursor 订阅额度", report.FetchedAt, buckets)
		if result != nil {
			result.Plan = report.PlanName
			result.Message = report.Message
		}
		return result
	}
	return nil
}
