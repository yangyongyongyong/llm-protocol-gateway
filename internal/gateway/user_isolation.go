package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// requestIdentity returns the resolved identity, defaulting to admin for
// requests that passed the middleware without an explicit identity (e.g.
// local bypass paths registered before multi-user).
func (s *Server) requestIdentity(r *http.Request) sessionIdentity {
	if identity, ok := identityFromRequest(r); ok {
		return identity
	}
	return adminIdentity()
}

// allowedProviderIDsForUser returns the providers a non-admin user may access:
// the ones it created plus the ones an admin assigned on the users page. Roles
// with the "all" scope see every enabled provider. Grants are additive, so
// assigning a shared provider never takes away what the user already had.
//
// Providers disabled by the admin are excluded entirely — normal users cannot
// see, bind, or use them until re-enabled.
func (s *Server) allowedProviderIDsForUser(userID string) map[string]bool {
	return s.allowedProviderIDsForPrincipal(s.principalForUserID(userID))
}

// principalForUserID resolves a stored user's principal (used by request-time
// checks that only hold the key owner's id).
func (s *Server) principalForUserID(userID string) principal {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return principal{IsAdmin: true, RoleID: domain.RoleIDAdmin, scope: domain.ScopeAll, localAdmin: true}
	}
	if userID == legacyAdminUserID {
		return s.localAdminPrincipal()
	}
	identity := sessionIdentity{UserID: userID}
	if s.userStore != nil {
		if user, err := s.userStore.UserByID(userID); err == nil {
			identity.Username = user.Username
			identity.Role = user.Role
		}
	}
	return s.principalFor(identity)
}

// localAdminPrincipal is the implicit administrator (loopback bypass, legacy
// sessions). It holds every permission and sees every resource.
func (s *Server) localAdminPrincipal() principal {
	return principal{
		UserID: legacyAdminUserID, Username: s.adminDisplayName(), Role: domain.UserRoleAdmin,
		RoleID: domain.RoleIDAdmin, IsAdmin: true, scope: domain.ScopeAll, localAdmin: true,
	}
}

func (s *Server) allowedProviderIDsForPrincipal(p principal) map[string]bool {
	out := map[string]bool{}
	if p.UserID == "" {
		return out
	}
	scope := p.scopeOf()
	if scope == domain.ScopeAll {
		for _, provider := range s.router.State().Providers {
			if provider.Disabled || provider.Deleted {
				continue
			}
			out[provider.ID] = true
		}
		return out
	}
	granted := map[string]bool{}
	for _, id := range p.Grants {
		granted[strings.TrimSpace(id)] = true
	}
	for _, provider := range s.router.State().Providers {
		if provider.Disabled || provider.Deleted {
			continue
		}
		owns := strings.TrimSpace(provider.OwnerUserID) == p.UserID
		if owns || granted[provider.ID] {
			out[provider.ID] = true
		}
	}
	return out
}

// providerVisibleTo reports whether the provider is in the principal's scope.
func (s *Server) providerVisibleTo(p principal, providerID string) bool {
	return s.allowedProviderIDsForPrincipal(p)[strings.TrimSpace(providerID)]
}

// requireProviderOwnerForUser rejects users mutating providers they do not own.
// Grants only widen what a user may *use* (see providerVisibleTo); renaming,
// re-keying or deleting someone else's provider stays owner-only even after an
// admin assigns it to them. "all" passes, and so do admins. Returns false after
// writing the error response.
func (s *Server) requireProviderOwnerForUser(w http.ResponseWriter, r *http.Request, providerID string) bool {
	p := s.requestPrincipal(r)
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return false
	}
	if p.scopeOf() == domain.ScopeAll {
		return true
	}
	if strings.TrimSpace(provider.OwnerUserID) != p.UserID {
		writeOpenAIError(w, http.StatusForbidden, "permission denied: only the provider owner or an admin can do this")
		return false
	}
	return true
}

// requireProviderTesterForUser guards the read-only diagnostic endpoints
// (fetch-models / chat-test): the owner passes, and so do normal users the
// admin granted the provider to — anything a user can see on the Providers
// page must be testable. Edit/delete flows stay on the stricter
// requireProviderOwnerForUser.
func (s *Server) requireProviderTesterForUser(w http.ResponseWriter, r *http.Request, providerID string) bool {
	p := s.requestPrincipal(r)
	if s.providerVisibleTo(p, providerID) {
		return true
	}
	writeOpenAIError(w, http.StatusForbidden, "permission denied: only the provider owner, granted users, or an admin can do this")
	return false
}

