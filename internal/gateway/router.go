package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/luca/llm-protocol-gateway/internal/config"
	"github.com/luca/llm-protocol-gateway/internal/domain"
)

type Router struct {
	mu    sync.RWMutex
	state domain.GatewayState
}

func NewRouter(state domain.GatewayState) *Router {
	mergeDefaultEndpoints(&state)
	normalizePublicAccess(&state)
	rebuildModels(&state)
	return &Router{state: state}
}

func mergeDefaultEndpoints(state *domain.GatewayState) {
	defaults := config.DefaultEndpoints()
	overrides := make(map[string]domain.OutputEndpoint, len(state.Endpoints))
	for _, endpoint := range state.Endpoints {
		overrides[endpoint.ID] = endpoint
	}
	merged := make([]domain.OutputEndpoint, 0, len(defaults))
	for _, endpoint := range defaults {
		endpoint.StreamEnabled = true
		if override, ok := overrides[endpoint.ID]; ok {
			// StreamEnabled is applied from a dedicated settings map when present;
			// endpoint copies from Load may carry the field after an explicit save.
			endpoint.StreamEnabled = override.StreamEnabled
			if host := strings.TrimSpace(override.ListenHost); host != "" {
				endpoint.ListenHost = host
			}
			if override.ListenPort > 0 {
				endpoint.ListenPort = override.ListenPort
			}
		}
		merged = append(merged, endpoint)
	}
	state.Endpoints = merged
}

// ApplyEndpointStreamOverrides merges persisted per-endpoint stream flags onto
// the fixed endpoint list. Missing IDs keep the current (default true) value.
func ApplyEndpointStreamOverrides(state *domain.GatewayState, overrides map[string]bool) {
	if len(overrides) == 0 {
		return
	}
	for index := range state.Endpoints {
		if enabled, ok := overrides[state.Endpoints[index].ID]; ok {
			state.Endpoints[index].StreamEnabled = enabled
		}
	}
}

// UpdateEndpointStreamEnabled toggles whether clients may request SSE streaming
// on a fixed output protocol endpoint.
func (r *Router) UpdateEndpointStreamEnabled(endpointID string, enabled bool) (domain.OutputEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Endpoints {
		if r.state.Endpoints[index].ID != endpointID {
			continue
		}
		r.state.Endpoints[index].StreamEnabled = enabled
		return r.state.Endpoints[index], nil
	}
	return domain.OutputEndpoint{}, fmt.Errorf("endpoint %q not found", endpointID)
}

// EndpointByProtocol returns the fixed output endpoint for a protocol.
func (r *Router) EndpointByProtocol(protocol domain.Protocol) (domain.OutputEndpoint, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.endpointByProtocolLocked(protocol)
}

// StreamEnabledForProtocol reports whether streaming is allowed on the output
// protocol. Missing endpoints default to enabled.
func (r *Router) StreamEnabledForProtocol(protocol domain.Protocol) bool {
	endpoint, ok := r.EndpointByProtocol(protocol)
	if !ok {
		return true
	}
	return endpoint.StreamEnabled
}

// SetEndpointAdvertise updates the host/port shown to clients for fixed output
// endpoints (LAN IP + listen port). Bind address is controlled separately by
// GATEWAY_ADDR.
func (r *Router) SetEndpointAdvertise(host string, port int) {
	host = strings.TrimSpace(host)
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Endpoints {
		if host != "" {
			r.state.Endpoints[index].ListenHost = host
		}
		if port > 0 {
			r.state.Endpoints[index].ListenPort = port
		}
	}
}

func (r *Router) State() domain.GatewayState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *Router) SetRequestLogRetentionDays(days int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if days <= 0 {
		days = 7
	}
	r.state.RequestLogRetentionDays = days
}

func (r *Router) SetWebExposed(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.WebExposed = enabled
}

func (r *Router) WebExposed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.WebExposed
}

func (r *Router) UpdatePublicAccess(settings domain.PublicAccessSettings) domain.PublicAccessSettings {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.state.PublicAccess
	current.Enabled = settings.Enabled
	if strings.TrimSpace(settings.Provider) != "" {
		current.Provider = strings.TrimSpace(settings.Provider)
	}
	if settings.Mode != "" {
		current.Mode = settings.Mode
	}
	// Domains and named-tunnel fields are assigned verbatim (including empty) so
	// callers can clear a previous custom-domain bind. Internal helpers that only
	// want a partial update must pass the current values they intend to keep.
	current.CustomDomain = strings.TrimSpace(settings.CustomDomain)
	current.UIDomain = strings.TrimSpace(settings.UIDomain)
	// ExposeAPI / ExposeUI are only applied for custom-domain writes. Quick-tunnel
	// / partial updates omit these bools (JSON false zero-value) and must not wipe
	// the independently configured public surfaces.
	if settings.Mode == domain.PublicAccessModeCustomDomain ||
		current.CustomDomain != "" ||
		current.UIDomain != "" {
		current.ExposeAPI = settings.ExposeAPI
		current.ExposeUI = settings.ExposeUI
	}
	if strings.TrimSpace(settings.Expose) != "" {
		current.Expose = strings.TrimSpace(settings.Expose)
	}
	current.RuntimeURL = strings.TrimSpace(settings.RuntimeURL)
	current.TunnelName = strings.TrimSpace(settings.TunnelName)
	current.TunnelToken = strings.TrimSpace(settings.TunnelToken)
	current.CredentialsFile = strings.TrimSpace(settings.CredentialsFile)
	current.TunnelConfigFile = strings.TrimSpace(settings.TunnelConfigFile)

	r.state.PublicAccess = current
	normalizePublicAccess(&r.state)
	return r.state.PublicAccess
}

