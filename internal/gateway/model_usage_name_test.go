package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestResolveRequestLogModelForUsageUsesRouteAndKey(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID:           "p1",
			DefaultModel: "claude-opus-4-8",
			Models:       []domain.Model{{ID: "claude-opus-4-8"}, {ID: "claude-sonnet-5"}},
		}},
		Routes: []domain.Route{{ID: "r1", ProviderID: "p1"}},
		APIKeys: []domain.APIKey{{
			ID:            "luca",
			Name:          "luca",
			RouteID:       "r1",
			ModelOverride: "claude-opus-4-8",
			ModelAliases: map[string]string{
				"luca-claude-sonnet-5": "claude-sonnet-5",
			},
		}},
	})
	keysByID := map[string]domain.APIKey{"luca": router.State().APIKeys[0]}
	keysByName := map[string]domain.APIKey{"luca": router.State().APIKeys[0]}

	log := monitor.RequestLog{
		Model:    "luca-opus",
		RouteID:  "r1",
		APIKeyID: "luca",
	}
	if got := resolveRequestLogModelForUsage(router, log, keysByID, keysByName); got != "claude-opus-4-8" {
		t.Fatalf("override resolve got %q", got)
	}

	log.Model = "luca-claude-sonnet-5 -> claude-opus-4-8"
	if got := resolveRequestLogModelForUsage(router, log, keysByID, keysByName); got != "claude-opus-4-8" {
		t.Fatalf("arrow form got %q", got)
	}

	log.Model = "your-model"
	if got := resolveRequestLogModelForUsage(router, log, keysByID, keysByName); got != "claude-opus-4-8" {
		t.Fatalf("placeholder with override got %q", got)
	}
}

func TestRewriteRequestLogModelsForUsage(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID:     "p1",
			Models: []domain.Model{{ID: "claude-sonnet-5"}, {ID: "claude-opus-4-8"}},
		}},
		Routes: []domain.Route{{ID: "r1", ProviderID: "p1"}},
		APIKeys: []domain.APIKey{{
			ID:     "k1",
			Name:   "k1",
			RouteID: "r1",
			ModelAliases: map[string]string{
				"luca-claude-sonnet-5": "claude-sonnet-5",
			},
		}},
	})
	logs := []monitor.RequestLog{
		{Model: "luca-claude-sonnet-5", RouteID: "r1", APIKeyID: "k1"},
		{Model: "luca-claude-sonnet-5 -> claude-opus-4-8", RouteID: "r1", APIKeyID: "k1"},
	}
	out := rewriteRequestLogModelsForUsage(router, logs)
	if out[0].Model != "claude-sonnet-5" {
		t.Fatalf("plain alias rewrite got %q", out[0].Model)
	}
	if out[1].Model != "claude-opus-4-8" {
		t.Fatalf("arrow rewrite got %q", out[1].Model)
	}
}
