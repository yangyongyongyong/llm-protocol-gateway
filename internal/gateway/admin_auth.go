package gateway

import (
	"context"
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

	"github.com/luca/llm-protocol-gateway/internal/domain"
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

// UserStore persists console user accounts (multi-user management).
type UserStore interface {
	ListUsers() ([]domain.User, error)
	UserByID(id string) (domain.User, error)
	UserByUsername(username string) (domain.User, error)
	CreateUser(domain.User) error
	UpdateUser(domain.User) error
	DeleteUser(id string) error
	TouchUserLogin(id string, lastLoginAt string) error
	TouchUserActive(id string, lastActiveAt string) error
}

type adminAuthStatus struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	RequireAuth   bool   `json:"requireAuth"`
	LocalBypass   bool   `json:"localBypass"`
	Role          string `json:"role,omitempty"`
	Username      string `json:"username,omitempty"`
	UserID        string `json:"userId,omitempty"`
}

// sessionIdentity is the authenticated principal resolved from the session
// cookie (or local bypass, which is treated as the admin).
type sessionIdentity struct {
	UserID   string
	Username string
	Role     domain.UserRole
}

func (id sessionIdentity) isAdmin() bool { return id.Role == domain.UserRoleAdmin }

type authContextKey struct{}

// identityFromRequest returns the authenticated identity stored by withAdminAuth.
func identityFromRequest(r *http.Request) (sessionIdentity, bool) {
	value, ok := r.Context().Value(authContextKey{}).(sessionIdentity)
	return value, ok
}

// adminIdentity is the implicit identity for local bypass and legacy sessions.
func adminIdentity() sessionIdentity {
	return sessionIdentity{UserID: legacyAdminUserID, Username: "admin", Role: domain.UserRoleAdmin}
}

const legacyAdminUserID = "admin"

func (s *Server) SetAdminAuthStore(store AdminAuthStore) {
	s.adminAuth = store
}

func (s *Server) withAdminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isModelProtocolPath(r.URL.Path) || isAdminAuthPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Provider self-register: authenticated purely via its own
		// provider-scoped bearer token (see handleProviderSelfRegister), never
		// via console session/cookie. Bypasses this whole session/role
		// middleware entirely — no session/CSRF surface applies to it.
		if isSelfRegisterPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Provider self-check (health/chat): same bearer-token-only trust
		// model as self-register, also bypasses console session/cookie auth
		// entirely — see authenticateSelfRegistrationRequest.
		if isSelfCheckPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		localBypass := isLocalAdminBypass(r)
		if localBypass {
			// Local desktop app acts as the administrator.
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, adminIdentity())))
			return
		}

		configured := s.adminAuthConfigured()
		if !configured {
			// Non-local must set an admin password before using the console.
			s.denyAdminAuth(w, r, "admin password setup required")
			return
		}
		identity, ok := s.sessionIdentity(r)
		if !ok {
			s.denyAdminAuth(w, r, "authentication required")
			return
		}
		// Normal users may only access a whitelisted subset of console APIs.
		if !identity.isAdmin() && !isUserAllowedPath(r.Method, r.URL.Path) {
			writeOpenAIError(w, http.StatusForbidden, "permission denied")
			return
		}
		// 记录该用户浏览器最近一次控制台请求时间（内存精确，周期性节流落库）。
		s.noteUserActivity(identity.UserID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, identity)))
	})
}

// isUserAllowedPath whitelists console routes reachable by role=user.
// Data-level isolation (own keys only) is enforced inside each handler.
func isUserAllowedPath(method, path string) bool {
	switch path {
	case "/__state", "/__logs", "/__request-stats", "/__apikeys", "/__auth/password":
		return true
	}
	if strings.HasPrefix(path, "/__apikeys/") {
		return true
	}
	// Users may create providers (they become the owner) and create routes for
	// binding their keys; per-provider route/provider checks live in handlers.
	if method == http.MethodPost && (path == "/__providers" || path == "/__routes") {
		return true
	}
	// Owner-only provider management (enforced via requireProviderOwnerForUser
	// inside each handler): edit / delete / fetch-models / chat-test / preview.
	if isUserProviderManagementPath(method, path) {
		return true
	}
	// Read-only OAuth quota panels on the Providers page (no CRUD / test).
	if method == http.MethodGet && isUserProviderUsagePath(path) {
		return true
	}
	// Static UI assets and SPA pages are served by the same handler chain.
	if !strings.HasPrefix(path, "/__") && method == http.MethodGet {
		return true
	}
	return false
}

