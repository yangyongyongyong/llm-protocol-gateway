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
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Claude.ai OAuth constants. This replays the same PKCE flow the official
// Claude Code CLI uses to turn a Claude.ai Pro/Max subscription into an API
// backend (see sub2api / claude-oauth-proxy for prior art).
const (
	claudeOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// Browser OAuth scope must include org:create_api_key (sub2api ScopeOAuth).
	claudeOAuthScope        = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeOAuthAuthorizeURL = "https://claude.ai/oauth/authorize"
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
	claudeMessagesURL = "https://api.anthropic.com/v1/messages"
	claudeCountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens"
	claudeModelsURL     = "https://api.anthropic.com/v1/models"
	// claudeBillingHeaderMarker is injected into the system prompt for
	// claude_oauth providers; required for OAuth-authenticated Sonnet/Opus
	// billing to work correctly against the subscription.
	claudeBillingHeaderMarker = "x-anthropic-billing-header: cc_version=2.1.77; cc_entrypoint=cli; cch=00000;"
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
}

type claudeOAuthFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	store.statuses[flowID] = claudeOAuthFlowStatus{
		Status:    status,
		Message:   message,
		UpdatedAt: time.Now(),
	}
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
	response, err := client.Do(request)
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
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(credential.ExpiresAt))
	if err != nil {
		// Unknown/unparseable expiry: be conservative and refresh.
		return true
	}
	return time.Now().UTC().Add(claudeTokenExpirySkew).After(expiresAt)
}

// ensureFreshClaudeToken returns a provider with a valid (non-expired) Claude
// OAuth access token, refreshing and persisting it first if necessary. It is
// a no-op for providers that are not in claude_oauth auth mode.
func (s *Server) ensureFreshClaudeToken(provider domain.Provider) (domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeClaudeOAuth {
		return provider, nil
	}
	if provider.ClaudeOAuth == nil || strings.TrimSpace(provider.ClaudeOAuth.RefreshToken) == "" {
		return provider, fmt.Errorf("provider %q has no Claude OAuth connection; connect it first", provider.ID)
	}
	if !claudeTokenNeedsRefresh(provider.ClaudeOAuth) {
		return provider, nil
	}

	refreshed, err := refreshClaudeOAuthToken(provider.ClaudeOAuth.RefreshToken)
	if err != nil {
		return provider, fmt.Errorf("failed to refresh claude oauth token: %w", err)
	}
	// The refresh endpoint may omit refresh_token when it doesn't rotate it;
	// keep the previous one in that case.
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = provider.ClaudeOAuth.RefreshToken
	}
	refreshed.AccountLabel = provider.ClaudeOAuth.AccountLabel
	if strings.TrimSpace(refreshed.Scope) == "" {
		refreshed.Scope = provider.ClaudeOAuth.Scope
	}

	updated, err := s.router.SetProviderClaudeOAuth(provider.ID, refreshed)
	if err != nil {
		return provider, err
	}
	if err := s.persistProviderOAuth(updated.ID, updated.ClaudeOAuth, nil, nil); err != nil {
		s.logs.AddApp("warn", "failed to persist refreshed claude oauth token", err.Error())
	}
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
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		body = injectClaudeBillingHeader(body)
	} else if _, ok := payload["messages"]; ok {
		if nativePassThrough {
			normalizeClaudePassThroughPayload(payload)
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
	response, err := client.Do(request)
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
