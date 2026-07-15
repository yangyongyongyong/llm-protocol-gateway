package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadTestdataJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return payload
}

// 回归：2026-07-15 17:10:44 — web_search_20250305 被误注入 input_schema → Anthropic 400
func TestLogReplayWebSearchPassThroughDoesNotInjectInputSchema(t *testing.T) {
	payload := loadTestdataJSON(t, "bug_web_search_400.json")
	tools, _ := payload["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("fixture missing tools")
	}
	before, _ := tools[0].(map[string]any)
	if stringValue(before["type"]) != "web_search_20250305" {
		t.Fatalf("fixture tool type=%#v", before["type"])
	}
	if _, exists := before["input_schema"]; exists {
		t.Fatalf("fixture should not already contain input_schema: %#v", before)
	}

	normalizeClaudePassThroughPayload(payload)

	outTools, _ := payload["tools"].([]any)
	if len(outTools) == 0 {
		t.Fatal("normalized tools empty")
	}
	web := outTools[0].(map[string]any)
	if web["type"] != "web_search_20250305" {
		t.Fatalf("type=%#v", web["type"])
	}
	if web["name"] != "web_search" {
		t.Fatalf("name=%#v", web["name"])
	}
	if web["max_uses"] != float64(8) && web["max_uses"] != 8 {
		t.Fatalf("max_uses=%#v want 8", web["max_uses"])
	}
	if _, exists := web["input_schema"]; exists {
		t.Fatalf("BUG NOT FIXED: server tool still has input_schema: %#v", web)
	}
}

// 回归：2026-07-15 16:40:42 — Claude→Chat 把 thinking 块带给 GLM → 1214 type error
func TestLogReplayThinkingStrippedInClaudeToChat(t *testing.T) {
	payload := loadTestdataJSON(t, "bug_thinking_type_400.json")
	chatReq, err := claudeRequestToOpenAIChat(payload, "glm-5.2")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	msgs, _ := chatReq["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("no converted messages")
	}

	thinkingHits := 0
	arrayContentMsgs := 0
	for i, item := range msgs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		arrayContentMsgs++
		for j, block := range content {
			bm, ok := block.(map[string]any)
			if !ok {
				continue
			}
			typ := stringValue(bm["type"])
			if typ == "thinking" || typ == "redacted_thinking" {
				thinkingHits++
				t.Errorf("messages[%d].content[%d] still has type=%s", i, j, typ)
			}
		}
	}
	if thinkingHits > 0 {
		t.Fatalf("BUG NOT FIXED: %d thinking blocks remain in Chat messages (array content msgs=%d)", thinkingHits, arrayContentMsgs)
	}

	// 合成最小复现：无 tool_use 的 thinking+text 必须变成纯 text 字符串
	mini := map[string]any{
		"model": "glm-5.2",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "secret plan", "signature": "sig"},
					map[string]any{"type": "text", "text": "hello"},
				},
			},
			map[string]any{"role": "user", "content": "continue"},
		},
	}
	chatMini, err := claudeRequestToOpenAIChat(mini, "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	miniMsgs := chatMini["messages"].([]any)
	first := miniMsgs[0].(map[string]any)
	if first["content"] != "hello" {
		t.Fatalf("expected thinking dropped to plain text, got %#v", first["content"])
	}
}
