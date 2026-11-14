package domain

import (
	"encoding/json"
	"strings"
)

type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolClaude          Protocol = "claude"
	// ProtocolGemini is Google's native generateContent API: a model endpoint of
	// its own, with its own request and response shape.
	ProtocolGemini Protocol = "gemini"
)

func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolOpenAIChat:
		return "OpenAI Chat"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses"
	case ProtocolClaude:
		return "Claude"
	case ProtocolGemini:
		return "Gemini"
	default:
		return string(p)
	}
}

type PublicAccessMode string

const (
	PublicAccessModeRandomTunnel PublicAccessMode = "random_tunnel"
	PublicAccessModeCustomDomain PublicAccessMode = "custom_domain"
	// PublicAccessModeExternalDNS means the hostnames already resolve through
	// DNS/TLS/reverse-proxy infrastructure the user manages elsewhere. The
	// gateway records the hostnames for Host separation and client URLs, but
	// never starts or manages a tunnel and never writes DNS records.
	PublicAccessModeExternalDNS PublicAccessMode = "external_dns"
)

type PublicAccessSettings struct {
	Enabled  bool             `json:"enabled"`
	Provider string           `json:"provider"`
	Mode     PublicAccessMode `json:"mode"`
	// ExposeAPI controls whether the model-API custom hostname is published
	// on the Cloudflare tunnel. Independent from ExposeUI.
	ExposeAPI bool `json:"exposeApi"`
	// ExposeUI controls whether the management-UI custom hostname is published
	// on the Cloudflare tunnel. Independent from ExposeAPI.
	ExposeUI bool `json:"exposeUi"`
	// CustomDomain is the public hostname for model API traffic only
	// (e.g. gateway.example.com). Must differ from UIDomain when both are set.
	CustomDomain string `json:"customDomain,omitempty"`
	// UIDomain is the public hostname for the management UI only
	// (e.g. console.example.com). Must differ from CustomDomain when both are set.
	UIDomain   string `json:"uiDomain,omitempty"`
	Expose     string `json:"expose"`
	RuntimeURL string `json:"runtimeUrl,omitempty"`
	// Named-tunnel fields for custom-domain mode (cloudflared). These are
	// persisted so the setup can be reused, but full named-tunnel automation is
	// stubbed in this build (see internal/tunnel).
	TunnelName       string `json:"tunnelName,omitempty"`
	TunnelToken      string `json:"tunnelToken,omitempty"`
	CredentialsFile  string `json:"credentialsFile,omitempty"`
	TunnelConfigFile string `json:"tunnelConfigFile,omitempty"`
	PublicBaseURL    string `json:"publicBaseUrl,omitempty"`
	// UIPublicBaseURL is the browser management URL for custom-domain mode.
	UIPublicBaseURL string `json:"uiPublicBaseUrl,omitempty"`
	Status          string `json:"status"`
	StatusMessage   string `json:"statusMessage"`
	// Tunnel is the live cloudflared process state; runtime-only, not persisted.
	Tunnel *TunnelRuntime `json:"tunnel,omitempty"`
}

// AlertRuleAPIKeyMultiIP is the rule id for "one API key used from too many
// distinct client IPs inside a sliding window" — the key-leak signal.
const AlertRuleAPIKeyMultiIP = "apikey_multi_ip"

// AlertRuleAPIKeyConcurrentIP is the rule id for "one API key had requests from
// several distinct IPs in flight at the same instant". Stronger than the
// multi-IP rule: switching networks yields sequential requests, never
// overlapping ones, so this has almost no false positives.
const AlertRuleAPIKeyConcurrentIP = "apikey_concurrent_ip"

// Alert status values. Alerts start unread; the console can mark them read or
// ignored (ignored means "reviewed, not a leak" — it is never auto-set).
const (
	AlertStatusUnread  = "unread"
	AlertStatusRead    = "read"
	AlertStatusIgnored = "ignored"
)

// Alert push states. Disabled means Telegram was off when the alert fired, so
// it is not a delivery failure and must not be surfaced as one.
const (
	AlertPushSent     = "sent"
	AlertPushFailed   = "failed"
	AlertPushDisabled = "disabled"
)

// AlertSettings holds alerting configuration. It is persisted as a single JSON
// value under the "alerts" settings key rather than on GatewayState, so the
// Telegram bot token can never ride along with GET /__state.
type AlertSettings struct {
	MultiIPEnabled       bool `json:"multiIpEnabled"`
	MultiIPWindowMinutes int  `json:"multiIpWindowMinutes"`
	MultiIPThreshold     int  `json:"multiIpThreshold"`
	// ConcurrentIP* configures the overlap rule: distinct IPs with requests in
	// flight simultaneously. Its window only bounds how far back to look for
	// overlaps; the detection itself is instant-based, not window-based.
	ConcurrentIPEnabled       bool             `json:"concurrentIpEnabled"`
	ConcurrentIPWindowMinutes int              `json:"concurrentIpWindowMinutes"`
	ConcurrentIPThreshold     int              `json:"concurrentIpThreshold"`
	CooldownMinutes           int              `json:"cooldownMinutes"`
	Telegram                  TelegramSettings `json:"telegram"`
}

