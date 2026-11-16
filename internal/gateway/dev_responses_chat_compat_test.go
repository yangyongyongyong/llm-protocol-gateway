package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const oceanDevTestBaseURL = "http://ocean-dev.tuya-in.net/openai/deployments/{model}/chat/completions?api-version=2024-02-01"

func TestIsOceanDevProviderOnlyMatchesChatDeployment(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"ocean chat deployment", oceanDevTestBaseURL, true},
		{"other host", "https://relay.example/openai/deployments/{model}/chat/completions", false},
		{"ocean bare v1", "http://ocean-dev.tuya-in.net/openai/v1", false},
		{"ocean responses deployment", "http://ocean-dev.tuya-in.net/openai/deployments/{model}/responses", false},
	}
	for _, tc := range cases {
		if got := isOceanDevProvider(domain.Provider{BaseURL: tc.url}); got != tc.want {
			t.Errorf("%s: isOceanDevProvider = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Ocean 的 Chat deployment 不接受 gpt-5.4-pro / gpt-5.5 的 max 档；只在
// Ocean 专属整流里降为 xhigh，通用转换器不受影响。
func TestOceanDevChatEffortCompat(t *testing.T) {
	for _, tc := range []struct {
		model, want, expected string
	}{
		{"gpt-5.5", "max", "xhigh"},
		{"gpt-5.4-pro", "max", "xhigh"},
		{"gpt-5.4-pro", "low", "medium"},
		{"gpt-5.5", "minimal", "low"},
		{"gpt-5.5", "medium", "medium"},
		{"gpt-4o", "max", "max"},
	} {
		req := map[string]any{"reasoning_effort": tc.want}
		applyOceanDevChatEffort(req, tc.model)
		if got := req["reasoning_effort"]; got != tc.expected {
			t.Errorf("%s effort %s = %#v, want %s", tc.model, tc.want, got, tc.expected)
		}
	}
}

// 非 Ocean Provider 的通用转换结果必须保持基线：用户提示里的 tool schema
// 不清洗、超量工具不裁剪、长 call id 不缩短。
func TestResponsesToChatGenericPathStaysUntouched(t *testing.T) {
	longID := strings.Repeat("call_", 32)
	children := make([]any, 0, 160)
	for i := 0; i < 160; i++ {
		children = append(children, map[string]any{
			"type": "function", "name": fmt.Sprintf("tool_%03d", i),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"nested": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
				},
				"oneOf": []any{map[string]any{"required": []any{"x"}}},
			},
		})
	}
	responsesReq := map[string]any{
		"model": "default[1m]", "input": []any{
			map[string]any{"type": "function_call", "call_id": longID, "name": "exec_command", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": longID, "output": "ok"},
		},
		"reasoning": map[string]any{"effort": "max"},
		"tools":     []any{map[string]any{"type": "namespace", "name": "mcp__large", "tools": children}},
	}
	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5.5")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	tools, _ := chatReq["tools"].([]any)
	if len(tools) != 160 {
		t.Fatalf("generic tools = %d, want 160 (no Ocean cap)", len(tools))
	}
	first, _ := tools[0].(map[string]any)
	fn, _ := first["function"].(map[string]any)
	schema, _ := fn["parameters"].(map[string]any)
	if _, exists := schema["oneOf"]; !exists {
		t.Fatal("generic top-level union was removed")
	}
	messages, _ := chatReq["messages"].([]any)
	assistant, _ := messages[0].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	if got := stringValue(call["id"]); got != longID {
		t.Fatalf("generic call id = %q, want untouched", got)
	}
	if got := chatReq["reasoning_effort"]; got != "max" {
		t.Fatalf("generic effort = %#v, want max", got)
	}
}

// Ocean 的 Chat 路径才做严格整流：工具裁剪、union 归并、嵌套 additionalProperties、
// 长 id 缩短与 tool_choice 退化都要同时覆盖。
func TestOceanDevChatCompatSanitizesStrictTools(t *testing.T) {
	longID := strings.Repeat("call_", 32)
	parameters := map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"oneOf":      []any{map[string]any{"$ref": "#/$defs/view"}, map[string]any{"$ref": "#/$defs/delete"}},
		"$defs": map[string]any{
			"view": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
					"functions": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": []any{"object", "null"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
					},
				},
				"required": []any{"id", "functions"},
			},
			"delete": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}},
		},
	}
	chatReq := map[string]any{
		"model":            "gpt-5.5",
		"reasoning_effort": "max",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": longID, "type": "function", "function": map[string]any{"name": "mcp__rea__build_call_path", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": longID, "content": "ok"},
		},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "mcp__rea__build_call_path", "parameters": parameters}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "mcp__rea__build_call_path"}},
	}
	applyOceanDevChatCompat(chatReq, "gpt-5.5")
	if got := chatReq["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("effort = %#v", got)
	}
	tools, _ := chatReq["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	schema, _ := fn["parameters"].(map[string]any)
	if _, exists := schema["oneOf"]; exists {
		t.Fatal("Ocean top-level oneOf survived")
	}
	props, _ := schema["properties"].(map[string]any)
	functions, _ := props["functions"].(map[string]any)
	items, _ := functions["items"].(map[string]any)
	if got := items["additionalProperties"]; got != false {
		t.Fatalf("nested items additionalProperties = %#v, want false", got)
	}
	messages, _ := chatReq["messages"].([]any)
	assistant, _ := messages[0].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	callID := stringValue(call["id"])
	result, _ := messages[1].(map[string]any)
	if len(callID) > 64 || callID != stringValue(result["tool_call_id"]) {
		t.Fatalf("call IDs not shortened/paired: call=%q result=%q", callID, result["tool_call_id"])
	}
}

