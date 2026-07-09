package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamClaudeToOpenAIPreservesToolArgumentSpaces guards against a
// regression where each Claude input_json_delta fragment was passed through
// strings.TrimSpace, dropping spaces that fell on a fragment boundary and
// corrupting reassembled tool-call arguments (e.g. "git status --short"
// became "git status--short").
func TestStreamClaudeToOpenAIPreservesToolArgumentSpaces(t *testing.T) {
	// The command string is deliberately split so a boundary lands right on a
	// space: fragment 1 ends with "git status" and fragment 2 starts with
	// " --short".
	input := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4"}}`,
		``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Shell"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"git status"}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" --short\"}"}}`,
		``,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":7}}`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	if _, err := streamClaudeToOpenAIChatEvents(recorder, strings.NewReader(input), "claude-sonnet-4", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reassemble the streamed tool-call arguments across chunks.
	var args strings.Builder
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				args.WriteString(call.Function.Arguments)
			}
		}
	}

	got := args.String()
	const want = `{"command":"git status --short"}`
	if got != want {
		t.Fatalf("tool arguments corrupted:\n got=%q\nwant=%q", got, want)
	}
}
