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
		normalized["messages"] = normalizeClaudePassThroughMessages(rawMessages)
	}
	if rawTools, ok := payload["tools"].([]any); ok && len(rawTools) > 0 {
		normalized["tools"] = normalizeClaudePassThroughTools(rawTools)
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
	for key := range payload {
		delete(payload, key)
	}
	for key, value := range normalized {
		payload[key] = value
	}
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
			continue
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": content,
		})
	}
	return out
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
		return map[string]any{
			"type": "text",
			"text": stringValue(block["text"]),
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
		out := map[string]any{"type": "thinking"}
		if thinking, ok := block["thinking"]; ok {
			out["thinking"] = thinking
		}
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
