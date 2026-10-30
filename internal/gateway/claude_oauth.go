package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Claude.ai OAuth constants. This replays the same PKCE flow the official
// Claude Code CLI uses to turn a Claude.ai Pro/Max subscription into an API
// backend (see sub2api / claude-oauth-proxy for prior art).
const (
	claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// Browser OAuth scope must include org:create_api_key (sub2api ScopeOAuth).
	claudeOAuthScope           = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeOAuthAuthorizeURL    = "https://claude.ai/oauth/authorize"
	claudeOAuthCAIAuthorizeURL = "https://claude.com/cai/oauth/authorize"
	// claudeOAuthTokenURL is the current token endpoint. console.anthropic.com/v1/oauth/token
	// is a legacy alternative that reportedly still works, but platform.claude.com is
	// used exclusively here per the plan (no fallback logic).
	claudeOAuthTokenURL = "https://platform.claude.com/v1/oauth/token"
	// claudeOAuthRedirectURI points at Anthropic's own consent-confirmation page,
	// which displays the resulting code/state for manual copy/paste instead of
	// requiring a local loopback server.
	claudeOAuthRedirectURI = "https://platform.claude.com/oauth/code/callback"
	// claudeOAuthBetaHeader unlocks OAuth-authenticated Messages API billing
	// against the underlying Claude.ai subscription.
	claudeOAuthBetaHeader = "oauth-2025-04-20"
	// claudeMessagesURL is the upstream Anthropic Messages API used for both
	// the real pass-through path and the Claude-native chat test path.
	claudeMessagesURL    = "https://api.anthropic.com/v1/messages"
	claudeCountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens"
	claudeModelsURL      = "https://api.anthropic.com/v1/models"
	// claudeBillingHeaderMarker is injected into the system prompt for
	// claude_oauth providers; required for OAuth-authenticated Sonnet/Opus
	// billing to work correctly against the subscription.
	claudeBillingHeaderMarker = "x-anthropic-billing-header: cc_version=" + claudeOAuthBillingVersion + "; cc_entrypoint=cli; cch=00000;"
	// claudeTokenExpirySkew is subtracted from the token's expiry before
	// deciding whether a refresh is needed, so requests don't race an
	// about-to-expire access token.
	claudeTokenExpirySkew = 5 * time.Minute
	// claudeOAuthPendingTTL bounds how long a start()'d PKCE flow stays valid
	// in memory before it is considered abandoned.
	claudeOAuthPendingTTL = 15 * time.Minute
)

// claudeOAuthPending is the in-memory state stashed between /claude-oauth/start
// and /claude-oauth/complete for a single provider's manual-paste OAuth flow.
type claudeOAuthPending struct {
	Verifier    string
	State       string
	RedirectURI string
	FlowID      string
	Mode        string
	CreatedAt   time.Time
	shutdown    func()
	// claim is shared by every copy of this flow (the store hands out values), so
	// the browser callback and a pasted address cannot both spend the one-time
	// code. A second exchange is rejected by Anthropic, which reads to the user
	// as a failed login.
	claim *atomic.Bool
}

type claudeOAuthFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	// How this flow can be finished besides the browser redirect: the callback
	// goes to the gateway's own port on localhost, which is unreachable when the
	// gateway runs elsewhere, so pasting the address the browser ended on is a
	// first-class way in (the same shape ChatGPT's flow advertises).
	PasteCallback bool   `json:"pasteCallback,omitempty"`
	CallbackURL   string `json:"callbackUrl,omitempty"`
	Instructions  string `json:"instructions,omitempty"`
}

// claudeOAuthPendingStore is a short-lived, in-memory map of provider ID to
// pending PKCE flow state. The flow is synchronous/manual-paste and
// single-user, so no persistence is needed; entries expire defensively.
type claudeOAuthPendingStore struct {
	mu       sync.Mutex
	pending  map[string]claudeOAuthPending
	statuses map[string]claudeOAuthFlowStatus
}

