package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// RoleStore persists custom roles and per-user resource grants. It is optional:
// a Server built without one still serves the built-in roles from code.
type RoleStore interface {
	ListRoles() ([]domain.Role, error)
	RoleByID(id string) (domain.Role, error)
	SaveRole(role domain.Role) error
	DeleteRole(id string) error
	ListResourceGrants() (map[string][]string, error)
	SetResourceGrants(userID string, resourceIDs []string) error
}

// principal is the fully resolved authorization view of one console session:
// the user, its role, the permissions the role grants and the resource scope.
type principal struct {
	UserID   string
	Username string
	Role     domain.UserRole
	RoleID   string
	IsAdmin  bool
	Grants   []string
	// perms is nil for admins, who hold domain.PermAll.
	perms map[domain.Permission]bool
	scope domain.ResourceScope
	// localAdmin marks the implicit loopback/legacy administrator, which owns
	// no users-table row. Owner labels fall back to a stable name for it.
	localAdmin bool
}

func (p principal) has(perm domain.Permission) bool {
	if p.IsAdmin {
		return true
	}
	return p.perms[perm]
}

func (p principal) canAny(perms ...domain.Permission) bool {
	for _, perm := range perms {
		if p.has(perm) {
			return true
		}
	}
	return false
}

func (p principal) scopeOf() domain.ResourceScope {
	if p.IsAdmin || p.scope == "" {
		return domain.ScopeAll
	}
	return p.scope
}

// principalFor builds the authorization view for an identity. Unknown custom
// roles fail closed to the least-privileged built-in role.
func (s *Server) principalFor(identity sessionIdentity) principal {
	p := principal{UserID: identity.UserID, Username: identity.Username, Role: identity.Role}
	role := s.effectiveRole(identity)
	p.RoleID = role.ID
	// A legacy "admin" flag always wins over a narrower stored role: an account
	// that was promoted through the old role column can never be locked out of
	// the console by a stale role_id.
	p.IsAdmin = role.Has(domain.PermAll) || identity.Role == domain.UserRoleAdmin
	if p.IsAdmin {
		p.scope = domain.ScopeAll
		return p
	}
	p.scope = role.EffectiveScope()
	p.perms = make(map[domain.Permission]bool, len(role.Permissions))
	for _, perm := range role.Permissions {
		p.perms[perm] = true
	}
	// Grants are additive to whatever the role already allows, so every
	// non-admin principal needs them regardless of scope. They live in
	// user_resource_grants (the store never fills AllowedResourceIDs).
	p.Grants = s.resourceGrants()[identity.UserID]
	if s.userStore != nil {
		if user, err := s.userStore.UserByID(identity.UserID); err == nil {
			p.Grants = append(append([]string{}, p.Grants...), user.AllowedResourceIDs...)
		}
	}
	return p
}

// effectiveRole resolves the Role for an identity, preferring the user's
// RoleID, then the legacy Role, then the built-in defaults.
func (s *Server) effectiveRole(identity sessionIdentity) domain.Role {
	if identity.UserID == legacyAdminUserID {
		role, _ := domain.BuiltinRole(domain.RoleIDAdmin)
		return role
	}
	if s.userStore == nil {
		if identity.Role == domain.UserRoleAdmin {
			role, _ := domain.BuiltinRole(domain.RoleIDAdmin)
			return role
		}
		role, _ := domain.BuiltinRole(domain.RoleIDUser)
		return role
	}
	user, err := s.userStore.UserByID(identity.UserID)
	if err != nil {
		if identity.Role == domain.UserRoleAdmin {
			role, _ := domain.BuiltinRole(domain.RoleIDAdmin)
			return role
		}
		role, _ := domain.BuiltinRole(domain.RoleIDUser)
		return role
	}
	if user.Role == domain.UserRoleAdmin {
		role, _ := domain.BuiltinRole(domain.RoleIDAdmin)
		return role
	}
	return s.roleByID(user.RoleID)
}

// roleByID looks a role up: built-ins from code, custom ones from the role
// store. Anything unresolvable fails closed to "user". Legacy values such as
// "ADMIN" arrive as the optional second element and are matched
// case-insensitively before giving up.
func (s *Server) roleByID(roleID string, legacy ...string) domain.Role {
	roleID = strings.TrimSpace(roleID)
	if role, ok := domain.BuiltinRole(roleID); ok {
		return role
	}
	for _, candidate := range legacy {
		if lower := strings.ToLower(strings.TrimSpace(candidate)); lower != "" && lower != roleID {
			if role, ok := domain.BuiltinRole(lower); ok {
				return role
			}
		}
	}
	if roleID != "" {
		if roles, ok := s.roleStoreRef(); ok {
			if role, err := roles.RoleByID(roleID); err == nil && role.ID != "" {
				return role
			}
		}
	}
	role, _ := domain.BuiltinRole(domain.RoleIDUser)
	return role
}

func (s *Server) roleStoreRef() (RoleStore, bool) {
	if s.roles == nil {
		return nil, false
	}
	return s.roles, true
}

