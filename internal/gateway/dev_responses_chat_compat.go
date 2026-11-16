package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Ocean Dev 按 OpenAI Chat 单协议使用：Responses / Claude 客户端由平台转换成
// Chat，不把 Ocean 的 deployment 地址当作原生 Responses 或 Claude 端点。
//
// 它的 Chat deployment 比通用上游严格：gpt-5.4-pro / gpt-5.5 不接受 max，
// 展开工具可能超过 128，函数嵌套 object 必须显式 additionalProperties:false，
// 且 tool_call id 不能超过 64 字符。这些约束只属于 Ocean Dev，不能写进通用
// Responses↔Chat 转换器，否则会改变所有 OpenAI 兼容上游的行为。

func isOceanDevProvider(provider domain.Provider) bool {
	parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "ocean-dev.tuya-in.net" {
		return false
	}
	path := strings.ToLower(strings.TrimSpace(parsed.Path))
	return strings.Contains(path, "/deployments/") && strings.Contains(path, "/chat/completions")
}

// applyOceanDevChatCompat 是所有发往 Ocean Dev Chat 的统一整流。通用转换器
// 保持原样，只有这里根据 Ocean Dev 的严格校验整理工具和思考档位。
func applyOceanDevChatCompat(chatReq map[string]any, model string) {
	if chatReq == nil {
		return
	}
	applyOceanDevChatEffort(chatReq, model)
	// Ocean 的 gpt-5.5 只认新字段；这一层覆盖 Chat 直通，避免客户端用旧字段
	// 直接打 Ocean 时再被 400。通用转换器不做这个判断。
	normalizeOpenAIChatMaxTokensField(chatReq, model)
	fitOceanDevChatCallIDs(chatReq)
	tools, _ := chatReq["tools"].([]any)
	if len(tools) == 0 {
		return
	}
	sanitizeOceanDevChatFunctionTools(tools)
	tools = capOceanDevChatFunctionTools(tools, 128)
	chatReq["tools"] = tools
	if choice, exists := chatReq["tool_choice"]; exists {
		if selected := oceanDevChatToolChoiceName(choice); selected != "" && !oceanDevChatToolsContainName(tools, selected) {
			delete(chatReq, "tool_choice")
		}
	}
}

func applyOceanDevChatEffort(chatReq map[string]any, model string) {
	effort := normalizeReasoningEffort(stringValue(chatReq["reasoning_effort"]))
	if effort == "" {
		return
	}
	id := strings.ToLower(normalizeModelContextID(model))
	switch {
	case strings.Contains(id, "gpt-5.5"):
		switch effort {
		case "max":
			chatReq["reasoning_effort"] = "xhigh"
		case "minimal":
			chatReq["reasoning_effort"] = "low"
		}
	case strings.Contains(id, "gpt-5.4"):
		switch effort {
		case "max":
			chatReq["reasoning_effort"] = "xhigh"
		case "none", "minimal", "low":
			chatReq["reasoning_effort"] = "medium"
		}
	}
}

// fitOceanDevChatCallIDs 把 Chat 历史里的工具调用 id 稳定缩短到 64 字符以内。
// call 与后续 tool_result 使用同一个哈希，配对关系不变；对其他上游不生效。
func fitOceanDevChatCallIDs(chatReq map[string]any) {
	messages, _ := chatReq["messages"].([]any)
	if len(messages) == 0 {
		return
	}
	replacements := map[string]string{}
	shorten := func(raw string) string {
		id := strings.TrimSpace(raw)
		if id == "" {
			return ""
		}
		if len(id) <= 64 {
			return id
		}
		if existing, ok := replacements[id]; ok {
			return existing
		}
		sum := sha256.Sum256([]byte(id))
		prefix := "call"
		if i := strings.Index(id, "_"); i > 0 {
			prefix = id[:i]
		}
		maxPrefix := 64 - 1 - hex.EncodedLen(8)
		if maxPrefix < 0 {
			maxPrefix = 0
		}
		if len(prefix) > maxPrefix {
			prefix = prefix[:maxPrefix]
		}
		short := prefix + "_" + hex.EncodeToString(sum[:8])
		replacements[id] = short
		return short
	}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, item := range calls {
				call, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if id := stringValue(call["id"]); id != "" {
					call["id"] = shorten(id)
				}
			}
		}
		if id := stringValue(message["tool_call_id"]); id != "" {
			message["tool_call_id"] = shorten(id)
		}
	}
}

func capOceanDevChatFunctionTools(tools []any, limit int) []any {
	if limit <= 0 || len(tools) <= limit {
		return tools
	}
	return tools[:limit]
}

func oceanDevChatToolsContainName(tools []any, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if ok && strings.TrimSpace(stringValue(function["name"])) == name {
			return true
		}
	}
	return false
}

func oceanDevChatToolChoiceName(choice any) string {
	choiceMap, _ := choice.(map[string]any)
	if choiceMap == nil {
		return ""
	}
	function, _ := choiceMap["function"].(map[string]any)
	return strings.TrimSpace(stringValue(function["name"]))
}

