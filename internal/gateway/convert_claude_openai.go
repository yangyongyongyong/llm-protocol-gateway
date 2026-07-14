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
		blocks := make([]any, 0, len(typed))
		for _, block := range typed {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if converted := openAIContentBlockToClaude(item); converted != nil {
				blocks = append(blocks, converted)
			}
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

func openAIContentBlockToClaude(item map[string]any) map[string]any {
	blockType := stringValue(item["type"])
	switch blockType {
	case "input_text", "text":
		out := map[string]any{"type": "text", "text": stringValue(item["text"])}
		if cacheControl, ok := item["cache_control"]; ok {
			out["cache_control"] = cacheControl
		}
		return out
	case "image_url":
		return openAIImageURLBlockToClaude(item)
	case "image":
		// Already Claude-shaped (or close); pass through with a shallow copy.
		out := cloneAnyMap(item)
		if out == nil {
			return nil
		}
		out["type"] = "image"
		return out
	default:
		// Preserve unknown blocks (including cache_control-bearing text variants).
		out := cloneAnyMap(item)
		if out == nil {
			return nil
		}
		if blockType == "" {
			if _, hasText := out["text"]; hasText {
				out["type"] = "text"
			}
		}
		return out
	}
}

func openAIImageURLBlockToClaude(item map[string]any) map[string]any {
	imageURL, _ := item["image_url"].(map[string]any)
	if imageURL == nil {
		if url := stringValue(item["image_url"]); url != "" {
			imageURL = map[string]any{"url": url}
		}
	}
	if imageURL == nil {
		return nil
	}
	url := strings.TrimSpace(stringValue(imageURL["url"]))
	if url == "" {
		return nil
	}
	out := map[string]any{"type": "image"}
	if strings.HasPrefix(url, "data:") {
		mediaType, data, ok := splitDataURL(url)
		if !ok {
			return nil
		}
		out["source"] = map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		}
	} else {
		out["source"] = map[string]any{
			"type": "url",
			"url":  url,
		}
	}
	if cacheControl, ok := item["cache_control"]; ok {
		out["cache_control"] = cacheControl
	}
	return out
}

func splitDataURL(dataURL string) (mediaType, data string, ok bool) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(dataURL, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(dataURL, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	if data == "" {
		return "", "", false
	}
	mediaType = "image/png"
	if semi := strings.IndexByte(meta, ';'); semi >= 0 {
		if mt := strings.TrimSpace(meta[:semi]); mt != "" {
			mediaType = mt
		}
	} else if mt := strings.TrimSpace(meta); mt != "" {
		mediaType = mt
	}
	return mediaType, data, true
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
func openAIChatToClaudeRequest(openAIReq map[string]any, model string, maxTokensOverride int) (map[string]any, error) {
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

	// Anthropic 要求必填 max_tokens。预算必须按「实际上游模型」计算（本函数的
	// model 参数），不能看客户端 body 里的 model / max_tokens：客户端常按别名或
	// 错误目录写成 4096，会把长 agent 截断。密钥级覆盖 >0 时优先使用。
	claudeReq["max_tokens"] = effectiveClaudeMaxTokens(model, maxTokensOverride)
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