// TelegramSettings configures the Telegram bot used for alert push.
type TelegramSettings struct {
	Enabled bool `json:"enabled"`
	// BotToken is a credential: never return it to the browser. See
	// redactAlertSettingsForClient in internal/gateway/alerts.go.
	BotToken string `json:"botToken,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
}

// Alert-setting bounds. A zero or out-of-range value would otherwise degrade
// the scan into alerting on every pass, so Normalize clamps into these.
const (
	alertMultiIPWindowMinutesDefault = 10
	alertMultiIPWindowMinutesMin     = 1
	alertMultiIPWindowMinutesMax     = 1440
	// 5 rather than 3: real traffic shows legitimate clients (e.g. Cursor's
	// server-side egress pool) reaching 3 distinct IPs inside a 10-minute
	// window, which would false-positive at a lower threshold.
	alertMultiIPThresholdDefault = 5
	alertMultiIPThresholdMin     = 2
	// The overlap rule looks back far enough to catch a recent burst; the
	// detection itself is instant-based so this only bounds the scan range.
	alertConcurrentIPWindowMinutesDefault = 10
	// "同一时刻超过三个 IP" → more than 3, i.e. 4. Real traffic confirms the
	// boundary: Cursor's AWS egress pool genuinely runs 3 IPs concurrently
	// (~13-15s per request), so a threshold of 3 would false-positive.
	alertConcurrentIPThresholdDefault = 4
	alertConcurrentIPThresholdMin     = 2
	alertCooldownMinutesDefault       = 60
	alertCooldownMinutesMin           = 1
)

func DefaultAlertSettings() AlertSettings {
	return AlertSettings{
		MultiIPEnabled:            true,
		MultiIPWindowMinutes:      alertMultiIPWindowMinutesDefault,
		MultiIPThreshold:          alertMultiIPThresholdDefault,
		ConcurrentIPEnabled:       true,
		ConcurrentIPWindowMinutes: alertConcurrentIPWindowMinutesDefault,
		ConcurrentIPThreshold:     alertConcurrentIPThresholdDefault,
		CooldownMinutes:           alertCooldownMinutesDefault,
	}
}

// Normalize fills zero values with defaults and clamps out-of-range numbers.
func (s *AlertSettings) Normalize() {
	if s.MultiIPWindowMinutes <= 0 {
		s.MultiIPWindowMinutes = alertMultiIPWindowMinutesDefault
	}
	if s.MultiIPWindowMinutes < alertMultiIPWindowMinutesMin {
		s.MultiIPWindowMinutes = alertMultiIPWindowMinutesMin
	}
	if s.MultiIPWindowMinutes > alertMultiIPWindowMinutesMax {
		s.MultiIPWindowMinutes = alertMultiIPWindowMinutesMax
	}
	if s.MultiIPThreshold <= 0 {
		s.MultiIPThreshold = alertMultiIPThresholdDefault
	}
	if s.MultiIPThreshold < alertMultiIPThresholdMin {
		s.MultiIPThreshold = alertMultiIPThresholdMin
	}
	if s.ConcurrentIPWindowMinutes <= 0 {
		s.ConcurrentIPWindowMinutes = alertConcurrentIPWindowMinutesDefault
	}
	if s.ConcurrentIPWindowMinutes < alertMultiIPWindowMinutesMin {
		s.ConcurrentIPWindowMinutes = alertMultiIPWindowMinutesMin
	}
	if s.ConcurrentIPWindowMinutes > alertMultiIPWindowMinutesMax {
		s.ConcurrentIPWindowMinutes = alertMultiIPWindowMinutesMax
	}
	if s.ConcurrentIPThreshold <= 0 {
		s.ConcurrentIPThreshold = alertConcurrentIPThresholdDefault
	}
	if s.ConcurrentIPThreshold < alertConcurrentIPThresholdMin {
		s.ConcurrentIPThreshold = alertConcurrentIPThresholdMin
	}
	if s.CooldownMinutes <= 0 {
		s.CooldownMinutes = alertCooldownMinutesDefault
	}
	if s.CooldownMinutes < alertCooldownMinutesMin {
		s.CooldownMinutes = alertCooldownMinutesMin
	}
	s.Telegram.BotToken = strings.TrimSpace(s.Telegram.BotToken)
	s.Telegram.ChatID = strings.TrimSpace(s.Telegram.ChatID)
}

// TunnelRuntime mirrors the tunnel manager's live state for status responses.
// It is populated at runtime and never persisted to disk.
type TunnelRuntime struct {
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	PublicURL string `json:"publicUrl"`
	// UIPublicURL is set in custom-domain mode when management UI uses a
	// separate hostname from the model API.
	UIPublicURL string `json:"uiPublicUrl,omitempty"`
	Message     string `json:"message"`
	StartedAt   string `json:"startedAt,omitempty"`
	PID         int    `json:"pid,omitempty"`
}

// ProviderGroup 是管理员侧的 Provider 归类维度，不参与任何权限判定。
// 普通用户看不到分组，管理员想把 Provider 授权给用户仍然走用户管理里的
// user_resource_grants 直接授权。
type ProviderGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type Provider struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Protocol             Protocol `json:"protocol"`
	BaseURL              string   `json:"baseUrl"`
	APIKeySource         string   `json:"apiKeySource"`
	DefaultModel         string   `json:"defaultModel"`
	DefaultThinkingDepth string   `json:"defaultThinkingDepth,omitempty"`
	Models               []Model  `json:"models"`
	HealthStatus         string   `json:"healthStatus"`
	// NextRetryAt is a runtime-only hint (RFC3339) shown by the console when
	// HealthStatus == "unavailable": a live upstream request just failed
	// against this provider, and the background recovery loop
	// (see gateway.StartProviderFailoverRecovery) will re-probe it at this
	// time. Computed at response time in handleState; never persisted.
	NextRetryAt string `json:"nextRetryAt,omitempty"`
	// ModelAvailability is a runtime-only overlay for models that are
	// temporarily limited on this Provider. It is populated by handleState and
	// never persisted; HealthStatus remains the account-wide state.
	ModelAvailability []ProviderModelAvailability `json:"modelAvailability,omitempty"`
	// OwnerUserID is the console user that created this provider. Empty means
	// admin-created. A normal user owns the providers they create and may
	// edit/clone/delete/test them; admins have full access to every provider.
	OwnerUserID string `json:"ownerUserId,omitempty"`
	// GroupID is the administrator-only Provider 分组。它只用于控制台归类、
	// 筛选与批量操作，不参与可见性、路由或 API Key 授权；空值表示未分组。
	GroupID string `json:"groupId,omitempty"`
	// Disabled is the admin-only kill switch. Zero value (false) means enabled,
	// so newly created providers and legacy rows/exports default to enabled.
	// While disabled, normal users cannot see, bind, or send traffic through
	// this provider; admins keep full access.
	Disabled bool `json:"disabled,omitempty"`
	// RTKCompress enables gateway-side compression of tool results (command
	// output) before forwarding. It is toggled through the dedicated
	// POST /__providers/{id}/rtk endpoint so partial provider patches cannot
	// reset it. Requires the rtk CLI on the gateway host.
	RTKCompress bool `json:"rtkCompress,omitempty"`
	// CheckinEnabled asks this subscription's daily check-in on the loop, once
	// a Beijing day. Off by default: pressing 签到 spends an account's daily
	// grant, so it is never enabled behind the user's back, and it is toggled
	// through POST /__providers/{id}/checkin/auto for the same reason RTKCompress
	// is. Only a provider whose vendor runs such an event
	// (gateway.checkinVendorOf) may turn it on.
	CheckinEnabled bool `json:"checkinEnabled,omitempty"`
	// Deleted marks a provider as soft-deleted (see Router.DeleteProvider).
	// Soft-deleted providers are hidden from normal listings/routing and can't
	// be bound to new routes or API keys, but their configuration/secrets are
	// preserved on disk so an accidental delete can be undone (RestoreProvider)
	// instead of losing OAuth credentials, request adapters, etc. Only a
	// separate, explicit purge (Router.PurgeProvider) removes the row for real.
	Deleted bool `json:"deleted,omitempty"`
	// DeletedAt is the RFC3339 timestamp of the soft delete; empty when not deleted.
	DeletedAt     string `json:"deletedAt,omitempty"`
	AuthHeader    string `json:"authHeader"`
	ExtraEndpoint string `json:"extraEndpoint,omitempty"`
	// AuthType selects the provider's authentication mode. "" and "api_key"
	// both mean today's default APIKeySource-driven auth; "claude_oauth" means
	// the provider authenticates via a Claude.ai OAuth token pair instead.
	AuthType string `json:"authType,omitempty"`
	// ClaudeOAuth holds the OAuth token pair for AuthType == "claude_oauth"
	// providers. Nil/omitted when not in OAuth mode. These are secrets: never
	// send this struct's raw values to the frontend (see redactProvidersForClient).
	ClaudeOAuth *ClaudeOAuthCredential `json:"claudeOAuth,omitempty"`
	// CursorOAuth holds the OAuth token pair for AuthType == "cursor_oauth"
	// providers. Nil/omitted when not in OAuth mode.
	CursorOAuth *CursorOAuthCredential `json:"cursorOAuth,omitempty"`
	// ChatGPTOAuth holds the ChatGPT/Codex CLI OAuth token pair for
	// AuthType == "chatgpt_oauth" providers (sub2api prior art).
	ChatGPTOAuth *ChatGPTOAuthCredential `json:"chatgptOAuth,omitempty"`
	// QoderPAT holds the Qoder account credential — a browser sign-in's token
	// pair, or a personal access token plus the job token exchanged from it —
	// for AuthType == "qoder_pat" (qoder.com) and "qoder_cn" (qoder.cn).
	QoderPAT *QoderCredential `json:"qoderPat,omitempty"`
	// WorkBuddy holds the WorkBuddy subscription token pair for
	// AuthType == "workbuddy" (Tencent build) and "workbuddy_ai"
	// (international build) providers. Nil/omitted when not signed in.
	WorkBuddy *WorkBuddyCredential `json:"workbuddyOAuth,omitempty"`
	// ZCode holds the GLM Coding Plan credential minted through ZCode's own
	// sign-in, for AuthType == "zcode" providers. Nil/omitted when not signed
	// in. Secrets never reach the frontend (see redactProviderForClient).
	ZCode *ZCodeCredential `json:"zcodeOAuth,omitempty"`
	// Trae holds the ByteDance Trae account session for AuthType ==
	// "trae_cn" / "trae_global" providers. Nil/omitted when not signed in.
	Trae *TraeCredential `json:"traeOAuth,omitempty"`
	// RequestAdapter is an optional provider-level request rewrite template
	// (URL/headers/body/model mapping). Nil means use built-in protocol logic.
	RequestAdapter *RequestAdapter `json:"requestAdapter,omitempty"`
	// CodingPlanProvider explicitly tags providers whose coding-plan quota API
	// can't be disambiguated by BaseURL alone. Currently only "zhipu_team":
	// its quota endpoint shares open.bigmodel.cn with personal Zhipu and is
	// distinguished by the ?type=2 query + org/project headers.
	CodingPlanProvider string `json:"codingPlanProvider,omitempty"`
	// TeamOrganizationID / TeamProjectID are the Zhipu team-plan (bigmodel)
	// quota-query headers (bigmodel-organization / bigmodel-project). These are
	// account identifiers, not secrets, so they may be sent to the frontend.
	TeamOrganizationID string `json:"teamOrganizationId,omitempty"`
	TeamProjectID      string `json:"teamProjectId,omitempty"`
	// SelfRegistration lets the provider owner's own automation script keep
	// BaseURL/APIKeySource in sync (e.g. a self-hosted backend behind a
	// rotating reverse-tunnel URL) by calling
	// PATCH /__providers/{id}/self-register with a provider-scoped bearer
	// token, instead of a console session login. Nil means self-registration
	// has not been set up for this provider.
	SelfRegistration *ProviderSelfRegistration `json:"selfRegistration,omitempty"`
	// ProxyURL is an optional HTTP/HTTPS/SOCKS5 proxy for this provider's
	// upstream requests. Format: "http://host:port", "https://host:port",
	// "socks5://user:pass@host:port". Empty means no proxy (direct connection).
	// This allows each provider to use a dedicated IP address, avoiding
	// account association issues when managing multiple Claude/OpenAI accounts.
	ProxyURL string `json:"proxyUrl,omitempty"`
	// FirstPacketTimeoutSec bounds how long one attempt to this provider may
	// wait for the upstream's response headers before the gateway abandons it
	// and moves to the next provider in the key's chain. 0 (the default) means
	// use the gateway's 600s header wait. A positive value overrides that for
	// this provider. The timer covers only the wait for headers: once they
	// arrive it is stopped, so a long reasoning stream or a slow body is never
	// cut short.
	FirstPacketTimeoutSec int `json:"firstPacketTimeoutSec,omitempty"`
	// ProtocolSupport 是这台上游「实测支持哪些线协议」的结论，由创建/变更
	// baseUrl 之后的后台探测写下（见 gateway.detectProviderProtocols）。
	//
	// 为什么要存下来：一个上游（OpenCode Go 是典型）可能同时在 /v1 和
	// /anthropic/v1 提供两套协议。知道这件事之后，网关才能在链路里优先挑与
	// 下游协议相同的那个 provider —— 直通只是搬字节，转换才是容易出问题的那层。
	// 探测是网络动作，不能每次渲染控制台都跑一遍，所以结论落库。
	//
	// 空 = 还没探过（老记录、或探测失败）。绝不把「没探过」当成「什么都不支持」。
	ProtocolSupport []string `json:"protocolSupport,omitempty"`
	// ProtocolProbedAt 是上次探测的时间（RFC3339），空 = 从未探过。
	ProtocolProbedAt string `json:"protocolProbedAt,omitempty"`
	// MaxConcurrentRequests caps how many upstream requests this provider serves
	// at the same time; 0 (the default) means no cap. It is admission control,
	// not a fairness promise: a burst is refused by the gateway with 429 before
	// it can make the vendor answer 429 and get the account cooled down as if it
	// had broken.
	MaxConcurrentRequests int `json:"maxConcurrentRequests,omitempty"`
	// QueueWaitSeconds is how long a request over the cap may wait in line for a
	// slot before it is refused. 0 refuses immediately, which is the default:
	// waiting is only worth it for clients that already retry slowly on their own.
	QueueWaitSeconds int `json:"queueWaitSeconds,omitempty"`
	// RequestsPerMinute caps how many requests this provider sends in any rolling
	// minute, counted when admitted. Subscription accounts are sold with a
	// request budget, not a concurrency one; without this the gateway only learns
	// the budget is gone from a 429 the vendor already counted against the
	// account. 0 = no rate limit.
	RequestsPerMinute int `json:"requestsPerMinute,omitempty"`
	// LaneGroup names the account the three limits above belong to, so several
	// Provider rows that are really one subscription (different keys of the same
	// account, or one account with several base URLs) share a single budget
	// instead of each getting the full one. Empty means the provider is its own
	// account, which is how every existing provider behaves. The group is
	// matched exactly (case-insensitively) and does not need to name an existing
	// provider: grouping providers that are not otherwise related is allowed.
	LaneGroup string `json:"laneGroup,omitempty"`
	// PeerGrants 是这条链路在上游那一侧的授权快照，只对 AuthType == "peer"
	// 的 provider 有意义（见 domain.PeerUpstreamGrants）。它只用来显示：
	// 真实鉴权永远由上游按当下授权决定，本机从不拿这份快照放行任何请求。
	PeerGrants *PeerUpstreamGrants `json:"peerGrants,omitempty"`
	// PeerRefreshToken 是网关互联的续签凭据，只对 AuthType == "peer" 有意义。
	// 它不能用于模型流量，只允许调用父网关 /__peer/renew 换取新的对端 bearer。
	// 这是秘密：json:"-" 保证它不会进入控制台、导出包或 Provider API 响应。
	PeerRefreshToken string `json:"-"`
}

// ProviderModelAvailability is the console-facing view of one exact
// Provider+Model cooldown. It intentionally carries no account state and never
// includes credentials.
type ProviderModelAvailability struct {
	Model       string `json:"model"`
	Reason      string `json:"reason,omitempty"`
	NextRetryAt string `json:"nextRetryAt,omitempty"`
}

// ProviderSelfRegistration is the server-side state backing the self-register
// bearer-token flow. TokenHash is never sent to the client (json:"-"); the
// raw token is shown to the console user exactly once, at generation time.
type ProviderSelfRegistration struct {
	TokenHash string `json:"-"`
	// TokenPreview is a short, non-secret suffix (last 4 chars of the raw
	// token) so the console can show "which token" without re-exposing it.
	TokenPreview string `json:"tokenPreview,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
	// LastSeenAt is informational only (last successful self-register call);
	// availability/异常 status is driven purely by real proxied-request
	// failures (see gateway.shouldFailoverProvider), not by heartbeat gaps.
	LastSeenAt string `json:"lastSeenAt,omitempty"`
}

