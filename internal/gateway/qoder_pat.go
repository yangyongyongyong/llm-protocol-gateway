package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	// qoderDirectBaseURL is Qoder's stateless, natively OpenAI-Chat compatible
	// endpoint. Defaulted rather than pinned in normalizeProvider so an endpoint
	// change can be handled by editing the provider instead of rebuilding.
	qoderDirectBaseURL    = "https://api2-v2.qoder.sh/model/v1"
	qoderTokenExchangeURL = "https://openapi.qoder.sh/api/v1/jobToken/exchange"
	qoderTokenExpirySkew  = 5 * time.Minute
	// qoderTokenFallbackTTL is the conservative floor used when the exchange
	// response carries no explicit lifetime and the job token is not a decodable
	// JWT. The observed real lifetime is ~24h, so this only costs extra
	// exchanges, never a stale token.
	qoderTokenFallbackTTL = time.Hour
	qoderAccountLabel     = "Qoder"
)

// isPlaceholderBaseURL reports whether a base URL carries no real routing
// information: either empty, or still the create form's example.com prefill.
func isPlaceholderBaseURL(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}
	return strings.Contains(strings.ToLower(baseURL), "example.com")
}

// defaultQoderModels is the model catalog from Qoder's official docs
// (docs.qoder.com/cli/model). The direct endpoint serves no /models route, so
// this list is the catalog. Names are the documented ones lowercased, matching
// the CLI's own `--model efficient` form — the endpoint rejects the
// title-cased display names.
//
// Only the tier aliases are listed. The docs' frontier models (qwen3.8-max-preview,
// glm-5.2, kimi-k3, …) are gated per plan and are rejected with
// invalid_model_error on accounts that lack them; listing them puts entries in
// the console dropdown that fail at request time. Users whose plan includes a
// frontier model can add it as an extra model on the provider.
func defaultQoderModels(providerID string) []domain.Model {
	aliases := []string{
		"auto", // Smart Routing
		"ultimate",
		"performance",
		"efficient",
		"lite",
	}
	models := make([]domain.Model, 0, len(aliases))
	for _, alias := range aliases {
		model := domain.Model{
			ID:         alias,
			ProviderID: providerID,
			Protocol:   domain.ProtocolOpenAIChat,
			InMenu:     true,
		}
		fillModelTokenBudgets(&model)
		models = append(models, model)
	}
	return models
}

// qoderJobTokenResponse is deliberately permissive: the exchange response shape
// is not fully pinned down, so every plausible token/lifetime field is accepted
// and the first non-empty one wins.
type qoderJobTokenResponse struct {
	JobToken    string `json:"jobToken"`
	Token       string `json:"token"`
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	ExpiresAt   int64  `json:"expiresAt"`
	TTL         int64  `json:"ttl"`
	Data        *struct {
		JobToken    string `json:"jobToken"`
		Token       string `json:"token"`
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
		ExpiresAt   int64  `json:"expiresAt"`
		TTL         int64  `json:"ttl"`
	} `json:"data"`
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// qoderJWTExp reads the exp claim when the token is JWT-shaped. Opaque tokens
// return ok=false.
func qoderJWTExp(token string) (int64, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return 0, false
	}
	return claims.Exp, true
}

// qoderTokenExpiry derives the job token expiry from, in order of preference:
// an absolute expiry from the exchange response, a relative lifetime from it,
// the JWT exp claim, and finally qoderTokenFallbackTTL. Every branch subtracts
// the skew so the gateway always re-exchanges before the real expiry.
func qoderTokenExpiry(jobToken string, expiresInSeconds, expiresAtEpoch int64, now time.Time) time.Time {
	if expiresAtEpoch > 0 {
		if expiresAtEpoch > 1e12 { // milliseconds
			expiresAtEpoch /= 1000
		}
		if absolute := time.Unix(expiresAtEpoch, 0); absolute.After(now) {
			return absolute.Add(-qoderTokenExpirySkew)
		}
	}
	if expiresInSeconds > 0 {
		return now.Add(time.Duration(expiresInSeconds) * time.Second).Add(-qoderTokenExpirySkew)
	}
	if exp, ok := qoderJWTExp(jobToken); ok {
		if absolute := time.Unix(exp, 0); absolute.After(now) {
			return absolute.Add(-qoderTokenExpirySkew)
		}
	}
	return now.Add(qoderTokenFallbackTTL)
}

// exchangeQoderJobToken trades the long-lived personal access token for a
// short-lived job token, mirroring refreshCursorOAuthToken: the long-lived
// credential goes out as the bearer and the response body is length-limited.
func exchangeQoderJobToken(pat string) (domain.QoderPATCredential, error) {
	return exchangeQoderJobTokenAt(qoderTokenExchangeURL, pat)
}

