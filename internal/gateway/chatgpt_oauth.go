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

// ChatGPT / Codex CLI OAuth (sub2api / CRS prior art).
const (
	chatgptOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	chatgptOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	chatgptOAuthRedirectURI  = "http://localhost:1455/auth/callback"
	chatgptOAuthScopes       = "openid profile email offline_access"
	chatgptOAuthRefreshScope = "openid profile email"
	chatgptTokenExpirySkew   = 5 * time.Minute
	chatgptOAuthPendingTTL   = 30 * time.Minute

	chatgptCodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	chatgptCodexBaseURL      = "https://chatgpt.com/backend-api"
	chatgptCodexCLIVersion   = "0.144.1"
	chatgptCodexCLIUserAgent = "codex_cli_rs/0.144.1 (Macintosh; Intel Mac OS X) xterm-256color"
)

type chatgptOAuthPending struct {
	Verifier  string
	State     string
	FlowID    string
	CreatedAt time.Time
	shutdown  func()
}

type chatgptOAuthFlowStatus struct {
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type chatgptOAuthPendingStore struct {
	mu       sync.Mutex
	pending  map[string]chatgptOAuthPending
	statuses map[string]chatgptOAuthFlowStatus
}

func newChatGPTOAuthPendingStore() *chatgptOAuthPendingStore {
	return &chatgptOAuthPendingStore{
		pending:  map[string]chatgptOAuthPending{},
		statuses: map[string]chatgptOAuthFlowStatus{},
	}
}

func (store *chatgptOAuthPendingStore) put(providerID string, entry chatgptOAuthPending) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.pending[providerID]; ok && existing.shutdown != nil {
		existing.shutdown()
	}
	store.pending[providerID] = entry
}

func (store *chatgptOAuthPendingStore) take(providerID string) (chatgptOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return chatgptOAuthPending{}, false
	}
	delete(store.pending, providerID)
	if time.Since(entry.CreatedAt) > chatgptOAuthPendingTTL {
		if entry.shutdown != nil {
			entry.shutdown()
		}
		return chatgptOAuthPending{}, false
	}
	return entry, true
}

func (store *chatgptOAuthPendingStore) get(providerID string) (chatgptOAuthPending, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.pending[providerID]
	if !ok {
		return chatgptOAuthPending{}, false
	}
	if time.Since(entry.CreatedAt) > chatgptOAuthPendingTTL {
		if entry.shutdown != nil {
			entry.shutdown()
		}
		delete(store.pending, providerID)
		return chatgptOAuthPending{}, false
	}
	return entry, true
}

func (store *chatgptOAuthPendingStore) finish(providerID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if entry, ok := store.pending[providerID]; ok && entry.shutdown != nil {
		entry.shutdown()
	}
	delete(store.pending, providerID)
}

func (store *chatgptOAuthPendingStore) setStatus(flowID, status, message string) {
	if strings.TrimSpace(flowID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.statuses[flowID] = chatgptOAuthFlowStatus{
		Status:    status,
		Message:   message,
		UpdatedAt: time.Now(),
	}
}

func (store *chatgptOAuthPendingStore) status(flowID string) (chatgptOAuthFlowStatus, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.statuses[flowID]
	return entry, ok
}

func (store *chatgptOAuthPendingStore) getByState(state string) (string, chatgptOAuthPending, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "", chatgptOAuthPending{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for providerID, entry := range store.pending {
		if entry.State != state {
			continue
		}
		if time.Since(entry.CreatedAt) > chatgptOAuthPendingTTL {
			if entry.shutdown != nil {
				entry.shutdown()
			}
			delete(store.pending, providerID)
			continue
		}
		return providerID, entry, true
	}
	return "", chatgptOAuthPending{}, false
}

func (store *chatgptOAuthPendingStore) setStatusByState(state, status, message string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, entry := range store.pending {
		if entry.State == state && strings.TrimSpace(entry.FlowID) != "" {
			store.statuses[entry.FlowID] = chatgptOAuthFlowStatus{
				Status:    status,
				Message:   message,
				UpdatedAt: time.Now(),
			}
			return
		}
	}
}

func generateChatGPTPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 64)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")
	return verifier, challenge, nil
}

func generateChatGPTOAuthState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func buildChatGPTAuthorizeURL(challenge, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", chatgptOAuthClientID)
	params.Set("redirect_uri", chatgptOAuthRedirectURI)
	params.Set("scope", chatgptOAuthScopes)
	params.Set("state", state)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	return chatgptOAuthAuthorizeURL + "?" + params.Encode()
}

type chatgptTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type chatgptIDTokenClaims struct {
	Email     string `json:"email"`
	OpenAIAuth *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		ChatGPTUserID    string `json:"chatgpt_user_id"`
		ChatGPTPlanType  string `json:"chatgpt_plan_type"`
		Organizations    []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
		} `json:"organizations"`
	} `json:"https://api.openai.com/auth"`
}

