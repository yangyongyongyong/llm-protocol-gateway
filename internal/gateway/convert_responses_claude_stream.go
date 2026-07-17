package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Direct Claude Messages SSE → OpenAI Responses SSE converter.
// Ported (adapted) from cc-switch streaming_codex_anthropic.rs (MIT).

type claudeDirectBlockKind int

const (
	claudeDirectBlockText claudeDirectBlockKind = iota
	claudeDirectBlockTool
	claudeDirectBlockThinking
)

type claudeDirectBlockState struct {
	kind              claudeDirectBlockKind
	outputIndex       int
	itemID            string
	callID            string
	name              string
	accum             strings.Builder
	startInput        string
	sourceBlock       map[string]any
	hasVisibleSummary bool
	done              bool
	writtenArgs       int
	messageOpened     bool
}

// visibilityGatedSSEWriter buffers written SSE bytes until markVisible is
// called, then flushes the buffer and passes all further writes straight
// through. It lets streamClaudeToResponsesEventsDirect hold back
// response.created / reasoning events until it knows the turn will actually
// produce visible output (text or tool_use): if the turn instead ends up
// thinking-only (see isThinkingOnlyEmptyOutput), nothing was ever sent to the
// real client, so the caller can safely retry against the same provider — the
// same "retryable" contract finishConvertedProxy already uses for empty
// upstream streams (dw.WroteBody() == false).
type visibilityGatedSSEWriter struct {
	base    http.ResponseWriter
	buf     bytes.Buffer
	visible bool
}

func newVisibilityGatedSSEWriter(base http.ResponseWriter) *visibilityGatedSSEWriter {
	return &visibilityGatedSSEWriter{base: base}
}

func (v *visibilityGatedSSEWriter) Header() http.Header { return v.base.Header() }

// WriteHeader is a no-op: the caller (finishConvertedProxy) already committed
// status on the outer deferred writer before streaming began.
func (v *visibilityGatedSSEWriter) WriteHeader(int) {}

func (v *visibilityGatedSSEWriter) Write(p []byte) (int, error) {
	if v.visible {
		return v.base.Write(p)
	}
	return v.buf.Write(p)
}

func (v *visibilityGatedSSEWriter) Flush() {
	if !v.visible {
		return
	}
	if f, ok := v.base.(http.Flusher); ok {
		f.Flush()
	}
}

// markVisible flushes any buffered bytes through to base and switches to
// direct passthrough for all subsequent writes. Idempotent.
func (v *visibilityGatedSSEWriter) markVisible() {
	if v.visible {
		return
	}
	v.visible = true
	if v.buf.Len() > 0 {
		_, _ = v.base.Write(v.buf.Bytes())
		v.buf.Reset()
	}
	if f, ok := v.base.(http.Flusher); ok {
		f.Flush()
	}
}

// everVisible reports whether any visible (non-thinking) output block was
// ever seen, i.e. whether anything was actually flushed to the real client.
func (v *visibilityGatedSSEWriter) everVisible() bool { return v.visible }

type claudeToResponsesDirectStreamState struct {
	w               http.ResponseWriter
	visGate         *visibilityGatedSSEWriter
	model           string
	responseID      string
	clientToolNames map[string]struct{}

	seq             int
	outputIndex     int
	responseCreated bool
	completed       bool
	stopReason      string
	usage           TokenUsage

	blocks      map[int]*claudeDirectBlockState
	outputItems []map[string]any
}

func newClaudeToResponsesDirectStreamState(w http.ResponseWriter, model string, clientToolNames map[string]struct{}) *claudeToResponsesDirectStreamState {
	gate := newVisibilityGatedSSEWriter(w)
	return &claudeToResponsesDirectStreamState{
		w:               gate,
		visGate:         gate,
		model:           model,
		responseID:      fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		clientToolNames: clientToolNames,
		blocks:          make(map[int]*claudeDirectBlockState),
	}
}

func (s *claudeToResponsesDirectStreamState) nextSeq() int {
	s.seq++
	return s.seq
}

func (s *claudeToResponsesDirectStreamState) nextOutputIndex() int {
	idx := s.outputIndex
	s.outputIndex++
	return idx
}

func (s *claudeToResponsesDirectStreamState) ensureResponseCreated() error {
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
	return writeResponsesSSEEvent(s.w, "response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"sequence_number": s.nextSeq(),
		"response":        response,
	})
}

func (s *claudeToResponsesDirectStreamState) handleMessageStart(event map[string]any) error {
	if message, ok := event["message"].(map[string]any); ok {
		if id := stringValue(message["id"]); id != "" {
			if strings.HasPrefix(id, "resp_") {
				s.responseID = id
			} else {
				s.responseID = "resp_" + id
			}
		}
		if model := stringValue(message["model"]); model != "" {
			s.model = model
		}
		if usageValue, ok := message["usage"].(map[string]any); ok {
			s.usage = parseClaudeUsageMap(usageValue)
		}
	}
	return s.ensureResponseCreated()
}

