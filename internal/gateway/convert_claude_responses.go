package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Direction 2: Claude client ↔ Responses upstream direct converters.
// Complements convert_responses_claude.go (Responses client ↔ Claude upstream).

// claudeSystemToInstructions flattens Claude's system field into a Responses
// instructions string.
func claudeSystemToInstructions(system any) string {
	switch typed := system.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			switch v := item.(type) {
			case string:
				if t := strings.TrimSpace(v); t != "" {
					parts = append(parts, t)
				}
			case map[string]any:
				if t := stringValue(v["text"]); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

// claudeMessagesToResponsesInput flattens Anthropic messages into Responses
// input items, promoting tool_use / tool_result to top-level items and
// preserving signed thinking blocks via encrypted_content.
func claudeMessagesToResponsesInput(messages []any) []any {
	input := make([]any, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := firstNonEmpty(stringValue(msg["role"]), "user")
		if role != "assistant" {
			role = "user"
		}
		switch content := msg["content"].(type) {
		case string:
			if !isMeaningfulText(content) {
				continue
			}
			contentType := "input_text"
			if role == "assistant" {
				contentType = "output_text"
			}
			input = append(input, map[string]any{
				"role":    role,
				"content": []any{map[string]any{"type": contentType, "text": content}},
			})
		case []any:
			messageContent := make([]any, 0, len(content))
			flushMessage := func() {
				if len(messageContent) == 0 {
					return
				}
				input = append(input, map[string]any{"role": role, "content": messageContent})
				messageContent = make([]any, 0, 4)
			}
			for _, rawBlock := range content {
				block, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				switch stringValue(block["type"]) {
				case "text":
					if text := stringValue(block["text"]); isMeaningfulText(text) {
						contentType := "input_text"
						if role == "assistant" {
							contentType = "output_text"
						}
						messageContent = append(messageContent, map[string]any{"type": contentType, "text": text})
					}
				case "image":
					if source, ok := block["source"].(map[string]any); ok {
						mediaType := firstNonEmpty(stringValue(source["media_type"]), "image/png")
						data := stringValue(source["data"])
						if data != "" {
							messageContent = append(messageContent, map[string]any{
								"type":      "input_image",
								"image_url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
							})
						}
					}
				case "tool_use":
					flushMessage()
					callID := sanitizeResponsesCallID(stringValue(block["id"]))
					name := stringValue(block["name"])
					if name == "" {
						continue
					}
					args := "{}"
					if block["input"] != nil {
						if raw, err := json.Marshal(block["input"]); err == nil {
							args = string(raw)
						}
					}
					input = append(input, map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      name,
						"arguments": args,
					})
				case "tool_result":
					flushMessage()
					callID := sanitizeResponsesCallID(stringValue(block["tool_use_id"]))
					if callID == "" {
						continue
					}
					input = append(input, map[string]any{
						"type":    "function_call_output",
						"call_id": callID,
						"output":  toolResultContentToString(block["content"]),
					})
				case "thinking", "redacted_thinking":
					flushMessage()
					if item, ok := responsesReasoningItemFromAnthropicBlock(fmt.Sprintf("rs_%d", len(input)), block); ok {
						input = append(input, item)
					}
				}
			}
			flushMessage()
		}
	}
	return input
}

// claudeThinkingToResponsesEffort extracts a Responses reasoning.effort from a
// Claude thinking / output_config block.
func claudeThinkingToResponsesEffort(claudeReq map[string]any) string {
	if thinking, ok := claudeReq["thinking"].(map[string]any); ok {
		switch stringValue(thinking["type"]) {
		case "disabled":
			return ""
		case "adaptive":
			if cfg, ok := claudeReq["output_config"].(map[string]any); ok {
				if effort := normalizeReasoningEffort(stringValue(cfg["effort"])); effort != "" {
					return effort
				}
			}
			if effort := normalizeReasoningEffort(stringValue(thinking["effort"])); effort != "" {
				return effort
			}
			return "high"
		case "enabled", "":
			budget := int64FromAny(thinking["budget_tokens"])
			switch {
			case budget <= 0:
				return "medium"
			case budget <= 4096:
				return "low"
			case budget <= 10000:
				return "medium"
			case budget <= 16000:
				return "high"
			default:
				return "xhigh"
			}
		}
	}
	if cfg, ok := claudeReq["output_config"].(map[string]any); ok {
		return normalizeReasoningEffort(stringValue(cfg["effort"]))
	}
	return ""
}

// claudeToResponsesRequestDirect converts an Anthropic Messages request directly
// into an OpenAI Responses request (no Chat intermediate).
func claudeToResponsesRequestDirect(claudeReq map[string]any, model string) (map[string]any, error) {
	responsesReq := map[string]any{"model": model}

	if instructions := claudeSystemToInstructions(claudeReq["system"]); instructions != "" {
		responsesReq["instructions"] = instructions
	}

	rawMessages, ok := claudeReq["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("claude request missing messages array")
	}
	input := claudeMessagesToResponsesInput(rawMessages)
	if len(input) == 0 {
		return nil, fmt.Errorf("claude request has no usable messages")
	}
	responsesReq["input"] = input

	if maxTokens, exists := claudeReq["max_tokens"]; exists {
		responsesReq["max_output_tokens"] = maxTokens
	}
	if stream, ok := claudeReq["stream"].(bool); ok && stream {
		responsesReq["stream"] = true
	} else {
		responsesReq["stream"] = false
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := claudeReq[key]; exists {
			responsesReq[key] = value
		}
	}
	if effort := claudeThinkingToResponsesEffort(claudeReq); effort != "" {
		responsesReq["reasoning"] = map[string]any{"effort": effort}
	}

	// Tools: Claude → Chat shape → Responses flat function tools.
	chatCarrier := map[string]any{}
	copyToolsField(claudeReq, chatCarrier, false)
	copyToolChoiceField(claudeReq, chatCarrier, false)
	copyChatToolsToResponses(chatCarrier, responsesReq)

	return responsesReq, nil
}

// mapResponsesStatusToClaudeStopReason maps Responses status / incomplete reason
// back to an Anthropic stop_reason.
func mapResponsesStatusToClaudeStopReason(status, incompleteReason string, hasToolUse bool) string {
	if hasToolUse {
		return "tool_use"
	}
	switch strings.TrimSpace(status) {
	case "incomplete":
		switch incompleteReason {
		case "max_output_tokens":
			return "max_tokens"
		case "content_filter":
			return "refusal"
		default:
			return "max_tokens"
		}
	default:
		return "end_turn"
	}
}

// responsesToClaudeResponseDirect converts a non-streamed Responses response
// directly into an Anthropic Messages response.
func responsesToClaudeResponseDirect(responsesBody []byte, model string) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(responsesBody, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return responsesErrorValueToClaudeDirect(errorValue, model)
	}
	if detail := strings.TrimSpace(stringValue(payload["detail"])); detail != "" {
		return responsesErrorValueToClaudeDirect(detail, model)
	}

	content := make([]any, 0, 4)
	hasToolUse := false
	if outputItems, ok := payload["output"].([]any); ok {
		for _, raw := range outputItems {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(item["type"]) {
			case "message":
				if parts, ok := item["content"].([]any); ok {
					for _, rawPart := range parts {
						part, ok := rawPart.(map[string]any)
						if !ok {
							continue
						}
						switch stringValue(part["type"]) {
						case "output_text":
							if text := stringValue(part["text"]); text != "" {
								content = append(content, map[string]any{"type": "text", "text": text})
							}
						case "refusal":
							if text := stringValue(part["refusal"]); text != "" {
								content = append(content, map[string]any{"type": "text", "text": text})
							}
						}
					}
				}
			case "function_call":
				callID := sanitizeAnthropicToolUseID(stringValue(item["call_id"]))
				name := stringValue(item["name"])
				if name == "" {
					continue
				}
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": parseJSONArguments(stringValue(item["arguments"])),
				})
				hasToolUse = true
			case "reasoning":
				// Prefer restoring a signed Anthropic thinking block when present.
				if block, ok := decodeAnthropicThinkingBlock(stringValue(item["encrypted_content"])); ok {
					content = append(content, block)
					continue
				}
				if summary, ok := item["summary"].([]any); ok {
					parts := make([]string, 0, len(summary))
					for _, s := range summary {
						sm, ok := s.(map[string]any)
						if !ok {
							continue
						}
						if stringValue(sm["type"]) == "summary_text" {
							if text := stringValue(sm["text"]); text != "" {
								parts = append(parts, text)
							}
						}
					}
					if text := strings.Join(parts, ""); text != "" {
						content = append(content, map[string]any{"type": "thinking", "thinking": text})
					}
				}
			}
		}
	}

	incompleteReason := ""
	if details, ok := payload["incomplete_details"].(map[string]any); ok {
		incompleteReason = stringValue(details["reason"])
	}
	stopReason := mapResponsesStatusToClaudeStopReason(stringValue(payload["status"]), incompleteReason, hasToolUse)
	usage := ParseResponsesUsage(responsesBody)

	id := stringValue(payload["id"])
	if id == "" {
		id = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	} else if strings.HasPrefix(id, "resp_") {
		id = "msg_" + strings.TrimPrefix(id, "resp_")
	}

	response := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         firstNonEmpty(model, stringValue(payload["model"])),
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                usage.InputTokens - usage.CacheTokens,
			"output_tokens":               usage.OutputTokens,
			"cache_read_input_tokens":     usage.CacheTokens,
			"cache_creation_input_tokens": 0,
		},
	}
	if usage.InputTokens-usage.CacheTokens < 0 {
		response["usage"].(map[string]any)["input_tokens"] = usage.InputTokens
	}
	body, err := json.Marshal(response)
	return body, usage, err
}

func responsesErrorValueToClaudeDirect(errorValue any, model string) ([]byte, TokenUsage, error) {
	message, errorType := errorMessageFromValue(errorValue, "upstream request failed")
	body, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errorType, "message": message},
		"model": model,
	})
	return body, TokenUsage{}, err
}
