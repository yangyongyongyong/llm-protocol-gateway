package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

func (r *Router) SetLog2xxBodies(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Log2xxBodies = enabled
}

// SetPeerRegistrationAllowCIDRs 设置对端注册的来源白名单。空串 = 没有白名单
// （任何来源都能注册）。
func (r *Router) SetPeerRegistrationAllowCIDRs(raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.PeerRegistrationAllowCIDRs = strings.TrimSpace(raw)
}

// PeerRegistrationAllowCIDRs 返回当前白名单。Go 不允许同一个类型上出现与
// 字段同名的 Getter，所以字段是 PeerRegistrationAllowCIDRs、方法加前缀区分。
func (r *Router) PeerRegistrationAllowCIDRs() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return strings.TrimSpace(r.state.PeerRegistrationAllowCIDRs)
}

// SetMaxGatewayHops 设置链路跳数上限（1..8）。0 恢复默认 3。
func (r *Router) SetMaxGatewayHops(hops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.MaxGatewayHops = clampMaxGatewayHops(hops)
}

// MaxGatewayHops 返回当前跳数上限。配置缺失（0）时用默认值。
func (r *Router) MaxGatewayHops() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clampMaxGatewayHops(r.state.MaxGatewayHops)
}

// clampMaxGatewayHops 把配置收进合法区间。0 表示"没配过"而不是"0 跳"：
// 没有哪个部署真的想要 0 跳（那会拒掉一切透传），所以它回到默认值。
// 上限 8 是刻意的：与旧版共用常量上限之外，更大只是给配置错误留出更长的
// 等待窗口，每跳都占着两边的连接。
func clampMaxGatewayHops(hops int) int {
	if hops <= 0 {
		return maxGatewayHops
	}
	if hops > maxGatewayHopsCeiling {
		return maxGatewayHopsCeiling
	}
	return hops
}

func (r *Router) SetUsageBatchConfig(maxSize, maxWaitSeconds int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.UsageBatchMaxSize = maxSize
	r.state.UsageBatchMaxWaitSeconds = maxWaitSeconds
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

// validPublicHost reports whether value is a bare DNS hostname the operator may
// bind to this gateway. The role of a request is decided purely by its Host, so
// anything that is not a plain hostname — a path, a port, a wildcard, an IP or
// a scheme-in-disguise — would make that decision ambiguous.
func validPublicHost(value string) bool {
	if value == "" {
		return true // not bound
	}
	if len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	// 至少两级：单标签主机名（localhost 之类）没有可绑定的公网域名语义。
	return strings.Contains(value, ".")
}

// PublicHostError reports why a public-access hostname cannot be bound, or ""
// when both are acceptable. The two hosts must also differ: one hostname cannot
// play both the API and the UI role.
func PublicHostError(apiHost, uiHost string) string {
	apiHost = cleanPublicHost(strings.ToLower(apiHost))
	uiHost = cleanPublicHost(strings.ToLower(uiHost))
	if !validPublicHost(apiHost) {
		return fmt.Sprintf("API 域名 %q 不是合法的主机名（不能带协议、路径、端口或通配符）", apiHost)
	}
	if !validPublicHost(uiHost) {
		return fmt.Sprintf("UI 域名 %q 不是合法的主机名（不能带协议、路径、端口或通配符）", uiHost)
	}
	if apiHost != "" && apiHost == uiHost {
		return "API 域名与 UI 域名不能相同：同一个主机名无法同时扮演两个角色"
	}
	return ""
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

// ProviderGroups 返回管理员侧 Provider 分组的快照，按 position 排列。
func (r *Router) ProviderGroups() []domain.ProviderGroup {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]domain.ProviderGroup(nil), r.state.ProviderGroups...)
}

// ProviderGroupByID 按 ID 查找 Provider 分组。
func (r *Router) ProviderGroupByID(groupID string) (domain.ProviderGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	groupID = strings.TrimSpace(groupID)
	for _, group := range r.state.ProviderGroups {
		if group.ID == groupID {
			return group, nil
		}
	}
	return domain.ProviderGroup{}, fmt.Errorf("provider group %q not found", groupID)
}

// AddProviderGroup 创建管理员分组。名称去首尾空白后必须非空且大小写不敏感唯一；
// ID 从名称派生，后续重命名不会改变已有 Provider 的 groupId 引用。
func (r *Router) AddProviderGroup(name string) (domain.ProviderGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ProviderGroup{}, fmt.Errorf("provider group name is required")
	}
	for _, group := range r.state.ProviderGroups {
		if strings.EqualFold(group.Name, name) {
			return domain.ProviderGroup{}, fmt.Errorf("provider group %q already exists", name)
		}
	}
	group := domain.ProviderGroup{
		ID: uniqueID(slug(name), func(id string) bool {
			for _, existing := range r.state.ProviderGroups {
				if existing.ID == id {
					return true
				}
			}
			return false
		}),
		Name:     name,
		Position: len(r.state.ProviderGroups),
	}
	r.state.ProviderGroups = append(r.state.ProviderGroups, group)
	return group, nil
}

// UpdateProviderGroup 重命名管理员分组；ID 与 Provider 归属保持不变。
func (r *Router) UpdateProviderGroup(groupID, name string) (domain.ProviderGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groupID = strings.TrimSpace(groupID)
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ProviderGroup{}, fmt.Errorf("provider group name is required")
	}
	index := -1
	for i := range r.state.ProviderGroups {
		if r.state.ProviderGroups[i].ID == groupID {
			index = i
			continue
		}
		if strings.EqualFold(r.state.ProviderGroups[i].Name, name) {
			return domain.ProviderGroup{}, fmt.Errorf("provider group %q already exists", name)
		}
	}
	if index < 0 {
		return domain.ProviderGroup{}, fmt.Errorf("provider group %q not found", groupID)
	}
	r.state.ProviderGroups[index].Name = name
	return r.state.ProviderGroups[index], nil
}

