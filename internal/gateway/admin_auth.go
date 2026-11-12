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
	// Public-access control token (see public_access_control.go): a
	// machine-facing bearer secret whose only capability is flipping public
	// access on/off, stored like the admin secrets above (settings table,
	// hash only — never the raw token).
	settingPublicAccessControlTokenHash    = "publicAccessControlTokenHash"
	settingPublicAccessControlTokenPreview = "publicAccessControlTokenPreview"
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
	// UserByGatewayID 按网关稳定身份找 peer 账号，用于下游注册的幂等判定。
	UserByGatewayID(gatewayID string) (domain.User, error)
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

// principalContextKey carries the resolved RBAC principal alongside the raw
// identity, so handlers never re-resolve the role on the hot path.
type principalContextKey struct{}

// identityFromRequest returns the authenticated identity stored by withAdminAuth.
func identityFromRequest(r *http.Request) (sessionIdentity, bool) {
	value, ok := r.Context().Value(authContextKey{}).(sessionIdentity)
	return value, ok
}

// requestPrincipal returns the RBAC principal of the request, defaulting to
// the implicit administrator for requests that carry no identity (loopback
// bypass routes registered before multi-user).
func (s *Server) requestPrincipal(r *http.Request) principal {
	if value, ok := r.Context().Value(principalContextKey{}).(principal); ok {
		return value
	}
	if identity, ok := identityFromRequest(r); ok {
		return s.principalFor(identity)
	}
	return s.principalFor(adminIdentity())
}

func withPrincipal(r *http.Request, identity sessionIdentity, p principal) *http.Request {
	ctx := context.WithValue(r.Context(), authContextKey{}, identity)
	ctx = context.WithValue(ctx, principalContextKey{}, p)
	return r.WithContext(ctx)
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
		// Public-access on/off toggle: same bearer-token-only trust model, so a
		// script / LLM agent with the dedicated control token can flip public
		// access without a console session — see
		// authenticatePublicAccessControlRequest.
		if isPublicAccessControlPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 对端网关入口：只认对端凭据，与控制台的会话/cookie 鉴权完全无关。
		// 两条鉴权路径必须彻底分开——混在一起的话，任何一个控制台接口的疏漏
		// 都会变成对端入口的疏漏，而那个入口能把请求转发到真实的 provider。
		if strings.HasPrefix(r.URL.Path, peerEndpointPrefix) {
			next.ServeHTTP(w, r)
			return
		}

		localBypass := isLocalAdminBypass(r)
		if localBypass {
			// Local desktop app acts as the administrator.
			identity := adminIdentity()
			next.ServeHTTP(w, withPrincipal(r, identity, s.localAdminPrincipal()))
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
		// RBAC: every console route resolves to one permission; a principal
		// without it is refused here. Resource scope (own / granted / all) is
		// enforced inside each handler by the require*ForUser helpers.
		current := s.principalFor(identity)
		if required := routePermission(r.Method, r.URL.Path); required != nil && !current.has(*required) {
			deniedPermission(w, *required)
			return
		}
		// ScopeOwn 角色（内置普通用户/只读/受限用户及同类自定义角色）除 permission
		// 外还必须落在 isUserAllowedPath 白名单里；否则 providers.create 等权限
		// 会误开 /__providers/export 这类管理端点（全站 OAuth 备份泄露）。
		if current.scopeOf() != domain.ScopeAll {
			path := r.URL.Path
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			if strings.HasPrefix(path, "/__") && !isUserAllowedPath(r.Method, path) {
				writeOpenAIError(w, http.StatusForbidden, "administrator access required")
				return
			}
		}
		// 记录该用户浏览器最近一次控制台请求时间（内存精确，周期性节流落库）。
		s.noteUserActivity(identity.UserID)
		next.ServeHTTP(w, withPrincipal(r, identity, current))
	})
}

