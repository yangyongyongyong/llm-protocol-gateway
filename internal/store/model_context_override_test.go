package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 运维设的模型上下文窗口必须真的落库：它在内存里生效、重启后没了，表现是
// 「Claude Desktop 里那个模型又从 1M 变回 128K」，而界面上不会报任何错。
//
// 它和 context_length 是两列：后者每次目录同步都会重写，所以覆盖值单独存一份。
func TestModelContextOverrideRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	provider := domain.Provider{
		ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1",
		Models: []domain.Model{
			// 有效窗口已经按覆盖值算好了（见 gateway.fillModelTokenBudgets）。
			{ID: "m1", Protocol: domain.ProtocolOpenAIChat, ContextLength: 1_000_000, ContextOverride: 1_000_000, InMenu: true},
			{ID: "m2", Protocol: domain.ProtocolOpenAIChat, ContextLength: 128_000, InMenu: true},
		},
	}
	if err := store.Save(domain.GatewayState{Providers: []domain.Provider{provider}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Providers) != 1 || len(loaded.Providers[0].Models) != 2 {
		t.Fatalf("models did not round-trip: %+v", loaded.Providers)
	}
	byID := map[string]domain.Model{}
	for _, model := range loaded.Providers[0].Models {
		byID[model.ID] = model
	}
	if got := byID["m1"]; got.ContextOverride != 1_000_000 || got.ContextLength != 1_000_000 {
		t.Fatalf("m1 override=%d window=%d, want 1M kept", got.ContextOverride, got.ContextLength)
	}
	// 没设过的模型不能被写上点什么。
	if got := byID["m2"]; got.ContextOverride != 0 || got.ContextLength != 128_000 {
		t.Fatalf("m2 override=%d window=%d, want no override", got.ContextOverride, got.ContextLength)
	}
}
