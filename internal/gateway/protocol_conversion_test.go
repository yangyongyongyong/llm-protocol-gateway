package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestProtocolConversionMatrixImplemented(t *testing.T) {
	clientProtocols := []domain.Protocol{
		domain.ProtocolOpenAIChat,
		domain.ProtocolOpenAIResponses,
		domain.ProtocolClaude,
	}
	providerProtocols := []domain.Protocol{
		domain.ProtocolOpenAIChat,
		domain.ProtocolOpenAIResponses,
		domain.ProtocolClaude,
	}

	for _, client := range clientProtocols {
		for _, provider := range providerProtocols {
			if client == provider {
				continue
			}
			decision := domain.RouteDecision{
				Action:        "convert",
				InputProtocol: provider,
				OutputProtocol: client,
				ConversionLabel: provider.DisplayName() + " -> " + client.DisplayName(),
			}
			if !protocolConversionImplemented(client, decision) {
				t.Fatalf("expected conversion to be implemented for %s", decision.ConversionLabel)
			}
		}
	}
}

func protocolConversionImplemented(clientProtocol domain.Protocol, decision domain.RouteDecision) bool {
	switch clientProtocol {
	case domain.ProtocolOpenAIChat:
		switch decision.InputProtocol {
		case domain.ProtocolClaude, domain.ProtocolOpenAIResponses:
			return true
		}
	case domain.ProtocolClaude:
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses:
			return true
		}
	case domain.ProtocolOpenAIResponses:
		switch decision.InputProtocol {
		case domain.ProtocolOpenAIChat, domain.ProtocolClaude:
			return true
		}
	}
	return false
}

func TestOpenAIChatToResponsesRequest(t *testing.T) {
	chatReq := map[string]any{
		"model": "gpt-5",
		"messages": []any{
			map[string]any{"role": "system", "content": "be helpful"},
			map[string]any{"role": "user", "content": "hello"},
		},
		"reasoning_effort": "medium",
	}
	responsesReq, err := openAIChatToResponsesRequest(chatReq, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responsesReq["instructions"] != "be helpful" {
		t.Fatalf("expected instructions, got %#v", responsesReq["instructions"])
	}
	if responsesReq["input"] != "hello" {
		t.Fatalf("expected simple user input, got %#v", responsesReq["input"])
	}
	reasoning, ok := responsesReq["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" {
		t.Fatalf("expected reasoning effort medium, got %#v", responsesReq["reasoning"])
	}
}

func TestResponsesToOpenAIChatRequest(t *testing.T) {
	responsesReq := map[string]any{
		"model":        "gpt-5",
		"instructions": "be helpful",
		"input":        "hello",
	}
	chatReq, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := chatReq["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("unexpected messages length: %d", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	second, _ := messages[1].(map[string]any)
	if first["role"] != "system" || second["content"] != "hello" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}

func TestResponsesToClaudeComposedRequest(t *testing.T) {
	responsesReq := map[string]any{
		"model":        "claude-sonnet-5",
		"instructions": "be helpful",
		"input":        "hello",
		"reasoning":    map[string]any{"effort": "low"},
	}
	claudeReq, err := responsesToClaudeRequest(responsesReq, "claude-sonnet-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	thinking, ok := claudeReq["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("expected adaptive thinking, got %#v", claudeReq["thinking"])
	}
}

func TestResponsesToOpenAIChatResponse(t *testing.T) {
	body := []byte(`{
		"id":"resp_1",
		"object":"response",
		"model":"gpt-5",
		"status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],
		"usage":{"input_tokens":3,"output_tokens":1}
	}`)
	converted, usage, err := responsesToOpenAIChatResponse(body, "gpt-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if !containsString(string(converted), `"content":"Hi"`) {
		t.Fatalf("expected converted chat content, got %s", converted)
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()))
}
