package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestResolveProviderChatURLTuyaDeployment(t *testing.T) {
	provider := domain.Provider{
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "http://ocean-dev.tuya-in.net/openai/deployments/{model}/chat/completions?api-version=2024-02-01",
	}
	got := resolveProviderChatURL(provider, "gpt-5.5")
	want := "http://ocean-dev.tuya-in.net/openai/deployments/gpt-5.5/chat/completions?api-version=2024-02-01"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveProviderChatURLStandardOpenAI(t *testing.T) {
	provider := domain.Provider{
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://api.deepseek.com",
	}
	got := resolveProviderChatURL(provider, "deepseek-chat")
	want := "https://api.deepseek.com/chat/completions"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDeriveModelsURLAnthropicOAuthBase(t *testing.T) {
	provider := domain.Provider{
		Protocol: domain.ProtocolClaude,
		BaseURL:  "https://api.anthropic.com",
	}
	got, err := deriveModelsURL(provider)
	if err != nil {
		t.Fatalf("deriveModelsURL: %v", err)
	}
	if got != "https://api.anthropic.com/v1/models" {
		t.Fatalf("got %q want %q", got, "https://api.anthropic.com/v1/models")
	}
}