// listRoles returns every role the console can assign: built-ins first, then
// custom roles ordered by creation time.
func (s *Server) listRoles() ([]domain.Role, error) {
	roles := domain.BuiltinRoles()
	if store, ok := s.roleStoreRef(); ok {
		custom, err := store.ListRoles()
		if err != nil {
			return roles, err
		}
		for _, role := range custom {
			role.Builtin = false
			role.Scope = role.EffectiveScope()
			roles = append(roles, role)
		}
	}
	return roles, nil
}

// resourceGrants returns the per-user provider grant table.
func (s *Server) resourceGrants() map[string][]string {
	if store, ok := s.roleStoreRef(); ok {
		if grants, err := store.ListResourceGrants(); err == nil {
			return grants
		}
	}
	return map[string][]string{}
}

// routePermission maps a console API request to the permission that guards it.
// It is the single authorization table: the middleware only asks "does the
// principal hold routePermission(method, path)?" and each handler additionally
// enforces resource scope via the require* helpers. A nil permission means the
// route is session-authenticated but not permission-checked (public assets,
// the caller's own password/profile, self-only endpoints).
func routePermission(method, path string) *domain.Permission {
	perm := func(p domain.Permission) *domain.Permission { return &p }
	read := method == http.MethodGet

	if !strings.HasPrefix(path, "/__") {
		// Static console assets and OAuth callbacks.
		return nil
	}

	switch path {
	case "/__state":
		// The console's dashboard snapshot; handlers narrow rows by scope.
		if read {
			return perm(domain.PermDashboardView)
		}
	case "/__logs", "/__request-stats":
		if read {
			return perm(domain.PermLogsView)
		}
	case "/__usage-analysis", "/__rtk/stats":
		if read {
			return perm(domain.PermUsageView)
		}
	case "/__auth/password", "/__auth/logout", "/__profile/avatar", "/__profile", "/__roles/catalog", "/__roles/grant-targets", "/__roles/options":
		return nil
	case "/__ui-log":
		return nil
	case "/__app/logs":
		return perm(domain.PermAppLogsView)
	case "/__host-metrics", "/__host-metrics/snapshot", "/__host-metrics/stream", "/__host-diagnostics", "/__settings/paths":
		return perm(domain.PermMachineView)
	case "/__users":
		if read {
			return perm(domain.PermUsersView)
		}
		return perm(domain.PermUsersManage)
	case "/__roles":
		if read {
			return perm(domain.PermUsersView)
		}
		return perm(domain.PermRolesManage)
	case "/__apikeys":
		if read {
			return perm(domain.PermKeysView)
		}
		return perm(domain.PermKeysCreate)
	case "/__peers", "/__peer-upstreams":
		// 网关互联：列出下游/上游是浏览，注册与吊销是改动。
		if read {
			return perm(domain.PermPeersView)
		}
		return perm(domain.PermPeersManage)
	case "/__settings/peer-registration-cidrs":
		return perm(domain.PermPeersManage)
	case "/__providers":
		if method == http.MethodPost {
			return perm(domain.PermProvidersCreate)
		}
		if read {
			return perm(domain.PermProvidersView)
		}
	case "/__providers/import", "/__providers/export":
		return perm(domain.PermProvidersCreate)
	case "/__routes":
		return perm(domain.PermRoutesWrite)
	case "/__quota-tasks":
		// Reading the list is the page itself; creating needs manage. Before
		// this split a read-only role could not even open 额度保活.
		if read {
			return perm(domain.PermQuotaKeeperView)
		}
		return perm(domain.PermQuotaKeeperManage)
	case "/__quota-tasks/preview":
		// Preview fires a real upstream probe against the draft, so it stays a
		// manage action even though nothing is persisted.
		return perm(domain.PermQuotaKeeperManage)
	case "/__prices":
		return perm(domain.PermPricing)
	case "/__monitor/stream":
		return perm(domain.PermMonitorView)
	case "/__public", "/__public/control-token":
		return perm(domain.PermPublicAccess)
	}

	switch {
	case strings.HasPrefix(path, "/__roles/"):
		if method == http.MethodDelete || method == http.MethodPatch || method == http.MethodPut || method == http.MethodPost {
			return perm(domain.PermRolesManage)
		}
		return perm(domain.PermUsersView)
	case strings.HasPrefix(path, "/__users/"):
		return perm(domain.PermUsersManage)
	case strings.HasPrefix(path, "/__peers/"):
		return perm(domain.PermPeersManage)
	case strings.HasPrefix(path, "/__peer-upstreams/"):
		return perm(domain.PermPeersManage)
	case strings.HasPrefix(path, "/__apikeys/"):
		if read {
			return perm(domain.PermKeysView)
		}
		// Quota is written by the same PATCH as ordinary fields; the handler
		// additionally requires keys.quota when the payload carries quota.
		return perm(domain.PermKeysEdit)
	case strings.HasPrefix(path, "/__routes/"):
		return perm(domain.PermRoutesWrite)
	case strings.HasPrefix(path, "/__providers/"):
		return providerRoutePermission(method, path)
	case strings.HasPrefix(path, "/__quota-tasks/"):
		if read {
			return perm(domain.PermQuotaKeeperView)
		}
		return perm(domain.PermQuotaKeeperManage)
	case strings.HasPrefix(path, "/__monitor/"):
		if read {
			return perm(domain.PermMonitorView)
		}
		return perm(domain.PermMonitorManage)
	case strings.HasPrefix(path, "/__prices/"):
		return perm(domain.PermPricing)
	case strings.HasPrefix(path, "/__public/"):
		return perm(domain.PermPublicAccess)
	case strings.HasPrefix(path, "/__settings/"):
		if read {
			return perm(domain.PermSettingsView)
		}
		return perm(domain.PermSettingsEdit)
	case strings.HasPrefix(path, "/__endpoints/"):
		return perm(domain.PermEndpointsEdit)
	case strings.HasPrefix(path, "/__app/"):
		return perm(domain.PermSettingsEdit)
	}
	// Self-registration bearer endpoints are served above the session
	// middleware; anything else under /__ is administrator-only by default so a
	// new route cannot accidentally ship open.
	return perm(domain.PermAll)
}