// 最新日志里的实际形状：properties.functions.items.type 是 ["object","null"]，
// 严格 Chat 要求这个 object 分支补 additionalProperties:false。
func TestOceanDevNestedUnionObjectGetsAdditionalProperties(t *testing.T) {
	items := map[string]any{
		"type":       []any{"object", "null"},
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	}
	root := map[string]any{"type": "object", "properties": map[string]any{"functions": map[string]any{"type": "array", "items": items}}}
	got := sanitizeOceanDevChatFunctionParameters(root).(map[string]any)
	props, _ := got["properties"].(map[string]any)
	functions, _ := props["functions"].(map[string]any)
	itemsOut, _ := functions["items"].(map[string]any)
	if gotType := oceanDevChatSchemaType(itemsOut["type"]); gotType != "object" {
		t.Fatalf("items type = %#v", itemsOut["type"])
	}
	if itemsOut["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", itemsOut["additionalProperties"])
	}
}

// Provider 对话测试给 gpt-5.5 带输出上限时，必须用 max_completion_tokens；
// dev 的 Chat deployment 对旧字段直接 400。这里只验证测试请求体整形，不改变
// 主协议转换流程。
func TestProviderChatTestUsesMaxCompletionTokensForGPT55(t *testing.T) {
	payload := buildProviderChatPayload("gpt-5.5", []map[string]any{{"role": "user", "content": "ok"}}, false)
	payload["max_tokens"] = 32
	normalizeOpenAIChatMaxTokensField(payload, "gpt-5.5")
	if _, exists := payload["max_tokens"]; exists {
		t.Fatalf("max_tokens should be rewritten: %#v", payload)
	}
	if got := payload["max_completion_tokens"]; got != 32 {
		t.Fatalf("max_completion_tokens = %#v, want 32", got)
	}
}

// Ocean Dev 的旧协议探测结论在启动加载时应收敛为 Chat-only；普通 Provider
// 的多协议结论不能被这条迁移碰到。
func TestNormalizeOceanDevProtocolSupport(t *testing.T) {
	state := domain.GatewayState{Providers: []domain.Provider{
		{ID: "dev", BaseURL: oceanDevTestBaseURL, ProtocolSupport: []string{"openai_chat", "openai_responses", "claude"}},
		{ID: "dev-2", BaseURL: oceanDevTestBaseURL, ProtocolSupport: []string{"claude"}},
		{ID: "multi", BaseURL: "https://relay.example/v1/chat/completions", ProtocolSupport: []string{"openai_chat", "claude"}},
	}}
	if !normalizeOceanDevProtocolSupport(&state) {
		t.Fatal("expected a migration")
	}
	for _, id := range []string{"dev", "dev-2"} {
		for _, provider := range state.Providers {
			if provider.ID != id {
				continue
			}
			if len(provider.ProtocolSupport) != 1 || provider.ProtocolSupport[0] != "openai_chat" {
				t.Fatalf("%s protocol support = %v", id, provider.ProtocolSupport)
			}
		}
	}
	for _, provider := range state.Providers {
		if provider.ID == "multi" && (len(provider.ProtocolSupport) != 2 || provider.ProtocolSupport[1] != "claude") {
			t.Fatalf("non-Ocean support changed: %v", provider.ProtocolSupport)
		}
	}
	if normalizeOceanDevProtocolSupport(&state) {
		t.Fatal("second normalization should be a no-op")
	}
}

// Ocean Dev 只按 Chat 使用：Responses 客户端必须直接进平台转换，不能再尝试
// 原生 Responses 直通，否则会重新触发 Ocean 自己的 max effort 等参数限制。
func TestOceanDevSkipsNativeResponsesPassThrough(t *testing.T) {
	provider := domain.Provider{Protocol: domain.ProtocolOpenAIChat, BaseURL: oceanDevTestBaseURL, ProtocolSupport: []string{"openai_chat", "openai_responses"}}
	if protocol, ok := nativeProtocolForProvider(provider, domain.ProtocolOpenAIResponses); ok {
		t.Fatalf("Ocean Dev native Responses unexpectedly allowed: %s", protocol)
	}
	// 普通多协议 Provider 不能被误伤。
	regular := domain.Provider{Protocol: domain.ProtocolOpenAIChat, AuthType: domain.AuthTypeAPIKey, BaseURL: "https://relay.example/v1/chat/completions", ProtocolSupport: []string{"openai_chat", "openai_responses"}}
	if protocol, ok := nativeProtocolForProvider(regular, domain.ProtocolOpenAIResponses); !ok || protocol != domain.ProtocolOpenAIResponses {
		t.Fatalf("regular provider native Responses = %q, %v", protocol, ok)
	}
}