// PeerUpstreamGrants 是本机注册到上游网关后，上游那一侧告诉我们的授权状态。
//
// 它必须落库，而不是每次现取：界面上「已连接的上游」那一行要显示上游当前
// 授权了几个 provider，而 /__peer-upstreams 是个纯读接口——每渲染一次就去
// 打一遍上游，上游慢一点整个控制台就跟着卡，上游挂了更是连自己已经存在哪
// 都看不见。所以注册/刷新/测试连接拿到清单时顺手存下来，列表只读这份快照。
//
// 快照会和上游真实授权渐渐不一致（管理员改了授权但本机没再刷新），所以它
// 只用于显示，不做任何鉴权判断：每一次真实透传仍由上游按当下授权决定。
type PeerUpstreamGrants struct {
	// Username 是上游为这台机器建的账号名（带 peer- 前缀）。排查时能直接
	// 对上上游用户管理页里的那一行。
	Username string `json:"username,omitempty"`
	// UserID 是同一行在上游的账号 ID。
	UserID string `json:"userId,omitempty"`
	// ParentGatewayID 是上游网关自己的身份；注册响应里回显的是"上游记录的
	// 身份"（也就是我们），所以这里存的是谁在为我们服务。
	ParentGatewayID string `json:"parentGatewayId,omitempty"`
	// GrantedProviderIDs 是上游最近一次告诉我们的授权清单。为空是真实状态
	// （上游一个都没授权），不是"未知"——未知由 SyncedAt 为空表示。
	GrantedProviderIDs []string `json:"grantedProviderIds"`
	// GrantedProviders 是同一份清单的详细版：每个被授权 provider 在上游的
	// 名字、协议和模型。GrantedProviderIDs 留着是因为老库里只有它，而它足够
	// 回答"授权了几个"；详细版让子网关能把上游的 provider 原样摊开显示。
	GrantedProviders []PeerGrantedProvider `json:"grantedProviders,omitempty"`
	// UpstreamKey 把同一次注册落下来的若干条 provider 归到一个上游下面。
	//
	// 一个上游授权了 N 个 provider，子网关就落 N 条 provider——每条一个协议、
	// 一份模型清单，和本地 provider 长得一模一样，也才能被单独选进备选。
	// 它们共享同一把对端凭据和同一个上游地址，界面靠这个 key 把它们收成一组。
	// 它在注册时生成一次，之后改名、刷新、重连都不变，否则分组会在改名时裂开。
	UpstreamKey string `json:"upstreamKey,omitempty"`
	// UpstreamName 是这条上游在子网关这边的显示名（注册时填的，或从主机名取）。
	// 组标题和"属于哪个上游"都用它。
	UpstreamName string `json:"upstreamName,omitempty"`
	// GrantedProviderID 是这条 provider 对应的上游授权 provider。空 = 整条
	// 上游链路（升级前注册的老记录，它自己一行为一整条链路）。非空时透传请求
	// 会把这个 id 带上，上游据此把请求路由到这个被授权的 provider，而不是在
	// 授权集里自己挑一个——这正是"备选里能单独选一个上游 provider"能成立的
	// 前提。
	GrantedProviderID string `json:"grantedProviderId,omitempty"`
	// SyncedAt 是这份快照是什么时候取的（RFC3339）。空 = 从未同步过，
	// 界面据此区分"确实授权了 0 个"和"还没问过上游"。
	SyncedAt string `json:"syncedAt,omitempty"`
	// Card 是上游这一条 Provider 的卡片展示元数据与最近一次额度快照。
	// 只含可安全展示的字段，不含 base URL、API key、OAuth token 等秘密。
	Card *PeerProviderCard `json:"card,omitempty"`
}

// PeerGrantedProvider 是上游授权清单里的一项，原样照抄上游的 /__peer/providers。
//
// 上游只下发 id、名字、协议和模型，这正是子网关把这条 provider"像本地 provider
// 一样显示"所需要的全部；地址和凭据永远不下发，也永远不该出现在这里。
type PeerGrantedProvider struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Models   []string `json:"models,omitempty"`
	// Card is the same safe presentation snapshot as the member provider uses.
	Card *PeerProviderCard `json:"card,omitempty"`
}

// PeerProviderCard 是沿网关互联链路透传的 Provider 卡片展示信息。
// 它只描述“长什么样、当前额度大约是多少”，绝不携带连接凭据或真实端点。
type PeerProviderCard struct {
	AuthType     string             `json:"authType,omitempty"`
	Vendor       string             `json:"vendor,omitempty"`
	AccountLabel string             `json:"accountLabel,omitempty"`
	Connected    bool               `json:"connected,omitempty"`
	Usage        *PeerProviderUsage `json:"usage,omitempty"`
	// TodayTokens 是父网关今天在这个 Provider 上的全量 Token（输入 + 输出），
	// 包含父端所有用户与下游流量。指针用于区分“上游没下发/旧版未知”(nil) 和
	// “上游明确统计为 0”；子网关绝不拿本机转发量冒充这个总量。
	TodayTokens *int64 `json:"todayTokens,omitempty"`
}

// PeerProviderUsage 是上游额度面板的只读投影。子网关只展示，不负责刷新；
// 数据由父网关已有的额度缓存/后台采集更新。
type PeerProviderUsage struct {
	Title     string                    `json:"title,omitempty"`
	Plan      string                    `json:"plan,omitempty"`
	Message   string                    `json:"message,omitempty"`
	Balance   string                    `json:"balance,omitempty"`
	FetchedAt string                    `json:"fetchedAt,omitempty"`
	Buckets   []PeerProviderUsageBucket `json:"buckets,omitempty"`
}

// PeerProviderUsageBucket 是卡片上的一个额度窗口。
type PeerProviderUsageBucket struct {
	Label       string  `json:"label"`
	Utilization float64 `json:"utilization"`
	Note        string  `json:"note,omitempty"`
	ResetsAt    string  `json:"resetsAt,omitempty"`
}

