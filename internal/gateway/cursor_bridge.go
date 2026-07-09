package gateway

import (
	"fmt"

	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func (s *Server) resolveCursorBridgeBaseURL(provider domain.Provider) (string, domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeCursorOAuth {
		return "", provider, fmt.Errorf("provider %q is not a Cursor OAuth provider", provider.ID)
	}
	refreshed, err := s.ensureFreshCursorToken(provider)
	if err != nil {
		return "", provider, err
	}
	if refreshed.CursorOAuth == nil || refreshed.CursorOAuth.AccessToken == "" {
		return "", provider, fmt.Errorf("provider %q has no Cursor OAuth access token", provider.ID)
	}
	if s.cursorBridge == nil {
		return "", provider, fmt.Errorf("cursor bridge is not configured")
	}
	if _, err := s.cursorBridge.EnsureRunning(refreshed.CursorOAuth.AccessToken); err != nil {
		return "", provider, err
	}
	baseURL := s.cursorBridge.BaseURL()
	if baseURL == "" {
		return "", provider, fmt.Errorf("cursor bridge is not running")
	}
	return baseURL, refreshed, nil
}

// SetCursorBridge attaches the local Cursor gRPC bridge manager.
func (s *Server) SetCursorBridge(bridge *cursor.Bridge) {
	s.cursorBridge = bridge
}
