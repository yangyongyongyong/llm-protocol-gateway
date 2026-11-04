package gateway

import "testing"

// An always-thinking model asked for max spends its whole output budget on
// thinking and answers nothing: the turn closes with no visible output and the
// client takes silence as the answer. A key overriding the depth to "max" hit
// this on every turn.
func TestClampClaudeAdaptiveEffortLowersNeverThinkingModels(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5", "claude-mythos-5"} {
		for _, effort := range []string{"max", "xhigh", "MAX"} {
			payload := map[string]any{
				"thinking":      map[string]any{"type": "adaptive"},
				"output_config": map[string]any{"effort": effort},
			}
			if !clampClaudeAdaptiveEffort(payload, model) {
				t.Fatalf("%s at %q should be clamped", model, effort)
			}
			if got := outputConfigEffort(payload); got != "high" {
				t.Fatalf("%s at %q clamped to %q, want high", model, effort, got)
			}
		}
	}
}

// A model that accepts "disabled" up to a modest effort is not this case: an
// adaptive request at xhigh is legal for it and must be left alone.
func TestClampClaudeAdaptiveEffortLeavesOtherGenerations(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":    "xhigh", // disabled only up to high; adaptive xhigh is fine
		"claude-sonnet-5":  "max",
		"claude-opus-4-7":  "max",
		"glm-5.3":          "max",
		"unknown-model-xx": "max",
	}
	for model, effort := range cases {
		payload := map[string]any{
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": effort},
		}
		if clampClaudeAdaptiveEffort(payload, model) {
			t.Fatalf("%s at %q must not be clamped", model, effort)
		}
		if got := outputConfigEffort(payload); got != effort {
			t.Fatalf("%s kept %q, want %q", model, got, effort)
		}
	}
}

// Nothing to do without an effort, or with one already below the ceiling.
func TestClampClaudeAdaptiveEffortLeavesLowEfforts(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high"} {
		payload := map[string]any{"thinking": map[string]any{"type": "adaptive"}}
		if effort != "" {
			payload["output_config"] = map[string]any{"effort": effort}
		}
		if clampClaudeAdaptiveEffort(payload, "claude-opus-5-5") {
			t.Fatalf("effort %q must not be clamped", effort)
		}
	}
}

// The native pass-through applies the clamp, so a key override at "max" cannot
// reach an opus-5-5 upstream as max.
func TestNormalizeClaudePassThroughClampsNeverThinkingEffort(t *testing.T) {
	payload := map[string]any{
		"model":         "claude-opus-5-5",
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "max"},
		"max_tokens":    64000,
	}
	normalizeClaudePassThroughPayload(payload)
	if got := outputConfigEffort(payload); got != "high" {
		t.Fatalf("expected effort clamped to high for opus-5-5, got %q", got)
	}
	if got := stringValue(payload["thinking"].(map[string]any)["type"]); got != "adaptive" {
		t.Fatalf("expected adaptive thinking kept, got %q", got)
	}
}

// fitModelEffort knows the ceiling too, so the key-override path (which fits the
// depth to the model before writing it) lands on high by itself.
func TestFitModelEffortCeilsNeverThinkingClaude(t *testing.T) {
	if got := fitModelEffort("max", "claude-opus-5-5"); got != "high" {
		t.Fatalf("fitModelEffort(max, opus-5-5) = %q, want high", got)
	}
	if got := fitModelEffort("xhigh", "claude-opus-5-5"); got != "high" {
		t.Fatalf("fitModelEffort(xhigh, opus-5-5) = %q, want high", got)
	}
	// A generation that takes max keeps it.
	if got := fitModelEffort("max", "claude-sonnet-5"); got != "max" {
		t.Fatalf("fitModelEffort(max, sonnet-5) = %q, want max", got)
	}
}
