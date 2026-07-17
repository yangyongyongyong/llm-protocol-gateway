package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Cursor subscription OAuth constants (opencode-cursor / auth2api prior art).
const (
	cursorOAuthLoginURL   = "https://cursor.com/loginDeepControl"
	cursorOAuthPollURL    = "https://api2.cursor.sh/auth/poll"
	cursorOAuthRefreshURL = "https://api2.cursor.sh/auth/exchange_user_api_key"
	cursorTokenExpirySkew = 5 * time.Minute
	cursorOAuthPendingTTL = 15 * time.Minute
	cursorPollMaxAttempts = 150
)

type cursorOAuthPending struct {
	Verifier  string
	UUID      string
	FlowID    string
	CreatedAt time.Time
}

type cursorOAuthFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type cursorOAuthPendingStore struct {
	mu       sync.Mutex
	pending  map[string]cursorOAuthPending
	statuses map[string]cursorOAuthFlowStatus
}

func newCursorOAuthPendingStore() *cursorOAuthPendingStore {
	return &cursorOAuthPendingStore{
		pending:  map[string]cursorOAuthPending{},
		statuses: map[string]cursorOAuthFlowStatus{},
	}
}

func (store *cursorOAuthPendingStore) put(providerID string, entry cursorOAuthPending) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pending[providerID] = entry
}

func (store *cursorOAuthPendingStore) get(providerID string) (cursorOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return cursorOAuthPending{}, false
	}
	if time.Since(entry.CreatedAt) > cursorOAuthPendingTTL {
		delete(store.pending, providerID)
		return cursorOAuthPending{}, false
	}
	return entry, true
}

func (store *cursorOAuthPendingStore) take(providerID string) (cursorOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return cursorOAuthPending{}, false
	}
	delete(store.pending, providerID)
	if time.Since(entry.CreatedAt) > cursorOAuthPendingTTL {
		return cursorOAuthPending{}, false
	}
	return entry, true
}

func (store *cursorOAuthPendingStore) finish(providerID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.pending, providerID)
}

func (store *cursorOAuthPendingStore) setStatus(flowID, status, message string) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.statuses[flowID] = cursorOAuthFlowStatus{
		Status:    status,
		Message:   message,
		UpdatedAt: time.Now(),
	}
}

func (store *cursorOAuthPendingStore) status(flowID string) (cursorOAuthFlowStatus, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.statuses[flowID]
	return entry, ok
}