func normalizePublicAccess(state *domain.GatewayState) {
	settings := state.PublicAccess
	if strings.TrimSpace(settings.Provider) == "" {
		settings.Provider = "cloudflare"
	}
	if settings.Mode == "" {
		settings.Mode = domain.PublicAccessModeRandomTunnel
	}
	if strings.TrimSpace(settings.Expose) == "" {
		settings.Expose = "all"
	}
	settings.CustomDomain = cleanPublicHost(settings.CustomDomain)
	settings.UIDomain = cleanPublicHost(settings.UIDomain)
	settings.RuntimeURL = strings.TrimRight(strings.TrimSpace(settings.RuntimeURL), "/")
	settings.PublicBaseURL = ""
	settings.UIPublicBaseURL = ""

	if settings.Mode == domain.PublicAccessModeCustomDomain &&
		settings.CustomDomain != "" &&
		settings.UIDomain != "" &&
		strings.EqualFold(settings.CustomDomain, settings.UIDomain) {
		// Keep API hostname; force UI hostname empty so callers re-bind with a split pair.
		settings.UIDomain = ""
	}
	if settings.Mode == domain.PublicAccessModeCustomDomain {
		if !settings.ExposeAPI && !settings.ExposeUI {
			// Keep mode, but nothing is published until a surface is enabled.
		}
		if settings.ExposeAPI && settings.CustomDomain == "" && settings.ExposeUI && settings.UIDomain == "" {
			settings.Mode = domain.PublicAccessModeRandomTunnel
		}
	}

	switch {
	case !settings.Enabled:
		settings.Status = "disabled"
		settings.StatusMessage = "Public access is disabled. Enable it to use a Cloudflare quick tunnel URL, or configure a purchased Cloudflare domain such as lucadesign.uk."
	case settings.Provider != "cloudflare":
		settings.Status = "unsupported"
		settings.StatusMessage = "Only the Cloudflare control-plane scaffold is available in this MVP."
	case settings.Mode == domain.PublicAccessModeCustomDomain:
		if settings.ExposeAPI && settings.CustomDomain != "" {
			settings.PublicBaseURL = "https://" + settings.CustomDomain
		}
		if settings.ExposeUI && settings.UIDomain != "" {
			settings.UIPublicBaseURL = "https://" + settings.UIDomain
		}
		settings.Status = "configured_pending_tunnel"
		settings.StatusMessage = "Custom domain is saved locally. Live Cloudflare setup still needs cloudflared credentials or API token, a tunnel, DNS route, and ingress mapping to this gateway."
	case settings.RuntimeURL != "":
		settings.PublicBaseURL = settings.RuntimeURL
		settings.Status = "runtime_url_recorded"
		settings.StatusMessage = "Cloudflare random tunnel mode is selected. The URL is recorded from the tunnel runtime; start cloudflared quick tunnel to refresh it."
	default:
		settings.Status = "waiting_for_tunnel"
		settings.StatusMessage = "Cloudflare random tunnel mode is selected. Start cloudflared with the local gateway URL to receive a random trycloudflare.com domain."
	}

	state.PublicAccess = settings
	for index := range state.Endpoints {
		apiPublished := settings.Enabled && settings.PublicBaseURL != "" &&
			(settings.Mode != domain.PublicAccessModeCustomDomain || settings.ExposeAPI)
		state.Endpoints[index].PublicAccessEnabled = apiPublished
		if apiPublished && publicExposeMatches(settings.Expose, state.Endpoints[index].Protocol) {
			state.Endpoints[index].PublicURL = settings.PublicBaseURL + state.Endpoints[index].BasePath
		} else {
			state.Endpoints[index].PublicURL = ""
		}
	}
}

func cleanPublicHost(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	return strings.TrimRight(value, "/")
}

func publicExposeMatches(expose string, protocol domain.Protocol) bool {
	switch expose {
	case "openai_chat":
		return protocol == domain.ProtocolOpenAIChat
	case "openai_responses":
		return protocol == domain.ProtocolOpenAIResponses
	case "claude":
		return protocol == domain.ProtocolClaude
	default:
		return true
	}
}

func (r *Router) AddProvider(provider domain.Provider) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	provider.Name = strings.TrimSpace(provider.Name)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	provider.DefaultModel = strings.TrimSpace(provider.DefaultModel)
	provider.DefaultThinkingDepth = strings.TrimSpace(provider.DefaultThinkingDepth)
	if provider.Name == "" {
		return domain.Provider{}, fmt.Errorf("provider name is required")
	}
	// OAuth providers have their baseUrl auto-filled by normalizeProvider
	// (there's nothing else for the user to configure), so skip the manual
	// requirement for those modes only.
	if provider.BaseURL == "" &&
		provider.AuthType != domain.AuthTypeClaudeOAuth &&
		provider.AuthType != domain.AuthTypeCursorOAuth &&
		provider.AuthType != domain.AuthTypeChatGPTOAuth {
		return domain.Provider{}, fmt.Errorf("provider baseUrl is required")
	}
	if provider.ID == "" {
		provider.ID = uniqueID(slug(provider.Name), func(id string) bool {
			for _, item := range r.state.Providers {
				if item.ID == id {
					return true
				}
			}
			return false
		})
	}
	normalizeProvider(&provider)

	r.state.Providers = append(r.state.Providers, provider)
	r.rebuildModelsLocked()
	return provider, nil
}

func (r *Router) UpdateProvider(providerID string, patch domain.Provider) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		if strings.TrimSpace(patch.Name) != "" {
			updated.Name = strings.TrimSpace(patch.Name)
		}
		if patch.Protocol != "" {
			updated.Protocol = patch.Protocol
		}
		if strings.TrimSpace(patch.BaseURL) != "" {
			updated.BaseURL = strings.TrimSpace(patch.BaseURL)
		}
		updated.APIKeySource = strings.TrimSpace(patch.APIKeySource)
		updated.DefaultModel = strings.TrimSpace(patch.DefaultModel)
		updated.DefaultThinkingDepth = strings.TrimSpace(patch.DefaultThinkingDepth)
		if strings.TrimSpace(patch.AuthHeader) != "" {
			updated.AuthHeader = strings.TrimSpace(patch.AuthHeader)
		}
		if patch.Models != nil {
			updated.Models = patch.Models
		}
		if strings.TrimSpace(patch.AuthType) != "" {
			updated.AuthType = strings.TrimSpace(patch.AuthType)
		}
		// Explicitly allow clearing requestAdapter by sending null/empty object.
		updated.RequestAdapter = patch.RequestAdapter
		// Zhipu coding-plan quota-query config (non-secret). Sent verbatim from
		// the editor; trim so blank inputs clear the fields.
		updated.CodingPlanProvider = strings.TrimSpace(patch.CodingPlanProvider)
		updated.TeamOrganizationID = strings.TrimSpace(patch.TeamOrganizationID)
		updated.TeamProjectID = strings.TrimSpace(patch.TeamProjectID)
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		r.rebuildModelsLocked()
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

