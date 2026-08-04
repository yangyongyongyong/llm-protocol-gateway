package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// publicAccessControlTokenPrefix marks tokens as belonging to this feature, so a
// leaked value is recognizable in logs/support requests without decoding it.
const publicAccessControlTokenPrefix = "pac_"

// generatePublicAccessControlToken returns a fresh high-entropy raw token, its
// sha256 hex digest (what we persist), and a short non-secret preview (last 4
// chars) for display. The raw value is never stored — the caller must return it
// to the console exactly once.
func generatePublicAccessControlToken() (raw, hash, preview string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	raw = publicAccessControlTokenPrefix + hex.EncodeToString(buf)
	hash = hashPublicAccessControlToken(raw)
	if len(raw) >= 4 {
		preview = raw[len(raw)-4:]
	}
	return raw, hash, preview, nil
}

func hashPublicAccessControlToken(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// isPublicAccessControlPath matches the machine-facing public-access on/off
// toggle so the admin-auth middleware can let it bypass console session/cookie
// auth entirely — handlePublicAccessControl does its own bearer-token check,
// the same non-console trust model as isSelfRegisterPath / isSelfCheckPath.
func isPublicAccessControlPath(method, path string) bool {
	return method == http.MethodPost && path == "/__public/control"
}

// handleGeneratePublicAccessControlToken (re)issues the public-access control
// bearer token (admin console session only). Any previously issued token is
// invalidated. The raw token is returned exactly once and never retrievable
// again — only its hash and a last-4 preview are persisted.
func (s *Server) handleGeneratePublicAccessControlToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	raw, hash, preview, err := generatePublicAccessControlToken()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to generate token: "+err.Error())
		return
	}
	if err := s.adminAuth.SetSetting(settingPublicAccessControlTokenHash, hash); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save token: "+err.Error())
		return
	}
	if err := s.adminAuth.SetSetting(settingPublicAccessControlTokenPreview, preview); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save token: "+err.Error())
		return
	}
	now := nowRFC3339()
	s.logs.AddApp("info", "public access control token generated", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     raw, // shown once; not recoverable afterwards
		"preview":   preview,
		"createdAt": now,
	})
}

// handleRevokePublicAccessControlToken disables machine control of public
// access; any previously issued token stops working immediately.
func (s *Server) handleRevokePublicAccessControlToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if err := s.adminAuth.SetSetting(settingPublicAccessControlTokenHash, ""); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to revoke token: "+err.Error())
		return
	}
	_ = s.adminAuth.SetSetting(settingPublicAccessControlTokenPreview, "")
	s.logs.AddApp("info", "public access control token revoked", "")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

// handlePublicAccessControlTokenStatus reports whether a control token exists,
// for the console panel. The raw token is never returned again.
func (s *Server) handlePublicAccessControlTokenStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	hash := strings.TrimSpace(s.adminAuth.Setting(settingPublicAccessControlTokenHash))
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": hash != "",
		"preview":    strings.TrimSpace(s.adminAuth.Setting(settingPublicAccessControlTokenPreview)),
	})
}

// authenticatePublicAccessControlRequest verifies Authorization: Bearer against
// the stored control-token hash. Deliberately independent from the console
// session: this token's only capability is toggling public access.
func (s *Server) authenticatePublicAccessControlRequest(w http.ResponseWriter, r *http.Request) bool {
	token := bearerToken(r)
	if token == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "missing bearer token")
		return false
	}
	storedHash := strings.TrimSpace(s.adminAuth.Setting(settingPublicAccessControlTokenHash))
	if storedHash == "" {
		writeOpenAIError(w, http.StatusNotFound, "public access control token is not configured; generate one from the console first")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(hashPublicAccessControlToken(token)), []byte(storedHash)) != 1 {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid token")
		return false
	}
	return true
}

// requestArrivedFromPublic reports whether a request came in over the public
// tunnel rather than loopback / LAN.
//
// RemoteAddr is useless for this: cloudflared runs on this machine and connects
// to 127.0.0.1, so tunnelled requests look loopback at the socket level. The
// reliable signals are Cloudflare's own headers and the Host the caller used
// (either configured public hostname, or a quick-tunnel domain).
func (s *Server) requestArrivedFromPublic(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" || strings.TrimSpace(r.Header.Get("CF-Ray")) != "" {
		return true
	}
	settings := s.router.State().PublicAccess
	host := requestClientHost(r)
	if classifyAccessSource(host, settings.PublicBaseURL) == monitor.AccessSourcePublic {
		return true
	}
	return classifyAccessSource(host, settings.UIPublicBaseURL) == monitor.AccessSourcePublic
}

