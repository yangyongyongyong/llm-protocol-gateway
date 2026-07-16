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
