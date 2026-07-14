package gateway

import (
	"encoding/json"
	"testing"
)

func TestShouldRectifyThinkingSignature(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "invalid signature in thinking block",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`,
			want: true,
		},
		{
			name: "no backticks lowercased",
			body: `Messages.1.Content.0: invalid signature in thinking block`,
			want: true,
		},
		{
			name: "nested json wrapper",
			body: `{"error":{"message":"{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"x.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block\"}}"}}`,
			want: true,
		},
		{
			name: "must start with thinking block",
			body: `a final ` + "`assistant`" + ` message must start with a thinking block`,
			want: true,
		},
		{
			name: "expected thinking found tool_use",
			body: "messages.69.content.0.type: Expected `thinking` or `redacted_thinking`, but found `tool_use`.",
			want: true,
		},
		{
			name: "signature field required",
			body: `x.x.x.signature: Field required`,
			want: true,
		},
		{
			name: "unrelated timeout",
			body: `{"error":{"message":"request timeout"}}`,
			want: false,
		},
		{
			name: "unrelated rate limit",
			body: `{"error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`,
			want: false,
		},
		{
			name: "empty",
			body: ``,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRectifyThinkingSignature([]byte(tc.body)); got != tc.want {
				t.Fatalf("shouldRectifyThinkingSignature(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestRectifyClaudeThinkingRequestRemovesBlocks(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"draft","signature":"sig1"},
				{"type":"text","text":"answer","signature":"sig_text"},
				{"type":"tool_use","id":"toolu_1","name":"Read","input":{},"signature":"sig_tool"},
				{"type":"redacted_thinking","data":"r","signature":"sig_red"}
			]}
		]
	}`)
	rectified, result := rectifyClaudeThinkingBody(body)
	if !result.applied {
		t.Fatal("expected applied=true")
	}
	if result.removedThinkingBlocks != 1 {
		t.Fatalf("removedThinkingBlocks = %d, want 1", result.removedThinkingBlocks)
	}
	if result.removedRedactedThinkingBlocks != 1 {
		t.Fatalf("removedRedactedThinkingBlocks = %d, want 1", result.removedRedactedThinkingBlocks)
	}
	if result.removedSignatureFields != 2 {
		t.Fatalf("removedSignatureFields = %d, want 2", result.removedSignatureFields)
	}

	var parsed map[string]any
	if err := json.Unmarshal(rectified, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	messages := parsed["messages"].([]any)
	assistant := messages[1].(map[string]any)
	content := assistant["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content len = %d, want 2", len(content))
	}
	// Remaining blocks must be text + tool_use, both without signature.
	for _, item := range content {
		block := item.(map[string]any)
		if _, has := block["signature"]; has {
			t.Fatalf("residual signature on block type %v", block["type"])
		}
		typ := block["type"].(string)
		if typ != "text" && typ != "tool_use" {
			t.Fatalf("unexpected surviving block type %q", typ)
		}
	}
}

func TestRectifyRemovesTopLevelThinkingWhenPrefixMissing(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}
			]}
		]
	}`)
	rectified, result := rectifyClaudeThinkingBody(body)
	if !result.applied {
		t.Fatal("expected applied=true")
	}
	if !result.removedTopLevelThinking {
		t.Fatal("expected removedTopLevelThinking=true")
	}
	var parsed map[string]any
	if err := json.Unmarshal(rectified, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := parsed["thinking"]; has {
		t.Fatal("top-level thinking should be removed")
	}
}

func TestRectifyKeepsTopLevelThinkingWhenPrefixPresent(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"thinking":{"type":"enabled"},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"s"},
				{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}
			]}
		]
	}`)
	// After stripping the thinking block, the first block becomes tool_use, so
	// top-level thinking must also be removed to avoid the "must start with a
	// thinking block" error. This mirrors cc-switch behavior.
	rectified, result := rectifyClaudeThinkingBody(body)
	if !result.applied {
		t.Fatal("expected applied=true")
	}
	if result.removedThinkingBlocks != 1 {
		t.Fatalf("removedThinkingBlocks = %d, want 1", result.removedThinkingBlocks)
	}
	if !result.removedTopLevelThinking {
		t.Fatal("expected removedTopLevelThinking=true after thinking block stripped")
	}
	var parsed map[string]any
	_ = json.Unmarshal(rectified, &parsed)
	if _, has := parsed["thinking"]; has {
		t.Fatal("top-level thinking should be removed")
	}
}

func TestRectifyNoChangeWhenClean(t *testing.T) {
	body := []byte(`{"model":"c","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	rectified, result := rectifyClaudeThinkingBody(body)
	if result.applied {
		t.Fatal("expected applied=false for clean body")
	}
	if string(rectified) != string(body) {
		t.Fatal("clean body should be returned unchanged")
	}
}

func TestRectifyStringContentUnaffected(t *testing.T) {
	body := []byte(`{"model":"c","messages":[{"role":"user","content":"plain string"}]}`)
	_, result := rectifyClaudeThinkingBody(body)
	if result.applied {
		t.Fatal("string content has no thinking blocks; applied should be false")
	}
}
