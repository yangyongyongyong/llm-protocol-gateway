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

// Chat→Responses stream conversion is SHARED by:
//   - Cursor OAuth → Responses (proxyOpenAIChatToResponses)
//   - Claude OAuth → Responses (proxyClaudeToResponses second hop)
// Provider-specific quirks MUST go through ChatToResponsesStreamOptions,
// not hardcoded behavior. Changing this file requires both path regressions.

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

// ChatToResponsesStreamOptions isolates provider-specific Chat→Responses quirks.
// Default/standard options must stay safe for Claude→Responses; Cursor opts are
// injected only on the Cursor OAuth pipeline.
type ChatToResponsesStreamOptions struct {
	// SanitizeCallID normalizes tool call IDs from upstream Chat SSE.
	// Cursor bridge may emit "call_xxx\nfc_yyy"; standard OpenAI uses a single id.
	SanitizeCallID func(string) string
	// AllowReasoningFallback promotes delta.reasoning_content into output_text
	// when content is empty. Cursor bridge often streams reasoning-only chunks;
	// Claude→Chat and standard OpenAI Chat should not rely on this.
	AllowReasoningFallback bool
}

// StandardChatToResponsesStreamOptions is the shared default for non-Cursor paths
// (including Claude OAuth → Responses).
func StandardChatToResponsesStreamOptions() ChatToResponsesStreamOptions {
	return ChatToResponsesStreamOptions{
		SanitizeCallID:         trimCallID,
		AllowReasoningFallback: false,
	}
}

// CursorChatToResponsesStreamOptions enables Cursor-bridge quirks for the
// Cursor OAuth → Responses pipeline only.
func CursorChatToResponsesStreamOptions() ChatToResponsesStreamOptions {
	return ChatToResponsesStreamOptions{
		SanitizeCallID:         sanitizeResponsesCallID,
		AllowReasoningFallback: true,
	}
}

func trimCallID(raw string) string {
	return strings.TrimSpace(raw)
}

type chatToResponsesStreamState struct {
	w       http.ResponseWriter
	opts    ChatToResponsesStreamOptions
	responseID string
	model      string
	seq        int
	usage      TokenUsage
	responseCreated bool

	messageItemID string
	messageOpened bool
	messageClosed bool
	outputIndex   int
	fullText      strings.Builder
	reasoningText strings.Builder

	toolCalls map[int]*streamingToolCallState
}

type streamingToolCallState struct {
	itemID           string
	callID           string
	name             string
	arguments        strings.Builder
	writtenArguments int
	outputIndex      int
	opened           bool
	done             bool
}

func (s *chatToResponsesStreamState) nextSeq() int {
	s.seq++
	return s.seq
}

func (s *chatToResponsesStreamState) sanitizeCallID(raw string) string {
	if s.opts.SanitizeCallID != nil {
		return s.opts.SanitizeCallID(raw)
	}
	return trimCallID(raw)
}

func (s *chatToResponsesStreamState) ensureResponseCreated() error {
	if s.responseCreated {
		return nil
	}
	s.responseCreated = true
	response := map[string]any{
		"id":     s.responseID,
		"object": "response",
		"model":  s.model,
		"status": "in_progress",
		"output": []any{},
	}
	if err := writeResponsesSSEEvent(s.w, "response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": s.nextSeq(),
		"response":        response,
	}); err != nil {
		return err
	}
	// Codex / OpenAI clients expect in_progress after created before any output items.
	return writeResponsesSSEEvent(s.w, "response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"sequence_number": s.nextSeq(),
		"response":        response,
	})
}

func (s *chatToResponsesStreamState) openMessageItem() error {
	if s.messageOpened {
		return nil
	}
	if err := s.ensureResponseCreated(); err != nil {
		return err
	}
	if s.messageItemID == "" {
		s.messageItemID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	// content must be present (even empty): Codex drops the item if Message
	// deserialization fails, then logs "OutputTextDelta without active item".
	if err := writeResponsesSSEEvent(s.w, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": s.nextSeq(),
		"output_index":    s.outputIndex,
		"item": map[string]any{
			"id":      s.messageItemID,
			"type":    "message",
			"role":    "assistant",
			"status":  "in_progress",
			"content": []any{},
		},
	}); err != nil {
		return err
	}
	if err := writeResponsesSSEEvent(s.w, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": s.nextSeq(),
		"item_id":         s.messageItemID,
		"output_index":    s.outputIndex,
		"content_index":   0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	}); err != nil {
		return err
	}
	s.messageOpened = true
	return nil
}

func (s *chatToResponsesStreamState) appendTextDelta(text string) error {
	if text == "" {
		return nil
	}
	if err := s.openMessageItem(); err != nil {
		return err
	}
	s.fullText.WriteString(text)
	return writeResponsesSSEEvent(s.w, "response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": s.nextSeq(),
		"item_id":         s.messageItemID,
		"output_index":    s.outputIndex,
		"content_index":   0,
		"delta":           text,
	})
}