func newClaudeOAuthPendingStore() *claudeOAuthPendingStore {
	return &claudeOAuthPendingStore{
		pending:  map[string]claudeOAuthPending{},
		statuses: map[string]claudeOAuthFlowStatus{},
	}
}

func (store *claudeOAuthPendingStore) put(providerID string, entry claudeOAuthPending) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.pending[providerID]; ok && existing.shutdown != nil {
		existing.shutdown()
	}
	store.pending[providerID] = entry
}

func (store *claudeOAuthPendingStore) get(providerID string) (claudeOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return claudeOAuthPending{}, false
	}
	if time.Since(entry.CreatedAt) > claudeOAuthPendingTTL {
		if entry.shutdown != nil {
			entry.shutdown()
		}
		delete(store.pending, providerID)
		return claudeOAuthPending{}, false
	}
	return entry, true
}

func (store *claudeOAuthPendingStore) setStatus(flowID, status, message string) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	// The callback hint belongs to the flow rather than to one transition: keep
	// it through every update so the console never loses the paste-in finish.
	previous := store.statuses[flowID]
	store.statuses[flowID] = claudeOAuthFlowStatus{
		Status:        status,
		Message:       message,
		UpdatedAt:     time.Now(),
		PasteCallback: previous.PasteCallback,
		CallbackURL:   previous.CallbackURL,
		Instructions:  previous.Instructions,
	}
}

// setCallbackHint records how this flow's callback can be completed, before any
// status transition happens.
func (store *claudeOAuthPendingStore) setCallbackHint(flowID string, hint claudeOAuthFlowStatus) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry := store.statuses[flowID]
	entry.PasteCallback = hint.PasteCallback
	entry.CallbackURL = hint.CallbackURL
	entry.Instructions = hint.Instructions
	if entry.Status == "" {
		entry.Status = "pending"
	}
	entry.UpdatedAt = time.Now()
	store.statuses[flowID] = entry
}

func (store *claudeOAuthPendingStore) status(flowID string) (claudeOAuthFlowStatus, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.statuses[flowID]
	return entry, ok
}

func (store *claudeOAuthPendingStore) getByState(state string) (string, claudeOAuthPending, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "", claudeOAuthPending{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for providerID, entry := range store.pending {
		if entry.State != state {
			continue
		}
		if time.Since(entry.CreatedAt) > claudeOAuthPendingTTL {
			delete(store.pending, providerID)
			continue
		}
		return providerID, entry, true
	}
	return "", claudeOAuthPending{}, false
}

func (store *claudeOAuthPendingStore) setStatusByState(state, status, message string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, entry := range store.pending {
		if entry.State == state && strings.TrimSpace(entry.FlowID) != "" {
			store.statuses[entry.FlowID] = claudeOAuthFlowStatus{
				Status:    status,
				Message:   message,
				UpdatedAt: time.Now(),
			}
			return
		}
	}
}

func (store *claudeOAuthPendingStore) take(providerID string) (claudeOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return claudeOAuthPending{}, false
	}
	delete(store.pending, providerID)
	if time.Since(entry.CreatedAt) > claudeOAuthPendingTTL {
		if entry.shutdown != nil {
			entry.shutdown()
		}
		return claudeOAuthPending{}, false
	}
	return entry, true
}

func (store *claudeOAuthPendingStore) finish(providerID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return
	}
	delete(store.pending, providerID)
	if entry.shutdown != nil {
		entry.shutdown()
	}
}

