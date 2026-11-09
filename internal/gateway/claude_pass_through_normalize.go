package gateway

import (
	"encoding/json"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// normalizeClaudePassThroughPayload trims volatile client-only fields from a
// native Claude /v1/messages body before OAuth cloaking. This mirrors the
// stable field set produced by openAIChatToClaudeRequest so prompt-cache
// prefixes stay byte-identical across turns.
func normalizeClaudePassThroughPayload(payload map[string]any) {
	if payload == nil {
		return
	}
	normalized := map[string]any{}
	if model, ok := payload["model"]; ok && model != nil {
		normalized["model"] = model
	}
	if system := normalizeClaudePassThroughSystem(payload["system"]); system != nil {
		normalized["system"] = system
	}
	if rawMessages, ok := payload["messages"].([]any); ok {
		normalized["messages"] = dropTrailingClaudePassThroughThinking(normalizeClaudePassThroughMessages(rawMessages))
	}
	declaredTools := 0
	if rawTools, ok := payload["tools"].([]any); ok && len(rawTools) > 0 {
		if normalizedTools := normalizeClaudePassThroughTools(rawTools); len(normalizedTools) > 0 {
			normalized["tools"] = normalizedTools
			declaredTools = len(normalizedTools)
		}
	}
	// 历史里调用过、这次请求却没有声明的工具（tool_search 一类按需加载工具集的
	// 客户端就是这样）：Anthropic 直接拒绝（"Requests which include tool_use or
	// tool_result blocks must define tools"），一个空 tools 也一样。补一个占位
	// 声明让历史合法。
	//
	// 占位声明和 tool_choice 是两件事，只有前者能解决问题。tool_choice=none 只在
	// 这一轮确实没声明任何工具时才是实话（客户端这一轮不打算用工具，也顺带挡住
	// 模型去调那个占位名）。
	//
	// 客户端明明声明了一堆工具、只是历史里多了一个已经不在声明表里的名字时，绝不能
	// 设 none：Claude Code 默认根本不发 tool_choice，下面的
	// normalizeClaudePassThroughToolChoice 读到 nil 就不会覆盖，none 会一路带到
	// 上游，模型被禁止调用任何工具，只能回一句文字——而历史里那条 tool_use 永远在，
	// 于是这一段会话的每一轮都被禁，看起来就是「回复一句就不继续往下干了」。
	if stubbed, ok := stubUndeclaredHistoryTools(normalized); ok {
		normalized["tools"] = stubbed
		if declaredTools == 0 {
			normalized["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	if toolChoice := normalizeClaudePassThroughToolChoice(payload["tool_choice"]); toolChoice != nil {
		normalized["tool_choice"] = toolChoice
	}
	for _, key := range []string{"stream", "temperature", "top_p", "output_config"} {
		if value, ok := payload[key]; ok {
			normalized[key] = value
		}
	}
	// 优先保留上游已写入的预算（rewriteClaudeUpstreamMaxTokens 会按真实模型/密钥覆盖设置）；
	// 否则按 body 内模型自动解析，绝不沿用 Claude Code 按客户端目录填的偏小值。
	if value, ok := payload["max_tokens"]; ok {
		normalized["max_tokens"] = value
	} else {
		modelID, _ := normalized["model"].(string)
		normalized["max_tokens"] = defaultClaudeMaxTokens(modelID)
	}
	if thinking, ok := payload["thinking"]; ok {
		if normalizedThinking := normalizeClaudePassThroughThinking(thinking); normalizedThinking != nil {
			normalized["thinking"] = normalizedThinking
		}
	}
	sanitizeClaudePassThroughDisabledThinkingEffort(normalized)
	// 实测模型可能不是 Claude：关闭 thinking 的写法按模型族改写（glm 不认识
	// thinking.type，会把它当默认开启思考）。
	normalizeClaudeThinkingOff(normalized, stringValue(normalized["model"]))
	// 客户端要的思考档位原样送到上游。magpie（本协议的参考实现）从不为
	// opus-5-5 / fable / mythos 降低 effort：它的 thinkingOffAsTaken 只在
	// thinking 已经是 disabled/between_tools 时才动 effort，对这类模型只去掉
	// thinking 字段；真机档位表也把 opus-5-5 列为 low medium high xhigh max。
	// clampClaudeAdaptiveEffort 因此默认不生效，仅 CLAUDE_CLAMP_ADAPTIVE_EFFORT=1
	// 时恢复旧的降档行为（上游又开始返回空回合时的回滚杠杆）。
	clampClaudeAdaptiveEffort(normalized, stringValue(normalized["model"]))
	for key := range payload {
		delete(payload, key)
	}
	for key, value := range normalized {
		payload[key] = value
	}
}

// sanitizeClaudePassThroughDisabledThinkingEffort reconciles thinking.type=disabled
// with the upstream model on native pass-through:
//   - Models that reject disabled outright (fable-5) are rewritten to
//     adaptive+effort:low, matching the Chat->Claude conversion path.
//   - Models that accept disabled but reject high-effort pairings (Opus 5+)
//     have output_config.effort clamped from xhigh/max down to high
//     (400: use effort 'high' or below, or enable thinking).
//
// Claude Code / clients sometimes send these incompatible pairs on pass-through.
func sanitizeClaudePassThroughDisabledThinkingEffort(payload map[string]any) {
	if payload == nil {
		return
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if strings.ToLower(strings.TrimSpace(stringValue(thinking["type"]))) != "disabled" {
		return
	}
	// Some models (fable-5) reject thinking.type=disabled outright — 400
	// "thinking.type.disabled is not supported for this model". Rewrite to
	// adaptive+low, matching the Chat->Claude conversion path
	// (normalizeExplicitThinkingForModel). opus-5 accepts disabled, so it
	// falls through to the effort clamp below instead.
	model := strings.TrimSpace(stringValue(payload["model"]))
	if claudeModelRejectsDisabledThinking(model) {
		payload["thinking"] = map[string]any{"type": "adaptive"}
		cfg, _ := payload["output_config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfg["effort"] = "low"
		payload["output_config"] = cfg
		return
	}
	cfg, _ := payload["output_config"].(map[string]any)
	if cfg == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(stringValue(cfg["effort"])))
	if effort != "xhigh" && effort != "max" {
		return
	}
	cfg["effort"] = "high"
	payload["output_config"] = cfg
}

// claudeCountTokensDisallowedFields are generation-only Message fields that
// Anthropic's /v1/messages/count_tokens rejects with
// "…: Extra inputs are not permitted" (see Claude Code /context → count_tokens).
// Allowed inputs include model/messages/system/tools/tool_choice/thinking/
// output_config/cache_control — not max_tokens or sampling controls.
var claudeCountTokensDisallowedFields = []string{
	"max_tokens",
	"stream",
	"temperature",
	"top_p",
	"top_k",
	"stop_sequences",
	"metadata",
	"service_tier",
	"context_management",
}

func sanitizeClaudeCountTokensPayload(payload map[string]any) {
	if payload == nil {
		return
	}
	for _, key := range claudeCountTokensDisallowedFields {
		delete(payload, key)
	}
}

func sanitizeClaudeCountTokensBody(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	sanitizeClaudeCountTokensPayload(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// rewriteClaudeUpstreamMaxTokens 在 Claude 透传发送前，按实际上游模型写入 max_tokens，
// 忽略 Claude Code 等客户端按「请求模型名」填的预算（模型覆盖场景会偏小截断）。
// maxTokensOverride>0 时使用密钥级覆盖。
func rewriteClaudeUpstreamMaxTokens(body []byte, provider domain.Provider, maxTokensOverride int) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	model, _ := payload["model"].(string)
	model = strings.TrimSpace(applyProviderModelMapping(provider, model))
	if model != "" {
		payload["model"] = model
	}
	payload["max_tokens"] = effectiveClaudeMaxTokens(model, maxTokensOverride)
	sanitizeClaudePassThroughDisabledThinkingEffort(payload)
	normalizeClaudeThinkingOff(payload, model)
	clampClaudeAdaptiveEffort(payload, model)
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

func normalizeClaudePassThroughSystem(system any) any {
	if system == nil {
		return nil
	}
	switch typed := system.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return typed
	case []any:
		blocks := normalizeClaudePassThroughContentBlocks(typed)
		if len(blocks) == 0 {
			return nil
		}
		return claudeSystemValueFromBlocks(blocks)
	default:
		return nil
	}
}

func normalizeClaudePassThroughMessages(raw []any) []any {
	out := make([]any, 0, len(raw))
	dropped := false
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(entry["role"]))
		if role == "" {
			continue
		}
		content := normalizeClaudePassThroughMessageContent(entry["content"])
		if isEmptyClaudeContent(content) {
			// A turn that was nothing but an unreadable block (an empty thinking
			// placeholder, an empty text) carries nothing to send. Dropping it
			// can leave two same-role messages next to each other, which
			// Anthropic refuses — cleaned up below.
			dropped = true
			continue
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": content,
		})
	}
	if dropped {
		out = mergeAdjacentClaudePassThroughMessages(out)
	}
	if len(out) == 0 && len(raw) > 0 {
		// Every turn was unreadable. Sending no messages at all earns a different
		// refusal ("at least one message is required"), so hand the history back
		// as it came and let the upstream decide.
		return raw
	}
	return out
}

// dropTrailingClaudePassThroughThinking removes thinking / redacted_thinking
// blocks that would otherwise end an assistant message on the native
// pass-through path.
//
// Anthropic refuses such a history outright ("The final block in an assistant
// message cannot be `thinking`"), and an upstream that does accept it can
// answer the turn with nothing the client can see at all — a thinking-only
// reply with stop_reason=end_turn, which reads to the client as the assistant
// going silent. That empty turn is then replayed as history, and every later
// turn of the conversation answers the same way; see
// errThinkingOnlyEmptyResponse for the same verdict on the converter path.
//
// Trailing thinking is never re-read by the model, so dropping it is lossless:
// a thinking block only has to survive when a text/tool_use block follows it in
// the same turn, which is exactly the case this never touches. An assistant
// message left with nothing is dropped, and the same-role messages it stood
// between are merged back into one (Anthropic refuses two of them sent apart).
//
// A redacted_thinking block is the exception: it carries the upstream
// ciphertext a later turn replays, so it stays as one empty thinking
// placeholder instead of being dropped.
func dropTrailingClaudePassThroughThinking(messages []any) []any {
	if len(messages) == 0 {
		return messages
	}
	out := make([]any, 0, len(messages))
	trimmedAny := false
	droppedAny := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if stringValue(message["role"]) == "assistant" {
			if blocks, ok := message["content"].([]any); ok {
				trimmed := claudeTrailingThinkingTrimmed(blocks)
				if trimmed < len(blocks) {
					trimmedAny = true
					if trimmed == 0 {
						// A turn that was nothing but reasoning. It is dropped,
						// redacted or not: an assistant message may not end on a
						// thinking block, and the placeholder this used to leave
						// behind ({"type":"thinking","thinking":""}) is itself
						// refused — "each thinking block must contain
						// non-whitespace thinking". A signature cannot rescue an
						// empty text, so keeping the ciphertext costs a 400 on
						// every later turn of the conversation.
						droppedAny = true
						continue
					}
					message = map[string]any{
						"role":    "assistant",
						"content": blocks[:trimmed],
					}
				}
			}
		}
		out = append(out, message)
	}
	if !trimmedAny || len(out) == 0 {
		// Nothing to drop, or a history that was nothing but those turns:
		// leave the request as the client wrote it rather than answer
		// Anthropic's "messages: at least one message is required".
		return messages
	}
	if droppedAny {
		out = mergeAdjacentClaudePassThroughMessages(out)
	}
	return out
}

// claudeTrailingThinkingTrimmed is the length the blocks would have once the
// thinking blocks ending them are taken off.
func claudeTrailingThinkingTrimmed(blocks []any) int {
	trimmed := len(blocks)
	for trimmed > 0 {
		block, ok := blocks[trimmed-1].(map[string]any)
		if !ok {
			return trimmed
		}
		switch stringValue(block["type"]) {
		case "thinking", "redacted_thinking":
			trimmed--
		default:
			return trimmed
		}
	}
	return trimmed
}

func claudeBlocksHaveRedactedThinking(blocks []any) bool {
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok && stringValue(block["type"]) == "redacted_thinking" {
			return true
		}
	}
	return false
}

