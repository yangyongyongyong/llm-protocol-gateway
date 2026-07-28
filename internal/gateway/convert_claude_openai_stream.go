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

func writeOpenAISSEChunk(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// streamClaudeToOpenAIChatEvents reads an Anthropic Messages API SSE stream and
// writes an OpenAI Chat Completions SSE stream to w.
//
// If the upstream turn completes (end_turn) with no visible text/tool_use —
// only thinking / redacted_thinking — this returns errThinkingOnlyEmptyResponse
// without writing any client bytes so finishConvertedProxy can retry.
func streamClaudeToOpenAIChatEvents(w http.ResponseWriter, reader io.Reader, model string, clientToolNames map[string]struct{}) (TokenUsage, error) {
	chunkID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	resolvedModel := strings.TrimSpace(model)
	usage := TokenUsage{}
	claudeStopReason := ""
	sentRole := false
	openAIToolIndex := -1
	currentToolOpenAIIndex := -1
	sawVisibleOutput := false
	sawAnyEvent := false

	emitToolCallDelta := func(toolIndex int, id string, name string, arguments string) error {
		if id == "" && name == "" && arguments == "" {
			return nil
		}
		sawVisibleOutput = true
		toolCall := map[string]any{"index": toolIndex}
		if id != "" {
			toolCall["id"] = id
		}
		if name != "" || arguments != "" {
			functionValue := map[string]any{}
			if name != "" {
				functionValue["name"] = name
			}
			if arguments != "" {
				functionValue["arguments"] = arguments
			}
			toolCall["type"] = "function"
			toolCall["function"] = functionValue
		}
		deltaPayload := map[string]any{"tool_calls": []any{toolCall}}
		if !sentRole {
			deltaPayload["role"] = "assistant"
			sentRole = true
		}
		chunk := map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   firstNonEmpty(resolvedModel, model),
			"choices": []map[string]any{{
				"index": 0,
				"delta": deltaPayload,
			}},
		}
		return writeOpenAISSEChunk(w, chunk)
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
		if payload == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		sawAnyEvent = true
		if errorValue, ok := event["error"]; ok {
			body, _, convErr := claudeErrorValueToOpenAI(errorValue, resolvedModel)
			if convErr == nil && len(body) > 0 {
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				_ = writeOpenAISSEChunk(w, payload)
			}
			errText := extractResponseErrorMessage(body)
			if errText == "" {
				errText, _ = errorMessageFromValue(errorValue, "upstream stream error")
			}
			return usage, fmt.Errorf("%s", errText)
		}

		eventType := stringValue(event["type"])
		switch eventType {
		case "message_start":
			if messageValue, ok := event["message"].(map[string]any); ok {
				if value := stringValue(messageValue["model"]); value != "" {
					resolvedModel = value
				}
			}
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			blockType := stringValue(block["type"])
			switch blockType {
			case "tool_use", "server_tool_use", "mcp_tool_use":
				openAIToolIndex++
				currentToolOpenAIIndex = openAIToolIndex
				if err := emitToolCallDelta(currentToolOpenAIIndex, stringValue(block["id"]), resolveOpenAIToolNameFromClaude(stringValue(block["name"]), clientToolNames), ""); err != nil {
					return usage, err
				}
			case "thinking", "redacted_thinking":
				// Ignored for Chat clients; tracked only for thinking-only detection.
			}
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			switch stringValue(delta["type"]) {
			case "input_json_delta":
				if currentToolOpenAIIndex < 0 {
					continue
				}
				// NOTE: do NOT TrimSpace here. Tool-call arguments are streamed as
				// partial_json fragments and a fragment boundary can fall on a
				// space (e.g. "git status --short" split into "git status" + " --short").
				// Trimming each fragment drops those boundary spaces and corrupts
				// the reassembled arguments (producing "git status--short").
				partialJSON := stringValue(delta["partial_json"])
				if partialJSON == "" {
					continue
				}
				if err := emitToolCallDelta(currentToolOpenAIIndex, "", "", partialJSON); err != nil {
					return usage, err
				}
				continue
			case "thinking_delta", "signature_delta":
				continue
			}
			text := stringValue(delta["text"])
			if text == "" {
				continue
			}
			sawVisibleOutput = true
			deltaPayload := map[string]any{"content": text}
			if !sentRole {
				deltaPayload["role"] = "assistant"
				sentRole = true
			}
			chunk := map[string]any{
				"id":      chunkID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   firstNonEmpty(resolvedModel, model),
				"choices": []map[string]any{{
					"index": 0,
					"delta": deltaPayload,
				}},
			}
			if err := writeOpenAISSEChunk(w, chunk); err != nil {
				return usage, err
			}
		case "content_block_stop":
			currentToolOpenAIIndex = -1
		case "message_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if reason := strings.TrimSpace(stringValue(delta["stop_reason"])); reason != "" {
					claudeStopReason = reason
				}
			}
			if usageValue, ok := event["usage"].(map[string]any); ok {
				usage = parseClaudeUsageMap(usageValue)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}

	if !sawAnyEvent {
		return usage, fmt.Errorf("openai stream ended without any events")
	}

	// Completed turn with zero visible output (thinking-only): retryable, do not
	// flush a silent stop chunk to Cursor (looks like a dead sub-agent).
	switch claudeStopReason {
	case "", "end_turn":
		if !sawVisibleOutput {
			return usage, errThinkingOnlyEmptyResponse
		}
	case "max_tokens", "tool_use", "refusal", "pause_turn":
		// Not the silent-completed-empty bug.
	default:
		if !sawVisibleOutput {
			return usage, errThinkingOnlyEmptyResponse
		}
	}

	stopReason := mapClaudeStopReasonToOpenAI(claudeStopReason)
	if stopReason == "" {
		stopReason = "stop"
	}
	finalChunk := map[string]any{
		"id":      chunkID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(resolvedModel, model),
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": stopReason,
		}},
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheTokens > 0 {
		finalChunk["usage"] = claudeUsageToOpenAIUsage(usage, map[string]any{
			"cache_read_input_tokens": usage.CacheTokens,
		})
	}
	if err := writeOpenAISSEChunk(w, finalChunk); err != nil {
		return usage, err
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return usage, err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return usage, nil
}
