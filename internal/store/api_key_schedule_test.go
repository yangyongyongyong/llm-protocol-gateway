package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The scheduling policy is routing state: if the save/load pair drops it, the
// key silently falls back to "order" on the next restart and traffic that was
// being spread across accounts piles back onto the preferred one.
func TestAPIKeyScheduleModeRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	state := domain.GatewayState{
		APIKeys: []domain.APIKey{
			{ID: "k1", Name: "k1", Key: "sk-k1", RouteID: "r1", Enabled: true, ScheduleMode: "quota",
				Profiles: []domain.KeyProfile{{ID: "pf1", Name: "scheme", RouteID: "r1", ScheduleMode: "least_used", StreamEnabled: true}}},
			{ID: "k2", Name: "k2", Key: "sk-k2", RouteID: "r1", Enabled: true},
		},
	}
	// Keys live in their own rows written by Create/UpdateAPIKey: the full-state
	// Save only rewrites providers, models and routes.
	for _, key := range state.APIKeys {
		if err := store.CreateAPIKey(key); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(domain.GatewayState{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.APIKey{}
	for _, key := range loaded.APIKeys {
		byID[key.ID] = key
	}
	if got := byID["k1"].ScheduleMode; got != "quota" {
		t.Fatalf("expected the mode to survive a restart, got %q", got)
	}
	if len(byID["k1"].Profiles) != 1 || byID["k1"].Profiles[0].ScheduleMode != "least_used" {
		t.Fatalf("a profile's mode must survive too, got %#v", byID["k1"].Profiles)
	}
	if got := byID["k2"].ScheduleMode; got != "" {
		t.Fatalf("an unset mode must load as the default order, got %q", got)
	}

	// And through the update path the failover/recovery writes use.
	update := byID["k2"]
	update.ScheduleMode = "rotate"
	if err := store.UpdateAPIKey(update); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range reloaded {
		if key.ID == "k2" && key.ScheduleMode != "rotate" {
			t.Fatalf("UpdateAPIKey lost the mode, got %q", key.ScheduleMode)
		}
	}
}

// api_keys is written and read through four positional lists (SELECT / Scan /
// INSERT / UPDATE). CodexForceFast is the standing proof that missing one silently
// loses a field across a restart, and a lost quota is worse than a lost flag: the
// key starts spending again the morning after.
func TestAPIKeyQuotaRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	key := domain.APIKey{ID: "kq", Name: "kq", Key: "sk-kq", RouteID: "r1", Enabled: true,
		StreamEnabled: true, QuotaWindow: "month", QuotaTokens: 2_000_000, QuotaCostMicros: 12_500_000}
	if err := store.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one key, got %d", len(loaded))
	}
	if got := loaded[0]; got.QuotaWindow != "month" || got.QuotaTokens != 2_000_000 || got.QuotaCostMicros != 12_500_000 {
		t.Fatalf("CreateAPIKey lost the quota: %+v", got)
	}

	// The update path must be able to change it, and to clear it.
	updated := loaded[0]
	updated.QuotaWindow, updated.QuotaTokens, updated.QuotaCostMicros = "day", 500, 0
	if err := store.UpdateAPIKey(updated); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded[0]; got.QuotaWindow != "day" || got.QuotaTokens != 500 || got.QuotaCostMicros != 0 {
		t.Fatalf("UpdateAPIKey lost the quota: %+v", got)
	}
}

// Provider rows may repeat, and their model locks are positional. Losing
// FallbackModels on restart would silently collapse a p1/p2/p3 menu back to
// "same provider, same model" even though the ids still look intact.
func TestAPIKeyProviderRowsAndModelsRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	key := domain.APIKey{
		ID: "kp", Name: "kp", Key: "sk-kp", RouteID: "r1", Enabled: true,
		FallbackProviderIDs: []string{"shared", "shared", "other"},
		FallbackModels:      []string{"model-a", "", "model-c"},
		ScheduleMode:        "provider_index",
	}
	if err := store.CreateAPIKey(key); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.APIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one key, got %d", len(loaded))
	}
	got := loaded[0]
	if len(got.FallbackProviderIDs) != 3 || got.FallbackProviderIDs[0] != "shared" || got.FallbackProviderIDs[1] != "shared" || got.FallbackProviderIDs[2] != "other" {
		t.Fatalf("duplicate rows lost: %v", got.FallbackProviderIDs)
	}
	if len(got.FallbackModels) != 3 || got.FallbackModels[0] != "model-a" || got.FallbackModels[1] != "" || got.FallbackModels[2] != "model-c" {
		t.Fatalf("positional models lost: %v", got.FallbackModels)
	}
	if got.ScheduleMode != "provider_index" {
		t.Fatalf("schedule mode = %q", got.ScheduleMode)
	}
}
