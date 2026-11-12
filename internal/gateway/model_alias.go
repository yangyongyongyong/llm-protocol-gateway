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

// defaultModelAlias is the reserved model name a client sends when it wants the
// platform to pick the model. It exists so a Claude Code or Codex session can pin
// the reserved alias once and never touch its config again: the client stops
// naming a model, and switching the model on the gateway (per API key, or per
// provider) takes effect on the next request without a client restart. Gateway-
// generated client configs default to the 1M spelling, "default[1m]".
//
// "default" is not a model id any provider serves, so it can never collide with
// a real one. Claude Code also spells it "default[1m]" when the user wants the
// 1M-context variant; see resolveReservedModelAlias.
const defaultModelAlias = "default"

// resolveReservedModelAlias answers whether the client asked for the reserved
// "default" alias and, if so, which model should serve it.
//
// The trailing "[1m]" is Claude Code's extended-context marker, not part of any
// model id: the client strips it before sending (byte-verified against 2.1.322)
// and signals the same thing with the anthropic-beta: context-1m-2025-08-07
// header. Both spellings therefore resolve to the same upstream model — the
// marker only decides which candidate is allowed to serve the request.
//
// A provider that has a DefaultModel gets it. A provider without one is left
// alone: the request goes upstream naming "default" and the provider's own
// error reaches the client. That is deliberate — configuring a default model is
// optional on this gateway, because naming the model in the client is a
// supported way to use it, and inventing a substitute (catalog order, first
// entry) would silently run a model the user never chose.
func resolveReservedModelAlias(provider domain.Provider, requestModel string) (model string, mapped bool) {
	if !isReservedDefaultModel(requestModel) {
		return "", false
	}
	if defaultModel := strings.TrimSpace(provider.DefaultModel); defaultModel != "" {
		return defaultModel, true
	}
	return "", false
}

// isReservedDefaultModel reports whether a model name is (any spelling of) the
// reserved "default" alias, including Claude Code's "[1m]" extended-context tag.
func isReservedDefaultModel(model string) bool {
	return strings.EqualFold(stripClaudeContextTag(model), defaultModelAlias)
}

// claudeContextTag1M 是 Claude Code / Claude Desktop 用来标记「要 1M 窗口」的
// 后缀。它是窗口标记而不是模型名的一部分：本方对 Desktop 列 1M 模型时也用它
// （见 desktopModelView），请求回来时必须能去掉。
const claudeContextTag1M = "[1m]"

// stripClaudeContextTag removes the trailing extended-context markers Claude
// Code appends to a model name ("[1m]", "[2m]"). Mirrors the client's own
// normalization: other bracketed text is left alone so a real model id that
// happens to contain brackets survives.
func stripClaudeContextTag(model string) string {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	for _, tag := range []string{claudeContextTag1M, "[2m]"} {
		if strings.HasSuffix(lower, tag) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(tag)])
		}
	}
	return trimmed
}

func resolveConsumerModel(router *Router, route domain.Route, key domain.APIKey, gatewayKeyMatched bool, rawRequestModel string) (model string, logModel string) {
	provider, err := router.ProviderForRoute(route)
	if err != nil {
		provider = domain.Provider{}
	}
	return resolveConsumerModelOnProvider(provider, key, gatewayKeyMatched, rawRequestModel)
}

