package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesToClaudeRequestDirectBasic(t *testing.T) {
	responsesReq := map[string]any{
		"model":        "claude-sonnet-5",
		"instructions": "You are helpful.",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
		"max_output_tokens": float64(1024),
		"reasoning":         map[string]any{"effort": "high"},
	}
	claudeReq, err := responsesToClaudeRequestDirect(responsesReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if claudeReq["system"] != "You are helpful." {
		t.Fatalf("system not carried: %v", claudeReq["system"])
	}
	msgs, ok := claudeReq["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages wrong: %#v", claudeReq["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("first role not user: %v", first["role"])
	}
	if claudeReq["thinking"] == nil {
		t.Fatalf("thinking should be set for sonnet-5 with high effort")
	}
}

func TestClaudeToResponsesDirectThinkingRoundTrip(t *testing.T) {
	claudeResp := map[string]any{
		"id":    "msg_123",
		"model": "claude-sonnet-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "let me think", "signature": "sig-abc"},
			map[string]any{"type": "text", "text": "hello"},
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "ExecCommand", "input": map[string]any{"cmd": "ls"}},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": float64(10), "output_tokens": float64(5)},
	}
	body, _ := json.Marshal(claudeResp)
	clientTools := map[string]struct{}{"exec_command": {}}
	out, _, err := claudeToResponsesResponseDirect(body, "claude-sonnet-5", clientTools)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output := got["output"].([]any)
	if len(output) < 3 {
		t.Fatalf("expected 3 output items, got %d: %s", len(output), out)
	}
	// item 0: reasoning with encrypted_content
	reasoning := output[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Fatalf("item0 not reasoning: %v", reasoning["type"])
	}
	enc, _ := reasoning["encrypted_content"].(string)
	if enc == "" {
		t.Fatalf("reasoning missing encrypted_content")
	}
	// Round-trip: decode should restore the signed block.
	block, ok := decodeAnthropicThinkingBlock(enc)
	if !ok || block["signature"] != "sig-abc" {
		t.Fatalf("encrypted_content round-trip failed: %#v", block)
	}
	// Feed the reasoning item back into a request and confirm the signed block replays.
	replayReq := map[string]any{
		"model": "claude-sonnet-5",
		"input": []any{reasoning, map[string]any{"type": "message", "role": "user", "content": "next"}},
	}
	claudeReq, err := responsesToClaudeRequestDirect(replayReq, "claude-sonnet-5", 0)
	if err != nil {
		t.Fatalf("replay convert: %v", err)
	}
	replayMsgs := claudeReq["messages"].([]any)
	foundThinking := false
	for _, m := range replayMsgs {
		msg := m.(map[string]any)
		if blocks, ok := msg["content"].([]any); ok {
			for _, b := range blocks {
				if bm, ok := b.(map[string]any); ok && bm["type"] == "thinking" {
					foundThinking = true
				}
			}
		}
	}
	if !foundThinking {
		t.Fatalf("signed thinking block did not replay into claude request: %s", mustJSON(claudeReq))
	}

	// tool_use item should restore the client tool name.
	last := output[len(output)-1].(map[string]any)
	if last["type"] != "function_call" || last["name"] != "exec_command" {
		t.Fatalf("tool name not restored: %#v", last)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestClaudeToResponsesRequestDirectBasic(t *testing.T) {
	claudeReq := map[string]any{
		"model":      "gpt-5",
		"system":     "Be concise.",
		"max_tokens": float64(512),
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu_1", "name": "lookup", "input": map[string]any{"q": "x"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "ok"},
			}},
		},
		"tools": []any{
			map[string]any{
				"name":         "lookup",
				"description":  "look things up",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4096)},
	}
	responsesReq, err := claudeToResponsesRequestDirect(claudeReq, "gpt-5")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if responsesReq["instructions"] != "Be concise." {
		t.Fatalf("instructions: %v", responsesReq["instructions"])
	}
	input := responsesReq["input"].([]any)
	if len(input) < 3 {
		t.Fatalf("input too short: %s", mustJSON(responsesReq))
	}
	foundCall, foundOutput := false, false
	for _, item := range input {
		m := item.(map[string]any)
		switch m["type"] {
		case "function_call":
			foundCall = true
		case "function_call_output":
			foundOutput = true
		}
	}
	if !foundCall || !foundOutput {
		t.Fatalf("missing tool items: %s", mustJSON(responsesReq))
	}
	if responsesReq["tools"] == nil {
		t.Fatalf("tools not carried")
	}
	reasoning, _ := responsesReq["reasoning"].(map[string]any)
	if normalizeReasoningEffort(stringValue(reasoning["effort"])) != "low" {
		t.Fatalf("effort mapping wrong: %#v", reasoning)
	}
}

func TestResponsesToClaudeResponseDirectRoundTrip(t *testing.T) {
	claudeResp := map[string]any{
		"id":    "msg_abc",
		"model": "claude-sonnet-5",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "plan", "signature": "sig-1"},
			map[string]any{"type": "text", "text": "done"},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": float64(3), "output_tokens": float64(2)},
	}
	body, _ := json.Marshal(claudeResp)
	responsesBody, _, err := claudeToResponsesResponseDirect(body, "claude-sonnet-5", nil)
	if err != nil {
		t.Fatalf("claude→responses: %v", err)
	}
	back, _, err := responsesToClaudeResponseDirect(responsesBody, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("responses→claude: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(back, &got)
	content := got["content"].([]any)
	foundThinking, foundText := false, false
	for _, raw := range content {
		block := raw.(map[string]any)
		switch block["type"] {
		case "thinking", "redacted_thinking":
			foundThinking = true
			if block["signature"] != "sig-1" {
				t.Fatalf("signature lost: %#v", block)
			}
		case "text":
			foundText = strings.Contains(stringValue(block["text"]), "done")
		}
	}
	if !foundThinking || !foundText {
		t.Fatalf("round-trip content wrong: %s", back)
	}
}
