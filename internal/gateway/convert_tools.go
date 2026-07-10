package gateway

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// anthropicToolUseIDInvalid matches characters Anthropic rejects in tool_use.id /
// tool_result.tool_use_id (must be ^[a-zA-Z0-9_-]+$). Cursor and other clients
// often emit IDs like "functions.Read:1" or "call|abc" that pass OpenAI validation
// but fail on Claude.
var anthropicToolUseIDInvalid = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// sanitizeAnthropicToolUseID rewrites a tool call id to Anthropic's allowed
// charset. The same transform is applied to tool_use.id and tool_result.tool_use_id
// so pairings stay intact.
func sanitizeAnthropicToolUseID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "toolu_missing"
	}
	sanitized := anthropicToolUseIDInvalid.ReplaceAllString(id, "_")
	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		return "toolu_sanitized"
	}
	return sanitized
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func asMapSlice(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

func parseJSONArguments(arguments string) any {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return map[string]any{}
	}
	if parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func stringifyToolArguments(input any) string {
	switch typed := input.(type) {
	case string:
		return typed
	case nil:
		return "{}"
	default:
		body, err := json.Marshal(typed)
		if err != nil {
			return "{}"
		}
		return string(body)
	}
}

func normalizeToolResultContent(content any) any {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		if len(typed) == 0 {
			return ""
		}
		return typed
	default:
		text := strings.TrimSpace(fmt.Sprint(content))
		if text == "" {
			return ""
		}
		return text
	}
}

func toolResultContentToString(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		return claudeContentToString(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(content))
	}
}

func contentBlocksFromAny(content any) []any {
	switch typed := content.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	case []any:
		return cloneContentBlocks(typed)
	default:
		text := strings.TrimSpace(fmt.Sprint(content))
		if text == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": text}}
	}
}

func emptyClaudeToolInputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func claudeToolInputSchema(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		return value
	}
	return emptyClaudeToolInputSchema()
}

func openAIToolToClaude(tool map[string]any) map[string]any {
	toolType := strings.TrimSpace(stringValue(tool["type"]))
	if toolType == "" {
		toolType = "function"
	}

	// Chat Completions nested function tool: {type:"function", function:{name,parameters}}
	if toolType == "function" {
		if functionValue, ok := tool["function"].(map[string]any); ok {
			claudeTool := map[string]any{"name": stringValue(functionValue["name"])}
			if description := stringValue(functionValue["description"]); description != "" {
				claudeTool["description"] = description
			}
			claudeTool["input_schema"] = claudeToolInputSchema(functionValue["parameters"], functionValue["input_schema"])
			return claudeTool
		}
		// Responses API flat function tool: {type:"function", name, parameters}
		claudeTool := map[string]any{"name": stringValue(tool["name"])}
		if description := stringValue(tool["description"]); description != "" {
			claudeTool["description"] = description
		}
		claudeTool["input_schema"] = claudeToolInputSchema(tool["parameters"], tool["input_schema"])
		return claudeTool
	}

	// Responses/Claude custom tools may be flat or nested under "custom".
	name := stringValue(tool["name"])
	description := stringValue(tool["description"])
	schemaCandidates := []any{tool["input_schema"], tool["parameters"]}
	if customValue, ok := tool["custom"].(map[string]any); ok {
		if name == "" {
			name = stringValue(customValue["name"])
		}
		if description == "" {
			description = stringValue(customValue["description"])
		}
		schemaCandidates = append(schemaCandidates, customValue["input_schema"], customValue["parameters"])
	}
	claudeTool := map[string]any{"name": name}
	if description != "" {
		claudeTool["description"] = description
	}
	claudeTool["input_schema"] = claudeToolInputSchema(schemaCandidates...)
	return claudeTool
}

