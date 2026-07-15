package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func claudeContentToString(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, block := range typed {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := item["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func cloneContentBlocks(blocks []any) []any {
	cloned := make([]any, 0, len(blocks))
	for _, block := range blocks {
		item, ok := block.(map[string]any)
		if !ok {
			continue
		}
		copyBlock := make(map[string]any, len(item))
		for key, value := range item {
			copyBlock[key] = value
		}
		cloned = append(cloned, copyBlock)
	}
	return cloned
}

func contentBlockArrayHasCacheControl(blocks []any) bool {
	for _, block := range blocks {
		item, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := item["cache_control"]; exists {
			return true
		}
	}
	return false
}

// stripClaudeThinkingBlocks removes Anthropic thinking / redacted_thinking
// blocks. OpenAI-compatible Chat upstreams (GLM, DeepSeek, …) reject
// content[].type=thinking with 400 "type type error".
func stripClaudeThinkingBlocks(blocks []any) []any {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		item, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "thinking", "redacted_thinking":
			continue
		default:
			out = append(out, item)
		}
	}
	return out
}

// normalizeClaudeContentForOpenAI preserves structured content blocks (and
// cache_control) when converting Claude requests to OpenAI Chat format.
// Thinking blocks are dropped — Chat upstreams do not accept them.
func normalizeClaudeContentForOpenAI(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		blocks := stripClaudeThinkingBlocks(cloneContentBlocks(typed))
		if len(blocks) == 0 {
			return ""
		}
		if len(blocks) == 1 && !contentBlockArrayHasCacheControl(blocks) {
			if text, ok := blocks[0].(map[string]any)["text"].(string); ok && len(blocks[0].(map[string]any)) <= 2 {
				return text
			}
		}
		return blocks
	default:
		return claudeContentToString(content)
	}
}

func claudeSystemToOpenAIMessages(system any) []map[string]any {
	if system == nil {
		return nil
	}
	switch typed := system.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []map[string]any{{"role": "system", "content": typed}}
	case []any:
		blocks := cloneContentBlocks(typed)
		if len(blocks) == 0 {
			return nil
		}
		if len(blocks) == 1 && !contentBlockArrayHasCacheControl(blocks) {
			if text, ok := blocks[0].(map[string]any)["text"].(string); ok && len(blocks[0].(map[string]any)) <= 2 {
				return []map[string]any{{"role": "system", "content": text}}
			}
		}
		return []map[string]any{{"role": "system", "content": blocks}}
	default:
		text := claudeContentToString(system)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []map[string]any{{"role": "system", "content": text}}
	}
}

// claudeRequestToOpenAIChat converts an Anthropic Messages API request body to
// an OpenAI Chat Completions request.
func claudeRequestToOpenAIChat(claudeReq map[string]any, model string) (map[string]any, error) {
	openAIReq := map[string]any{"model": model}
	messages := make([]map[string]any, 0, 8)
	messages = append(messages, claudeSystemToOpenAIMessages(claudeReq["system"])...)

	rawMessages, ok := claudeReq["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("claude request missing messages array")
	}
	convertedMessages, err := claudeMessagesToOpenAI(rawMessages)
	if err != nil {
		return nil, err
	}
	messages = append(messages, convertedMessages...)
	messageItems := make([]any, 0, len(messages))
	for _, message := range messages {
		messageItems = append(messageItems, message)
	}
	openAIReq["messages"] = messageItems

	if stream, ok := claudeReq["stream"].(bool); ok && stream {
		openAIReq["stream"] = true
		openAIReq["stream_options"] = map[string]any{"include_usage": true}
	} else {
		openAIReq["stream"] = false
	}
	for _, key := range []string{"max_tokens", "temperature", "top_p"} {
		if value, exists := claudeReq[key]; exists {
			openAIReq[key] = value
		}
	}
	normalizeOpenAIChatMaxTokensField(openAIReq, model)
	copyToolsField(claudeReq, openAIReq, false)
	copyToolChoiceField(claudeReq, openAIReq, false)
	return openAIReq, nil
}

