package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func writeClaudeSSE(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// streamOpenAIChatToClaudeEvents reads an OpenAI Chat Completions SSE stream and
// writes an Anthropic Messages API SSE stream to w.
func streamOpenAIChatToClaudeEvents(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	resolvedModel := strings.TrimSpace(model)
	messageStarted := false
	blockIndex := 0
	textBlockOpen := false
	toolBlocks := make(map[int]bool)
	toolBlockIndexByCall := make(map[int]int)
	usage := TokenUsage{}
	stopReason := "end_turn"

	closeTextBlock := func() error {
		if !textBlockOpen {
			return nil
		}
		if err := writeClaudeSSE(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": blockIndex,
		}); err != nil {
			return err
		}
		textBlockOpen = false
		blockIndex++
		return nil
	}

	startTextBlock := func() error {
		if textBlockOpen {
			return nil
		}
		if err := writeClaudeSSE(w, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         blockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
		textBlockOpen = true
		return nil
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if errorValue, ok := chunk["error"]; ok {
			claudeBody, _, convErr := openAIErrorValueToClaude(errorValue, resolvedModel)
			if convErr == nil && len(claudeBody) > 0 {
				var payload map[string]any
				_ = json.Unmarshal(claudeBody, &payload)
				_ = writeClaudeSSE(w, "error", payload)
			}
			errText := extractResponseErrorMessage(claudeBody)
			if errText == "" {
				errText, _ = errorMessageFromValue(errorValue, "upstream stream error")
			}
			return usage, fmt.Errorf("%s", errText)
		}

		if value, ok := chunk["model"].(string); ok && strings.TrimSpace(value) != "" {
			resolvedModel = value
		}
		if chunkUsage := ParseOpenAIUsage([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 {
			usage = chunkUsage
		}

		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		text := stringValue(delta["content"])
		if reason := stringValue(choice["finish_reason"]); reason != "" {
			stopReason = mapOpenAIFinishReasonToClaude(reason)
		}

		if !messageStarted {
			if err := writeClaudeSSE(w, "message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":            messageID,
					"type":          "message",
					"role":          "assistant",
					"model":         firstNonEmpty(resolvedModel, model),
					"content":       []any{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
				},
			}); err != nil {
				return usage, err
			}
			messageStarted = true
		}

		if rawToolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, item := range rawToolCalls {
				call, ok := item.(map[string]any)
				if !ok {
					continue
				}
				callIndex := int(int64FromAny(call["index"]))
				functionValue, _ := call["function"].(map[string]any)
				if !toolBlocks[callIndex] {
					if err := closeTextBlock(); err != nil {
						return usage, err
					}
					toolBlockIndexByCall[callIndex] = blockIndex
					toolBlock := map[string]any{
						"type":  "tool_use",
						"id":    stringValue(call["id"]),
						"name":  stringValue(functionValue["name"]),
						"input": map[string]any{},
					}
					if err := writeClaudeSSE(w, "content_block_start", map[string]any{
						"type":          "content_block_start",
						"index":         blockIndex,
						"content_block": toolBlock,
					}); err != nil {
						return usage, err
					}
					toolBlocks[callIndex] = true
					blockIndex++
				}
				if partial := stringValue(functionValue["arguments"]); partial != "" {
					claudeIndex := toolBlockIndexByCall[callIndex]
					if err := writeClaudeSSE(w, "content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": claudeIndex,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
					}); err != nil {
						return usage, err
					}
				}
			}
			continue
		}

		if text != "" {
			if err := startTextBlock(); err != nil {
				return usage, err
			}
			if err := writeClaudeSSE(w, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]any{"type": "text_delta", "text": text},
			}); err != nil {
				return usage, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}

	if !messageStarted {
		return usage, fmt.Errorf("openai stream ended without any chunks")
	}
	if err := closeTextBlock(); err != nil {
		return usage, err
	}
	for callIndex, started := range toolBlocks {
		if !started {
			continue
		}
		if err := writeClaudeSSE(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": toolBlockIndexByCall[callIndex],
		}); err != nil {
			return usage, err
		}
	}
	claudeUsage := map[string]any{"output_tokens": usage.OutputTokens}
	if usage.InputTokens > 0 || usage.CacheTokens > 0 {
		// Anthropic clients expect input_tokens to exclude cache hits.
		claudeUsage["input_tokens"] = claudeExclusiveInputTokens(usage, 0)
	}
	if usage.CacheTokens > 0 {
		claudeUsage["cache_read_input_tokens"] = usage.CacheTokens
	}
	if err := writeClaudeSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": claudeUsage,
	}); err != nil {
		return usage, err
	}
	if err := writeClaudeSSE(w, "message_stop", map[string]any{"type": "message_stop"}); err != nil {
		return usage, err
	}
	return usage, nil
}
