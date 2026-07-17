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

// allowedProviderIDsForUser returns the providers a normal user may access:
// the admin-assigned whitelist plus every provider the user created
// themselves (ownership implies access). Providers disabled by the admin are
// excluded entirely — normal users cannot see, bind, or use them until
// re-enabled.
func (s *Server) allowedProviderIDsForUser(userID string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(userID) == "" {
		return out
	}
	granted := map[string]bool{}
	if s.userStore != nil {
		if user, err := s.userStore.UserByID(userID); err == nil {
			for _, id := range user.AllowedProviderIDs {
				granted[id] = true
			}
		}
	}
	for _, provider := range s.router.State().Providers {
		if provider.Disabled || provider.Deleted {
			continue
		}
		if granted[provider.ID] || strings.TrimSpace(provider.OwnerUserID) == userID {
			out[provider.ID] = true
		}
	}
	return out
}

// requireProviderOwnerForUser rejects normal users acting on providers they
// did not create (edit/clone/delete/test are owner-only for role=user).
// Admins always pass. Returns false after writing the error response.
func (s *Server) requireProviderOwnerForUser(w http.ResponseWriter, r *http.Request, providerID string) bool {
	identity := s.requestIdentity(r)
	if identity.isAdmin() {
		return true
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return false
	}
	if strings.TrimSpace(provider.OwnerUserID) != identity.UserID {
		writeOpenAIError(w, http.StatusForbidden, "permission denied: only the provider owner or an admin can do this")
		return false
	}
	return true
}

// requireProviderAccessForUser rejects normal users who were not granted the
// given provider. Admins always pass. Returns false after writing 403.
func (s *Server) requireProviderAccessForUser(w http.ResponseWriter, r *http.Request, providerID string) bool {
	identity := s.requestIdentity(r)
	if identity.isAdmin() {
		return true
	}
	if !s.allowedProviderIDsForUser(identity.UserID)[providerID] {
		writeOpenAIError(w, http.StatusForbidden, "permission denied")
		return false
	}
	return true
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
	switch userID {
	case "", "_anonymous":
		return "未绑定用户"
	case legacyAdminUserID:
		return "管理员"
	}
	if s.userStore != nil {
		if user, err := s.userStore.UserByID(userID); err == nil {
			if name := strings.TrimSpace(user.Username); name != "" {
				return name
			}
		}
	}
	return "已删除用户(" + userID + ")"
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

// keysVisibleTo filters API keys per role: admins see all, users see own.
func keysVisibleTo(identity sessionIdentity, keys []domain.APIKey) []domain.APIKey {
	if identity.isAdmin() {
		return keys
	}
	out := make([]domain.APIKey, 0)
	for _, key := range keys {
		if key.OwnerUserID == identity.UserID {
			out = append(out, key)
		}
	}
	return out
}

// validateKeyProvidersForUser rejects routes/fallbacks pointing at providers
// the user has not been granted by the administrator.
func (s *Server) validateKeyProvidersForUser(identity sessionIdentity, key domain.APIKey) error {
	if identity.isAdmin() {
		return nil
	}
	allowed := s.allowedProviderIDsForUser(identity.UserID)
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
	allowed := s.allowedProviderIDsForUser(identity.UserID)
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
	state.APIKeys = keysVisibleTo(identity, state.APIKeys)
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