func normalizeProvider(provider *domain.Provider) {
	provider.APIKeySource = strings.TrimSpace(provider.APIKeySource)
	provider.AuthType = strings.TrimSpace(provider.AuthType)
	if provider.AuthType == "" {
		provider.AuthType = domain.AuthTypeAPIKey
	}
	if provider.AuthType == domain.AuthTypeClaudeOAuth {
		// Claude OAuth providers always talk to the official Anthropic API;
		// there is nothing else for the user to configure.
		provider.Protocol = domain.ProtocolClaude
		provider.BaseURL = "https://api.anthropic.com"
	}
	if provider.AuthType == domain.AuthTypeCursorOAuth {
		// Cursor OAuth providers proxy through the local gRPC bridge at request time.
		provider.Protocol = domain.ProtocolOpenAIChat
		provider.BaseURL = ""
	}
	if provider.AuthType == domain.AuthTypeChatGPTOAuth {
		// ChatGPT OAuth providers call Codex CLI upstream (chatgpt.com/backend-api).
		provider.Protocol = domain.ProtocolOpenAIResponses
		provider.BaseURL = chatgptCodexResponsesURL
	}
	if provider.HealthStatus == "" {
		provider.HealthStatus = "unchecked"
	}
	if provider.AuthHeader == "" {
		provider.AuthHeader = "Authorization"
	}
	provider.RequestAdapter = normalizeRequestAdapter(provider.RequestAdapter)
	if len(provider.Models) == 0 && provider.DefaultModel != "" {
		provider.Models = []domain.Model{{
			ID:         provider.DefaultModel,
			ProviderID: provider.ID,
			Protocol:   provider.Protocol,
			InMenu:     true,
		}}
		fillModelTokenBudgets(&provider.Models[0])
	}
	for index := range provider.Models {
		provider.Models[index].ProviderID = provider.ID
		provider.Models[index].Protocol = provider.Protocol
		fillModelTokenBudgets(&provider.Models[index])
	}
}

func (r *Router) AddRoute(route domain.Route) (domain.Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	route.Name = strings.TrimSpace(route.Name)
	if route.Name == "" {
		return domain.Route{}, fmt.Errorf("route name is required")
	}
	if provider, ok := r.providerLocked(route.ProviderID); !ok || provider.Deleted {
		return domain.Route{}, fmt.Errorf("provider %q not found", route.ProviderID)
	}
	endpoint, ok := r.endpointByProtocolLocked(route.OutputProtocol)
	if !ok {
		return domain.Route{}, fmt.Errorf("output protocol %q not available", route.OutputProtocol)
	}
	if route.ID == "" {
		route.ID = uniqueID(slug(route.Name), func(id string) bool {
			for _, item := range r.state.Routes {
				if item.ID == id {
					return true
				}
			}
			return false
		})
	}
	if route.OutputEndpointID == "" {
		route.OutputEndpointID = endpoint.ID
	}
	if route.Mode == "" {
		route.Mode = domain.RouteModeAuto
	}
	route.Enabled = true

	r.state.Routes = append(r.state.Routes, route)
	return route, nil
}

func (r *Router) UpdateRoute(routeID string, patch domain.Route) (domain.Route, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Routes {
		if r.state.Routes[index].ID != routeID {
			continue
		}
		updated := r.state.Routes[index]
		if strings.TrimSpace(patch.Name) != "" {
			updated.Name = strings.TrimSpace(patch.Name)
		}
		if strings.TrimSpace(patch.ProviderID) != "" {
			if provider, ok := r.providerLocked(patch.ProviderID); !ok || provider.Deleted {
				return domain.Route{}, fmt.Errorf("provider %q not found", patch.ProviderID)
			}
			updated.ProviderID = strings.TrimSpace(patch.ProviderID)
		}
		if patch.OutputProtocol != "" {
			endpoint, ok := r.endpointByProtocolLocked(patch.OutputProtocol)
			if !ok {
				return domain.Route{}, fmt.Errorf("output protocol %q not available", patch.OutputProtocol)
			}
			updated.OutputProtocol = patch.OutputProtocol
			updated.OutputEndpointID = endpoint.ID
		}
		if patch.Mode != "" {
			updated.Mode = patch.Mode
		}
		r.state.Routes[index] = updated
		return updated, nil
	}
	return domain.Route{}, fmt.Errorf("route %q not found", routeID)
}

func (r *Router) DeleteRoute(routeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, key := range r.state.APIKeys {
		if key.RouteID == routeID {
			return fmt.Errorf("route %q is used by api key %q", routeID, key.Name)
		}
	}
	for index := range r.state.Routes {
		if r.state.Routes[index].ID == routeID {
			r.state.Routes = append(r.state.Routes[:index], r.state.Routes[index+1:]...)
			return nil
		}
	}
	return fmt.Errorf("route %q not found", routeID)
}

func (r *Router) AddAPIKey(key domain.APIKey) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key.Name = strings.TrimSpace(key.Name)
	if key.Name == "" {
		return domain.APIKey{}, fmt.Errorf("api key name is required")
	}
	key.RouteID = strings.TrimSpace(key.RouteID)
	if key.RouteID != "" {
		if _, ok := r.routeLocked(key.RouteID); !ok {
			return domain.APIKey{}, fmt.Errorf("route %q not found", key.RouteID)
		}
	}
	if strings.TrimSpace(key.Key) == "" {
		token, err := generateAPIKey()
		if err != nil {
			return domain.APIKey{}, err
		}
		key.Key = token
	}
	if key.ID == "" {
		key.ID = uniqueID(slug(key.Name), func(id string) bool {
			for _, item := range r.state.APIKeys {
				if item.ID == id {
					return true
				}
			}
			return false
		})
	}
	key.ModelOverride = strings.TrimSpace(key.ModelOverride)
	key.ModelAliases = normalizeModelAliases(key.ModelAliases)
	key.ThinkingDepthOverride = strings.TrimSpace(key.ThinkingDepthOverride)
	key.MaxOutputTokens = normalizeMaxOutputTokens(key.MaxOutputTokens)
	key.FallbackProviderIDs = normalizeFallbackProviderIDs(key.FallbackProviderIDs, "")
	key.FallbackModelOverrides = normalizeFallbackModelOverrides(key.FallbackProviderIDs, key.FallbackModelOverrides)
	if err := validateFallbackModelOverrides(key.FallbackProviderIDs, key.FallbackModelOverrides); err != nil {
		return domain.APIKey{}, err
	}
	key.ActiveProviderID = strings.TrimSpace(key.ActiveProviderID)
	key.OwnerUserID = strings.TrimSpace(key.OwnerUserID)
	key.Enabled = true
	if key.CreatedAt == "" {
		key.CreatedAt = nowRFC3339()
	}
	// StreamEnabled default is handled by the HTTP handler (absent field => true);
	// the value passed here is authoritative.

	r.state.APIKeys = append(r.state.APIKeys, key)
	return key, nil
}