// mergeAdjacentClaudePassThroughMessages joins consecutive same-role messages,
// which Anthropic refuses when they arrive as separate entries.
func mergeAdjacentClaudePassThroughMessages(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if n := len(out); n > 0 {
			if prev, ok := out[n-1].(map[string]any); ok && stringValue(prev["role"]) == stringValue(message["role"]) {
				if joined, ok := joinClaudePassThroughContent(prev["content"], message["content"]); ok {
					out[n-1] = map[string]any{"role": stringValue(prev["role"]), "content": joined}
					continue
				}
			}
		}
		out = append(out, message)
	}
	return out
}

// joinClaudePassThroughContent appends one message's content to another's,
// normalising a bare string into the one text block it stands for.
func joinClaudePassThroughContent(left, right any) (any, bool) {
	leftBlocks, leftOK := claudePassThroughContentBlocks(left)
	rightBlocks, rightOK := claudePassThroughContentBlocks(right)
	if !leftOK || !rightOK {
		return nil, false
	}
	joined := make([]any, 0, len(leftBlocks)+len(rightBlocks))
	joined = append(joined, leftBlocks...)
	joined = append(joined, rightBlocks...)
	return joined, true
}

func claudePassThroughContentBlocks(content any) ([]any, bool) {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, true
		}
		return []any{map[string]any{"type": "text", "text": typed}}, true
	case []any:
		return typed, true
	default:
		return nil, false
	}
}