// publicAccessSettingsWithMode returns current with only Mode/Enabled changed.
//
// Router.UpdatePublicAccess assigns the domain and named-tunnel fields verbatim
// (including empty values, so callers can clear a bind), so a toggle must hand
// back the full current settings rather than a freshly-built partial struct —
// otherwise flipping the switch would erase an existing custom-domain bind.
// Disabling also clears RuntimeURL, matching handlePublicStop.
func publicAccessSettingsWithMode(current domain.PublicAccessSettings, mode domain.PublicAccessMode, enabled bool) domain.PublicAccessSettings {
	current.Mode = mode
	current.Enabled = enabled
	if !enabled {
		current.RuntimeURL = ""
	}
	return current
}

// handlePublicAccessControl turns public access on/off for a given mode,
// authenticated purely via the control bearer token (no console session).
//
// Body: {"mode": "custom_domain" | "random_tunnel", "enabled": true | false}
//
// Reachable from loopback / LAN only: a token that could flip public exposure
// from the public internet would make the tunnel its own attack surface, so
// requests arriving over the tunnel are refused before the token is even
// checked (which also avoids telling a public caller whether one exists).
//
// Only one cloudflared tunnel runs at a time, so "mode" selects which kind of
// public access the caller means: enabling switches the active mode and starts
// the tunnel; disabling only stops it when that mode is the active one, making
// "turn off the mode that wasn't running" a harmless no-op instead of killing
// the other mode's tunnel.
func (s *Server) handlePublicAccessControl(w http.ResponseWriter, r *http.Request) {
	if s.requestArrivedFromPublic(r) {
		writeOpenAIError(w, http.StatusForbidden, "public access control is only reachable from the local machine or LAN, not over the public tunnel")
		return
	}
	if !s.authenticatePublicAccessControlRequest(w, r) {
		return
	}
	if s.tunnels == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "tunnel manager is not configured")
		return
	}

	var body struct {
		Mode    string `json:"mode"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.Enabled == nil {
		writeOpenAIError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	var mode domain.PublicAccessMode
	switch strings.TrimSpace(body.Mode) {
	case string(domain.PublicAccessModeCustomDomain):
		mode = domain.PublicAccessModeCustomDomain
	case string(domain.PublicAccessModeRandomTunnel):
		mode = domain.PublicAccessModeRandomTunnel
	default:
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("mode must be %q or %q",
			domain.PublicAccessModeCustomDomain, domain.PublicAccessModeRandomTunnel))
		return
	}

	current := s.router.State().PublicAccess

	if !*body.Enabled {
		if current.Mode == mode && current.Enabled {
			state := s.tunnels.Stop()
			s.router.UpdatePublicAccess(publicAccessSettingsWithMode(current, mode, false))
			_ = s.saveState()
			s.logs.AddApp("info", "public access stopped via control token", fmt.Sprintf("mode=%s status=%s", mode, state.Status))
		}
		writeJSON(w, http.StatusOK, s.withTunnelRuntime(s.router.State().PublicAccess))
		return
	}

	s.router.UpdatePublicAccess(publicAccessSettingsWithMode(current, mode, true))
	_ = s.saveState()

	settings := s.ensureSplitCustomDomains(r.Context())
	state, err := s.tunnels.Start(s.tunnelSettingsFromPublicAccess(settings))
	s.applyTunnelURL(state)
	settings = s.router.State().PublicAccess
	if err != nil {
		// Same contract as handlePublicStart: a failed start is reported through
		// status/statusMessage rather than an HTTP error, so callers always read
		// the resulting state from one place.
		s.logs.AddApp("warn", "public access start via control token failed", err.Error())
		writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
		return
	}
	s.logs.AddApp("info", "public access started via control token", fmt.Sprintf("mode=%s url=%s", state.Mode, state.PublicURL))
	writeJSON(w, http.StatusOK, s.withTunnelRuntime(settings))
}
