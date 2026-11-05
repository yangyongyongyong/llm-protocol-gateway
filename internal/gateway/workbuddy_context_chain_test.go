package gateway

import (
	"testing"
)

// The chain the reference implementation documents is four levels: the upstream's
// own figure, a persisted cache, a static seed table, and models.dev. This gateway
// resolves the first three from data it already has — a persisted cache of a value
// the upstream reports live buys nothing, and models.dev would put an external HTTP
// call on the model-sync path for models this upstream never omits.
//
// What matters is the order, and that a missing upstream figure never wipes out what
// the table knows: the config lists some models without maxInputTokens, and assigning
// that field straight through set their window to 0.

// A model the config knows but reports no window for keeps the table's value.
func TestResolveModelContextLengthKeepsTheTableWhenUpstreamIsSilent(t *testing.T) {
	cases := []struct {
		model    string
		reported int
		want     int
	}{
		// A known model with a silent upstream keeps the table's window.
		{"glm-5.3", 0, knownContextOrDefault("glm-5.3")},
		// The upstream reporting something larger wins: it is the same kind of
		// figure measured live, not a guess.
		{"glm-5.3", 2_000_000, 2_000_000},
		// A model nobody knows and the upstream is silent about gets the default.
		{"probe-unknown-model", 0, contextLengthDefault},
		{"probe-unknown-model", 320_000, 320_000},
	}
	for _, testCase := range cases {
		if got := resolveModelContextLength(testCase.model, testCase.reported); got != testCase.want {
			t.Fatalf("resolveModelContextLength(%q, %d) = %d, want %d",
				testCase.model, testCase.reported, got, testCase.want)
		}
	}
}

// knownContextOrDefault is what the resolver should answer for a known model when the
// upstream says nothing.
func knownContextOrDefault(model string) int {
	if value, ok := knownModelContextLength(model); ok {
		return value
	}
	return contextLengthDefault
}
