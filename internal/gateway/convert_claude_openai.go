package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func openAIContentToClaudeBlocks(content any) []any {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	case []any:
		blocks := cloneContentBlocks(typed)
		for index, block := range blocks {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if item["type"] == "input_text" {
				item["type"] = "text"
			}
			blocks[index] = item
		}
		return blocks
	default:
		text := strings.TrimSpace(fmt.Sprint(content))
		if text == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": text}}
	}
}

func normalizeOpenAIContentForClaude(content any) any {
	blocks := openAIContentToClaudeBlocks(content)
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 && !contentBlockArrayHasCacheControl(blocks) {
		if block, ok := blocks[0].(map[string]any); ok {
			if text, ok := block["text"].(string); ok && len(block) <= 2 {
				return text
			}
		}
	}
	return blocks
}

func isEmptyClaudeContent(content any) bool {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return strings.TrimSpace(fmt.Sprint(content)) == ""
	}
}

func claudeSystemValueFromBlocks(blocks []any) any {
	if len(blocks) == 0 {
		return nil
	}
	if len(blocks) == 1 && !contentBlockArrayHasCacheControl(blocks) {
		if block, ok := blocks[0].(map[string]any); ok {
			if text, ok := block["text"].(string); ok && len(block) <= 2 {
				return text
			}
		}
	}
	return blocks
}

// openAIChatToClaudeRequest converts an OpenAI Chat Completions request body to
// an Anthropic Messages API request, preserving cache_control on content blocks.
func openAIChatToClaudeRequest(openAIReq map[string]any, model string) (map[string]any, error) {
	claudeReq := map[string]any{"model": model}
	rawMessages, ok := openAIReq["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("openai request missing messages array")
	}

	systemBlocks, claudeMessages, err := openAIMessagesToClaude(rawMessages)
	if err != nil {
		return nil, err
	}
	claudeReq["messages"] = claudeMessages
	if systemValue := claudeSystemValueFromBlocks(systemBlocks); systemValue != nil {
		claudeReq["system"] = systemValue
	}

	if maxTokens, exists := openAIReq["max_tokens"]; exists {
		claudeReq["max_tokens"] = maxTokens
	} else if maxTokens, exists := openAIReq["max_completion_tokens"]; exists {
		claudeReq["max_tokens"] = maxTokens
	} else {
		claudeReq["max_tokens"] = 4096
	}
	if stream, ok := openAIReq["stream"].(bool); ok && stream {
		claudeReq["stream"] = true
	} else {
		claudeReq["stream"] = false
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := openAIReq[key]; exists {
			claudeReq[key] = value
		}
	}
	copyToolsField(openAIReq, claudeReq, true)
	copyToolChoiceField(openAIReq, claudeReq, true)
	applyOpenAIThinkingToClaudeRequest(openAIReq, claudeReq, model)
	return claudeReq, nil
}

func claudeModelRequiresAdaptiveThinking(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "sonnet-5"),
		strings.Contains(model, "fable-5"),
		strings.Contains(model, "mythos"),
		strings.Contains(model, "opus-4-7"),
		strings.Contains(model, "opus-4-8"):
		return true
	default:
		return false
	}
}

func claudeModelSupportsAdaptiveThinking(model string) bool {
	if claudeModelRequiresAdaptiveThinking(model) {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "opus-4-6") || strings.Contains(model, "sonnet-4-6")
}

func claudeModelSupportsXHighEffort(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "sonnet-5") ||
		strings.Contains(model, "fable-5") ||
		strings.Contains(model, "mythos") ||
		strings.Contains(model, "opus-4-7") ||
		strings.Contains(model, "opus-4-8")
}

func normalizeReasoningEffort(depth string) string {
	depth = strings.ToLower(strings.TrimSpace(depth))
	switch depth {
	case "", "none", "off", "disabled":
		return ""
	default:
		return depth
	}
}

func reasoningEffortBudgetTokens(depth string) int {
	switch normalizeReasoningEffort(depth) {
	case "low":
		return 4096
	case "medium":
		return 10000
	case "high":
		return 16000
	case "max", "xhigh":
		return 32000
	default:
		return 10000
	}
}