func claudeToolToOpenAI(tool map[string]any) map[string]any {
	functionValue := map[string]any{"name": stringValue(tool["name"])}
	if description := stringValue(tool["description"]); description != "" {
		functionValue["description"] = description
	}
	if schema := tool["input_schema"]; schema != nil {
		functionValue["parameters"] = schema
	} else if parameters := tool["parameters"]; parameters != nil {
		functionValue["parameters"] = parameters
	} else {
		functionValue["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{"type": "function", "function": functionValue}
}

// responsesToolToOpenAIChat normalizes a Responses API tool into Chat Completions
// form ({type:"function", function:{name,parameters}}). Built-in Responses tools
// without a client-callable name (web_search, file_search, ...) are dropped —
// Chat providers like BigModel/Z.AI only accept type=function.
func responsesToolToOpenAIChat(tool map[string]any) map[string]any {
	if tool == nil {
		return nil
	}
	// Already Chat Completions nested form.
	if functionValue, ok := tool["function"].(map[string]any); ok {
		name := stringValue(functionValue["name"])
		if name == "" {
			return nil
		}
		fn := map[string]any{"name": name}
		if description := stringValue(functionValue["description"]); description != "" {
			fn["description"] = description
		}
		fn["parameters"] = claudeToolInputSchema(functionValue["parameters"], functionValue["input_schema"])
		return map[string]any{"type": "function", "function": fn}
	}

	toolType := strings.TrimSpace(strings.ToLower(stringValue(tool["type"])))
	switch toolType {
	case "", "function", "custom":
		name := stringValue(tool["name"])
		description := stringValue(tool["description"])
		schemaCandidates := []any{tool["parameters"], tool["input_schema"]}
		if customValue, ok := tool["custom"].(map[string]any); ok {
			if name == "" {
				name = stringValue(customValue["name"])
			}
			if description == "" {
				description = stringValue(customValue["description"])
			}
			schemaCandidates = append(schemaCandidates, customValue["parameters"], customValue["input_schema"])
		}
		if name == "" {
			return nil
		}
		fn := map[string]any{"name": name}
		if description != "" {
			fn["description"] = description
		}
		fn["parameters"] = claudeToolInputSchema(schemaCandidates...)
		return map[string]any{"type": "function", "function": fn}
	default:
		return nil
	}
}

func responsesToolsToOpenAIChat(tools []any) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if converted := responsesToolToOpenAIChat(tool); converted != nil {
			out = append(out, converted)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// responsesToolChoiceToOpenAIChat maps Responses tool_choice into Chat Completions.
func responsesToolChoiceToOpenAIChat(choice any) any {
	switch typed := choice.(type) {
	case string:
		return typed
	case map[string]any:
		choiceType := strings.TrimSpace(strings.ToLower(stringValue(typed["type"])))
		switch choiceType {
		case "auto", "none", "required":
			return choiceType
		case "function":
			if functionValue, ok := typed["function"].(map[string]any); ok {
				if name := stringValue(functionValue["name"]); name != "" {
					return map[string]any{
						"type":     "function",
						"function": map[string]any{"name": name},
					}
				}
			}
			if name := stringValue(typed["name"]); name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		case "custom", "tool":
			if name := stringValue(typed["name"]); name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
		return cloneAnyMap(typed)
	default:
		return nil
	}
}

func copyResponsesToolsToChat(source map[string]any, target map[string]any) {
	if rawTools, exists := source["tools"]; exists {
		if converted := responsesToolsToOpenAIChat(asMapSlice(rawTools)); len(converted) > 0 {
			target["tools"] = converted
		}
	}
	if rawChoice, exists := source["tool_choice"]; exists {
		if converted := responsesToolChoiceToOpenAIChat(rawChoice); converted != nil {
			target["tool_choice"] = converted
		}
	}
}

func openAIToolsToClaude(tools []any) []any {
	if len(tools) == 0 {
		return nil
	}
	claudeTools := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if converted := openAIToolToClaude(tool); stringValue(converted["name"]) != "" {
			claudeTools = append(claudeTools, converted)
		}
	}
	return claudeTools
}

func claudeToolsToOpenAI(tools []any) []any {
	if len(tools) == 0 {
		return nil
	}
	openAITools := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if converted := claudeToolToOpenAI(tool); stringValue(converted["function"].(map[string]any)["name"]) != "" {
			openAITools = append(openAITools, converted)
		}
	}
	return openAITools
}

func openAIToolChoiceToClaude(choice any) any {
	switch typed := choice.(type) {
	case string:
		switch strings.TrimSpace(strings.ToLower(typed)) {
		case "", "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		choiceType := strings.TrimSpace(strings.ToLower(stringValue(typed["type"])))
		if choiceType == "function" {
			if functionValue, ok := typed["function"].(map[string]any); ok {
				if name := stringValue(functionValue["name"]); name != "" {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
		if name := stringValue(typed["name"]); name != "" && choiceType == "tool" {
			return map[string]any{"type": "tool", "name": name}
		}
		return cloneAnyMap(typed)
	default:
		return nil
	}
}

func claudeToolChoiceToOpenAI(choice any) any {
	switch typed := choice.(type) {
	case string:
		switch strings.TrimSpace(strings.ToLower(typed)) {
		case "", "auto":
			return "auto"
		case "none":
			return "none"
		case "any":
			return "required"
		default:
			return "auto"
		}
	case map[string]any:
		choiceType := strings.TrimSpace(strings.ToLower(stringValue(typed["type"])))
		switch choiceType {
		case "auto":
			return "auto"
		case "none":
			return "none"
		case "any":
			return "required"
		case "tool":
			if name := stringValue(typed["name"]); name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
		return cloneAnyMap(typed)
	default:
		return nil
	}
}

func copyToolsField(source map[string]any, target map[string]any, toClaude bool) {
	rawTools, exists := source["tools"]
	if !exists {
		return
	}
	tools := asMapSlice(rawTools)
	if len(tools) == 0 {
		return
	}
	if toClaude {
		if converted := openAIToolsToClaude(tools); len(converted) > 0 {
			target["tools"] = converted
		}
		return
	}
	if converted := claudeToolsToOpenAI(tools); len(converted) > 0 {
		target["tools"] = converted
	}
}

func copyToolChoiceField(source map[string]any, target map[string]any, toClaude bool) {
	rawChoice, exists := source["tool_choice"]
	if !exists {
		return
	}
	if toClaude {
		if converted := openAIToolChoiceToClaude(rawChoice); converted != nil {
			target["tool_choice"] = converted
		}
		return
	}
	if converted := claudeToolChoiceToOpenAI(rawChoice); converted != nil {
		target["tool_choice"] = converted
	}
}

func copyToolsFieldDirect(source map[string]any, target map[string]any) {
	if rawTools, exists := source["tools"]; exists {
		if tools := asMapSlice(rawTools); len(tools) > 0 {
			target["tools"] = tools
		}
	}
	if rawChoice, exists := source["tool_choice"]; exists {
		target["tool_choice"] = rawChoice
	}
}

func openAIToolCallsToClaudeBlocks(toolCalls []any) []any {
	blocks := make([]any, 0, len(toolCalls))
	for _, item := range toolCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		functionValue, _ := call["function"].(map[string]any)
		name := stringValue(functionValue["name"])
		if name == "" {
			continue
		}
		block := map[string]any{
			"type":  "tool_use",
			"id":    sanitizeAnthropicToolUseID(stringValue(call["id"])),
			"name":  name,
			"input": parseJSONArguments(stringValue(functionValue["arguments"])),
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func extractOpenAIToolNames(openAIReq map[string]any) map[string]struct{} {
	if openAIReq == nil {
		return nil
	}
	rawTools, ok := openAIReq["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(rawTools))
	for _, item := range rawTools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if functionValue, ok := tool["function"].(map[string]any); ok {
			if name := stringValue(functionValue["name"]); name != "" {
				names[name] = struct{}{}
			}
		}
		if name := stringValue(tool["name"]); name != "" {
			names[name] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// resolveOpenAIToolNameFromClaude maps Claude tool_use names back to the client
// tool names from the original OpenAI request. Cursor sends TitleCase names like
// "Read"; blindly reverse-remapping OAuth names would turn them into "read" and
// break client-side tool dispatch even when the upstream call succeeded.
func resolveOpenAIToolNameFromClaude(claudeName string, clientToolNames map[string]struct{}) string {
	claudeName = strings.TrimSpace(claudeName)
	if claudeName == "" {
		return claudeName
	}
	if clientToolNames != nil {
		if _, ok := clientToolNames[claudeName]; ok {
			return claudeName
		}
		lower := strings.ToLower(claudeName)
		if _, ok := clientToolNames[lower]; ok {
			return lower
		}
		if original, ok := oauthToolRenameReverseMap[claudeName]; ok {
			if _, ok := clientToolNames[original]; ok {
				return original
			}
		}
	}
	return reverseRemapClaudeOAuthToolName(claudeName)
}

func claudeToolUseBlocksToOpenAIToolCalls(blocks []any, clientToolNames map[string]struct{}) []any {
	toolCalls := make([]any, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok || stringValue(block["type"]) != "tool_use" {
			continue
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   stringValue(block["id"]),
			"type": "function",
			"function": map[string]any{
				"name":      resolveOpenAIToolNameFromClaude(stringValue(block["name"]), clientToolNames),
				"arguments": stringifyToolArguments(block["input"]),
			},
		})
	}
	return toolCalls
}

func claudeContentBlocksToOpenAIAssistantMessage(blocks []any, clientToolNames map[string]struct{}) map[string]any {
	message := map[string]any{"role": "assistant"}
	textParts := make([]string, 0, len(blocks))
	toolBlocks := make([]any, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			if text := stringValue(block["text"]); text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			toolBlocks = append(toolBlocks, block)
		}
	}
	if len(toolBlocks) > 0 {
		message["tool_calls"] = claudeToolUseBlocksToOpenAIToolCalls(toolBlocks, clientToolNames)
		if len(textParts) > 0 {
			message["content"] = strings.Join(textParts, "\n")
		} else {
			message["content"] = nil
		}
		return message
	}
	if len(textParts) == 1 {
		message["content"] = textParts[0]
	} else if len(textParts) > 1 {
		message["content"] = strings.Join(textParts, "\n")
	} else {
		message["content"] = ""
	}
	return message
}

func openAIAssistantMessageToClaudeContent(message map[string]any) any {
	blocks := make([]any, 0, 4)
	if content := message["content"]; content != nil && !isEmptyOpenAIContent(content) {
		blocks = append(blocks, openAIContentToClaudeBlocks(content)...)
	}
	if rawToolCalls, ok := message["tool_calls"].([]any); ok && len(rawToolCalls) > 0 {
		blocks = append(blocks, openAIToolCallsToClaudeBlocks(rawToolCalls)...)
	}
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 {
		if block, ok := blocks[0].(map[string]any); ok && stringValue(block["type"]) == "text" && len(block) <= 2 {
			return stringValue(block["text"])
		}
	}
	return blocks
}

func openAIMessagesToClaude(rawMessages []any) ([]any, []map[string]any, error) {
	systemBlocks := make([]any, 0, 4)
	claudeMessages := make([]map[string]any, 0, len(rawMessages))
	pendingToolResults := make([]any, 0, 2)

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		claudeMessages = append(claudeMessages, map[string]any{
			"role":    "user",
			"content": pendingToolResults,
		})
		pendingToolResults = nil
	}

	for _, item := range rawMessages {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(entry["role"]))
		if role == "" {
			continue
		}
		switch role {
		case "system", "developer":
			systemBlocks = append(systemBlocks, openAIContentToClaudeBlocks(entry["content"])...)
			continue
		case "tool":
			toolCallID := stringValue(entry["tool_call_id"])
			if toolCallID == "" {
				continue
			}
			pendingToolResults = append(pendingToolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": sanitizeAnthropicToolUseID(toolCallID),
				"content":     normalizeToolResultContent(entry["content"]),
			})
			continue
		}
		flushToolResults()

		switch role {
		case "assistant":
			content := openAIAssistantMessageToClaudeContent(entry)
			if isEmptyClaudeContent(content) {
				continue
			}
			claudeMessages = append(claudeMessages, map[string]any{"role": "assistant", "content": content})
		case "user":
			content := normalizeOpenAIContentForClaude(entry["content"])
			if isEmptyClaudeContent(content) {
				continue
			}
			claudeMessages = append(claudeMessages, map[string]any{"role": "user", "content": content})
		}
	}
	flushToolResults()

	if len(claudeMessages) == 0 {
		return systemBlocks, nil, fmt.Errorf("openai request has no usable messages")
	}
	return systemBlocks, claudeMessages, nil
}

func claudeMessagesToOpenAI(rawMessages []any) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(rawMessages))
	for _, item := range rawMessages {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(entry["role"]))
		if role == "" {
			continue
		}
		switch role {
		case "assistant":
			blocks := contentBlocksFromAny(entry["content"])
			if len(blocks) == 0 {
				continue
			}
			hasToolUse := false
			for _, blockItem := range blocks {
				if block, ok := blockItem.(map[string]any); ok && stringValue(block["type"]) == "tool_use" {
					hasToolUse = true
					break
				}
			}
			if hasToolUse {
				messages = append(messages, claudeContentBlocksToOpenAIAssistantMessage(blocks, nil))
				continue
			}
			content := normalizeClaudeContentForOpenAI(entry["content"])
			if isEmptyOpenAIContent(content) {
				continue
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": content})
		case "user":
			blocks := contentBlocksFromAny(entry["content"])
			if len(blocks) == 0 {
				continue
			}
			textBlocks := make([]any, 0, len(blocks))
			toolResults := make([]map[string]any, 0, len(blocks))
			for _, blockItem := range blocks {
				block, ok := blockItem.(map[string]any)
				if !ok {
					continue
				}
				switch stringValue(block["type"]) {
				case "tool_result":
					toolResults = append(toolResults, block)
				default:
					textBlocks = append(textBlocks, block)
				}
			}
			if len(textBlocks) > 0 {
				content := normalizeClaudeContentForOpenAI(textBlocks)
				if !isEmptyOpenAIContent(content) {
					messages = append(messages, map[string]any{"role": "user", "content": content})
				}
			}
			for _, result := range toolResults {
				toolUseID := stringValue(result["tool_use_id"])
				if toolUseID == "" {
					continue
				}
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": toolUseID,
					"content":      toolResultContentToString(result["content"]),
				})
			}
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("claude request has no usable messages")
	}
	return messages, nil
}

func claudeResponseContentToOpenAIAssistantMessage(content any, clientToolNames map[string]struct{}) map[string]any {
	blocks := contentBlocksFromAny(content)
	if len(blocks) == 0 {
		return map[string]any{"role": "assistant", "content": ""}
	}
	return claudeContentBlocksToOpenAIAssistantMessage(blocks, clientToolNames)
}

func openAIAssistantMessageToClaudeResponseContent(message map[string]any) []any {
	blocks := make([]any, 0, 4)
	if content := message["content"]; content != nil && !isEmptyOpenAIContent(content) {
		blocks = append(blocks, openAIContentToClaudeBlocks(content)...)
	}
	if rawToolCalls, ok := message["tool_calls"].([]any); ok && len(rawToolCalls) > 0 {
		blocks = append(blocks, openAIToolCallsToClaudeBlocks(rawToolCalls)...)
	}
	if len(blocks) == 0 {
		return []any{map[string]any{"type": "text", "text": ""}}
	}
	return blocks
}

func mapOpenAIFinishReasonToClaude(reason string) string {
	switch strings.TrimSpace(reason) {
	case "", "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return reason
	}
}
