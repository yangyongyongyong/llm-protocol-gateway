package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestEnsureBuiltinCustomProviders(t *testing.T) {
	state := domain.GatewayState{}
	EnsureBuiltinCustomProviders(&state)
	if len(state.CustomProviders) != 1 {
		t.Fatalf("builtin custom providers = %d, want 1", len(state.CustomProviders))
	}
	provider := state.CustomProviders[0]
	if provider.ID != builtinTencentCloudBaseID || !provider.Builtin {
		t.Fatalf("unexpected builtin provider: %+v", provider)
	}
	if provider.Script.Language != "python" || provider.Script.Entrypoint != "adapter.py" {
		t.Fatalf("unexpected script definition: %+v", provider.Script)
	}
	if !strings.Contains(provider.Script.Source, "class CloudBaseHandler") {
		t.Fatal("embedded adapter source is empty or invalid")
	}
	// 用户要求的“不要数量拦截”：内置脚本不能带旧的上限常量。
	for _, forbidden := range []string{"MAX_REQUEST_BODY_BYTES", "MAX_MESSAGE_JSON_CHARS", "MAX_TOTAL_MESSAGE_JSON_CHARS", "too many messages"} {
		if strings.Contains(provider.Script.Source, forbidden) {
			t.Fatalf("builtin adapter still contains %q", forbidden)
		}
	}

	// 重复调用不应重复注册。
	EnsureBuiltinCustomProviders(&state)
	if len(state.CustomProviders) != 1 {
		t.Fatalf("builtin providers duplicated: %d", len(state.CustomProviders))
	}
	// 旧定义应被二进制内的新版本覆盖。
	state.CustomProviders[0].Script.Source = "outdated"
	EnsureBuiltinCustomProviders(&state)
	if strings.Contains(state.CustomProviders[0].Script.Source, "outdated") {
		t.Fatal("builtin provider was not refreshed from binary")
	}
}

func TestBuiltinCustomProviderIsImmutable(t *testing.T) {
	state := domain.GatewayState{}
	EnsureBuiltinCustomProviders(&state)
	router := NewRouter(state)

	if err := router.DeleteCustomProvider(builtinTencentCloudBaseID); err == nil {
		t.Fatal("builtin provider must not be deletable")
	}
	if _, err := router.UpdateCustomProvider(builtinTencentCloudBaseID, domain.CustomProvider{Name: "renamed"}); err == nil {
		t.Fatal("builtin provider must not be editable")
	}
	if _, err := router.AddCustomProvider(domain.CustomProvider{ID: builtinTencentCloudBaseID, Name: "duplicate"}); err == nil {
		t.Fatal("builtin provider ID must not be re-creatable")
	}
}

func TestUpgradeLegacyTencentCloudBaseProviders(t *testing.T) {
	state := domain.GatewayState{Providers: []domain.Provider{
		{
			ID:           "腾讯云-tokenhub",
			Name:         "腾讯云 TokenHub",
			Protocol:     domain.ProtocolOpenAIResponses,
			BaseURL:      "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy",
			AuthType:     domain.AuthTypeAPIKey,
			AuthHeader:   "Authorization",
			APIKeySource: "literal:cloudbase-key",
			Models: []domain.Model{{
				ID: "hy3", ProviderID: "腾讯云-tokenhub", Protocol: domain.ProtocolOpenAIResponses,
			}},
		},
		{
			ID:       "other",
			Name:     "Other",
			Protocol: domain.ProtocolOpenAIResponses,
			BaseURL:  "https://relay.example.com/v1",
		},
	}}
	EnsureBuiltinCustomProviders(&state)

	upgraded, err := UpgradeLegacyTencentCloudBaseProviders(&state)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if upgraded != 1 {
		t.Fatalf("upgraded = %d, want 1", upgraded)
	}
	provider := state.Providers[0]
	if provider.ProviderScript == nil || provider.CustomProviderID != builtinTencentCloudBaseID {
		t.Fatalf("provider was not attached to builtin connector: %+v", provider)
	}
	if provider.ProviderScript.UpstreamBaseURL != "https://dev.api.tcloudbasegateway.com/v1/functions/aiProxy" {
		t.Fatalf("upstream URL was not preserved: %q", provider.ProviderScript.UpstreamBaseURL)
	}
	if !strings.HasPrefix(provider.BaseURL, "http://127.0.0.1:") || !strings.HasSuffix(provider.BaseURL, "/v1") {
		t.Fatalf("runtime base URL = %q", provider.BaseURL)
	}
	if provider.Protocol != domain.ProtocolOpenAIChat || provider.DefaultModel != "hy3" {
		t.Fatalf("provider protocol/default were not normalized: %+v", provider)
	}
	if got := strings.Join(provider.ProtocolSupport, ","); got != "openai_chat,openai_responses,claude" {
		t.Fatalf("protocol support = %q", got)
	}
	if strings.TrimSpace(provider.ProtocolProbedAt) == "" {
		t.Fatal("protocol probe timestamp was not set")
	}
	if len(provider.Models) != 1 || provider.Models[0].Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("models were not normalized: %+v", provider.Models)
	}
	if state.Providers[1].ProviderScript != nil {
		t.Fatalf("unrelated provider was migrated: %+v", state.Providers[1])
	}

	upgradedAgain, err := UpgradeLegacyTencentCloudBaseProviders(&state)
	if err != nil || upgradedAgain != 0 {
		t.Fatalf("second upgrade = %d, %v; want idempotent 0", upgradedAgain, err)
	}
}
