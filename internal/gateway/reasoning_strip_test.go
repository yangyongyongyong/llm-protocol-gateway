package gateway

import (
	"encoding/json"
	"testing"
)

func TestStripResponsesInputReasoningDropsReasoningItems(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"foreign-ciphertext"},
			{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"},
			{"type":"function_call_output","call_id":"c1","output":"ok"}
		]
	}`)
	out, changed := stripResponsesInputReasoning(body)
	if !changed {
		t.Fatal("expected changed=true (reasoning item present)")
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input := parsed["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input len=%d want 3 (reasoning dropped): %#v", len(input), input)
	}
	for _, raw := range input {
		item := raw.(map[string]any)
		if stringValue(item["type"]) == "reasoning" {
			t.Fatalf("reasoning item survived: %#v", item)
		}
		if _, has := item["encrypted_content"]; has {
			t.Fatalf("encrypted_content survived: %#v", item)
		}
	}
	// model preserved
	if parsed["model"] != "gpt-5.5" {
		t.Fatalf("model=%v want gpt-5.5", parsed["model"])
	}
}

func TestStripResponsesInputReasoningNoReasoningIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, changed := stripResponsesInputReasoning(body)
	if changed {
		t.Fatalf("expected changed=false; out=%s", out)
	}
}

func TestStripResponsesInputReasoningStringInputIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"just a string prompt"}`)
	if _, changed := stripResponsesInputReasoning(body); changed {
		t.Fatal("string input should be a no-op")
	}
}

func TestResponsesErrorLooksLikeEncryptedContent(t *testing.T) {
	yes := [][]byte{
		[]byte(`{"error":{"message":"Invalid value","code":"invalid_encrypted_content"}}`),
		[]byte(`{"error":{"message":"reasoning.encrypted_content could not be verified"}}`),
		[]byte(`{"error":{"type":"invalid_request_error","message":"ENCRYPTED_CONTENT bad"}}`),
	}
	for _, b := range yes {
		if !responsesErrorLooksLikeEncryptedContent(b) {
			t.Fatalf("expected match: %s", b)
		}
	}
	no := [][]byte{
		[]byte(`{"error":{"message":"rate limit"}}`),
		[]byte(`{"error":{"message":"model not found"}}`),
	}
	for _, b := range no {
		if responsesErrorLooksLikeEncryptedContent(b) {
			t.Fatalf("expected no match: %s", b)
		}
	}
}