func decodeChatGPTJWTClaims(token string) (chatgptIDTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return chatgptIDTokenClaims{}, fmt.Errorf("invalid jwt")
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return chatgptIDTokenClaims{}, err
		}
	}
	var claims chatgptIDTokenClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return chatgptIDTokenClaims{}, err
	}
	return claims, nil
}

func exchangeChatGPTCode(ctx context.Context, code, verifier string) (chatgptTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", chatgptOAuthClientID)
	form.Set("code", code)
	form.Set("redirect_uri", chatgptOAuthRedirectURI)
	form.Set("code_verifier", verifier)
	return postChatGPTToken(ctx, form)
}

func refreshChatGPTToken(ctx context.Context, refreshToken string) (chatgptTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", chatgptOAuthClientID)
	form.Set("refresh_token", refreshToken)
	form.Set("scope", chatgptOAuthRefreshScope)
	return postChatGPTToken(ctx, form)
}

func postChatGPTToken(ctx context.Context, form url.Values) (chatgptTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return chatgptTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatgptTokenResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return chatgptTokenResponse{}, fmt.Errorf("chatgpt oauth token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out chatgptTokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return chatgptTokenResponse{}, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return chatgptTokenResponse{}, fmt.Errorf("chatgpt oauth token response missing access_token")
	}
	return out, nil
}

func chatgptCredentialFromToken(token chatgptTokenResponse, previous domain.ChatGPTOAuthCredential) domain.ChatGPTOAuthCredential {
	cred := previous
	cred.AccessToken = token.AccessToken
	if strings.TrimSpace(token.RefreshToken) != "" {
		cred.RefreshToken = token.RefreshToken
	}
	if token.ExpiresIn > 0 {
		cred.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	idToken := strings.TrimSpace(token.IDToken)
	if idToken == "" {
		idToken = token.AccessToken
	}
	if claims, err := decodeChatGPTJWTClaims(idToken); err == nil {
		if claims.Email != "" {
			label := claims.Email
			if claims.OpenAIAuth != nil && claims.OpenAIAuth.ChatGPTPlanType != "" {
				label = claims.Email + " · " + claims.OpenAIAuth.ChatGPTPlanType
			}
			cred.AccountLabel = label
		}
		if claims.OpenAIAuth != nil {
			if claims.OpenAIAuth.ChatGPTAccountID != "" {
				cred.ChatGPTAccountID = claims.OpenAIAuth.ChatGPTAccountID
			}
		}
	}
	return cred
}

func chatgptTokenNeedsRefresh(cred *domain.ChatGPTOAuthCredential) bool {
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return true
	}
	expiresAt := strings.TrimSpace(cred.ExpiresAt)
	if expiresAt == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(parsed.Add(-chatgptTokenExpirySkew))
}

func (s *Server) ensureFreshChatGPTToken(provider domain.Provider) (domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeChatGPTOAuth {
		return provider, nil
	}
	if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.RefreshToken) == "" {
		return provider, fmt.Errorf("provider %q has no ChatGPT OAuth refresh token", provider.ID)
	}
	if !chatgptTokenNeedsRefresh(provider.ChatGPTOAuth) {
		return provider, nil
	}
	token, err := refreshChatGPTToken(context.Background(), provider.ChatGPTOAuth.RefreshToken)
	if err != nil {
		return provider, fmt.Errorf("chatgpt oauth refresh failed: %w", err)
	}
	cred := chatgptCredentialFromToken(token, *provider.ChatGPTOAuth)
	updated, err := s.router.SetProviderChatGPTOAuth(provider.ID, cred)
	if err != nil {
		return provider, err
	}
	if err := s.persistProviderOAuth(updated.ID, nil, nil, updated.ChatGPTOAuth); err != nil {
		s.logs.AddApp("warn", "failed to persist refreshed chatgpt oauth token", err.Error())
	}
	return updated, nil
}

