package gateway

import (
	"strings"
)

// Shared Responses request-field adapters used by Responses→Claude and
// Responses→Chat conversions (text.format, parallel_tool_calls, max tokens,
// multimodal input parts).

// applyResponsesTextFormatToClaude maps Responses `text.format` onto Claude
// `output_config.format` (GA structured outputs). Merges with any existing
// output_config (e.g. thinking effort) instead of replacing it.
func applyResponsesTextFormatToClaude(responsesReq, claudeReq map[string]any) {
	format := responsesTextFormat(responsesReq)
	if format == nil {
		return
	}
	claudeFormat := responsesTextFormatToClaudeOutputFormat(format)
	if claudeFormat == nil {
		return
	}
	cfg, _ := claudeReq["output_config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["format"] = claudeFormat
	claudeReq["output_config"] = cfg
}

// applyResponsesTextFormatToChat maps Responses `text.format` onto Chat
// Completions `response_format`.
func applyResponsesTextFormatToChat(responsesReq, chatReq map[string]any) {
	format := responsesTextFormat(responsesReq)
	if format == nil {
		return
	}
	if converted := responsesTextFormatToChatResponseFormat(format); converted != nil {
		chatReq["response_format"] = converted
	}
}

func responsesTextFormat(responsesReq map[string]any) map[string]any {
	if responsesReq == nil {
		return nil
	}
	text, _ := responsesReq["text"].(map[string]any)
	if text == nil {
		return nil
	}
	format, _ := text["format"].(map[string]any)
	return format
}

func responsesTextFormatToClaudeOutputFormat(format map[string]any) map[string]any {
	switch strings.TrimSpace(strings.ToLower(stringValue(format["type"]))) {
	case "json_schema":
		schema := format["schema"]
		if schema == nil {
			if nested, ok := format["json_schema"].(map[string]any); ok {
				schema = nested["schema"]
			}
		}
		if schema == nil {
			return nil
		}
		out := map[string]any{
			"type":   "json_schema",
			"schema": schema,
		}
		if name := stringValue(format["name"]); name != "" {
			out["name"] = name
		}
		return out
	case "json_object":
		// Claude has no dedicated json_object mode; approximate with a loose object schema.
		return map[string]any{
			"type": "json_schema",
			"schema": map[string]any{
				"type": "object",
			},
		}
	default:
		// "text" or unknown — leave Claude unconstrained.
		return nil
	}
}

func responsesTextFormatToChatResponseFormat(format map[string]any) map[string]any {
	switch strings.TrimSpace(strings.ToLower(stringValue(format["type"]))) {
	case "json_schema":
		schema := format["schema"]
		name := stringValue(format["name"])
		strict := format["strict"]
		if nested, ok := format["json_schema"].(map[string]any); ok {
			if schema == nil {
				schema = nested["schema"]
			}
			if name == "" {
				name = stringValue(nested["name"])
			}
			if strict == nil {
				strict = nested["strict"]
			}
		}
		if schema == nil {
			return nil
		}
		if name == "" {
			name = "response"
		}
		jsonSchema := map[string]any{
			"name":   name,
			"schema": schema,
		}
		if strict != nil {
			jsonSchema["strict"] = strict
		}
		return map[string]any{
			"type":        "json_schema",
			"json_schema": jsonSchema,
		}
	case "json_object":
		return map[string]any{"type": "json_object"}
	default:
		return nil
	}
}

// applyResponsesParallelToolCallsToClaude maps Responses parallel_tool_calls
// onto Claude tool_choice.disable_parallel_tool_use when disabling parallelism.
func applyResponsesParallelToolCallsToClaude(responsesReq, claudeReq map[string]any) {
	enabled, ok := responsesParallelToolCallsEnabled(responsesReq)
	if !ok || enabled {
		return
	}
	choice, _ := claudeReq["tool_choice"].(map[string]any)
	if choice == nil {
		choice = map[string]any{"type": "auto"}
	} else {
		choice = cloneAnyMap(choice)
		if choice == nil {
			choice = map[string]any{"type": "auto"}
		}
	}
	choice["disable_parallel_tool_use"] = true
	claudeReq["tool_choice"] = choice
}

// applyResponsesParallelToolCallsToChat forwards parallel_tool_calls onto Chat.
func applyResponsesParallelToolCallsToChat(responsesReq, chatReq map[string]any) {
	if responsesReq == nil {
		return
	}
	if value, exists := responsesReq["parallel_tool_calls"]; exists {
		chatReq["parallel_tool_calls"] = value
	}
}

func responsesParallelToolCallsEnabled(responsesReq map[string]any) (enabled bool, present bool) {
	if responsesReq == nil {
		return true, false
	}
	raw, exists := responsesReq["parallel_tool_calls"]
	if !exists || raw == nil {
		return true, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.TrimSpace(strings.ToLower(typed)) {
		case "false", "0", "no":
			return false, true
		case "true", "1", "yes":
			return true, true
		}
	}
	return true, true
}

// resolveClaudeMaxTokensFromResponses picks max_tokens for Responses→Claude.
// Key/model budget (override) is the hard cap; client max_output_tokens may only
// lower it.
func resolveClaudeMaxTokensFromResponses(responsesReq map[string]any, model string, maxTokensOverride int) int {
	capTokens := effectiveClaudeMaxTokens(model, maxTokensOverride)
	client := int(int64FromAny(responsesReq["max_output_tokens"]))
	if client <= 0 {
		return capTokens
	}
	client = normalizeMaxOutputTokens(client)
	if client <= 0 {
		return capTokens
	}
	if client < capTokens {
		return client
	}
	return capTokens
}

// responsesInputFileToClaudeBlock converts Responses input_file into a Claude
// document block. file_id cannot be resolved without OpenAI Files storage, so
// those are skipped.
func responsesInputFileToClaudeBlock(part map[string]any) map[string]any {
	if part == nil {
		return nil
	}
	// Prefer inline data URL / base64 payload.
	if dataURL := strings.TrimSpace(stringValue(part["file_data"])); dataURL != "" {
		if !strings.HasPrefix(dataURL, "data:") {
			// Bare base64: require media type from filename or explicit field.
			mediaType := firstNonEmpty(stringValue(part["media_type"]), mediaTypeFromFilename(stringValue(part["filename"])))
			if mediaType == "" {
				mediaType = "application/pdf"
			}
			return map[string]any{
				"type": "document",
				"source": map[string]any{
					"type":       "base64",
					"media_type": mediaType,
					"data":       dataURL,
				},
			}
		}
		mediaType, data, ok := splitDataURL(dataURL)
		if !ok {
			return nil
		}
		return map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}
	}
	if url := strings.TrimSpace(stringValue(part["file_url"])); url != "" {
		return map[string]any{
			"type": "document",
			"source": map[string]any{
				"type": "url",
				"url":  url,
			},
		}
	}
	// file_id has no local resolver in the gateway.
	return nil
}