// providerRoutePermission maps /__providers/{id}/... sub-routes, ordered from
// the most specific suffix to the generic "edit the provider" fallback.
func providerRoutePermission(method, path string) *domain.Permission {
	perm := func(p domain.Permission) *domain.Permission { return &p }
	rest := strings.TrimPrefix(path, "/__providers/")
	if rest == "" {
		return perm(domain.PermProvidersEdit)
	}
	// Usage panels are read-only and follow the provider's visibility.
	if strings.HasSuffix(rest, "/usage") {
		return perm(domain.PermProvidersView)
	}
	suffixes := []struct {
		suffix string
		perm   domain.Permission
	}{
		{"/self-register-token/revoke", domain.PermProvidersSecret},
		{"/self-register-token", domain.PermProvidersSecret},
		{"/enabled", domain.PermProvidersToggle},
		{"/rtk", domain.PermProvidersToggle},
		{"/first-packet-timeout", domain.PermProvidersToggle},
		{"/concurrency", domain.PermProvidersToggle},
		{"/restore", domain.PermProvidersDelete},
		{"/purge", domain.PermProvidersDelete},
		{"/auth-preview", domain.PermProvidersSecret},
		{"/chat-test", domain.PermProvidersTest},
		{"/detect-protocols", domain.PermProvidersTest},
		{"/cache-test", domain.PermProvidersTest},
		{"/thinking-test", domain.PermProvidersTest},
		{"/conformance", domain.PermProvidersTest},
		{"/checkin/auto", domain.PermProvidersEdit},
		{"/checkin", domain.PermProvidersTest},
		// The WorkBuddy task center. Before these entries the whole family fell
		// through to the "-oauth/" catch-all below and mapped to
		// providers.secret, so running a daily task demanded the credential
		// permission — a role that may run tasks but not read keys was refused.
		// The ladder matches the rest of the card: reading the list is view,
		// running a task is test (like 签到), turning the daily switch on is edit
		// (like checkin/auto).
		{"/workbuddy-oauth/tasks/auto", domain.PermProvidersTest},
		{"/workbuddy-oauth/tasks/run", domain.PermProvidersTest},
		{"/workbuddy-oauth/tasks", domain.PermProvidersView},
		{"/workbuddy-oauth/activity", domain.PermProvidersTest},
		{"/workbuddy-oauth/keepalive", domain.PermProvidersTest},
		{"/workbuddy-oauth/automation", domain.PermProvidersEdit},
	}
	for _, item := range suffixes {
		if strings.HasSuffix(rest, item.suffix) {
			return perm(item.perm)
		}
	}
	// oauth start/status/complete/disconnect/reconnect/pause/resume all touch
	// the provider's credentials.
	for _, family := range []string{"-oauth/", "-pat/"} {
		if strings.Contains(rest, family) {
			return perm(domain.PermProvidersSecret)
		}
	}
	switch method {
	case http.MethodGet:
		return perm(domain.PermProvidersView)
	case http.MethodDelete:
		return perm(domain.PermProvidersDelete)
	}
	if strings.HasSuffix(rest, "/test") {
		return perm(domain.PermProvidersTest)
	}
	return perm(domain.PermProvidersEdit)
}

// deniedPermission answers 403 with the permission name, so a normal user (and
// the console's error toast) can tell what the role is missing.
func deniedPermission(w http.ResponseWriter, permission domain.Permission) {
	writeOpenAIError(w, http.StatusForbidden, "permission denied: missing "+string(permission))
}

// requirePermission is the handler-level guard for individual actions inside a
// route whose permission is coarser (e.g. quota vs. plain key edit).
func (s *Server) requirePermission(w http.ResponseWriter, r *http.Request, permission domain.Permission) bool {
	if s.requestPrincipal(r).has(permission) {
		return true
	}
	deniedPermission(w, permission)
	return false
}

// errRoleNotFound lets handlers distinguish a missing role from a store error.
var errRoleNotFound = errors.New("role not found")

// decodeJSON decodes a request body into out, answering 400 on failure.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}
