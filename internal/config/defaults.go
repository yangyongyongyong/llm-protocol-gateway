package config

import "github.com/luca/llm-protocol-gateway/internal/domain"

func DefaultState() domain.GatewayState {
	return domain.GatewayState{
		Providers: []domain.Provider{},
		Endpoints: DefaultEndpoints(),
		Routes:    []domain.Route{},
		Models:    []domain.Model{},
		Metrics:   domain.MetricsSnapshot{},
		PublicAccess: domain.PublicAccessSettings{
			Enabled:       false,
			Provider:      "cloudflare",
			Mode:          domain.PublicAccessModeRandomTunnel,
			ExposeAPI:     true,
			ExposeUI:      true,
			Expose:        "all",
			Status:        "disabled",
			StatusMessage: "Public access is disabled. Enable it to use a Cloudflare quick tunnel URL, or configure a purchased Cloudflare domain such as lucadesign.uk.",
		},
	}
}

func DefaultEndpoints() []domain.OutputEndpoint {
	return []domain.OutputEndpoint{
		{ID: "openai-chat-output", Name: "OpenAI Chat Output", Protocol: domain.ProtocolOpenAIChat, BasePath: "/v1", ListenHost: "127.0.0.1", ListenPort: 18093, StreamEnabled: true},
		{ID: "openai-responses-output", Name: "OpenAI Responses Output", Protocol: domain.ProtocolOpenAIResponses, BasePath: "/openai/v1", ListenHost: "127.0.0.1", ListenPort: 18093, StreamEnabled: true},
		// BasePath is the client Base URL prefix (Claude Code appends /v1/messages itself).
		{ID: "claude-output", Name: "Claude Output", Protocol: domain.ProtocolClaude, BasePath: "/anthropic", ListenHost: "127.0.0.1", ListenPort: 18093, StreamEnabled: true},
	}
}
