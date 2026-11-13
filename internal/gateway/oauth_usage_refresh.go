package gateway

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// StartOAuthUsageBackgroundRefresh keeps Claude/Cursor quota reports warm in
// memory even when nobody has the UI open.
func (s *Server) StartOAuthUsageBackgroundRefresh(ctx context.Context) {
	go func() {
		// Delay first sweep so startup/rebuild work finishes first.
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.refreshAllOAuthUsage(context.Background())
				timer.Reset(time.Hour)
			}
		}
	}()
}

// chatgptTokenProactiveRefreshLead refreshes an access token this far before
// its recorded expiry. OpenAI's refresh tokens are rotating and expire when
// idle (roughly a week in practice): a lazy refresh-on-request design lets
// them go stale on quiet accounts and forces a full re-login. Keeping the
// token in constant rotation avoids that (sub2api runs the same scheme).
const chatgptTokenProactiveRefreshLead = 30 * time.Minute

// StartChatGPTTokenBackgroundRefresh sweeps ChatGPT OAuth providers on a short
// interval and proactively rotates access tokens before expiry, so the refresh
// token never idles long enough to be invalidated server-side.
func (s *Server) StartChatGPTTokenBackgroundRefresh(ctx context.Context) {
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.refreshAllChatGPTTokens()
				timer.Reset(15 * time.Minute)
			}
		}
	}()
}

func (s *Server) refreshAllChatGPTTokens() {
	for _, provider := range s.router.State().Providers {
		if provider.Deleted || provider.Disabled || provider.AuthType != domain.AuthTypeChatGPTOAuth {
			continue
		}
		cred := provider.ChatGPTOAuth
		if cred == nil || strings.TrimSpace(cred.RefreshToken) == "" {
			continue
		}
		expiresAt := strings.TrimSpace(cred.ExpiresAt)
		if expiresAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			continue
		}
		if time.Until(parsed) > chatgptTokenProactiveRefreshLead {
			continue
		}
		refreshed, err := s.ensureFreshChatGPTToken(provider)
		if err != nil {
			slog.Warn("chatgpt oauth background refresh failed", "provider", provider.ID, "error", err)
			continue
		}
		slog.Info("chatgpt oauth token refreshed proactively", "provider", provider.ID, "expiresAt", refreshed.ChatGPTOAuth.ExpiresAt)
	}
}

// claudeTokenProactiveRefreshLead is how early the sweep rotates a Claude
// access token. The request path only refreshes five minutes ahead; a quiet
// account would then let the refresh token sit idle for its whole lifetime,
// and Anthropic retires a refresh token that is not spent, which forces a full
// sign-in. Rotating half an hour early keeps the chain alive.
const claudeTokenProactiveRefreshLead = 30 * time.Minute

// StartClaudeTokenBackgroundRefresh sweeps Claude OAuth providers every 15
// minutes and refreshes a token whose expiry is inside the lead above.
func (s *Server) StartClaudeTokenBackgroundRefresh(ctx context.Context) {
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.refreshAllClaudeTokens()
				timer.Reset(15 * time.Minute)
			}
		}
	}()
}

func (s *Server) refreshAllClaudeTokens() {
	for _, provider := range s.router.State().Providers {
		if provider.Deleted || provider.Disabled || provider.AuthType != domain.AuthTypeClaudeOAuth {
			continue
		}
		cred := provider.ClaudeOAuth
		if cred == nil || strings.TrimSpace(cred.RefreshToken) == "" {
			continue
		}
		expiresAt := strings.TrimSpace(cred.ExpiresAt)
		if expiresAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			continue
		}
		if time.Until(parsed) > claudeTokenProactiveRefreshLead {
			continue
		}
		refreshed, err := s.ensureFreshClaudeTokenWithin(provider, claudeTokenProactiveRefreshLead)
		if err != nil {
			slog.Warn("claude oauth background refresh failed", "provider", provider.ID, "error", err)
			if claudeOAuthRefreshRequiresRelogin(err) {
				s.logs.AddApp("warn", "claude oauth 刷新被拒（invalid_grant），需要重新登录", "provider="+provider.ID)
			}
			continue
		}
		if refreshed.ClaudeOAuth != nil {
			slog.Info("claude oauth token refreshed proactively", "provider", provider.ID, "expiresAt", refreshed.ClaudeOAuth.ExpiresAt)
		}
	}
}