// Clone 深拷贝卡片中的额度桶，避免父子两侧共享底层切片。
func (c PeerProviderCard) Clone() PeerProviderCard {
	out := c
	if c.Usage != nil {
		usage := *c.Usage
		usage.Buckets = append([]PeerProviderUsageBucket(nil), c.Usage.Buckets...)
		out.Usage = &usage
	}
	if c.TodayTokens != nil {
		tokens := *c.TodayTokens
		out.TodayTokens = &tokens
	}
	return out
}

// Clone 深拷贝一份，包括切片的元素。
//
// router 存快照时要脱离调用方手里的那片内存；嵌套切片漏拷一层，调用方之后改
// 自己那份就等于绕过了 router 的锁改到了"已经存好"的状态。
func (g PeerUpstreamGrants) Clone() PeerUpstreamGrants {
	out := g
	out.GrantedProviderIDs = append([]string(nil), g.GrantedProviderIDs...)
	if g.GrantedProviders != nil {
		out.GrantedProviders = make([]PeerGrantedProvider, len(g.GrantedProviders))
		for index, provider := range g.GrantedProviders {
			out.GrantedProviders[index] = provider
			out.GrantedProviders[index].Models = append([]string(nil), provider.Models...)
			if provider.Card != nil {
				card := provider.Card.Clone()
				out.GrantedProviders[index].Card = &card
			}
		}
	}
	if g.Card != nil {
		card := g.Card.Clone()
		out.Card = &card
	}
	return out
}

// RequestAdapter customizes how gateway requests are sent to an upstream
// provider. Templates support {model} and {baseUrl} placeholders.
type RequestAdapter struct {
	URLTemplate  string            `json:"urlTemplate,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate string            `json:"bodyTemplate,omitempty"`
	ModelMapping map[string]string `json:"modelMapping,omitempty"`
	// CurlExample is generated server-side for UI display; not required on save.
	CurlExample string `json:"curlExample,omitempty"`
}

// ClaudeOAuthCredential holds the Claude.ai OAuth token pair for a
// claude_oauth provider. AccessToken/RefreshToken are secrets and must never
// be sent to the frontend (see gateway.redactProviderForClient).
type ClaudeOAuthCredential struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	Scope        string `json:"scope,omitempty"`
	AccountLabel string `json:"accountLabel,omitempty"` // e.g. email/subscription tier if available
	// Connected is a computed, display-only flag: false on the real
	// internally-held credential (it is derived, not stored), true/false only
	// on client-facing redacted copies to indicate an active connection.
	Connected bool `json:"connected,omitempty"`
}

// CursorOAuthCredential holds the Cursor subscription OAuth token pair.
type CursorOAuthCredential struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	AccountLabel string `json:"accountLabel,omitempty"`
	Connected    bool   `json:"connected,omitempty"`
}

// QoderCredential is a connected Qoder account. Qoder is signed in two ways —
// the browser device flow its own client uses, or a long-lived personal access
// token pasted by the user — and both end up holding the short-lived job token
// that signs inference calls, so the same struct serves either.
//
// Field names mirror CursorOAuthCredential so the existing sqlite oauth_*
// columns are reused with no migration; the whole struct also rides in
// oauth_json, which is authoritative on load:
//
//	AccessToken = the job token (jt-…) or, on a device-chat Qoder CN account,
//	              the device token that signs the model calls
//	RefreshToken = that token's single-use refresh token
//	PAT          = the personal access token (pt-…) for a token-connected account
//
// AccessToken, RefreshToken, PAT and DeviceToken are secrets and must never
// reach the frontend (see gateway.redactProviderForClient). The field name
// Provider.QoderPAT and its "qoderPat" JSON key predate the device flow and
// are kept stable so exported providers still import.
type QoderCredential struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	AccountLabel string `json:"accountLabel,omitempty"`
	Connected    bool   `json:"connected,omitempty"`
	// Disconnected marks a user-initiated pause (the "断开连接" button): the
	// credential itself is kept, so reconnecting costs no browser trip and no
	// fresh token. Forwarding must still refuse to use it while this is true.
	Disconnected bool `json:"disconnected,omitempty"`
	// HasStoredToken is computed at redaction time, never itself persisted:
	// it lets the console tell "paused, still known" apart from "never
	// configured" without any token reaching the browser.
	HasStoredToken bool `json:"hasStoredToken,omitempty"`

	// SignInMode is "device" for a browser sign-in or "pat" for a pasted
	// personal access token; empty is a credential written before the browser
	// flow existed, which is read as "pat".
	SignInMode string `json:"signInMode,omitempty"`
	// PAT is the long-lived personal access token the job token was exchanged
	// from. Qoder never rotates it, so it is carried forward on every
	// exchange and is what re-mints a job token when the refresh pair lapses.
	PAT string `json:"pat,omitempty"`
	// UID, Email and Name are the account Qoder's /api/v1/userinfo reports
	// for it — the label the console shows and the uid the COSY envelope
	// signs with.
	UID   string `json:"uid,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	// DeviceToken authorizes the account endpoints (usage, check-in) and, on a
	// device-chat account, the model calls themselves. Qoder records no expiry
	// for it, so it is rotated when Qoder refuses it.
	DeviceToken   string `json:"deviceToken,omitempty"`
	DeviceRefresh string `json:"deviceRefresh,omitempty"`
	// MachineID is generated once per sign-in and reused by every request:
	// Qoder's envelope carries it as the machine identity, and its client
	// rejects an account whose machine keeps changing.
	MachineID string `json:"machineId,omitempty"`
	// UserType, OrganizationID and OrganizationName are the plan identity
	// /api/v1/userinfo reports. They travel inside the COSY identity blob, and
	// the vendor filters both the campaign list and the model list by what that
	// blob claims: an enterprise account whose organization is missing there is
	// answered as if it were a personal one.
	UserType         string `json:"userType,omitempty"`
	OrganizationID   string `json:"organizationId,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`
	// DeviceChat says AccessToken is the device token rather than a job token,
	// because the site refused to trade it (Qoder CN's CLI works this way). It
	// is then refreshed as a device token.
	DeviceChat bool `json:"deviceChat,omitempty"`
	// Models is the account's own model listing, kept verbatim because a
	// request must carry each model's upstream configuration with it.
	Models json.RawMessage `json:"models,omitempty"`
}

// QoderDisconnectedScopeMarker is stashed in the shared oauth_scope column
// (otherwise unused by Qoder) to persist QoderPATCredential.Disconnected across
// restarts without a schema migration. Referenced from both internal/gateway
// (write) and internal/store (read/write), hence living in domain.
const QoderDisconnectedScopeMarker = "disconnected"

// ChatGPTOAuthCredential holds a ChatGPT (Codex CLI) OAuth token pair used to
// call chatgpt.com/backend-api/codex/responses. Secrets must never reach the
// frontend (see gateway.redactProviderForClient).
type ChatGPTOAuthCredential struct {
	AccessToken      string `json:"accessToken,omitempty"`
	RefreshToken     string `json:"refreshToken,omitempty"`
	ExpiresAt        string `json:"expiresAt,omitempty"`    // RFC3339
	AccountLabel     string `json:"accountLabel,omitempty"` // email / plan
	ChatGPTAccountID string `json:"chatgptAccountId,omitempty"`
	Connected        bool   `json:"connected,omitempty"`
}