// generateClaudePKCE creates a random code_verifier and its S256 code_challenge.
func generateClaudePKCE() (verifier string, challenge string, err error) {
	verifierBytes := make([]byte, 32)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// generateClaudeOAuthState creates a random anti-CSRF state token.
func generateClaudeOAuthState() (string, error) {
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

func generateClaudeOAuthFlowID() (string, error) {
	flowBytes := make([]byte, 16)
	if _, err := rand.Read(flowBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(flowBytes), nil
}

func encodeClaudeOAuthScope() string {
	return strings.ReplaceAll(url.QueryEscape(claudeOAuthScope), "%20", "+")
}

// buildClaudeManualAuthorizeURL builds the copy/paste OAuth URL used by
// sub2api (code=true + platform.claude.com callback).
func buildClaudeManualAuthorizeURL(challenge, state string) string {
	encodedRedirectURI := url.QueryEscape(claudeOAuthRedirectURI)
	return fmt.Sprintf(
		"%s?code=true&client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		claudeOAuthAuthorizeURL,
		claudeOAuthClientID,
		encodedRedirectURI,
		encodeClaudeOAuthScope(),
		challenge,
		state,
	)
}

// buildClaudeLocalAuthorizeURL builds the localhost-callback OAuth URL used by
// Claude Code desktop. It avoids the platform.claude.com redirect_uri mangling
// bug that surfaces as "Authorization failed" after login.
func buildClaudeLocalAuthorizeURL(redirectURI, challenge, state string) string {
	encodedRedirectURI := url.QueryEscape(redirectURI)
	return fmt.Sprintf(
		"%s?code=true&client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		claudeOAuthCAIAuthorizeURL,
		claudeOAuthClientID,
		encodedRedirectURI,
		encodeClaudeOAuthScope(),
		challenge,
		state,
	)
}

// buildClaudeAuthorizeURL is kept for tests/backward compatibility.
func buildClaudeAuthorizeURL(challenge, state string) string {
	return buildClaudeManualAuthorizeURL(challenge, state)
}

// claudePasteInstructions is what a user whose browser cannot reach the gateway
// has to be told: the address the tab ended on is the finish.
const claudePasteInstructions = "授权完成后浏览器会停在回调地址（形如 http://localhost:18093/callback?code=…&state=…）。这个页面打不开是正常的（网关不在你这台机器上时必然打不开）：把地址栏里那条完整地址复制粘贴到下面，一样能收尾。"

// parseClaudeOAuthInput takes whatever was pasted: a whole callback URL (the
// usual thing a browser's address bar offers), Anthropic's "code#state" form, or
// a bare code.
func parseClaudeOAuthInput(raw string) (code string, state string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	// The unclaimed-callback page hands out a path like /callback?code=…, so a
	// pasted address may be relative to the gateway rather than absolute.
	if strings.Contains(raw, "?") {
		if parsed, err := url.Parse(raw); err == nil {
			query := parsed.Query()
			code = strings.TrimSpace(query.Get("code"))
			state = strings.TrimSpace(query.Get("state"))
			if code != "" {
				return code, state
			}
		}
	}
	return parseClaudeOAuthCode(raw)
}

// claimClaudeCallback lets only the first of the browser callback and a pasted
// address spend the code.
func claimClaudeCallback(pending claudeOAuthPending) bool {
	return pending.claim == nil || pending.claim.CompareAndSwap(false, true)
}

// parseClaudeOAuthCode splits Anthropic's occasional "code#state" paste
// fragment format into its parts. If there is no '#', state is returned empty
// and the caller should fall back to the state it already has on file.
func parseClaudeOAuthCode(raw string) (code string, state string) {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "#"); idx >= 0 {
		return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
	}
	return raw, ""
}

// claudeOAuthTokenResponse mirrors the token endpoint's JSON response shape.
type claudeOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// fetchClaudeOAuthProfileLabel queries api.anthropic.com/api/oauth/profile
// with the OAuth access token and builds a human account label like
// "user@example.com · Example Corp". Best-effort: callers treat errors as "no label".
func fetchClaudeOAuthProfileLabel(accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("empty access token")
	}
	request, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/api/oauth/profile", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := guardUpstreamIdleClient(client, request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("profile endpoint status %d", response.StatusCode)
	}
	var payload struct {
		Account struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"account"`
		Organization struct {
			Name string `json:"name"`
		} `json:"organization"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	parts := []string{}
	if email := strings.TrimSpace(payload.Account.Email); email != "" {
		parts = append(parts, email)
	} else if name := strings.TrimSpace(payload.Account.DisplayName); name != "" {
		parts = append(parts, name)
	}
	if org := strings.TrimSpace(payload.Organization.Name); org != "" {
		parts = append(parts, org)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("profile has no account identity")
	}
	return strings.Join(parts, " · "), nil
}

// exchangeClaudeOAuthCode exchanges an authorization code + PKCE verifier for
// an access/refresh token pair at the Claude OAuth token endpoint.
func exchangeClaudeOAuthCode(code, verifier, state, redirectURI string) (domain.ClaudeOAuthCredential, error) {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = claudeOAuthRedirectURI
	}
	payload := map[string]any{
		"code":          code,
		"grant_type":    "authorization_code",
		"client_id":     claudeOAuthClientID,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}
	if strings.TrimSpace(state) != "" {
		payload["state"] = strings.TrimSpace(state)
	}
	return postClaudeOAuthToken(payload)
}

