package gateway

import (
	"strings"
	"testing"
)

// The effort a client asks for must reach the upstream as asked. magpie, the
// reference for this protocol, never lowers the effort for the always-adaptive
// Claude models: its per-model table lists claude-opus-5-5 as
// "low medium high xhigh max" (internal/agent/claude_effort_test.go:119), and
// adaptive_test.go pins buildAnthropic({Thinking:true, Effort:"xhigh"}) to
// output_config {"effort":"xhigh"}.
func TestClaudeEffortLevelsFollowTheReferenceTable(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":           "low medium high xhigh max",
		"claude-sonnet-5-5":         "low medium high xhigh max",
		"claude-fable-5-1":          "low medium high xhigh max",
		"claude-opus-5":             "low medium high xhigh max",
		"claude-opus-4-7":           "low medium high xhigh max",
		"claude-opus-4-6":           "low medium high xhigh max",
		"claude-sonnet-4-6":         "low medium high xhigh max",
		"claude-opus-4-5-20251101":  "low medium high xhigh max",
		"claude-haiku-4-5-20251001": "low medium high xhigh max",
		"claude-opus-4-8":           "low medium high xhigh max",
		"anthropic/claude-opus-5-5": "low medium high xhigh max",
	}
	for model, want := range cases {
		got := strings.Join(modelEffortLevels(model), " ")
		if got != want {
			t.Errorf("%s: levels = %q, want %q", model, got, want)
		}
	}
}

// And fitModelEffort therefore keeps max: the key-override path writes the depth
// gated by this, so a ceiling here would silently downgrade every turn.
func TestFitModelEffortKeepsMaxForAdaptiveClaude(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5"} {
		for _, effort := range []string{"max", "xhigh"} {
			if got := fitModelEffort(effort, model); got != effort {
				t.Errorf("fitModelEffort(%q, %s) = %q, want it unchanged", effort, model, got)
			}
		}
	}
}

// The clamp is now opt-in. Left alone, xhigh and max survive for the models that
// used to be capped.
func TestClampClaudeAdaptiveEffortIsOffByDefault(t *testing.T) {
	t.Setenv(claudeClampAdaptiveEffortEnv, "")
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5", "claude-mythos-5"} {
		for _, effort := range []string{"max", "xhigh", "MAX"} {
			payload := map[string]any{
				"thinking":      map[string]any{"type": "adaptive"},
				"output_config": map[string]any{"effort": effort},
			}
			if clampClaudeAdaptiveEffort(payload, model) {
				t.Fatalf("%s at %q must not be clamped by default", model, effort)
			}
			if got := outputConfigEffort(payload); !strings.EqualFold(got, effort) {
				t.Fatalf("%s at %q became %q", model, effort, got)
			}
		}
	}
}

// The old behaviour stays reachable without a rebuild, for the day an upstream
// starts answering these efforts with empty turns again.
func TestClampClaudeAdaptiveEffortCanBeReEnabled(t *testing.T) {
	t.Setenv(claudeClampAdaptiveEffortEnv, "1")
	payload := map[string]any{
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "max"},
	}
	if !clampClaudeAdaptiveEffort(payload, "claude-opus-5-5") {
		t.Fatal("with the lever on, opus-5-5 at max should be clamped")
	}
	if got := outputConfigEffort(payload); got != "high" {
		t.Fatalf("clamped to %q, want high", got)
	}
	// Models outside the always-adaptive group are untouched either way.
	other := map[string]any{"output_config": map[string]any{"effort": "max"}}
	if clampClaudeAdaptiveEffort(other, "claude-sonnet-5") {
		t.Fatal("sonnet-5 at max must not be clamped")
	}
}

// The native pass-through is what Claude Code actually hits, so the effort has to
// survive normalization there — including with the key override at max.
func TestNormalizeClaudePassThroughKeepsRequestedEffort(t *testing.T) {
	t.Setenv(claudeClampAdaptiveEffortEnv, "")
	payload := map[string]any{
		"model":         "claude-opus-5-5",
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "max"},
		"max_tokens":    64000,
	}
	normalizeClaudePassThroughPayload(payload)
	if got := outputConfigEffort(payload); got != "max" {
		t.Fatalf("effort = %q, want max to reach the upstream", got)
	}
	if got := stringValue(payload["thinking"].(map[string]any)["type"]); got != "adaptive" {
		t.Fatalf("thinking = %q, want adaptive kept", got)
	}
}

// Turning thinking off is the one case where the effort is still reconciled, and
// that behaviour is unchanged: opus-5-5 takes no thinking, so the field goes and
// an unspecified effort becomes low (magpie's offNever branch, anthropic.go:663).
func TestNormalizeClaudeThinkingOffStillReconcilesDisabled(t *testing.T) {
	payload := map[string]any{
		"model":      "claude-opus-5-5",
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":   map[string]any{"type": "disabled"},
		"max_tokens": 64,
	}
	normalizeClaudePassThroughPayload(payload)
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("thinking should be dropped for opus-5-5, got %#v", payload["thinking"])
	}
	if got := outputConfigEffort(payload); got != "low" {
		t.Fatalf("effort = %q, want low when none was asked", got)
	}
	// The classifier-sized budget gets room for a model that always thinks.
	if n, _ := intFromAny(payload["max_tokens"]); n < classifierRoom {
		t.Fatalf("max_tokens = %d, want classifierRoom added", n)
	}
}
