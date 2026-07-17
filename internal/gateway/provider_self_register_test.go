package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestValidateSelfRegisterBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "public https", url: "https://foo.trycloudflare.com/v1/chat/completions", wantErr: false},
		{name: "public http", url: "http://foo.example.com/v1/messages", wantErr: false},
		{name: "loopback ip", url: "http://127.0.0.1:8080/v1/chat/completions", wantErr: true},
		{name: "loopback host", url: "http://localhost:8080/v1/chat/completions", wantErr: true},
		{name: "private ip 10.x", url: "http://10.0.0.5/v1/chat/completions", wantErr: true},
		{name: "private ip 192.168.x", url: "http://192.168.1.5/v1/chat/completions", wantErr: true},
		{name: "link-local", url: "http://169.254.1.1/v1/chat/completions", wantErr: true},
		{name: "dot-local host", url: "http://myserver.local/v1/chat/completions", wantErr: true},
		{name: "bad scheme", url: "ftp://foo.example.com/x", wantErr: true},
		{name: "no host", url: "https:///v1/x", wantErr: true},
		{name: "invalid url", url: "://bad", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSelfRegisterBaseURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateSelfRegisterBaseURL(%q) err=%v wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestGenerateAndHashSelfRegistrationTokenRoundTrip(t *testing.T) {
	t.Parallel()
	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(raw, selfRegistrationTokenPrefix) {
		t.Fatalf("raw token missing prefix: %q", raw)
	}
	if len(preview) != 4 || !strings.HasSuffix(raw, preview) {
		t.Fatalf("preview %q is not the raw token's last 4 chars (raw=%q)", preview, raw)
	}
	if hashSelfRegistrationToken(raw) != hash {
		t.Fatalf("hashSelfRegistrationToken(raw) != returned hash")
	}
	raw2, hash2, _, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate2: %v", err)
	}
	if raw2 == raw || hash2 == hash {
		t.Fatalf("two generated tokens must not collide")
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPatch, "/x", nil)
	if got := bearerToken(req); got != "" {
		t.Fatalf("no header: got %q", got)
	}
	req.Header.Set("Authorization", "Bearer abc123")
	if got := bearerToken(req); got != "abc123" {
		t.Fatalf("got %q want abc123", got)
	}
	req.Header.Set("Authorization", "Basic xxx")
	if got := bearerToken(req); got != "" {
		t.Fatalf("non-bearer scheme should yield empty, got %q", got)
	}
}

func TestIsSelfRegisterPathVsTokenEndpoints(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPatch, "/__providers/p1/self-register", true},
		{http.MethodPost, "/__providers/p1/self-register", false}, // wrong method
		{http.MethodPost, "/__providers/p1/self-register-token", false},
		{http.MethodPost, "/__providers/p1/self-register-token/revoke", false},
		{http.MethodPatch, "/__providers/p1", false},
	}
	for _, tc := range cases {
		if got := isSelfRegisterPath(tc.method, tc.path); got != tc.want {
			t.Fatalf("isSelfRegisterPath(%s, %s) = %v want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestRouterSelfRegistrationLifecycle(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://old.example/v1/chat/completions", APIKeySource: "literal:old-key"},
		},
	})

	if hash := router.ProviderSelfRegistrationTokenHash("p1"); hash != "" {
		t.Fatalf("expected no token hash before setup, got %q", hash)
	}

	updated, err := router.SetProviderSelfRegistrationToken("p1", domain.ProviderSelfRegistration{TokenHash: "hash1", TokenPreview: "abcd", CreatedAt: "now"})
	if err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}
	if updated.SelfRegistration == nil || updated.SelfRegistration.TokenHash != "hash1" {
		t.Fatalf("token not stored: %+v", updated.SelfRegistration)
	}
	if got := router.ProviderSelfRegistrationTokenHash("p1"); got != "hash1" {
		t.Fatalf("ProviderSelfRegistrationTokenHash = %q want hash1", got)
	}

	newBase := "https://new.example.com/v1/chat/completions"
	newKey := "literal:new-key"
	updated, err = router.SelfRegisterProvider("p1", &newBase, &newKey, nil, nil, "seen-at-1")
	if err != nil {
		t.Fatalf("SelfRegisterProvider: %v", err)
	}
	if updated.BaseURL != newBase || updated.APIKeySource != newKey {
		t.Fatalf("baseUrl/apiKeySource not updated: %+v", updated)
	}
	if updated.SelfRegistration.LastSeenAt != "seen-at-1" {
		t.Fatalf("LastSeenAt not updated: %+v", updated.SelfRegistration)
	}
	if updated.Name != "P1" {
		t.Fatalf("unrelated field Name must stay untouched, got %q", updated.Name)
	}

	// Partial update: only apiKeySource supplied — baseUrl must stay as just set above.
	onlyKey := "literal:only-key-changed"
	updated, err = router.SelfRegisterProvider("p1", nil, &onlyKey, nil, nil, "seen-at-2")
	if err != nil {
		t.Fatalf("SelfRegisterProvider partial: %v", err)
	}
	if updated.BaseURL != newBase {
		t.Fatalf("partial update must not touch baseUrl, got %q", updated.BaseURL)
	}
	if updated.APIKeySource != onlyKey {
		t.Fatalf("apiKeySource not updated by partial call, got %q", updated.APIKeySource)
	}

	updated, err = router.RevokeProviderSelfRegistration("p1")
	if err != nil {
		t.Fatalf("RevokeProviderSelfRegistration: %v", err)
	}
	if updated.SelfRegistration != nil {
		t.Fatalf("expected self-registration cleared, got %+v", updated.SelfRegistration)
	}
	if got := router.ProviderSelfRegistrationTokenHash("p1"); got != "" {
		t.Fatalf("token hash must be gone after revoke, got %q", got)
	}
}

