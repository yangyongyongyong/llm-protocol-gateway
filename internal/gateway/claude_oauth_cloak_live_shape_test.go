package gateway

import (
	"strings"
	"testing"
)

// liveClaudeCodeSystem is the system a real Claude Code 2.1.318 turn sent, as the
// request log recorded it (systemBytes 55475): the billing line, the agent
// identifier, then the client's own prompt — the environment, the CLAUDE.md
// contents, the agent loop's rules and the tool-use policy.
func liveClaudeCodeSystem() []any {
	clientPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude, running inside a terminal.",
		"# Environment\nWorking directory: /var/www/app\nIs a git repository: true\nPlatform: linux",
		"# Project instructions\n### CLAUDE.md\nRun gofmt before committing. Tests are colocated.",
		"# Doing tasks\nThe user will primarily request you to perform software engineering tasks.",
		"When you have completed a task, you MUST run the lint and typecheck commands.",
		"You are an agent - please keep going until the user's query is completely resolved.",
		"# Using your tools\nUse TodoWrite to plan. Use Read before Edit. Use Bash for shell.",
		"# Output efficiency\nGo straight to the point. Try the simplest approach first.",
	}, "\n\n")
	return []any{
		map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.318.924; cc_entrypoint=cli; cch=abcde;"},
		map[string]any{"type": "text", "text": claudeOAuthAgentIdentifier},
		map[string]any{"type": "text", "text": clientPrompt},
	}
}

func liveClaudeCodeTurn() map[string]any {
	return map[string]any{
		"model":  "claude-opus-5-5",
		"system": liveClaudeCodeSystem(),
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<system-reminder>\nCodebase and user instructions are shown below.\n</system-reminder>\n\n开始实现"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "规划一下", "signature": strings.Repeat("Sig9", 300)},
				map[string]any{"type": "text", "text": "开始实现。"},
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"file_path": "a.go"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "package a"},
			}},
		},
		"tools": []any{
			map[string]any{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"name": "Read", "description": "read", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"name": "TodoWrite", "description": "plan", "input_schema": map[string]any{"type": "object"}},
		},
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "medium"},
		"max_tokens":    64000,
	}
}

// A real Claude Code turn keeps its whole system prompt through the cloak. This is
// the repair that matters most: the canned Claude Code imitation this function used
// to substitute threw away the environment, the CLAUDE.md, the tool-use policy and
// the agent loop's "keep going until resolved", and a model without those answers
// one line — or only thinks and says nothing, the thinking-only empty turn the
// request log flagged four times on a live subscription.
func TestCloakKeepsARealClaudeCodeSystem(t *testing.T) {
	payload := liveClaudeCodeTurn()
	applyClaudeOAuthCloaking(payload)

	system := payload["system"].([]any)
	if len(system) != 3 {
		t.Fatalf("expected billing + identifier + the client prompt, got %d blocks: %#v", len(system), system)
	}
	if !claudeOAuthBillingLine.MatchString(stringValue(system[0].(map[string]any)["text"])) {
		t.Fatalf("system[0] must be the billing line alone, got %#v", system[0])
	}
	if got := stringValue(system[1].(map[string]any)["text"]); got != claudeOAuthAgentIdentifier {
		t.Fatalf("system[1] must be the agent identifier, got %q", got)
	}
	kept := stringValue(system[2].(map[string]any)["text"])
	for _, want := range []string{
		"# Environment",
		"Working directory: /var/www/app",
		"### CLAUDE.md",
		"Run gofmt before committing",
		"You are an agent - please keep going until the user's query is completely resolved.",
		"# Using your tools",
	} {
		if !strings.Contains(kept, want) {
			t.Fatalf("the client prompt lost %q after the cloak:\n%s", want, kept)
		}
	}
	// The client's own billing line is not sent twice, and neither is the
	// identifier: a duplicate costs tokens and shifts the cache prefix.
	if strings.Count(kept, "x-anthropic-billing-header:") != 0 {
		t.Fatalf("the client billing line must not be kept in its block:\n%s", kept)
	}
	if strings.Count(kept, claudeOAuthAgentIdentifier) != 0 {
		t.Fatalf("the identifier must not be duplicated:\n%s", kept)
	}
}

// The cloak leaves the conversation and its tools alone: the client's messages,
// its thinking blocks with their signatures and its tool declarations are exactly
// what makes the next turn continuable.
func TestCloakLeavesTheConversationAlone(t *testing.T) {
	payload := liveClaudeCodeTurn()
	before := payload["messages"].([]any)
	beforeTools := payload["tools"].([]any)
	applyClaudeOAuthCloaking(payload)

	after := payload["messages"].([]any)
	if len(after) != len(before) {
		t.Fatalf("expected %d messages kept, got %d", len(before), len(after))
	}
	first := stringValue(after[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
	if strings.Contains(first, "<system-reminder>\nAs you answer the user's questions, you can use the following context") {
		t.Fatalf("nothing may be prepended to the first user message, got:\n%s", first)
	}
	if !strings.Contains(first, "开始实现") {
		t.Fatalf("the user's own text must survive, got:\n%s", first)
	}
	// The assistant turn keeps its thinking signature (the next turn replays it)
	// and its tool call.
	blocks := after[1].(map[string]any)["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected the assistant turn untouched, got %#v", blocks)
	}
	if stringValue(blocks[0].(map[string]any)["signature"]) == "" {
		t.Fatal("the thinking signature must be kept for the next turn")
	}
	if got := len(payload["tools"].([]any)); got != len(beforeTools) {
		t.Fatalf("expected %d tools kept, got %d", len(beforeTools), got)
	}
	for _, raw := range payload["tools"].([]any) {
		if raw.(map[string]any)["cache_control"] == nil && stringValue(raw.(map[string]any)["name"]) == "TodoWrite" {
			t.Fatal("expected a cache mark on the last tool")
		}
	}
}

// Signing the billing header still works on a kept system: the cch is computed
// over the body as it now stands.
//
// A system that already holds the billing line and the identifier in that order
// is left as the client wrote it — the client's own cc_version is the truer one —
// and only the cch is filled in.
func TestCloakBillingSignsOverTheKeptSystem(t *testing.T) {
	payload := liveClaudeCodeTurn()
	applyClaudeOAuthCloaking(payload)
	unsigned, err := marshalClaudeOAuthBody(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signed := signClaudeOAuthCCH(unsigned)
	if strings.Contains(string(signed), "cch=00000;") {
		t.Fatalf("expected a signed cch, got %s", signed)
	}
	value := claudeOAuthBillingHTTPHeaderValue(signed)
	if !strings.Contains(value, "cc_version=") || !strings.Contains(value, "cc_entrypoint=") {
		t.Fatalf("expected the billing header value, got %q", value)
	}
	if _, cch, ok := strings.Cut(value, "cch="); !ok || cch == "00000;" || cch == "" {
		t.Fatalf("expected a signed cch in the billing header value, got %q", value)
	}
}
