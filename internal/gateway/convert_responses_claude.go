package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Direct point-to-point conversion between OpenAI Responses and Anthropic
// (Claude) Messages, avoiding the lossy OpenAI-Chat intermediate representation.
//
// Ported (and adapted to this gateway's helpers) from cc-switch
// (github.com/farion1231/cc-switch, MIT License). See THIRD_PARTY_NOTICES.md.
// The key win over the Chat IR is preserving Anthropic signed thinking blocks
// round-trip via the opaque Responses `reasoning.encrypted_content` field, so a
// follow-up tool-result turn can replay the exact signed block upstream.

const anthropicThinkingEncryptedPrefix = "luca-anthropic-thinking-v1:"

// encodeAnthropicThinkingBlock stores a signed Anthropic thinking /
// redacted_thinking block inside an opaque, prefixed base64 string so it can be
// carried through Responses `reasoning.encrypted_content` and later replayed.
func encodeAnthropicThinkingBlock(block map[string]any) (string, bool) {
	switch stringValue(block["type"]) {
	case "thinking", "redacted_thinking":
	default:
		return "", false
	}
	raw, err := json.Marshal(block)
	if err != nil {
		return "", false
	}
	return anthropicThinkingEncryptedPrefix + base64.RawURLEncoding.EncodeToString(raw), true
}

// decodeAnthropicThinkingBlock reverses encodeAnthropicThinkingBlock. Non-matching
// (e.g. another provider's ciphertext) input yields ok=false.
func decodeAnthropicThinkingBlock(encrypted string) (map[string]any, bool) {
	encoded, ok := strings.CutPrefix(encrypted, anthropicThinkingEncryptedPrefix)
	if !ok {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	var block map[string]any
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, false
	}
	switch stringValue(block["type"]) {
	case "thinking", "redacted_thinking":
		return block, true
	default:
		return nil, false
	}
}

// responsesReasoningItemFromAnthropicBlock builds a Responses `reasoning` output
// item that both surfaces a human-readable summary and preserves the signed
// block for replay.
func responsesReasoningItemFromAnthropicBlock(itemID string, block map[string]any) (map[string]any, bool) {
	encrypted, ok := encodeAnthropicThinkingBlock(block)
	if !ok {
		return nil, false
	}
	summary := []any{}
	if text := stringValue(block["thinking"]); text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return map[string]any{
		"id":                itemID,
		"type":              "reasoning",
		"summary":           summary,
		"encrypted_content": encrypted,
	}, true
}

// mapAnthropicStopReasonToStatus maps Anthropic stop_reason to the Responses
// (status, incomplete_details.reason) pair.
func mapAnthropicStopReasonToStatus(stopReason string) (string, string) {
	switch strings.TrimSpace(stopReason) {
	case "max_tokens", "model_context_window_exceeded":
		return "incomplete", "max_output_tokens"
	case "refusal":
		return "incomplete", "content_filter"
	default:
		return "completed", ""
	}
}

func _directResponsesClaudeMarker() { _ = fmt.Sprint }

func isMeaningfulText(text string) bool {
	return strings.TrimSpace(text) != ""
}

// pushClaudeBlock appends a content block to the last message when it already
// belongs to role, otherwise starts a new message. Anthropic requires content to
// be a block array here (we always build arrays and normalize later).
func pushClaudeBlock(messages *[]map[string]any, role string, block map[string]any) {
	if n := len(*messages); n > 0 {
		last := (*messages)[n-1]
		if stringValue(last["role"]) == role {
			blocks, _ := last["content"].([]any)
			last["content"] = append(blocks, block)
			return
		}
	}
	*messages = append(*messages, map[string]any{
		"role":    role,
		"content": []any{block},
	})
}

// pushClaudeToolResult always attaches tool_result blocks to a user message,
// merging consecutive results into the same user turn.
func pushClaudeToolResult(messages *[]map[string]any, block map[string]any) {
	if n := len(*messages); n > 0 {
		last := (*messages)[n-1]
		if stringValue(last["role"]) == "user" {
			blocks, _ := last["content"].([]any)
			last["content"] = append(blocks, block)
			return
		}
	}
	*messages = append(*messages, map[string]any{
		"role":    "user",
		"content": []any{block},
	})
}

// responsesContentPartToClaudeBlock converts one Responses content part
// (input_text/output_text/input_image/text) into a Claude content block.
func responsesContentPartToClaudeBlock(part map[string]any) map[string]any {
	switch stringValue(part["type"]) {
	case "input_text", "output_text", "text":
		if text := stringValue(part["text"]); isMeaningfulText(text) {
			return map[string]any{"type": "text", "text": text}
		}
	case "input_image":
		if chatBlock := responsesInputBlockToChat(part); chatBlock != nil {
			if claude := openAIImageURLBlockToClaude(chatBlock); claude != nil {
				return claude
			}
		}
	}
	return nil
}