// WorkBuddyCredential holds a WorkBuddy subscription session. WorkBuddy (and
// its international build, WorkBuddy AI) serves the Tencent CodeBuddy plan on
// an OpenAI-compatible endpoint, where a request carries the account's JWT
// access token plus the account id and domain in headers. AccessToken and
// RefreshToken are secrets and must never reach the frontend (see
// gateway.redactProviderForClient).
type WorkBuddyCredential struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	AccountLabel string `json:"accountLabel,omitempty"`
	// UserID is the plan account's id, sent as X-User-Id on every request.
	// It is an account identifier rather than a secret, so it may be shown to
	// the console.
	UserID string `json:"userId,omitempty"`
	// Domain is the account's auth domain, sent as X-Domain; empty means
	// derive it from the endpoint host.
	Domain string `json:"domain,omitempty"`
	// SignInAt records when these tokens were obtained. WorkBuddy sometimes
	// signs in without an expiry, and the access token's real lifetime is
	// hours, not the month ExpiresAt (the refresh token's) implies.
	SignInAt  string `json:"signInAt,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	// DeviceToken is the Turing Shield device token the desktop client sends as
	// X-Device-Token when present. Never forge or log it; omit the header when
	// empty (see gateway.resolveWorkBuddyDeviceToken).
	DeviceToken string `json:"deviceToken,omitempty"`
}

// ZCodeCredential is a ZCode (zcode.z.ai) subscription: the GLM Coding Plan
// project key that sign-in minted, plus the business-site state needed to mint
// it again and to read the plan's quota. ZCode is only a sign-in and key
// minting service — inference goes to the plan's own Anthropic-compatible
// endpoint, so forwarding uses the plain Claude protocol with APIKey as the
// key. APIKey, SessionToken and BizToken are secrets and must never reach the
// frontend (see gateway.redactProviderForClient).
type ZCodeCredential struct {
	// APIKey is the "<id>.<secret>" project key ZCode served or created
	// ("zcode-api-key" for a personal plan, "zcode-team-api-key" for a team
	// seat). It is what an inference request carries.
	APIKey string `json:"apiKey,omitempty"`
	// Site selects the ZCode realm: "zai" (Z.ai) or "bigmodel" (智谱).
	Site string `json:"site,omitempty"`
	// Base is the inference endpoint the key bills against, e.g.
	// https://api.z.ai/api/anthropic. Empty means derive it from Site.
	Base string `json:"base,omitempty"`
	// BizToken authorizes the business-site calls that re-mint or re-discover
	// the key (Z.ai sends it as a bearer, BigModel as a bare token).
	BizToken string `json:"bizToken,omitempty"`
	// OrganizationID / ProjectID name the team-plan project the seat was found
	// in; empty for a personal plan. They are account identifiers, not secrets.
	OrganizationID string `json:"organizationId,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	// Plan is the display name of the subscription ("GLM Coding Pro", …).
	Plan         string `json:"plan,omitempty"`
	AccountLabel string `json:"accountLabel,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	Connected    bool   `json:"connected,omitempty"`
	// Disconnected marks a user-initiated pause: the credential is kept so
	// reconnecting needs no browser trip, but forwarding must refuse to use it.
	Disconnected bool `json:"disconnected,omitempty"`
}

// TraeCredential is a Trae (ByteDance) subscription account. Trae's IDE
// protocol is its own, so a request carries the account's Cloud-IDE-JWT plus
// the device identity that JWT was issued for; the JWT is renewed against that
// same device. AccessToken, RefreshToken and DevicePrivateKey are secrets and
// must never reach the frontend (see gateway.redactProviderForClient).
type TraeCredential struct {
	// AccessToken is the Cloud-IDE-JWT every Trae request is signed with.
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"` // RFC3339
	AccountLabel string `json:"accountLabel,omitempty"`
	// UserID is the account's uid, sent as x-uid on every request.
	UserID string `json:"userId,omitempty"`
	// ClientID is the Trae client the session was signed in as; renewals ask as
	// the same client.
	ClientID string `json:"clientId,omitempty"`
	// DeviceID (19 digits) and MachineID (32 hex) are the identity created at
	// sign-in. Trae drops requests whose device keeps changing, so every
	// request for this account must send this same pair.
	DeviceID  string `json:"deviceId,omitempty"`
	MachineID string `json:"machineId,omitempty"`
	// DevicePrivateKey / DevicePublicKey are the ECDSA P-256 pair (PEM) that a
	// device-bound renewal signs its proof with. Trae CN requires them.
	DevicePrivateKey string `json:"devicePrivateKey,omitempty"`
	DevicePublicKey  string `json:"devicePublicKey,omitempty"`
	// APIHost overrides the realm's model host when the sign-in named one
	// (enterprise deployments, per-region hosts). AuthHost does the same for
	// renewals. Both are validated against Trae's own domains before use.
	APIHost  string `json:"apiHost,omitempty"`
	AuthHost string `json:"authHost,omitempty"`
	// Region is the Trae Global deployment ("US" or empty for Singapore);
	// it selects the model and usage hosts. Unused for Trae CN.
	Region string `json:"region,omitempty"`
	// ChatFunction is the Trae agent function that answered for this account,
	// remembered so later requests do not probe every one in turn.
	ChatFunction string `json:"chatFunction,omitempty"`
	Connected    bool   `json:"connected,omitempty"`
	// Disconnected marks a user-initiated pause, as in ZCodeCredential.
	Disconnected bool `json:"disconnected,omitempty"`
}

const (
	AuthTypeAPIKey       = "api_key"
	AuthTypeClaudeOAuth  = "claude_oauth"
	AuthTypeCursorOAuth  = "cursor_oauth"
	AuthTypeChatGPTOAuth = "chatgpt_oauth"
	// AuthTypeQoderPAT is Qoder's international site (qoder.com) and
	// AuthTypeQoderCN is Qoder CN (qoder.cn): the same subscription protocol on
	// its own hosts, with its own device-flow client id and model catalog, so
	// they are two connection kinds rather than a setting. The name of the
	// first predates the browser sign-in and is kept as the stored value; a
	// provider of it may be connected by device flow or by personal access
	// token (QoderCredential.SignInMode).
	AuthTypeQoderPAT = "qoder_pat"
	AuthTypeQoderCN  = "qoder_cn"
	// AuthTypeWorkBuddy is WorkBuddy (China, copilot.tencent.com) and
	// AuthTypeWorkBuddyAI is WorkBuddy AI (www.workbuddy.ai): the same sign-in
	// and API, a different endpoint, model list and extra request headers.
	AuthTypeWorkBuddy   = "workbuddy"
	AuthTypeWorkBuddyAI = "workbuddy_ai"
	// AuthTypeZCode signs in through ZCode (zcode.z.ai) to a GLM Coding Plan on
	// Z.ai; AuthTypeZCodeBigModel signs in to the same plan on BigModel (智谱).
	// One sign-in service, two realms: a different business API, inference
	// endpoint and subscription page, so they are two connection kinds rather
	// than a setting.
	AuthTypeZCode         = "zcode"
	AuthTypeZCodeBigModel = "zcode_bigmodel"
	// AuthTypeTraeCN is Trae's China deployment (trae.cn) and AuthTypeTrae
	// its international one (trae.ai). Same IDE protocol, different hosts,
	// client catalogs, model lists and billing pages.
	AuthTypeTraeCN     = "trae_cn"
	AuthTypeTraeGlobal = "trae_global"
	// AuthTypeOpenCodeZenFree is OpenCode Zen's free tier: the models Zen
	// serves to no key at all, which it will only serve to a request that
	// looks like OpenCode's own. No sign-in exists; the provider works as soon
	// as it is added.
	AuthTypeOpenCodeZenFree = "opencode_zen_free"
	// AuthTypeSelfRegister is a Bearer provider whose baseUrl/apiKeySource are
	// declared by its owner's own script through PATCH /__providers/{id}/
	// self-register instead of being typed into the console form: the console
	// only issues the provider-scoped token (see domain.ProviderSelfRegistration).
	// It forwards exactly like AuthTypeAPIKey once a script has registered, so
	// the forward path treats it as a Bearer provider; it exists as its own
	// value because the console's create form has nothing to type for one.
	AuthTypeSelfRegister = "self_register"
	// AuthTypePeer 是本机注册到某个上游网关后得到的那一侧：请求不是直接打给
	// 真实上游，而是打到上游的 POST /__peer/chat/completions，由上游用它自己
	// 的凭据转发，并把用量记在它的账上。
	//
	// 它之所以是独立的一种而不是复用 api_key：这类 provider 的 baseUrl 必须
	// 固定拼上 /__peer/chat/completions，请求体要包一层 {"providerId",
	// "payload"}，而且要带上链路头——把这三件事塞进 api_key 分支的 if 里，
	// 等于让一条普通转发路径同时承担两种协议。
	AuthTypePeer = "peer"
)

type Model struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	// Name is what the upstream calls this model to its own users. It is
	// display-only: a request still names the model by ID, because that is the
	// key the upstream routes on. Empty means the upstream gives no other name,
	// which is the case for every API-Key vendor whose model id is its label.
	Name          string   `json:"name,omitempty"`
	Protocol      Protocol `json:"protocol"`
	ContextLength int      `json:"contextLength"`
	// ContextOverride 是运维给这个模型单独定的上下文窗口；0 = 没定，按厂商
	// 已知值或上游声明值算。定过之后对外的窗口由它说了算（见
	// gateway.modelWindow），目录同步也不会把它冲掉——这正是它和 ContextLength
	// 分开存的原因：ContextLength 记的是「上游/厂商怎么说」，会随同步变化，
	// 也是清空设置后要退回去的那个值。
	//
	// 有了它，一条实际上真有 1M 的中转模型才能在 Claude Desktop / Claude Code
	// 里被当成 1M 模型列出去（对端认 max_input_tokens >= 1e6，见
	// gateway.desktopModelView）。
	ContextOverride int  `json:"contextOverride,omitempty"`
	MaxOutputTokens int  `json:"maxOutputTokens,omitempty"`
	InMenu          bool `json:"inMenu"`
	// Free and Rate are what the upstream charges for this model, read from its
	// own listing: Rate is the credits multiple a request costs, RateWas the
	// price struck through beside it while a discount runs (0: none), and Free
	// means the plan pays nothing for it. Display-only, and zero/false for every
	// vendor that does not publish prices per model — the console hides the
	// badges rather than inventing them.
	Free bool    `json:"free,omitempty"`
	Rate float64 `json:"rate,omitempty"`
	// RateWas is only meaningful above Rate; a promotion that zeroes the price
	// for a while is a discount, not a free model, and this is what shows it.
	RateWas float64 `json:"rateWas,omitempty"`
}

// ModelPrice is what the gateway charges itself for one model's tokens, so the
// console can say what a request cost. It is not the vendor's invoice: the
// numbers are entered here (from the vendor's published price list) and applied
// to the token counts the gateway actually saw.
//
// All four rates are US dollars per million tokens, and the tiers are mutually
// exclusive — InputUSDPerMTok is the *uncached* part of the prompt, because our
// InputTokens total already contains the cache reads and the cache writes and
// billing them again would double-count. ModelID "*" is the default for the
// row's scope, used for any model without its own row; ProviderID "*" is the
// global scope shared by every provider that has no row of its own. A provider
// or model with no row at all is not priced, and the console says 未计价 rather
// than showing a cost of zero. Subscription providers that bill in credits
// rather than dollars should stay unpriced for the same reason.
type ModelPrice struct {
	ProviderID           string  `json:"providerId"`
	ModelID              string  `json:"modelId"`
	InputUSDPerMTok      float64 `json:"inputUsdPerMTok"`
	OutputUSDPerMTok     float64 `json:"outputUsdPerMTok"`
	CacheReadUSDPerMTok  float64 `json:"cacheReadUsdPerMTok"`
	CacheWriteUSDPerMTok float64 `json:"cacheWriteUsdPerMTok"`
	UpdatedAt            string  `json:"updatedAt,omitempty"`
}