// refreshClaudeOAuthToken exchanges a refresh token for a new access token.
func refreshClaudeOAuthToken(refreshToken string) (domain.ClaudeOAuthCredential, error) {
	payload := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     claudeOAuthClientID,
	}
	return postClaudeOAuthToken(payload)
}

// claudeOAuthRefreshRequiresRelogin says whether a failed refresh is the one
// failure no retry can fix: the refresh token itself was retired or revoked, so
// only signing in again revives the account.
func claudeOAuthRefreshRequiresRelogin(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_grant")
}

func postClaudeOAuthToken(payload map[string]any) (domain.ClaudeOAuthCredential, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.ClaudeOAuthCredential{}, err
	}
	request, err := http.NewRequest(http.MethodPost, claudeOAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return domain.ClaudeOAuthCredential{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("User-Agent", "axios/1.13.6")

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := guardUpstreamIdleClient(client, request)
	if err != nil {
		return domain.ClaudeOAuthCredential{}, err
	}
	defer response.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ClaudeOAuthCredential{}, fmt.Errorf("claude oauth token endpoint returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed claudeOAuthTokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return domain.ClaudeOAuthCredential{}, fmt.Errorf("failed to parse claude oauth token response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return domain.ClaudeOAuthCredential{}, fmt.Errorf("claude oauth token response missing access_token")
	}

	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 8 * 3600
	}
	return domain.ClaudeOAuthCredential{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339),
		Scope:        parsed.Scope,
	}, nil
}

// claudeTokenNeedsRefresh reports whether the credential is missing, expired,
// or within the expiry skew buffer of expiring.
func claudeTokenNeedsRefresh(credential *domain.ClaudeOAuthCredential) bool {
	return claudeTokenNeedsRefreshWithin(credential, claudeTokenExpirySkew)
}

// claudeTokenNeedsRefreshWithin is the same question with a chosen lead, so the
// background sweep can rotate a token half an hour before it dies rather than
// five minutes before a request would.
func claudeTokenNeedsRefreshWithin(credential *domain.ClaudeOAuthCredential, lead time.Duration) bool {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(credential.ExpiresAt))
	if err != nil {
		// Unknown/unparseable expiry: be conservative and refresh.
		return true
	}
	return time.Now().UTC().Add(lead).After(expiresAt)
}

// ensureFreshClaudeToken returns a provider with a valid (non-expired) Claude
// OAuth access token, refreshing and persisting it first if necessary. It is
// a no-op for providers that are not in claude_oauth auth mode.
func (s *Server) ensureFreshClaudeToken(provider domain.Provider) (domain.Provider, error) {
	return s.ensureFreshClaudeTokenWithin(provider, claudeTokenExpirySkew)
}