// requireRouteAccessForUser rejects users acting on a route whose provider is
// outside their scope: reading, testing, retargeting or deleting it would
// otherwise reach another user's provider (or the administrator's) through a
// route they were never granted. Admins always pass. A route that does not
// exist is the caller's 404 to give, so it is let through here and answered by
// the handler as it always was. Returns false after writing 403.
func (s *Server) requireRouteAccessForUser(w http.ResponseWriter, r *http.Request, routeID string) bool {
	p := s.requestPrincipal(r)
	if p.scopeOf() == domain.ScopeAll || s.router == nil {
		return true
	}
	route, err := s.router.RouteByID(routeID)
	if err != nil {
		return true
	}
	if !s.providerVisibleTo(p, route.ProviderID) {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return false
	}
	return true
}

// requireRouteRetargetForUser refuses a user's update that would move a route
// onto a provider outside their scope — the same rule handleCreateRoute
// applies, so a granted route cannot be re-pointed at somebody else's account.
func (s *Server) requireRouteRetargetForUser(w http.ResponseWriter, r *http.Request, patch domain.Route) bool {
	if strings.TrimSpace(patch.ProviderID) == "" {
		return true
	}
	return s.requireProviderAccessForUser(w, r, strings.TrimSpace(patch.ProviderID))
}

// requireProviderAccessForUser rejects users who cannot see the provider.
func (s *Server) requireProviderAccessForUser(w http.ResponseWriter, r *http.Request, providerID string) bool {
	if s.providerVisibleTo(s.requestPrincipal(r), providerID) {
		return true
	}
	writeOpenAIError(w, http.StatusForbidden, "permission denied")
	return false
}

// providerAllowedForKeyOwner reports whether the owner of an API key may still
// use the given provider at request time. Keys with no owner belong to the
// administrator (legacy/admin keys) and are unrestricted.
//
// Console-side checks (validateKeyProvidersForUser) only run when a key is
// created or edited, so a key stays bound to whatever it was granted back then.
// Without this request-time check, revoking a provider from a user in the 用户
// page left every already-issued key of theirs working against that provider
// indefinitely — both via its bound route and via its fallback chain.
func (s *Server) providerAllowedForKeyOwner(key domain.APIKey, providerID string) bool {
	ownerID := strings.TrimSpace(key.OwnerUserID)
	if ownerID == "" {
		return true
	}
	return s.allowedProviderIDsForUser(ownerID)[strings.TrimSpace(providerID)]
}