type OutputEndpoint struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Protocol            Protocol `json:"protocol"`
	BasePath            string   `json:"basePath"`
	ListenHost          string   `json:"listenHost"`
	ListenPort          int      `json:"listenPort"`
	PublicAccessEnabled bool     `json:"publicAccessEnabled"`
	PublicURL           string   `json:"publicUrl,omitempty"`
	// StreamEnabled controls whether clients may request SSE streaming on this
	// output protocol. Default true; when false, stream:true requests are rejected.
	StreamEnabled bool `json:"streamEnabled"`
}

// Route 只描述“这条 Key 绑定哪个 Provider”及其默认输出端点，不承载
// 直通/转换策略。每次请求由实际客户端协议、Provider.Protocol 和
// ProtocolSupport 现场判定。
type Route struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	OutputEndpointID string   `json:"outputEndpointId"`
	ProviderID       string   `json:"providerId"`
	OutputProtocol   Protocol `json:"outputProtocol"`
	Enabled          bool     `json:"enabled"`
}

type APIKey struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Key                   string            `json:"key"`
	RouteID               string            `json:"routeId"`
	ModelOverride         string            `json:"modelOverride,omitempty"`
	ModelAliases          map[string]string `json:"modelAliases,omitempty"`
	ThinkingDepthOverride string            `json:"thinkingDepthOverride,omitempty"`
	// MaxOutputTokens 可选覆盖上游 max_tokens / max_output_tokens。
	// 0 表示按实际上游模型自动解析；>0 时优先使用该值（用于新模型或临时调高/压低）。
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// FirstPacketTimeoutSec 是这个 Key 的「等上游响应头」上限，覆盖它名下所有
	// Provider。0 = 不单独设，按各 Provider 自己的 FirstPacketTimeoutSec（再没有
	// 就落到网关默认的 600s 响应头等待）。
	//
	// 为什么要在 Key 上再放一个：同一个 Provider 可能同时服务「秒回的聊天」和
	// 「先想两分钟再开口」的重模型，Provider 级的一个数答不了两种用法；而调用方
	// 最清楚自己这一把 Key 是干什么的。
	FirstPacketTimeoutSec int `json:"firstPacketTimeoutSec,omitempty"`
	// SameProviderRetries 是「同一个 Provider 上最多再试几次」。
	//
	// 默认 0 = 一失败就换下一个节点（历史行为）。设成 1 或 2 之后，网络抖动、
	// 上游一瞬间的 5xx 这类瞬时故障会先在原地重试，不把整条链路往前推——这才是
	// 「第一个明明还能用，偶尔出错就切走」那类抱怨的正解。只对可重试的失败生效
	// （传输错误、5xx、429 这类），4xx 参数错误重试多少次都一样。
	SameProviderRetries int `json:"sameProviderRetries,omitempty"`
	// QuotaWindow 是额度口径："day"（按网关本机时区的 0 点重置）或 "month"
	// （1 号 0 点）。空表示不设额度，也是升级前所有密钥的行为。
	// 按本机日历而不是 UTC：这套网关是一个运维给自己的客户端用的，「每天 0 点重新开始」
	// 是他会对用户说的话，UTC 日界会让 +08:00 的部署早上 8 点才"新的一天"。
	QuotaWindow string `json:"quotaWindow,omitempty"`
	// QuotaTokens 是窗口内可用的 token 总量，口径与用量页一致：输入（已含缓存）+ 输出。
	// 0 = 不限。它是治理闸门而不是预付费钱包：入口只预扣请求里已经确定的 prompt 部分，
	// 回答的 token 要等上游结束才计入，所以一波并发最多超出「这些在途请求真实产生的量」，
	// 下一个请求就会被拒。
	QuotaTokens int64 `json:"quotaTokens,omitempty"`
	// QuotaCostMicros 是窗口内的成本上限，单位微美元（1e-6 USD），按价格表逐请求算出的
	// 已计价成本累计。0 = 不限。只对设过价格行的上游有效——未计价的请求贡献 0，
	// 永远不会触达这条上限，所以要用成本限额时最好同时给一个 QuotaTokens。
	QuotaCostMicros int64 `json:"quotaCostMicros,omitempty"`
	// StreamEnabled controls whether requests authenticated with this API key
	// may use SSE streaming. Default true; when false, stream:true requests are
	// rejected. Bound to the API key (not the output endpoint) so streaming can
	// be toggled per consumer.
	StreamEnabled bool `json:"streamEnabled"`
	// CodexKeepOfficialLogin controls whether the generated Codex config.toml
	// provider table mimics the built-in official "openai" provider shape
	// (name = "OpenAI", supports_websockets = true) so Codex's own official
	// feature gates (plugin marketplace, mobile remote control) keep matching
	// while base_url / experimental_bearer_token still point at this gateway.
	// It never touches ~/.codex/auth.json. Default false; bound per API key so
	// the choice survives across "复制 Codex 配置" dialog opens.
	CodexKeepOfficialLogin bool `json:"codexKeepOfficialLogin,omitempty"`
	// CodexForceFast forces OpenAI's fast processing tier (service_tier:
	// "priority", 2x-2.5x credit burn) on requests that end up on a ChatGPT
	// OAuth (Codex) provider, regardless of what the caller sends. Key-level
	// switch like ModelOverride; ignored on every other provider type.
	CodexForceFast bool `json:"codexForceFast,omitempty"`
	// FallbackProviderIDs is an ordered list of backup input providers after the
	// preferred provider on the bound route. Higher priority comes first.
	FallbackProviderIDs []string `json:"fallbackProviderIds,omitempty"`
	// ScheduleMode decides which provider of the chain (preferred + fallbacks)
	// answers a request. Empty and "order" keep the historical behavior: the
	// preferred provider first, then the chain in the order the administrator
	// wrote, with the key's sticky provider taken first. "rotate" walks the
	// chain round-robin, "least_used" picks the account that carried the
	// smallest load in the last few minutes, and "quota" prefers the account
	// with the most quota left (subscription windows read from the quota
	// panels; accounts without a readable quota sit in the middle). "spread"
	// balances conversations rather than load: a new conversation goes to the
	// account holding the fewest live ones and stays there for two hours, so
	// separate conversations run on separate accounts in parallel. A
	// conversation's session pin still wins over all of them, so prompt caches
	// survive; every mode still skips providers that are unavailable, disabled,
	// revoked from the key's owner, or cross-protocol without a locked model.
	ScheduleMode string `json:"scheduleMode,omitempty"`
	// FallbackModelOverrides maps each fallback provider id to a required fixed
	// model replacement used when that provider is active.
	FallbackModelOverrides map[string]string `json:"fallbackModelOverrides,omitempty"`
	// ActiveProviderID is the provider currently used after failover. Empty
	// means use the route's preferred provider.
	ActiveProviderID string `json:"activeProviderId,omitempty"`
	// ServingProviderID 是最近一次真正返回响应的 Provider（运行期状态，不持久化）。
	// 控制台的「在服务」看它：会话粘性命中的请求不会改写 ActiveProviderID，
	// 只看粘滞位会把正在服务的备选显示成首选。空 = 进程启动后还没有请求。
	ServingProviderID string `json:"servingProviderId,omitempty"`
	// ServingProviderIDs 是最近 10 分钟内真正返回过响应的全部 Provider。
	//
	// 会话分散（spread）下多条会话各落一个账号是常态，那时它们都真的在服务，
	// 控制台要同时把它们标绿才对得上「分散」二字。只给一个的话，另外两条会被
	// 显示成「待命」，看起来像分散没生效。
	ServingProviderIDs []string `json:"servingProviderIds,omitempty"`
	// OwnerUserID binds this key to a console user. Empty means the key belongs
	// to the administrator (legacy keys default to admin ownership).
	OwnerUserID string `json:"ownerUserId,omitempty"`
	// Profiles holds named forwarding-config snapshots for this key. A single
	// key (unchanged token) can carry multiple profiles; one is active at a
	// time. The top-level routing fields above always mirror the active
	// profile so request-time resolution never needs to touch this list.
	Profiles []KeyProfile `json:"profiles,omitempty"`
	// ActiveProfileID is the profile currently in effect. Empty means no
	// profiles are configured and the top-level fields are used directly.
	ActiveProfileID string `json:"activeProfileId,omitempty"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       string `json:"createdAt"`
	LastUsedAt      string `json:"lastUsedAt,omitempty"`
}

// KeyProfile is a named forwarding-configuration snapshot owned by an APIKey.
// Switching the key's active profile copies these fields onto the key's
// top-level routing fields, so the client-facing token never changes while the
// upstream routing does. Fields mirror the routing subset of APIKey.
type KeyProfile struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	RouteID                string            `json:"routeId"`
	ModelOverride          string            `json:"modelOverride,omitempty"`
	ModelAliases           map[string]string `json:"modelAliases,omitempty"`
	ThinkingDepthOverride  string            `json:"thinkingDepthOverride,omitempty"`
	MaxOutputTokens        int               `json:"maxOutputTokens,omitempty"`
	FallbackProviderIDs    []string          `json:"fallbackProviderIds,omitempty"`
	FallbackModelOverrides map[string]string `json:"fallbackModelOverrides,omitempty"`
	StreamEnabled          bool              `json:"streamEnabled"`
	// ScheduleMode mirrors APIKey.ScheduleMode: switching a scheme switches the
	// scheduling policy with it.
	ScheduleMode string `json:"scheduleMode,omitempty"`
}

// UserRole enumerates console user roles.
type UserRole string

const (
	UserRoleAdmin UserRole = "admin"
	UserRoleUser  UserRole = "user"
)

// User is a management-console account. What it may do comes from its role
// (see Role); which resources it may touch comes from the role's scope plus
// AllowedProviderIDs for the "granted" scope.
type User struct {
	ID           string   `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"-"`
	Role         UserRole `json:"role"`
	// RoleID is the RBAC role. Built-ins ("admin"/"user"/"viewer"/"grantee")
	// are answered from code; anything else is a row in the roles table. Empty
	// on legacy rows is resolved to "user" (or "admin" when Role is admin).
	RoleID string `json:"roleId"`
	// DisplayName is the human name shown in the console (falls back to
	// Username). Provider/key owner labels use it, not a generic "管理员".
	DisplayName string `json:"displayName,omitempty"`
	// AvatarURL is a data URL or an /__avatars/... path served by the gateway.
	AvatarURL string `json:"avatarUrl,omitempty"`
	// AllowedResourceIDs restricts which input providers this user may bind
	// API keys to when the role scope is "granted". Admin-assigned; empty means
	// no shared providers are available.
	AllowedResourceIDs []string `json:"allowedResourceIds,omitempty"`
	Enabled            bool     `json:"enabled"`
	CreatedAt          string   `json:"createdAt"`
	LastLoginAt        string   `json:"lastLoginAt,omitempty"`
	// LastActiveAt is the latest console API request time from this user's
	// browser session. The in-memory tracker is always accurate; the DB copy
	// is flushed asynchronously and at most once per 5 minutes per user to
	// avoid hot-path write churn.
	LastActiveAt string `json:"lastActiveAt,omitempty"`
	// Kind 区分本地账号与「下游网关」账号。空 = local（升级前所有行都是
	// 本地账号）。peer 行由下游网关注册时自动创建，它存在的意义只有两个：
	// 让下游出现在用户管理页、让授权表能按 user_id 挂 provider。
	Kind UserKind `json:"kind,omitempty"`
	// GatewayID 是 peer 账号对应的网关稳定身份（同 gatewayid.ID）。本地账号
	// 为空。它比用户名可靠：用户名可以改，网关身份不会，所以环路检测和用量
	// 对账都以它为准。
	GatewayID string `json:"gatewayId,omitempty"`
	// OwnerUserID 只对 kind=peer 有意义：这个下游网关归属哪个真实用户。
	// 空 = 尚未人工归属；这类流量仍进入 Provider 总量，但不进入任何人的个人
	// Token 统计。它和「最近授权人」分开，后续管理员调授权不会转移归属。
	OwnerUserID string `json:"ownerUserId,omitempty"`
}

// PeerGrantAudit records one manual downstream-grant change. Actor identity is
// snapshotted so the history remains readable even after the account is renamed
// or deleted.
type PeerGrantAudit struct {
	ID                int64    `json:"id"`
	PeerUserID        string   `json:"peerUserId"`
	ActorUserID       string   `json:"actorUserId"`
	ActorUsername     string   `json:"actorUsername,omitempty"`
	ActorDisplayName  string   `json:"actorDisplayName,omitempty"`
	GrantMode         string   `json:"grantMode,omitempty"`
	BeforeProviderIDs []string `json:"beforeProviderIds"`
	AfterProviderIDs  []string `json:"afterProviderIds"`
	CreatedAt         string   `json:"createdAt"`
}

// UserKind 区分 users 表里两类完全不同东西。
//
// 它们共用一张表是刻意的：授权（AllowedResourceIDs）、用量归属、按 owner
// 过滤密钥、禁用即停流量，这些机制全都已经按 user_id 建好了。给下游网关另
// 起一张表意味着上面每一条都要写第二遍，而它们迟早会不一致。
//
// 区别也必须在数据上体现出来：peer 账号不能登录控制台（登录与会话恢复都
// 显式拒绝 IsPeer 的账号），不能持有会话，只能在 /__peer/* 上以凭据形式
// 出现。password_hash 仍然保留——它只服务于「重新注册时证明还是同一个下游」，
// 不是一个登录凭据。
type UserKind string

const (
	// UserKindLocal 是控制台账号：有密码、能登录、能拥有密钥。
	UserKindLocal UserKind = "local"
	// UserKindPeer 是注册到本机的下游网关。它同样拥有 api_keys 行（那把密钥
	// 就是它对上游的凭据），但永远不能登录。
	UserKindPeer UserKind = "peer"
)

// IsPeer 报告这是否是一个下游网关账号。
func (u User) IsPeer() bool { return u.Kind == UserKindPeer }

// RouteDecision 是一次请求的运行时决策。Action 是结果，不是可配置输入。
type RouteDecision struct {
	RouteID         string   `json:"routeId"`
	ProviderID      string   `json:"providerId"`
	OutputProtocol  Protocol `json:"outputProtocol"`
	InputProtocol   Protocol `json:"inputProtocol"`
	Action          string   `json:"action"`
	ConversionLabel string   `json:"conversionLabel"`
}

// DataPaths lists absolute filesystem locations for user-level gateway data.
// All of these live outside the macOS .app bundle so App updates keep state.
type DataPaths struct {
	DataDir             string `json:"dataDir"`
	ConfigFile          string `json:"configFile"`
	SQLiteDB            string `json:"sqliteDb"`
	CloudflareConfigDir string `json:"cloudflareConfigDir,omitempty"`
	CloudflaredHome     string `json:"cloudflaredHome,omitempty"`
	CursorTokenDir      string `json:"cursorTokenDir,omitempty"`
	CursorTokenFile     string `json:"cursorTokenFile,omitempty"`
	Note                string `json:"note,omitempty"`
}

type GatewayState struct {
	ProviderGroups []ProviderGroup      `json:"providerGroups"`
	Providers      []Provider           `json:"providers"`
	Endpoints      []OutputEndpoint     `json:"endpoints"`
	Routes         []Route              `json:"routes"`
	Models         []Model              `json:"models"`
	APIKeys        []APIKey             `json:"apiKeys"`
	Metrics        MetricsSnapshot      `json:"metrics"`
	PublicAccess   PublicAccessSettings `json:"publicAccess"`
	LogLevel       string               `json:"logLevel,omitempty"`
	// RequestLogRetentionDays controls how long request_logs are kept (default 7).
	RequestLogRetentionDays int `json:"requestLogRetentionDays,omitempty"`
	// Log2xxBodies controls whether successful (2xx) requests store their request
	// and response bodies in request_logs. Off by default: those bodies dominate
	// the per-request disk write volume, while for a successful request they are
	// rarely needed. Failed requests always store bodies regardless of this flag,
	// and so does a 2xx whose response body carries an error payload.
	Log2xxBodies bool `json:"log2xxBodies"`
	// PeerRegistrationAllowCIDRs 是允许注册的对端来源白名单（逗号分隔的 CIDR）。
	// 空 = 不限制来源（下游从公网直接连上来是正常用法）。显式配置后只认白名单：
	// 注册的边界是用户名+密码与上游的授权/吊销，白名单是额外的收紧手段。
	PeerRegistrationAllowCIDRs string `json:"peerRegistrationAllowCidrs,omitempty"`
	// MaxGatewayHops 是一条链路允许经过的网关数量上限（1..8）。0 = 默认 3。
	// 链路上的每台网关都按自己这份配置判断，超限的透传在 508 被拒；
	// 想组更长的多级链路（内网 → 公网 → 公网）就把它调大。
	MaxGatewayHops int `json:"maxGatewayHops,omitempty"`
	// UsageBatchMaxSize / UsageBatchMaxWaitSeconds tune how the async usage
	// worker batches usage_daily_buckets commits. Larger values mean fewer,
	// bigger commits (less disk wear) at the cost of a longer window whose
	// stats an ungraceful kill would lose. Zero means use the defaults
	// (500 events / 60s).
	UsageBatchMaxSize        int `json:"usageBatchMaxSize,omitempty"`
	UsageBatchMaxWaitSeconds int `json:"usageBatchMaxWaitSeconds,omitempty"`
	// HostMetricsDisabled turns off the machine-status sampler and its endpoints.
	// False (the zero value) preserves the historical always-on behavior.
	HostMetricsDisabled bool `json:"hostMetricsDisabled,omitempty"`
	// WebExposed controls whether the HTTP server binds all interfaces (0.0.0.0)
	// so LAN / tunnel clients can reach the management UI and APIs. When false,
	// the server listens on 127.0.0.1 only (local App / loopback).
	WebExposed bool `json:"webExposed"`
	// DataPaths is runtime-only (not persisted); absolute paths for backup/analysis.
	DataPaths *DataPaths `json:"dataPaths,omitempty"`
	// CursorBridge is the live local gRPC bridge status; runtime-only, not persisted.
	CursorBridge *CursorBridgeRuntime `json:"cursorBridge,omitempty"`
	// ServerTimeZone names the gateway process's local zone (e.g. "Asia/Shanghai").
	// Server-day arithmetic — API-key "today" counts, scheduled-task clocks, the
	// day bounds of a bare date — all use it, so the console must build its
	// "today" from the same zone instead of the browser's. Runtime-only.
	ServerTimeZone string `json:"serverTimeZone,omitempty"`
	// GatewayIdentity 是本网关的稳定身份，父子对接靠它互相识别。Runtime-only。
	// 所有角色都能看到（普通用户也需要它来填"父网关 ID"），不含机器标识本身。
	GatewayIdentity *GatewayIdentity `json:"gatewayIdentity,omitempty"`
}

// GatewayIdentity 是暴露给控制台的网关身份。
//
// ID 从机器标识派生，重装、清库都不变；它也是环路检测里判断"这是不是我自己"
// 的那个值。这里只放派生后的哈希，机器标识本身绝不出现在这个结构里。
type GatewayIdentity struct {
	ID string `json:"id"`
	// Name 是界面上显示的名字，默认取主机名，可改。
	Name string `json:"name,omitempty"`
	// Source 是 machine（从机器标识派生）或 persisted（库里存的随机值）。
	Source string `json:"source"`
	// MachineIDReadable 为 false 时重装会换身份，界面必须提示，否则运维会
	// 在重装后发现下游全部失联却不知道为什么。
	MachineIDReadable bool `json:"machineIdReadable"`
}

// CursorBridgeRuntime mirrors the local cursor-bridge subprocess for status responses.
type CursorBridgeRuntime struct {
	Status    string `json:"status"`
	Port      int    `json:"port,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Message   string `json:"message,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// Quota-keeper ("额度保活") scheduled tasks.