func (s *claudeToResponsesDirectStreamState) handleContentBlockStart(event map[string]any) error {
	if err := s.ensureResponseCreated(); err != nil {
		return err
	}
	index := int(int64FromAny(event["index"]))
	block, _ := event["content_block"].(map[string]any)
	blockType := stringValue(block["type"])

	switch blockType {
	case "text":
		s.visGate.markVisible()
		outputIndex := s.nextOutputIndex()
		itemID := fmt.Sprintf("%s_msg_%d", s.responseID, outputIndex)
		if err := writeResponsesSSEEvent(s.w, "response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": s.nextSeq(),
			"output_index":    outputIndex,
			"item": map[string]any{
				"id":      itemID,
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
			"item_id":         itemID,
			"output_index":    outputIndex,
			"content_index":   0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		}); err != nil {
			return err
		}
		state := &claudeDirectBlockState{
			kind:          claudeDirectBlockText,
			outputIndex:   outputIndex,
			itemID:        itemID,
			messageOpened: true,
			sourceBlock:   cloneAnyMap(block),
		}
		if text := stringValue(block["text"]); text != "" {
			state.accum.WriteString(text)
		}
		s.blocks[index] = state

	case "tool_use":
		s.visGate.markVisible()
		outputIndex := s.nextOutputIndex()
		callID := sanitizeResponsesCallID(stringValue(block["id"]))
		name := sanitizeResponsesToolName(stringValue(block["name"]), s.clientToolNames)
		itemID := fmt.Sprintf("fc_%s_%d", s.responseID, outputIndex)
		startInput := ""
		if input := block["input"]; input != nil {
			if m, ok := input.(map[string]any); ok && len(m) > 0 {
				if raw, err := json.Marshal(input); err == nil {
					startInput = string(raw)
				}
			}
		}
		if err := writeResponsesSSEEvent(s.w, "response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": s.nextSeq(),
			"output_index":    outputIndex,
			"item": map[string]any{
				"id":      itemID,
				"type":    "function_call",
				"call_id": callID,
				"name":    name,
				"status":  "in_progress",
			},
		}); err != nil {
			return err
		}
		s.blocks[index] = &claudeDirectBlockState{
			kind:        claudeDirectBlockTool,
			outputIndex: outputIndex,
			itemID:      itemID,
			callID:      callID,
			name:        name,
			startInput:  startInput,
			sourceBlock: cloneAnyMap(block),
		}

	case "thinking", "redacted_thinking":
		outputIndex := s.nextOutputIndex()
		itemID := fmt.Sprintf("rs_%s_%d", s.responseID, outputIndex)
		if err := writeResponsesSSEEvent(s.w, "response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": s.nextSeq(),
			"output_index":    outputIndex,
			"item": map[string]any{
				"id":                itemID,
				"type":              "reasoning",
				"summary":           []any{},
				"encrypted_content": nil,
			},
		}); err != nil {
			return err
		}
		hasVisible := blockType == "thinking"
		if hasVisible {
			if err := writeResponsesSSEEvent(s.w, "response.reasoning_summary_part.added", map[string]any{
				"type":            "response.reasoning_summary_part.added",
				"sequence_number": s.nextSeq(),
				"item_id":         itemID,
				"output_index":    outputIndex,
				"summary_index":   0,
				"part":            map[string]any{"type": "summary_text", "text": ""},
			}); err != nil {
				return err
			}
		}
		state := &claudeDirectBlockState{
			kind:              claudeDirectBlockThinking,
			outputIndex:       outputIndex,
			itemID:            itemID,
			hasVisibleSummary: hasVisible,
			sourceBlock:       cloneAnyMap(block),
		}
		if text := stringValue(block["thinking"]); text != "" {
			state.accum.WriteString(text)
		}
		s.blocks[index] = state
	}
	return nil
}

