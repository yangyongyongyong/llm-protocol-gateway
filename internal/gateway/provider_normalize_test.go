package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestNormalizeProviderForcesCursorOAuthToOpenAIChat(t *testing.T) {
	provider := domain.Provider{
		ID:       "cursor-pro",
		Name:     "Cursor Pro",
		AuthType: domain.AuthTypeCursorOAuth,
		Protocol: domain.ProtocolOpenAIResponses,
		BaseURL:  "https://example.invalid",
	}
	normalizeProvider(&provider)
	if provider.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("expected openai_chat, got %q", provider.Protocol)
	}
	if provider.BaseURL != "" {
		t.Fatalf("expected empty base URL for cursor oauth, got %q", provider.BaseURL)
	}
}

func TestNormalizeProviderForcesClaudeOAuthToClaude(t *testing.T) {
	provider := domain.Provider{
		ID:       "claude-pro",
		Name:     "Claude Pro",
		AuthType: domain.AuthTypeClaudeOAuth,
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://example.invalid",
	}
	normalizeProvider(&provider)
	if provider.Protocol != domain.ProtocolClaude {
		t.Fatalf("expected claude, got %q", provider.Protocol)
	}
	if provider.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("unexpected base URL: %q", provider.BaseURL)
	}
}

func TestNormalizeProviderDefaultsQoderBaseURL(t *testing.T) {
	provider := domain.Provider{
		ID:       "qoder",
		Name:     "Qoder",
		AuthType: domain.AuthTypeQoderPAT,
		Protocol: domain.ProtocolClaude,
	}
	normalizeProvider(&provider)
	if provider.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("expected openai_chat, got %q", provider.Protocol)
	}
	if provider.BaseURL != qoderDirectBaseURL {
		t.Fatalf("unexpected base URL: %q", provider.BaseURL)
	}
}

// The create form prefills example.com. A provider switched to Qoder must not
// keep sending requests there, so the placeholder counts as unset.
func TestNormalizeProviderReplacesQoderPlaceholderBaseURL(t *testing.T) {
	provider := domain.Provider{
		ID:       "qoder-placeholder",
		Name:     "Qoder",
		AuthType: domain.AuthTypeQoderPAT,
		BaseURL:  "https://example.com/v1/chat/completions",
	}
	normalizeProvider(&provider)
	if provider.BaseURL != qoderDirectBaseURL {
		t.Fatalf("placeholder base URL survived: %q", provider.BaseURL)
	}
}

// Unlike the Claude/ChatGPT clauses, Qoder's base URL is only defaulted: a
// manual override must survive so an upstream endpoint change needs a config
// edit rather than a rebuild.
func TestNormalizeProviderKeepsManualQoderBaseURL(t *testing.T) {
	provider := domain.Provider{
		ID:       "qoder-custom",
		Name:     "Qoder Custom",
		AuthType: domain.AuthTypeQoderPAT,
		BaseURL:  "https://qoder.example.invalid/model/v1",
	}
	normalizeProvider(&provider)
	if provider.BaseURL != "https://qoder.example.invalid/model/v1" {
		t.Fatalf("manual base URL was overwritten: %q", provider.BaseURL)
	}
}

func TestNormalizeProviderForcesChatGPTOAuthToOpenAIResponses(t *testing.T) {
	provider := domain.Provider{
		ID:       "chatgpt-pro",
		Name:     "ChatGPT Pro",
		AuthType: domain.AuthTypeChatGPTOAuth,
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://example.invalid",
	}
	normalizeProvider(&provider)
	if provider.Protocol != domain.ProtocolOpenAIResponses {
		t.Fatalf("expected openai_responses, got %q", provider.Protocol)
	}
	if provider.BaseURL != chatgptCodexResponsesURL {
		t.Fatalf("unexpected base URL: %q", provider.BaseURL)
	}
}
