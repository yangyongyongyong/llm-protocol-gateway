package gateway

import "strings"

const (
	contextLength1M      = 1_000_000
	contextLength200K    = 200_000
	contextLengthDefault = 128_000
)

// resolveModelContextLength picks a display/capacity context length for a model.
// Prefer an explicit upstream value when present, except for known vendor models
// whose capacity is well-defined and often omitted (or under-reported) by
// /models responses (Claude, 智谱 GLM / BigModel, etc.).
func resolveModelContextLength(modelID string, reported int) int {
	if known, ok := knownModelContextLength(modelID); ok {
		if reported > known {
			return reported
		}
		return known
	}
	if reported > 0 {
		return reported
	}
	return contextLengthDefault
}

func knownModelContextLength(modelID string) (int, bool) {
	raw := normalizeModelContextID(modelID)
	if raw == "" {
		return 0, false
	}
	// Claude Code / gateway convention: "[1m]" always means a 1M window.
	if strings.Contains(raw, "[1m]") {
		return contextLength1M, true
	}
	base := strings.TrimSpace(strings.Split(raw, "[")[0])
	if n, ok := knownClaudeContextLength(base); ok {
		return n, true
	}
	if n, ok := knownGLMContextLength(base); ok {
		return n, true
	}
	return 0, false
}

func normalizeModelContextID(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	id = strings.ReplaceAll(id, "_", "-")
	return id
}

// knownClaudeContextLength returns Anthropic/Claude-family context windows.
// Current flagship Opus/Sonnet generations are native 1M; older Claude 4.5-era
// models (and Haiku) remain 200k.
func knownClaudeContextLength(modelID string) (int, bool) {
	base := strings.TrimSpace(resolveClaudeModelAlias(normalizeModelContextID(modelID)))
	if !isClaudeFamilyModelID(base) {
		return 0, false
	}
	if isClaude1MContextModel(base) {
		return contextLength1M, true
	}
	return contextLength200K, true
}

func isClaudeFamilyModelID(modelID string) bool {
	id := normalizeModelContextID(modelID)
	switch id {
	case "opus", "sonnet", "haiku":
		return true
	}
	return strings.Contains(id, "claude")
}

func isClaude1MContextModel(modelID string) bool {
	id := normalizeModelContextID(modelID)
	markers := []string{
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"opus-4-8",
		"opus-4-7",
		"opus-4-6",
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"sonnet-4-6",
		"sonnet-5",
		"claude-fable-5",
		"fable-5",
		"claude-mythos-5",
		"claude-mythos-preview",
		"mythos-5",
		"mythos-preview",
	}
	for _, marker := range markers {
		if id == marker || strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

// knownGLMContextLength maps 智谱 BigModel / Z.ai GLM text(+common VLM) IDs to
// official context windows from https://docs.bigmodel.cn/cn/guide/start/model-overview
func knownGLMContextLength(modelID string) (int, bool) {
	id := normalizeModelContextID(modelID)
	if !strings.Contains(id, "glm") && !strings.Contains(id, "codegeex") {
		return 0, false
	}

	// 1M: GLM-5.2 flagship + dedicated long model.
	if strings.Contains(id, "glm-5.2") || strings.Contains(id, "glm-4-long") {
		return contextLength1M, true
	}

	// VLM / older Flash families at 128k — check before broader glm-4.6 (200k).
	if strings.Contains(id, "glm-4.6v") ||
		strings.Contains(id, "glm-4.5") ||
		strings.Contains(id, "glm-4-flash") ||
		strings.Contains(id, "glm-4.1v") ||
		strings.Contains(id, "codegeex") {
		return contextLengthDefault, true
	}

	// 200k text / coding lineup.
	if strings.Contains(id, "glm-5.1") ||
		strings.Contains(id, "glm-5-turbo") ||
		strings.Contains(id, "glm-5v") ||
		strings.Contains(id, "glm-4.7") ||
		strings.Contains(id, "glm-4.6") ||
		isGLM5BaseModelID(id) {
		return contextLength200K, true
	}

	// Unknown glm-* still treat as vendor family with the generic 128k default.
	if strings.Contains(id, "glm-") {
		return contextLengthDefault, true
	}
	return 0, false
}

// isGLM5BaseModelID matches glm-5 / glm-5-xxx but not 5.1 / 5.2 / 5v / 5-turbo.
func isGLM5BaseModelID(modelID string) bool {
	id := normalizeModelContextID(modelID)
	if strings.Contains(id, "glm-5.1") ||
		strings.Contains(id, "glm-5.2") ||
		strings.Contains(id, "glm-5v") ||
		strings.Contains(id, "glm-5-turbo") {
		return false
	}
	return strings.Contains(id, "glm-5")
}

// resolveModelMaxOutputTokens returns a reasonable max output token budget for
// clients that read Models API / OpenCode limit.output fields.
func resolveModelMaxOutputTokens(modelID string, contextLength int) int {
	id := normalizeModelContextID(modelID)
	if isClaudeHaikuModel(id) {
		return 64_000
	}
	if contextLength >= contextLength1M {
		return 128_000
	}
	if contextLength >= contextLength200K {
		// Current GLM-5.x / 4.7 coding lineup documents 128k max output.
		if strings.Contains(id, "glm-5") || strings.Contains(id, "glm-4.7") || strings.Contains(id, "glm-4.6") {
			return 128_000
		}
		return 64_000
	}
	return 65_536
}