// UpdateAPIKeyOwner reassigns a key to a console user ("" = admin-owned).
func (r *Router) UpdateAPIKeyOwner(keyID, ownerUserID string) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		if r.state.APIKeys[index].ID != keyID {
			continue
		}
		r.state.APIKeys[index].OwnerUserID = strings.TrimSpace(ownerUserID)
		return r.state.APIKeys[index], nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

func (r *Router) UpdateAPIKey(keyID string, patch domain.APIKey) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.APIKeys {
		if r.state.APIKeys[index].ID != keyID {
			continue
		}
		updated := r.state.APIKeys[index]
		if strings.TrimSpace(patch.Name) != "" {
			updated.Name = strings.TrimSpace(patch.Name)
		}
		if routeID := strings.TrimSpace(patch.RouteID); routeID != "" {
			if _, ok := r.routeLocked(routeID); !ok {
				return domain.APIKey{}, fmt.Errorf("route %q not found", routeID)
			}
			updated.RouteID = routeID
		}
		updated.ModelOverride = strings.TrimSpace(patch.ModelOverride)
		if patch.ModelAliases != nil {
			updated.ModelAliases = normalizeModelAliases(patch.ModelAliases)
		}
		updated.ThinkingDepthOverride = strings.TrimSpace(patch.ThinkingDepthOverride)
		updated.MaxOutputTokens = normalizeMaxOutputTokens(patch.MaxOutputTokens)
		updated.Enabled = patch.Enabled
		updated.StreamEnabled = patch.StreamEnabled
		updated.CodexKeepOfficialLogin = patch.CodexKeepOfficialLogin
		preferredProviderID := ""
		if route, ok := r.routeLocked(updated.RouteID); ok {
			preferredProviderID = route.ProviderID
		}
		if patch.FallbackProviderIDs != nil {
			updated.FallbackProviderIDs = normalizeFallbackProviderIDs(patch.FallbackProviderIDs, preferredProviderID)
			if patch.FallbackModelOverrides != nil {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, patch.FallbackModelOverrides)
			} else {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, updated.FallbackModelOverrides)
			}
			if err := validateFallbackModelOverrides(updated.FallbackProviderIDs, updated.FallbackModelOverrides); err != nil {
				return domain.APIKey{}, err
			}
		} else {
			updated.FallbackProviderIDs = normalizeFallbackProviderIDs(updated.FallbackProviderIDs, preferredProviderID)
			if patch.FallbackModelOverrides != nil {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, patch.FallbackModelOverrides)
				if err := validateFallbackModelOverrides(updated.FallbackProviderIDs, updated.FallbackModelOverrides); err != nil {
					return domain.APIKey{}, err
				}
			} else {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, updated.FallbackModelOverrides)
			}
		}
		updated.ActiveProviderID = sanitizeActiveProviderID(updated.ActiveProviderID, preferredProviderID, updated.FallbackProviderIDs)
		// Keep the active profile's full snapshot in sync with live top-level
		// routing fields so editing the form is editing the current scheme.
		if pid := strings.TrimSpace(updated.ActiveProfileID); pid != "" {
			if pIdx := profileIndex(updated.Profiles, pid); pIdx >= 0 {
				name := updated.Profiles[pIdx].Name
				updated.Profiles[pIdx] = keyProfileFromKey(updated, pid, name)
			}
		}
		r.state.APIKeys[index] = updated
		return updated, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// SetAPIKeyActiveProvider updates the sticky in-use provider after failover/recovery.
