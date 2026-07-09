package gateway

import "strings"

var claudeModelAliases = map[string]string{
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-4-8",
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

func claudeModelAliasEntries() []map[string]any {
	entries := make([]map[string]any, 0, len(claudeModelAliases))
	for alias, target := range claudeModelAliases {
		entries = append(entries, map[string]any{
			"id":           alias,
			"type":         "model",
			"display_name": target,
			"created_at":   "",
		})
	}
	return entries
}
