package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Codex Responses extensions (namespace / tool_search / custom) that Claude
// Messages cannot accept verbatim. Ported from cc-switch
// transform_codex_chat.rs / transform_codex_responses_namespace.rs (and
// sub2api responses_namespace.go). See THIRD_PARTY_NOTICES.md.

const (
	codexChatToolNameMaxLen          = 64
	codexToolSearchProxyName         = "tool_search"
	codexCustomToolInputField        = "input"
	codexCustomToolInputDescription  = "Raw string input for the original custom tool. Preserve formatting exactly and follow the original tool definition embedded in the description."
	codexCustomToolMetadataHeading   = "Original tool definition:"
)

type codexToolKind int

const (
	codexToolKindFunction codexToolKind = iota
	codexToolKindNamespace
	codexToolKindCustom
	codexToolKindToolSearch
)

type codexToolSpec struct {
	Kind      codexToolKind
	Name      string // bare / original name (not flattened)
	Namespace string // empty for non-namespace tools
}

// codexToolContext flattens Codex Responses tools for Claude and restores
// identity (namespace / custom / tool_search) on the way back to the client.
type codexToolContext struct {
	flatToSpec    map[string]codexToolSpec
	claudeTools   []any
	seenFlatNames map[string]struct{}
	clientNames   map[string]struct{} // names Codex registered (bare + flat)
}

func newCodexToolContext() *codexToolContext {
	return &codexToolContext{
		flatToSpec:    make(map[string]codexToolSpec),
		seenFlatNames: make(map[string]struct{}),
		clientNames:   make(map[string]struct{}),
	}
}

// codexToolContextFromClientNames builds a minimal context that only restores
// OAuth-cloaked tool names (no namespace/custom metadata).
func codexToolContextFromClientNames(names map[string]struct{}) *codexToolContext {
	if len(names) == 0 {
		return nil
	}
	ctx := newCodexToolContext()
	for name := range names {
		ctx.rememberClientName(name)
	}
	return ctx
}

// buildCodexToolContextFromRequest parses Responses tools (+ tools loaded via
// prior tool_search_output / additional_tools in input history) into
// Claude-callable tools. Newer Codex clients (Responses Lite / sub-agents)
// declare runtime tools only in input items shaped as
// {"type":"additional_tools","tools":[...]} — matching sub2api
// EffectiveResponsesTools.
func buildCodexToolContextFromRequest(responsesReq map[string]any) *codexToolContext {
	ctx := newCodexToolContext()
	if responsesReq == nil {
		return ctx
	}
	for _, item := range asMapSlice(responsesReq["tools"]) {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ctx.addResponseTool(tool)
	}
	collectAdditionalToolsFromInput(responsesReq["input"], ctx)
	collectToolSearchOutputTools(responsesReq["input"], ctx)
	return ctx
}

func (c *codexToolContext) ClientToolNames() map[string]struct{} {
	if c == nil || len(c.clientNames) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(c.clientNames))
	for name := range c.clientNames {
		out[name] = struct{}{}
	}
	return out
}

func (c *codexToolContext) ClaudeTools() []any {
	if c == nil || len(c.claudeTools) == 0 {
		return nil
	}
	return c.claudeTools
}

func (c *codexToolContext) rememberClientName(name string) {
	name = strings.TrimSpace(name)
	if name == "" || c == nil {
		return
	}
	c.clientNames[name] = struct{}{}
}

func (c *codexToolContext) addClaudeTool(flatName string, spec codexToolSpec, claudeTool map[string]any) {
	if c == nil || flatName == "" || claudeTool == nil {
		return
	}
	if _, seen := c.seenFlatNames[flatName]; seen {
		return
	}
	c.seenFlatNames[flatName] = struct{}{}
	c.flatToSpec[flatName] = spec
	c.rememberClientName(flatName)
	c.rememberClientName(spec.Name)
	// Claude OAuth cloaking remaps lowercase/snake tools to TitleCase (exec → Exec)
	// before the upstream round-trip. Index the cloaked alias so stream/non-stream
	// restore still recognizes custom / tool_search / namespace kinds.
	if cloaked, changed := remapClaudeOAuthToolName(flatName); changed && cloaked != "" && cloaked != flatName {
		if _, exists := c.flatToSpec[cloaked]; !exists {
			c.flatToSpec[cloaked] = spec
		}
	}
	c.claudeTools = append(c.claudeTools, claudeTool)
}

