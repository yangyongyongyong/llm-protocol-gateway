package gateway

import "strings"

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
	for _, key := range []string{"max_tokens", "stream", "temperature", "top_p", "output_config"} {
		if value, ok := payload[key]; ok {
			normalized[key] = value
		}
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

func normalizeClaudePassThroughTools(raw []any) []any {
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		normalized := map[string]any{}
		if toolType := stringValue(tool["type"]); toolType != "" {
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
