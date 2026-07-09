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
			callID := stringValue(entry["tool_call_id"])
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
						"call_id":   stringValue(call["id"]),
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
			if itemType == "function_call" {
				callID := stringValue(entry["call_id"])
				name := stringValue(entry["name"])
				if callID == "" || name == "" {
					continue
				}
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id":   callID,
						"type": "function",
						"function": map[string]any{
							"name":      name,
							"arguments": stringValue(entry["arguments"]),
						},
					}},
				})
				continue
			}
			if itemType == "function_call_output" {
				callID := stringValue(entry["call_id"])
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
			content := normalizeClaudeContentForOpenAI(entry["content"])
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
	copyToolsFieldDirect(responsesReq, chatReq)
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
				callID := stringValue(block["call_id"])
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
				"call_id":   stringValue(call["id"]),
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

func responsesToClaudeRequest(responsesReq map[string]any, model string) (map[string]any, error) {
	chatReq, err := responsesToOpenAIChatRequest(responsesReq, model)
	if err != nil {
		return nil, err
	}
	return openAIChatToClaudeRequest(chatReq, model)
}

func claudeToResponsesRequest(claudeReq map[string]any, model string) (map[string]any, error) {
	chatReq, err := claudeRequestToOpenAIChat(claudeReq, model)
	if err != nil {
		return nil, err
	}
	return openAIChatToResponsesRequest(chatReq, model)
}

func responsesToClaudeResponse(responsesBody []byte, model string) ([]byte, TokenUsage, error) {
	chatBody, _, err := responsesToOpenAIChatResponse(responsesBody, model)
	if err != nil {
		return nil, TokenUsage{}, err
	}
	return openAIChatResponseToClaude(chatBody, model)
}

func claudeToResponsesResponse(claudeBody []byte, model string) ([]byte, TokenUsage, error) {
	chatBody, usage, err := claudeResponseToOpenAIChat(claudeBody, model, nil)
	if err != nil {
		return nil, TokenUsage{}, err
	}
	_ = usage
	return openAIChatToResponsesResponse(chatBody, model)
}