func (c *codexToolContext) addResponseTool(tool map[string]any) {
	if c == nil || tool == nil {
		return
	}
	switch strings.TrimSpace(strings.ToLower(stringValue(tool["type"]))) {
	case "", "function":
		c.addFunctionTool(tool, "")
	case "custom":
		c.addCustomTool(tool)
	case "tool_search":
		c.addToolSearchTool()
	case "namespace":
		c.addNamespaceTool(tool)
	default:
		// Built-ins without a client-callable name (web_search, ...) stay dropped.
	}
}

func (c *codexToolContext) addFunctionTool(tool map[string]any, namespace string) {
	original := responsesToolDefinitionName(tool)
	if original == "" {
		return
	}
	flat := original
	kind := codexToolKindFunction
	if namespace != "" {
		flat = flattenNamespaceToolName(namespace, original)
		kind = codexToolKindNamespace
	}
	claudeTool := map[string]any{
		"name":         flat,
		"input_schema": normalizeFunctionInputSchema(tool),
	}
	if desc := responsesToolDefinitionDescription(tool); desc != "" {
		claudeTool["description"] = desc
	}
	c.addClaudeTool(flat, codexToolSpec{Kind: kind, Name: original, Namespace: namespace}, claudeTool)
}

func (c *codexToolContext) addCustomTool(tool map[string]any) {
	name := responsesToolDefinitionName(tool)
	if name == "" {
		return
	}
	desc := codexCustomToolMetadataHeading + "\n```json\n" + compactJSONString(tool) + "\n```"
	claudeTool := map[string]any{
		"name":        name,
		"description": desc,
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				codexCustomToolInputField: map[string]any{
					"type":        "string",
					"description": codexCustomToolInputDescription,
				},
			},
			"required": []any{codexCustomToolInputField},
		},
	}
	c.addClaudeTool(name, codexToolSpec{Kind: codexToolKindCustom, Name: name}, claudeTool)
}

func (c *codexToolContext) addToolSearchTool() {
	claudeTool := map[string]any{
		"name":        codexToolSearchProxyName,
		"description": "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query for tools or connectors to load.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of tool groups to return.",
				},
			},
			"required": []any{"query"},
		},
	}
	c.addClaudeTool(codexToolSearchProxyName, codexToolSpec{
		Kind: codexToolKindToolSearch,
		Name: codexToolSearchProxyName,
	}, claudeTool)
}

func (c *codexToolContext) addNamespaceTool(namespaceTool map[string]any) {
	namespace := strings.TrimSpace(stringValue(namespaceTool["name"]))
	if namespace == "" {
		return
	}
	children := asMapSlice(namespaceTool["tools"])
	if len(children) == 0 {
		children = asMapSlice(namespaceTool["children"])
	}
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.ToLower(stringValue(child["type"]))) != "function" {
			continue
		}
		c.addFunctionTool(child, namespace)
	}
}

func collectToolSearchOutputTools(value any, ctx *codexToolContext) {
	if ctx == nil || value == nil {
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectToolSearchOutputTools(item, ctx)
		}
	case map[string]any:
		if stringValue(typed["type"]) == "tool_search_output" {
			for _, raw := range asMapSlice(typed["tools"]) {
				if tool, ok := raw.(map[string]any); ok {
					ctx.addResponseTool(tool)
				}
			}
		}
		for _, child := range typed {
			collectToolSearchOutputTools(child, ctx)
		}
	}
}

