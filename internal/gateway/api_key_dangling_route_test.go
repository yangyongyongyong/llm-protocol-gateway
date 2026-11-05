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
		// With no resolvable primary the console requires a fixed model for the
		// backup (see fallbackModelRequiredLocked), and offers a picker for it.
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

// The runtime rule stays: a backup whose protocol cannot be compared with the
// primary must pin a model. Saving one without it is still refused.
func TestUpdateAPIKeyStillRequiresAModelForADanglingPrimary(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p-new", Name: "new", AuthType: domain.AuthTypeAPIKey}},
		APIKeys:   []domain.APIKey{{ID: "k1", Name: "k", RouteID: "route-that-was-deleted"}},
	})

	if _, err := router.UpdateAPIKey("k1", domain.APIKey{
		RouteID:             "route-that-was-deleted",
		FallbackProviderIDs: []string{"p-new"},
	}); err == nil {
		t.Fatal("want an error: a backup with no comparable primary must pin a model")
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