// allowedProviderChainForKey filters a failover chain down to the providers the
// key's owner is still granted, preserving priority order. Admin-owned keys have
// no grant table, but a disabled or deleted provider is dropped for them too: a
// paused account must not keep taking traffic just because the key belongs to
// the administrator. An id the router cannot resolve at all is kept — the
// failover loop records it as an undecidable candidate, which is what answers
// "why wasn't it used?", instead of silently dropping a configured entry. A nil
// return means "the chain was not filtered" — the router is not reachable, so
// nothing could be validated and routing falls back to its own checks rather
// than trusting an empty list.
func (s *Server) allowedProviderChainForKey(key domain.APIKey, chain []string) []string {
	if s == nil || s.router == nil {
		return nil
	}
	ownerID := strings.TrimSpace(key.OwnerUserID)
	var allowed map[string]bool
	if ownerID != "" {
		allowed = s.allowedProviderIDsForUser(ownerID)
	}
	out := make([]string, 0, len(chain))
	for _, id := range chain {
		providerID := strings.TrimSpace(id)
		if allowed != nil && !allowed[providerID] {
			continue
		}
		provider, err := s.router.ProviderByID(providerID)
		if s.isDisabledOrDeletedProvider(providerID, err, provider) {
			continue
		}
		out = append(out, id)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// isDisabledOrDeletedProvider reports whether a provider lookup found one that
// may not take traffic: disabled, or soft-deleted.
//
// A provider that is not found at all is deliberately *not* treated as unusable
// here: the failover loop records such a candidate as skipped (the console shows
// it as 「Provider 不存在」), which is how an operator learns that a key still
// points at something that was purged. Dropping the id earlier would hide that.
// The cost of keeping it is a local map lookup, not a request.
func (s *Server) isDisabledOrDeletedProvider(providerID string, err error, provider domain.Provider) bool {
	if err != nil {
		deleted, deletedErr := s.router.ProviderByIDIncludingDeleted(providerID)
		return deletedErr == nil && deleted.Deleted
	}
	return provider.Disabled || provider.Deleted
}

// ownedKeyIDs returns the IDs of API keys owned by the given user.
func (s *Server) ownedKeyIDs(userID string) []string {
	ids := []string{}
	for _, key := range s.router.State().APIKeys {
		if key.OwnerUserID == userID {
			ids = append(ids, key.ID)
		}
	}
	return ids
}

// logOwnerFilterAdmin is the sentinel value the admin-only request-log "所属用户"
// filter uses to mean "keys with no owner (legacy admin-owned keys)", matching
// the frontend's LOG_OWNER_FILTER_ADMIN constant. It is distinct from the empty
// string, which instead means "no owner filter applied" (show every user).
const logOwnerFilterAdmin = "_admin"

// ownedKeyIDsForOwnerFilter resolves the request-log "所属用户" filter value to
// the matching set of API key ids. Unlike ownedKeyIDs (used for per-request
// identity restriction, where an empty OwnerUserID never matches a real user
// id), this treats logOwnerFilterAdmin as matching legacy admin-owned keys
// (empty OwnerUserID), so admins can filter logs down to "管理员" specifically.
func (s *Server) ownedKeyIDsForOwnerFilter(ownerUserID string) []string {
	ids := []string{}
	for _, key := range s.router.State().APIKeys {
		owner := strings.TrimSpace(key.OwnerUserID)
		if ownerUserID == logOwnerFilterAdmin {
			if owner == "" {
				ids = append(ids, key.ID)
			}
			continue
		}
		if owner == ownerUserID {
			ids = append(ids, key.ID)
		}
	}
	return ids
}

// ownerUserIDForStats normalizes a key's OwnerUserID for usage bucketing. Legacy
// keys with an empty owner belong to the administrator, so they collapse onto
// the stable admin id rather than the "_anonymous" bucket.
func ownerUserIDForStats(ownerUserID string) string {
	if strings.TrimSpace(ownerUserID) == "" {
		return legacyAdminUserID
	}
	return strings.TrimSpace(ownerUserID)
}

// resolveUserName maps a stable user id to its current display name. Renames are
// reflected immediately because names are resolved fresh at query time. Unknown
// / deleted users fall back to a readable label keyed by id.
func (s *Server) resolveUserName(userID string) string {
	userID = strings.TrimSpace(userID)
	switch userID {
	case "_anonymous":
		return "未绑定用户"
	case "":
		// Legacy keys without an owner belong to the administrator. Use the
		// real administrator name rather than a synthetic label.
		return s.adminDisplayName()
	case legacyAdminUserID:
		return s.adminDisplayName()
	}
	if s.userStore != nil {
		if user, err := s.userStore.UserByID(userID); err == nil {
			if name := strings.TrimSpace(user.DisplayName); name != "" {
				return name
			}
			if name := strings.TrimSpace(user.Username); name != "" {
				return name
			}
		}
	}
	return "已删除用户(" + userID + ")"
}

// legacyAdminUsername is the display name of the implicit administrator: the
// account named "admin" when one exists, otherwise "admin" itself. It is used
// wherever an owner id is empty/legacy admin, so created providers and keys are
// labelled with the administrator's own name instead of a hardcoded "管理员".
func (s *Server) adminDisplayName() string {
	if s.userStore != nil {
		if user, err := s.userStore.UserByUsername(legacyAdminUserID); err == nil {
			if name := strings.TrimSpace(user.DisplayName); name != "" {
				return name
			}
			if name := strings.TrimSpace(user.Username); name != "" {
				return name
			}
		}
		// Fall back to the first administrator-role account, if any.
		if users, err := s.userStore.ListUsers(); err == nil {
			for _, user := range users {
				if user.Role == domain.UserRoleAdmin {
					if name := strings.TrimSpace(user.DisplayName); name != "" {
						return name
					}
					return user.Username
				}
			}
		}
	}
	return "内置管理员"
}

// adminOwnerID is the user id stored as the owner of everything the
// administrator creates. When an account named "admin" exists it is that
// account, so the console shows its real username instead of "管理员"; the
// implicit legacy administrator keeps its stable id.
func (s *Server) adminOwnerID() string {
	if s.userStore != nil {
		if user, err := s.userStore.UserByUsername(legacyAdminUserID); err == nil && strings.TrimSpace(user.ID) != "" {
			return user.ID
		}
	}
	return legacyAdminUserID
}

// displayNameOf resolves the label for one account: DisplayName, then the
// login name. Used for the session's user card and for owner columns.
func displayNameOf(user domain.User) string {
	if name := strings.TrimSpace(user.DisplayName); name != "" {
		return name
	}
	return user.Username
}

// fillUserNames resolves display names for a byUser stat slice in place.
func (s *Server) fillUserNames(users []monitor.UserDayStats) []monitor.UserDayStats {
	for i := range users {
		users[i].UserName = s.resolveUserName(users[i].UserID)
	}
	return users
}

// fillRequestLogUserNames resolves the owning user's display name for each
// request log in place. It maps each log to its API key (by id, then name),
// derives the owner user id, and resolves the current username. Names are
// resolved fresh at query time so renames reflect immediately; a per-user-id
// cache avoids repeated store lookups within one page.
func (s *Server) fillRequestLogUserNames(logs []monitor.RequestLog) {
	if len(logs) == 0 {
		return
	}
	state := s.router.State()
	keysByID := make(map[string]domain.APIKey, len(state.APIKeys))
	keysByName := make(map[string]domain.APIKey, len(state.APIKeys))
	for _, key := range state.APIKeys {
		keysByID[key.ID] = key
		if name := strings.TrimSpace(key.Name); name != "" {
			keysByName[strings.ToLower(name)] = key
		}
	}
	nameCache := make(map[string]string)
	for i := range logs {
		key, ok := lookupLogAPIKey(logs[i], keysByID, keysByName)
		if !ok {
			logs[i].UserName = "未绑定用户"
			continue
		}
		userID := ownerUserIDForStats(key.OwnerUserID)
		name, cached := nameCache[userID]
		if !cached {
			name = s.resolveUserName(userID)
			nameCache[userID] = name
		}
		logs[i].UserName = name
	}
}

// keysVisibleTo filters API keys per role: "all" scope (and admins) see every
// key; "own"/"granted" scopes see the keys they own, plus — for "granted" —
// keys issued on the administrators' behalf that were explicitly delegated to
// them are not inherited, so a shared-resource user still only manages what it
// created.
func keysVisibleTo(p principal, keys []domain.APIKey) []domain.APIKey {
	if p.scopeOf() == domain.ScopeAll {
		return keys
	}
	out := make([]domain.APIKey, 0)
	for _, key := range keys {
		if strings.TrimSpace(key.OwnerUserID) == p.UserID {
			out = append(out, key)
		}
	}
	return out
}

// validateKeyProvidersForUser rejects routes/fallbacks pointing at providers
// outside the user's resource scope.
func (s *Server) validateKeyProvidersForUser(p principal, key domain.APIKey) error {
	if p.scopeOf() == domain.ScopeAll {
		return nil
	}
	allowed := s.allowedProviderIDsForPrincipal(p)
	if routeID := strings.TrimSpace(key.RouteID); routeID != "" {
		state := s.router.State()
		routeProvider := ""
		for _, route := range state.Routes {
			if route.ID == routeID {
				routeProvider = route.ProviderID
				break
			}
		}
		if routeProvider != "" && !allowed[routeProvider] {
			return fmt.Errorf("provider %q is not assigned to your account", routeProvider)
		}
	}
	for _, providerID := range key.FallbackProviderIDs {
		if !allowed[providerID] {
			return fmt.Errorf("fallback provider %q is not assigned to your account", providerID)
		}
	}
	return nil
}

// stateForUser produces a reduced, redacted state for role=user consoles:
// only own keys, only assigned providers (already secret-redacted upstream),
// no public-access/tunnel/settings details.
// Endpoints and models stay (non-secret) so the API-key page can render client
// URLs and model pickers; never return nil slices — the UI assumes arrays.
func (s *Server) stateForUser(identity sessionIdentity, state domain.GatewayState) domain.GatewayState {
	p := s.principalFor(identity)
	allowed := s.allowedProviderIDsForPrincipal(p)
	providers := make([]domain.Provider, 0)
	for _, provider := range state.Providers {
		if allowed[provider.ID] {
			// 非本人创建的 Provider 对普通用户隐去密钥明文（只留掩码展示）；
			// 自己创建的保留完整值，编辑弹窗需要回填。
			if strings.TrimSpace(provider.OwnerUserID) != identity.UserID {
				provider.APIKeySource = maskAPIKeySource(provider.APIKeySource)
			}
			providers = append(providers, provider)
		}
	}
	routes := make([]domain.Route, 0)
	for _, route := range state.Routes {
		if allowed[route.ProviderID] {
			routes = append(routes, route)
		}
	}
	models := make([]domain.Model, 0)
	for _, model := range state.Models {
		if allowed[model.ProviderID] {
			models = append(models, model)
		}
	}
	state.Providers = providers
	state.Routes = routes
	state.Models = models
	state.APIKeys = keysVisibleTo(p, state.APIKeys)
	// 普通用户看不到未授权的 Provider：把「在服务」里指向它们的那一项按未设置处理，
	// 否则界面会拿着一个不存在的 id 去画绿点。
	for index := range state.APIKeys {
		serving := strings.TrimSpace(state.APIKeys[index].ServingProviderID)
		if serving != "" && !allowed[serving] {
			state.APIKeys[index].ServingProviderID = ""
		}
	}
	if state.Endpoints == nil {
		state.Endpoints = []domain.OutputEndpoint{}
	}
	// Expose only the non-secret public-access fields the user console needs to
	// build the public/tunnel URL. Without these the API-key page falls back to
	// the LAN address; regular users should see the same public domain the admin
	// sees when a tunnel / custom domain is active. Secrets (tunnel token,
	// credentials/config files, tunnel name) are deliberately dropped.
	state.PublicAccess = publicAccessForUser(state.PublicAccess)
	state.LogLevel = ""
	state.DataPaths = nil
	return state
}

// maskAPIKeySource hides key material for display: env: references are not
// secrets and stay readable; literal/raw keys keep only a short prefix/suffix.
func maskAPIKeySource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" || strings.HasPrefix(source, "env:") {
		return source
	}
	value := strings.TrimPrefix(source, "literal:")
	if len(value) <= 8 {
		return "••••"
	}
	return value[:4] + "••••" + value[len(value)-4:]
}

