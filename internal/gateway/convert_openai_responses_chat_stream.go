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

func writeResponsesSSEEvent(w http.ResponseWriter, event string, payload any) error {
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

// streamOpenAIChatToResponsesEvents reads an OpenAI Chat SSE stream and writes
// an OpenAI Responses API SSE stream.
func streamOpenAIChatToResponsesEvents(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	resolvedModel := strings.TrimSpace(model)
	usage := TokenUsage{}
	textStarted := false

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
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
			respBody, _, convErr := chatErrorValueToResponses(errorValue, resolvedModel)
			if convErr == nil && len(respBody) > 0 {
				var errPayload map[string]any
				_ = json.Unmarshal(respBody, &errPayload)
				_ = writeResponsesSSEEvent(w, "error", errPayload)
			}
			errText := extractResponseErrorMessage(respBody)
			if errText == "" {
				errText = "upstream stream error"
			}
			return usage, fmt.Errorf("%s", errText)
		}

		if value, ok := chunk["model"].(string); ok && strings.TrimSpace(value) != "" {
			resolvedModel = value
		}
		if chunkUsage := ParseOpenAIUsage([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CacheTokens > 0 {
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
		if text == "" {
			continue
		}
		if !textStarted {
			if err := writeResponsesSSEEvent(w, "response.created", map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id":     responseID,
					"object": "response",
					"model":  firstNonEmpty(resolvedModel, model),
					"status": "in_progress",
				},
			}); err != nil {
				return usage, err
			}
			textStarted = true
		}
		if err := writeResponsesSSEEvent(w, "response.output_text.delta", map[string]any{
			"type":  "response.output_text.delta",
			"delta": text,
		}); err != nil {
			return usage, err
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if !textStarted {
		return usage, fmt.Errorf("openai stream ended without any chunks")
	}
	completed := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     responseID,
			"object": "response",
			"model":  firstNonEmpty(resolvedModel, model),
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":  usage.InputTokens,
				"output_tokens": usage.OutputTokens,
				"total_tokens":  usage.InputTokens + usage.OutputTokens,
			},
		},
	}
	if err := writeResponsesSSEEvent(w, "response.completed", completed); err != nil {
		return usage, err
	}
	return usage, nil
}

// streamResponsesToOpenAIChatEvents reads an OpenAI Responses SSE stream and
// writes an OpenAI Chat Completions SSE stream.
func streamResponsesToOpenAIChatEvents(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
	chunkID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	resolvedModel := strings.TrimSpace(model)
	usage := TokenUsage{}
	roleSent := false

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		var eventName string
		var payload string
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		} else {
			continue
		}
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if errorValue, ok := chunk["error"]; ok {
			openAIBody, _, convErr := responsesErrorValueToOpenAI(errorValue, resolvedModel)
			if convErr == nil && len(openAIBody) > 0 {
				_, _ = w.Write(openAIBody)
			}
			errText := extractResponseErrorMessage(openAIBody)
			if errText == "" {
				errText = "upstream stream error"
			}
			return usage, fmt.Errorf("%s", errText)
		}

		if chunkUsage := ParseResponsesUsage([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CacheTokens > 0 {
			usage = chunkUsage
		}
		if response, ok := chunk["response"].(map[string]any); ok {
			if value, ok := response["model"].(string); ok && strings.TrimSpace(value) != "" {
				resolvedModel = value
			}
			if chunkUsage := ParseResponsesUsage([]byte(payload)); chunkUsage.InputTokens == 0 && chunkUsage.OutputTokens == 0 {
				if usageMap, ok := response["usage"].(map[string]any); ok {
					usage.InputTokens = int64FromAny(usageMap["input_tokens"])
					usage.OutputTokens = int64FromAny(usageMap["output_tokens"])
				}
			}
		}

		text := ""
		switch eventName {
		case "response.output_text.delta":
			text = stringValue(chunk["delta"])
		default:
			text = stringValue(chunk["delta"])
			if text == "" {
				text = stringValue(chunk["text"])
			}
		}
		if text == "" {
			continue
		}

		delta := map[string]any{"content": text}
		if !roleSent {
			delta["role"] = "assistant"
			roleSent = true
		}
		if err := writeOpenAISSEChunk(w, map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   firstNonEmpty(resolvedModel, model),
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		}); err != nil {
			return usage, err
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if !roleSent {
		return usage, fmt.Errorf("responses stream ended without any text deltas")
	}
	finalChunk := map[string]any{
		"id":      chunkID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(resolvedModel, model),
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalChunk["usage"] = map[string]any{
			"prompt_tokens":     usage.InputTokens,
			"completion_tokens": usage.OutputTokens,
			"total_tokens":      usage.InputTokens + usage.OutputTokens,
		}
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