func (s *chatToResponsesStreamState) closeMessageItem() error {
	if !s.messageOpened || s.messageClosed {
		return nil
	}
	text := s.fullText.String()
	if err := writeResponsesSSEEvent(s.w, "response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": s.nextSeq(),
		"item_id":         s.messageItemID,
		"output_index":    s.outputIndex,
		"content_index":   0,
		"text":            text,
	}); err != nil {
		return err
	}
	if err := writeResponsesSSEEvent(s.w, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": s.nextSeq(),
		"item_id":         s.messageItemID,
		"output_index":    s.outputIndex,
		"content_index":   0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
		},
	}); err != nil {
		return err
	}
	if err := writeResponsesSSEEvent(s.w, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": s.nextSeq(),
		"output_index":    s.outputIndex,
		"item": map[string]any{
			"id":     s.messageItemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		},
	}); err != nil {
		return err
	}
	s.messageClosed = true
	s.outputIndex++
	return nil
}

func (s *chatToResponsesStreamState) toolCallForIndex(index int) *streamingToolCallState {
	if s.toolCalls == nil {
		s.toolCalls = make(map[int]*streamingToolCallState)
	}
	call, ok := s.toolCalls[index]
	if !ok {
		call = &streamingToolCallState{}
		s.toolCalls[index] = call
	}
	return call
}

// sanitizeResponsesCallID strips Cursor's dual-id form (`call_xxx\nfc_yyy`)
// down to a single OpenAI-compatible call_id.
func sanitizeResponsesCallID(raw string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(raw), func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	for _, part := range cleaned {
		if strings.HasPrefix(part, "call_") {
			return part
		}
	}
	if len(cleaned) > 0 {
		return cleaned[0]
	}
	return strings.TrimSpace(raw)
}

func (s *chatToResponsesStreamState) handleToolCallDelta(rawToolCalls []any) error {
	for _, item := range rawToolCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index := int(int64FromAny(call["index"]))
		state := s.toolCallForIndex(index)
		if id := s.sanitizeCallID(stringValue(call["id"])); id != "" {
			state.callID = id
		}
		if functionValue, ok := call["function"].(map[string]any); ok {
			if name := stringValue(functionValue["name"]); name != "" {
				state.name = name
			}
			if args := stringValue(functionValue["arguments"]); args != "" {
				state.arguments.WriteString(args)
			}
		}
		if state.callID == "" || state.name == "" {
			continue
		}
		if err := s.ensureResponseCreated(); err != nil {
			return err
		}
		if !state.opened {
			if s.messageOpened && !s.messageClosed {
				if err := s.closeMessageItem(); err != nil {
					return err
				}
			}
			state.outputIndex = s.outputIndex
			state.itemID = fmt.Sprintf("fc_%d", time.Now().UnixNano())
			if err := writeResponsesSSEEvent(s.w, "response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": s.nextSeq(),
				"output_index":    state.outputIndex,
				"item": map[string]any{
					"id":      state.itemID,
					"type":    "function_call",
					"call_id": state.callID,
					"name":    state.name,
					"status":  "in_progress",
				},
			}); err != nil {
				return err
			}
			state.opened = true
			s.outputIndex++
		}
		args := state.arguments.String()
		if len(args) > state.writtenArguments {
			if err := writeResponsesSSEEvent(s.w, "response.function_call_arguments.delta", map[string]any{
				"type":            "response.function_call_arguments.delta",
				"sequence_number": s.nextSeq(),
				"item_id":         state.itemID,
				"output_index":    state.outputIndex,
				"delta":           args[state.writtenArguments:],
			}); err != nil {
				return err
			}
			state.writtenArguments = len(args)
		}
	}
	return nil
}

func (s *chatToResponsesStreamState) finalizeToolCalls() error {
	for _, state := range s.toolCalls {
		if !state.opened || state.done {
			continue
		}
		args := state.arguments.String()
		if err := writeResponsesSSEEvent(s.w, "response.function_call_arguments.done", map[string]any{
			"type":            "response.function_call_arguments.done",
			"sequence_number": s.nextSeq(),
			"item_id":         state.itemID,
			"output_index":    state.outputIndex,
			"name":            state.name,
			"arguments":       args,
		}); err != nil {
			return err
		}
		if err := writeResponsesSSEEvent(s.w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": s.nextSeq(),
			"output_index":    state.outputIndex,
			"item": map[string]any{
				"id":        state.itemID,
				"type":      "function_call",
				"call_id":   state.callID,
				"name":      state.name,
				"arguments": args,
				"status":    "completed",
			},
		}); err != nil {
			return err
		}
		state.done = true
	}
	return nil
}