//
// The reason these exist: a Claude-style subscription meter starts a rolling
// window only when a request actually arrives, and resets it five hours later.
// A day spent not chatting therefore does not spend a window at all, so the
// first request the next morning is still inside yesterday's window and the
// user burns two days' allowance in one afternoon. A scheduled probe anchors
// the window at a chosen hour instead, and the rest of the day's resets can be
// inferred from that anchor.

// ScheduleMode selects how a task decides its next firing time.
type ScheduleMode string

const (
	// ScheduleDailyTimes fires at each HH:MM in Times, every day.
	ScheduleDailyTimes ScheduleMode = "daily_times"
	// ScheduleEveryHours rolls forward from AnchorAt in fixed hour steps,
	// which is the shape of a five-hour window: anchor once at 09:00 and the
	// task keeps re-anchoring at 14:00, 19:00, …
	ScheduleEveryHours ScheduleMode = "every_hours"
	// ScheduleEveryMinutes rolls forward from AnchorAt in fixed minute steps,
	// for anyone who wants a tighter cadence than an hour.
	ScheduleEveryMinutes ScheduleMode = "every_minutes"
)

// ScheduledTask is one keep-alive job: a schedule, the providers it pokes, and
// the model each provider should answer with.
type ScheduledTask struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Enabled     bool         `json:"enabled"`
	OwnerUserID string       `json:"ownerUserId,omitempty"`
	Mode        ScheduleMode `json:"mode"`
	// TimeZone is the IANA zone the daily clock list is read against, e.g.
	// "Asia/Shanghai". Empty means the server's own zone — but a gateway on a
	// US VPS and a user in Beijing then disagree about what "09:00" means, which
	// is exactly how this field came to exist.
	TimeZone string `json:"timeZone,omitempty"`
	// Times are "HH:MM" strings, used by ScheduleDailyTimes.
	Times []string `json:"times,omitempty"`
	// Interval is the step for the rolling modes: hours for ScheduleEveryHours,
	// minutes for ScheduleEveryMinutes.
	Interval int `json:"interval"`
	// AnchorAt is where the rolling modes start counting from (RFC3339). Empty
	// means the task's creation time, so saving a task at 09:07 with a 5-hour
	// interval fires at 14:07, 19:07, … without the user doing arithmetic.
	AnchorAt string `json:"anchorAt,omitempty"`
	// ProviderIDs are poked in order on every firing.
	ProviderIDs []string `json:"providerIds"`
	// ModelOverrides pins the model per provider id; empty means the provider's
	// own default.
	ModelOverrides map[string]string `json:"modelOverrides,omitempty"`
	// QuietMinutes is the anti-self-sabotage window: a provider used within the
	// last QuietMinutes is skipped, because firing then would only push its
	// reset further out. Zero falls back to the default.
	QuietMinutes int `json:"quietMinutes"`
	// ProbePrompt is the throwaway message. Short on purpose: one request per
	// provider per firing is the entire cost of the feature.
	ProbePrompt string `json:"probePrompt,omitempty"`
	// LastRunAt / NextRunAt are RFC3339; NextRunAt is recomputed after every
	// firing so a restart cannot double-fire a slot that already ran.
	LastRunAt string `json:"lastRunAt,omitempty"`
	NextRunAt string `json:"nextRunAt,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	// PausedBy records who turned the task off. Only ScheduledTaskPausedByKeeper
	// tasks are resumed automatically: the keeper pauses a task whose providers
	// were all deleted and revives it once one comes back, but a pause a person
	// asked for must stay until a person lifts it.
	PausedBy string `json:"pausedBy,omitempty"`
}

const (
	// ScheduledTaskPausedByKeeper marks a pause the quota keeper made itself.
	ScheduledTaskPausedByKeeper = "keeper"
	// ScheduledTaskPausedByUser marks a pause an operator asked for.
	ScheduledTaskPausedByUser = "user"
)

// ScheduledTaskRun is one provider's outcome inside one firing of a task. A
// firing writes one row per provider, so the console can show "3 家成功、1 家跳过"
// and the reason for a skip.
type ScheduledTaskRun struct {
	ID         int64  `json:"id"`
	TaskID     string `json:"taskId"`
	ProviderID string `json:"providerId"`
	// BatchID groups the rows of a single firing.
	BatchID   string `json:"batchId"`
	FiredAt   string `json:"firedAt"`
	Outcome   string `json:"outcome"` // fired | skipped | failed
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Model     string `json:"model,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const (
	QuotaRunFired   = "fired"
	QuotaRunSkipped = "skipped"
	QuotaRunFailed  = "failed"
	// DefaultQuotaQuietMinutes is how recently a provider must have been used
	// for a scheduled probe to stand down.
	DefaultQuotaQuietMinutes = 60
	// DefaultQuotaProbePrompt is the throwaway message.
	DefaultQuotaProbePrompt = "ping"
	// QuotaMissGraceMinutes bounds how late a firing may still be worth
	// running after the gateway was down through it.
	QuotaMissGraceMinutes = 10
)

