package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
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

// allowedProviderIDsForUser loads the admin-assigned provider whitelist.
func (s *Server) allowedProviderIDsForUser(userID string) map[string]bool {
	out := map[string]bool{}
	if s.userStore == nil {
		return out
	}
	user, err := s.userStore.UserByID(userID)
	if err != nil {
		return out
	}
	for _, id := range user.AllowedProviderIDs {
		out[id] = true
	}
	return out
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
	state.PublicAccess = domain.PublicAccessSettings{}
	state.LogLevel = ""
	state.DataPaths = nil
	return state
}
