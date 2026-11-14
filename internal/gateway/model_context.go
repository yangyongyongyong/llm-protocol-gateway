package gateway

import (
	"context"
	"encoding/json"
	"fmt"
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
	if r == nil {
		return r
	}
	if matched && key.MaxOutputTokens > 0 {
		r = r.WithContext(withMaxOutputTokensOverride(r.Context(), key.MaxOutputTokens))
	}
	if matched && key.CodexForceFast {
		r = r.WithContext(withCodexForceFast(r.Context()))
	}
	return r
}

type codexForceFastKey struct{}

func withCodexForceFast(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexForceFastKey{}, true)
}

func codexForceFastFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(codexForceFastKey{}).(bool)
	return enabled
}

const (
	contextLength1050K   = 1_050_000
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

// maxModelContextOverride 是运维能声明的窗口上限。它只用来挡明显的手滑
// （把 1000000 打成 1000000000），不是一个有依据的容量上限。
const maxModelContextOverride = 10_000_000

// modelWindow 是一个模型最终对外宣称的上下文窗口：运维显式设过就按他的，
// 否则按厂商已知值 / 上游声明值算。
//
// 显式值压过一切，连「厂商已知是 1M」也压过去，不只是抬高：一个标称 1M 的
// 模型实际被套餐限到 200K（或者走的是会截断的中转）时同样要说得出 200K，
// 而 resolveModelContextLength 的规则是「已知值和报告值取大的那个」，说不出
// 更小的数。这就是 magpie 的 provider.Contexts 那条「用户的话最优先」。
//
// 覆盖值不写进 ContextLength：后者留着「自动值」，清空设置时靠它原样退回去。
func modelWindow(model domain.Model) int {
	if model.ContextOverride > 0 {
		return model.ContextOverride
	}
	return resolveModelContextLength(model.ID, model.ContextLength)
}

// handleSetProviderModelContext 是「这个模型的上下文窗口是多大」这一个旋钮。
//
// 它存在的理由：网关对窗口的判断只有两条腿——厂商已知的那几个（Claude / GLM /
// GPT 的公开规格）和上游 /models 自己报的数。中转站这两条都不占的时候（自建
// 服务、改了名字的镜像、上游干脆不报），一个真的能跑 1M 的模型在网关眼里就是
// 默认的 128K，于是 Claude Desktop / Claude Code 那边也永远按 128K 压缩上下文。
// 这里让运维直接说一句话把这件事定下来（magpie 的 provider.SetContext 同一个
// 意思），定完窗口就按它算，目录同步也不会冲掉。
func (s *Server) handleSetProviderModelContext(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderOwnerForUser(w, r, r.PathValue("id")) {
		return
	}
	var payload struct {
		Model string `json:"model"`
		// ContextLength 是新的窗口；0 = 退回自动（按厂商/上游算）。
		ContextLength *int `json:"contextLength"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.ContextLength == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"model": "id", "contextLength": N}`)
		return
	}
	updated, err := s.router.SetProviderModelContext(r.PathValue("id"), payload.Model, *payload.ContextLength)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "has no model") {
			status = http.StatusNotFound
		}
		writeOpenAIError(w, status, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	model := strings.TrimSpace(payload.Model)
	if *payload.ContextLength > 0 {
		s.logs.AddApp("info", "model context window set", fmt.Sprintf("provider=%s model=%s window=%d", updated.ID, model, *payload.ContextLength))
	} else {
		s.logs.AddApp("info", "model context window cleared", fmt.Sprintf("provider=%s model=%s", updated.ID, model))
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
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
	if n, ok := knownOpenAIContextLength(base); ok {
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
// Current flagship Opus 5 / Opus 4.6+ / Sonnet 5 generations are native 1M;
// older Claude 4.5-era models (and Haiku) remain 200k.
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
		"claude-opus-5",
		"opus-5",
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

// knownOpenAIContextLength maps OpenAI GPT family IDs to official context
// windows. Per OpenAI docs (developers.openai.com/api/docs/models/gpt-5.6-*):
// all tiers share a 1.05M context window; GPT-5.5 is ~1M.
func knownOpenAIContextLength(modelID string) (int, bool) {
	id := normalizeModelContextID(modelID)
	if !strings.Contains(id, "gpt-") {
		return 0, false
	}
	if strings.Contains(id, "gpt-5.6") {
		return contextLength1050K, true
	}
	if strings.Contains(id, "gpt-5.5") {
		return contextLength1M, true
	}
	return 0, false
}

// resolveModelMaxOutputTokens returns a reasonable max output token budget for
// clients that read Models API / OpenCode limit.output fields.
func resolveModelMaxOutputTokens(modelID string, contextLength int) int {
	id := normalizeModelContextID(modelID)
	// Qoder tier IDs aren't real model names and match none of the heuristics
	// below, so they'd all fall through to the generic 65_536 catch-all.
	// Empirically probed against the live upstream (2026-08-03, real request
	// with max_tokens forced past 65536): performance/auto/efficient/lite all
	// either hard-reject or stay well-behaved at the 65_536 catch-all already
	// in place, but ultimate accepted max_tokens=200_000 with no rejection —
	// so it gets a dedicated higher budget instead of silently capping at 65536.
	if id == "ultimate" {
		return 200_000
	}
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

// fillModelTokenBudgets sets MaxOutputTokens for the UI, and ContextLength when
// nobody has overridden the window.
//
// ContextLength is deliberately left alone while an override is set: it is the
// automatic value ("what the vendor's spec or the upstream's /models says"), and
// that is what clearing the override has to fall back to. Writing the override
// over it would make the setting permanent in effect — it survives the database
// round trip, so a later "clear" could only restore the override itself.
func fillModelTokenBudgets(model *domain.Model) {
	if model == nil {
		return
	}
	if model.ContextOverride <= 0 {
		model.ContextLength = resolveModelContextLength(model.ID, model.ContextLength)
	}
	model.MaxOutputTokens = resolveModelMaxOutputTokens(model.ID, modelWindow(*model))
}