func (s *claudeToResponsesDirectStreamState) handleContentBlockDelta(event map[string]any) error {
	index := int(int64FromAny(event["index"]))
	block := s.blocks[index]
	if block == nil || block.done {
		return nil
	}
	delta, _ := event["delta"].(map[string]any)
	switch stringValue(delta["type"]) {
	case "text_delta":
		text := stringValue(delta["text"])
		if text == "" {
			return nil
		}
		block.accum.WriteString(text)
		return writeResponsesSSEEvent(s.w, "response.output_text.delta", map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"content_index":   0,
			"delta":           text,
		})
	case "input_json_delta":
		partial := stringValue(delta["partial_json"])
		if partial == "" {
			return nil
		}
		block.accum.WriteString(partial)
		if err := writeResponsesSSEEvent(s.w, "response.function_call_arguments.delta", map[string]any{
			"type":            "response.function_call_arguments.delta",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"delta":           partial,
		}); err != nil {
			return err
		}
		block.writtenArgs = block.accum.Len()
		return nil
	case "thinking_delta":
		text := stringValue(delta["thinking"])
		if text == "" {
			return nil
		}
		block.accum.WriteString(text)
		if block.sourceBlock != nil {
			block.sourceBlock["thinking"] = block.accum.String()
		}
		if !block.hasVisibleSummary {
			return nil
		}
		return writeResponsesSSEEvent(s.w, "response.reasoning_summary_text.delta", map[string]any{
			"type":            "response.reasoning_summary_text.delta",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"summary_index":   0,
			"delta":           text,
		})
	case "signature_delta":
		if sig := stringValue(delta["signature"]); sig != "" && block.sourceBlock != nil {
			block.sourceBlock["signature"] = sig
		}
	}
	return nil
}

func (s *claudeToResponsesDirectStreamState) closeBlock(index int) error {
	block := s.blocks[index]
	if block == nil || block.done {
		return nil
	}
	block.done = true
	text := block.accum.String()

	switch block.kind {
	case claudeDirectBlockText:
		if err := writeResponsesSSEEvent(s.w, "response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"content_index":   0,
			"text":            text,
		}); err != nil {
			return err
		}
		if err := writeResponsesSSEEvent(s.w, "response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"content_index":   0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			},
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id":     block.itemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		}
		if err := writeResponsesSSEEvent(s.w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": s.nextSeq(),
			"output_index":    block.outputIndex,
			"item":            item,
		}); err != nil {
			return err
		}
		s.outputItems = append(s.outputItems, item)

	case claudeDirectBlockTool:
		args := text
		if strings.TrimSpace(args) == "" {
			args = block.startInput
		}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if err := writeResponsesSSEEvent(s.w, "response.function_call_arguments.done", map[string]any{
			"type":            "response.function_call_arguments.done",
			"sequence_number": s.nextSeq(),
			"item_id":         block.itemID,
			"output_index":    block.outputIndex,
			"name":            block.name,
			"arguments":       args,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id":        block.itemID,
			"type":      "function_call",
			"call_id":   block.callID,
			"name":      block.name,
			"arguments": args,
			"status":    "completed",
		}
		if err := writeResponsesSSEEvent(s.w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": s.nextSeq(),
			"output_index":    block.outputIndex,
			"item":            item,
		}); err != nil {
			return err
		}
		s.outputItems = append(s.outputItems, item)

	case claudeDirectBlockThinking:
		source := block.sourceBlock
		if source == nil {
			source = map[string]any{"type": "thinking"}
		}
		if stringValue(source["type"]) == "thinking" {
			source["thinking"] = text
		}
		item, ok := responsesReasoningItemFromAnthropicBlock(block.itemID, source)
		if !ok {
			return nil
		}
		if block.hasVisibleSummary {
			if err := writeResponsesSSEEvent(s.w, "response.reasoning_summary_text.done", map[string]any{
				"type":            "response.reasoning_summary_text.done",
				"sequence_number": s.nextSeq(),
				"item_id":         block.itemID,
				"output_index":    block.outputIndex,
				"summary_index":   0,
				"text":            text,
			}); err != nil {
				return err
			}
			if err := writeResponsesSSEEvent(s.w, "response.reasoning_summary_part.done", map[string]any{
				"type":            "response.reasoning_summary_part.done",
				"sequence_number": s.nextSeq(),
				"item_id":         block.itemID,
				"output_index":    block.outputIndex,
				"summary_index":   0,
				"part":            map[string]any{"type": "summary_text", "text": text},
			}); err != nil {
				return err
			}
		}
		if err := writeResponsesSSEEvent(s.w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": s.nextSeq(),
			"output_index":    block.outputIndex,
			"item":            item,
		}); err != nil {
			return err
		}
		s.outputItems = append(s.outputItems, item)
	}
	return nil
}

func (s *claudeToResponsesDirectStreamState) handleMessageDelta(event map[string]any) {
	if delta, ok := event["delta"].(map[string]any); ok {
		if reason := stringValue(delta["stop_reason"]); reason != "" {
			s.stopReason = reason
		}
	}
	if usageValue, ok := event["usage"].(map[string]any); ok {
		s.usage = parseClaudeUsageMap(usageValue)
	}
}

