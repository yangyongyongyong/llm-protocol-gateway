package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestResolveAPIKeyModelAlias(t *testing.T) {
	aliases := map[string]string{
		"opus4.8": "claude-opus-4-8",
		"sonnet5": "claude-sonnet-5",
	}

	if got, mapped := resolveAPIKeyModelAlias(aliases, "opus4.8"); got != "claude-opus-4-8" || !mapped {
		t.Fatalf("alias resolve got=%q mapped=%v", got, mapped)
	}
	if got, mapped := resolveAPIKeyModelAlias(aliases, "claude-opus-4-8"); got != "claude-opus-4-8" || mapped {
		t.Fatalf("real model passthrough got=%q mapped=%v", got, mapped)
	}
	if got, mapped := resolveAPIKeyModelAlias(aliases, "OPUS4.8"); got != "claude-opus-4-8" || !mapped {
		t.Fatalf("case-insensitive alias got=%q mapped=%v", got, mapped)
	}
}

func TestResolveConsumerModelLogsAliasMapping(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p1", DefaultModel: "default-model"}},
		Routes:    []domain.Route{{ID: "r1", ProviderID: "p1"}},
	})
	route := domain.Route{ID: "r1", ProviderID: "p1"}
	key := domain.APIKey{
		ModelAliases: map[string]string{"opus4.8": "claude-opus-4-8"},
	}

	model, logModel := resolveConsumerModel(router, route, key, true, "opus4.8")
	if model != "claude-opus-4-8" {
		t.Fatalf("model=%q", model)
	}
	if logModel != "opus4.8 -> claude-opus-4-8" {
		t.Fatalf("logModel=%q", logModel)
	}

	_, logModel = resolveConsumerModel(router, route, key, true, "claude-opus-4-8")
	if logModel != "claude-opus-4-8" {
		t.Fatalf("direct real model logModel=%q", logModel)
	}
}

func TestResolveModelOverrideAlwaysWins(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{ID: "p1", DefaultModel: "default-model"}},
		Routes:    []domain.Route{{ID: "r1", ProviderID: "p1"}},
	})
	route := domain.Route{ID: "r1", ProviderID: "p1"}

	if got := router.ResolveModel(route, "claude-opus-4-8", "claude-opus-4-7[1m]"); got != "claude-opus-4-8" {
		t.Fatalf("opus request should still use override, got %q", got)
	}
	if got := router.ResolveModel(route, "claude-opus-4-8", "claude-haiku-4-5-20251001"); got != "claude-opus-4-8" {
		t.Fatalf("haiku request should still use override, got %q", got)
	}
	if got := router.ResolveModel(route, "claude-opus-4-8", "sonnet"); got != "claude-opus-4-8" {
		t.Fatalf("sonnet alias should still use override, got %q", got)
	}
	if got := router.ResolveModel(route, "", "claude-opus-4-7[1m]"); got != "claude-opus-4-7[1m]" {
		t.Fatalf("without override request model should pass through, got %q", got)
	}
}

func TestResolveConsumerModelFallsBackToDefaultWhenNotInProviderModels(t *testing.T) {
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{{
			ID:           "tuyadev",
			DefaultModel: "gpt-5.5",
			Models:       []domain.Model{{ID: "gpt-5.5"}, {ID: "gpt-4o"}},
		}},
		Routes: []domain.Route{{ID: "r1", ProviderID: "tuyadev"}},
	})
	route := domain.Route{ID: "r1", ProviderID: "tuyadev"}
	key := domain.APIKey{}

	model, logModel := resolveConsumerModel(router, route, key, true, "claude-sonnet-5")
	if model != "gpt-5.5" {
		t.Fatalf("expected default model fallback, got %q", model)
	}
	if logModel != "claude-sonnet-5 -> gpt-5.5" {
		t.Fatalf("logModel=%q", logModel)
	}

	model, _ = resolveConsumerModel(router, route, key, true, "gpt-4o")
	if model != "gpt-4o" {
		t.Fatalf("listed model should pass through, got %q", model)
	}
}

func TestErrorMessageFromValueUsesBodyDump(t *testing.T) {
	msg, errType := errorMessageFromValue(map[string]any{"code": "DeploymentNotFound", "detail": "model missing"}, "")
	if msg != "model missing" {
		t.Fatalf("unexpected message %q", msg)
	}
	if errType != "DeploymentNotFound" {
		t.Fatalf("unexpected type %q", errType)
	}
	if !strings.Contains(msg, "model") {
		t.Fatalf("expected model in message")
	}
}