// isUserProviderManagementPath matches the provider endpoints a normal user
// may call on providers they own: PATCH/DELETE /__providers/{id}, plus
// POST {id}/test (获取模型), POST {id}/chat-test (对话测试),
// GET {id}/auth-preview (edit-modal helper) and the OAuth connect flows
// ({id}/(claude|cursor|chatgpt)-oauth/start|status|complete|disconnect) so
// users can log their own accounts into providers they created. Ownership
// itself is checked in the handlers via requireProviderOwnerForUser.
func isUserProviderManagementPath(method, path string) bool {
	if !strings.HasPrefix(path, "/__providers/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/__providers/"), "/")
	if strings.TrimSpace(parts[0]) == "" {
		return false
	}
	if len(parts) == 1 {
		return method == http.MethodPatch || method == http.MethodDelete
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "test", "chat-test", "cache-test", "thinking-test", "self-register-token":
			return method == http.MethodPost
		case "auth-preview":
			return method == http.MethodGet
		default:
			return false
		}
	}
	if len(parts) == 3 {
		switch parts[1] {
		case "claude-oauth", "cursor-oauth", "chatgpt-oauth":
			switch parts[2] {
			case "start", "complete", "disconnect":
				return method == http.MethodPost
			case "status":
				return method == http.MethodGet
			}
			return false
		case "self-register-token":
			// self-register-token/revoke: console-session-authenticated
			// owner/admin action (unlike PATCH .../self-register, which is
			// the separate bearer-token machine endpoint handled above the
			// session middleware entirely).
			return parts[2] == "revoke" && method == http.MethodPost
		}
		return false
	}
	return false
}

// isUserProviderUsagePath matches GET /__providers/{id}/(claude|cursor|chatgpt)-oauth/usage.
func isUserProviderUsagePath(path string) bool {
	if !strings.HasPrefix(path, "/__providers/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/__providers/"), "/")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return false
	}
	switch parts[1] {
	case "claude-oauth", "cursor-oauth", "chatgpt-oauth":
		return parts[2] == "usage"
	default:
		return false
	}
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
	case "/login", "/favicon.ico", "/favicon.svg", "/apple-touch-icon.png":
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
	identity, ok := s.sessionIdentity(r)
	return ok && identity.isAdmin()
}

