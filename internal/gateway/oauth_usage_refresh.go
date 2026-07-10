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
		}
	}
}

func (s *Server) refreshClaudeOAuthUsage(providerID string) {
	cacheKey := "claude:" + providerID
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()

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

func (s *Server) refreshCursorOAuthUsage(providerID string) {
	cacheKey := "cursor:" + providerID
	unlock := s.lockOAuthUsageFetch(cacheKey)
	defer unlock()

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

func (s *Server) maybeRefreshClaudeOAuthUsageAsync(providerID string) {
	if !s.oauthUsageCache.needsRefresh("claude:" + providerID) {
		return
	}
	go s.refreshClaudeOAuthUsage(providerID)
}

func (s *Server) maybeRefreshCursorOAuthUsageAsync(providerID string) {
	if !s.oauthUsageCache.needsRefresh("cursor:" + providerID) {
		return
	}
	go s.refreshCursorOAuthUsage(providerID)
}
