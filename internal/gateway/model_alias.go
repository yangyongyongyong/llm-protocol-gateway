package gateway

import (
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func normalizeModelAliases(aliases map[string]string) map[string]string {
	if len(aliases) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(aliases))
	for alias, target := range aliases {
		alias = strings.TrimSpace(alias)
		target = strings.TrimSpace(target)
		if alias == "" || target == "" || strings.EqualFold(alias, target) {
			continue
		}
		normalized[alias] = target
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func resolveAPIKeyModelAlias(aliases map[string]string, requestModel string) (string, bool) {
	requestModel = strings.TrimSpace(requestModel)
	if requestModel == "" || len(aliases) == 0 {
		return requestModel, false
	}
	if target, ok := aliases[requestModel]; ok {
		return strings.TrimSpace(target), true
	}
	lower := strings.ToLower(requestModel)
	for alias, target := range aliases {
		if strings.ToLower(alias) == lower {
			return strings.TrimSpace(target), true
		}
	}
	return requestModel, false
}

func resolveConsumerModel(router *Router, route domain.Route, key domain.APIKey, gatewayKeyMatched bool, rawRequestModel string) (model string, logModel string) {
	clientModel := strings.TrimSpace(rawRequestModel)
	working := resolveClaudeModelAlias(clientModel)
	mapped := false
	if gatewayKeyMatched {
		working, mapped = resolveAPIKeyModelAlias(key.ModelAliases, working)
	}
	override := ""
	if gatewayKeyMatched {
		override = key.ModelOverride
	}
	// Resolution order:
	// APIKey.ModelOverride → APIKey.modelAliases → provider.requestAdapter.modelMapping
	// → if not in provider.Models use DefaultModel → else clientModel
	model = router.ResolveModel(route, override, working)
	if override == "" {
		if provider, err := router.ProviderForRoute(route); err == nil {
			if !mapped {
				mappedModel := applyProviderModelMapping(provider, model)
				if mappedModel != model {
					model = mappedModel
					mapped = true
				}
			} else {
				model = applyProviderModelMapping(provider, model)
			}
			model = applyProviderModelFallback(provider, model)
		}
	}
	if model == "" {
		model = "request-model-not-set"
	}
	logModel = model
	if mapped && clientModel != "" && clientModel != model {
		logModel = clientModel + " -> " + model
	} else if clientModel != "" && clientModel != model && override == "" {
		logModel = clientModel + " -> " + model
	}
	return model, logModel
}

func providerHasModel(provider domain.Provider, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, item := range provider.Models {
		if strings.EqualFold(strings.TrimSpace(item.ID), model) {
			return true
		}
	}
	return false
}

// applyProviderModelFallback uses DefaultModel when the resolved model is not
// listed in provider.Models (and a default is configured). Empty Models means
// "unknown catalog" and the resolved model is kept as-is.
func applyProviderModelFallback(provider domain.Provider, resolvedModel string) string {
	resolvedModel = strings.TrimSpace(resolvedModel)
	if resolvedModel == "" || len(provider.Models) == 0 {
		return resolvedModel
	}
	if providerHasModel(provider, resolvedModel) {
		return resolvedModel
	}
	if defaultModel := strings.TrimSpace(provider.DefaultModel); defaultModel != "" {
		return defaultModel
	}
	return resolvedModel
}