// sessionIdentity verifies the session cookie and resolves the logged-in user.
// Legacy tokens (minted before multi-user) carry no user info and map to admin.
func (s *Server) sessionIdentity(r *http.Request) (sessionIdentity, bool) {
	if s.adminAuth == nil {
		return sessionIdentity{}, false
	}
	secret := strings.TrimSpace(s.adminAuth.Setting(settingAdminSession))
	if secret == "" {
		return sessionIdentity{}, false
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil || cookie == nil || strings.TrimSpace(cookie.Value) == "" {
		return sessionIdentity{}, false
	}
	userID, ok := verifySessionToken(cookie.Value, secret)
	if !ok {
		return sessionIdentity{}, false
	}
	if userID == "" || userID == legacyAdminUserID {
		return adminIdentity(), true
	}
	if s.userStore == nil {
		return sessionIdentity{}, false
	}
	user, err := s.userStore.UserByID(userID)
	if err != nil || !user.Enabled {
		return sessionIdentity{}, false
	}
	return sessionIdentity{UserID: user.ID, Username: user.Username, Role: user.Role}, true
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	localBypass := isLocalAdminBypass(r)
	configured := s.adminAuthConfigured()
	status := adminAuthStatus{
		Configured:  configured,
		RequireAuth: !localBypass,
		LocalBypass: localBypass,
	}
	if localBypass {
		status.Authenticated = true
		status.Role = string(domain.UserRoleAdmin)
		status.Username = "admin"
		status.UserID = legacyAdminUserID
	} else if identity, ok := s.sessionIdentity(r); ok {
		status.Authenticated = true
		status.Role = string(identity.Role)
		status.Username = identity.Username
		status.UserID = identity.UserID
	}
	writeJSON(w, http.StatusOK, status)
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	username := strings.TrimSpace(payload.Username)
	// Empty username or "admin" logs in as the administrator (legacy password).
	if username == "" || strings.EqualFold(username, "admin") {
		hash := s.adminAuth.Setting(settingAdminPassword)
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(payload.Password)); err != nil {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		s.issueSession(w, r, legacyAdminUserID)
		s.logs.AddApp("info", "admin login success", requestHostname(r.Host))
		localBypass := isLocalAdminBypass(r)
		writeJSON(w, http.StatusOK, adminAuthStatus{
			Configured:    true,
			Authenticated: true,
			RequireAuth:   !localBypass,
			LocalBypass:   localBypass,
			Role:          string(domain.UserRoleAdmin),
			Username:      "admin",
			UserID:        legacyAdminUserID,
		})
		return
	}
	// Normal-user login.
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	user, err := s.userStore.UserByUsername(username)
	if err != nil || !user.Enabled {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(payload.Password)); err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.issueSession(w, r, user.ID)
	_ = s.userStore.TouchUserLogin(user.ID, nowRFC3339())
	s.logs.AddApp("info", "user login success", user.Username)
	localBypass := isLocalAdminBypass(r)
	writeJSON(w, http.StatusOK, adminAuthStatus{
		Configured:    true,
		Authenticated: true,
		RequireAuth:   !localBypass,
		LocalBypass:   localBypass,
		Role:          string(user.Role),
		Username:      user.Username,
		UserID:        user.ID,
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
	identity, hasIdentity := s.sessionIdentity(r)
	if !localBypass && !hasIdentity {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication required")
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

	// Normal user: change own password (must prove current password).
	if hasIdentity && !identity.isAdmin() {
		if s.userStore == nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
			return
		}
		user, err := s.userStore.UserByID(identity.UserID)
		if err != nil {
			writeOpenAIError(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(payload.CurrentPassword)); err != nil {
			writeOpenAIError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		user.PasswordHash = string(hash)
		if err := s.userStore.UpdateUser(user); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.logs.AddApp("info", "user password updated", user.Username)
		writeJSON(w, http.StatusOK, adminAuthStatus{
			Configured:    true,
			Authenticated: true,
			RequireAuth:   !localBypass,
			LocalBypass:   localBypass,
			Role:          string(user.Role),
			Username:      user.Username,
			UserID:        user.ID,
		})
		return
	}

	// Admin path (or local bypass recovery).
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
		Role:          string(domain.UserRoleAdmin),
		Username:      "admin",
		UserID:        legacyAdminUserID,
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
	s.issueSession(w, r, legacyAdminUserID)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID string) {
	secret := ""
	if s.adminAuth != nil {
		secret = strings.TrimSpace(s.adminAuth.Setting(settingAdminSession))
	}
	if secret == "" {
		return
	}
	token, err := mintSessionToken(secret, userID, time.Now().Add(adminSessionTTL))
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

// Session token format: v2.<base64url(userID)>.<expiryUnix>.<nonce>.<hmac>
// Legacy (pre multi-user) format: <expiryUnix>.<nonce>.<hmac> maps to admin.
func mintSessionToken(secret, userID string, expiry time.Time) (string, error) {
	nonce, err := randomToken(16)
	if err != nil {
		return "", err
	}
	encodedUser := base64.RawURLEncoding.EncodeToString([]byte(userID))
	payload := "v2." + encodedUser + "." + strconv.FormatInt(expiry.Unix(), 10) + "." + nonce
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

// verifySessionToken returns the embedded userID ("" for legacy admin tokens)
// and whether the token is valid.
func verifySessionToken(token, secret string) (string, bool) {
	parts := strings.Split(token, ".")
	switch len(parts) {
	case 5:
		if parts[0] != "v2" {
			return "", false
		}
		userBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", false
		}
		expiryUnix, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || time.Now().Unix() > expiryUnix {
			return "", false
		}
		payload := strings.Join(parts[:4], ".")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(payload))
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(parts[4])) {
			return "", false
		}
		return string(userBytes), true
	case 3:
		// Legacy admin session token.
		expiryUnix, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || time.Now().Unix() > expiryUnix {
			return "", false
		}
		payload := parts[0] + "." + parts[1]
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(payload))
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(parts[2])) {
			return "", false
		}
		return "", true
	default:
		return "", false
	}
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