// closeRemainingBlocks closes any content blocks that never received an
// explicit content_block_stop (defensive; well-formed Anthropic streams always
// close every block before message_stop). Idempotent: closeBlock is a no-op
// for already-done blocks, so calling this more than once is safe.
func (s *claudeToResponsesDirectStreamState) closeRemainingBlocks() error {
	if err := s.ensureResponseCreated(); err != nil {
		return err
	}
	for index, block := range s.blocks {
		if !block.done {
			if err := s.closeBlock(index); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *claudeToResponsesDirectStreamState) finalize() error {
	if s.completed {
		return nil
	}
	if err := s.closeRemainingBlocks(); err != nil {
		return err
	}
	status, incompleteReason := mapAnthropicStopReasonToStatus(s.stopReason)
	response := map[string]any{
		"id":     s.responseID,
		"object": "response",
		"model":  s.model,
		"status": status,
		"output": s.outputItems,
		"usage": map[string]any{
			"input_tokens":  s.usage.InputTokens,
			"output_tokens": s.usage.OutputTokens,
			"total_tokens":  s.usage.InputTokens + s.usage.OutputTokens,
		},
	}
	if incompleteReason != "" {
		response["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	s.completed = true
	return writeResponsesSSEEvent(s.w, "response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": s.nextSeq(),
		"response":        response,
	})
}

// streamClaudeToResponsesEventsDirect reads Anthropic Messages SSE and writes
// OpenAI Responses SSE directly (no Chat intermediate).
func streamClaudeToResponsesEventsDirect(w http.ResponseWriter, reader io.Reader, model string, clientToolNames map[string]struct{}) (TokenUsage, error) {
	state := newClaudeToResponsesDirectStreamState(w, model, clientToolNames)

	// Only tee (buffer) the raw upstream SSE bytes when the near-empty debug
	// dump is enabled, so this has zero overhead in normal operation.
	var debugTee *bytes.Buffer
	if strings.TrimSpace(os.Getenv("LPG_DEBUG_NEAR_EMPTY_DUMP")) != "" {
		debugTee = &bytes.Buffer{}
		reader = io.TeeReader(reader, debugTee)
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
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if errorValue, ok := event["error"]; ok {
			msg, _ := errorMessageFromValue(errorValue, "upstream stream error")
			_ = state.ensureResponseCreated()
			// Surface upstream errors immediately: flush anything buffered so
			// far and stop gating further writes. This intentionally makes
			// dw.WroteBody() true, so the thinking-only-empty retry path
			// below never fires for a genuine upstream error.
			state.visGate.markVisible()
			_ = writeResponsesSSEEvent(state.w, "response.failed", map[string]any{
				"type":            "response.failed",
				"sequence_number": state.nextSeq(),
				"response": map[string]any{
					"id":     state.responseID,
					"object": "response",
					"model":  state.model,
					"status": "failed",
					"error":  map[string]any{"message": msg},
				},
			})
			state.completed = true
			return state.usage, fmt.Errorf("%s", msg)
		}
		switch stringValue(event["type"]) {
		case "message_start":
			if err := state.handleMessageStart(event); err != nil {
				return state.usage, err
			}
		case "content_block_start":
			if err := state.handleContentBlockStart(event); err != nil {
				return state.usage, err
			}
		case "content_block_delta":
			if err := state.handleContentBlockDelta(event); err != nil {
				return state.usage, err
			}
		case "content_block_stop":
			if err := state.closeBlock(int(int64FromAny(event["index"]))); err != nil {
				return state.usage, err
			}
		case "message_delta":
			state.handleMessageDelta(event)
		case "message_stop":
			// finalize below
		}
	}
	if err := scanner.Err(); err != nil {
		return state.usage, err
	}

	// Detect the thinking-only-empty upstream bug before committing anything:
	// close any still-open blocks, then check whether the turn ended up with
	// zero visible output. Nothing has reached the real client yet as long as
	// no text/tool_use block ever started (visGate stayed buffered), so this
	// is safe to treat as retryable — see errThinkingOnlyEmptyResponse.
	if err := state.closeRemainingBlocks(); err != nil {
		return state.usage, err
	}
	if len(state.outputItems) <= 1 && debugTee != nil {
		status, _ := mapAnthropicStopReasonToStatus(state.stopReason)
		dumpNearEmptyConvertedResponse("stream", model, debugTee.Bytes(), map[string]any{
			"status":        status,
			"stop_reason":   state.stopReason,
			"output_len":    len(state.outputItems),
			"output_types":  outputItemTypes(state.outputItems),
			"ever_visible":  state.visGate.everVisible(),
			"output_tokens": state.usage.OutputTokens,
		})
	}
	if !state.visGate.everVisible() {
		status, _ := mapAnthropicStopReasonToStatus(state.stopReason)
		if isThinkingOnlyEmptyOutput(status, state.outputItems) {
			return state.usage, fmt.Errorf("claude stream completed with no visible output (stop_reason=%q): %w",
				state.stopReason, errThinkingOnlyEmptyResponse)
		}
	}

	if err := state.finalize(); err != nil {
		return state.usage, err
	}
	return state.usage, nil
}
