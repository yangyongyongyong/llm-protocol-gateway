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