func normalizeClaudePassThroughMessageContent(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		blocks := normalizeClaudePassThroughContentBlocks(typed)
		if len(blocks) == 0 {
			return ""
		}
		if len(blocks) == 1 {
			if block, ok := blocks[0].(map[string]any); ok {
				if stringValue(block["type"]) == "text" && len(block) == 2 {
					return stringValue(block["text"])
				}
			}
		}
		return blocks
	default:
		text := strings.TrimSpace(claudeContentToString(content))
		if text == "" {
			return ""
		}
		return text
	}
}

func normalizeClaudePassThroughContentBlocks(blocks []any) []any {
	out := make([]any, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if normalized := normalizeClaudePassThroughContentBlock(block); normalized != nil {
			out = append(out, normalized)
		}
	}
	return out
}

func normalizeClaudePassThroughContentBlock(block map[string]any) map[string]any {
	switch stringValue(block["type"]) {
	case "text":
		// Anthropic rejects "" with "text content blocks must be non-empty".
		// Drop the block (same as clients that omit empty texts); message-level
		// emptiness is already handled by isEmptyClaudeContent.
		text := stringValue(block["text"])
		if text == "" {
			return nil
		}
		return map[string]any{
			"type": "text",
			"text": text,
		}
	case "tool_use":
		out := map[string]any{
			"type": "tool_use",
			"id":   sanitizeAnthropicToolUseID(stringValue(block["id"])),
			"name": stringValue(block["name"]),
		}
		if input, ok := block["input"]; ok {
			out["input"] = input
		}
		return out
	case "tool_result":
		return map[string]any{
			"type":        "tool_result",
			"tool_use_id": sanitizeAnthropicToolUseID(stringValue(block["tool_use_id"])),
			"content":     normalizeClaudePassThroughToolResultContent(block["content"]),
		}
	case "image":
		out := map[string]any{"type": "image"}
		if source, ok := block["source"]; ok {
			out["source"] = source
		}
		return out
	case "document":
		out := map[string]any{"type": "document"}
		if source, ok := block["source"]; ok {
			out["source"] = source
		}
		return out
	case "thinking":
		// Anthropic refuses a thinking block whose text is blank:
		//   messages.N.content.M.thinking: each thinking block must contain
		//   non-whitespace thinking
		// Claude Code does replay such a block (measured: signature "" with
		// thinking " ", i.e. a single space), and a signature cannot rescue it —
		// the text itself is validated. Dropping it here means the turn goes out
		// valid instead of paying a 400 and a rectified retry. A message left
		// with no blocks at all is dropped by the caller
		// (isEmptyClaudeContent), which is what such a placeholder turn deserves.
		thinking := stringValue(block["thinking"])
		if strings.TrimSpace(thinking) == "" {
			return nil
		}
		out := map[string]any{"type": "thinking", "thinking": block["thinking"]}
		if signature, ok := block["signature"]; ok {
			out["signature"] = signature
		}
		return out
	case "tool_reference":
		// Nested under tool_result.content (tool-search results). Anthropic
		// requires tool_name; the default branch below only kept type/text and
		// dropped it → 400 "tool_reference.tool_name: Field required".
		name := stringValue(block["tool_name"])
		if name == "" {
			name = stringValue(block["name"]) // some clients misuse "name"
		}
		if name == "" {
			return nil
		}
		return map[string]any{
			"type":      "tool_reference",
			"tool_name": name,
		}
	case "redacted_thinking":
		out := map[string]any{"type": "redacted_thinking"}
		if data, ok := block["data"]; ok {
			out["data"] = data
		}
		return out
	default:
		blockType := stringValue(block["type"])
		if blockType == "" {
			return nil
		}
		out := map[string]any{"type": blockType}
		if text, ok := block["text"]; ok {
			out["text"] = text
		}
		return out
	}
}

