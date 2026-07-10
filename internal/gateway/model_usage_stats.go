package gateway

import (
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func isUsagePlaceholderModel(model string) bool {
	_, ok := monitor.NormalizeModelForStats(model)
	return !ok && strings.TrimSpace(model) != ""
}

func lookupLogAPIKey(log monitor.RequestLog, keysByID, keysByName map[string]domain.APIKey) (domain.APIKey, bool) {
	if log.APIKeyID != "" {
		if key, ok := keysByID[log.APIKeyID]; ok {
			return key, true
		}
	}
	if name := strings.TrimSpace(log.APIKeyName); name != "" {
		if key, ok := keysByName[strings.ToLower(name)]; ok {
			return key, true
		}
	}
	return domain.APIKey{}, false
}

// rewriteRequestLogModelsForUsage re-resolves each log's model the same way as
// live requests (route + API key + aliases + override) so rankings only show
// upstream model ids, not client aliases or UI placeholders.
func rewriteRequestLogModelsForUsage(router *Router, logs []monitor.RequestLog) []monitor.RequestLog {
	if len(logs) == 0 {
		return logs
	}
	state := router.State()
	keysByID := make(map[string]domain.APIKey, len(state.APIKeys))
	keysByName := make(map[string]domain.APIKey, len(state.APIKeys))
	for _, key := range state.APIKeys {
		keysByID[key.ID] = key
		if name := strings.TrimSpace(key.Name); name != "" {
			keysByName[strings.ToLower(name)] = key
		}
	}
	out := make([]monitor.RequestLog, len(logs))
	copy(out, logs)
	for i := range out {
		out[i].Model = resolveRequestLogModelForUsage(router, out[i], keysByID, keysByName)
	}
	return out
}

func resolveRequestLogModelForUsage(router *Router, log monitor.RequestLog, keysByID, keysByName map[string]domain.APIKey) string {
	raw := strings.TrimSpace(log.Model)
	if raw == "" {
		return "_unknown"
	}
	// Historical rows stored "alias -> real-model"; the right side is already upstream id.
	if left, right, ok := strings.Cut(raw, "->"); ok {
		if real := strings.TrimSpace(right); real != "" && !isUsagePlaceholderModel(real) {
			return real
		}
		raw = strings.TrimSpace(left)
		if raw == "" {
			return "_unknown"
		}
	}

	route, routeOK := domain.Route{}, false
	if log.RouteID != "" {
		if resolved, err := router.RouteByID(log.RouteID); err == nil {
			route, routeOK = resolved, true
		}
	}

	key, keyMatched := lookupLogAPIKey(log, keysByID, keysByName)
	if routeOK {
		model, _ := resolveConsumerModel(router, route, key, keyMatched, raw)
		if model != "" && !isUsagePlaceholderModel(model) {
			return model
		}
	}

	if isUsagePlaceholderModel(raw) {
		return "_unknown"
	}
	if mapped := resolveClaudeModelAlias(raw); mapped != raw && mapped != "" {
		return mapped
	}
	return raw
}
