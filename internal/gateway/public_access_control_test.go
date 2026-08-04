package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/tunnel"
)

// boundCustomDomainState is a gateway state whose custom-domain bind is fully
// provisioned but currently switched off — the state a toggle call must not
// damage.
func boundCustomDomainState() domain.GatewayState {
	return domain.GatewayState{
		PublicAccess: domain.PublicAccessSettings{
			Enabled:          false,
			Provider:         "cloudflare",
			Mode:             domain.PublicAccessModeCustomDomain,
			ExposeAPI:        true,
			ExposeUI:         true,
			CustomDomain:     "api.example.com",
			UIDomain:         "console.example.com",
			TunnelName:       "gateway-tunnel",
			TunnelToken:      "tunnel-token-value",
			CredentialsFile:  "/tmp/creds.json",
			TunnelConfigFile: "/tmp/config.yml",
		},
	}
}

func newPublicAccessControlTestServer(t *testing.T, state domain.GatewayState) (*Server, *memoryAdminAuthStore, http.Handler) {
	t.Helper()
	router := NewRouter(state)
	server := NewServer(router, monitor.NewStore())
	store := newMemoryAdminAuthStore()
	server.SetAdminAuthStore(store)
	return server, store, server.Handler()
}

// storeControlToken installs a token the same way the mint endpoint would.
func storeControlToken(t *testing.T, store *memoryAdminAuthStore) string {
	t.Helper()
	raw, hash, preview, err := generatePublicAccessControlToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	_ = store.SetSetting(settingPublicAccessControlTokenHash, hash)
	_ = store.SetSetting(settingPublicAccessControlTokenPreview, preview)
	return raw
}

func controlRequest(t *testing.T, token, mode string, enabled bool) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{"mode": mode, "enabled": enabled}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/__public/control", &buf)
	// Non-loopback: the local-admin bypass must not be what lets this through.
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.5:443"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// Without a minted token the endpoint must not be usable at all, even though it
// bypasses the console session middleware.
func TestPublicAccessControlRejectsWhenTokenNotConfigured(t *testing.T) {
	_, _, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, "pac_whatever", "custom_domain", true))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

func TestPublicAccessControlRejectsMissingAndWrongToken(t *testing.T) {
	_, store, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())
	storeControlToken(t, store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, "", "custom_domain", true))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, "pac_wrong", "custom_domain", true))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
}

// A valid token still needs a tunnel manager; without one the caller gets a
// clear 503 rather than a nil dereference.
func TestPublicAccessControlWithoutTunnelManager(t *testing.T) {
	_, store, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())
	token := storeControlToken(t, store)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, token, "custom_domain", true))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
}

func TestPublicAccessControlRejectsBadBody(t *testing.T) {
	server, store, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())
	token := storeControlToken(t, store)
	server.SetTunnelManager(tunnel.NewManager(18093))

	// Unknown mode.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, token, "not_a_mode", true))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// Missing "enabled" must not be silently treated as false.
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"mode": "random_tunnel"})
	req := httptest.NewRequest(http.MethodPost, "/__public/control", &buf)
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.5:443"
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

// Turning off a mode that is not the active one must be a no-op: it must not
// stop the other mode's tunnel, and must not flip its Enabled flag.
func TestPublicAccessControlDisableInactiveModeIsNoop(t *testing.T) {
	state := boundCustomDomainState()
	state.PublicAccess.Mode = domain.PublicAccessModeRandomTunnel
	state.PublicAccess.Enabled = true
	state.PublicAccess.RuntimeURL = "https://random.trycloudflare.com"

	server, store, handler := newPublicAccessControlTestServer(t, state)
	token := storeControlToken(t, store)
	server.SetTunnelManager(tunnel.NewManager(18093))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, token, "custom_domain", false))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after := server.router.State().PublicAccess
	if !after.Enabled {
		t.Fatal("disabling the inactive mode must not disable the running one")
	}
	if after.Mode != domain.PublicAccessModeRandomTunnel {
		t.Fatalf("mode changed to %q, want random_tunnel untouched", after.Mode)
	}
	if after.RuntimeURL != "https://random.trycloudflare.com" {
		t.Fatalf("runtime url cleared: %q", after.RuntimeURL)
	}
}