func (s *Server) ensureFreshClaudeTokenWithin(provider domain.Provider, lead time.Duration) (domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeClaudeOAuth {
		return provider, nil
	}
	if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) == "" {
		return provider, fmt.Errorf("provider %q has no Claude OAuth connection; connect it first", provider.ID)
	}
	if !claudeTokenNeedsRefreshWithin(provider.ClaudeOAuth, lead) {
		return provider, nil
	}

	// Serialize refresh per provider. Concurrent refresh with the same refresh
	// token can yield invalid_grant (Anthropic may rotate/invalidate R1 after
	// the first success); UI usage polls + live traffic used to race here.
	unlock := s.lockOAuthUsageFetch("claude-token:" + provider.ID)
	defer unlock()

	current, err := s.router.ProviderByID(provider.ID)
	if err != nil {
		return provider, err
	}
	if current.ClaudeOAuth == nil || strings.TrimSpace(current.ClaudeOAuth.RefreshToken) == "" {
		return current, fmt.Errorf("provider %q has no Claude OAuth connection; connect it first", provider.ID)
	}
	if !claudeTokenNeedsRefreshWithin(current.ClaudeOAuth, lead) {
		return current, nil
	}

	refreshed, err := refreshClaudeOAuthToken(current.ClaudeOAuth.RefreshToken)
	if err != nil {
		s.recordRenewal(current, "claude", time.Now(), err)
		return current, fmt.Errorf("failed to refresh claude oauth token: %w", err)
	}
	// The refresh endpoint may omit refresh_token when it doesn't rotate it;
	// keep the previous one in that case.
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = current.ClaudeOAuth.RefreshToken
	}
	refreshed.AccountLabel = current.ClaudeOAuth.AccountLabel
	if strings.TrimSpace(refreshed.AccountLabel) == "" {
		// 老连接没存过账号信息：借 token 刷新的时机补齐。
		if label, labelErr := fetchClaudeOAuthProfileLabel(refreshed.AccessToken); labelErr == nil {
			refreshed.AccountLabel = label
		}
	}
	if strings.TrimSpace(refreshed.Scope) == "" {
		refreshed.Scope = current.ClaudeOAuth.Scope
	}

	updated, err := s.router.SetProviderClaudeOAuth(current.ID, refreshed)
	if err != nil {
		return current, err
	}
	if err := s.persistProviderOAuth(updated); err != nil {
		// Memory has the new token; DB does not. A restart would then load the
		// old refresh token and hit invalid_grant — surface loudly.
		s.logs.AddApp("error", "failed to persist refreshed claude oauth token", err.Error())
		slog.Error("failed to persist refreshed claude oauth token", "provider", updated.ID, "error", err)
	}
	// 成功也要埋点：缺了这一条，监控中心的续期记录与连续失败判断会一直停在
	// 上一次失败上，看起来像「续期成功却仍报失败」。
	s.recordRenewal(updated, "claude", time.Now(), nil)
	return updated, nil
}

