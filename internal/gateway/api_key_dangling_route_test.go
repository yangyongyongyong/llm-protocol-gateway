package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// A key whose route was deleted (its provider was purged) must still accept edits
// to its other fields. The console posts the whole scheme snapshot, so the dead
// routeId comes back with every save; validating an unchanged id turned that into
// "route not found" and blocked the only repair path — saving a fallback, then
// promoting it to rebuild the route.
func TestUpdateAPIKeyToleratesItsOwnDanglingRoute(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p-new", Name: "new", AuthType: domain.AuthTypeAPIKey}},
		APIKeys: []domain.APIKey{{
			ID:      "k1",
			Name:    "luca-glm",
			RouteID: "route-that-was-deleted",
		}},
	})

	updated, err := router.UpdateAPIKey("k1", domain.APIKey{
		// The console echoes the current routeId back on every save.
		RouteID:             "route-that-was-deleted",
		FallbackProviderIDs: []string{"p-new"},
		// 没锁模型也该存得下：备选自己的目录会给出默认模型，缺一个锁定值
		// 不该让整次保存被拒。
		FallbackModelOverrides: map[string]string{"p-new": "glm-5.2"},
	})
	if err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}
	if len(updated.FallbackProviderIDs) != 1 || updated.FallbackProviderIDs[0] != "p-new" {
		t.Fatalf("fallbacks = %v, want the one that was saved", updated.FallbackProviderIDs)
	}
	if updated.FallbackModelOverrides["p-new"] != "glm-5.2" {
		t.Fatalf("overrides = %v, want the pinned model kept", updated.FallbackModelOverrides)
	}
	if updated.RouteID != "route-that-was-deleted" {
		t.Fatalf("routeId = %q, want the unchanged value left alone", updated.RouteID)
	}
}

// 首选失效时，备选没有锁定模型也该存得下：链路的可用性不该取决于有没有填
// 一个覆盖值——备选自己的模型目录会兜底（applyProviderModelFallback）。
func TestUpdateAPIKeyAcceptsAModelLessBackupForADanglingPrimary(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p-new", Name: "new", AuthType: domain.AuthTypeAPIKey}},
		APIKeys:   []domain.APIKey{{ID: "k1", Name: "k", RouteID: "route-that-was-deleted"}},
	})

	updated, err := router.UpdateAPIKey("k1", domain.APIKey{
		RouteID:             "route-that-was-deleted",
		FallbackProviderIDs: []string{"p-new"},
	})
	if err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}
	if len(updated.FallbackProviderIDs) != 1 {
		t.Fatalf("fallbacks = %v, want p-new kept", updated.FallbackProviderIDs)
	}
}

// Pointing the key at a route that does not exist is still refused: that would
// be a new dangling reference, not an inherited one.
func TestUpdateAPIKeyStillRefusesANewMissingRoute(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Routes:  []domain.Route{{ID: "r-live", ProviderID: "p1", OutputProtocol: domain.ProtocolOpenAIChat}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "k", RouteID: "r-live"}},
	})

	if _, err := router.UpdateAPIKey("k1", domain.APIKey{RouteID: "r-missing"}); err == nil {
		t.Fatal("want an error when rebinding to a route that does not exist")
	}
}
