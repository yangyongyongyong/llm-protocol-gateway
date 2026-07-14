package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func normalizeResponsesContent(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		return cloneContentBlocks(typed)
	default:
		text := strings.TrimSpace(fmt.Sprint(content))
		if text == "" {
			return ""
		}
		return text
	}
}

func responsesContentToString(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType := stringValue(block["type"])
			switch blockType {
			case "output_text", "input_text", "text":
				if text := stringValue(block["text"]); text != "" {
					parts = append(parts, text)
				}
			case "refusal":
				if text := stringValue(block["refusal"]); text != "" {
					parts = append(parts, text)
				}
			default:
				if text := stringValue(block["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return strings.TrimSpace(fmt.Sprint(content))
	}
}

func responsesOutputText(output any) string {
	items, ok := output.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType := stringValue(block["type"])
		if blockType != "message" && blockType != "output_text" {
			continue
		}
		if blockType == "output_text" {
			if text := stringValue(block["text"]); text != "" {
				parts = append(parts, text)
			}
			continue
		}
		if text := responsesContentToString(block["content"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func inputItemsFromChatMessages(messages []any) ([]any, string, error) {
	instructions := strings.Builder{}
	inputItems := make([]any, 0, len(messages))
	for _, item := range messages {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(entry["role"]))
		if role == "" {
			continue
		}
		if role == "system" || role == "developer" {
			text := claudeContentToString(entry["content"])
			if text != "" {
				if instructions.Len() > 0 {
					instructions.WriteString("\n\n")
				}
				instructions.WriteString(text)
			}
			continue
		}
		if role == "tool" {
			callID := sanitizeResponsesCallID(stringValue(entry["tool_call_id"]))
			if callID == "" {
				continue
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  toolResultContentToString(entry["content"]),
			})
			continue
		}
		if role == "assistant" {
			if rawToolCalls, ok := entry["tool_calls"].([]any); ok && len(rawToolCalls) > 0 {
				if content := entry["content"]; content != nil && !isEmptyOpenAIContent(content) {
					inputItems = append(inputItems, map[string]any{"role": "assistant", "content": normalizeResponsesContent(content)})
				}
				for _, toolItem := range rawToolCalls {
					call, ok := toolItem.(map[string]any)
					if !ok {
						continue
					}
					functionValue, _ := call["function"].(map[string]any)
					name := stringValue(functionValue["name"])
					if name == "" {
						continue
					}
					inputItems = append(inputItems, map[string]any{
						"type":      "function_call",
						"call_id":   sanitizeResponsesCallID(stringValue(call["id"])),
						"name":      name,
						"arguments": stringValue(functionValue["arguments"]),
					})
				}
				continue
			}
		}
		if role != "user" && role != "assistant" {
			continue
		}
		content := normalizeResponsesContent(entry["content"])
		if isEmptyOpenAIContent(content) {
			continue
		}
		inputItems = append(inputItems, map[string]any{"role": role, "content": content})
	}
	if len(inputItems) == 0 {
		return nil, "", fmt.Errorf("chat request has no usable messages")
	}
	return inputItems, instructions.String(), nil
}

func setResponsesInput(responsesReq map[string]any, inputItems []any) {
	if len(inputItems) == 1 {
		if item, ok := inputItems[0].(map[string]any); ok && stringValue(item["role"]) == "user" {
			if text, ok := item["content"].(string); ok {
				responsesReq["input"] = text
				return
			}
		}
	}
	responsesReq["input"] = inputItems
}

// openAIChatToResponsesRequest converts an OpenAI Chat Completions request to
// an OpenAI Responses API request.
func openAIChatToResponsesRequest(chatReq map[string]any, model string) (map[string]any, error) {
	responsesReq := map[string]any{"model": model}
	rawMessages, ok := chatReq["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("chat request missing messages array")
	}
	inputItems, instructions, err := inputItemsFromChatMessages(rawMessages)
	if err != nil {
		return nil, err
	}
	if instructions != "" {
		responsesReq["instructions"] = instructions
	}
	setResponsesInput(responsesReq, inputItems)

	if maxTokens, exists := chatReq["max_tokens"]; exists {
		responsesReq["max_output_tokens"] = maxTokens
	} else if maxTokens, exists := chatReq["max_completion_tokens"]; exists {
		responsesReq["max_output_tokens"] = maxTokens
	}
	if stream, ok := chatReq["stream"].(bool); ok {
		responsesReq["stream"] = stream
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := chatReq[key]; exists {
			responsesReq[key] = value
		}
	}
	if depth := normalizeReasoningEffort(stringValue(chatReq["reasoning_effort"])); depth != "" {
		responsesReq["reasoning"] = map[string]any{"effort": depth}
	}
	copyToolsFieldDirect(chatReq, responsesReq)
	return responsesReq, nil
}

func responsesInputBlockToChat(block map[string]any) map[string]any {
	blockType := strings.TrimSpace(stringValue(block["type"]))
	switch blockType {
	case "input_text", "output_text":
		text := stringValue(block["text"])
		if text == "" {
			return nil
		}
		return map[string]any{"type": "text", "text": text}
	case "input_image":
		url := ""
		detail := strings.TrimSpace(stringValue(block["detail"]))
		switch typed := block["image_url"].(type) {
		case string:
			url = strings.TrimSpace(typed)
		case map[string]any:
			url = strings.TrimSpace(stringValue(typed["url"]))
			if detail == "" {
				detail = strings.TrimSpace(stringValue(typed["detail"]))
			}
		}
		if url == "" {
			return nil
		}
		imageURL := map[string]any{"url": url}
		if detail != "" {
			imageURL["detail"] = detail
		}
		return map[string]any{"type": "image_url", "image_url": imageURL}
	case "image_url", "text":
		if cloned := cloneAnyMap(block); cloned != nil {
			return cloned
		}
		return nil
	default:
		if cloned := cloneAnyMap(block); cloned != nil {
			return cloned
		}
		return nil
	}
}

func normalizeResponsesContentForChat(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		blocks := make([]any, 0, len(typed))
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if converted := responsesInputBlockToChat(block); converted != nil {
				blocks = append(blocks, converted)
			}
		}
		if len(blocks) == 0 {
			return ""
		}
		if len(blocks) == 1 {
			if single, ok := blocks[0].(map[string]any); ok && stringValue(single["type"]) == "text" {
				if text := stringValue(single["text"]); text != "" && len(single) <= 2 {
					return text
				}
			}
		}
		return blocks
	default:
		return normalizeClaudeContentForOpenAI(content)
	}
}

// appendChatFunctionCall folds a Responses function_call into Chat Completions
// history. Codex/Responses often emit a preceding assistant message item (preamble
// text) and a separate function_call item; many OpenAI-compatible upstreams
// (including GLM) expect a single assistant message with both content and
// tool_calls. Splitting them can yield empty streams or off-topic continuations.
func appendChatFunctionCall(messages []map[string]any, callID, name, arguments string) []map[string]any {
	toolCall := map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	if n := len(messages); n > 0 {
		last := messages[n-1]
		if stringValue(last["role"]) == "assistant" {
			if raw, ok := last["tool_calls"].([]any); ok {
				last["tool_calls"] = append(raw, toolCall)
				return messages
			}
			last["tool_calls"] = []any{toolCall}
			return messages
		}
	}
	return append(messages, map[string]any{
		"role":       "assistant",
		"content":    nil,
		"tool_calls": []any{toolCall},
	})
}

func chatMessagesFromResponsesInput(input any) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, 8)
	switch typed := input.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, fmt.Errorf("responses request has empty input")
		}
		return []map[string]any{{"role": "user", "content": typed}}, nil
	case []any:
		for _, item := range typed {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			itemType := strings.TrimSpace(stringValue(entry["type"]))
			if itemType == "message" {
				role := strings.TrimSpace(stringValue(entry["role"]))
				if role == "" {
					continue
				}
				if role == "system" || role == "developer" {
					role = "system"
				}
				if role != "user" && role != "assistant" && role != "system" {
					continue
				}
				content := normalizeResponsesContentForChat(entry["content"])
				if isEmptyOpenAIContent(content) {
					continue
				}
				messages = append(messages, map[string]any{"role": role, "content": content})
				continue
			}
			if itemType == "function_call" {
				callID := sanitizeResponsesCallID(stringValue(entry["call_id"]))
				name := stringValue(entry["name"])
				if callID == "" || name == "" {
					continue
				}
				messages = appendChatFunctionCall(messages, callID, name, stringValue(entry["arguments"]))
				continue
			}
			if itemType == "function_call_output" {
				callID := sanitizeResponsesCallID(stringValue(entry["call_id"]))
				if callID == "" {
					continue
				}
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      toolResultContentToString(entry["output"]),
				})
				continue
			}
			role := strings.TrimSpace(stringValue(entry["role"]))
			if role == "" {
				continue
			}
			if role == "system" || role == "developer" {
				role = "system"
			}
			if role != "user" && role != "assistant" && role != "system" {
				continue
			}
			content := normalizeResponsesContentForChat(entry["content"])
			if isEmptyOpenAIContent(content) {
				continue
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		}
	default:
		text := strings.TrimSpace(fmt.Sprint(input))
		if text == "" {
			return nil, fmt.Errorf("responses request has empty input")
		}
		return []map[string]any{{"role": "user", "content": text}}, nil
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("responses request has no usable input items")
	}
	return messages, nil
}

