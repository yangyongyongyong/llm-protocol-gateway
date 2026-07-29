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
				Action:          "convert",
				InputProtocol:   provider,
				OutputProtocol:  client,
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
	input, ok := responsesReq["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected list user input, got %#v", responsesReq["input"])
	}
	entry, _ := input[0].(map[string]any)
	if stringValue(entry["role"]) != "user" {
		t.Fatalf("expected user role, got %#v", entry)
	}
	content, _ := entry["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected typed content, got %#v", entry["content"])
	}
	part, _ := content[0].(map[string]any)
	if stringValue(part["type"]) != "input_text" || stringValue(part["text"]) != "hello" {
		t.Fatalf("expected input_text hello, got %#v", part)
	}
	reasoning, ok := responsesReq["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" {
		t.Fatalf("expected reasoning effort medium, got %#v", responsesReq["reasoning"])
	}
}

func TestOpenAIChatToResponsesRequestRewritesChatTextParts(t *testing.T) {
	// Cursor / Chat clients send multimodal-style [{type:text}]; Responses upstream
	// rejects bare "text" (must be input_text / output_text).
	chatReq := map[string]any{
		"model": "gpt-5.5",
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "plain string context"},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hello"},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "hi there"},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "see this"},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url":    "data:image/png;base64,abc",
							"detail": "high",
						},
					},
				},
			},
		},
	}
	responsesReq, err := openAIChatToResponsesRequest(chatReq, "gpt-5.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	input, ok := responsesReq["input"].([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("expected 4 input items, got %#v", responsesReq["input"])
	}

	user0, _ := input[0].(map[string]any)
	parts0, _ := user0["content"].([]any)
	p0, _ := parts0[0].(map[string]any)
	if stringValue(p0["type"]) != "input_text" || stringValue(p0["text"]) != "plain string context" {
		t.Fatalf("user string -> input_text: %#v", p0)
	}

	user1, _ := input[1].(map[string]any)
	parts1, _ := user1["content"].([]any)
	p1, _ := parts1[0].(map[string]any)
	if stringValue(p1["type"]) != "input_text" || stringValue(p1["text"]) != "hello" {
		t.Fatalf("user text part -> input_text: %#v", p1)
	}

	asst, _ := input[2].(map[string]any)
	partsA, _ := asst["content"].([]any)
	pa, _ := partsA[0].(map[string]any)
	if stringValue(pa["type"]) != "output_text" || stringValue(pa["text"]) != "hi there" {
		t.Fatalf("assistant text part -> output_text: %#v", pa)
	}

	user2, _ := input[3].(map[string]any)
	parts2, _ := user2["content"].([]any)
	if len(parts2) != 2 {
		t.Fatalf("expected text+image parts, got %#v", parts2)
	}
	img, _ := parts2[1].(map[string]any)
	if stringValue(img["type"]) != "input_image" {
		t.Fatalf("image_url -> input_image: %#v", img)
	}
	if stringValue(img["image_url"]) != "data:image/png;base64,abc" {
		t.Fatalf("expected image url string, got %#v", img)
	}
	if stringValue(img["detail"]) != "high" {
		t.Fatalf("expected detail high, got %#v", img)
	}
}

func TestOpenAIChatToResponsesRequestMapsFileAudioRefusal(t *testing.T) {
	chatReq := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "attach"},
					map[string]any{
						"type": "file",
						"file": map[string]any{
							"file_id":  "file_123",
							"filename": "notes.pdf",
						},
					},
					map[string]any{
						"type": "input_audio",
						"input_audio": map[string]any{
							"data":   "AAAA",
							"format": "wav",
						},
					},
				},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "refusal", "refusal": "nope"},
				},
			},
		},
	}
	responsesReq, err := openAIChatToResponsesRequest(chatReq, "gpt-5.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	input := responsesReq["input"].([]any)
	user, _ := input[0].(map[string]any)
	parts := user["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("expected 3 user parts, got %#v", parts)
	}
	filePart, _ := parts[1].(map[string]any)
	if stringValue(filePart["type"]) != "input_file" || stringValue(filePart["file_id"]) != "file_123" {
		t.Fatalf("file -> input_file: %#v", filePart)
	}
	if stringValue(filePart["filename"]) != "notes.pdf" {
		t.Fatalf("expected filename notes.pdf, got %#v", filePart)
	}
	audioPart, _ := parts[2].(map[string]any)
	if stringValue(audioPart["type"]) != "input_audio" {
		t.Fatalf("input_audio passthrough: %#v", audioPart)
	}
	asst, _ := input[1].(map[string]any)
	asstParts := asst["content"].([]any)
	refusal, _ := asstParts[0].(map[string]any)
	if stringValue(refusal["type"]) != "refusal" || stringValue(refusal["refusal"]) != "nope" {
		t.Fatalf("refusal mapping: %#v", refusal)
	}
}

func TestResponsesToOpenAIChatRequest(t *testing.T) {
	responsesReq := map[string]any{
		"model":        "gpt-5",
		"instructions": "be helpful",
		"input":        "hello",
	}
	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5")
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

func TestResponsesInputImageConvertsToChatImageURL(t *testing.T) {
	responsesReq := map[string]any{
		"model": "gpt-5.3-codex",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "what is this?"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64,abc123"},
				},
			},
		},
	}
	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages, ok := chatReq["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one message, got %#v", chatReq["messages"])
	}
	message := messages[0].(map[string]any)
	content, ok := message["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected text+image content, got %#v", message["content"])
	}
	image := content[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("expected image_url block, got %#v", image)
	}
}

func TestResponsesInputImageObjectURLConvertsToChatImageURL(t *testing.T) {
	responsesReq := map[string]any{
		"model": "gpt-5.3-codex",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "describe"},
					map[string]any{
						"type": "input_image",
						"image_url": map[string]any{
							"url":    "data:image/png;base64,abc123",
							"detail": "high",
						},
					},
				},
			},
		},
	}
	chatReq, _, err := responsesToOpenAIChatRequest(responsesReq, "gpt-5.3-codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := chatReq["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	image := content[1].(map[string]any)
	imageURL := image["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,abc123" || imageURL["detail"] != "high" {
		t.Fatalf("expected object image_url conversion, got %#v", image)
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
