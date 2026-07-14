package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatToClaudeRequestDefaultsMaxTokensToModelBudget(t *testing.T) {
	openAIReq := map[string]any{
		"model": "gpt-4o", // 客户端模型名，不得用于预算
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"max_tokens": 4096, // 客户端目录默认值，应被实际上游模型预算覆盖
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-fable-5", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claudeReq["model"] != "claude-fable-5" {
		t.Fatalf("upstream model=%#v", claudeReq["model"])
	}
	got, ok := claudeReq["max_tokens"].(int)
	if !ok {
		t.Fatalf("expected int max_tokens, got %#v", claudeReq["max_tokens"])
	}
	want := defaultClaudeMaxTokens("claude-fable-5")
	if got != want {
		t.Fatalf("max_tokens=%d want %d (must follow upstream model, not client)", got, want)
	}
	if got <= 4096 {
		t.Fatalf("default max_tokens should exceed legacy 4096 cap, got %d", got)
	}
}

func TestOpenAIChatToClaudeRequestIgnoresClientMaxTokens(t *testing.T) {
	openAIReq := map[string]any{
		"model":      "some-client-alias",
		"max_tokens": 2048,
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-fable-5", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := defaultClaudeMaxTokens("claude-fable-5")
	if claudeReq["max_tokens"] != want {
		t.Fatalf("expected upstream budget %d, got %#v", want, claudeReq["max_tokens"])
	}
}

func TestOpenAIChatToClaudeRequestPreservesCacheControl(t *testing.T) {
	openAIReq := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{
				"role": "system",
				"content": []any{
					map[string]any{
						"type":          "text",
						"text":          "cached system prefix",
						"cache_control": map[string]any{"type": "ephemeral"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":          "text",
						"text":          "hello",
						"cache_control": map[string]any{"type": "ephemeral"},
					},
				},
			},
		},
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	systemBlocks, ok := claudeReq["system"].([]any)
	if !ok {
		t.Fatalf("expected system blocks, got %#v", claudeReq["system"])
	}
	systemBlock, ok := systemBlocks[0].(map[string]any)
	if !ok || systemBlock["cache_control"] == nil {
		t.Fatalf("expected cache_control on system block: %#v", systemBlocks[0])
	}
	messages := claudeReq["messages"].([]map[string]any)
	userContent, ok := messages[0]["content"].([]any)
	if !ok {
		t.Fatalf("expected user content blocks, got %#v", messages[0]["content"])
	}
	userBlock, ok := userContent[0].(map[string]any)
	if !ok || userBlock["cache_control"] == nil {
		t.Fatalf("expected cache_control on user block: %#v", userContent[0])
	}
}

func TestOpenAIChatToClaudeRequestMapsReasoningEffortToAdaptiveThinking(t *testing.T) {
	openAIReq := map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"reasoning_effort": "low",
		"temperature":      0.5,
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	thinking, ok := claudeReq["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("expected adaptive thinking, got %#v", claudeReq["thinking"])
	}
	outputConfig, ok := claudeReq["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "low" {
		t.Fatalf("expected output_config.effort=low, got %#v", claudeReq["output_config"])
	}
	if claudeReq["temperature"] != 1 {
		t.Fatalf("expected temperature=1 with thinking, got %#v", claudeReq["temperature"])
	}
}

func TestOpenAIChatToClaudeRequestMapsReasoningEffortToBudgetTokensForLegacyModel(t *testing.T) {
	openAIReq := map[string]any{
		"model": "claude-haiku-4-5-20251001",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"reasoning_effort": "medium",
		"temperature":      0.5,
	}
	claudeReq, err := openAIChatToClaudeRequest(openAIReq, "claude-haiku-4-5-20251001", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	thinking, ok := claudeReq["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("expected enabled thinking, got %#v", claudeReq["thinking"])
	}
	if thinking["budget_tokens"] != 10000 {
		t.Fatalf("expected budget_tokens=10000, got %#v", thinking["budget_tokens"])
	}
	if claudeReq["temperature"] != 1 {
		t.Fatalf("expected temperature=1 with thinking, got %#v", claudeReq["temperature"])
	}
}

func TestClaudeResponseToOpenAIChatMapsCacheUsage(t *testing.T) {
	claudeResp := []byte(`{
		"id":"msg_test",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-5",
		"content":[{"type":"text","text":"hi"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":96}
	}`)
	body, usage, err := claudeResponseToOpenAIChat(claudeResp, "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CacheTokens != 96 || usage.InputTokens != 10+96 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid openai json: %v", err)
	}
	usageMap := payload["usage"].(map[string]any)
	if usageMap["prompt_tokens"] != float64(106) {
		t.Fatalf("expected inclusive prompt_tokens=106, got %#v", usageMap["prompt_tokens"])
	}
	if usageMap["prompt_cache_hit_tokens"] != float64(96) {
		t.Fatalf("expected prompt_cache_hit_tokens=96, got %#v", usageMap["prompt_cache_hit_tokens"])
	}
	choices := payload["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("expected finish_reason stop, got %#v", choice["finish_reason"])
	}
}

func TestStreamClaudeToOpenAIChatEvents(t *testing.T) {
	input := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[]}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":12}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	recorder := httptest.NewRecorder()
	usage, err := streamClaudeToOpenAIChatEvents(recorder, strings.NewReader(input), "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CacheTokens != 12 || usage.OutputTokens != 1 || usage.InputTokens != 3+12 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"Hi"`) {
		t.Fatalf("expected streamed content, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected DONE marker, got %q", body)
	}
}
