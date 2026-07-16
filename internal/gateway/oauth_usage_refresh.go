package gateway

import (
	"context"
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

func (s *Server) refreshAllOAuthUsage(ctx context.Context) {
	for _, provider := range s.router.State().Providers {
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
		}
	}
}

func (s *Server) refreshClaudeOAuthUsage(providerID string) {
	cacheKey := "claude:" + providerID
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshClaudeOAuthUsageHoldingLock(providerID)
}

func (s *Server) refreshCursorOAuthUsage(providerID string) {
	cacheKey := "cursor:" + providerID
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshCursorOAuthUsageHoldingLock(providerID)
}

func (s *Server) maybeRefreshClaudeOAuthUsageAsync(providerID string) {
	cacheKey := "claude:" + providerID
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
	cacheKey := "cursor:" + providerID
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
	cacheKey := "chatgpt:" + providerID
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()
	s.refreshChatGPTOAuthUsageHoldingLock(providerID)
}

func (s *Server) maybeRefreshChatGPTOAuthUsageAsync(providerID string) {
	cacheKey := "chatgpt:" + providerID
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
	cacheKey := "claude:" + providerID
	// Coalesce stampedes: many UI tabs may race after fresh TTL; only the first
	// holder that still sees a stale cache should hit Anthropic.
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
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
		return
	}
	if report.Available {
		s.oauthUsageCache.set(cacheKey, report)
	}
}

// refreshCursorOAuthUsageHoldingLock runs the Cursor usage fetch while the
// caller already holds lockOAuthUsageFetch("cursor:"+providerID).
func (s *Server) refreshCursorOAuthUsageHoldingLock(providerID string) {
	cacheKey := "cursor:" + providerID
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
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
		return
	}
	if report.Available {
		s.oauthUsageCache.set(cacheKey, report)
	}
}

func (s *Server) refreshChatGPTOAuthUsageHoldingLock(providerID string) {
	cacheKey := "chatgpt:" + providerID
	if !s.oauthUsageCache.needsRefresh(cacheKey) {
		return
	}

	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
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
		return
	}
	if report.Available {
		s.oauthUsageCache.set(cacheKey, report)
	}
}
