package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Azure deployment URL 的 {model} 模板无法通过 /models 枚举；获取模型必须
// 沿用 Provider 已配置目录，而不是把一个对话正常的账号标成 degraded。
func TestFetchProviderModelsUsesDeclaredModelsForTemplateURL(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), nil)
	provider := domain.Provider{
		ID: "azure-dev", Protocol: domain.ProtocolOpenAIChat, AuthType: domain.AuthTypeAPIKey,
		BaseURL:      "http://127.0.0.1:1/openai/deployments/{model}/chat/completions?api-version=2024-02-01",
		DefaultModel: "gpt-4o",
		Models:       []domain.Model{{ID: "gpt-4o", ProviderID: "azure-dev", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
	}
	result := server.fetchProviderModels(httptest.NewRequest(http.MethodGet, "/", nil), provider, time.Now())
	if !result.Success || len(result.Models) != 1 || result.Models[0].ID != "gpt-4o" {
		t.Fatalf("template model result = %+v", result)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Status)
	}
}

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