func exchangeQoderJobTokenAt(exchangeURL, pat string) (domain.QoderPATCredential, error) {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return domain.QoderPATCredential{}, fmt.Errorf("qoder personal access token is empty")
	}
	// The upstream takes the PAT in the body as personal_token, not (only) as a
	// bearer header: an empty body is rejected with "personal_token is required".
	// The header is sent as well since it is harmless and some deployments read it.
	payload, err := json.Marshal(map[string]string{"personal_token": pat})
	if err != nil {
		return domain.QoderPATCredential{}, err
	}
	request, err := http.NewRequest(http.MethodPost, exchangeURL, strings.NewReader(string(payload)))
	if err != nil {
		return domain.QoderPATCredential{}, err
	}
	request.Header.Set("Authorization", "Bearer "+pat)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return domain.QoderPATCredential{}, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Never interpolate the PAT here: exchange errors flow into request logs.
		return domain.QoderPATCredential{}, fmt.Errorf("qoder job token exchange failed with HTTP %d: %s",
			response.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed qoderJobTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return domain.QoderPATCredential{}, fmt.Errorf("decode qoder job token response: %w", err)
	}
	jobToken := firstNonEmpty(parsed.JobToken, parsed.Token, parsed.AccessToken)
	expiresIn := firstPositiveInt64(parsed.ExpiresIn, parsed.TTL)
	expiresAt := parsed.ExpiresAt
	if parsed.Data != nil {
		jobToken = firstNonEmpty(jobToken, parsed.Data.JobToken, parsed.Data.Token, parsed.Data.AccessToken)
		expiresIn = firstPositiveInt64(expiresIn, parsed.Data.ExpiresIn, parsed.Data.TTL)
		expiresAt = firstPositiveInt64(expiresAt, parsed.Data.ExpiresAt)
	}
	if jobToken == "" {
		return domain.QoderPATCredential{}, fmt.Errorf("qoder job token response contained no token")
	}
	return domain.QoderPATCredential{
		AccessToken: jobToken,
		// The exchange never rotates the PAT, so it is always carried forward.
		RefreshToken: pat,
		ExpiresAt:    qoderTokenExpiry(jobToken, expiresIn, expiresAt, time.Now()).UTC().Format(time.RFC3339),
		AccountLabel: qoderAccountLabel,
		Connected:    true,
	}, nil
}

// qoderTokenNeedsRefresh matches cursorTokenNeedsRefresh, including its
// deliberate fail-open behaviour on a missing or unparseable expiry: sending a
// possibly-stale token and letting the failover path handle the 401 beats
// hitting the exchange endpoint on every single request.
func qoderTokenNeedsRefresh(credential *domain.QoderPATCredential) bool {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return true
	}
	if strings.TrimSpace(credential.ExpiresAt) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, credential.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(expiresAt)
}

// ensureFreshQoderToken guarantees the provider holds a usable job token. It is
// called on every proxied request, so the unexpired path performs no network
// I/O at all.
func (s *Server) ensureFreshQoderToken(provider domain.Provider) (domain.Provider, error) {
	if provider.AuthType != domain.AuthTypeQoderPAT {
		return provider, nil
	}
	if provider.QoderPAT == nil || strings.TrimSpace(provider.QoderPAT.RefreshToken) == "" {
		return provider, fmt.Errorf("provider %q has no Qoder personal access token; paste one in provider settings", provider.ID)
	}
	if !qoderTokenNeedsRefresh(provider.QoderPAT) {
		return provider, nil
	}
	refreshed, err := exchangeQoderJobToken(provider.QoderPAT.RefreshToken)
	if err != nil {
		return provider, err
	}
	updated, err := s.router.SetProviderQoderPAT(provider.ID, refreshed)
	if err != nil {
		return provider, err
	}
	_ = s.persistProviderOAuth(updated.ID, nil, nil, nil, updated.QoderPAT)
	return updated, nil
}

// syncQoderProviderModels installs the model catalog. Qoder's direct endpoint
// serves no /models route, so this uses the probed alias list directly instead
// of attempting a fetch that always 404s.
func (s *Server) syncQoderProviderModels(providerID string) (domain.Provider, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, err
	}
	updated, err := s.router.UpdateProviderModels(providerID, defaultQoderModels(providerID), "healthy")
	if err != nil {
		return provider, err
	}
	if err := s.saveState(); err != nil {
		return updated, err
	}
	return updated, nil
}
