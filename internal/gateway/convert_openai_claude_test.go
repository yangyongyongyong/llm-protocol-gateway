package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeRequestToOpenAIChat(t *testing.T) {
	claudeReq := map[string]any{
		"model":      "deepseek-v4-flash",
		"max_tokens": 128,
		"system":     "你是数学老师",
		"messages": []any{
			map[string]any{"role": "user", "content": "1+1等于几"},
		},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := openAIReq["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected system message first")
	}
	if openAIReq["max_tokens"] != 128 {
		t.Fatalf("expected max_tokens preserved for non-gpt5 model, got %#v", openAIReq["max_tokens"])
	}
	if _, exists := openAIReq["max_completion_tokens"]; exists {
		t.Fatalf("did not expect max_completion_tokens for deepseek")
	}
}

func TestClaudeRequestToOpenAIChatRewritesMaxTokensForGPT5(t *testing.T) {
	claudeReq := map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": 64,
		"messages": []any{
			map[string]any{"role": "user", "content": "1+1"},
		},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "gpt-5.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := openAIReq["max_tokens"]; exists {
		t.Fatalf("expected max_tokens removed for gpt-5.5")
	}
	if openAIReq["max_completion_tokens"] != 64 {
		t.Fatalf("expected max_completion_tokens=64, got %#v", openAIReq["max_completion_tokens"])
	}
}

func TestClaudeRequestToOpenAIChatPreservesCacheControl(t *testing.T) {
	claudeReq := map[string]any{
		"model": "claude-sonnet-5",
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          "cached system prefix",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"messages": []any{
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
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := openAIReq["messages"].([]any)
	systemMessage, _ := messages[0].(map[string]any)
	systemContent, ok := systemMessage["content"].([]any)
	if !ok {
		t.Fatalf("expected system content blocks, got %#v", systemMessage["content"])
	}
	systemBlock, ok := systemContent[0].(map[string]any)
	if !ok || systemBlock["cache_control"] == nil {
		t.Fatalf("expected cache_control on system block: %#v", systemContent[0])
	}
	userMessage, _ := messages[1].(map[string]any)
	userContent, ok := userMessage["content"].([]any)
	if !ok {
		t.Fatalf("expected user content blocks, got %#v", userMessage["content"])
	}
	userBlock, ok := userContent[0].(map[string]any)
	if !ok || userBlock["cache_control"] == nil {
		t.Fatalf("expected cache_control on user block: %#v", userContent[0])
	}
}

// 回归：2026-07-24 — Claude→GLM failover 把 type=image 原样带给 Chat 上游 → 1214 type error
func TestClaudeRequestToOpenAIChatRewritesImageBlocks(t *testing.T) {
	claudeReq := map[string]any{
		"model": "claude-opus-4-8",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "what is in this image?"},
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       "abc123",
						},
					},
					map[string]any{"type": "thinking", "thinking": "secret", "signature": "sig"},
				},
			},
		},
	}
	openAIReq, err := claudeRequestToOpenAIChat(claudeReq, "glm-5.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := openAIReq["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	user, _ := messages[0].(map[string]any)
	content, ok := user["content"].([]any)
	if !ok {
		t.Fatalf("expected multimodal content array, got %#v", user["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected text+image_url only, got %#v", content)
	}
	textBlock, _ := content[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Fatalf("first block type=%#v", textBlock["type"])
	}
	imageBlock, _ := content[1].(map[string]any)
	if imageBlock["type"] != "image_url" {
		t.Fatalf("image must become image_url, got %#v", imageBlock)
	}
	imageURL, _ := imageBlock["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,abc123" {
		t.Fatalf("unexpected image url %#v", imageURL["url"])
	}
	raw, _ := json.Marshal(openAIReq)
	if strings.Contains(string(raw), `"type":"thinking"`) || strings.Contains(string(raw), `"type":"image","source"`) {
		t.Fatalf("converted body still contains Claude-only types: %s", raw)
	}
}

func TestOpenAIChatResponseToClaudeMapsCacheUsage(t *testing.T) {
	openAIResp := []byte(`{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"model":"deepseek-v4-flash",
		"choices":[{"index":0,"message":{"role":"assistant","content":"2"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":96,"prompt_cache_miss_tokens":4}
	}`)
	body, usage, err := openAIChatResponseToClaude(openAIResp, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CacheTokens != 96 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid claude json: %v", err)
	}
	usageMap := payload["usage"].(map[string]any)
	if usageMap["cache_read_input_tokens"] != float64(96) {
		t.Fatalf("expected cache_read_input_tokens=96, got %#v", usageMap["cache_read_input_tokens"])
	}
}

func TestOpenAIChatResponseToClaude(t *testing.T) {
	openAIResp := []byte(`{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"model":"deepseek-v4-flash",
		"choices":[{"index":0,"message":{"role":"assistant","content":"2"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
	}`)
	body, usage, err := openAIChatResponseToClaude(openAIResp, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 2 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid claude json: %v", err)
	}
	if payload["type"] != "message" {
		t.Fatalf("expected claude message response")
	}
}