// TestRouterSelfRegisterProviderProtocolSwitch verifies the self-register
// script can correct a protocol mismatch (e.g. Provider created as Claude but
// the actual local server only speaks OpenAI Chat) purely through the router
// layer, independent of authHeader defaulting (that lives in the handler).
func TestRouterSelfRegisterProviderProtocolSwitch(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude, AuthHeader: "x-api-key", BaseURL: "https://old.example/v1/messages"},
		},
	})
	protocol := string(domain.ProtocolOpenAIChat)
	authHeader := "Authorization"
	updated, err := router.SelfRegisterProvider("p1", nil, nil, &protocol, &authHeader, "seen-at")
	if err != nil {
		t.Fatalf("SelfRegisterProvider: %v", err)
	}
	if updated.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("protocol not switched, got %q", updated.Protocol)
	}
	if updated.AuthHeader != "Authorization" {
		t.Fatalf("authHeader not switched, got %q", updated.AuthHeader)
	}
}

// TestProviderSelfRegisterHandlerProtocolMismatchFix exercises the exact
// real-world scenario from support: a Provider created as Claude
// (authHeader=x-api-key) whose actual self-hosted script only speaks OpenAI
// Chat. The script's self-register call declares protocol=openai_chat
// without an explicit authHeader, and the handler must derive
// authHeader=Authorization automatically.
func TestProviderSelfRegisterHandlerProtocolMismatchFix(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude, AuthHeader: "x-api-key", BaseURL: "https://old.example/v1/messages"},
		},
	})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	handler := server.Handler()

	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := router.SetProviderSelfRegistrationToken("p1", domain.ProviderSelfRegistration{TokenHash: hash, TokenPreview: preview, CreatedAt: nowRFC3339()}); err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"baseUrl":  "https://tunnel.example.com/v1/chat/completions",
		"protocol": "openai_chat",
		// authHeader deliberately omitted: handler must derive it.
	})
	req := httptest.NewRequest(http.MethodPatch, "/__providers/p1/self-register", &buf)
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.5:443"
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Protocol   string `json:"protocol"`
		AuthHeader string `json:"authHeader"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Protocol != "openai_chat" || resp.AuthHeader != "Authorization" {
		t.Fatalf("response protocol/authHeader = %q/%q, want openai_chat/Authorization", resp.Protocol, resp.AuthHeader)
	}
	updated, err := router.ProviderByID("p1")
	if err != nil {
		t.Fatalf("ProviderByID: %v", err)
	}
	if updated.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("provider protocol not persisted, got %q", updated.Protocol)
	}
	if updated.AuthHeader != "Authorization" {
		t.Fatalf("provider authHeader not auto-derived, got %q", updated.AuthHeader)
	}
}

// TestProviderSelfRegisterHandlerRejectsInvalidProtocol ensures an unknown
// protocol value is rejected with 400 and never applied.
func TestProviderSelfRegisterHandlerRejectsInvalidProtocol(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude, AuthHeader: "x-api-key", BaseURL: "https://old.example/v1/messages"},
		},
	})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	handler := server.Handler()

	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := router.SetProviderSelfRegistrationToken("p1", domain.ProviderSelfRegistration{TokenHash: hash, TokenPreview: preview, CreatedAt: nowRFC3339()}); err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"protocol": "some_bogus_protocol"})
	req := httptest.NewRequest(http.MethodPatch, "/__providers/p1/self-register", &buf)
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.5:443"
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := router.ProviderByID("p1")
	if err != nil {
		t.Fatalf("ProviderByID: %v", err)
	}
	if updated.Protocol != domain.ProtocolClaude {
		t.Fatalf("protocol must stay unchanged after rejected update, got %q", updated.Protocol)
	}
}

// TestProviderSelfRegisterRejectsOAuthProviders ensures the endpoint refuses
// to (silently no-op) update OAuth-typed providers, whose BaseURL is pinned
// by normalizeProvider regardless of what a caller sends.
func TestProviderSelfRegisterRejectsOAuthProviders(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "oauth1", Name: "OAuth1", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeClaudeOAuth, BaseURL: "https://api.anthropic.com"},
		},
	})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	handler := server.Handler()

	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := router.SetProviderSelfRegistrationToken("oauth1", domain.ProviderSelfRegistration{TokenHash: hash, TokenPreview: preview, CreatedAt: nowRFC3339()}); err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"baseUrl": "https://tunnel.example.com/v1/messages"})
	req := httptest.NewRequest(http.MethodPatch, "/__providers/oauth1/self-register", &buf)
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.5:443"
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oauth provider self-register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := router.ProviderByID("oauth1")
	if err != nil {
		t.Fatalf("ProviderByID: %v", err)
	}
	if updated.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("oauth provider baseUrl must stay untouched, got %q", updated.BaseURL)
	}
}

// TestProviderSelfRegisterHandlerEndToEnd exercises the full bearer-token
// flow through the real HTTP handler chain (server.Handler(), i.e. including
// withAdminAuth), proving PATCH /__providers/{id}/self-register bypasses
// console session/cookie auth entirely and is gated purely by the
// provider-scoped token.
func TestProviderSelfRegisterHandlerEndToEnd(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{
			{ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://old.example/v1/chat/completions", APIKeySource: "literal:old-key"},
		},
	})
	logs := monitor.NewStore()
	server := NewServer(router, logs)
	handler := server.Handler()

	doRequest := func(token string, body map[string]any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(http.MethodPatch, "/__providers/p1/self-register", &buf)
		// Non-loopback host: proves this bypasses the console session
		// requirement that would otherwise apply on a public hostname.
		req.Host = "gateway.example.com"
		req.RemoteAddr = "203.0.113.5:443"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1) Self-registration not set up yet -> 404, even with a made-up token.
	rec := doRequest("whatever", map[string]any{"baseUrl": "https://tunnel.example.com/v1/chat/completions"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("before setup: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2) Generate a token directly via the router (mirrors what the
	// console-authenticated generate-token handler does).
	raw, hash, preview, err := generateSelfRegistrationToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := router.SetProviderSelfRegistrationToken("p1", domain.ProviderSelfRegistration{TokenHash: hash, TokenPreview: preview, CreatedAt: nowRFC3339()}); err != nil {
		t.Fatalf("SetProviderSelfRegistrationToken: %v", err)
	}

	// 3) Missing bearer token -> 401.
	rec = doRequest("", map[string]any{"baseUrl": "https://tunnel.example.com/v1/chat/completions"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 4) Wrong token -> 401.
	rec = doRequest("wrong-token", map[string]any{"baseUrl": "https://tunnel.example.com/v1/chat/completions"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 5) Correct token + private-IP baseUrl -> 400 (SSRF guard), provider
	// unchanged.
	rec = doRequest(raw, map[string]any{"baseUrl": "http://10.0.0.5/v1/chat/completions"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("private ip baseUrl: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 6) Correct token + valid public baseUrl + new apiKeySource -> 200, and
	// the provider record is actually updated end-to-end.
	rec = doRequest(raw, map[string]any{
		"baseUrl":      "https://tunnel.example.com/v1/chat/completions",
		"apiKeySource": "literal:new-shared-secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid self-register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := router.ProviderByID("p1")
	if err != nil {
		t.Fatalf("ProviderByID: %v", err)
	}
	if updated.BaseURL != "https://tunnel.example.com/v1/chat/completions" {
		t.Fatalf("baseUrl not applied: %+v", updated)
	}
	if updated.APIKeySource != "literal:new-shared-secret" {
		t.Fatalf("apiKeySource not applied: %+v", updated)
	}
	if updated.SelfRegistration == nil || updated.SelfRegistration.LastSeenAt == "" {
		t.Fatalf("expected LastSeenAt heartbeat to be recorded: %+v", updated.SelfRegistration)
	}

	// 7) After revoking, the same (previously valid) token must stop working.
	if _, err := router.RevokeProviderSelfRegistration("p1"); err != nil {
		t.Fatalf("RevokeProviderSelfRegistration: %v", err)
	}
	rec = doRequest(raw, map[string]any{"baseUrl": "https://tunnel2.example.com/v1/chat/completions"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoked token should 404: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
