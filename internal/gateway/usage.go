package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// TokenUsage holds parsed token counts from an upstream LLM response.
//
// InputTokens always uses OpenAI-compatible semantics: total prompt/input
// tokens INCLUDING cache hits (and Claude cache creation). CacheTokens is the
// cache-read/hit portion only, never added again on top of InputTokens.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
}

// normalizeClaudeUsageFields converts Anthropic usage fields into the gateway's
// OpenAI-compatible TokenUsage: Claude reports input_tokens as non-cached only,
// so total input = input_tokens + cache_read_input_tokens + cache_creation_input_tokens.
func normalizeClaudeUsageFields(inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int64) TokenUsage {
	totalInput := inputTokens + cacheReadTokens + cacheCreationTokens
	if totalInput < 0 {
		totalInput = 0
	}
	return TokenUsage{
		InputTokens:  totalInput,
		OutputTokens: outputTokens,
		CacheTokens:  cacheReadTokens,
	}
}

// claudeExclusiveInputTokens converts inclusive InputTokens back to Anthropic's
// non-cached input_tokens field for Claude client responses.
func claudeExclusiveInputTokens(usage TokenUsage, cacheCreationTokens int64) int64 {
	exclusive := usage.InputTokens - usage.CacheTokens - cacheCreationTokens
	if exclusive < 0 {
		return 0
	}
	return exclusive
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}

// ParseOpenAIUsage extracts prompt/completion/cache-hit tokens from an OpenAI
// Chat Completions JSON response body, or from an SSE stream body.
func ParseOpenAIUsage(body []byte) TokenUsage {
	if usage := parseOpenAIUsageJSON(body); usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheTokens > 0 {
		return usage
	}
	return parseOpenAIUsageFromSSE(body)
}

func parseOpenAIUsageJSON(body []byte) TokenUsage {
	usage := extractCacheUsage(body)
	if usage == nil {
		return TokenUsage{}
	}
	return TokenUsage{
		InputTokens:  int64FromAny(usage["prompt_tokens"]),
		OutputTokens: int64FromAny(usage["completion_tokens"]),
		CacheTokens:  int64(cacheHitTokenCount(usage)),
	}
}

func parseOpenAIUsageFromSSE(body []byte) TokenUsage {
	text := strings.TrimSpace(string(body))
	if text == "" || !strings.Contains(text, "data:") {
		return TokenUsage{}
	}
	usage := TokenUsage{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if chunkUsage := parseOpenAIUsageJSON([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CacheTokens > 0 {
			usage = chunkUsage
		}
	}
	return usage
}

// ParseClaudeUsage extracts input/output/cache-read tokens from an Anthropic
// Messages API JSON response body or SSE stream body.
func ParseClaudeUsage(body []byte) TokenUsage {
	if usage := parseClaudeUsageJSON(body); usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheTokens > 0 {
		return usage
	}
	return parseClaudeUsageFromSSE(body)
}

func parseClaudeUsageMap(usage map[string]any) TokenUsage {
	if usage == nil {
		return TokenUsage{}
	}
	return normalizeClaudeUsageFields(
		int64FromAny(usage["input_tokens"]),
		int64FromAny(usage["output_tokens"]),
		int64FromAny(usage["cache_read_input_tokens"]),
		int64FromAny(usage["cache_creation_input_tokens"]),
	)
}

func parseClaudeUsageJSON(body []byte) TokenUsage {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return TokenUsage{}
	}
	usageValue, ok := payload["usage"]
	if !ok {
		return TokenUsage{}
	}
	usage, ok := usageValue.(map[string]any)
	if !ok {
		return TokenUsage{}
	}
	return parseClaudeUsageMap(usage)
}

func parseClaudeUsageFromSSE(body []byte) TokenUsage {
	text := strings.TrimSpace(string(body))
	if text == "" || !strings.Contains(text, "data:") {
		return TokenUsage{}
	}
	usage := TokenUsage{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch stringValue(event["type"]) {
		case "message_start":
			if message, ok := event["message"].(map[string]any); ok {
				if usageValue, ok := message["usage"].(map[string]any); ok {
					parsed := parseClaudeUsageMap(usageValue)
					// Keep later message_delta output/cache overrides when present.
					if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheTokens == 0 {
						usage = parsed
					} else {
						if parsed.InputTokens > 0 {
							usage.InputTokens = parsed.InputTokens
						}
						if parsed.CacheTokens > 0 {
							usage.CacheTokens = parsed.CacheTokens
						}
						if parsed.OutputTokens > usage.OutputTokens {
							usage.OutputTokens = parsed.OutputTokens
						}
					}
				}
			}
		case "message_delta":
			if usageValue, ok := event["usage"].(map[string]any); ok {
				parsed := parseClaudeUsageMap(usageValue)
				if parsed.InputTokens > 0 || parsed.CacheTokens > 0 {
					usage.InputTokens = parsed.InputTokens
					usage.CacheTokens = parsed.CacheTokens
				}
				if parsed.OutputTokens > 0 {
					usage.OutputTokens = parsed.OutputTokens
				}
			}
		}
	}
	return usage
}

// ParseResponsesUsage extracts token counts from an OpenAI Responses API body.
func ParseResponsesUsage(body []byte) TokenUsage {
	if usage := parseResponsesUsageJSON(body); usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheTokens > 0 {
		return usage
	}
	return parseResponsesUsageFromSSE(body)
}

func parseResponsesUsageJSON(body []byte) TokenUsage {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return TokenUsage{}
	}
	usageValue, ok := payload["usage"].(map[string]any)
	if !ok {
		if response, ok := payload["response"].(map[string]any); ok {
			usageValue, ok = response["usage"].(map[string]any)
		}
	}
	if !ok {
		return TokenUsage{}
	}
	cacheTokens := int64(0)
	if details, ok := usageValue["input_tokens_details"].(map[string]any); ok {
		cacheTokens = int64FromAny(details["cached_tokens"])
	}
	return TokenUsage{
		InputTokens:  int64FromAny(usageValue["input_tokens"]),
		OutputTokens: int64FromAny(usageValue["output_tokens"]),
		CacheTokens:  cacheTokens,
	}
}

func parseResponsesUsageFromSSE(body []byte) TokenUsage {
	text := strings.TrimSpace(string(body))
	if text == "" || !strings.Contains(text, "data:") {
		return TokenUsage{}
	}
	usage := TokenUsage{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if chunkUsage := parseResponsesUsageJSON([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CacheTokens > 0 {
			usage = chunkUsage
		}
	}
	return usage
}

// EstimateTokenUsage falls back to a rough body-length estimate when real usage
// is unavailable (e.g. streaming responses).
func EstimateTokenUsage(requestBody, responseBody []byte) TokenUsage {
	return TokenUsage{
		InputTokens:  int64(len(requestBody) / 4),
		OutputTokens: int64(len(responseBody) / 4),
	}
}