// isUserAllowedPath whitelists console routes reachable by role=user.
// Data-level isolation (own keys only) is enforced inside each handler.
func isUserAllowedPath(method, path string) bool {
	switch path {
	case "/__state", "/__logs", "/__request-stats", "/__usage-analysis", "/__apikeys", "/__auth/password", "/__ui-log":
		return true
	case "/__profile", "/__profile/avatar":
		return method == http.MethodGet || method == http.MethodPatch
	case "/__roles/catalog", "/__roles/grant-targets", "/__roles/options":
		return method == http.MethodGet
	}
	if strings.HasPrefix(path, "/__apikeys/") {
		return true
	}
	// Users may create providers (they become the owner) and create routes for
	// binding their keys; per-provider route/provider checks live in handlers.
	if method == http.MethodPost && (path == "/__providers" || path == "/__routes") {
		return true
	}
	// A user's own routes: edit / delete / test, with the same per-route
	// ownership check the other provider-scoped handlers carry
	// (requireRouteAccessForUser). Routes of providers they were not granted
	// answer 403 inside the handler.
	if isUserRoutePath(method, path) {
		return true
	}
	// Owner-only provider management (enforced via requireProviderOwnerForUser
	// inside each handler): edit / delete / fetch-models / chat-test / preview.
	if isUserProviderManagementPath(method, path) {
		return true
	}
	// 额度保活：用户可以维护自己的定时任务（数据级隔离在 quota_keeper_api.go 里
	// 按 OwnerUserID 判定，别人的任务一律 404）。
	if isUserQuotaPath(method, path) {
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

// isUserRoutePath matches the routes a normal user may act on: PATCH and DELETE
// /__routes/{id} and POST /__routes/{id}/test. Every one of them is checked
// against the caller's grants inside the handler (requireRouteAccessForUser), so
// the middleware only decides that the request is a route action at all.
func isUserRoutePath(method, path string) bool {
	if !strings.HasPrefix(path, "/__routes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/__routes/"), "/")
	if strings.TrimSpace(parts[0]) == "" {
		return false
	}
	switch len(parts) {
	case 1:
		return method == http.MethodPatch || method == http.MethodDelete
	case 2:
		return parts[1] == "test" && method == http.MethodPost
	}
	return false
}

// isUserQuotaPath matches the 额度保活 endpoints a normal user may call. Every
// one of them is safe to reach because the handlers themselves scope the data:
// a task owned by somebody else answers 404, and a provider that was never
// granted to the caller is refused on create/update.
func isUserQuotaPath(method, path string) bool {
	if path == "/__quota-tasks" {
		return method == http.MethodGet || method == http.MethodPost
	}
	if !strings.HasPrefix(path, "/__quota-tasks/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/__quota-tasks/"), "/")
	if strings.TrimSpace(parts[0]) == "" {
		return false
	}
	if parts[0] == "preview" {
		return len(parts) == 1 && method == http.MethodPost
	}
	switch len(parts) {
	case 1:
		return method == http.MethodPatch || method == http.MethodDelete
	case 2:
		if parts[1] == "run" {
			return method == http.MethodPost
		}
		return parts[1] == "runs" && method == http.MethodGet
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
		case "test", "chat-test", "cache-test", "thinking-test", "conformance", "self-register-token", "checkin", "detect-protocols":
			return method == http.MethodPost
		case "auth-preview":
			return method == http.MethodGet
		default:
			return false
		}
	}
	if len(parts) == 3 {
		switch parts[1] {
		case "checkin":
			// checkin/auto is the daily switch on a subscription's card; the
			// owner of the card owns it, as they own the RTK switch.
			return parts[2] == "auto" && method == http.MethodPost
		case "claude-oauth", "cursor-oauth", "chatgpt-oauth", "qoder-pat", "qoder-oauth",
			"zcode-oauth", "trae-oauth", "workbuddy-oauth":
			// A Qoder connection has two doors: qoder-oauth is the browser
			// device flow (start/status), qoder-pat the pasted token
			// (complete/status). Both share disconnect, and reconnect is the
			// PAT family's — the stored token is re-minted rather than re-pasted.
			// Ownership itself is checked inside each handler.
			switch parts[2] {
			case "start", "complete", "disconnect":
				return method == http.MethodPost
			case "status":
				return method == http.MethodGet
			case "reconnect":
				return method == http.MethodPost && parts[1] == "qoder-pat"
			case "pause", "resume":
				// The subscription panels pause and resume with their own verbs,
				// on the same handlers as disconnect and reconnect.
				return method == http.MethodPost && subscriptionPausableFamily(parts[1])
			case "tasks":
				// The WorkBuddy task center's list (任务中心): the owner of the
				// card reads their own account's tasks. Without this a ScopeOwn
				// user's own card answered "administrator access required".
				return method == http.MethodGet && parts[1] == "workbuddy-oauth"
			case "activity", "keepalive", "automation":
				// The card's daily actions (活跃上报 / 保活) and the two switches
				// behind them. All three check provider access inside the handler.
				return method == http.MethodPost && parts[1] == "workbuddy-oauth"
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
	// The WorkBuddy task center's run endpoints: 一键完成 starts a run, and the
	// run is polled by id. Both answer only for a provider the caller can access,
	// which the handler checks with requireProviderAccessForUser.
	if len(parts) == 4 && parts[1] == "workbuddy-oauth" && parts[2] == "tasks" {
		switch parts[3] {
		case "auto":
			return method == http.MethodPost
		case "run":
			return method == http.MethodGet
		}
		return false
	}
	return false
}

// subscriptionPausableFamily reports whether an OAuth family has the pause and
// resume verbs the shared subscription panel offers.
func subscriptionPausableFamily(family string) bool {
	switch family {
	case "qoder-oauth", "zcode-oauth", "trae-oauth":
		return true
	}
	return false
}

// isUserProviderUsagePath matches GET /__providers/{id}/(claude|cursor|chatgpt)-oauth/usage
// and the API-key usage readers (zhipu / deepseek / opencode-go).
func isUserProviderUsagePath(path string) bool {
	if !strings.HasPrefix(path, "/__providers/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/__providers/"), "/")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return false
	}
	switch parts[1] {
	case "claude-oauth", "cursor-oauth", "chatgpt-oauth", "workbuddy-oauth", "qoder-oauth", "zcode-oauth", "trae-oauth", "zhipu", "deepseek", "opencode-go":
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
	// The running build's version is public like /__health: a tab still on the
	// login screen must also be able to learn that a release is live.
	case "/__health", "/__app/version", "/__auth/status", "/__auth/setup", "/__auth/login", "/__auth/logout":
		return true
	case "/login", "/favicon.ico", "/favicon.svg", "/apple-touch-icon.png",
		// 未登录也要能取到 manifest/sw，否则浏览器判定应用不可安装。
		"/manifest.webmanifest", "/sw.js", "/registerSW.js":
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
	// 反代后面的网关永远看到 127.0.0.1 的对端：只看地址就会把公网域名访问当成
	// 本机操作员，等于整站免登录。带域名时必须是回环名；空 Host 留给桌面端。
	host := requestHostname(r.Host)
	if host != "" && !isLoopbackHost(host) {
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

// consoleInstanceInUse reports whether this gateway has already been set up —
// console users exist, or any provider/route/model/key is configured. Used to
// keep the one-time /__auth/setup on a fresh install only.
func (s *Server) consoleInstanceInUse() bool {
	if s.userStore != nil {
		if users, err := s.userStore.ListUsers(); err == nil && len(users) > 0 {
			return true
		}
	}
	if s.router == nil {
		return false
	}
	state := s.router.State()
	return len(state.Providers) > 0 || len(state.Routes) > 0 || len(state.Models) > 0 || len(state.APIKeys) > 0
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
	// IsPeer 与登录口一致：即便某个对端账号将来真的拿到了会话 cookie
	// （比如升级前误发的），它也不能恢复成一个控制台身份。
	if err != nil || !user.Enabled || user.IsPeer() {
		return sessionIdentity{}, false
	}
	return sessionIdentity{UserID: user.ID, Username: user.Username, Role: user.Role}, true
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.authStatusFor(r))
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
	// 只有全新实例才接受公网首设：没有账号、没有配置。已经在用的实例（口令
	// 记录丢了、或还是旧版本建的）不能把管理员身份交给第一个连上来的人 ——
	// 那等于把整个控制台（含所有 Provider 凭据）送出去。改用本机桌面端补设。
	if !isLocalAdminBypass(r) && s.consoleInstanceInUse() {
		writeOpenAIError(w, http.StatusForbidden, "this instance is already in use; set the admin password from the local desktop app")
		return
	}
	// 首次设置也一样按来源限流，防止公网暴露的未初始化实例被反复探测。
	if s.rejectIfAuthThrottled(w, r, "admin") {
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
	// 口令可被离线穷举前先挡一层：同一来源反复试同一账号，超过阈值直接 429。
	// 必须在解析正文之前判断 —— 被挡下的来源不该再消耗一次 JSON 解析。
	if s.rejectIfAuthThrottledPeek(w, r) {
		return
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
		// TrustDevice is the login page's "信任此安全设备（30 天内免登录）".
		// False (or absent) means the session lasts the browser session only.
		TrustDevice bool `json:"trustDevice"`
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
			s.recordAuthFailure(r, username)
			writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		s.recordAuthSuccess(r, username)
		s.issueSessionFor(w, r, legacyAdminUserID, payload.TrustDevice)
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
	// Console user login (role=user or role=admin, resolved from the users table).
	if s.userStore == nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	user, err := s.userStore.UserByUsername(username)
	// peer 账号（下游网关）永远不能登录控制台：它的密码只用于「重新注册时
	// 证明自己是同一个下游」，不是一个身份入口。允许它登录等于把一个只该
	// 按授权集取流量的账号，变成能看日志、能改自己密钥的会话用户。
	if err != nil || !user.Enabled || user.IsPeer() {
		s.recordAuthFailure(r, username)
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(payload.Password)); err != nil {
		s.recordAuthFailure(r, username)
		writeOpenAIError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.recordAuthSuccess(r, username)
	s.issueSessionFor(w, r, user.ID, payload.TrustDevice)
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

	// Database-backed user (whatever their role): change own password, must prove
	// current one. Keyed on UserID, not isAdmin(): a user promoted to admin must
	// not land in the branch below, which rewrites the shared legacy admin
	// password instead of their own hash.
	if hasIdentity && identity.UserID != legacyAdminUserID {
		if s.userStore == nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "user store is not configured")
			return
		}
		user, err := s.userStore.UserByID(identity.UserID)
		if err != nil {
			writeOpenAIError(w, http.StatusUnauthorized, "user not found")
			return
		}
		if s.rejectIfAuthThrottled(w, r, user.Username) {
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(payload.CurrentPassword)); err != nil {
			s.recordAuthFailure(r, user.Username)
			writeOpenAIError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		s.recordAuthSuccess(r, user.Username)
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
	s.issueSessionFor(w, r, userID, false)
}

// issueSessionFor mints the console session. trustDevice is the login page's
// "信任此安全设备（30 天内免登录）": with it the cookie outlives the browser
// (Max-Age) for adminSessionTTL; without it the cookie carries no Max-Age or
// Expires, so it lives as long as the browser session and a shared computer is
// signed out when the window closes. The server-side token expiry is the same
// either way — trusting a device decides how long the browser keeps it, not
// whether the server honours it.
func (s *Server) issueSessionFor(w http.ResponseWriter, r *http.Request, userID string, trustDevice bool) {
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
	cookie := &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	}
	if trustDevice {
		cookie.Expires = time.Now().Add(adminSessionTTL)
		cookie.MaxAge = int(adminSessionTTL.Seconds())
	}
	http.SetCookie(w, cookie)
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
