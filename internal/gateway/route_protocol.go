package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func outputProtocolPath(protocol domain.Protocol) string {
	switch protocol {
	case domain.ProtocolOpenAIChat:
		return "POST /v1/chat/completions"
	case domain.ProtocolOpenAIResponses:
		return "POST /openai/v1/responses"
	case domain.ProtocolClaude:
		return "POST /anthropic/v1/messages"
	default:
		return string(protocol)
	}
}

func routeProtocolMismatchMessage(route domain.Route, endpointProtocol domain.Protocol) string {
	routeLabel := route.Name
	if routeLabel == "" {
		routeLabel = route.ID
	}
	return fmt.Sprintf(
		"API key is bound to route %q with output protocol %s, but this endpoint is %s. Use %s for this key.",
		routeLabel,
		route.OutputProtocol.DisplayName(),
		endpointProtocol.DisplayName(),
		outputProtocolPath(route.OutputProtocol),
	)
}

func writeRouteProtocolMismatch(w http.ResponseWriter, route domain.Route, endpointProtocol domain.Protocol) {
	writeJSON(w, http.StatusBadRequest, jsonRawMessage(routeProtocolMismatchBody(route, endpointProtocol)))
}

func jsonRawMessage(body []byte) any {
	var value any
	_ = json.Unmarshal(body, &value)
	if value == nil {
		return map[string]any{"error": map[string]any{"message": "route protocol mismatch", "type": "route_protocol_mismatch"}}
	}
	return value
}

func routeProtocolMismatchBody(route domain.Route, endpointProtocol domain.Protocol) []byte {
	message := routeProtocolMismatchMessage(route, endpointProtocol)
	if endpointProtocol == domain.ProtocolClaude {
		body, _ := json.Marshal(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "route_protocol_mismatch",
				"message": message,
			},
		})
		return body
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "route_protocol_mismatch",
		},
	})
	return body
}