// openAIModelPrefersMaxCompletionTokens reports models (notably GPT-5 / o-series)
// that reject the legacy Chat Completions field max_tokens in favor of
// max_completion_tokens.
func openAIModelPrefersMaxCompletionTokens(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	switch {
	case strings.HasPrefix(normalized, "gpt-5"),
		strings.HasPrefix(normalized, "o1"),
		strings.HasPrefix(normalized, "o3"),
		strings.HasPrefix(normalized, "o4"):
		return true
	default:
		return false
	}
}

// normalizeOpenAIChatMaxTokensField rewrites max_tokens → max_completion_tokens
// for models that require the newer field name (e.g. gpt-5.5 via Azure/Tuya).
func normalizeOpenAIChatMaxTokensField(openAIReq map[string]any, model string) {
	if openAIReq == nil || !openAIModelPrefersMaxCompletionTokens(model) {
		return
	}
	if _, exists := openAIReq["max_completion_tokens"]; exists {
		delete(openAIReq, "max_tokens")
		return
	}
	if value, exists := openAIReq["max_tokens"]; exists {
		openAIReq["max_completion_tokens"] = value
		delete(openAIReq, "max_tokens")
	}
}

func isEmptyOpenAIContent(content any) bool {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return strings.TrimSpace(fmt.Sprint(content)) == ""
	}
}

func usageMapToClaudeUsage(usage TokenUsage, usageMap map[string]any) map[string]any {
	cacheRead := usage.CacheTokens
	cacheCreation := int64(0)
	claudeUsage := map[string]any{
		"output_tokens": usage.OutputTokens,
	}
	if usageMap != nil {
		if value, exists := usageMap["cache_read_input_tokens"]; exists {
			cacheRead = int64FromAny(value)
			claudeUsage["cache_read_input_tokens"] = value
		} else if value, exists := usageMap["prompt_cache_hit_tokens"]; exists {
			cacheRead = int64FromAny(value)
			claudeUsage["cache_read_input_tokens"] = value
		}
		if value, exists := usageMap["cache_creation_input_tokens"]; exists {
			cacheCreation = int64FromAny(value)
			claudeUsage["cache_creation_input_tokens"] = value
		}
		if value, exists := usageMap["prompt_cache_miss_tokens"]; exists {
			claudeUsage["prompt_cache_miss_tokens"] = value
		}
	}
	if _, exists := claudeUsage["cache_read_input_tokens"]; !exists && usage.CacheTokens > 0 {
		cacheRead = usage.CacheTokens
		claudeUsage["cache_read_input_tokens"] = usage.CacheTokens
	}
	// Anthropic clients expect input_tokens to exclude cache read/creation.
	claudeUsage["input_tokens"] = claudeExclusiveInputTokens(TokenUsage{
		InputTokens: usage.InputTokens,
		CacheTokens: cacheRead,
	}, cacheCreation)
	return claudeUsage
}

// openAIChatResponseToClaude converts an OpenAI Chat Completions JSON response
// into an Anthropic Messages API response.
func openAIChatResponseToClaude(openAIResp []byte, model string) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(openAIResp, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return openAIErrorValueToClaude(errorValue, model)
	}

	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, TokenUsage{}, fmt.Errorf("openai response missing choices")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, TokenUsage{}, fmt.Errorf("openai response has invalid choice")
	}
	message, _ := choice["message"].(map[string]any)
	contentBlocks := []any{map[string]any{"type": "text", "text": ""}}
	if message != nil {
		contentBlocks = openAIAssistantMessageToClaudeResponseContent(message)
	}

	stopReason := "end_turn"
	if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
		stopReason = mapOpenAIFinishReasonToClaude(reason)
	}

	usage := ParseOpenAIUsage(openAIResp)
	claudeUsage := usageMapToClaudeUsage(usage, extractCacheUsage(openAIResp))

	response := map[string]any{
		"id":            fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         firstNonEmpty(model, stringValue(payload["model"])),
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         claudeUsage,
	}
	body, err := json.Marshal(response)
	return body, usage, err
}

func openAIErrorValueToClaude(errorValue any, model string) ([]byte, TokenUsage, error) {
	message, errorType := errorMessageFromValue(errorValue, "upstream request failed")
	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
	_ = model
	return body, TokenUsage{}, err
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
