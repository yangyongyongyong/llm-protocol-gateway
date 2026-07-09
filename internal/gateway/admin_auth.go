package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	adminSessionCookieName = "gateway_admin_session"
	adminSessionTTL        = 7 * 24 * time.Hour
	adminPasswordMinLen    = 8
	settingAdminPassword   = "adminPasswordHash"
	settingAdminSession    = "adminSessionSecret"
)

// AdminAuthStore persists the admin password hash and session signing secret.
type AdminAuthStore interface {
	Setting(key string) string
	SetSetting(key, value string) error
}

type adminAuthStatus struct {
	Configured    bool `json:"configured"`
	Authenticated bool `json:"authenticated"`
	RequireAuth   bool `json:"requireAuth"`
	LocalBypass   bool `json:"localBypass"`
}

func (s *Server) SetAdminAuthStore(store AdminAuthStore) {
	s.adminAuth = store
}

func (s *Server) withAdminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isModelProtocolPath(r.URL.Path) || isAdminAuthPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		localBypass := isLocalAdminBypass(r)
		if localBypass {
			next.ServeHTTP(w, r)
			return
		}

		configured := s.adminAuthConfigured()
		if !configured {
			// Non-local must set an admin password before using the console.
			s.denyAdminAuth(w, r, "admin password setup required")
			return
		}
		if s.adminSessionValid(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.denyAdminAuth(w, r, "admin authentication required")
	})
}

func (s *Server) denyAdminAuth(w http.ResponseWriter, r *http.Request, message string) {
	if wantsJSONAuthError(r) || strings.HasPrefix(r.URL.Path, "/__") {
		writeOpenAIError(w, http.StatusUnauthorized, message)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

func isAdminAuthPublicPath(path string) bool {
	switch path {
	case "/__health", "/__auth/status", "/__auth/setup", "/__auth/login", "/__auth/logout":
		return true
	case "/login", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(path, "/assets/")
}

func wantsJSONAuthError(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/__")
}

func isLocalAdminBypass(r *http.Request) bool {
	host := requestHostname(r.Host)
	if !isLoopbackHost(host) {
		return false
	}
	ip := requestRemoteIP(r)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func requestRemoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	return net.ParseIP(host)
}

func (s *Server) adminAuthConfigured() bool {
	if s.adminAuth == nil {
		return false
	}
	return strings.TrimSpace(s.adminAuth.Setting(settingAdminPassword)) != ""
}

func (s *Server) adminSessionValid(r *http.Request) bool {
	if s.adminAuth == nil {
		return false
	}
	secret := strings.TrimSpace(s.adminAuth.Setting(settingAdminSession))
	if secret == "" {
		return false
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil || cookie == nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	return verifyAdminSessionToken(cookie.Value, secret)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	localBypass := isLocalAdminBypass(r)
	configured := s.adminAuthConfigured()
	authenticated := localBypass || s.adminSessionValid(r)
	writeJSON(w, http.StatusOK, adminAuthStatus{
		Configured:    configured,
		Authenticated: authenticated,
		RequireAuth:   !localBypass,
		LocalBypass:   localBypass,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.adminAuth == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "admin auth store is not configured")
		return
	}
	if s.adminAuthConfigured() {
		writeOpenAIError(w, http.StatusConflict, "admin password is already configured")
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	password := strings.TrimSpace(payload.Password)
	if len(password) < adminPasswordMinLen {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", adminPasswordMinLen))
		return
	}
	if err := s.setAdminPassword(password); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.issueAdminSession(w, r)
	s.logs.AddApp("info", "admin password configured", "")
	localBypass := isLocalAdminBypass(r)
	writeJSON(w, http.StatusOK, adminAuthStatus{
		Configured:    true,
		Authenticated: true,
		RequireAuth:   !localBypass,
		LocalBypass:   localBypass,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminAuth == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "admin auth store is not configured")
		return
	}
	if !s.adminAuthConfigured() {
		writeOpenAIError(w, http.StatusBadRequest, "admin password is not configured yet")
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	hash := s.adminAuth.Setting(settingAdminPassword)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(payload.Password)); err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	s.issueAdminSession(w, r)
	s.logs.AddApp("info", "admin login success", requestHostname(r.Host))
	localBypass := isLocalAdminBypass(r)
	writeJSON(w, http.StatusOK, adminAuthStatus{
		Configured:    true,
		Authenticated: true,
		RequireAuth:   !localBypass,
		LocalBypass:   localBypass,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	clearAdminSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if s.adminAuth == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "admin auth store is not configured")
		return
	}
	localBypass := isLocalAdminBypass(r)
	if !localBypass && !s.adminSessionValid(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "admin authentication required")
		return
	}
	var payload struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	newPassword := strings.TrimSpace(payload.NewPassword)
	if len(newPassword) < adminPasswordMinLen {
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", adminPasswordMinLen))
		return
	}
	configured := s.adminAuthConfigured()
	if configured {
		// Non-local must prove current password. Local App may omit it for recovery.
		if !localBypass || strings.TrimSpace(payload.CurrentPassword) != "" {
			hash := s.adminAuth.Setting(settingAdminPassword)
			if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(payload.CurrentPassword)); err != nil {
				writeOpenAIError(w, http.StatusUnauthorized, "current password is incorrect")
				return
			}
		}
	}
	if err := s.setAdminPassword(newPassword); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.issueAdminSession(w, r)
	s.logs.AddApp("info", "admin password updated", "")
	writeJSON(w, http.StatusOK, adminAuthStatus{
		Configured:    true,
		Authenticated: true,
		RequireAuth:   !localBypass,
		LocalBypass:   localBypass,
	})
}

func (s *Server) setAdminPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	secret, err := randomToken(32)
	if err != nil {
		return fmt.Errorf("session secret: %w", err)
	}
	if err := s.adminAuth.SetSetting(settingAdminPassword, string(hash)); err != nil {
		return err
	}
	if err := s.adminAuth.SetSetting(settingAdminSession, secret); err != nil {
		return err
	}
	return nil
}

func (s *Server) issueAdminSession(w http.ResponseWriter, r *http.Request) {
	secret := ""
	if s.adminAuth != nil {
		secret = strings.TrimSpace(s.adminAuth.Setting(settingAdminSession))
	}
	if secret == "" {
		return
	}
	token, err := mintAdminSessionToken(secret, time.Now().Add(adminSessionTTL))
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		Expires:  time.Now().Add(adminSessionTTL),
		MaxAge:   int(adminSessionTTL.Seconds()),
	})
}

func clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	return proto == "https"
}

func mintAdminSessionToken(secret string, expiry time.Time) (string, error) {
	nonce, err := randomToken(16)
	if err != nil {
		return "", err
	}
	payload := strconv.FormatInt(expiry.Unix(), 10) + "." + nonce
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

func verifyAdminSessionToken(token, secret string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	expiryUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expiryUnix {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(parts[2]))
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
