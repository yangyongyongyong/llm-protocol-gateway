package domain

type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolClaude          Protocol = "claude"
)

func (p Protocol) DisplayName() string {
	switch p {
	case ProtocolOpenAIChat:
		return "OpenAI Chat"
	case ProtocolOpenAIResponses:
		return "OpenAI Responses"
	case ProtocolClaude:
		return "Claude"
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
	Enabled      bool             `json:"enabled"`
	Provider     string           `json:"provider"`
	Mode         PublicAccessMode `json:"mode"`
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
	UIDomain string           `json:"uiDomain,omitempty"`
	Expose   string           `json:"expose"`
	RuntimeURL   string           `json:"runtimeUrl,omitempty"`
	// Named-tunnel fields for custom-domain mode (cloudflared). These are
	// persisted so the setup can be reused, but full named-tunnel automation is
	// stubbed in this build (see internal/tunnel).
	TunnelName        string `json:"tunnelName,omitempty"`
	TunnelToken       string `json:"tunnelToken,omitempty"`
	CredentialsFile   string `json:"credentialsFile,omitempty"`
	TunnelConfigFile  string `json:"tunnelConfigFile,omitempty"`
	PublicBaseURL   string `json:"publicBaseUrl,omitempty"`
	// UIPublicBaseURL is the browser management URL for custom-domain mode.
	UIPublicBaseURL string `json:"uiPublicBaseUrl,omitempty"`
	Status          string `json:"status"`
	StatusMessage   string `json:"statusMessage"`
	// Tunnel is the live cloudflared process state; runtime-only, not persisted.
	Tunnel *TunnelRuntime `json:"tunnel,omitempty"`
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
	AuthHeader           string   `json:"authHeader"`
	ExtraEndpoint        string   `json:"extraEndpoint,omitempty"`
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
	// RequestAdapter is an optional provider-level request rewrite template
	// (URL/headers/body/model mapping). Nil means use built-in protocol logic.
	RequestAdapter *RequestAdapter `json:"requestAdapter,omitempty"`
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

const (
	AuthTypeAPIKey       = "api_key"
	AuthTypeClaudeOAuth  = "claude_oauth"
	AuthTypeCursorOAuth  = "cursor_oauth"
)

type Model struct {
	ID            string   `json:"id"`
	ProviderID    string   `json:"providerId"`
	Protocol      Protocol `json:"protocol"`
	ContextLength int      `json:"contextLength"`
	InMenu        bool     `json:"inMenu"`
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
	Enabled               bool              `json:"enabled"`
	CreatedAt             string            `json:"createdAt"`
	LastUsedAt            string            `json:"lastUsedAt,omitempty"`
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
	// WebExposed controls whether the HTTP server binds all interfaces (0.0.0.0)
	// so LAN / tunnel clients can reach the management UI and APIs. When false,
	// the server listens on 127.0.0.1 only (local App / loopback).
	WebExposed bool `json:"webExposed"`
	// DataPaths is runtime-only (not persisted); absolute paths for backup/analysis.
	DataPaths *DataPaths `json:"dataPaths,omitempty"`
}

type MetricsSnapshot struct {
	Requests       int64   `json:"requests"`
	SuccessRate    float64 `json:"successRate"`
	InputTokens    int64   `json:"inputTokens"`
	OutputTokens   int64   `json:"outputTokens"`
	AverageLatency int64   `json:"averageLatencyMs"`
}
