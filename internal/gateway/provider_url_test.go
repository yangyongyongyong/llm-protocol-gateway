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

// TestOceanDevTemplateModelCandidates 验证 Ocean Dev 的 deployment 模板会推导
// 统一目录候选；真正的 host 判定由 isOceanDevProvider 单测覆盖，避免测试把
// 127.0.0.1 误当成 Ocean 主机。
func TestOceanDevTemplateModelCandidates(t *testing.T) {
	got := templateModelBaseCandidates(oceanDevTestBaseURL)
	want := []string{"http://ocean-dev.tuya-in.net/openai/v1", "http://ocean-dev.tuya-in.net/openai"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}

// TestResolveProviderResponsesURLDoesNotRewriteDeploymentTemplate 验证通用
// Responses URL 构造不被 Ocean 的 deployment 特例污染。Ocean 本身只按 Chat
// 使用，Responses 客户端会在转换入口先变成 Chat，不会走到这里。
func TestResolveProviderResponsesURLDoesNotRewriteDeploymentTemplate(t *testing.T) {
	provider := domain.Provider{
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  oceanDevTestBaseURL,
	}
	got := resolveProviderResponsesURL(provider, "gpt-5.4-pro")
	want := "http://ocean-dev.tuya-in.net/openai/deployments/gpt-5.4-pro/chat/completions?api-version=2024-02-01"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestResolveProviderResponsesURLKeepsChatEndpointForNonResponsesProvider
// 验证通用逻辑也不再偷偷把 /chat/completions 换成 /responses。
func TestResolveProviderResponsesURLKeepsChatEndpointForNonResponsesProvider(t *testing.T) {
	provider := domain.Provider{
		Protocol: domain.ProtocolOpenAIChat,
		BaseURL:  "https://relay.example/v1/chat/completions",
	}
	got := resolveProviderResponsesURL(provider, "gpt-5.5")
	want := "https://relay.example/v1/chat/completions"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