// responsesInputFileToChatBlock best-effort maps input_file for Chat Completions.
// Most Chat upstreams lack native PDF parts; data-URL images become image_url,
// otherwise we attach a short textual reference so the turn still has content.
func responsesInputFileToChatBlock(part map[string]any) map[string]any {
	if part == nil {
		return nil
	}
	if dataURL := strings.TrimSpace(stringValue(part["file_data"])); strings.HasPrefix(dataURL, "data:image/") {
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": dataURL,
			},
		}
	}
	if url := strings.TrimSpace(stringValue(part["file_url"])); strings.HasPrefix(strings.ToLower(url), "data:image/") {
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": url,
			},
		}
	}
	name := firstNonEmpty(stringValue(part["filename"]), stringValue(part["file_id"]), "attachment")
	if url := strings.TrimSpace(stringValue(part["file_url"])); url != "" {
		return map[string]any{
			"type": "text",
			"text": "[file: " + name + " url=" + url + "]",
		}
	}
	if stringValue(part["file_data"]) != "" || stringValue(part["file_id"]) != "" {
		return map[string]any{
			"type": "text",
			"text": "[file: " + name + "]",
		}
	}
	return nil
}

func mediaTypeFromFilename(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(name, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain"
	case strings.HasSuffix(name, ".md"):
		return "text/markdown"
	case strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".htm"):
		return "text/html"
	case strings.HasSuffix(name, ".csv"):
		return "text/csv"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	default:
		return ""
	}
}