func (s *Server) refreshAllOAuthUsage(ctx context.Context) {
	for _, provider := range s.router.State().Providers {
		if provider.Deleted || provider.Disabled {
			continue
		}
		switch provider.AuthType {
		case domain.AuthTypeClaudeOAuth:
			if provider.ClaudeOAuth != nil && strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) != "" {
				s.refreshClaudeOAuthUsage(provider.ID)
			}
		case domain.AuthTypeCursorOAuth:
			if provider.CursorOAuth != nil && strings.TrimSpace(provider.CursorOAuth.RefreshToken) != "" {
				s.refreshCursorOAuthUsage(provider.ID)
			}
		case domain.AuthTypeChatGPTOAuth:
			if provider.ChatGPTOAuth != nil && strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) != "" {
				s.refreshChatGPTOAuthUsage(provider.ID)
			}
		case domain.AuthTypeQoderPAT, domain.AuthTypeQoderCN:
			// Qoder's usage endpoint needs the device token, not the job token.
			// Keep the read-only snapshot warm for the console and for peer cards.
			if provider.QoderPAT != nil && !provider.QoderPAT.Disconnected && strings.TrimSpace(provider.QoderPAT.DeviceToken) != "" {
				s.refreshQoderUsage(provider.ID)
			}
		case domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI:
			// WorkBuddy records the refresh token's month-long life, not the
			// access token's, so the sweep can only learn the token is dead by
			// asking — and answering that 401 with a refresh is what keeps a
			// signed-in account from silently rotting.
			if provider.WorkBuddy != nil && strings.TrimSpace(provider.WorkBuddy.RefreshToken) != "" {
				s.refreshWorkBuddyUsage(provider.ID)
			}
		default:
			// OpenCode Go 是 API Key Provider，但它的套餐额度也由厂商接口
			// 提供；缓存预热后额度面板和 quota 调度都能直接读。
			if isOpenCodeGoProvider(provider) && strings.TrimSpace(resolveProviderAuth(provider)) != "" {
				s.refreshOpenCodeGoUsage(provider.ID)
			}
			// api_key Zhipu (bigmodel / z.ai) providers expose a coding-plan
			// quota endpoint; keep it warm like the OAuth reports.
			if isZhipuBaseURL(provider.BaseURL) && strings.TrimSpace(resolveProviderAuth(provider)) != "" {
				s.refreshZhipuUsage(provider.ID)
			}
			// DeepSeek exposes an account-balance endpoint on the same terms.
			if isDeepSeekBaseURL(provider.BaseURL) && strings.TrimSpace(resolveProviderAuth(provider)) != "" {
				s.refreshDeepSeekBalance(provider.ID)
			}
		}
	}
}

// refreshOpenCodeGoUsage 强制刷新一次 OpenCode Go 额度缓存。
func (s *Server) refreshOpenCodeGoUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("opencode-go", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshOpenCodeGoUsageHoldingLock(providerID)
}

// maybeRefreshOpenCodeGoUsageAsync 在缓存过期时后台刷新，不阻塞当前请求。
func (s *Server) maybeRefreshOpenCodeGoUsageAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("opencode-go", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshOpenCodeGoUsageHoldingLock(providerID)
	}()
}

// refreshOpenCodeGoUsageHoldingLock 在调用方已持有同一缓存锁时刷新额度。
func (s *Server) refreshOpenCodeGoUsageHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("opencode-go", providerID)
	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled || !isOpenCodeGoProvider(provider) {
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report, err := fetchOpenCodeGoUsage(ctx, provider.BaseURL, apiKey)
	if err != nil {
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}

func (s *Server) refreshDeepSeekBalance(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("deepseek", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshDeepSeekBalanceHoldingLock(providerID)
}

func (s *Server) maybeRefreshDeepSeekBalanceAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("deepseek", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshDeepSeekBalanceHoldingLock(providerID)
	}()
}

func (s *Server) refreshDeepSeekBalanceHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("deepseek", providerID)
	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled {
		return
	}
	if !isDeepSeekBaseURL(provider.BaseURL) {
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report, err := fetchDeepSeekBalance(ctx, provider.BaseURL, apiKey)
	if err != nil {
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	// Cache both success and "unsupported" so relays are not hammered.
	if report.Available || report.Unsupported {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}

func (s *Server) refreshZhipuUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("zhipu", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshZhipuUsageHoldingLock(providerID)
}

func (s *Server) maybeRefreshZhipuUsageAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("zhipu", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshZhipuUsageHoldingLock(providerID)
	}()
}

func (s *Server) refreshZhipuUsageHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("zhipu", providerID)
	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled {
		return
	}
	if !isZhipuBaseURL(provider.BaseURL) {
		return
	}
	apiKey := resolveProviderAuth(provider)
	if strings.TrimSpace(apiKey) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report, err := fetchZhipuUsageForProvider(ctx, provider, apiKey)
	if err != nil {
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	if report.resetsError != "" {
		s.logs.AddApp("info", "zhipu reset list unread", "provider="+providerID+" "+report.resetsError)
	}
	// Cache both success and "unsupported" so we don't keep hammering 按量 keys.
	if report.Available || report.Unsupported {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}

func (s *Server) refreshClaudeOAuthUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("claude", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshClaudeOAuthUsageHoldingLock(providerID)
}

func (s *Server) refreshCursorOAuthUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("cursor", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshCursorOAuthUsageHoldingLock(providerID)
}

func (s *Server) maybeRefreshClaudeOAuthUsageAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("claude", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	// Skip if a fetch is already running (avoid goroutine pile-up under multi-tab stampede).
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshClaudeOAuthUsageHoldingLock(providerID)
	}()
}

func (s *Server) maybeRefreshCursorOAuthUsageAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("cursor", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshCursorOAuthUsageHoldingLock(providerID)
	}()
}

func (s *Server) refreshChatGPTOAuthUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("chatgpt", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshChatGPTOAuthUsageHoldingLock(providerID)
}

// refreshQoderUsage keeps the Qoder credit snapshot warm for the local card
// and for peer provider cards. Qoder usage is read with the device token; PAT-only
// connections have no quota endpoint and are skipped by the caller.
func (s *Server) refreshQoderUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("qoder", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled || !isQoderProvider(provider) || provider.QoderPAT == nil || provider.QoderPAT.Disconnected {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	updated, quota := s.qoderQuotaFor(ctx, provider)
	if quota.Error != "" {
		return
	}
	s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(updated), quota)
}

// refreshWorkBuddyUsage sweeps an account's credit meter, which doubles as the
// health check for its access token: WorkBuddy refuses a revoked token with a
// bare 401 and the reader refreshes it then. The result is not cached — the
// console's usage endpoint reads live — the sweep exists for the repair.
func (s *Server) refreshWorkBuddyUsage(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("workbuddy", providerID)
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled || provider.WorkBuddy == nil {
		return
	}
	before := provider.WorkBuddy.AccessToken
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	quota := s.workBuddyQuota(ctx, provider)
	if quota.Error != "" {
		slog.Debug("workbuddy quota sweep failed", "provider", providerID, "error", quota.Error)
		return
	}
	s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), quota)
	if latest, latestErr := s.router.ProviderByID(providerID); latestErr == nil && latest.WorkBuddy != nil && latest.WorkBuddy.AccessToken != before {
		slog.Info("workbuddy token refreshed after the quota meter refused it", "provider", providerID)
	}
}

