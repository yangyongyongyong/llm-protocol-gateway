package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestResolveProviderChatURLWithAdapter(t *testing.T) {
	provider := domain.Provider{
		BaseURL: "http://ocean-dev.tuya-in.net/openai",
		RequestAdapter: &domain.RequestAdapter{
			URLTemplate:  "{baseUrl}/deployments/{model}/chat/completions?api-version=2024-02-01",
			ModelMapping: map[string]string{"claude-sonnet-5": "gpt-5.5"},
		},
	}
	got := resolveProviderChatURLWithAdapter(provider, "claude-sonnet-5")
	want := "http://ocean-dev.tuya-in.net/openai/deployments/gpt-5.5/chat/completions?api-version=2024-02-01"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveProviderChatURLAppendsResponsesPath(t *testing.T) {
	// Regression: the chat-test/conformance path only appended /chat/completions,
	// so Responses providers (e.g. DeepSeek) hit the bare base URL and 404'd.
	cases := []struct {
		name     string
		provider domain.Provider
		want     string
	}{
		{
			name:     "responses base url gets /responses",
			provider: domain.Provider{BaseURL: "https://api.deepseek.com", Protocol: domain.ProtocolOpenAIResponses},
			want:     "https://api.deepseek.com/responses",
		},
		{
			name:     "existing /responses is not duplicated",
			provider: domain.Provider{BaseURL: "https://api.deepseek.com/v1/responses", Protocol: domain.ProtocolOpenAIResponses},
			want:     "https://api.deepseek.com/v1/responses",
		},
		{
			name:     "chat protocol still gets /chat/completions",
			provider: domain.Provider{BaseURL: "https://api.deepseek.com", Protocol: domain.ProtocolOpenAIChat},
			want:     "https://api.deepseek.com/chat/completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveProviderChatURL(tc.provider, "deepseek-v4-flash"); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestApplyRequestAdapterBodyRewritesModel(t *testing.T) {
	provider := domain.Provider{
		BaseURL: "http://ocean-dev.tuya-in.net/openai/deployments/{model}/chat/completions?api-version=2024-02-01",
		RequestAdapter: &domain.RequestAdapter{
			ModelMapping: map[string]string{"claude-sonnet-5": "gpt-5.5"},
		},
	}
	body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":32}`)
	out, err := applyRequestAdapterBody(provider, "claude-sonnet-5", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("expected model rewritten to gpt-5.5, got %#v", payload["model"])
	}
}

func TestGenerateRequestAdapterCurl(t *testing.T) {
	adapter := &domain.RequestAdapter{
		URLTemplate: "{baseUrl}/chat/completions",
		Headers:     map[string]string{"api-key": "secret"},
	}
	curl := generateRequestAdapterCurl(adapter, "https://example.com/v1", "gpt-5.5", `{"model":"gpt-5.5"}`)
	if curl == "" || !containsString(curl, "https://example.com/v1/chat/completions") || !containsString(curl, "api-key: secret") {
		t.Fatalf("unexpected curl: %s", curl)
	}
}

func TestGenerateRequestAdapterCurlUsesBaseURLModelPlaceholder(t *testing.T) {
	adapter := &domain.RequestAdapter{
		ModelMapping: map[string]string{"claude-sonnet-5": "gpt-5.5"},
	}
	base := "http://ocean-dev.tuya-in.net/openai/deployments/{model}/chat/completions?api-version=2024-02-01"
	curl := generateRequestAdapterCurl(adapter, base, "claude-sonnet-5", `{"model":"claude-sonnet-5","messages":[]}`)
	if !strings.Contains(curl, "/deployments/gpt-5.5/chat/completions") {
		t.Fatalf("expected mapped model in URL, got %s", curl)
	}
	if !strings.Contains(curl, `"model":"gpt-5.5"`) {
		t.Fatalf("expected rewritten body model, got %s", curl)
	}
}

// 控制台不再展示自定义适配后，普通 Provider PATCH 不会携带 requestAdapter。
// 这种“字段缺省”的更新必须保留存量配置；只有显式 null / 空对象才清空。
func TestUpdateProviderOmitsRequestAdapterKeepsExisting(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "legacy", Name: "Legacy", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "https://example.com/v1/chat/completions",
		RequestAdapter: &domain.RequestAdapter{
			URLTemplate:  "https://example.com/v1/chat/completions",
			ModelMapping: map[string]string{"claude-sonnet-5": "gpt-5.5"},
		},
	}}})
	if _, err := router.UpdateProvider("legacy", domain.Provider{Name: "Renamed"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := router.ProviderByID("legacy")
	if got.RequestAdapter == nil || got.RequestAdapter.ModelMapping["claude-sonnet-5"] != "gpt-5.5" {
		t.Fatalf("requestAdapter lost on partial update: %#v", got.RequestAdapter)
	}
	if _, err := router.UpdateProvider("legacy", domain.Provider{RequestAdapter: &domain.RequestAdapter{}}); err != nil {
		t.Fatalf("clear update: %v", err)
	}
	got, _ = router.ProviderByID("legacy")
	if got.RequestAdapter != nil {
		t.Fatalf("requestAdapter not cleared by explicit empty object: %#v", got.RequestAdapter)
	}
}