// resolveConsumerModelOnProvider resolves the model for one attempt against the
// provider that will actually receive it. Failover reuses the request model for
// same-protocol backups, so mappings and defaults must come from that backup
// rather than from the route's preferred provider.
func resolveConsumerModelOnProvider(provider domain.Provider, key domain.APIKey, gatewayKeyMatched bool, rawRequestModel string) (model string, logModel string) {
	clientModel := strings.TrimSpace(rawRequestModel)
	// 日志左侧写的是请求体里字面的值，下面几步会改写 clientModel（别名反查、
	// 去 [1m] 标记），所以先把原话留一份。
	saidModel := clientModel
	// Claude Desktop 只能列出读起来像 Anthropic 的模型 id，其余模型它拿到的是
	// anthropic/gw-<哈希> 别名（见 desktop_models.go）。这里把别名换回真实
	// 模型，后续的映射/默认值/上游请求就都按真实 id 走。
	// 反查依据是这次请求能看到的模型集合，所以撤销授权后别名自动失效。
	if resolved := desktopModelForAlias(clientModel, desktopAliasCandidates(provider, key, gatewayKeyMatched)); resolved != clientModel {
		clientModel = resolved
	}
	// "[1m]" 是「要 1M 窗口」的标记，不是模型名的一部分：我们对 Desktop 列 1M
	// 模型时就用它（见 desktopModelView），Claude Code 发请求时也带着它。目录里
	// 没有这个带标记的 id 时按去掉标记的那一个走——否则 applyProviderModelFallback
	// 认不出来，会把请求悄悄换成默认模型。目录里真有这个带标记的 id（有的中转站
	// 就这么卖）时保留原样，那是一个真实的模型名。
	if trimmed := stripClaudeContextTag(clientModel); trimmed != clientModel && !providerHasModel(provider, clientModel) {
		clientModel = trimmed
	}
	working := resolveClaudeModelAlias(clientModel)
	mapped := false
	if gatewayKeyMatched {
		working, mapped = resolveAPIKeyModelAlias(key.ModelAliases, working)
	}
	override := ""
	if gatewayKeyMatched {
		override = strings.TrimSpace(key.ModelOverride)
	}
	// Resolution order:
	// APIKey.ModelOverride → APIKey.modelAliases → provider.requestAdapter.modelMapping
	// → if not in provider.Models use DefaultModel → else clientModel
	model = override
	if model == "" {
		model = working
	}
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if override == "" && provider.ID != "" {
		if !mapped {
			mappedModel := applyProviderModelMapping(provider, model)
			if mappedModel != model {
				model = mappedModel
				mapped = true
			}
		} else {
			model = applyProviderModelMapping(provider, model)
		}
		// Reserved "default" (and Claude Code's "default[1m]"): the client asked
		// the platform to choose. Resolved against this provider only, because a
		// failover target may carry a different default model than the route's
		// preferred provider. Skipped when an alias or mapping already chose.
		if !mapped {
			if resolved, ok := resolveReservedModelAlias(provider, model); ok {
				model = resolved
				mapped = true
			}
		}
		model = stripProviderModelPrefix(provider, model)
		model = applyProviderModelFallback(provider, model)
	}
	if model == "" {
		model = "request-model-not-set"
	}
	// The log keeps both halves whenever they differ: the left side is literally
	// what the request body named, the right side is what went upstream. That has
	// to hold for an API key's ModelOverride too — it replaces the model just as
	// a provider mapping does, and dropping the left side there left the log
	// claiming the client had asked for the override, which it never did.
	logModel = model
	if saidModel != "" && saidModel != model {
		// 只有 "[1m]" 之差不算映射：那是同一个模型的窗口标记（见上面去标记那一
		// 段），记成 "m1[1m] -> m1" 会让人以为这里做了一次模型替换。
		if strings.EqualFold(stripClaudeContextTag(saidModel), model) {
			logModel = model
		} else {
			logModel = saidModel + " -> " + model
		}
	}
	return model, logModel
}

// finalizeRequestLogModel keeps "alias -> real" in API logs when a mapping was
// applied, while reflecting the effective upstream model (e.g. after failover).
func finalizeRequestLogModel(logModel, resolvedModel, effectiveModel string) string {
	effective := strings.TrimSpace(effectiveModel)
	if effective == "" {
		effective = strings.TrimSpace(resolvedModel)
	}
	logModel = strings.TrimSpace(logModel)
	if logModel == "" {
		return effective
	}
	if alias, _, ok := strings.Cut(logModel, " -> "); ok {
		alias = strings.TrimSpace(alias)
		if alias != "" && alias != effective {
			return alias + " -> " + effective
		}
	}
	return effective
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

// stripProviderModelPrefix drops a provider namespace a client pinned to the
// model id ("qoder-cn/dfmodel"), the way route aliases are written. Models a
// provider genuinely names with a slash are kept: the full id wins, and only a
// tail the provider itself lists is accepted as the real model.
func stripProviderModelPrefix(provider domain.Provider, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || len(provider.Models) == 0 || providerHasModel(provider, model) {
		return model
	}
	i := strings.LastIndex(model, "/")
	if i <= 0 || i == len(model)-1 {
		return model
	}
	tail := strings.TrimSpace(model[i+1:])
	if tail == "" || !providerHasModel(provider, tail) {
		return model
	}
	return tail
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
