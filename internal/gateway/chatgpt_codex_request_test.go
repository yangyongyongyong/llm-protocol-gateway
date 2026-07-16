package gateway

import (
	"encoding/json"
	"testing"
)

func TestPrepareChatGPTCodexRequestBodyNormalizesInputAndStripsUnsupported(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"model":             "gpt-5.6-terra",
		"stream":            false,
		"store":             true,
		"input":             "say hi",
		"max_output_tokens": 32000,
		"temperature":       0.7,
		"top_p":             0.9,
		"verbosity":         "low",
		"metadata":          map[string]any{"a": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, clientStream, err := prepareChatGPTCodexRequestBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	if clientStream {
		t.Fatalf("expected clientWantedStream=false")
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["store"] != false {
		t.Fatalf("store=%v want false", payload["store"])
	}
	if payload["stream"] != true {
		t.Fatalf("stream=%v want true", payload["stream"])
	}
	for _, key := range chatgptCodexUnsupportedParams {
		if _, ok := payload[key]; ok {
			t.Fatalf("unsupported param %q still present", key)
		}
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%v want single-item list", payload["input"])
	}
	entry, _ := input[0].(map[string]any)
	if stringValue(entry["role"]) != "user" {
		t.Fatalf("role=%v", entry["role"])
	}
	content, _ := entry["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content empty: %#v", entry["content"])
	}
	part, _ := content[0].(map[string]any)
	if stringValue(part["type"]) != "input_text" || stringValue(part["text"]) != "say hi" {
		t.Fatalf("content part=%v", part)
	}
}

func TestSetResponsesInputKeepsList(t *testing.T) {
	req := map[string]any{}
	setResponsesInput(req, []any{
		map[string]any{"role": "user", "content": "hello"},
	})
	input, ok := req["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%v", req["input"])
	}
	entry, _ := input[0].(map[string]any)
	content, ok := entry["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content=%v", entry["content"])
	}
}

func TestResponsesToOpenAIChatResponseHandlesDetailError(t *testing.T) {
	body := []byte(`{"detail":"Input must be a list"}`)
	converted, _, err := responsesToOpenAIChatResponse(body, "gpt-5.6-terra")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(converted, &payload); err != nil {
		t.Fatal(err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if stringValue(errObj["message"]) != "Input must be a list" {
		t.Fatalf("error=%v", payload["error"])
	}
}

func TestExtractResponseErrorMessageReadsDetail(t *testing.T) {
	got := extractResponseErrorMessage([]byte(`{"detail":"Unsupported parameter: temperature"}`))
	if got != "Unsupported parameter: temperature" {
		t.Fatalf("got %q", got)
	}
}