func normalizeClaudePassThroughToolResultContent(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		blocks := normalizeClaudePassThroughContentBlocks(typed)
		if len(blocks) == 0 {
			return ""
		}
		return blocks
	default:
		return content
	}
}

// claudeServerToolType reports Anthropic built-in / server tools (web_search_*,
// bash_*, text_editor_*, computer_*, code_execution_*, …). These use `type` as
// the discriminator and must NOT receive a synthetic input_schema — upstream
// returns 400 "…input_schema: Extra inputs are not permitted".
func claudeServerToolType(toolType string) bool {
	toolType = strings.TrimSpace(toolType)
	return toolType != "" && toolType != "custom"
}

// serverToolPassthroughKeys are fields Anthropic accepts on built-in tools.
// Anything else (cache_control, input_schema, …) is dropped for cache stability
// and schema compliance.
var claudeServerToolPassthroughKeys = []string{
	"name",
	"max_uses",
	"allowed_domains",
	"blocked_domains",
	"user_location",
	"allowed_callers",
	"defer_loading",
}

func normalizeClaudePassThroughTools(raw []any) []any {
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		toolType := stringValue(tool["type"])
		if claudeServerToolType(toolType) {
			normalized := map[string]any{"type": toolType}
			for _, key := range claudeServerToolPassthroughKeys {
				if value, ok := tool[key]; ok {
					normalized[key] = value
				}
			}
			out = append(out, normalized)
			continue
		}

		normalized := map[string]any{}
		if toolType != "" {
			normalized["type"] = toolType
		}
		if name := stringValue(tool["name"]); name != "" {
			normalized["name"] = name
		}
		if description := stringValue(tool["description"]); description != "" {
			normalized["description"] = description
		}
		if schema, ok := tool["input_schema"]; ok {
			normalized["input_schema"] = schema
		} else if parameters, ok := tool["parameters"]; ok {
			normalized["input_schema"] = parameters
		} else if name := stringValue(normalized["name"]); name != "" {
			// Anthropic rejects custom tools without input_schema.
			normalized["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if len(normalized) == 0 {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func normalizeClaudePassThroughToolChoice(choice any) any {
	item, ok := choice.(map[string]any)
	if !ok || len(item) == 0 {
		return nil
	}
	out := map[string]any{}
	if choiceType := stringValue(item["type"]); choiceType != "" {
		out["type"] = choiceType
	}
	if name := stringValue(item["name"]); name != "" {
		out["name"] = name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeClaudePassThroughThinking(thinking any) any {
	item, ok := thinking.(map[string]any)
	if !ok || len(item) == 0 {
		return thinking
	}
	out := map[string]any{}
	if thinkingType := stringValue(item["type"]); thinkingType != "" {
		out["type"] = thinkingType
	}
	if budget, ok := item["budget_tokens"]; ok {
		out["budget_tokens"] = budget
	}
	if len(out) == 0 {
		return thinking
	}
	return out
}

// stubUndeclaredHistoryTools declares a stub for every tool the conversation
// called that the request no longer declares. An agent that loads tools on
// demand (pi, T3 Code's tool_search) sends a history full of calls it made
// earlier together with the few tools it thinks it needs now — and Anthropic
// turns such a request away outright: "Requests which include `tool_use` or
// `tool_result` blocks must define tools". A stub keeps the history valid; with
// tool_choice=none nothing new can be called through it.
//
// It reports whether anything was added.
func stubUndeclaredHistoryTools(payload map[string]any) ([]any, bool) {
	declared := map[string]bool{}
	tools, _ := payload["tools"].([]any)
	for _, raw := range tools {
		if tool, ok := raw.(map[string]any); ok {
			declared[strings.TrimSpace(stringValue(tool["name"]))] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, raw := range asMapSlice(payload["messages"]) {
		message, _ := raw.(map[string]any)
		for _, blockRaw := range asMapSlice(message["content"]) {
			block, _ := blockRaw.(map[string]any)
			if stringValue(block["type"]) != "tool_use" {
				continue
			}
			name := strings.TrimSpace(stringValue(block["name"]))
			if name == "" || declared[name] || seen[name] {
				continue
			}
			seen[name] = true
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return tools, false
	}
	stubbed := append([]any(nil), tools...)
	for _, name := range missing {
		stubbed = append(stubbed, map[string]any{
			"name":         name,
			"description":  "Called earlier in this conversation; not available now.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	return stubbed, true
}