// DeleteProviderGroup 删除管理员分组，并把组内 Provider 重置为未分组。
// 该操作不删除 Provider，也不改变任何用户授权。
func (r *Router) DeleteProviderGroup(groupID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groupID = strings.TrimSpace(groupID)
	index := -1
	for i := range r.state.ProviderGroups {
		if r.state.ProviderGroups[i].ID == groupID {
			index = i
			break
		}
	}
	if index < 0 {
		return 0, fmt.Errorf("provider group %q not found", groupID)
	}
	r.state.ProviderGroups = append(r.state.ProviderGroups[:index], r.state.ProviderGroups[index+1:]...)
	for i := range r.state.ProviderGroups {
		r.state.ProviderGroups[i].Position = i
	}
	moved := 0
	for i := range r.state.Providers {
		if strings.TrimSpace(r.state.Providers[i].GroupID) == groupID {
			r.state.Providers[i].GroupID = ""
			moved++
		}
	}
	return moved, nil
}

// ReorderProviderGroups 按调用方给出的完整 ID 顺序重排分组；未列出的旧分组
// 追加到末尾，既容忍前端并发删除，也不会意外丢掉分组。
func (r *Router) ReorderProviderGroups(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := make(map[string]domain.ProviderGroup, len(r.state.ProviderGroups))
	for _, group := range r.state.ProviderGroups {
		byID[group.ID] = group
	}
	ordered := make([]domain.ProviderGroup, 0, len(r.state.ProviderGroups))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		group, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, group)
	}
	for _, group := range r.state.ProviderGroups {
		if !seen[group.ID] {
			ordered = append(ordered, group)
		}
	}
	for i := range ordered {
		ordered[i].Position = i
	}
	r.state.ProviderGroups = ordered
}

