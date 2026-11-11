package gateway

import (
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

var claudeModelAliases = map[string]string{
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-5",
	"haiku":  "claude-haiku-4-5-20251001",
}

func resolveClaudeModelAlias(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if mapped, ok := claudeModelAliases[strings.ToLower(model)]; ok {
		return mapped
	}
	return model
}

func isClaudeHaikuModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "haiku" || strings.Contains(model, "haiku")
}

func isClaudeOpusModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "opus" || strings.Contains(model, "opus")
}

// reservedDefaultModelEntries lists the reserved "default" alias for model
// discovery. Claude Code reads /v1/models when gateway discovery is on, and a
// model missing from the catalog still works but is reported as unrecognized;
// publishing both spellings keeps that honest and lets the 1M variant be found.
//
// The window is deliberately the generic default, not the provider's real
// model: this endpoint does not know which provider the caller's next request
// will land on, and over-reporting capacity would let a client skip compaction
// it still needs.
func reservedDefaultModelEntries() []map[string]any {
	contextLen := contextLengthDefault
	return []map[string]any{
		{
			"id":               defaultModelAlias,
			"type":             "model",
			"display_name":     defaultModelAlias,
			"created_at":       "",
			"max_input_tokens": contextLen,
			"max_tokens":       resolveModelMaxOutputTokens(defaultModelAlias, contextLen),
		},
		{
			"id":               defaultModelAlias + "[1m]",
			"type":             "model",
			"display_name":     defaultModelAlias + " (1M context)",
			"created_at":       "",
			"max_input_tokens": contextLength1M,
			"max_tokens":       resolveModelMaxOutputTokens(defaultModelAlias, contextLength1M),
		},
	}
}

// claudeModelAliasEntries publishes the sonnet/opus/haiku tier aliases that a
// caller can actually reach: an alias is listed only when the model it maps to
// is in this caller's catalog.
//
// The aliases exist because Claude Code's built-in sub-agents name their model
// in the client (claude-code-guide uses haiku, statusline-setup uses sonnet),
// so a gateway that rewrote unknown names would break them; listing the alias
// keeps discovery from reporting it as unrecognized. What it must not do is
// conjure a tier the provider does not serve: a provider offering only m1/m2
// would otherwise advertise Sonnet 5 with a 1M window, and an over-reported
// window lets the client skip compaction it still needs.
//
// This only decides what discovery shows. resolveClaudeModelAlias still rewrites
// the name on every request, so a client that pins a tier keeps working the way
// it did before.
func claudeModelAliasEntries(catalog []domain.Model) []map[string]any {
	entries := make([]map[string]any, 0, len(claudeModelAliases))
	for alias, target := range claudeModelAliases {
		if !catalogServesModel(catalog, target) {
			continue
		}
		contextLen := resolveModelContextLength(target, 0)
		entries = append(entries, map[string]any{
			"id":               alias,
			"type":             "model",
			"display_name":     target,
			"created_at":       "",
			"max_input_tokens": contextLen,
			"max_tokens":       resolveModelMaxOutputTokens(target, contextLen),
		})
	}
	return entries
}

// catalogServesModel reports whether one of the models in the catalog is the
// given id — either spelled exactly, or behind a provider namespace
// ("anthropic/claude-sonnet-5"), which is how relay providers list their models
// and what stripProviderModelPrefix already treats as the same model.
func catalogServesModel(catalog []domain.Model, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, item := range catalog {
		id := strings.ToLower(strings.TrimSpace(item.ID))
		if id == model || strings.HasSuffix(id, "/"+model) {
			return true
		}
	}
	return false
}