// Daily check-in ("每日签到").
//
// Some subscriptions hand out credits once a day for pressing 签到 in their own
// app — WorkBuddy (China) on a streak, Qoder through its campaigns page. The
// gateway presses it for a signed-in account so the user does not have to open
// the vendor's app every day. Only the vendors listed by
// gateway.checkinVendorOf have one; every other subscription shows no switch at
// all, because a button that can never fire is worse than no button.

// CheckinRecord is what came of one account's check-in on Day, a Beijing day
// ("2006-01-02") — the day the vendors count in, fixed to UTC+8 so a gateway on
// a US VPS and its user in Shenzhen agree about whether today is done. One row
// per provider holds the latest answer, which is all the console needs.
type CheckinRecord struct {
	ProviderID string `json:"providerId,omitempty"`
	// AccountKey is the account the record belongs to (site|uid). A row kept
	// under a provider whose account was swapped out is not today's answer, and
	// the loop says so rather than skipping the new account.
	AccountKey string  `json:"accountKey,omitempty"`
	Day        string  `json:"day,omitempty"`
	At         string  `json:"at,omitempty"` // RFC3339 UTC
	Outcome    string  `json:"outcome,omitempty"`
	Credit     float64 `json:"credit,omitempty"`
	// Streak is how many days in a row the vendor counts. Only shown when the
	// vendor reports it: WorkBuddy does, Qoder's campaigns do not.
	Streak  int    `json:"streak,omitempty"`
	Message string `json:"msg,omitempty"`
	// Until is the end (RFC3339) of the window a success was read inside: a
	// Qoder campaign can hand out again before the Beijing day turns, so a
	// "done" is only trusted until then.
	Until string `json:"validUntil,omitempty"`
	// Offline marks a check-in whose request never reached the vendor (no route,
	// no answer in time), and Tries how many before it the same day did the
	// same; that pair is what backs the retry off, minute by minute, instead of
	// waiting out the failure retry on a machine that just woke up.
	Offline bool `json:"offline,omitempty"`
	Tries   int  `json:"tries,omitempty"`
}

// CheckinOutcome.
const (
	CheckinClaimed    = "claimed"    // credits claimed now
	CheckinDone       = "done"       // today's already in, pressed by us or in the app
	CheckinIneligible = "ineligible" // the account can't take part
	CheckinInactive   = "inactive"   // no event running, or it has ended
	CheckinFailed     = "failed"     // no answer; tried again later the same day
	// CheckinOwnApp is a check-in the vendor only lets its own app press. It is
	// an answer for the day, so it is not retried: WorkBuddy's check-in is
	// signed with a device token from its security SDK, which we cannot make.
	CheckinOwnApp = "own-app"
)

// CheckedIn says whether the day's credits are in, claimed now or before.
func (r CheckinRecord) CheckedIn() bool {
	return r.Outcome == CheckinClaimed || r.Outcome == CheckinDone
}

type MetricsSnapshot struct {
	Requests       int64   `json:"requests"`
	SuccessRate    float64 `json:"successRate"`
	InputTokens    int64   `json:"inputTokens"`
	OutputTokens   int64   `json:"outputTokens"`
	AverageLatency int64   `json:"averageLatencyMs"`
}
