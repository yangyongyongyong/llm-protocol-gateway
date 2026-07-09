package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamOpenAIChatToClaudeEvents(t *testing.T) {
	input := strings.Join([]string{
		`data: {"id":"chatcmpl-test","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"2"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":1,"total_tokens":14}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	recorder := httptest.NewRecorder()
	usage, err := streamOpenAIChatToClaudeEvents(recorder, strings.NewReader(input), "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.InputTokens != 13 || usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"event: message_start",
		"event: content_block_delta",
		`"text":"2"`,
		"event: message_stop",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected stream to contain %q, got:\n%s", expected, body)
		}
	}
}