// The core regression guard: Router.UpdatePublicAccess overwrites domain and
// tunnel-credential fields verbatim, so a toggle must carry the existing bind
// through instead of blanking it.
func TestPublicAccessSettingsWithModePreservesBinding(t *testing.T) {
	before := boundCustomDomainState().PublicAccess
	router := NewRouter(boundCustomDomainState())

	for _, tc := range []struct {
		name    string
		mode    domain.PublicAccessMode
		enabled bool
	}{
		{"enable custom domain", domain.PublicAccessModeCustomDomain, true},
		{"disable custom domain", domain.PublicAccessModeCustomDomain, false},
		{"switch to random tunnel", domain.PublicAccessModeRandomTunnel, true},
		{"disable random tunnel", domain.PublicAccessModeRandomTunnel, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := router.State().PublicAccess
			updated := router.UpdatePublicAccess(publicAccessSettingsWithMode(current, tc.mode, tc.enabled))
			if updated.Mode != tc.mode || updated.Enabled != tc.enabled {
				t.Fatalf("mode/enabled = %q/%v, want %q/%v", updated.Mode, updated.Enabled, tc.mode, tc.enabled)
			}
			if updated.CustomDomain != before.CustomDomain || updated.UIDomain != before.UIDomain {
				t.Fatalf("domains lost: api=%q ui=%q", updated.CustomDomain, updated.UIDomain)
			}
			if updated.TunnelName != before.TunnelName || updated.TunnelToken != before.TunnelToken {
				t.Fatalf("tunnel identity lost: name=%q token=%q", updated.TunnelName, updated.TunnelToken)
			}
			if updated.CredentialsFile != before.CredentialsFile || updated.TunnelConfigFile != before.TunnelConfigFile {
				t.Fatalf("tunnel files lost: creds=%q config=%q", updated.CredentialsFile, updated.TunnelConfigFile)
			}
			if !updated.ExposeAPI || !updated.ExposeUI {
				t.Fatalf("exposure flags lost: api=%v ui=%v", updated.ExposeAPI, updated.ExposeUI)
			}
		})
	}
}

// The control endpoint must be unreachable over the public tunnel, otherwise a
// leaked token would let anyone on the internet flip exposure. cloudflared runs
// locally so RemoteAddr looks like loopback — the check has to rely on
// Cloudflare's headers and the Host used.
func TestPublicAccessControlRefusesPublicOrigin(t *testing.T) {
	state := boundCustomDomainState()
	state.PublicAccess.Enabled = true
	server, store, handler := newPublicAccessControlTestServer(t, state)
	token := storeControlToken(t, store)
	server.SetTunnelManager(tunnel.NewManager(18093))

	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    int
	}{
		{
			name:    "cloudflare headers present",
			host:    "127.0.0.1:18093",
			headers: map[string]string{"CF-Connecting-IP": "203.0.113.9", "CF-Ray": "abc123-LAX"},
			want:    http.StatusForbidden,
		},
		// The API hostname serves only model-protocol paths, so the host-split
		// layer (withHostSeparatedServing) already 404s /__* there before the
		// handler runs — still unreachable, just via a different gate.
		{name: "api public hostname", host: "api.example.com", want: http.StatusNotFound},
		{name: "ui public hostname", host: "console.example.com", want: http.StatusForbidden},
		{name: "quick tunnel hostname", host: "random-words.trycloudflare.com", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := controlRequest(t, token, "random_tunnel", false)
			req.Host = tc.host
			// cloudflared dials the gateway from this machine, so a tunnelled
			// request genuinely arrives with a loopback RemoteAddr.
			req.RemoteAddr = "127.0.0.1:52344"
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

// Loopback and LAN callers must still get through (they are the intended
// callers), reaching the token check rather than the origin check.
func TestPublicAccessControlAllowsLocalAndLANOrigin(t *testing.T) {
	state := boundCustomDomainState()
	state.PublicAccess.Enabled = true
	server, store, handler := newPublicAccessControlTestServer(t, state)
	token := storeControlToken(t, store)
	server.SetTunnelManager(tunnel.NewManager(18093))

	for _, host := range []string{"127.0.0.1:18093", "192.168.1.20:18093", "mac-mini.local:18093"} {
		t.Run(host, func(t *testing.T) {
			req := controlRequest(t, token, "random_tunnel", false)
			req.Host = host
			req.RemoteAddr = "192.168.1.55:51000"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			// random_tunnel is not the active mode here, so this is the
			// idempotent no-op path: proves it got past both gates.
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
			}
		})
	}
}

// Minting is admin-console-only: the control token itself must never be able to
// issue or revoke a token (no privilege-escalation chain).
func TestPublicAccessControlTokenEndpointsRequireAdminSession(t *testing.T) {
	_, store, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())
	token := storeControlToken(t, store)

	for _, path := range []string{"/__public/control-token", "/__public/control-token/revoke"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "gateway.example.com"
		req.RemoteAddr = "203.0.113.5:443"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s accepted a control bearer token; must require an admin session", path)
		}
	}
}

// Mint → status → revoke round trip over the loopback admin bypass.
func TestPublicAccessControlTokenMintStatusRevoke(t *testing.T) {
	_, _, handler := newPublicAccessControlTestServer(t, boundCustomDomainState())

	localReq := func(method, path string) *http.Request {
		req := httptest.NewRequest(method, path, nil)
		req.Host = "127.0.0.1:18093"
		req.RemoteAddr = "127.0.0.1:4321"
		return req
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, localReq(http.MethodPost, "/__public/control-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint status=%d body=%s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Token   string `json:"token"`
		Preview string `json:"preview"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	if minted.Token == "" || minted.Preview == "" {
		t.Fatalf("mint response missing token/preview: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, localReq(http.MethodGet, "/__public/control-token"))
	var status struct {
		Configured bool   `json:"configured"`
		Preview    string `json:"preview"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Preview != minted.Preview {
		t.Fatalf("status=%+v, want configured with preview %q", status, minted.Preview)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, localReq(http.MethodPost, "/__public/control-token/revoke"))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The revoked token must stop working immediately.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, controlRequest(t, minted.Token, "random_tunnel", true))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoked token still accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