func generateCursorPKCE() (verifier string, challenge string, err error) {
	verifierBytes := make([]byte, 96)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func generateCursorOAuthUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func generateCursorOAuthFlowID() (string, error) {
	flowBytes := make([]byte, 16)
	if _, err := rand.Read(flowBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(flowBytes), nil
}

func buildCursorLoginURL(challenge, uuid string) string {
	params := url.Values{}
	params.Set("challenge", challenge)
	params.Set("uuid", uuid)
	params.Set("mode", "login")
	params.Set("redirectTarget", "cli")
	return cursorOAuthLoginURL + "?" + params.Encode()
}

type cursorPollResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

type cursorRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func cursorTokenExpiry(accessToken string) time.Time {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return time.Now().Add(time.Hour)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(time.Hour)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Now().Add(time.Hour)
	}
	return time.Unix(claims.Exp, 0).Add(-cursorTokenExpirySkew)
}

func pollCursorAuth(ctx context.Context, uuid, verifier string) (domain.CursorOAuthCredential, error) {
	delay := time.Second
	consecutiveErrors := 0
	client := &http.Client{Timeout: 30 * time.Second}

	for attempt := 0; attempt < cursorPollMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return domain.CursorOAuthCredential{}, ctx.Err()
		case <-time.After(delay):
		}

		pollURL := fmt.Sprintf("%s?uuid=%s&verifier=%s", cursorOAuthPollURL, url.QueryEscape(uuid), url.QueryEscape(verifier))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return domain.CursorOAuthCredential{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= 3 {
				return domain.CursorOAuthCredential{}, fmt.Errorf("cursor auth polling failed: %w", err)
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			consecutiveErrors = 0
			if delay < 10*time.Second {
				delay = time.Duration(float64(delay) * 1.2)
				if delay > 10*time.Second {
					delay = 10 * time.Second
				}
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return domain.CursorOAuthCredential{}, fmt.Errorf("cursor auth poll returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var parsed cursorPollResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return domain.CursorOAuthCredential{}, err
		}
		if strings.TrimSpace(parsed.AccessToken) == "" {
			return domain.CursorOAuthCredential{}, fmt.Errorf("cursor auth poll returned empty access token")
		}
		refresh := strings.TrimSpace(parsed.RefreshToken)
		return domain.CursorOAuthCredential{
			AccessToken:  strings.TrimSpace(parsed.AccessToken),
			RefreshToken: refresh,
			ExpiresAt:    cursorTokenExpiry(parsed.AccessToken).UTC().Format(time.RFC3339),
			AccountLabel: "Cursor",
		}, nil
	}
	return domain.CursorOAuthCredential{}, fmt.Errorf("cursor authentication polling timeout")
}

func refreshCursorOAuthToken(refreshToken string) (domain.CursorOAuthCredential, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return domain.CursorOAuthCredential{}, fmt.Errorf("cursor refresh token is empty")
	}
	req, err := http.NewRequest(http.MethodPost, cursorOAuthRefreshURL, strings.NewReader("{}"))
	if err != nil {
		return domain.CursorOAuthCredential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return domain.CursorOAuthCredential{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.CursorOAuthCredential{}, fmt.Errorf("cursor token refresh failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed cursorRefreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return domain.CursorOAuthCredential{}, err
	}
	access := strings.TrimSpace(parsed.AccessToken)
	if access == "" {
		return domain.CursorOAuthCredential{}, fmt.Errorf("cursor token refresh returned empty access token")
	}
	nextRefresh := strings.TrimSpace(parsed.RefreshToken)
	if nextRefresh == "" {
		nextRefresh = refreshToken
	}
	return domain.CursorOAuthCredential{
		AccessToken:  access,
		RefreshToken: nextRefresh,
		ExpiresAt:    cursorTokenExpiry(access).UTC().Format(time.RFC3339),
		AccountLabel: "Cursor",
	}, nil
}

func cursorTokenNeedsRefresh(cred *domain.CursorOAuthCredential) bool {
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return true
	}
	if strings.TrimSpace(cred.ExpiresAt) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, cred.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(expiresAt)
}

func (s *Server) ensureFreshCursorToken(provider domain.Provider) (domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeCursorOAuth {
		return provider, nil
	}
	if provider.CursorOAuth == nil || strings.TrimSpace(provider.CursorOAuth.RefreshToken) == "" {
		return provider, fmt.Errorf("provider %q has no Cursor OAuth credentials; connect Cursor in Provider settings", provider.ID)
	}
	if !cursorTokenNeedsRefresh(provider.CursorOAuth) {
		return provider, nil
	}
	refreshed, err := refreshCursorOAuthToken(provider.CursorOAuth.RefreshToken)
	if err != nil {
		return provider, err
	}
	updated, err := s.router.SetProviderCursorOAuth(provider.ID, refreshed)
	if err != nil {
		return provider, err
	}
	_ = s.persistProviderOAuth(updated.ID, nil, updated.CursorOAuth, nil)
	return updated, nil
}

func (s *Server) finishCursorOAuthExchange(providerID string, cred domain.CursorOAuthCredential) (domain.Provider, error) {
	updated, err := s.router.SetProviderCursorOAuth(providerID, cred)
	if err != nil {
		return domain.Provider{}, err
	}
	if err := s.saveState(); err != nil {
		return domain.Provider{}, err
	}
	s.pendingCursorOAuth.finish(providerID)
	// Sync models BEFORE reporting connected, so /__state already has the catalog
	// when the UI refreshes after OAuth success.
	if synced, syncErr := s.syncCursorProviderModels(providerID); syncErr != nil {
		s.logs.AddApp("warn", "cursor oauth connected but model sync failed", syncErr.Error())
	} else {
		updated = synced
	}
	return updated, nil
}

func (s *Server) syncCursorProviderModels(providerID string) (domain.Provider, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1/__internal/cursor-models", nil)
	if err != nil {
		return provider, err
	}
	result := s.fetchProviderModels(req, provider, time.Now())
	if !result.Success {
		errMsg := firstNonEmpty(result.Error, "cursor model sync failed")
		s.logs.AddApp("warn", "cursor oauth model sync failed", errMsg)
		return provider, fmt.Errorf("%s", errMsg)
	}
	if len(result.Models) == 0 {
		s.logs.AddApp("warn", "cursor oauth model sync returned empty list", providerID)
		return provider, fmt.Errorf("cursor model list is empty")
	}
	updated, err := s.router.UpdateProviderModels(providerID, result.Models, "healthy")
	if err != nil {
		s.logs.AddApp("warn", "cursor oauth model sync save failed", err.Error())
		return provider, err
	}
	if err := s.saveState(); err != nil {
		return updated, err
	}
	s.logs.AddApp("info", "cursor oauth models synced", fmt.Sprintf("provider=%s models=%d", providerID, len(result.Models)))
	return updated, nil
}

// SyncConnectedCursorProvidersWithEmptyModels fills model catalogs for Cursor
// OAuth providers that are already connected but have no models (e.g. older
// connects that raced the async sync, or after a restart).
func (s *Server) SyncConnectedCursorProvidersWithEmptyModels() {
	s.syncConnectedCursorProviders(false)
}

// SyncConnectedCursorProviderModels refreshes model catalogs for every
// connected Cursor OAuth provider (used on startup and periodic refresh so
// newly shipped Cursor models appear without manual FALLBACK edits).
func (s *Server) SyncConnectedCursorProviderModels() {
	s.syncConnectedCursorProviders(true)
}

func (s *Server) syncConnectedCursorProviders(forceRefresh bool) {
	state := s.router.State()
	for _, provider := range state.Providers {
		if provider.Deleted {
			continue
		}
		if provider.AuthType != domain.AuthTypeCursorOAuth {
			continue
		}
		if provider.CursorOAuth == nil || strings.TrimSpace(provider.CursorOAuth.AccessToken) == "" {
			continue
		}
		if !forceRefresh && len(provider.Models) > 0 {
			continue
		}
		if _, err := s.syncCursorProviderModels(provider.ID); err != nil {
			s.logs.AddApp("warn", "cursor model sync failed", fmt.Sprintf("provider=%s err=%s", provider.ID, err.Error()))
		}
	}
}

// StartCursorModelBackgroundRefresh periodically re-discovers Cursor models
// so the provider catalog stays current without manual intervention.
func (s *Server) StartCursorModelBackgroundRefresh(ctx context.Context) {
	go func() {
		// Initial full sync shortly after boot (bridge may still be warming).
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		s.SyncConnectedCursorProviderModels()

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.SyncConnectedCursorProviderModels()
			}
		}
	}()
}

func (s *Server) pollCursorOAuthInBackground(providerID, flowID, uuid, verifier string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s.pendingCursorOAuth.setStatus(flowID, "polling", "等待 Cursor 授权…")

	cred, err := pollCursorAuth(ctx, uuid, verifier)
	if err != nil {
		s.pendingCursorOAuth.setStatus(flowID, "error", err.Error())
		s.logs.AddApp("warn", "cursor oauth poll failed", err.Error())
		return
	}
	if _, err := s.finishCursorOAuthExchange(providerID, cred); err != nil {
		s.pendingCursorOAuth.setStatus(flowID, "error", err.Error())
		return
	}
	s.pendingCursorOAuth.setStatus(flowID, "connected", "Cursor 已连接")
	s.logs.AddApp("info", "cursor oauth connected", providerID)
}