// responsesToOpenAIChatRequest converts an OpenAI Responses API request to an
// OpenAI Chat Completions request.
func responsesToOpenAIChatRequest(responsesReq map[string]any, model string) (map[string]any, error) {
	chatReq := map[string]any{"model": model}
	messages := make([]map[string]any, 0, 8)
	if instructions := strings.TrimSpace(stringValue(responsesReq["instructions"])); instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	inputMessages, err := chatMessagesFromResponsesInput(responsesReq["input"])
	if err != nil {
		return nil, err
	}
	messages = append(messages, inputMessages...)
	messageItems := make([]any, 0, len(messages))
	for _, message := range messages {
		messageItems = append(messageItems, message)
	}
	chatReq["messages"] = messageItems

	if maxTokens, exists := responsesReq["max_output_tokens"]; exists {
		chatReq["max_tokens"] = maxTokens
	}
	if stream, ok := responsesReq["stream"].(bool); ok && stream {
		chatReq["stream"] = true
		chatReq["stream_options"] = map[string]any{"include_usage": true}
	} else {
		chatReq["stream"] = false
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := responsesReq[key]; exists {
			chatReq[key] = value
		}
	}
	normalizeOpenAIChatMaxTokensField(chatReq, model)
	if reasoning, ok := responsesReq["reasoning"].(map[string]any); ok {
		if effort := normalizeReasoningEffort(stringValue(reasoning["effort"])); effort != "" {
			chatReq["reasoning_effort"] = effort
		}
	}
	copyResponsesToolsToChat(responsesReq, chatReq)
	return chatReq, nil
}

