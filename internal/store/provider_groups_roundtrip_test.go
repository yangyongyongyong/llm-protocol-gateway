package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Provider 分组与归属通过 settings 持久化，重启后必须完整恢复；它们只是管理员
// 归类元数据，不能影响 Provider 本身的顺序或字段。
func TestProviderGroupsRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	state := domain.GatewayState{
		ProviderGroups: []domain.ProviderGroup{
			{ID: "openai", Name: "OpenAI", Position: 0},
			{ID: "claude", Name: "Claude", Position: 1},
		},
		Providers: []domain.Provider{
			{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://p1.example/v1", GroupID: "openai"},
			{ID: "p2", Name: "p2", Protocol: domain.ProtocolClaude, BaseURL: "https://p2.example/v1", GroupID: "claude"},
		},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ProviderGroups) != 2 || loaded.ProviderGroups[0].ID != "openai" || loaded.ProviderGroups[1].ID != "claude" {
		t.Fatalf("groups=%+v", loaded.ProviderGroups)
	}
	if len(loaded.Providers) != 2 || loaded.Providers[0].GroupID != "openai" || loaded.Providers[1].GroupID != "claude" {
		t.Fatalf("providers=%+v", loaded.Providers)
	}
}
