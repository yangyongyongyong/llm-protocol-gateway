package gateway

import "strings"

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

func claudeModelAliasEntries() []map[string]any {
	entries := make([]map[string]any, 0, len(claudeModelAliases))
	for alias, target := range claudeModelAliases {
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
