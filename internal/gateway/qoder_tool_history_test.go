package gateway

import (
	"strings"
	"testing"
)

// qoderHistoryChat is one conversation with a call and its result.
const qoderHistoryChat = `{
	"model":"qwen3.8-max",
	"messages":[
		{"role":"user","content":"read it"},
		{"role":"assistant","content":"on it","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"the file says hi"}
	],
	"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{}}}}]
}`

// qoderHistoryMessages is a built body's turns.
func qoderHistoryMessages(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	body := decodeQoderBody(t, raw)
	items, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing: %v", body)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, message)
	}
	return out
}

func qoderMessageOfRole(messages []map[string]any, role string) map[string]any {
	for _, message := range messages {
		if message["role"] == role {
			return message
		}
	}
	return nil
}

// A model on the site that reads structured tool turns gets them: the call and
// the result stay paired by id.
func TestQoderStructuredToolHistoryForAWhitelistedCNModel(t *testing.T) {
	info := qoderCNStructuredInfo()
	raw, err := qoderChatBodyWith("qwen3.8-max", []byte(qoderHistoryChat), info, qoderBodyContext{Site: qoderCNSite})
	if err != nil {
		t.Fatal(err)
	}
	messages := qoderHistoryMessages(t, raw)
	assistant := qoderMessageOfRole(messages, "assistant")
	if _, ok := assistant["tool_calls"].([]any); !ok {
		t.Fatalf("the call must stay structured: %v", assistant)
	}
	tool := qoderMessageOfRole(messages, "tool")
	if tool == nil || tool["tool_call_id"] != "call_1" {
		t.Fatalf("the result must answer its call by id: %v", tool)
	}
	for _, message := range messages {
		if text, ok := message["content"].(string); ok && strings.Contains(text, qoderEchoMarkerText) {
			t.Fatalf("a structured history must not carry the text envelope: %q", text)
		}
	}
}

// The international site's models generally do not read structured tool turns:
// the call and its result travel as the vendor client's text envelope instead.
func TestQoderTextEnvelopeOnTheInternationalSite(t *testing.T) {
	info := qoderCNStructuredInfo() // same model, other site
	raw, err := qoderChatBodyWith("qwen3.8-max", []byte(qoderHistoryChat), info, qoderBodyContext{Site: qoderGlobalSite})
	if err != nil {
		t.Fatal(err)
	}
	messages := qoderHistoryMessages(t, raw)
	if tool := qoderMessageOfRole(messages, "tool"); tool != nil {
		t.Fatalf("a text envelope must not carry a structured tool turn: %v", tool)
	}
	assistant := qoderMessageOfRole(messages, "assistant")
	text, _ := assistant["content"].(string)
	if !strings.Contains(text, qoderEchoMarkerText) {
		t.Fatalf("the call must travel behind the marker: %q", text)
	}
	if !strings.Contains(text, `"name":"read_file"`) || !strings.Contains(text, `"arguments":"{\"path\":\"a.go\"}"`) {
		t.Fatalf("the calls must travel as a JSON array: %q", text)
	}
	if !strings.HasPrefix(text, "on it") {
		t.Fatalf("the turn's own prose must come first: %q", text)
	}
	// The result becomes a user turn naming the tool it came from.
	var result map[string]any
	for _, message := range messages {
		if text, ok := message["content"].(string); ok && strings.HasPrefix(text, qoderToolResultOpen) {
			result = message
			break
		}
	}
	if result == nil {
		t.Fatalf("the result envelope is missing: %v", messages)
	}
	if result["role"] != "user" {
		t.Fatalf("the result travels as a user turn, got %v", result["role"])
	}
	if text := result["content"].(string); !strings.HasPrefix(text, "[工具结果 (read_file)]\n") {
		t.Fatalf("the envelope must name the tool: %q", text)
	}
}

// A CN model that is not on the list is served the text envelope too: the list
// is what the vendor client has verified, not a guess about the model's name.
func TestQoderTextEnvelopeForAnUnlistedCNModel(t *testing.T) {
	info := qoderModelInfo{Key: "kimi-k2", DisplayName: "Kimi"}
	raw, err := qoderChatBodyWith("kimi-k2", []byte(qoderHistoryChat), info, qoderBodyContext{Site: qoderCNSite})
	if err != nil {
		t.Fatal(err)
	}
	if tool := qoderMessageOfRole(qoderHistoryMessages(t, raw), "tool"); tool != nil {
		t.Fatalf("an unlisted model must not be sent structured turns: %v", tool)
	}
}

// One missing id makes the whole history fall back to text: a model handed an
// unpaired result answers with a provider_error and loses the turn.
func TestQoderUnpairedToolResultFallsBackToText(t *testing.T) {
	info := qoderCNStructuredInfo()
	unpaired := `{
		"model":"qwen3.8-max",
		"messages":[
			{"role":"user","content":"read it"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","content":"the file says hi"}
		],
		"tools":[{"type":"function","function":{"name":"read_file","parameters":{}}}]
	}`
	raw, err := qoderChatBodyWith("qwen3.8-max", []byte(unpaired), info, qoderBodyContext{Site: qoderCNSite})
	if err != nil {
		t.Fatal(err)
	}
	messages := qoderHistoryMessages(t, raw)
	if tool := qoderMessageOfRole(messages, "tool"); tool != nil {
		t.Fatalf("an unpaired history must not go out structured: %v", tool)
	}
	if assistant := qoderMessageOfRole(messages, "assistant"); assistant == nil {
		t.Fatal("the assistant turn disappeared")
	} else if text, _ := assistant["content"].(string); !strings.Contains(text, qoderEchoMarkerText) {
		t.Fatalf("the call must travel behind the marker: %q", text)
	}
}

// The switch the vendor client exposes: force either shape regardless of site
// and model.
func TestQoderToolHistoryModeOverridesTheModel(t *testing.T) {
	info := qoderCNStructuredInfo()
	cases := []struct {
		mode       string
		site       qoderSite
		structured bool
	}{
		{"off", qoderCNSite, false},
		{"on", qoderGlobalSite, true},
		{"auto", qoderCNSite, true},
		{"auto", qoderGlobalSite, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.site.Name, func(t *testing.T) {
			previous := qoderStructuredToolHistoryMode
			qoderStructuredToolHistoryMode = tc.mode
			t.Cleanup(func() { qoderStructuredToolHistoryMode = previous })

			raw, err := qoderChatBodyWith("qwen3.8-max", []byte(qoderHistoryChat), info, qoderBodyContext{Site: tc.site})
			if err != nil {
				t.Fatal(err)
			}
			structured := qoderMessageOfRole(qoderHistoryMessages(t, raw), "tool") != nil
			if structured != tc.structured {
				t.Fatalf("structured = %v, want %v", structured, tc.structured)
			}
		})
	}
}