func (s *chatToResponsesStreamState) buildCompletedOutput() []map[string]any {
	output := make([]map[string]any, 0, 1+len(s.toolCalls))
	if s.messageOpened {
		text := s.fullText.String()
		output = append(output, map[string]any{
			"id":     s.messageItemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	for _, call := range s.toolCalls {
		if !call.opened {
			continue
		}
		output = append(output, map[string]any{
			"id":        call.itemID,
			"type":      "function_call",
			"call_id":   call.callID,
			"name":      call.name,
			"arguments": call.arguments.String(),
			"status":    "completed",
		})
	}
	return output
}

func (s *chatToResponsesStreamState) finalize() error {
	if s.opts.AllowReasoningFallback && s.fullText.Len() == 0 && s.reasoningText.Len() > 0 {
		if err := s.appendTextDelta(s.reasoningText.String()); err != nil {
			return err
		}
	}
	if s.messageOpened && !s.messageClosed {
		if err := s.closeMessageItem(); err != nil {
			return err
		}
	}
	if err := s.finalizeToolCalls(); err != nil {
		return err
	}
	if !s.responseCreated {
		return fmt.Errorf("openai stream ended without any chunks")
	}
	output := s.buildCompletedOutput()
	text := s.fullText.String()
	completed := map[string]any{
		"type":            "response.completed",
		"sequence_number": s.nextSeq(),
		"response": map[string]any{
			"id":          s.responseID,
			"object":      "response",
			"model":       s.model,
			"status":      "completed",
			"output_text": text,
			"output":      output,
			"usage": map[string]any{
				"input_tokens":  s.usage.InputTokens,
				"output_tokens": s.usage.OutputTokens,
				"total_tokens":  s.usage.InputTokens + s.usage.OutputTokens,
			},
		},
	}
	return writeResponsesSSEEvent(s.w, "response.completed", completed)
}

func (s *chatToResponsesStreamState) hasOutput() bool {
	if s.fullText.Len() > 0 || s.messageOpened {
		return true
	}
	if s.opts.AllowReasoningFallback && s.reasoningText.Len() > 0 {
		return true
	}
	for _, call := range s.toolCalls {
		if call.opened {
			return true
		}
	}
	return false
}

// streamOpenAIChatToResponsesEvents reads an OpenAI Chat SSE stream and writes
// an OpenAI Responses API SSE stream using standard (non-Cursor) options.
func streamOpenAIChatToResponsesEvents(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
	return streamOpenAIChatToResponsesEventsWithOptions(w, reader, model, StandardChatToResponsesStreamOptions())
}

// streamOpenAIChatToResponsesEventsWithOptions is the shared Chat→Responses
// stream engine. Pass CursorChatToResponsesStreamOptions only on the Cursor path.
func streamOpenAIChatToResponsesEventsWithOptions(w http.ResponseWriter, reader io.Reader, model string, opts ChatToResponsesStreamOptions) (TokenUsage, error) {
	if opts.SanitizeCallID == nil {
		opts.SanitizeCallID = trimCallID
	}
	state := &chatToResponsesStreamState{
		w:          w,
		opts:       opts,
		responseID: fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		model:      strings.TrimSpace(model),
	}

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
			respBody, _, convErr := chatErrorValueToResponses(errorValue, state.model)
			if convErr == nil && len(respBody) > 0 {
				var errPayload map[string]any
				_ = json.Unmarshal(respBody, &errPayload)
				_ = writeResponsesSSEEvent(w, "error", errPayload)
			}
			errText := extractResponseErrorMessage(respBody)
			if errText == "" {
				errText = "upstream stream error"
			}
			return state.usage, fmt.Errorf("%s", errText)
		}

		if value, ok := chunk["model"].(string); ok && strings.TrimSpace(value) != "" {
			state.model = value
		}
		if chunkUsage := ParseOpenAIUsage([]byte(payload)); chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CacheTokens > 0 {
			state.usage = chunkUsage
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
		if rawToolCalls, ok := delta["tool_calls"].([]any); ok && len(rawToolCalls) > 0 {
			if err := state.handleToolCallDelta(rawToolCalls); err != nil {
				return state.usage, err
			}
		}
		if text := stringValue(delta["content"]); text != "" {
			if err := state.appendTextDelta(text); err != nil {
				return state.usage, err
			}
		}
		if reasoning := stringValue(delta["reasoning_content"]); reasoning != "" {
			state.reasoningText.WriteString(reasoning)
		}
	}
	if err := scanner.Err(); err != nil {
		return state.usage, err
	}
	if !state.hasOutput() {
		return state.usage, fmt.Errorf("openai stream ended without any chunks")
	}
	if err := state.finalize(); err != nil {
		return state.usage, err
	}
	return state.usage, nil
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
	eventName := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

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
		case "response.output_text.done":
			text = stringValue(chunk["text"])
		case "response.completed":
			if response, ok := chunk["response"].(map[string]any); ok {
				text = stringValue(response["output_text"])
				if text == "" {
					text = responsesOutputText(response["output"])
				}
			}
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
