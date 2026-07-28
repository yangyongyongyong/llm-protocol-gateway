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

func TestStripResponsesInputReasoningKeepsAgentMessageEncryptedContent(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"agent_message","author":"/root","recipient":"/root/a","encrypted_content":"task-ciphertext","content":[]},
			{"type":"reasoning","id":"rs_1","encrypted_content":"reason-cipher"},
			{"type":"message","role":"user","content":"hi","encrypted_content":"stray"}
		]
	}`)
	out, changed := stripResponsesInputReasoning(body)
	if !changed {
		t.Fatal("expected changed=true")
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input := parsed["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input len=%d want 2 (reasoning dropped)", len(input))
	}
	am := input[0].(map[string]any)
	if stringValue(am["type"]) != "agent_message" {
		t.Fatalf("first item=%#v", am)
	}
	if stringValue(am["encrypted_content"]) != "task-ciphertext" {
		t.Fatalf("agent_message encrypted_content must be preserved, got %#v", am)
	}
	msg := input[1].(map[string]any)
	if _, has := msg["encrypted_content"]; has {
		t.Fatalf("stray encrypted_content on message should be stripped: %#v", msg)
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
