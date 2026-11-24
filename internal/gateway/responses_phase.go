package gateway

import (
	"encoding/json"
	"strings"
)

// phaseOutput marks assistant message items the way OpenAI's own Responses
// replies do: commentary before a tool call or another output item, and
// final_answer only for the last non-reasoning item of a completed reply.
//
// Codex treats an assistant message with no phase as a final answer. A Chat
// upstream that says "Let me check" and then calls a tool therefore looked
// like it had ended the turn before the work was done. This mirrors Magpie's
// phase.go: keep an upstream-supplied phase, and never guess a final answer
// for an incomplete/failed response.
func phaseOutput(output []any, done bool) bool {
	last := -1
	for i, value := range output {
		item, _ := value.(map[string]any)
		if item != nil && stringValue(item["type"]) != "reasoning" {
			last = i
		}
	}
	changed := false
	for i, value := range output {
		item, _ := value.(map[string]any)
		if item == nil || stringValue(item["type"]) != "message" {
			continue
		}
		if role := stringValue(item["role"]); role != "" && role != "assistant" {
			continue
		}
		if strings.TrimSpace(stringValue(item["phase"])) != "" {
			continue
		}
		switch {
		case i < last:
			item["phase"] = "commentary"
		case i == last && done:
			item["phase"] = "final_answer"
		default:
			continue
		}
		changed = true
	}
	return changed
}

func phaseResponsesWhole(body []byte) ([]byte, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || payload["object"] != "response" {
		return nil, false
	}
	output, _ := payload["output"].([]any)
	if !phaseOutput(output, stringValue(payload["status"]) == "completed") {
		return nil, false
	}
	out, err := json.Marshal(payload)
	return out, err == nil
}

func toAnySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}