func mapReasoningEffortToClaudeEffort(depth, model string) string {
	depth = normalizeReasoningEffort(depth)
	if depth == "" {
		return "high"
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if depth == "max" && !strings.Contains(model, "opus-4-6") {
		return "high"
	}
	if depth == "xhigh" && !claudeModelSupportsXHighEffort(model) {
		return "high"
	}
	for _, allowed := range []string{"low", "medium", "high", "max", "xhigh"} {
		if depth == allowed {
			return depth
		}
	}
	return "high"
}

func applyAdaptiveThinking(claudeReq map[string]any, effort string) {
	claudeReq["thinking"] = map[string]any{"type": "adaptive"}
	claudeReq["output_config"] = map[string]any{"effort": effort}
}

func applyEnabledThinking(claudeReq map[string]any, budgetTokens int) {
	claudeReq["thinking"] = map[string]any{
		"type":          "enabled",
		"budget_tokens": budgetTokens,
	}
}

func normalizeExplicitThinkingForModel(thinking map[string]any, model string) (map[string]any, map[string]any) {
	thinkingType := strings.ToLower(strings.TrimSpace(stringValue(thinking["type"])))
	if thinkingType == "" {
		thinkingType = "enabled"
	}
	if thinkingType == "disabled" {
		if claudeModelRequiresAdaptiveThinking(model) {
			return map[string]any{"type": "adaptive"}, map[string]any{"effort": "low"}
		}
		return map[string]any{"type": "disabled"}, nil
	}
	if thinkingType == "adaptive" {
		outputConfig, _ := thinking["output_config"].(map[string]any)
		if outputConfig == nil {
			if effort := normalizeReasoningEffort(stringValue(thinking["effort"])); effort != "" {
				outputConfig = map[string]any{"effort": mapReasoningEffortToClaudeEffort(effort, model)}
			} else {
				outputConfig = map[string]any{"effort": "high"}
			}
		}
		return map[string]any{"type": "adaptive"}, outputConfig
	}
	if claudeModelRequiresAdaptiveThinking(model) {
		effort := "high"
		if budget, ok := thinking["budget_tokens"]; ok {
			switch v := budget.(type) {
			case float64:
				switch {
				case v <= 4096:
					effort = "low"
				case v <= 10000:
					effort = "medium"
				default:
					effort = "high"
				}
			case int:
				switch {
				case v <= 4096:
					effort = "low"
				case v <= 10000:
					effort = "medium"
				default:
					effort = "high"
				}
			}
		}
		return map[string]any{"type": "adaptive"}, map[string]any{"effort": effort}
	}
	normalized := map[string]any{"type": "enabled"}
	if budget, exists := thinking["budget_tokens"]; exists {
		normalized["budget_tokens"] = budget
	} else {
		normalized["budget_tokens"] = 10000
	}
	return normalized, nil
}

func applyOpenAIThinkingToClaudeRequest(openAIReq map[string]any, claudeReq map[string]any, model string) {
	if rawThinking, exists := openAIReq["thinking"]; exists {
		if thinking, ok := rawThinking.(map[string]any); ok {
			normalizedThinking, outputConfig := normalizeExplicitThinkingForModel(thinking, model)
			claudeReq["thinking"] = normalizedThinking
			if outputConfig != nil {
				claudeReq["output_config"] = outputConfig
			}
		}
		normalizeClaudeTemperatureForThinking(claudeReq)
		return
	}
	depth := normalizeReasoningEffort(stringValue(openAIReq["reasoning_effort"]))
	if depth == "" {
		return
	}
	if claudeModelSupportsAdaptiveThinking(model) {
		applyAdaptiveThinking(claudeReq, mapReasoningEffortToClaudeEffort(depth, model))
	} else {
		applyEnabledThinking(claudeReq, reasoningEffortBudgetTokens(depth))
	}
	normalizeClaudeTemperatureForThinking(claudeReq)
}

// normalizeClaudeTemperatureForThinking forces temperature=1 when extended
// thinking is active. Claude rejects non-1 temperature with thinking enabled.
func normalizeClaudeTemperatureForThinking(claudeReq map[string]any) {
	if claudeReq["thinking"] == nil {
		return
	}
	claudeReq["temperature"] = 1
}

func mapClaudeStopReasonToOpenAI(reason string) string {
	switch strings.TrimSpace(reason) {
	case "", "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

func claudeUsageToOpenAIUsage(usage TokenUsage, usageMap map[string]any) map[string]any {
	// usage.InputTokens is already normalized to include cache read/creation.
	openAIUsage := map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
	}
	if usageMap != nil {
		if value, exists := usageMap["cache_read_input_tokens"]; exists {
			openAIUsage["prompt_cache_hit_tokens"] = value
		}
		if value, exists := usageMap["cache_creation_input_tokens"]; exists {
			openAIUsage["cache_creation_input_tokens"] = value
		}
	} else if usage.CacheTokens > 0 {
		openAIUsage["prompt_cache_hit_tokens"] = usage.CacheTokens
	}
	if usage.CacheTokens > 0 {
		openAIUsage["prompt_tokens_details"] = map[string]any{"cached_tokens": usage.CacheTokens}
	}
	return openAIUsage
}

// claudeResponseToOpenAIChat converts an Anthropic Messages API JSON response
// into an OpenAI Chat Completions response.
func claudeResponseToOpenAIChat(claudeResp []byte, model string, clientToolNames map[string]struct{}) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(claudeResp, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return claudeErrorValueToOpenAI(errorValue, model)
	}

	assistantMessage := claudeResponseContentToOpenAIAssistantMessage(payload["content"], clientToolNames)
	stopReason := mapClaudeStopReasonToOpenAI(stringValue(payload["stop_reason"]))
	usage := ParseClaudeUsage(claudeResp)
	openAIUsage := claudeUsageToOpenAIUsage(usage, extractCacheUsage(claudeResp))

	response := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(model, stringValue(payload["model"])),
		"choices": []map[string]any{{
			"index":         0,
			"message":       assistantMessage,
			"finish_reason": stopReason,
		}},
		"usage": openAIUsage,
	}
	body, err := json.Marshal(response)
	return body, usage, err
}

func claudeErrorValueToOpenAI(errorValue any, model string) ([]byte, TokenUsage, error) {
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