func responsesToOpenAIChatResponse(responsesBody []byte, model string) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(responsesBody, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return responsesErrorValueToOpenAI(errorValue, model)
	}

	text := strings.TrimSpace(stringValue(payload["output_text"]))
	if text == "" {
		text = responsesOutputText(payload["output"])
	}
	assistantMessage := map[string]any{"role": "assistant", "content": text}
	if outputItems, ok := payload["output"].([]any); ok {
		for _, item := range outputItems {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if stringValue(block["type"]) == "function_call" {
				callID := sanitizeResponsesCallID(stringValue(block["call_id"]))
				name := stringValue(block["name"])
				if callID == "" || name == "" {
					continue
				}
				assistantMessage["content"] = nil
				assistantMessage["tool_calls"] = []any{map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": stringValue(block["arguments"]),
					},
				}}
				break
			}
		}
	}
	usage := ParseResponsesUsage(responsesBody)
	openAIUsage := map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheTokens > 0 {
		openAIUsage["prompt_cache_hit_tokens"] = usage.CacheTokens
	}

	finishReason := "stop"
	if _, ok := assistantMessage["tool_calls"]; ok {
		finishReason = "tool_calls"
	}

	response := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(model, stringValue(payload["model"])),
		"choices": []map[string]any{{
			"index":         0,
			"message":       assistantMessage,
			"finish_reason": finishReason,
		}},
		"usage": openAIUsage,
	}
	body, err := json.Marshal(response)
	return body, usage, err
}

