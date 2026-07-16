package gateway

import (
	"context"
	"fmt"

	"github.com/luca/llm-protocol-gateway/internal/cursor"
	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func (s *Server) resolveCursorBridgeBaseURL(ctx context.Context, provider domain.Provider) (string, domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeCursorOAuth {
		return "", provider, fmt.Errorf("provider %q is not a Cursor OAuth provider", provider.ID)
	}
	neededRefresh := provider.CursorOAuth != nil && cursorTokenNeedsRefresh(provider.CursorOAuth)
	refreshed, err := s.ensureFreshCursorToken(provider)
	if err != nil {
		return "", provider, err
	}
	if neededRefresh {
		markTimingFlag(ctx, timingFlagOAuthRefresh)
		markTimingFlag(ctx, timingFlagSaveState)
	}
	if refreshed.CursorOAuth == nil || refreshed.CursorOAuth.AccessToken == "" {
		return "", provider, fmt.Errorf("provider %q has no Cursor OAuth access token", provider.ID)
	}
	if s.cursorBridge == nil {
		return "", provider, fmt.Errorf("cursor bridge is not configured")
	}
	if _, started, err := s.cursorBridge.EnsureRunning(refreshed.CursorOAuth.AccessToken); err != nil {
		return "", provider, err
	} else if started {
		markTimingFlag(ctx, timingFlagCursorBridgeStart)
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

// StopCursorBridge stops the managed bridge subprocess and its health watch.
func (s *Server) StopCursorBridge() {
	if s.cursorBridge != nil {
		s.cursorBridge.Stop()
	}
}

// cursorBridgeRuntime returns the live bridge snapshot for /__state.
func (s *Server) cursorBridgeRuntime() *domain.CursorBridgeRuntime {
	if s.cursorBridge == nil {
		return &domain.CursorBridgeRuntime{Status: cursor.BridgeStatusStopped}
	}
	snap := s.cursorBridge.Snapshot()
	return &domain.CursorBridgeRuntime{
		Status:    snap.Status,
		Port:      snap.Port,
		PID:       snap.PID,
		Message:   snap.Message,
		StartedAt: snap.StartedAt,
		CheckedAt: snap.CheckedAt,
	}
}