// AssignProvidersToGroup 批量设置 Provider 的管理员分组。groupID 为空表示移出分组；
// 该方法只修改 GroupID 元数据，不触碰路由、密钥、授权或 Provider 配置。
func (r *Router) AssignProvidersToGroup(providerIDs []string, groupID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	groupID = strings.TrimSpace(groupID)
	if groupID != "" {
		found := false
		for _, group := range r.state.ProviderGroups {
			if group.ID == groupID {
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("provider group %q not found", groupID)
		}
	}
	wanted := make(map[string]bool, len(providerIDs))
	for _, id := range providerIDs {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	updated := 0
	for i := range r.state.Providers {
		if wanted[r.state.Providers[i].ID] {
			r.state.Providers[i].GroupID = groupID
			updated++
		}
	}
	return updated, nil
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
		provider.AuthType != domain.AuthTypeChatGPTOAuth &&
		!isQoderAuthType(provider.AuthType) &&
		provider.AuthType != domain.AuthTypeOpenCodeZenFree &&
		!isZCodeAuthType(provider.AuthType) &&
		!isTraeAuthType(provider.AuthType) &&
		!isWorkBuddyAuthType(provider.AuthType) &&
		// A self-register provider is created empty on purpose: its owner's
		// script declares baseUrl (and protocol) through the self-register
		// endpoint right after the console mints the token.
		provider.AuthType != domain.AuthTypeSelfRegister {
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
		if strings.TrimSpace(patch.PeerRefreshToken) != "" {
			updated.PeerRefreshToken = strings.TrimSpace(patch.PeerRefreshToken)
		}
		updated.DefaultModel = strings.TrimSpace(patch.DefaultModel)
		updated.DefaultThinkingDepth = strings.TrimSpace(patch.DefaultThinkingDepth)
		if strings.TrimSpace(patch.AuthHeader) != "" {
			updated.AuthHeader = strings.TrimSpace(patch.AuthHeader)
		}
		if patch.Models != nil {
			updated.Models = carryModelContextOverrides(patch.Models, updated.Models)
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
		// RTKCompress is intentionally not merged here: this is a partial
		// update path (trae/zcode refresh only Models), and a bool cannot
		// distinguish "false" from "not sent". The switch has its own endpoint.
		// FirstPacketTimeoutSec follows the same rule for the same reason.
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
	// A mistyped first-packet limit must not park a request for hours; the
	// request path clamps too, so a legacy row keeps the same bound.
	if provider.FirstPacketTimeoutSec < 0 {
		provider.FirstPacketTimeoutSec = 0
	}
	if provider.FirstPacketTimeoutSec > maxFirstPacketTimeoutSec {
		provider.FirstPacketTimeoutSec = maxFirstPacketTimeoutSec
	}
	if provider.MaxConcurrentRequests < 0 {
		provider.MaxConcurrentRequests = 0
	}
	if provider.MaxConcurrentRequests > providerConcurrencyCap {
		provider.MaxConcurrentRequests = providerConcurrencyCap
	}
	// A WorkBuddy account's own client sends one request at a time, so the
	// gateway gives every account a small in-flight cap by default: an agent
	// that fans out would otherwise put a burst on one account that no real
	// client ever produces. This lives in normalizeProvider rather than in
	// AddProvider so accounts loaded from the database get it too — an existing
	// account otherwise keeps 0 (= unlimited) forever.
	if isWorkBuddyAuthType(provider.AuthType) &&
		provider.MaxConcurrentRequests == 0 && provider.RequestsPerMinute == 0 {
		provider.MaxConcurrentRequests = workBuddyDefaultConcurrency(provider.AuthType)
		if provider.QueueWaitSeconds == 0 {
			provider.QueueWaitSeconds = workBuddyDefaultQueueWaitSeconds
		}
	}
	if provider.QueueWaitSeconds < 0 {
		provider.QueueWaitSeconds = 0
	}
	if provider.QueueWaitSeconds > int(providerQueueWaitCap/time.Second) {
		provider.QueueWaitSeconds = int(providerQueueWaitCap / time.Second)
	}
	if provider.RequestsPerMinute < 0 {
		provider.RequestsPerMinute = 0
	}
	if provider.RequestsPerMinute > providerRequestsPerMinuteCap {
		provider.RequestsPerMinute = providerRequestsPerMinuteCap
	}
	if provider.MaxConcurrentRequests == 0 && provider.RequestsPerMinute == 0 {
		provider.QueueWaitSeconds = 0
	}
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
	if isWorkBuddyAuthType(provider.AuthType) {
		// Both WorkBuddy builds serve the plan on an OpenAI-Chat compatible
		// endpoint. Like Qoder and unlike the clauses above, the base URL is
		// only defaulted, so an endpoint move is a config edit, not a rebuild.
		provider.Protocol = domain.ProtocolOpenAIChat
		site, _ := workBuddySiteForAuthType(provider.AuthType)
		if isPlaceholderBaseURL(provider.BaseURL) {
			provider.BaseURL = site.chatBaseURL()
		}
	}
	if provider.AuthType == domain.AuthTypeOpenCodeZenFree {
		// OpenCode Zen speaks OpenAI Chat natively and needs no key for its
		// free models, so the only thing to configure is which of its two
		// roots to ask; the free tier is moved to the inference root per
		// request (see resolveOpenCodeZenURL).
		provider.Protocol = domain.ProtocolOpenAIChat
		if isPlaceholderBaseURL(provider.BaseURL) {
			provider.BaseURL = openCodeZenFreeBase
		}
	}
	if isZCodeAuthType(provider.AuthType) {
		// A ZCode account's key bills on its realm's Anthropic endpoint, which
		// is the whole of what forwarding needs to know. The base URL is only
		// defaulted — a realm moving its path is a config edit — but nothing
		// else is: the key never comes from the form, only from the sign-in.
		provider.Protocol = domain.ProtocolClaude
		if isPlaceholderBaseURL(provider.BaseURL) {
			if site, ok := zcodeSiteForAuthType(provider.AuthType); ok {
				provider.BaseURL = site.inference
			}
		}
	}
	if isTraeAuthType(provider.AuthType) {
		// Trae's IDE protocol is its own: one agent endpoint per realm, and a
		// request shaped for it rather than for OpenAI Chat. The protocol is
		// still declared as OpenAI Chat because that is what clients see; only
		// the hop upstream is Trae's (see resolveProviderChatURL).
		provider.Protocol = domain.ProtocolOpenAIChat
		if isPlaceholderBaseURL(provider.BaseURL) {
			if site, ok := traeSiteForAuthType(provider.AuthType); ok {
				provider.BaseURL = site.api
			}
		}
	}
	if isQoderAuthType(provider.AuthType) {
		// Qoder serves its models on the API its own client talks to, and a
		// request there is shaped for it rather than for OpenAI Chat. The
		// protocol is still declared as OpenAI Chat because that is what
		// clients see; only the hop upstream is Qoder's (see sendQoderChat).
		// The base URL is defaulted rather than pinned, so a host change is a
		// config edit; the create form's example.com placeholder counts as unset.
		provider.Protocol = domain.ProtocolOpenAIChat
		if isPlaceholderBaseURL(provider.BaseURL) {
			if site, ok := qoderSiteForAuthType(provider.AuthType); ok {
				provider.BaseURL = site.API
			}
		}
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
	key.FirstPacketTimeoutSec = clampFirstPacketTimeoutSec(key.FirstPacketTimeoutSec)
	key.SameProviderRetries = clampSameProviderRetries(key.SameProviderRetries)
	preferredProviderID := ""
	if route, ok := r.routeLocked(key.RouteID); ok {
		preferredProviderID = route.ProviderID
	}
	key.FallbackProviderIDs = normalizeFallbackProviderIDs(key.FallbackProviderIDs, "")
	key.FallbackModelOverrides = normalizeFallbackModelOverrides(key.FallbackProviderIDs, key.FallbackModelOverrides)
	if err := r.validateFallbackModelOverridesLocked(preferredProviderID, key.FallbackProviderIDs, key.FallbackModelOverrides); err != nil {
		return domain.APIKey{}, err
	}
	key.ActiveProviderID = strings.TrimSpace(key.ActiveProviderID)
	key.OwnerUserID = strings.TrimSpace(key.OwnerUserID)
	mode, err := normalizeScheduleMode(key.ScheduleMode)
	if err != nil {
		return domain.APIKey{}, err
	}
	key.ScheduleMode = mode
	quota, quotaTokens, quotaCost, err := normalizeKeyQuota(key.QuotaWindow, key.QuotaTokens, key.QuotaCostMicros)
	if err != nil {
		return domain.APIKey{}, err
	}
	key.QuotaWindow = string(quota)
	key.QuotaTokens = quotaTokens
	key.QuotaCostMicros = quotaCost
	key.Enabled = true
	if key.CreatedAt == "" {
		key.CreatedAt = nowRFC3339()
	}
	// StreamEnabled default is handled by the HTTP handler (absent field => true);
	// the value passed here is authoritative.

	r.state.APIKeys = append(r.state.APIKeys, key)
	return key, nil
}

// ReplacePeerKey 用一个对端的全部旧凭据换成新的一把。
//
// 换发（重装后重新注册）和吊销后重建在语义上是同一件事，共用这一条路径。
// 整个过程在写锁内完成：请求线程看到的要么是旧凭据、要么是新凭据，绝不会
// 出现"旧的已删、新的还没插上"的空档，否则下游会在那一刻被拒。
//
// 只动 kind=peer 账号名下的密钥；名字里带 peerTokenPrefix 但归属本地用户的
// 行不会被碰——那种行只可能来自手工干预，留给管理员处理而不是悄悄删掉。
func (r *Router) ReplacePeerKey(key domain.APIKey) error {
	return r.ReplacePeerKeys(key)
}

// ReplacePeerKeys 同时换发一组对端凭据（流量 bearer + 续签凭据）。两类
// 前缀都在这里清掉，避免重注册后旧续签凭据继续有效。
func (r *Router) ReplacePeerKeys(keys ...domain.APIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(keys) == 0 {
		return fmt.Errorf("peer credentials are required")
	}
	ownerID := strings.TrimSpace(keys[0].OwnerUserID)
	if ownerID == "" {
		return fmt.Errorf("peer key must have an owner")
	}
	kept := make([]domain.APIKey, 0, len(r.state.APIKeys)+len(keys))
	for _, existing := range r.state.APIKeys {
		if strings.TrimSpace(existing.OwnerUserID) == ownerID && isPeerCredential(existing.Key) {
			continue
		}
		kept = append(kept, existing)
	}
	for index := range keys {
		key := keys[index]
		if strings.TrimSpace(key.OwnerUserID) != ownerID {
			return fmt.Errorf("peer credentials must all belong to the same owner")
		}
		key.Name = strings.TrimSpace(key.Name)
		if key.Name == "" {
			key.Name = "网关"
		}
		// 对端凭据不绑 route：走哪个 provider 由上游按模型和授权集决定，不是
		// 一把密钥对应一条固定路由。
		key.RouteID = ""
		key.Enabled = true
		if key.CreatedAt == "" {
			key.CreatedAt = nowRFC3339()
		}
		keys[index] = key
	}
	r.state.APIKeys = append(kept, keys...)
	return nil
}

// isPeerCredential 判断一把密钥是否属于网关互联的任一凭据类型。
func isPeerCredential(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, peerTokenPrefix) || strings.HasPrefix(raw, peerRefreshTokenPrefix)
}

// RemovePeerKeys 删掉一个对端名下的全部对端凭据。吊销用它，返回删掉的条数。
//
// 只删带 peerTokenPrefix 的行：同一个 owner 名下如果还有手工建的普通密钥，
// 那不属于对端这一侧，吊销网关不该顺手把它们删掉。
func (r *Router) RemovePeerKeys(ownerUserID string) error {
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return fmt.Errorf("owner is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]domain.APIKey, 0, len(r.state.APIKeys))
	for _, key := range r.state.APIKeys {
		if strings.TrimSpace(key.OwnerUserID) == ownerUserID && isPeerCredential(key.Key) {
			continue
		}
		kept = append(kept, key)
	}
	r.state.APIKeys = kept
	return nil
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
		previousPreferredProviderID := ""
		if route, ok := r.routeLocked(updated.RouteID); ok {
			previousPreferredProviderID = strings.TrimSpace(route.ProviderID)
		}
		if strings.TrimSpace(patch.Name) != "" {
			updated.Name = strings.TrimSpace(patch.Name)
		}
		if routeID := strings.TrimSpace(patch.RouteID); routeID != "" {
			// 只在「真的改绑到另一条 route」时校验存在性。前端提交方案快照时会把
			// key 当前的 routeId 一起回传，若那条 route 已被删除（provider 被
			// purge 后 route 随之消失），为一条没改动的字段报 route not found 会
			// 把整个保存动作挡死——用户连备选都存不进去，也就无法再用「设为首选」
			// 重建 route 把密钥救回来。
			if routeID != strings.TrimSpace(updated.RouteID) {
				if _, ok := r.routeLocked(routeID); !ok {
					return domain.APIKey{}, fmt.Errorf("route %q not found", routeID)
				}
				updated.RouteID = routeID
			}
		}
		updated.ModelOverride = strings.TrimSpace(patch.ModelOverride)
		if patch.ModelAliases != nil {
			updated.ModelAliases = normalizeModelAliases(patch.ModelAliases)
		}
		updated.ThinkingDepthOverride = strings.TrimSpace(patch.ThinkingDepthOverride)
		updated.MaxOutputTokens = normalizeMaxOutputTokens(patch.MaxOutputTokens)
		updated.FirstPacketTimeoutSec = clampFirstPacketTimeoutSec(patch.FirstPacketTimeoutSec)
		updated.SameProviderRetries = clampSameProviderRetries(patch.SameProviderRetries)
		updated.Enabled = patch.Enabled
		updated.StreamEnabled = patch.StreamEnabled
		updated.CodexKeepOfficialLogin = patch.CodexKeepOfficialLogin
		updated.CodexForceFast = patch.CodexForceFast
		mode, err := normalizeScheduleMode(patch.ScheduleMode)
		if err != nil {
			return domain.APIKey{}, err
		}
		updated.ScheduleMode = mode
		quota, quotaTokens, quotaCost, err := normalizeKeyQuota(patch.QuotaWindow, patch.QuotaTokens, patch.QuotaCostMicros)
		if err != nil {
			return domain.APIKey{}, err
		}
		updated.QuotaWindow = string(quota)
		updated.QuotaTokens = quotaTokens
		updated.QuotaCostMicros = quotaCost
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
			if err := r.validateFallbackModelOverridesLocked(preferredProviderID, updated.FallbackProviderIDs, updated.FallbackModelOverrides); err != nil {
				return domain.APIKey{}, err
			}
		} else {
			updated.FallbackProviderIDs = normalizeFallbackProviderIDs(updated.FallbackProviderIDs, preferredProviderID)
			if patch.FallbackModelOverrides != nil {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, patch.FallbackModelOverrides)
				if err := r.validateFallbackModelOverridesLocked(preferredProviderID, updated.FallbackProviderIDs, updated.FallbackModelOverrides); err != nil {
					return domain.APIKey{}, err
				}
			} else {
				updated.FallbackModelOverrides = normalizeFallbackModelOverrides(updated.FallbackProviderIDs, updated.FallbackModelOverrides)
			}
		}
		updated.ActiveProviderID = sanitizeActiveProviderID(updated.ActiveProviderID, preferredProviderID, updated.FallbackProviderIDs)
		// 首选 Provider 真的换了时清掉 Key 级粘滞位。旧值即便还留在备选链里，
		// 也不该继续压过用户刚刚选择的新首选；会话级 pin 由 sessionAffinityKey
		// 的 primary-provider 命名空间一起失效。
		if preferredProviderID != "" && preferredProviderID != previousPreferredProviderID {
			updated.ActiveProviderID = ""
		}
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
		ScheduleMode:           key.ScheduleMode,
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
	if mode, err := normalizeScheduleMode(profile.ScheduleMode); err == nil {
		profile.ScheduleMode = mode
	}
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
	// A profile with a mode the key-level form would have rejected degrades to
	// the default order rather than breaking routing: normalizeProfileLocked is
	// called on paths that cannot return an error.
	if mode, err := normalizeScheduleMode(profile.ScheduleMode); err == nil {
		key.ScheduleMode = mode
	} else {
		key.ScheduleMode = ""
	}
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

// validateFallbackModelOverridesLocked used to reject a cross-protocol backup
// that pinned no model. It no longer does: the backup has a model catalog of
// its own, and resolveConsumerModelOnProvider falls back to the backup's own
// default when the request names a model it does not serve (see
// applyProviderModelFallback). Refusing the save made "设为首选" fail the moment
// the two sides spoke different protocols, which is exactly the case the chain
// exists for. A pinned model is therefore an optimization, not a requirement.
// Caller must hold r.mu.
func (r *Router) validateFallbackModelOverridesLocked(preferredProviderID string, fallbackIDs []string, overrides map[string]string) error {
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
	wanted := sha256.Sum256([]byte(token))
	found := false
	var match domain.APIKey
	r.mu.RLock()
	for _, key := range r.state.APIKeys {
		if !key.Enabled || key.Key == "" {
			continue
		}
		// 续签凭据也是 api_keys 行（为了重启持久化和统一 CRUD），但它绝不
		// 能充当客户端模型密钥；只有 /__peer/renew 的专用校验可以接受它。
		if strings.HasPrefix(key.Key, peerRefreshTokenPrefix) {
			continue
		}
		// 逐一比较全部 Key 的摘要（不提前返回）：比较本身恒定时间，命中的是第几个
		// Key 也不从耗时上泄露出去。
		candidate := sha256.Sum256([]byte(key.Key))
		if subtle.ConstantTimeCompare(candidate[:], wanted[:]) == 1 && !found {
			match, found = key, true
		}
	}
	r.mu.RUnlock()
	return match, found
}

// APIKeyByID looks a key up by its id, disabled ones included: the console patches
// a key it is showing, and a paused key still has to be readable to be edited.
func (r *Router) APIKeyByID(keyID string) (domain.APIKey, bool) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return domain.APIKey{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, key := range r.state.APIKeys {
		if key.ID == keyID {
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
// on disk). Dependents are cleaned up automatically so dead providers can be
// removed in bulk without editing every referencing object first:
//   - API keys: active provider, fallback list, and per-fallback model
//     overrides pointing at this provider are reset to empty;
//   - routes: routes using this provider are deleted, and API keys bound
//     to those routes get RouteID reset to empty.
//
// Deleting an already-deleted provider is reported as not found (it is
// effectively gone from every normal code path already). Use PurgeProvider to
// actually free the row once you're sure the delete wasn't a mistake.
func (r *Router) DeleteProvider(providerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	removedRoutes := make(map[string]bool)
	for _, route := range r.state.Routes {
		if route.ProviderID == providerID {
			removedRoutes[route.ID] = true
		}
	}
	for index := range r.state.APIKeys {
		key := &r.state.APIKeys[index]
		if removedRoutes[key.RouteID] {
			key.RouteID = ""
		}
		if strings.TrimSpace(key.ActiveProviderID) == providerID {
			key.ActiveProviderID = ""
		}
		cleaned := key.FallbackProviderIDs[:0]
		for _, fallbackID := range key.FallbackProviderIDs {
			if fallbackID != providerID {
				cleaned = append(cleaned, fallbackID)
			}
		}
		key.FallbackProviderIDs = cleaned
		if key.FallbackModelOverrides != nil {
			delete(key.FallbackModelOverrides, providerID)
		}
	}
	if len(removedRoutes) > 0 {
		routes := r.state.Routes[:0]
		for _, route := range r.state.Routes {
			if !removedRoutes[route.ID] {
				routes = append(routes, route)
			}
		}
		r.state.Routes = routes
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

// RestoreProviderSnapshot puts a provider removed by PurgeProvider back exactly
// as it was, including its soft-deleted marker, its position, and the models it
// contributed. It exists for the one caller that can fail after the removal: the
// purge handler, when persisting the new state fails. A provider that is still
// present is left alone.
func (r *Router) RestoreProviderSnapshot(provider domain.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, existing := range r.state.Providers {
		if existing.ID == provider.ID {
			return
		}
	}
	r.state.Providers = append(r.state.Providers, provider)
	r.rebuildModelsLocked()
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

// SetProviderQoderPAT stores (or replaces) the Qoder credential pair for a
// provider and forces it into qoder_pat auth mode.
func (r *Router) SetProviderQoderPAT(providerID string, credential domain.QoderCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		if !isQoderAuthType(updated.AuthType) {
			updated.AuthType = domain.AuthTypeQoderPAT
		}
		credentialCopy := credential
		updated.QoderPAT = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// DisconnectProviderQoderPAT pauses a Qoder connection without discarding the
// stored personal access token: the short-lived job token is cleared and the
// credential is marked Disconnected so forwarding stops and the console shows
// it as disconnected, but the PAT itself survives so reconnecting is a single
// click (no trip back to qoder.com/account/integrations for a fresh token).
//
// This replaces the previous behavior of wiping the whole credential on
// disconnect, which forced a full re-paste to reconnect — see
// handleQoderPATDisconnect / handleQoderPATReconnect.
func (r *Router) DisconnectProviderQoderPAT(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		if updated.QoderPAT != nil {
			credential := *updated.QoderPAT
			credential.AccessToken = ""
			credential.ExpiresAt = ""
			credential.Disconnected = true
			updated.QoderPAT = &credential
		}
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderWorkBuddy stores (or replaces) the WorkBuddy session for a
// provider and forces it into that build's auth mode: authType says which one
// (WorkBuddy or WorkBuddy AI), since the endpoint, model list and headers all
// follow from it.
func (r *Router) SetProviderWorkBuddy(providerID, authType string, credential domain.WorkBuddyCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.AuthType = authType
		credentialCopy := credential
		updated.WorkBuddy = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderWorkBuddy discards a WorkBuddy session; the provider keeps its
// name and protocol but has to sign in again before it can serve traffic.
func (r *Router) ClearProviderWorkBuddy(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.WorkBuddy = nil
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderZCode stores (or replaces) the credential a ZCode sign-in minted
// and forces the provider into ZCode's mode: the plan's Anthropic endpoint and
// the key that bills on it come from the credential, not from the form.
func (r *Router) SetProviderZCode(providerID string, credential domain.ZCodeCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		credentialCopy := credential
		updated.ZCode = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderZCode discards a ZCode credential. The provider keeps the key it
// minted only as long as the credential holds it, so clearing means the next
// request has to sign in again.
func (r *Router) ClearProviderZCode(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.ZCode = nil
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderTrae stores (or replaces) a Trae account session and forces the
// provider into that realm's auth mode: which deployment it signs in to is the
// connection kind, because its hosts, client catalog and billing page all
// follow from it.
func (r *Router) SetProviderTrae(providerID, authType string, credential domain.TraeCredential) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.AuthType = authType
		credentialCopy := credential
		updated.Trae = &credentialCopy
		normalizeProvider(&updated)
		r.state.Providers[index] = updated
		return updated, nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// ClearProviderTrae discards a Trae session; the provider keeps its
// configuration but has to sign in again before it serves traffic.
func (r *Router) ClearProviderTrae(providerID string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		updated := r.state.Providers[index]
		updated.Trae = nil
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

// SetProviderRTKCompress flips the provider-level RTK tool-result compression
// switch. Like the disabled switch it has a dedicated endpoint so partial
// provider updates (e.g. refreshing a model list) cannot reset it.
func (r *Router) SetProviderRTKCompress(providerID string, enabled bool) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].RTKCompress = enabled
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderCheckin turns the daily check-in of one subscription on or off. It
// goes through its own endpoint, as RTKCompress does, so a partial provider
// patch saved from the console cannot reset it.
func (r *Router) SetProviderCheckin(providerID string, enabled bool) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].CheckinEnabled = enabled
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderFirstPacketTimeout bounds how long one attempt to this provider
// may wait for the upstream's response headers (0 clears it). Like the RTK and
// check-in switches it has its own setter so a partial provider patch cannot
// reset it — and so that patch semantics stay unusable here: an int cannot tell
// "not sent" from "cleared".
// SetProviderProtocolSupport 记下这台上游实测支持哪些协议（见
// domain.Provider.ProtocolSupport）。探测是后台跑的，所以这条路径要能单独写：
// 走 UpdateProvider 会顺带覆盖凭据等字段，而这里只想改这一个结论。
func (r *Router) SetProviderProtocolSupport(providerID string, protocols []string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		cleaned := make([]string, 0, len(protocols))
		seen := map[string]bool{}
		for _, item := range protocols {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			cleaned = append(cleaned, item)
		}
		r.state.Providers[index].ProtocolSupport = cleaned
		r.state.Providers[index].ProtocolProbedAt = nowRFC3339()
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderFirstPacketTimeout 已知的 per-provider 超时设置函数。
func (r *Router) SetProviderFirstPacketTimeout(providerID string, seconds int) (domain.Provider, error) {
	if seconds < 0 {
		seconds = 0
	}
	if seconds > maxFirstPacketTimeoutSec {
		seconds = maxFirstPacketTimeoutSec
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].FirstPacketTimeoutSec = seconds
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetProviderConcurrency sets this provider's admission lane: at most
// maxConcurrentRequests upstream requests at a time, with over-cap requests
// waiting up to queueWaitSeconds for a slot (0 refuses immediately). 0/0 removes
// the lane, which is every provider's default.
func (r *Router) SetProviderConcurrency(providerID string, maxConcurrentRequests, queueWaitSeconds, requestsPerMinute int, laneGroup string) (domain.Provider, error) {
	laneGroup = strings.TrimSpace(laneGroup)
	if maxConcurrentRequests < 0 {
		maxConcurrentRequests = 0
	}
	if maxConcurrentRequests > providerConcurrencyCap {
		maxConcurrentRequests = providerConcurrencyCap
	}
	if queueWaitSeconds < 0 {
		queueWaitSeconds = 0
	}
	if queueWaitSeconds > int(providerQueueWaitCap/time.Second) {
		queueWaitSeconds = int(providerQueueWaitCap / time.Second)
	}
	if requestsPerMinute < 0 {
		requestsPerMinute = 0
	}
	if requestsPerMinute > providerRequestsPerMinuteCap {
		requestsPerMinute = providerRequestsPerMinuteCap
	}
	// No lane means nothing to queue for: clearing both caps clears the wait
	// too, so a provider never carries a setting that does nothing.
	if maxConcurrentRequests == 0 && requestsPerMinute == 0 {
		queueWaitSeconds = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		r.state.Providers[index].MaxConcurrentRequests = maxConcurrentRequests
		r.state.Providers[index].QueueWaitSeconds = queueWaitSeconds
		r.state.Providers[index].RequestsPerMinute = requestsPerMinute
		r.state.Providers[index].LaneGroup = laneGroup
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// dedupeProviderModels 去掉同一 Provider 下重复的模型 id。
//
// 上游的 /models 经常把同一个模型列好几遍（带不同后缀的副本、分页重叠、同一
// 账号被两条记录共用时两边各列一份），原样落到下拉里就是几十条一模一样的选项，
// 选模型时根本找不到要的那条。保留第一次出现的那条（上游通常把主推模型排在
// 前面），后续重复项直接丢弃；大小写不同但 id 相同的也按重复处理。
func dedupeProviderModels(models []domain.Model) []domain.Model {
	if len(models) < 2 {
		return models
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]domain.Model, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		model.ID = id
		out = append(out, model)
	}
	return out
}

// carryModelContextOverrides 把运维给模型定的上下文窗口带到新的清单上。
//
// 窗口是「运维对这个模型的判断」，不是上游属性：目录同步、刷新授权、重新注册
// 都会整份换掉模型清单，而这些路径都不该把人的话冲掉——冲掉之后那个模型会从
// 「1M」悄悄退回默认值，界面上看不出发生过什么（magpie 的模型规格同样明说
// 「任何一项都不会被目录同步碰到」）。清单里已经没有的模型，它的设置也就随之
// 消失，不另外存档。
//
// 要往上写的时候先复制一份：incoming 可能正是调用方从 state 里取出来的那一片
// （见 peer_upstream.go 拿 provider.Models 回写的路径），就地改会绕过调用方对
// 那份数据的预期。
func carryModelContextOverrides(incoming, existing []domain.Model) []domain.Model {
	if len(incoming) == 0 || len(existing) == 0 {
		return incoming
	}
	overrides := make(map[string]int, len(existing))
	for _, model := range existing {
		if model.ContextOverride > 0 {
			overrides[strings.ToLower(strings.TrimSpace(model.ID))] = model.ContextOverride
		}
	}
	if len(overrides) == 0 {
		return incoming
	}
	out := incoming
	copied := false
	for index := range out {
		if out[index].ContextOverride > 0 {
			continue
		}
		override := overrides[strings.ToLower(strings.TrimSpace(out[index].ID))]
		if override <= 0 {
			continue
		}
		if !copied {
			out = append([]domain.Model(nil), incoming...)
			copied = true
		}
		out[index].ContextOverride = override
	}
	return out
}

// SetProviderModelContext 给某个模型定一个上下文窗口，或（n = 0）退回自动。
//
// 它有自己的入口而不是走 Provider 的增量 PATCH：那里 int 的零值分不清「没传」
// 和「清空」，而清空恰恰是这里最要紧的一件事——设错了窗口会让客户端以为还能
// 塞更多，看不出错在哪。首包超时、并发上限也都是这个理由各走各的。
func (r *Router) SetProviderModelContext(providerID, modelID string, n int) (domain.Provider, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return domain.Provider{}, fmt.Errorf("model id is required")
	}
	if n < 0 || n > maxModelContextOverride {
		return domain.Provider{}, fmt.Errorf("a context window is between 0 and %d tokens, not %d", maxModelContextOverride, n)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		for modelIndex := range r.state.Providers[index].Models {
			if !strings.EqualFold(strings.TrimSpace(r.state.Providers[index].Models[modelIndex].ID), modelID) {
				continue
			}
			model := &r.state.Providers[index].Models[modelIndex]
			// 覆盖值只写进 ContextOverride，ContextLength 留着自动值——清空设置
			// 要能原样退回去（见 fillModelTokenBudgets）。老数据里被写进去过的
			// 顺手校正回来。
			model.ContextOverride = n
			model.ContextLength = resolveModelContextLength(model.ID, model.ContextLength)
			fillModelTokenBudgets(model)
			r.rebuildModelsLocked()
			return r.state.Providers[index], nil
		}
		return domain.Provider{}, fmt.Errorf("provider %q has no model %q", providerID, modelID)
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
		models = dedupeProviderModels(carryModelContextOverrides(models, r.state.Providers[index].Models))
		for modelIndex := range models {
			models[modelIndex].ProviderID = providerID
			models[modelIndex].Protocol = r.state.Providers[index].Protocol
			models[modelIndex].InMenu = true
			fillModelTokenBudgets(&models[modelIndex])
		}
		r.state.Providers[index].Models = models
		// 目录刷新后顺手补齐默认模型：没有它，所有「不指定模型」的路径（对话测试、
		// 模型探测、路由没给模型的兜底）都会拿到 "request-model-not-set" 这个占位
		// 符再发给上游，换回一句莫名其妙的 400。只补空值，运维自己选的绝不覆盖。
		if strings.TrimSpace(r.state.Providers[index].DefaultModel) == "" && len(models) > 0 {
			r.state.Providers[index].DefaultModel = strings.TrimSpace(models[0].ID)
		}
		if strings.TrimSpace(healthStatus) != "" {
			r.state.Providers[index].HealthStatus = strings.TrimSpace(healthStatus)
		}
		r.rebuildModelsLocked()
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetPeerGrants 记下这条链路上游那一侧的授权快照（见 domain.PeerUpstreamGrants）。
//
// 它和 UpdateProviderModels 分开，是因为两者由不同的上游响应驱动、更新的频率也
// 不同：模型清单来自 /__peer/providers，而授权快照还会带上注册响应里的账号信息。
// 合进 UpdateProvider 更不行——那条路径会清掉凭据（见那里的注释）。
func (r *Router) SetPeerGrants(providerID string, grants domain.PeerUpstreamGrants) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for index := range r.state.Providers {
		if r.state.Providers[index].ID != providerID {
			continue
		}
		// 存一份副本：调用方（注册/刷新）常常还留着入参那片切片，
		// 让它之后被改到就等于绕过了这里的锁。快照里还嵌着一层
		// GrantedProviders（每个带自己的模型切片），所以逐层拷。
		snapshot := grants.Clone()
		r.state.Providers[index].PeerGrants = &snapshot
		return r.state.Providers[index], nil
	}
	return domain.Provider{}, fmt.Errorf("provider %q not found", providerID)
}

// SetPeerUpstreamCredentials 在一组 peer provider 上原子替换流量 bearer 与
// 续签凭据。自动续签后所有成员行必须同时换到新凭据：只换一条会让同组其它
// 成员继续拿旧 token 打父网关。
func (r *Router) SetPeerUpstreamCredentials(providerID, token, refreshToken string) (domain.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.state.Providers {
		provider := &r.state.Providers[index]
		if provider.ID != providerID {
			continue
		}
		if provider.AuthType != domain.AuthTypePeer {
			return domain.Provider{}, fmt.Errorf("provider %q is not a peer gateway", providerID)
		}
		provider.APIKeySource = "literal:" + strings.TrimSpace(token)
		provider.PeerRefreshToken = strings.TrimSpace(refreshToken)
		return *provider, nil
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

// ProviderAccessible reports whether a normal user reaches providerID through
// the route: one the admin has not paused, that the user created themselves or
// was granted (granted is the user's AllowedProviderIDs). It answers the same
// question as Server.allowedProviderIDsForUser for a single id, with the grant
// list handed in rather than read from the user store, so a caller that already
// holds it does not fetch it twice.
func (r *Router) ProviderAccessible(providerID, userID string, granted []string) bool {
	providerID = strings.TrimSpace(providerID)
	userID = strings.TrimSpace(userID)
	if providerID == "" || userID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providerLocked(providerID)
	if !ok || provider.Disabled || provider.Deleted {
		return false
	}
	if strings.TrimSpace(provider.OwnerUserID) == userID {
		return true
	}
	for _, id := range granted {
		if strings.TrimSpace(id) == providerID {
			return true
		}
	}
	return false
}

func (r *Router) Decide(routeID string) (domain.RouteDecision, error) {
	return r.DecideForProtocol(routeID, "")
}

// DecideForProtocol 是 Decide 的「按这次请求的协议」版本：auto 模式下用它判定
// 直通还是转换。clientProtocol 为空时行为与 Decide 完全一致。
func (r *Router) DecideForProtocol(routeID string, clientProtocol domain.Protocol) (domain.RouteDecision, error) {
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
	return r.decideLockedForProtocol(*route, route.ProviderID, clientProtocol)
}

// DecideForProvider builds a route decision using an overridden input provider
// (API key failover / sticky active provider).
func (r *Router) DecideForProvider(routeID, providerID string) (domain.RouteDecision, error) {
	return r.DecideForProviderProtocol(routeID, providerID, "")
}

// DecideForProviderProtocol 是 DecideForProvider 的「按这次请求的协议」版本。
// clientProtocol 为空时行为与 DecideForProvider 完全一致。
func (r *Router) DecideForProviderProtocol(routeID, providerID string, clientProtocol domain.Protocol) (domain.RouteDecision, error) {
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
	return r.decideLockedForProtocol(*route, providerID, clientProtocol)
}

func (r *Router) decideLocked(route domain.Route, providerID string) (domain.RouteDecision, error) {
	return r.decideLockedForProtocol(route, providerID, "")
}

// decideLocked 用 auto 模式判定「直通还是转换」。
//
// clientProtocol 是调用方这一次请求真正走的那条端点的协议（/v1/chat/completions
// 是 openai_chat，/anthropic/v1/messages 是 claude，以此类推）。空值表示调用方
// 不知道（老调用点、内部探测），这时只能退回 route.OutputProtocol 那个口径。
//
// 为什么要按请求而不是按 route：一把 Key 本来就该能同时被 Claude Code 和 Codex
// 用——它们是两条不同的端点，客户端自己就分好了协议。以前 auto 模式拿 route 上
// 那个「输出协议」当客户端协议，于是把 route 钉死成一种协议，另一条端点的请求
// 会被直接拒掉（endpoint mismatch）。那不是安全边界，只是一个问错了人的判断。
func (r *Router) decideLockedForProtocol(route domain.Route, providerID string, clientProtocol domain.Protocol) (domain.RouteDecision, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		providerID = route.ProviderID
	}
	provider, ok := r.providerLocked(providerID)
	if !ok {
		return domain.RouteDecision{}, fmt.Errorf("provider %q not found", providerID)
	}

	// 客户端协议：调用方给了就用它，否则退回 route 声明的那个。
	protocol := clientProtocol
	if protocol == "" {
		protocol = route.OutputProtocol
	}
	// 空 Mode 与 auto 同义：AddRoute 会给新路由填 auto，但老数据（以及测试里
	// 手搓的 Route）可能是空串。不把这两者当成一回事，就会把一条本该直通的
	// 请求硬塞进转换器表，然后回一句「转换未实现」。
	mode := route.Mode
	if mode == "" {
		mode = domain.RouteModeAuto
	}
	action := "convert"
	if mode == domain.RouteModePassThrough || (mode == domain.RouteModeAuto && protocol == provider.Protocol) {
		action = "pass_through"
	}
	if mode == domain.RouteModeConvert {
		action = "convert"
	}

	return domain.RouteDecision{
		RouteID:         route.ID,
		ProviderID:      provider.ID,
		OutputProtocol:  protocol,
		InputProtocol:   provider.Protocol,
		Mode:            mode,
		Action:          action,
		ConversionLabel: fmt.Sprintf("%s -> %s", provider.Protocol.DisplayName(), protocol.DisplayName()),
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

// providerIsSelfRegistered 回答「这台上游是不是用户自建、只按约定透传的服务」。
//
// 它决定了网关要不要对请求自作主张：自助注册的上游既不在网关的模型目录里，也不
// 是 Anthropic —— 按模型名猜 max_tokens、把 budget_tokens 反推成 effort，都是拿
// Anthropic 的口径去改一个我们并不认识的实现，对方只认自己客户端那套字段时就会
// 400。这类 Provider 一律原样透传。providerID 为空（故障转移落到别的上游之前的
// 空档）按 false 处理，走原来的逻辑。
func (r *Router) providerIsSelfRegistered(providerID string) bool {
	if providerID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providerLocked(providerID)
	return ok && provider.AuthType == domain.AuthTypeSelfRegister
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
