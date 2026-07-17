package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// selfRegistrationTokenPrefix marks tokens as belonging to this feature, so a
// leaked value is recognizable in logs/support requests without decoding it.
const selfRegistrationTokenPrefix = "sr_"

// generateSelfRegistrationToken returns a fresh high-entropy raw token, its
// sha256 hex digest (what we persist), and a short non-secret preview (last 4
// chars) for display. The raw value is never stored — callers must return it
// to the caller exactly once.
func generateSelfRegistrationToken() (raw, hash, preview string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	raw = selfRegistrationTokenPrefix + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	if len(raw) >= 4 {
		preview = raw[len(raw)-4:]
	}
	return raw, hash, preview, nil
}

func hashSelfRegistrationToken(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// handleGenerateProviderSelfRegistrationToken (re)issues the self-register
// bearer token for a provider (owner/admin only, normal console session
// auth). Any previously issued token is invalidated. The raw token is
// returned exactly once and never persisted or retrievable again.
func (s *Server) handleGenerateProviderSelfRegistrationToken(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	if _, err := s.router.ProviderByID(providerID); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate token: "+err.Error())
		return
	}
	now := nowRFC3339()
	updated, err := s.router.SetProviderSelfRegistrationToken(providerID, domain.ProviderSelfRegistration{
		TokenHash:    hash,
		TokenPreview: preview,
		CreatedAt:    now,
	})
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider self-registration token generated", updated.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     raw, // shown once; not recoverable afterwards
		"preview":   preview,
		"createdAt": now,
		"provider":  redactProviderForClient(updated),
	})
}

// handleRevokeProviderSelfRegistration disables self-registration for a
// provider (owner/admin only); any previously issued token stops working
// immediately.
func (s *Server) handleRevokeProviderSelfRegistration(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, providerID) {
		return
	}
	updated, err := s.router.RevokeProviderSelfRegistration(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	s.logs.AddApp("info", "provider self-registration revoked", providerID)
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}

// isSelfRegisterPath matches the machine-facing self-register endpoint so the
// admin-auth middleware can let it bypass console session/cookie auth
// entirely — handleProviderSelfRegister does its own bearer-token check.
func isSelfRegisterPath(method, path string) bool {
	return method == http.MethodPatch && strings.HasSuffix(path, "/self-register") && strings.HasPrefix(path, "/__providers/")
}

// handleProviderSelfRegister is the machine-facing endpoint a provider
// owner's own automation script calls (e.g. whenever a reverse-tunnel URL
// rotates) to update BaseURL/APIKeySource. Authenticated purely via
// `Authorization: Bearer <provider-scoped token>` — no console session/cookie
// involved, so it carries no CSRF surface and the token grants no permission
// beyond this one provider's connection info.
func (s *Server) handleProviderSelfRegister(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.authenticateSelfRegistrationRequest(w, r, providerID) {
		return
	}
	// OAuth-typed providers pin BaseURL to their fixed upstream (see
	// normalizeProvider); a self-register call would silently no-op on
	// baseUrl there. Reject explicitly instead of returning a false "ok".
	if provider, err := s.router.ProviderByID(providerID); err == nil {
		switch provider.AuthType {
		case domain.AuthTypeClaudeOAuth, domain.AuthTypeCursorOAuth, domain.AuthTypeChatGPTOAuth:
			writeOpenAIError(w, http.StatusBadRequest, "self-registration is only supported for api_key providers, not OAuth-connected providers")
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}
	var raw map[string]json.RawMessage
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &raw); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	}

	var baseURL, apiKeySource, protocolValue, authHeaderValue *string
	if value, ok := raw["baseUrl"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "baseUrl must be a string")
			return
		}
		v = strings.TrimSpace(v)
		if v == "" {
			writeOpenAIError(w, http.StatusBadRequest, "baseUrl cannot be empty")
			return
		}
		if err := validateSelfRegisterBaseURL(v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}
		baseURL = &v
	}
	if value, ok := raw["apiKeySource"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "apiKeySource must be a string")
			return
		}
		apiKeySource = &v
	}
	// protocol: lets the self-hosted service declare what API shape it
	// actually implements, so a mismatch (e.g. Provider created as Claude but
	// the script only speaks OpenAI Chat) can be corrected by the script
	// itself instead of requiring a console edit.
	if value, ok := raw["protocol"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "protocol must be a string")
			return
		}
		v = strings.TrimSpace(v)
		switch domain.Protocol(v) {
		case domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude:
		default:
			writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf(
				"protocol must be one of %q, %q, %q", domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude))
			return
		}
		protocolValue = &v
	}
	if value, ok := raw["authHeader"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "authHeader must be a string")
			return
		}
		v = strings.TrimSpace(v)
		if v == "" {
			writeOpenAIError(w, http.StatusBadRequest, "authHeader cannot be empty")
			return
		}
		authHeaderValue = &v
	}
	// Caller declared a protocol but not an explicit header name: default to
	// the same convention the console's create-provider form uses (Claude ->
	// x-api-key, everything else -> Authorization), so switching protocol via
	// self-register "just works" without also requiring an authHeader field.
	if protocolValue != nil && authHeaderValue == nil {
		derived := "Authorization"
		if domain.Protocol(*protocolValue) == domain.ProtocolClaude {
			derived = "x-api-key"
		}
		authHeaderValue = &derived
	}
	if baseURL == nil && apiKeySource == nil && protocolValue == nil && authHeaderValue == nil {
		writeOpenAIError(w, http.StatusBadRequest, "at least one of baseUrl/apiKeySource/protocol/authHeader is required")
		return
	}

	updated, err := s.router.SelfRegisterProvider(providerID, baseURL, apiKeySource, protocolValue, authHeaderValue, nowRFC3339())
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	// A live connection just registered itself: clear any stale "unavailable"
	// mark so the console doesn't keep showing 异常 from before the tunnel
	// came back with a new address.
	s.markProviderAvailable(providerID)
	s.logs.AddApp("info", "provider self-registered", fmt.Sprintf("provider=%s ip=%s protocol=%s", providerID, requestClientIP(r), updated.Protocol))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"providerId": updated.ID,
		"baseUrl":    updated.BaseURL,
		"protocol":   updated.Protocol,
		"authHeader": updated.AuthHeader,
		"updatedAt":  nowRFC3339(),
	})
}