// responsesInputToClaudeMessages re-nests the flat Responses input[] back into
// Anthropic messages, restoring signed thinking blocks from encrypted_content.
func responsesInputToClaudeMessages(input any) []map[string]any {
	messages := make([]map[string]any, 0, 8)
	switch typed := input.(type) {
	case string:
		if isMeaningfulText(typed) {
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": typed}},
			})
		}
	case []any:
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(item["type"]) {
			case "function_call":
				callID := sanitizeAnthropicToolUseID(firstNonEmpty(stringValue(item["call_id"]), stringValue(item["id"])))
				name := stringValue(item["name"])
				if name == "" {
					continue
				}
				pushClaudeBlock(&messages, "assistant", map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": parseJSONArguments(stringValue(item["arguments"])),
				})
			case "function_call_output", "custom_tool_call_output", "tool_search_output":
				callID := sanitizeAnthropicToolUseID(stringValue(item["call_id"]))
				if callID == "" {
					continue
				}
				pushClaudeToolResult(&messages, map[string]any{
					"type":        "tool_result",
					"tool_use_id": callID,
					"content":     toolResultContentToString(item["output"]),
				})
			case "reasoning":
				if block, ok := decodeAnthropicThinkingBlock(stringValue(item["encrypted_content"])); ok {
					pushClaudeBlock(&messages, "assistant", block)
				}
			case "input_text", "output_text", "text", "input_image":
				role := firstNonEmpty(stringValue(item["role"]), "user")
				if role != "assistant" {
					role = "user"
				}
				if block := responsesContentPartToClaudeBlock(item); block != nil {
					pushClaudeBlock(&messages, role, block)
				}
			default:
				// message item or an item carrying a role + content
				role := firstNonEmpty(stringValue(item["role"]), "user")
				if role != "assistant" {
					role = "user"
				}
				switch content := item["content"].(type) {
				case string:
					if isMeaningfulText(content) {
						pushClaudeBlock(&messages, role, map[string]any{"type": "text", "text": content})
					}
				case []any:
					for _, rawPart := range content {
						part, ok := rawPart.(map[string]any)
						if !ok {
							continue
						}
						if block := responsesContentPartToClaudeBlock(part); block != nil {
							pushClaudeBlock(&messages, role, block)
						}
					}
				}
			}
		}
	}
	return messages
}

// ensureLeadingClaudeUser guarantees the first message is from the user, as
// required by Anthropic /v1/messages. Compacted/resumed sessions may start with
// an assistant/tool_use turn.
func ensureLeadingClaudeUser(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}
	if stringValue(messages[0]["role"]) == "user" {
		return messages
	}
	lead := map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": "."}},
	}
	return append([]map[string]any{lead}, messages...)
}

// responsesToClaudeRequestDirect converts a Responses request directly into an
// Anthropic Messages request (no OpenAI-Chat intermediate). Thinking/effort
// mapping reuses the existing Chat↔Claude helpers so behavior stays consistent.
func responsesToClaudeRequestDirect(responsesReq map[string]any, model string, maxTokensOverride int) (map[string]any, error) {
	claudeReq := map[string]any{"model": model}

	if instructions := strings.TrimSpace(stringValue(responsesReq["instructions"])); instructions != "" {
		claudeReq["system"] = instructions
	}

	messages := responsesInputToClaudeMessages(responsesReq["input"])
	messages = ensureLeadingClaudeUser(messages)
	if len(messages) == 0 {
		return nil, fmt.Errorf("responses request has no usable input items")
	}
	claudeMessages := make([]any, 0, len(messages))
	for _, m := range messages {
		claudeMessages = append(claudeMessages, m)
	}
	claudeReq["messages"] = claudeMessages

	// 预算按实际上游 model 参数计算；密钥覆盖 >0 时优先。忽略客户端 max_output_tokens。
	claudeReq["max_tokens"] = effectiveClaudeMaxTokens(model, maxTokensOverride)
	if stream, ok := responsesReq["stream"].(bool); ok && stream {
		claudeReq["stream"] = true
	} else {
		claudeReq["stream"] = false
	}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := responsesReq[key]; exists {
			claudeReq[key] = value
		}
	}

	// Tools: reuse the Responses→Chat tool normalization, then Chat→Claude tool
	// shape (both are pure shape maps, not a protocol hop).
	chatToolCarrier := map[string]any{}
	copyResponsesToolsToChat(responsesReq, chatToolCarrier)
	copyToolsField(chatToolCarrier, claudeReq, true)
	copyToolChoiceField(chatToolCarrier, claudeReq, true)

	// reasoning.effort → Claude thinking (reuse the shared mapping).
	if reasoning, ok := responsesReq["reasoning"].(map[string]any); ok {
		if effort := normalizeReasoningEffort(stringValue(reasoning["effort"])); effort != "" {
			carrier := map[string]any{"reasoning_effort": effort}
			applyOpenAIThinkingToClaudeRequest(carrier, claudeReq, model)
		}
	} else if claudeModelRequiresAdaptiveThinking(model) {
		// Model requires adaptive thinking even without an explicit effort.
		applyAdaptiveThinking(claudeReq, "high")
		normalizeClaudeTemperatureForThinking(claudeReq)
	}

	return claudeReq, nil
}