// sanitizeOceanDevChatFunctionTools 只处理 Ocean Dev 回退到 Chat 的函数参数：
// 顶层必须是 object，union 要归并，嵌套 object 要补 additionalProperties:false。
func sanitizeOceanDevChatFunctionTools(tools []any) {
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		function["parameters"] = sanitizeOceanDevChatFunctionParameters(function["parameters"])
	}
}

func sanitizeOceanDevChatFunctionParameters(parameters any) any {
	schema, ok := parameters.(map[string]any)
	if !ok || len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	cloned, ok := workBuddyCloneJSON(schema).(map[string]any)
	if !ok {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	if workBuddyHasUnion(cloned) {
		fitWorkBuddyObjectRoot(cloned, cloned, 0)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
		delete(cloned, key)
	}
	cloned["type"] = "object"
	if _, ok := cloned["properties"].(map[string]any); !ok {
		cloned["properties"] = map[string]any{}
	}
	fitOceanDevStrictChatObjectSchemas(cloned, 0, true)
	return cloned
}

// fitOceanDevStrictChatObjectSchemas 递归补齐嵌套 object 的严格约束。
func fitOceanDevStrictChatObjectSchemas(value any, depth int, root bool) {
	if depth > 64 || value == nil {
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			fitOceanDevStrictChatObjectSchemas(item, depth+1, false)
		}
	case map[string]any:
		isObject := false
		if rawType, exists := typed["type"]; exists {
			if kind := oceanDevChatSchemaType(rawType); kind == "object" {
				isObject = true
			}
		} else if oceanDevChatHasObjectShape(typed) {
			isObject = true
		}
		if !root && isObject {
			oceanDevCloseChatObject(typed)
		}
		for key, item := range typed {
			switch key {
			case "properties", "$defs", "definitions":
				if entries, ok := item.(map[string]any); ok {
					for _, child := range entries {
						fitOceanDevStrictChatObjectSchemas(child, depth+1, false)
					}
				}
			default:
				fitOceanDevStrictChatObjectSchemas(item, depth+1, false)
			}
		}
	}
}

func oceanDevChatSchemaType(raw any) string {
	switch typed := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(typed))
	case []any:
		for _, item := range typed {
			if strings.EqualFold(strings.TrimSpace(stringValue(item)), "object") {
				return "object"
			}
		}
	}
	return ""
}

func oceanDevChatHasObjectShape(schema map[string]any) bool {
	if _, ok := schema["properties"].(map[string]any); ok {
		return true
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if _, ok := schema[key].([]any); ok {
			return true
		}
	}
	return false
}

func oceanDevCloseChatObject(schema map[string]any) {
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		schema["properties"] = properties
	}
	schema["type"] = "object"
	schema["additionalProperties"] = false
	required := make([]any, 0, len(properties))
	seen := map[string]bool{}
	if raw, ok := schema["required"].([]any); ok {
		for _, item := range raw {
			name, ok := item.(string)
			if !ok || seen[name] {
				continue
			}
			if _, exists := properties[name]; !exists {
				continue
			}
			seen[name] = true
			required = append(required, name)
		}
	}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		delete(schema, "required")
	}
}

// applyOceanDevChatBodyCompat 是所有发往 Ocean Chat 的统一入口。它只处理
// Ocean deployment 的报文，普通 Provider 的请求体原样返回。
func applyOceanDevChatBodyCompat(body []byte, provider domain.Provider) ([]byte, error) {
	if !isOceanDevProvider(provider) || len(body) == 0 {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, nil
	}
	model := stringValue(payload["model"])
	if strings.TrimSpace(model) == "" {
		model = provider.DefaultModel
	}
	applyOceanDevChatCompat(payload, model)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, err
	}
	return rewritten, nil
}

// normalizeOceanDevProtocolSupport 在启动加载旧库时把 Ocean Dev 曾经探出的
// Responses/Claude 能力收回到 Chat-only。探测逻辑本身已经固定只认 Chat，但旧
// 数据仍会让 UI 和调度先看到多协议；这里只改这一类 deployment Provider，
// 其他 Provider 原样保留。返回是否有变化。
func normalizeOceanDevProtocolSupport(state *domain.GatewayState) bool {
	if state == nil {
		return false
	}
	changed := false
	for i := range state.Providers {
		provider := &state.Providers[i]
		if !isOceanDevProvider(*provider) {
			continue
		}
		if len(provider.ProtocolSupport) == 1 && strings.TrimSpace(provider.ProtocolSupport[0]) == string(domain.ProtocolOpenAIChat) {
			continue
		}
		provider.ProtocolSupport = []string{string(domain.ProtocolOpenAIChat)}
		changed = true
	}
	return changed
}

// NormalizeOceanDevProtocolSupport 是 app 启动迁移使用的导出包装。
func NormalizeOceanDevProtocolSupport(state *domain.GatewayState) bool {
	return normalizeOceanDevProtocolSupport(state)
}