func (s *Server) finishChatGPTOAuthExchange(providerID, flowID string, pending chatgptOAuthPending, code string) error {
	token, err := exchangeChatGPTCode(context.Background(), code, pending.Verifier)
	if err != nil {
		if flowID != "" {
			s.pendingChatGPTOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	cred := chatgptCredentialFromToken(token, domain.ChatGPTOAuthCredential{})
	_, err = s.router.SetProviderChatGPTOAuth(providerID, cred)
	if err != nil {
		if flowID != "" {
			s.pendingChatGPTOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	provider, _ := s.router.ProviderByID(providerID)
	models, fetchErr := fetchChatGPTOAuthModels(context.Background(), provider)
	if fetchErr != nil || len(models) == 0 {
		models = defaultChatGPTOAuthModels(providerID)
	}
	_, _ = s.router.UpdateProviderModels(providerID, models, "healthy")
	if err := s.saveState(); err != nil {
		if flowID != "" {
			s.pendingChatGPTOAuth.setStatus(flowID, "error", err.Error())
		}
		return err
	}
	s.pendingChatGPTOAuth.finish(providerID)
	if flowID != "" {
		s.pendingChatGPTOAuth.setStatus(flowID, "connected", "connected")
	}
	s.logs.AddApp("info", "chatgpt oauth connected", providerID)
	return nil
}

func applyChatGPTCodexHeaders(request *http.Request, provider domain.Provider) {
	if provider.ChatGPTOAuth == nil {
		return
	}
	token := strings.TrimSpace(provider.ChatGPTOAuth.AccessToken)
	if token == "" {
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if accountID := strings.TrimSpace(provider.ChatGPTOAuth.ChatGPTAccountID); accountID != "" {
		request.Header.Set("ChatGPT-Account-ID", accountID)
	}
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	request.Header.Set("originator", "codex_cli_rs")
	request.Header.Set("version", chatgptCodexCLIVersion)
	request.Header.Set("User-Agent", chatgptCodexCLIUserAgent)
}

func defaultChatGPTOAuthModels(providerID string) []domain.Model {
	// ChatGPT OAuth (Codex) only accepts the current Codex model slugs; free/plus
	// accounts reject legacy names like gpt-5.2 / gpt-5.3-codex with HTTP 400.
	ids := []string{"gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4-mini"}
	out := make([]domain.Model, 0, len(ids))
	for _, id := range ids {
		model := domain.Model{
			ID:         id,
			ProviderID: providerID,
			Protocol:   domain.ProtocolOpenAIResponses,
			InMenu:     true,
		}
		fillModelTokenBudgets(&model)
		out = append(out, model)
	}
	return out
}

// chatgptCodexUnsupportedParams are Responses fields that ChatGPT Codex
// (chatgpt.com/backend-api/codex/responses) rejects with HTTP 400
// `{"detail":"Unsupported parameter: ..."}`.
var chatgptCodexUnsupportedParams = []string{
	"max_output_tokens",
	"max_tokens",
	"temperature",
	"top_p",
	"presence_penalty",
	"frequency_penalty",
	"verbosity",
	"metadata",
}

// normalizeChatGPTCodexInput ensures `input` is a list. Codex rejects a bare
// string (`{"detail":"Input must be a list"}`), which the chat→responses
// converter previously emitted for a single user text message.
func normalizeChatGPTCodexInput(payload map[string]any) {
	switch typed := payload["input"].(type) {
	case string:
		text := strings.TrimSpace(typed)
		payload["input"] = []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": text},
				},
			},
		}
	case []any:
		for _, item := range typed {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := entry["content"].(string); ok {
				entry["content"] = []any{
					map[string]any{"type": "input_text", "text": text},
				}
			}
		}
	}
}

// prepareChatGPTCodexRequestBody enforces Codex ChatGPT-account constraints:
// store must be false, stream must be true, input must be a list, and known
// unsupported sampling/limit fields are stripped.
func prepareChatGPTCodexRequestBody(body []byte) ([]byte, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, requestBodyWantsStream(body), nil
	}
	clientWantedStream, _ := payload["stream"].(bool)
	payload["store"] = false
	payload["stream"] = true
	normalizeChatGPTCodexInput(payload)
	for _, key := range chatgptCodexUnsupportedParams {
		delete(payload, key)
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, clientWantedStream, err
	}
	return out, clientWantedStream, nil
}

func fetchChatGPTOAuthModels(ctx context.Context, provider domain.Provider) ([]domain.Model, error) {
	if provider.ChatGPTOAuth == nil || strings.TrimSpace(provider.ChatGPTOAuth.AccessToken) == "" {
		return nil, fmt.Errorf("provider has no ChatGPT OAuth access token")
	}
	modelsURL := chatgptCodexBaseURL + "/codex/models?client_version=" + url.QueryEscape(chatgptCodexCLIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	applyChatGPTCodexHeaders(req, provider)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chatgpt models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]domain.Model, 0, len(payload.Models))
	for _, item := range payload.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" || slug == "codex-auto-review" {
			continue
		}
		model := domain.Model{
			ID:         slug,
			ProviderID: provider.ID,
			Protocol:   domain.ProtocolOpenAIResponses,
			InMenu:     true,
		}
		fillModelTokenBudgets(&model)
		out = append(out, model)
	}
	if len(out) == 0 {
		return defaultChatGPTOAuthModels(provider.ID), nil
	}
	return out, nil
}