// sanitizeResponsesToolName restores the client's original tool name (pre-cloak)
// so the client (Codex) recognizes its own tool. Reuses resolveOpenAIToolNameFromClaude.
func sanitizeResponsesToolName(claudeName string, clientToolNames map[string]struct{}) string {
	return resolveOpenAIToolNameFromClaude(claudeName, clientToolNames)
}

// claudeToResponsesResponseDirect converts a non-streamed Anthropic Messages
// response directly into a Responses response, preserving signed thinking blocks
// as reasoning items and restoring client tool names.
func claudeToResponsesResponseDirect(claudeBody []byte, model string, clientToolNames map[string]struct{}) ([]byte, TokenUsage, error) {
	var payload map[string]any
	if err := json.Unmarshal(claudeBody, &payload); err != nil {
		return nil, TokenUsage{}, err
	}
	if errorValue, ok := payload["error"]; ok {
		return claudeErrorValueToResponsesDirect(errorValue, model)
	}
	if stringValue(payload["type"]) == "error" {
		return claudeErrorValueToResponsesDirect(payload, model)
	}

	id := stringValue(payload["id"])
	responseID := "resp_luca"
	switch {
	case id == "":
	case strings.HasPrefix(id, "resp_"):
		responseID = id
	default:
		responseID = "resp_" + id
	}

	output := make([]map[string]any, 0, 4)
	textParts := make([]any, 0, 4)
	flushText := func() {
		if len(textParts) == 0 {
			return
		}
		idx := len(output)
		output = append(output, map[string]any{
			"id":      fmt.Sprintf("%s_msg_%d", responseID, idx),
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": textParts,
		})
		textParts = make([]any, 0, 4)
	}

	if blocks, ok := payload["content"].([]any); ok {
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "text":
				if text := stringValue(block["text"]); text != "" {
					textParts = append(textParts, map[string]any{
						"type":        "output_text",
						"text":        text,
						"annotations": []any{},
					})
				}
			case "tool_use":
				flushText()
				callID := sanitizeResponsesCallID(stringValue(block["id"]))
				name := sanitizeResponsesToolName(stringValue(block["name"]), clientToolNames)
				args := "{}"
				if input := block["input"]; input != nil {
					if raw, err := json.Marshal(input); err == nil {
						args = string(raw)
					}
				}
				output = append(output, map[string]any{
					"id":        fmt.Sprintf("%s_call_%d", responseID, len(output)),
					"type":      "function_call",
					"status":    "completed",
					"call_id":   callID,
					"name":      name,
					"arguments": args,
				})
			case "thinking", "redacted_thinking":
				flushText()
				if item, ok := responsesReasoningItemFromAnthropicBlock(fmt.Sprintf("rs_%s_%d", responseID, len(output)), block); ok {
					output = append(output, item)
				}
			}
		}
	}
	flushText()

	status, incompleteReason := mapAnthropicStopReasonToStatus(stringValue(payload["stop_reason"]))
	usage := ParseClaudeUsage(claudeBody)

	response := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": 0,
		"status":     status,
		"model":      firstNonEmpty(model, stringValue(payload["model"])),
		"output":     output,
		"usage": map[string]any{
			"input_tokens":          usage.InputTokens,
			"output_tokens":         usage.OutputTokens,
			"total_tokens":          usage.InputTokens + usage.OutputTokens,
			"input_tokens_details":  map[string]any{"cached_tokens": usage.CacheTokens},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		},
	}
	if incompleteReason != "" {
		response["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}

	body, err := json.Marshal(response)
	return body, usage, err
}

func claudeErrorValueToResponsesDirect(errorValue any, model string) ([]byte, TokenUsage, error) {
	message, errorType := errorMessageFromValue(errorValue, "upstream request failed")
	body, err := json.Marshal(map[string]any{
		"id":         fmt.Sprintf("resp_%d", 0),
		"object":     "response",
		"created_at": 0,
		"status":     "failed",
		"model":      model,
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
	return body, TokenUsage{}, err
}