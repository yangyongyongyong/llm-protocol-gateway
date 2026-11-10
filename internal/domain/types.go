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

type RouteMode string

const (
	RouteModeAuto        RouteMode = "auto"
	RouteModePassThrough RouteMode = "pass_through"
	RouteModeConvert     RouteMode = "convert"
)

type PublicAccessMode string

const (
	PublicAccessModeRandomTunnel PublicAccessMode = "random_tunnel"
	PublicAccessModeCustomDomain PublicAccessMode = "custom_domain"
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
	// OwnerUserID is the console user that created this provider. Empty means
	// admin-created. A normal user owns the providers they create and may
	// edit/clone/delete/test them; admins have full access to every provider.
	OwnerUserID string `json:"ownerUserId,omitempty"`
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
	// no limit. The timer covers only the wait for headers: once they arrive it
	// is stopped, so a long reasoning stream or a slow body is never cut short,
	// and a provider that thinks for two minutes before answering is not
	// mistaken for a hung one.
	FirstPacketTimeoutSec int `json:"firstPacketTimeoutSec,omitempty"`
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
)

type Model struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	// Name is what the upstream calls this model to its own users. It is
	// display-only: a request still names the model by ID, because that is the
	// key the upstream routes on. Empty means the upstream gives no other name,
	// which is the case for every API-Key vendor whose model id is its label.
	Name            string   `json:"name,omitempty"`
	Protocol        Protocol `json:"protocol"`
	ContextLength   int      `json:"contextLength"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	InMenu          bool     `json:"inMenu"`
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

type Route struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	OutputEndpointID string    `json:"outputEndpointId"`
	ProviderID       string    `json:"providerId"`
	OutputProtocol   Protocol  `json:"outputProtocol"`
	Mode             RouteMode `json:"mode"`
	Enabled          bool      `json:"enabled"`
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
}

type RouteDecision struct {
	RouteID         string    `json:"routeId"`
	ProviderID      string    `json:"providerId"`
	OutputProtocol  Protocol  `json:"outputProtocol"`
	InputProtocol   Protocol  `json:"inputProtocol"`
	Mode            RouteMode `json:"mode"`
	Action          string    `json:"action"`
	ConversionLabel string    `json:"conversionLabel"`
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
	Providers    []Provider           `json:"providers"`
	Endpoints    []OutputEndpoint     `json:"endpoints"`
	Routes       []Route              `json:"routes"`
	Models       []Model              `json:"models"`
	APIKeys      []APIKey             `json:"apiKeys"`
	Metrics      MetricsSnapshot      `json:"metrics"`
	PublicAccess PublicAccessSettings `json:"publicAccess"`
	LogLevel     string               `json:"logLevel,omitempty"`
	// RequestLogRetentionDays controls how long request_logs are kept (default 7).
	RequestLogRetentionDays int `json:"requestLogRetentionDays,omitempty"`
	// Log2xxBodies controls whether successful (2xx) requests store their request
	// and response bodies in request_logs. Off by default: those bodies dominate
	// the per-request disk write volume, while for a successful request they are
	// rarely needed. Failed requests always store bodies regardless of this flag,
	// and so does a 2xx whose response body carries an error payload.
	Log2xxBodies bool `json:"log2xxBodies"`
	// UsageBatchMaxSize / UsageBatchMaxWaitSeconds tune how the async usage
	// worker batches usage_daily_buckets commits. Larger values mean fewer,
	// bigger commits (less disk wear) at the cost of a longer window whose
	// stats an ungraceful kill would lose. Zero means use the defaults
	// (500 events / 60s).
	UsageBatchMaxSize        int `json:"usageBatchMaxSize,omitempty"`
	UsageBatchMaxWaitSeconds int `json:"usageBatchMaxWaitSeconds,omitempty"`
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