func (s *Server) finishClaudeOAuthExchange(providerID, flowID string, pending claudeOAuthPending, code, tokenState string) error {
	credential, err := exchangeClaudeOAuthCode(code, pending.Verifier, tokenState, pending.RedirectURI)
	if err != nil {
		if flowID != "" {
			s.pendingClaudeOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	// 连接成功后顺带拉取账号信息（email · 组织），用于控制台展示；失败不阻塞连接。
	if label, labelErr := fetchClaudeOAuthProfileLabel(credential.AccessToken); labelErr == nil {
		credential.AccountLabel = label
	}
	_, err = s.router.SetProviderClaudeOAuth(providerID, credential)
	if err != nil {
		if flowID != "" {
			s.pendingClaudeOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	if err := s.saveState(); err != nil {
		if flowID != "" {
			s.pendingClaudeOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	s.pendingClaudeOAuth.finish(providerID)
	if flowID != "" {
		s.pendingClaudeOAuth.setStatus(flowID, "success", "connected")
	}
	s.logs.AddApp("info", "claude oauth connected", providerID)
	return nil
}

// BackfillClaudeOAuthAccountLabels fills missing account labels for already
// connected Claude OAuth providers (one-shot at startup; older connections
// were created before the profile fetch existed).
func (s *Server) BackfillClaudeOAuthAccountLabels() {
	go func() {
		for _, provider := range s.router.State().Providers {
			if provider.Deleted {
				continue
			}
			if provider.AuthType != domain.AuthTypeClaudeOAuth || provider.ClaudeOAuth == nil {
				continue
			}
			if strings.TrimSpace(provider.ClaudeOAuth.AccessToken) == "" || strings.TrimSpace(provider.ClaudeOAuth.AccountLabel) != "" {
				continue
			}
			label, err := fetchClaudeOAuthProfileLabel(provider.ClaudeOAuth.AccessToken)
			if err != nil {
				continue
			}
			credential := *provider.ClaudeOAuth
			credential.AccountLabel = label
			if updated, err := s.router.SetProviderClaudeOAuth(provider.ID, credential); err == nil {
				if err := s.persistProviderOAuth(updated); err != nil {
					s.logs.AddApp("warn", "failed to persist claude oauth account label", err.Error())
				}
				s.logs.AddApp("info", "claude oauth account label backfilled", fmt.Sprintf("provider=%s label=%s", provider.ID, label))
			}
		}
	}()
}

func (s *Server) claudeOAuthCallbackURL() string {
	addr := strings.TrimSpace(s.listenAddr)
	if addr == "" {
		addr = "127.0.0.1:18093"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Anthropic's Claude Code OAuth client only allows http://localhost:<port>/callback.
		return "http://localhost:18093/callback"
	}
	if port == "" {
		port = "18093"
	}
	// Always advertise localhost (not 127.0.0.1 / 0.0.0.0): the shared Claude Code
	// client_id allowlists loopback as http://localhost:<port>/callback.
	return fmt.Sprintf("http://localhost:%s/callback", port)
}

// requestHostIsLoopback says whether the console reached this gateway over a
// loopback address. Only then can the browser follow Anthropic's redirect back
// to this gateway's own localhost callback: a console opened over a LAN or
// public address has its loopback pointed at the user's own machine, where a
// different gateway may well be listening on the same port.
func requestHostIsLoopback(hostport string) bool {
	host := strings.TrimSpace(hostport)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func claudeOAuthDefaultMode(hostport string) string {
	if requestHostIsLoopback(hostport) {
		return "localhost"
	}
	return "manual"
}

// claudeOAuthUnclaimedHTML is what a callback this gateway has no pending flow
// for comes to. The usual cause is a remote console whose browser followed the
// loopback redirect to another gateway on the user's machine; the address shown
// is the one that console can finish the login with.
const claudeOAuthUnclaimedHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Claude 授权回调未被认领</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;max-width:720px;margin:48px auto;padding:0 20px;color:#1f2328;line-height:1.65}
h2{margin:0 0 12px;font-size:20px}
p{margin:8px 0;color:#57606a}
code{display:block;word-break:break-all;background:#f6f8fa;border:1px solid #d0d7de;border-radius:6px;padding:12px;margin:12px 0;font-size:13px;color:#1f2328}
button{font:inherit;padding:6px 14px;border:1px solid #d0d7de;border-radius:6px;background:#f6f8fa;cursor:pointer}
button:active{background:#eaeef2}
.note{border-left:3px solid #d0d7de;padding-left:12px;margin-top:20px}
</style></head><body>
<h2>这次回调没有被当前网关认领</h2>
<p>常见原因有两个：授权流程已超过 15 分钟有效期；或者你是在远程控制台发起的授权，浏览器按 <b>localhost</b> 打开了本机上的另一个网关，而不是发起授权的那一台。</p>
<code id="callback">__CALLBACK__</code>
<button id="copy" type="button">复制这条回调地址</button>
<div class="note">
<p>如果是在远程控制台授权的：回到那个控制台，把上面这条完整地址粘贴到「完成连接」输入框即可收尾，不需要重新授权。</p>
<p>如果授权确实已经过期：回到控制台重新点一次「连接 Claude 账号」。</p>
</div>
<script>
document.getElementById('copy').addEventListener('click', function () {
  var text = document.getElementById('callback').textContent;
  var done = function () { this.textContent = '已复制'; };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done.bind(this), function () {});
  } else {
    var range = document.createRange();
    range.selectNodeContents(document.getElementById('callback'));
    var selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    document.execCommand('copy');
    done.call(this);
  }
});
</script>
</body></html>`

// writeClaudeOAuthUnclaimedPage answers a callback whose state this gateway does
// not know with the explanation above and the address the user can paste into
// the console that started the flow.
func writeClaudeOAuthUnclaimedPage(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	}
	callback := scheme + "://" + r.Host + r.URL.Path
	if r.URL.RawQuery != "" {
		callback += "?" + r.URL.RawQuery
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(strings.ReplaceAll(claudeOAuthUnclaimedHTML, "__CALLBACK__", stdhtml.EscapeString(callback))))
}

func (s *Server) startClaudeOAuthLocalFlow(providerID, challenge string, pending claudeOAuthPending) (string, string, error) {
	flowID, err := generateClaudeOAuthFlowID()
	if err != nil {
		return "", "", err
	}
	redirectURI := s.claudeOAuthCallbackURL()
	authURL := buildClaudeLocalAuthorizeURL(redirectURI, challenge, pending.State)

	pending.FlowID = flowID
	pending.Mode = "localhost"
	pending.RedirectURI = redirectURI
	s.pendingClaudeOAuth.put(providerID, pending)
	s.pendingClaudeOAuth.setStatus(flowID, "pending", "waiting for browser callback")
	return authURL, flowID, nil
}

// stringFromRequestBody best-effort extracts the "model" field from a Claude
// Messages JSON request body; empty when unparsable.
func stringFromRequestBody(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Model
}

// claudeContext1MBeta is the beta flag that enables the 1M-token context
// window on Claude models that support it.
const claudeContext1MBeta = "context-1m-2025-08-07"

// mergeAnthropicBetaFlags appends extra beta flags to an existing
// anthropic-beta header value without duplicating entries.
func mergeAnthropicBetaFlags(existing string, extras ...string) string {
	seen := map[string]bool{}
	parts := make([]string, 0, 4)
	appendPart := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" || seen[strings.ToLower(part)] {
			return
		}
		seen[strings.ToLower(part)] = true
		parts = append(parts, part)
	}
	for _, part := range strings.Split(existing, ",") {
		appendPart(part)
	}
	for _, part := range extras {
		appendPart(part)
	}
	return strings.Join(parts, ",")
}

// mergeAnthropicBetaValue merges OAuth baseline beta flags into any existing
// anthropic-beta header value without duplicating entries.
func mergeAnthropicBetaValue(existing string) string {
	desired := strings.Split(claudeOAuthMessagesBetaHeader(), ",")
	seen := map[string]bool{}
	parts := make([]string, 0, len(desired)+4)
	appendPart := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" || seen[strings.ToLower(part)] {
			return
		}
		seen[strings.ToLower(part)] = true
		parts = append(parts, part)
	}
	for _, part := range strings.Split(existing, ",") {
		appendPart(part)
	}
	for _, part := range desired {
		appendPart(part)
	}
	return strings.Join(parts, ",")
}

func claudeOAuthMessagesBetaHeader() string {
	return strings.Join([]string{
		"claude-code-20250219",
		claudeOAuthBetaHeader,
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"structured-outputs-2025-12-15",
		"token-efficient-tools-2026-03-28",
	}, ",")
}

// claudeOAuthHTTPResult mirrors providerChatHTTPResult's shape for the
// Claude-native chat-test path, keeping the same fields the frontend already
// expects from testProviderChat.
type claudeOAuthHTTPResult struct {
	Status       int
	LatencyMs    int64
	ResponseBody string
	RequestBody  string
	TargetURL    string
	Error        string
}

// buildClaudeOAuthRequest constructs an authenticated *http.Request against
// the Anthropic Messages API for a claude_oauth provider, applying the same
// billing-header injection and anthropic-beta merge used by the real
// pass-through path. The caller must have already refreshed provider via
// ensureFreshClaudeToken.
func buildClaudeOAuthRequest(ctx context.Context, provider domain.Provider, body []byte, clientBetaHeader string, nativePassThrough bool, targetURL string) (*http.Request, []byte, error) {
	if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.AccessToken) == "" {
		return nil, nil, fmt.Errorf("provider %q has no Claude OAuth access token", provider.ID)
	}
	if strings.TrimSpace(targetURL) == "" {
		targetURL = claudeMessagesURL
	}
	countTokens := strings.Contains(strings.ToLower(targetURL), "count_tokens")
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		body = injectClaudeBillingHeader(body)
	} else if _, ok := payload["messages"]; ok {
		if nativePassThrough {
			normalizeClaudePassThroughPayload(payload)
		}
		// /v1/messages/count_tokens rejects generation-only fields (notably
		// max_tokens). normalizeClaudePassThroughPayload injects a default
		// budget for Messages; strip it back out for token counting.
		if countTokens {
			sanitizeClaudeCountTokensPayload(payload)
		}
		applyClaudeOAuthCloaking(payload)
		marshaled, marshalErr := marshalClaudeOAuthBody(payload)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		if targetURL == claudeMessagesURL {
			body = signClaudeOAuthCCH(marshaled)
		} else {
			body = marshaled
		}
	} else {
		body = injectClaudeBillingHeader(body)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("Authorization", "Bearer "+provider.ClaudeOAuth.AccessToken)
	request.Header.Set("anthropic-beta", mergeAnthropicBetaValue(clientBetaHeader))
	request.Header.Set("User-Agent", "claude-code/"+claudeOAuthBillingVersion)
	if billingHeader := claudeOAuthBillingHTTPHeaderValue(body); billingHeader != "" {
		request.Header.Set("x-anthropic-billing-header", billingHeader)
	}
	return request, body, nil
}

// sendClaudeOAuthMessagesRequest sends a native Anthropic Messages API
// payload for a claude_oauth provider (refreshing the token first if
// needed) and returns the raw HTTP result. Shared by the real chat-test path
// (testClaudeOAuthProviderChat) so the request-building logic isn't
// duplicated against proxyClaudeMessages.
func (s *Server) sendClaudeOAuthMessagesRequest(ctx context.Context, provider domain.Provider, payload map[string]any, started time.Time) claudeOAuthHTTPResult {
	refreshed, err := s.ensureFreshClaudeToken(provider)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: claudeMessagesURL, LatencyMs: time.Since(started).Milliseconds()}
	}

	rawBody, err := json.Marshal(payload)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: claudeMessagesURL, LatencyMs: time.Since(started).Milliseconds()}
	}

	request, sentBody, err := buildClaudeOAuthRequest(ctx, refreshed, rawBody, "", false, claudeMessagesURL)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: claudeMessagesURL, RequestBody: string(rawBody), LatencyMs: time.Since(started).Milliseconds()}
	}

	client := &http.Client{Timeout: 120 * time.Second}
	response, err := guardUpstreamIdleClient(client, request)
	if err != nil {
		return claudeOAuthHTTPResult{Error: err.Error(), TargetURL: claudeMessagesURL, RequestBody: string(sentBody), LatencyMs: time.Since(started).Milliseconds()}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16384))
	return claudeOAuthHTTPResult{
		Status:       response.StatusCode,
		LatencyMs:    time.Since(started).Milliseconds(),
		ResponseBody: string(responseBody),
		RequestBody:  string(sentBody),
		TargetURL:    claudeMessagesURL,
	}
}

// injectClaudeBillingHeader prepends the required OAuth billing marker text
// block into the request body's "system" field, handling the three shapes
// Anthropic's Messages API allows: absent, a plain string, or an array of
// content blocks. Only applies to bodies that look like Messages API requests
// (i.e. have a "messages" field); other bodies are returned unchanged.
func injectClaudeBillingHeader(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	if _, ok := payload["messages"]; !ok {
		return body
	}

	marker := map[string]any{"type": "text", "text": claudeBillingHeaderMarker}
	switch system := payload["system"].(type) {
	case nil:
		payload["system"] = []any{marker}
	case string:
		blocks := []any{marker}
		if strings.TrimSpace(system) != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": system})
		}
		payload["system"] = blocks
	case []any:
		payload["system"] = append([]any{marker}, system...)
	default:
		// Unknown shape: leave as-is rather than risk corrupting the request.
		return body
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return updated
}
