package gateway

import "testing"

func TestResolveClaudeModelAlias(t *testing.T) {
	if got := resolveClaudeModelAlias("sonnet"); got != "claude-sonnet-5" {
		t.Fatalf("expected claude-sonnet-5, got %q", got)
	}
	if got := resolveClaudeModelAlias("opus"); got != "claude-opus-5" {
		t.Fatalf("expected claude-opus-5, got %q", got)
	}
	if got := resolveClaudeModelAlias("claude-sonnet-5"); got != "claude-sonnet-5" {
		t.Fatalf("expected unchanged model id, got %q", got)
	}
	if got := resolveClaudeModelAlias("claude-opus-5"); got != "claude-opus-5" {
		t.Fatalf("expected unchanged model id, got %q", got)
	}
}