// publicAccessForUser returns a redacted PublicAccessSettings that keeps just
// enough for a role=user console to render the public/tunnel domain URL, while
// stripping every secret / provisioning field.
func publicAccessForUser(src domain.PublicAccessSettings) domain.PublicAccessSettings {
	out := domain.PublicAccessSettings{
		Enabled:         src.Enabled,
		Mode:            src.Mode,
		ExposeAPI:       src.ExposeAPI,
		ExposeUI:        src.ExposeUI,
		CustomDomain:    src.CustomDomain,
		UIDomain:        src.UIDomain,
		PublicBaseURL:   src.PublicBaseURL,
		UIPublicBaseURL: src.UIPublicBaseURL,
		Status:          src.Status,
	}
	if src.Tunnel != nil {
		out.Tunnel = &domain.TunnelRuntime{
			Status:      src.Tunnel.Status,
			Mode:        src.Tunnel.Mode,
			PublicURL:   src.Tunnel.PublicURL,
			UIPublicURL: src.Tunnel.UIPublicURL,
		}
	}
	return out
}

// imagesEffectiveProvider picks the provider an OpenAIImages request is billed
// to. Images has no failover pass of its own — one route, one upstream — so the
// key's chain is resolved here instead: the route's provider followed by the
// key's fallbacks, at the position the scheduler left the key on. It returns ""
// when nothing is left to send to, which the handler must treat as a refusal:
// sending the image to the route default anyway is exactly the revocation the
// chat path already refuses.
func (s *Server) imagesEffectiveProvider(key domain.APIKey, route domain.Route) string {
	routeProvider := strings.TrimSpace(route.ProviderID)
	// The chain as configured, then the same chain minus what is disabled,
	// deleted or no longer granted (grant only binds normal users). Dropping
	// entries shifts positions, so the first allowed provider at or after the
	// active one is taken — the sticky position means "don't go back to the
	// providers before this one", not the provider id itself.
	configured := apiKeyProviderChain(routeProvider, key.FallbackProviderIDs)
	chain := s.allowedProviderChainForKey(key, configured)
	if len(chain) == 0 {
		return ""
	}
	start := len(chain)
	for index, providerID := range configured {
		if !containsProviderID(chain, providerID) {
			continue
		}
		if index >= apiKeyEffectiveProviderIndex(key.ActiveProviderID, configured) {
			start = index
			break
		}
	}
	// The sticky provider is the last allowed one: there is nowhere left to go,
	// and falling back to the top would undo the switch the scheduler made.
	for index := start; index < len(configured); index++ {
		if containsProviderID(chain, configured[index]) {
			return configured[index]
		}
	}
	return ""
}

// containsProviderID reports whether id is one of ids.
func containsProviderID(ids []string, id string) bool {
	for _, candidate := range ids {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}
