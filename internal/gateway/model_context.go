package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

type maxOutputTokensOverrideKey struct{}

func withMaxOutputTokensOverride(ctx context.Context, n int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	n = normalizeMaxOutputTokens(n)
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxOutputTokensOverrideKey{}, n)
}

func maxOutputTokensOverrideFrom(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	n, _ := ctx.Value(maxOutputTokensOverrideKey{}).(int)
	return normalizeMaxOutputTokens(n)
}

func attachAPIKeyMaxOutputTokens(r *http.Request, key domain.APIKey, matched bool) *http.Request {
	if r == nil || !matched || key.MaxOutputTokens <= 0 {
		return r
	}
	return r.WithContext(withMaxOutputTokensOverride(r.Context(), key.MaxOutputTokens))
}

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

// defaultClaudeMaxTokens 在客户端未指定 / 密钥未覆盖时使用。
// Anthropic Messages API 要求该字段必填，无法真正“不截断”；这里取模型对外宣称的
// 输出上限，避免历史写死 4096 误伤长 agent / 工具调用。
func defaultClaudeMaxTokens(modelID string) int {
	ctxLen := resolveModelContextLength(modelID, 0)
	n := resolveModelMaxOutputTokens(modelID, ctxLen)
	if n <= 0 {
		return 64_000
	}
	return n
}

// effectiveClaudeMaxTokens 优先使用密钥级覆盖（>0），否则按实际上游模型自动解析。
func effectiveClaudeMaxTokens(modelID string, override int) int {
	if override > 0 {
		return override
	}
	return defaultClaudeMaxTokens(modelID)
}

// normalizeMaxOutputTokens clamps a key-level override. 0 means "auto".
func normalizeMaxOutputTokens(n int) int {
	if n <= 0 {
		return 0
	}
	const maxAllowed = 200_000
	if n > maxAllowed {
		return maxAllowed
	}
	return n
}

// fillModelTokenBudgets sets ContextLength / MaxOutputTokens display fields for UI.
func fillModelTokenBudgets(model *domain.Model) {
	if model == nil {
		return
	}
	model.ContextLength = resolveModelContextLength(model.ID, model.ContextLength)
	model.MaxOutputTokens = resolveModelMaxOutputTokens(model.ID, model.ContextLength)
}