// authenticateSelfRegistrationRequest verifies the Authorization: Bearer
// header against providerID's self-registration token. Shared by the
// self-register endpoint and the two self-check endpoints below — they all
// use the exact same provider-scoped, non-console trust model.
func (s *Server) authenticateSelfRegistrationRequest(w http.ResponseWriter, r *http.Request, providerID string) bool {
	token := bearerToken(r)
	if token == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "missing bearer token")
		return false
	}
	storedHash := s.router.ProviderSelfRegistrationTokenHash(providerID)
	if storedHash == "" {
		writeOpenAIError(w, http.StatusNotFound, "self-registration is not enabled for this provider")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(hashSelfRegistrationToken(token)), []byte(storedHash)) != 1 {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid token")
		return false
	}
	return true
}

// isSelfCheckPath matches the machine-facing self-check endpoints (health /
// chat) so the admin-auth middleware can let them bypass console
// session/cookie auth entirely, exactly like isSelfRegisterPath — both
// self-check handlers do their own bearer-token check via
// authenticateSelfRegistrationRequest.
func isSelfCheckPath(method, path string) bool {
	if method != http.MethodPost || !strings.HasPrefix(path, "/__providers/") {
		return false
	}
	return strings.HasSuffix(path, "/self-check/health") || strings.HasSuffix(path, "/self-check/chat")
}

// handleProviderSelfCheckHealth lets a self-registered provider's own script
// trigger a real connectivity+auth check against its current configuration
// (same check as the console's "获取模型" button), authenticated purely via
// its self-registration bearer token. The generated setup prompt tells
// scripts to call this 3 times and require success all 3 times before
// considering the local service correctly wired to the gateway.
func (s *Server) handleProviderSelfCheckHealth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.authenticateSelfRegistrationRequest(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	started := time.Now()
	result := s.fetchProviderModels(r, provider, started)
	s.logs.AddApp("info", "provider self-check health", fmt.Sprintf("provider=%s success=%v status=%d latency=%dms", providerID, result.Success, result.Status, result.LatencyMs))
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   result.Success,
		"status":    result.Status,
		"latencyMs": result.LatencyMs,
		"error":     result.Error,
	})
}

// selfCheckChatPrompt is the fixed test message for handleProviderSelfCheckChat.
// Deliberately not client-configurable: the point of a self-check is a known,
// stable, cheap probe — a script cannot substitute its own prompt here.
const selfCheckChatPrompt = "2+2等于几"

// handleProviderSelfCheckChat lets a self-registered provider's own script
// trigger a real end-to-end chat round-trip (same mechanism as the console's
// "对话测试" button, generalized to all three protocols — see
// testProviderChat), authenticated purely via its self-registration bearer
// token. A single success is enough; the prompt is always the fixed
// selfCheckChatPrompt regardless of any request body the caller sends.
func (s *Server) handleProviderSelfCheckChat(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.authenticateSelfRegistrationRequest(w, r, providerID) {
		return
	}
	started := time.Now()
	result, status := s.testProviderChat(r, providerID, providerChatTestRequest{UserPrompt: selfCheckChatPrompt}, started)
	success, _ := result["success"].(bool)
	s.logs.AddApp("info", "provider self-check chat", fmt.Sprintf("provider=%s success=%v", providerID, success))
	writeJSON(w, status, map[string]any{
		"success":   success,
		"status":    result["status"],
		"latencyMs": result["latencyMs"],
		"preview":   result["preview"],
		"error":     result["error"],
	})
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

// validateSelfRegisterBaseURL blocks obviously-private/loopback targets
// (SSRF guard): this endpoint lets an authenticated-but-external script set
// where the platform will send proxied LLM traffic, so it must not be usable
// to point the platform at its own internal network. This is a literal-value
// check (scheme + host/IP-literal), not full DNS-rebinding protection.
func validateSelfRegisterBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid baseUrl: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("baseUrl must use http or https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("baseUrl must include a host")
	}
	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".local") || strings.HasSuffix(lowerHost, ".internal") {
		return fmt.Errorf("baseUrl host %q is not allowed (internal/loopback hostname)", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("baseUrl host %q resolves to a private/loopback address, which is not allowed", host)
		}
	}
	return nil
}