func openAIChatToResponsesResponse(chatBody []byte, model string) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(chatBody, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return chatErrorValueToResponses(errorValue, model)
	}

	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, TokenUsage{}, fmt.Errorf("chat response missing choices")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, TokenUsage{}, fmt.Errorf("chat response has invalid choice")
	}
	message, _ := choice["message"].(map[string]any)
	text := claudeContentToString(message["content"])
	if text == "" && message != nil {
		text = strings.TrimSpace(fmt.Sprint(message["content"]))
	}

	usage := ParseOpenAIUsage(chatBody)
	output := []map[string]any{{
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{{
			"type": "output_text",
			"text": text,
		}},
	}}
	status := "completed"
	if rawToolCalls, ok := message["tool_calls"].([]any); ok && len(rawToolCalls) > 0 {
		output = make([]map[string]any, 0, len(rawToolCalls))
		for _, item := range rawToolCalls {
			call, ok := item.(map[string]any)
			if !ok {
				continue
			}
			functionValue, _ := call["function"].(map[string]any)
			name := stringValue(functionValue["name"])
			if name == "" {
				continue
			}
			output = append(output, map[string]any{
				"type":      "function_call",
				"call_id":   sanitizeResponsesCallID(stringValue(call["id"])),
				"name":      name,
				"arguments": stringValue(functionValue["arguments"]),
			})
		}
		text = ""
	}
	response := map[string]any{
		"id":          fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      status,
		"model":       firstNonEmpty(model, stringValue(payload["model"])),
		"output_text": text,
		"output":      output,
		"usage": map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  usage.InputTokens + usage.OutputTokens,
		},
	}
	body, err := json.Marshal(response)
	return body, usage, err
}

func responsesErrorValueToOpenAI(errorValue any, model string) ([]byte, TokenUsage, error) {
	message, errorType := errorMessageFromValue(errorValue, "upstream request failed")
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
			"code":    errorType,
		},
	})
	_ = model
	return body, TokenUsage{}, err
}

func chatErrorValueToResponses(errorValue any, model string) ([]byte, TokenUsage, error) {
	message, errorType := errorMessageFromValue(errorValue, "upstream request failed")
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
		},
	})
	_ = model
	return body, TokenUsage{}, err
}

// responsesToClaudeRequest converts Responses → Claude directly (no Chat IR).
func responsesToClaudeRequest(responsesReq map[string]any, model string) (map[string]any, error) {
	return responsesToClaudeRequestDirect(responsesReq, model, 0)
}

// claudeToResponsesRequest converts Claude → Responses directly (no Chat IR).
func claudeToResponsesRequest(claudeReq map[string]any, model string) (map[string]any, error) {
	return claudeToResponsesRequestDirect(claudeReq, model)
}

// responsesToClaudeResponse converts Responses → Claude directly (no Chat IR).
func responsesToClaudeResponse(responsesBody []byte, model string) ([]byte, TokenUsage, error) {
	return responsesToClaudeResponseDirect(responsesBody, model)
}

func claudeToResponsesResponse(claudeBody []byte, model string) ([]byte, TokenUsage, error) {
	return claudeToResponsesResponseWithTools(claudeBody, model, nil)
}

// claudeToResponsesResponseWithTools restores the client's original tool names
// (pre-cloaking) when converting a non-streamed Claude response back to
// Responses. Passing nil preserves the legacy behavior.
func claudeToResponsesResponseWithTools(claudeBody []byte, model string, clientToolNames map[string]struct{}) ([]byte, TokenUsage, error) {
	return claudeToResponsesResponseDirect(claudeBody, model, clientToolNames)
}
