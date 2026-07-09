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