// collectAdditionalToolsFromInput promotes Codex Responses Lite
// `additional_tools` carriers into the Claude/Chat tool list. These items must
// never become chat messages (they have role=developer but no content).
func collectAdditionalToolsFromInput(input any, ctx *codexToolContext) {
	if ctx == nil || input == nil {
		return
	}
	items, ok := input.([]any)
	if !ok {
		return
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.ToLower(stringValue(item["type"]))) != "additional_tools" {
			continue
		}
		for _, rawTool := range asMapSlice(item["tools"]) {
			if tool, ok := rawTool.(map[string]any); ok {
				ctx.addResponseTool(tool)
			}
		}
	}
}

// flatNameForCall resolves the Claude-facing tool name for a Responses call item.
func (c *codexToolContext) flatNameForCall(name, namespace string) string {
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if name == "" {
		return ""
	}
	if namespace != "" {
		return flattenNamespaceToolName(namespace, name)
	}
	if c != nil {
		if _, ok := c.flatToSpec[name]; ok {
			return name
		}
	}
	return name
}

// lookupSpec resolves a Claude-facing tool name (possibly OAuth-cloaked) back
// to the Codex tool spec registered from the original Responses request.
func (c *codexToolContext) lookupSpec(claudeName string) (codexToolSpec, bool) {
	if c == nil {
		return codexToolSpec{}, false
	}
	claudeName = strings.TrimSpace(claudeName)
	if claudeName == "" {
		return codexToolSpec{}, false
	}
	if spec, ok := c.flatToSpec[claudeName]; ok {
		return spec, true
	}
	resolved := sanitizeResponsesToolName(claudeName, c.ClientToolNames())
	if resolved != "" && resolved != claudeName {
		if spec, ok := c.flatToSpec[resolved]; ok {
			return spec, true
		}
	}
	if lower := strings.ToLower(claudeName); lower != claudeName {
		if spec, ok := c.flatToSpec[lower]; ok {
			return spec, true
		}
	}
	return codexToolSpec{}, false
}

// buildResponsesToolCallItem maps a Claude tool_use (flat name + JSON args)
// back into the Responses item shape Codex expects.
func (c *codexToolContext) buildResponsesToolCallItem(itemID, callID, claudeName, arguments, status string) map[string]any {
	if status == "" {
		status = "completed"
	}
	resolvedName := claudeName
	if c != nil {
		resolvedName = sanitizeResponsesToolName(claudeName, c.ClientToolNames())
	} else {
		resolvedName = sanitizeResponsesToolName(claudeName, nil)
	}
	if c != nil {
		if spec, ok := c.lookupSpec(claudeName); ok {
			switch spec.Kind {
			case codexToolKindToolSearch:
				return map[string]any{
					"type":      "tool_search_call",
					"call_id":   callID,
					"status":    status,
					"execution": "client",
					"arguments": parseToolArgumentsObject(arguments),
				}
			case codexToolKindCustom:
				return map[string]any{
					"id":      itemID,
					"type":    "custom_tool_call",
					"status":  status,
					"call_id": callID,
					"name":    spec.Name,
					"input":   customToolInputFromChatArguments(arguments),
				}
			case codexToolKindNamespace:
				item := map[string]any{
					"id":        itemID,
					"type":      "function_call",
					"status":    status,
					"call_id":   callID,
					"name":      spec.Name,
					"arguments": emptyArgsFallback(arguments),
				}
				if spec.Namespace != "" {
					item["namespace"] = spec.Namespace
				}
				return item
			default:
				return map[string]any{
					"id":        itemID,
					"type":      "function_call",
					"status":    status,
					"call_id":   callID,
					"name":      spec.Name,
					"arguments": emptyArgsFallback(arguments),
				}
			}
		}
	}
	return map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   callID,
		"name":      resolvedName,
		"arguments": emptyArgsFallback(arguments),
	}
}

func (c *codexToolContext) isCustomOrToolSearchFlatName(flatName string) bool {
	spec, ok := c.lookupSpec(flatName)
	if !ok {
		return false
	}
	return spec.Kind == codexToolKindCustom || spec.Kind == codexToolKindToolSearch
}