func (s *Server) maybeRefreshChatGPTOAuthUsageAsync(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("chatgpt", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}
	if !s.tryLockOAuthUsageFetch(cacheKey) {
		return
	}
	go func() {
		defer s.unlockOAuthUsageFetch(cacheKey)
		s.refreshChatGPTOAuthUsageHoldingLock(providerID)
	}()
}

// refreshClaudeOAuthUsageHoldingLock runs the Claude usage fetch while the
// caller already holds lockOAuthUsageFetch("claude:"+providerID).
func (s *Server) refreshClaudeOAuthUsageHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("claude", providerID)
	// Coalesce stampedes: many UI tabs may race after fresh TTL; only the first
	// holder that still sees a stale cache should hit Anthropic.
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled {
		return
	}
	refreshed, err := s.ensureFreshClaudeToken(provider)
	if err != nil {
		slog.Debug("claude oauth usage refresh skipped", "provider", providerID, "error", err)
		return
	}
	report, err := fetchClaudeOAuthUsage(context.Background(), refreshed.ClaudeOAuth.AccessToken)
	if err != nil {
		slog.Debug("claude oauth usage refresh failed", "provider", providerID, "error", err)
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	if !report.Available && report.Error != "" {
		// A 429 or a refusal comes back as a report, not an error: it is still
		// a failed probe and must not be re-asked on every console poll.
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), errors.New(report.Error))
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}

// refreshCursorOAuthUsageHoldingLock runs the Cursor usage fetch while the
// caller already holds lockOAuthUsageFetch("cursor:"+providerID).
func (s *Server) refreshCursorOAuthUsageHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("cursor", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled {
		return
	}
	refreshed, err := s.ensureFreshCursorToken(provider)
	if err != nil {
		slog.Debug("cursor oauth usage refresh skipped", "provider", providerID, "error", err)
		return
	}
	report, err := fetchCursorOAuthUsage(context.Background(), refreshed.CursorOAuth.AccessToken)
	if err != nil {
		slog.Debug("cursor oauth usage refresh failed", "provider", providerID, "error", err)
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}

func (s *Server) refreshChatGPTOAuthUsageHoldingLock(providerID string) {
	cacheKey := s.oauthUsageCacheKeyFor("chatgpt", providerID)
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil || provider.Disabled {
		return
	}
	refreshed, err := s.ensureFreshChatGPTToken(provider)
	if err != nil {
		slog.Debug("chatgpt oauth usage refresh skipped", "provider", providerID, "error", err)
		return
	}
	report, err := fetchChatGPTOAuthUsage(context.Background(), refreshed)
	if err != nil {
		slog.Debug("chatgpt oauth usage refresh failed", "provider", providerID, "error", err)
		s.oauthUsageCache.setFailureForAccount(cacheKey, oauthUsageAccountID(provider), err)
		return
	}
	s.oauthUsageCache.clearFailure(cacheKey)
	if report.Available {
		s.oauthUsageCache.setForAccount(cacheKey, oauthUsageAccountID(provider), report)
	}
}
