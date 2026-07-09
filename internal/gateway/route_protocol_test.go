package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestRouteProtocolMismatchMessage(t *testing.T) {
	route := domain.Route{
		ID:             "luca-route",
		Name:           "luca-route",
		OutputProtocol: domain.ProtocolClaude,
	}
	message := routeProtocolMismatchMessage(route, domain.ProtocolOpenAIChat)
	for _, expected := range []string{"luca-route", "Claude", "OpenAI Chat", "POST /anthropic/v1/messages"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected message to contain %q, got %q", expected, message)
		}
	}
}