func (r *Router) SetAPIKeyActiveProvider(keyID, providerID string) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.APIKeys {
		if r.state.APIKeys[index].ID != keyID {
			continue
		}
		updated := r.state.APIKeys[index]
		preferredProviderID := ""
		if route, ok := r.routeLocked(updated.RouteID); ok {
			preferredProviderID = route.ProviderID
		}
		updated.ActiveProviderID = sanitizeActiveProviderID(strings.TrimSpace(providerID), preferredProviderID, updated.FallbackProviderIDs)
		r.state.APIKeys[index] = updated
		return updated, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// keyProfileFromKey captures the key's current top-level routing fields as a
// profile snapshot (used to seed a default profile for legacy keys).
func keyProfileFromKey(key domain.APIKey, id, name string) domain.KeyProfile {
	return domain.KeyProfile{
		ID:                     id,
		Name:                   name,
		RouteID:                key.RouteID,
		ModelOverride:          key.ModelOverride,
		ModelAliases:           cloneStringMap(key.ModelAliases),
		ThinkingDepthOverride:  key.ThinkingDepthOverride,
		MaxOutputTokens:        key.MaxOutputTokens,
		FallbackProviderIDs:    cloneStringSlice(key.FallbackProviderIDs),
		FallbackModelOverrides: cloneStringMap(key.FallbackModelOverrides),
		StreamEnabled:          key.StreamEnabled,
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// profileIndex returns the slice index of a profile id, or -1.
func profileIndex(profiles []domain.KeyProfile, profileID string) int {
	for i := range profiles {
		if profiles[i].ID == profileID {
			return i
		}
	}
	return -1
}

// AddKeyProfile appends a new named forwarding profile to a key. When it is the
// first alternative, the key's current live top-level config is preserved as
// 「默认」so the prior setup becomes scheme #1; the new profile is then added
// as scheme #2 (and activated when activate is true).
func (r *Router) AddKeyProfile(keyID string, profile domain.KeyProfile, activate bool) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if key.ID != keyID {
			continue
		}
		profile.Name = strings.TrimSpace(profile.Name)
		if profile.Name == "" {
			return domain.APIKey{}, fmt.Errorf("profile name is required")
		}
		if strings.TrimSpace(profile.RouteID) != "" {
			if _, ok := r.routeLocked(profile.RouteID); !ok {
				return domain.APIKey{}, fmt.Errorf("route %q not found", profile.RouteID)
			}
		}
		profile.ID = uniqueID(slug(profile.Name), func(id string) bool {
			return profileIndex(key.Profiles, id) >= 0
		})
		profile = r.normalizeProfileLocked(profile)
		// First alternative: preserve the key's existing live config as「默认」
		// so the prior setup becomes scheme #1, then the new profile is #2.
		if len(key.Profiles) == 0 {
			defaultID := uniqueID("default", func(id string) bool { return id == profile.ID })
			key.Profiles = append(key.Profiles, keyProfileFromKey(*key, defaultID, "默认"))
			key.ActiveProfileID = defaultID
		}
		key.Profiles = append(key.Profiles, profile)
		if activate {
			r.activateProfileLocked(key, profile.ID)
		}
		return *key, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// UpdateKeyProfile edits an existing profile in place; if it is the active
// profile the change is reflected onto the key's live routing fields too.
func (r *Router) UpdateKeyProfile(keyID, profileID string, profile domain.KeyProfile) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if key.ID != keyID {
			continue
		}
		pIdx := profileIndex(key.Profiles, profileID)
		if pIdx < 0 {
			return domain.APIKey{}, fmt.Errorf("profile %q not found", profileID)
		}
		if name := strings.TrimSpace(profile.Name); name != "" {
			key.Profiles[pIdx].Name = name
		}
		if strings.TrimSpace(profile.RouteID) != "" {
			if _, ok := r.routeLocked(profile.RouteID); !ok {
				return domain.APIKey{}, fmt.Errorf("route %q not found", profile.RouteID)
			}
		}
		merged := key.Profiles[pIdx]
		merged.RouteID = profile.RouteID
		merged.ModelOverride = profile.ModelOverride
		merged.ModelAliases = profile.ModelAliases
		merged.ThinkingDepthOverride = profile.ThinkingDepthOverride
		merged.MaxOutputTokens = profile.MaxOutputTokens
		merged.FallbackProviderIDs = profile.FallbackProviderIDs
		merged.FallbackModelOverrides = profile.FallbackModelOverrides
		merged.StreamEnabled = profile.StreamEnabled
		merged = r.normalizeProfileLocked(merged)
		key.Profiles[pIdx] = merged
		if key.ActiveProfileID == profileID {
			r.activateProfileLocked(key, profileID)
		}
		return *key, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// DeleteKeyProfile removes a profile. Deleting the active profile falls back to
// the first remaining profile (if any).
func (r *Router) DeleteKeyProfile(keyID, profileID string) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if key.ID != keyID {
			continue
		}
		pIdx := profileIndex(key.Profiles, profileID)
		if pIdx < 0 {
			return domain.APIKey{}, fmt.Errorf("profile %q not found", profileID)
		}
		key.Profiles = append(key.Profiles[:pIdx], key.Profiles[pIdx+1:]...)
		if key.ActiveProfileID == profileID {
			key.ActiveProfileID = ""
			if len(key.Profiles) > 0 {
				r.activateProfileLocked(key, key.Profiles[0].ID)
			}
		}
		return *key, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// SwitchKeyProfile makes the given profile active: its routing snapshot is
// copied onto the key's live top-level fields. The client token is unchanged.
func (r *Router) SwitchKeyProfile(keyID, profileID string) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if key.ID != keyID {
			continue
		}
		if profileIndex(key.Profiles, profileID) < 0 {
			return domain.APIKey{}, fmt.Errorf("profile %q not found", profileID)
		}
		r.activateProfileLocked(key, profileID)
		return *key, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

// normalizeProfileLocked cleans a profile's fields against its own route's
// preferred provider. Caller must hold r.mu.
func (r *Router) normalizeProfileLocked(profile domain.KeyProfile) domain.KeyProfile {
	profile.RouteID = strings.TrimSpace(profile.RouteID)
	profile.ModelOverride = strings.TrimSpace(profile.ModelOverride)
	profile.ModelAliases = normalizeModelAliases(profile.ModelAliases)
	profile.ThinkingDepthOverride = strings.TrimSpace(profile.ThinkingDepthOverride)
	profile.MaxOutputTokens = normalizeMaxOutputTokens(profile.MaxOutputTokens)
	preferredProviderID := ""
	if route, ok := r.routeLocked(profile.RouteID); ok {
		preferredProviderID = route.ProviderID
	}
	profile.FallbackProviderIDs = normalizeFallbackProviderIDs(profile.FallbackProviderIDs, preferredProviderID)
	profile.FallbackModelOverrides = normalizeFallbackModelOverrides(profile.FallbackProviderIDs, profile.FallbackModelOverrides)
	return profile
}

// activateProfileLocked copies a profile's routing fields onto the key's live
// top-level fields and marks it active. Caller must hold r.mu.
func (r *Router) activateProfileLocked(key *domain.APIKey, profileID string) {
	pIdx := profileIndex(key.Profiles, profileID)
	if pIdx < 0 {
		return
	}
	profile := key.Profiles[pIdx]
	key.RouteID = strings.TrimSpace(profile.RouteID)
	key.ModelOverride = strings.TrimSpace(profile.ModelOverride)
	key.ModelAliases = normalizeModelAliases(profile.ModelAliases)
	key.ThinkingDepthOverride = strings.TrimSpace(profile.ThinkingDepthOverride)
	key.MaxOutputTokens = normalizeMaxOutputTokens(profile.MaxOutputTokens)
	preferredProviderID := ""
	if route, ok := r.routeLocked(key.RouteID); ok {
		preferredProviderID = route.ProviderID
	}
	key.FallbackProviderIDs = normalizeFallbackProviderIDs(profile.FallbackProviderIDs, preferredProviderID)
	key.FallbackModelOverrides = normalizeFallbackModelOverrides(key.FallbackProviderIDs, profile.FallbackModelOverrides)
	key.StreamEnabled = profile.StreamEnabled
	key.ActiveProviderID = "" // reset failover position; chain may differ
	key.ActiveProfileID = profileID
}

// SnapshotKeyProfile saves the key's current live top-level config as a new
// named profile and (optionally) activates it. Useful to preserve a legacy
// key's existing configuration before adding alternatives.
func (r *Router) SnapshotKeyProfile(keyID, name string, activate bool) (domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if key.ID != keyID {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return domain.APIKey{}, fmt.Errorf("profile name is required")
		}
		id := uniqueID(slug(name), func(id string) bool {
			return profileIndex(key.Profiles, id) >= 0
		})
		profile := keyProfileFromKey(*key, id, name)
		key.Profiles = append(key.Profiles, profile)
		if activate || key.ActiveProfileID == "" {
			key.ActiveProfileID = id
		}
		return *key, nil
	}
	return domain.APIKey{}, fmt.Errorf("api key %q not found", keyID)
}

func normalizeFallbackProviderIDs(ids []string, preferredProviderID string) []string {
	preferredProviderID = strings.TrimSpace(preferredProviderID)
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	if preferredProviderID != "" {
		seen[preferredProviderID] = struct{}{}
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeFallbackModelOverrides(fallbackIDs []string, overrides map[string]string) map[string]string {
	if len(fallbackIDs) == 0 {
		return nil
	}
	out := make(map[string]string, len(fallbackIDs))
	for _, id := range fallbackIDs {
		model := ""
		if overrides != nil {
			model = strings.TrimSpace(overrides[id])
		}
		if model != "" {
			out[id] = model
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateFallbackModelOverrides(fallbackIDs []string, overrides map[string]string) error {
	for _, id := range fallbackIDs {
		if overrides == nil || strings.TrimSpace(overrides[id]) == "" {
			return fmt.Errorf("fallback provider %q requires a fixed model override", id)
		}
	}
	return nil
}

func sanitizeActiveProviderID(activeID, preferredProviderID string, fallbacks []string) string {
	activeID = strings.TrimSpace(activeID)
	preferredProviderID = strings.TrimSpace(preferredProviderID)
	if activeID == "" || activeID == preferredProviderID {
		return ""
	}
	for _, id := range fallbacks {
		if id == activeID {
			return activeID
		}
	}
	return ""
}

func apiKeyProviderChain(preferredProviderID string, fallbacks []string) []string {
	preferredProviderID = strings.TrimSpace(preferredProviderID)
	chain := make([]string, 0, 1+len(fallbacks))
	seen := map[string]struct{}{}
	if preferredProviderID != "" {
		chain = append(chain, preferredProviderID)
		seen[preferredProviderID] = struct{}{}
	}
	for _, id := range fallbacks {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		chain = append(chain, id)
	}
	return chain
}

func apiKeyEffectiveProviderIndex(activeID string, chain []string) int {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" {
		return 0
	}
	for i, id := range chain {
		if id == activeID {
			return i
		}
	}
	return 0
}

func (r *Router) DeleteAPIKey(keyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.APIKeys {
		if r.state.APIKeys[index].ID == keyID {
			r.state.APIKeys = append(r.state.APIKeys[:index], r.state.APIKeys[index+1:]...)
			return nil
		}
	}
	return fmt.Errorf("api key %q not found", keyID)
}

// APIKeyByToken returns the enabled API key matching the raw token, if any.
func (r *Router) APIKeyByToken(token string) (domain.APIKey, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.APIKey{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, key := range r.state.APIKeys {
		if key.Enabled && key.Key == token {
			return key, true
		}
	}
	return domain.APIKey{}, false
}

func (r *Router) routeLocked(routeID string) (domain.Route, bool) {
	for _, route := range r.state.Routes {
		if route.ID == routeID {
			return route, true
		}
	}
	return domain.Route{}, false
}

// DeleteProvider soft-deletes a provider: it marks the row Deleted/DeletedAt
// instead of removing it, so an accidental delete can be undone with
// RestoreProvider (config, models, and OAuth credentials are preserved
// on disk). The provider still can't be deleted while a route or API key
// references it — that guard is unrelated to soft-vs-hard delete and stays
// in place so in-use providers are never silently orphaned. Deleting an
// already-deleted provider is reported as not found (it is effectively gone
// from every normal code path already). Use PurgeProvider to actually free
// the row once you're sure the delete wasn't a mistake.
func (r *Router) DeleteProvider(providerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, route := range r.state.Routes {
		if route.ProviderID == providerID {
			return fmt.Errorf("provider %q is used by route %q", providerID, route.Name)
		}
	}
	for _, key := range r.state.APIKeys {
		if route, ok := r.routeLocked(key.RouteID); ok && route.ProviderID == providerID {
			return fmt.Errorf("provider %q is used by api key %q", providerID, key.Name)
		}
		for _, fallbackID := range key.FallbackProviderIDs {
			if fallbackID == providerID {
				return fmt.Errorf("provider %q is used as fallback by api key %q", providerID, key.Name)
			}
		}
		if strings.TrimSpace(key.ActiveProviderID) == providerID {
			return fmt.Errorf("provider %q is the active provider for api key %q", providerID, key.Name)
		}
	}
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		if r.state.Providers[index].Deleted {
			return fmt.Errorf("provider %q not found", providerID)
		}
		r.state.Providers[index].Deleted = true
		r.state.Providers[index].DeletedAt = nowRFC3339()
		r.rebuildModelsLocked()
		return nil
	}
	return fmt.Errorf("provider %q not found", providerID)
}

// DeletedProviders returns soft-deleted providers (the admin "trash" view),
// most recently deleted first. Callers are responsible for redacting secrets
// (see redactProvidersForClient) and for any ownership filtering.
func (r *Router) DeletedProviders() []domain.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.Provider, 0)
	for _, provider := range r.state.Providers {
		if provider.Deleted {
			out = append(out, provider)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt > out[j].DeletedAt })
	return out
}

// RestoreProvider undoes a soft delete (see DeleteProvider), making the
// provider visible/usable again exactly as it was configured before deletion.
func (r *Router) RestoreProvider(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		if !r.state.Providers[index].Deleted {
			return domain.Provider{}, fmt.Errorf("provider %q is not deleted", providerID)
		}
		r.state.Providers[index].Deleted = false
		r.state.Providers[index].DeletedAt = ""
		r.rebuildModelsLocked()
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// PurgeProvider permanently removes a previously soft-deleted provider. This
// is the only way a provider row actually goes away; providers must already
// be soft-deleted (via DeleteProvider) before they can be purged, which keeps
// permanent removal a deliberate two-step action.
func (r *Router) PurgeProvider(providerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		if !r.state.Providers[index].Deleted {
			return fmt.Errorf("provider %q is not deleted; delete it first", providerID)
		}
		r.state.Providers = append(r.state.Providers[:index], r.state.Providers[index+1:]...)
		r.rebuildModelsLocked()
		return nil
	}
	return fmt.Errorf("provider %q not found", providerID)
}

// SetProviderClaudeOAuth stores (or replaces) the Claude OAuth credential for
// a provider and forces it into claude_oauth auth mode.
func (r *Router) SetProviderClaudeOAuth(providerID string, credential domain.ClaudeOAuthCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.AuthType = domain.AuthTypeClaudeOAuth
		credentialCopy := credential
		updated.ClaudeOAuth = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderClaudeOAuth removes the stored Claude OAuth credential (logout)
// while leaving the provider in claude_oauth auth mode so the user can
// reconnect without re-configuring the provider.
func (r *Router) ClearProviderClaudeOAuth(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.ClaudeOAuth = nil
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderCursorOAuth stores (or replaces) the Cursor OAuth credential for
// a provider and forces it into cursor_oauth auth mode.
func (r *Router) SetProviderCursorOAuth(providerID string, credential domain.CursorOAuthCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.AuthType = domain.AuthTypeCursorOAuth
		credentialCopy := credential
		updated.CursorOAuth = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderCursorOAuth removes the stored Cursor OAuth credential (logout)
// while leaving the provider in cursor_oauth auth mode so the user can
// reconnect without re-configuring the provider.
func (r *Router) ClearProviderCursorOAuth(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.CursorOAuth = nil
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderChatGPTOAuth stores (or replaces) the ChatGPT OAuth credential for
// a provider and forces it into chatgpt_oauth auth mode.
func (r *Router) SetProviderChatGPTOAuth(providerID string, credential domain.ChatGPTOAuthCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.AuthType = domain.AuthTypeChatGPTOAuth
		credentialCopy := credential
		updated.ChatGPTOAuth = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderChatGPTOAuth removes the stored ChatGPT OAuth credential (logout)
// while leaving the provider in chatgpt_oauth auth mode so the user can
// reconnect without re-configuring the provider.
func (r *Router) ClearProviderChatGPTOAuth(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.ChatGPTOAuth = nil
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ProviderByID returns a provider by ID, treating soft-deleted providers as
// not found: this is the single choke point that makes a deleted provider
// invisible to every admin action (edit/test/OAuth/self-register/failover
// probes) without needing a Deleted check at each call site. Use
// ProviderByIDIncludingDeleted for the restore/purge/trash-list paths that
// must still be able to see deleted providers.
func (r *Router) ProviderByID(providerID string) (domain.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providerLocked(providerID)
	if !ok || provider.Deleted {
		return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
	}
	return provider, nil
}

// ProviderByIDIncludingDeleted is like ProviderByID but also returns
// soft-deleted providers, for the restore/purge/trash-list admin paths.
func (r *Router) ProviderByIDIncludingDeleted(providerID string) (domain.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providerLocked(providerID)
	if !ok {
		return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
	}
	return provider, nil
}

// TouchAPIKeyLastUsed updates the in-memory LastUsedAt for an API key so the
// console sees exact usage times; SQLite persistence is throttled separately
// by apiKeyToucher. No state save is triggered here.
func (r *Router) TouchAPIKeyLastUsed(keyID, ts string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.APIKeys {
		if r.state.APIKeys[index].ID == keyID {
			r.state.APIKeys[index].LastUsedAt = ts
			return
		}
	}
}

// SetProviderSelfRegistrationToken stores a freshly generated self-register
// token's hash/preview for providerID, replacing any previous token (old
// tokens stop working immediately — there is only ever one live token).
func (r *Router) SetProviderSelfRegistrationToken(providerID string, reg domain.ProviderSelfRegistration) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		regCopy := reg
		r.state.Providers[index].SelfRegistration = &regCopy
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// RevokeProviderSelfRegistration clears self-registration for providerID;
// any previously issued token stops working immediately.
func (r *Router) RevokeProviderSelfRegistration(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].SelfRegistration = nil
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ProviderSelfRegistrationTokenHash returns the stored token hash for
// providerID ("" if self-registration is not set up), for bearer-token
// verification without exposing the full provider record.
func (r *Router) ProviderSelfRegistrationTokenHash(providerID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, provider := range r.state.Providers {
		if provider.ID == providerID {
			if provider.SelfRegistration == nil {
				return ""
			}
			return provider.SelfRegistration.TokenHash
		}
	}
	return ""
}

// SelfRegisterProvider applies a partial connection-info update from the
// provider's own self-register script: each field is updated only when the
// caller explicitly supplied it (nil pointer = leave unchanged), unlike the
// generic UpdateProvider patch. BaseURL/APIKeySource/Protocol/AuthHeader and
// the self-registration LastSeenAt heartbeat are the only fields touched —
// deliberately the smallest mutation surface for a bearer-token authenticated,
// non-console caller. Protocol lets the self-hosted service declare what API
// shape it actually implements (openai_chat/openai_responses/claude), fixing
// mismatches without requiring a console edit; the handler validates the
// value and derives a sensible AuthHeader default before calling this.
func (r *Router) SelfRegisterProvider(providerID string, baseURL, apiKeySource, protocol, authHeader *string, seenAt string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		if baseURL != nil {
			r.state.Providers[index].BaseURL = strings.TrimSpace(*baseURL)
		}
		if apiKeySource != nil {
			r.state.Providers[index].APIKeySource = strings.TrimSpace(*apiKeySource)
		}
		if protocol != nil {
			r.state.Providers[index].Protocol = domain.Protocol(strings.TrimSpace(*protocol))
		}
		if authHeader != nil {
			r.state.Providers[index].AuthHeader = strings.TrimSpace(*authHeader)
		}
		if r.state.Providers[index].SelfRegistration != nil {
			r.state.Providers[index].SelfRegistration.LastSeenAt = seenAt
		}
		normalizeProvider(&r.state.Providers[index])
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderDisabled flips the admin-only enable/disable switch for a
// provider. Disabled providers stay fully manageable by admins but become
// invisible and unusable for normal users.
func (r *Router) SetProviderDisabled(providerID string, disabled bool) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].Disabled = disabled
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

func (r *Router) UpdateProviderModels(providerID string, models []domain.Model, healthStatus string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		for modelIndex := range models {
			models[modelIndex].ProviderID = providerID
			models[modelIndex].Protocol = r.state.Providers[index].Protocol
			models[modelIndex].InMenu = true
			fillModelTokenBudgets(&models[modelIndex])
		}
		r.state.Providers[index].Models = models
		if strings.TrimSpace(healthStatus) != "" {
			r.state.Providers[index].HealthStatus = strings.TrimSpace(healthStatus)
		}
		r.rebuildModelsLocked()
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

func (r *Router) RouteByID(routeID string) (domain.Route, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, route := range r.state.Routes {
		if route.ID == routeID {
			return route, nil
		}
	}
	return domain.Route{}, fmt.Errorf("route %q not found", routeID)
}

func (r *Router) Decide(routeID string) (domain.RouteDecision, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var route *domain.Route
	for i := range r.state.Routes {
		if r.state.Routes[i].ID == routeID {
			route = &r.state.Routes[i]
			break
		}
	}
	if route == nil {
		return domain.RouteDecision{}, fmt.Errorf("route %q not found", routeID)
	}
	return r.decideLocked(*route, route.ProviderID)
}

// DecideForProvider builds a route decision using an overridden input provider
// (API key failover / sticky active provider).
func (r *Router) DecideForProvider(routeID, providerID string) (domain.RouteDecision, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var route *domain.Route
	for i := range r.state.Routes {
		if r.state.Routes[i].ID == routeID {
			route = &r.state.Routes[i]
			break
		}
	}
	if route == nil {
		return domain.RouteDecision{}, fmt.Errorf("route %q not found", routeID)
	}
	return r.decideLocked(*route, providerID)
}

func (r *Router) decideLocked(route domain.Route, providerID string) (domain.RouteDecision, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		providerID = route.ProviderID
	}
	provider, ok := r.providerLocked(providerID)
	if !ok {
		return domain.RouteDecision{}, fmt.Errorf("provider %q not found", providerID)
	}

	action := "convert"
	if route.Mode == domain.RouteModePassThrough || (route.Mode == domain.RouteModeAuto && route.OutputProtocol == provider.Protocol) {
		action = "pass_through"
	}
	if route.Mode == domain.RouteModeConvert {
		action = "convert"
	}

	return domain.RouteDecision{
		RouteID:         route.ID,
		ProviderID:      provider.ID,
		OutputProtocol:  route.OutputProtocol,
		InputProtocol:   provider.Protocol,
		Mode:            route.Mode,
		Action:          action,
		ConversionLabel: fmt.Sprintf("%s -> %s", provider.Protocol.DisplayName(), route.OutputProtocol.DisplayName()),
	}, nil
}

func (r *Router) ActiveOpenAIChatRoute() (domain.Route, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var fallback *domain.Route
	for index := range r.state.Routes {
		route := &r.state.Routes[index]
		if !route.Enabled || route.OutputProtocol != domain.ProtocolOpenAIChat {
			continue
		}
		if fallback == nil {
			fallback = route
		}
		provider, ok := r.providerLocked(route.ProviderID)
		if ok && provider.Protocol == domain.ProtocolOpenAIChat {
			return *route, nil
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return domain.Route{}, fmt.Errorf("no active OpenAI Chat route")
}

func (r *Router) ProviderForRoute(route domain.Route) (domain.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providerLocked(route.ProviderID)
	if !ok {
		return domain.Provider{}, fmt.Errorf("provider %q not found", route.ProviderID)
	}
	return provider, nil
}

// ResolveModel picks the effective model for a request. A fixed API-key model
// replacement wins outright and ignores the request body model; otherwise the
// request model is used, falling back to the route's provider default model.
func (r *Router) ResolveModel(route domain.Route, override string, requestModel string) string {
	requestModel = resolveClaudeModelAlias(requestModel)
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if requestModel != "" {
		return requestModel
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providerLocked(route.ProviderID)
	if ok && strings.TrimSpace(provider.DefaultModel) != "" {
		return strings.TrimSpace(provider.DefaultModel)
	}
	return ""
}

// ResolveThinkingDepth picks the effective reasoning effort. An API-key
// override wins outright; otherwise the request value is used when present,
// falling back to the route's provider default thinking depth.
func (r *Router) ResolveThinkingDepth(route domain.Route, override string, requestHasDepth bool, requestDepth string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if requestHasDepth {
		return strings.TrimSpace(requestDepth)
	}
	r.mu.RLock()
	provider, ok := r.providerLocked(route.ProviderID)
	r.mu.RUnlock()
	if ok {
		return strings.TrimSpace(provider.DefaultThinkingDepth)
	}
	return ""
}

func (r *Router) providerLocked(providerID string) (domain.Provider, bool) {
	for _, provider := range r.state.Providers {
		if provider.ID == providerID {
			return provider, true
		}
	}
	return domain.Provider{}, false
}

func (r *Router) endpointByProtocolLocked(protocol domain.Protocol) (domain.OutputEndpoint, bool) {
	for _, endpoint := range r.state.Endpoints {
		if endpoint.Protocol == protocol {
			return endpoint, true
		}
	}
	return domain.OutputEndpoint{}, false
}

func (r *Router) rebuildModelsLocked() {
	rebuildModels(&r.state)
}

func rebuildModels(state *domain.GatewayState) {
	models := make([]domain.Model, 0)
	for pIndex := range state.Providers {
		// Soft-deleted providers keep their Models slice on disk (so a
		// restore brings everything back), but their models must not appear
		// in the routable/selectable model list while deleted.
		if state.Providers[pIndex].Deleted {
			continue
		}
		for mIndex := range state.Providers[pIndex].Models {
			fillModelTokenBudgets(&state.Providers[pIndex].Models[mIndex])
			models = append(models, state.Providers[pIndex].Models[mIndex])
		}
	}
	state.Models = models
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "item"
	}
	return result
}

func generateAPIKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-gw-" + hex.EncodeToString(buf), nil
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func uniqueID(base string, exists func(string) bool) string {
	if !exists(base) {
		return base
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", base, index)
		if !exists(candidate) {
			return candidate
		}
	}
}
