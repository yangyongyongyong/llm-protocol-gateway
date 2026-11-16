package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// ProviderScript 通过 settings 持久化，重启后必须完整恢复；脚本正文和
// 私有上游参数都不能在 Provider 主表迁移中被静默丢弃。
func TestProviderScriptRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	state := domain.GatewayState{Providers: []domain.Provider{{
		ID: "script-ocean", Name: "Ocean Script", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "http://127.0.0.1:19001", APIKeySource: "literal:sk-vendor",
		CustomProviderID: "ocean",
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: "print('adapter')",
			UpstreamBaseURL: "https://vendor.example", Requirements: "requests==2.32.0",
			ListenPort: 19001, MaxConcurrentRequests: 4, RequestTimeoutSec: 90,
		},
	}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Providers) != 1 || loaded.Providers[0].ProviderScript == nil {
		t.Fatalf("script provider not restored: %+v", loaded.Providers)
	}
	got := loaded.Providers[0].ProviderScript
	if got.Source != "print('adapter')" || got.UpstreamBaseURL != "https://vendor.example" || got.MaxConcurrentRequests != 4 || loaded.Providers[0].CustomProviderID != "ocean" {
		t.Fatalf("script fields = %+v", got)
	}
}

// CustomProvider 类型模板同样通过 settings 持久化；它和 Provider 实例分开，
// 重载后必须保留脚本、协议与鉴权头，目录才能继续显示 ocean 之类的类型。
func TestCustomProviderRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state := domain.GatewayState{CustomProviders: []domain.CustomProvider{{
		ID: "ocean", Name: "ocean", Protocol: domain.ProtocolOpenAIChat, AuthHeader: "api-key",
		Script: domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')",
			MaxConcurrentRequests: 4, RequestTimeoutSec: 120,
		},
	}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.CustomProviders) != 1 {
		t.Fatalf("custom providers = %+v", loaded.CustomProviders)
	}
	got := loaded.CustomProviders[0]
	if got.ID != "ocean" || got.AuthHeader != "api-key" || got.Script.Source != "print('ocean')" || got.Script.MaxConcurrentRequests != 4 {
		t.Fatalf("custom provider fields = %+v", got)
	}
}