func (c *codexToolContext) normalizeToolChoice(choice any) any {
	if choice == nil {
		return nil
	}
	switch typed := choice.(type) {
	case string:
		return typed
	case map[string]any:
		choiceType := strings.TrimSpace(strings.ToLower(stringValue(typed["type"])))
		switch choiceType {
		case "namespace":
			return "auto"
		case "tool_search":
			return map[string]any{"type": "tool", "name": codexToolSearchProxyName}
		case "function", "custom", "tool":
			name := stringValue(typed["name"])
			if name == "" {
				if fn, ok := typed["function"].(map[string]any); ok {
					name = stringValue(fn["name"])
				}
			}
			namespace := stringValue(typed["namespace"])
			flat := c.flatNameForCall(name, namespace)
			if flat == "" {
				return "auto"
			}
			return map[string]any{"type": "tool", "name": flat}
		case "auto", "none", "required", "any":
			return typed
		default:
			return typed
		}
	default:
		return choice
	}
}

func flattenNamespaceToolName(namespace, name string) string {
	full := strings.TrimSpace(namespace) + "__" + strings.TrimSpace(name)
	if len(full) <= codexChatToolNameMaxLen {
		return full
	}
	hash := shortSHA256Hex([]byte(full))
	suffix := "__" + hash
	prefixLen := codexChatToolNameMaxLen - len(suffix)
	if prefixLen < 0 {
		prefixLen = 0
	}
	prefix := full
	if len(prefix) > prefixLen {
		prefix = prefix[:prefixLen]
	}
	return prefix + suffix
}

func shortSHA256Hex(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:8])
}

func responsesToolDefinitionName(tool map[string]any) string {
	if tool == nil {
		return ""
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		if name := stringValue(fn["name"]); name != "" {
			return name
		}
	}
	if custom, ok := tool["custom"].(map[string]any); ok {
		if name := stringValue(custom["name"]); name != "" {
			return name
		}
	}
	return stringValue(tool["name"])
}

func responsesToolDefinitionDescription(tool map[string]any) string {
	if tool == nil {
		return ""
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		if desc := stringValue(fn["description"]); desc != "" {
			return desc
		}
	}
	if custom, ok := tool["custom"].(map[string]any); ok {
		if desc := stringValue(custom["description"]); desc != "" {
			return desc
		}
	}
	return stringValue(tool["description"])
}

func normalizeFunctionInputSchema(tool map[string]any) map[string]any {
	var params any
	if fn, ok := tool["function"].(map[string]any); ok {
		params = fn["parameters"]
		if params == nil {
			params = fn["input_schema"]
		}
	}
	if params == nil {
		params = tool["parameters"]
	}
	if params == nil {
		params = tool["input_schema"]
	}
	schema, ok := params.(map[string]any)
	if !ok || schema == nil {
		return emptyClaudeToolInputSchema()
	}
	out := cloneAnyMap(schema)
	if strings.TrimSpace(stringValue(out["type"])) != "object" {
		out["type"] = "object"
	}
	if _, ok := out["properties"]; !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

func compactJSONString(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func emptyArgsFallback(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

func parseToolArgumentsObject(arguments string) map[string]any {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return map[string]any{"query": arguments}
	}
	if obj, ok := parsed.(map[string]any); ok {
		return obj
	}
	return map[string]any{"query": arguments}
}

func customToolInputFromChatArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return arguments
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return arguments
	}
	if input, ok := obj[codexCustomToolInputField]; ok {
		switch typed := input.(type) {
		case string:
			return typed
		default:
			return fmt.Sprint(typed)
		}
	}
	return arguments
}

// toolCallInputForClaude builds the Claude tool_use.input value for a Responses
// call item (function / custom / tool_search).
func toolCallInputForClaude(item map[string]any, itemType string) any {
	switch itemType {
	case "custom_tool_call":
		input := item["input"]
		if input == nil {
			input = ""
		}
		return map[string]any{codexCustomToolInputField: input}
	case "tool_search_call":
		if args, ok := item["arguments"].(map[string]any); ok {
			return args
		}
		if s := stringValue(item["arguments"]); s != "" {
			return parseJSONArguments(s)
		}
		return map[string]any{}
	default:
		return parseJSONArguments(stringValue(item["arguments"]))
	}
}
