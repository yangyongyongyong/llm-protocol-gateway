package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeCompletion(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var completion map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&completion); err != nil {
		t.Fatalf("completion is not JSON: %v (%s)", err, string(raw))
	}
	return completion
}

// buildCompletion is a minimal chat.completion for these tests.
func buildCompletion(content, finishReason string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{"completion_tokens": 10, "total_tokens": 20},
	}
}

// "length" is the upstream saying the answer was cut: that is the case worth
// continuing. A tool call is not, and a finished answer is not.
func TestWorkBuddyNeedsContinuation(t *testing.T) {
	cases := []struct {
		name       string
		completion map[string]any
		want       bool
	}{
		{"cut at the ceiling", buildCompletion("half a sentence", "length"), true},
		// Measured live: this upstream reports a cut answer as max_tokens, not length.
		{"cut, Anthropic wording", buildCompletion("half a sentence", "max_tokens"), true},
		{"empty with end_turn", buildCompletion("", "end_turn"), true},
		{"thinking-only budget", buildCompletion("", "max_tokens"), true},
		{"finished normally", buildCompletion("a whole answer", "stop"), false},
		{"empty answer", buildCompletion("", "stop"), true},
		{"empty answer with length", buildCompletion("   ", "length"), true},
		{"tool call", map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []any{map[string]any{"id": "c1", "type": "function"}},
				},
				"finish_reason": "length",
			}},
		}, false},
	}
	for _, testCase := range cases {
		if got := workBuddyNeedsContinuation(testCase.completion); got != testCase.want {
			t.Fatalf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// The continuation body carries the unfinished answer plus a nudge that forbids
// repeating, announcing or summarising — each of which produces a useless second
// half that reads like duplicated text.
func TestWorkBuddyContinueBodyAppendsPartialAndNudge(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","stream":true,"messages":[{"role":"system","content":"s"},{"role":"user","content":"写一篇长文"}]}`)
	got := workBuddyContinueBody(body, "第一段已经写了")
	if got == nil {
		t.Fatal("no continuation body")
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want the two originals plus assistant and nudge", len(messages))
	}
	assistant, _ := messages[2].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != "第一段已经写了" {
		t.Fatalf("assistant turn = %+v", assistant)
	}
	nudge, _ := messages[3].(map[string]any)
	if nudge["role"] != "user" {
		t.Fatalf("nudge role = %v", nudge["role"])
	}
	text, _ := nudge["content"].(string)
	for _, forbidden := range []string{"do not repeat", "do not add a preamble", "do not mention"} {
		if !strings.Contains(text, forbidden) {
			t.Fatalf("the nudge lost %q: %s", forbidden, text)
		}
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %v, every WorkBuddy call streams", payload["stream"])
	}
}

// An empty partial adds no assistant turn: an empty message in the history is a
// shape the upstream refuses (11153 family).
func TestWorkBuddyContinueBodyWithoutPartial(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	got := workBuddyContinueBody(body, "   ")
	if got == nil {
		t.Fatal("no continuation body")
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want only the nudge added", len(messages))
	}
	if first, _ := messages[0].(map[string]any); first["role"] != "user" {
		t.Fatalf("first turn = %+v", first)
	}
}

// A body that will not parse is not continued: that would be a second, invented
// request.
func TestWorkBuddyContinueBodyRejectsGarbage(t *testing.T) {
	for _, body := range []string{"", "not json", `{"model":"m"}`, `{"input":"x"}`} {
		if got := workBuddyContinueBody([]byte(body), "partial"); got != nil {
			t.Fatalf("%q was continued: %s", body, string(got))
		}
	}
}

// Joining concatenates the text, sums usage, and takes the last segment's finish
// reason so a second cut still reports "length".
func TestWorkBuddyMergeContinuation(t *testing.T) {
	base := buildCompletion("第一段。", "length")
	extra := buildCompletion("第二段。", "stop")
	merged := workBuddyMergeContinuation(base, extra)
	if text := workBuddyCompletionText(merged); text != "第一段。第二段。" {
		t.Fatalf("text = %q", text)
	}
	if reason := workBuddyCompletionFinish(merged); reason != "stop" {
		t.Fatalf("finish = %q, want the last segment's", reason)
	}
	usage, _ := merged["usage"].(map[string]any)
	if workBuddyAnyFloat(usage["completion_tokens"]) != 20 {
		t.Fatalf("completion_tokens = %v, want the sum", usage["completion_tokens"])
	}
	if workBuddyAnyFloat(usage["total_tokens"]) != 40 {
		t.Fatalf("total_tokens = %v, want the sum", usage["total_tokens"])
	}

	// A third segment that was cut again keeps "length", which is how the client
	// learns the answer is still incomplete.
	again := workBuddyMergeContinuation(merged, buildCompletion("第三段。", "length"))
	if reason := workBuddyCompletionFinish(again); reason != "length" {
		t.Fatalf("finish = %q, want length", reason)
	}
	if text := workBuddyCompletionText(again); text != "第一段。第二段。第三段。" {
		t.Fatalf("text = %q", text)
	}
}

// A segment that produced nothing must not erase the reason the previous one gave.
func TestWorkBuddyMergeContinuationKeepsReasonOnEmptySegment(t *testing.T) {
	base := buildCompletion("第一段。", "length")
	merged := workBuddyMergeContinuation(base, buildCompletion("", "stop"))
	if reason := workBuddyCompletionFinish(merged); reason != "length" {
		t.Fatalf("finish = %q, want the previous length", reason)
	}
	if text := workBuddyCompletionText(merged); text != "第一段。" {
		t.Fatalf("text = %q", text)
	}
}

// The loop is off unless asked for: joining changes both cost and shape, which is
// the operator's call.
func TestWorkBuddyContinueIsOptIn(t *testing.T) {
	t.Setenv(workBuddyContinueEnv, "")
	if workBuddyContinueEnabled() {
		t.Fatal("continuation is on by default")
	}
	for _, raw := range []string{"1", "true", "yes", "on"} {
		t.Setenv(workBuddyContinueEnv, raw)
		if !workBuddyContinueEnabled() {
			t.Fatalf("%q did not enable continuation", raw)
		}
	}
	t.Setenv(workBuddyContinueEnv, "0")
	if workBuddyContinueEnabled() {
		t.Fatal("0 enabled continuation")
	}
}

// Text is read out of both message shapes the chat route can produce.
func TestWorkBuddyCompletionTextReadsBothShapes(t *testing.T) {
	plain := buildCompletion("直接文本", "stop")
	if got := workBuddyCompletionText(plain); got != "直接文本" {
		t.Fatalf("plain = %q", got)
	}
	parts := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "第一"},
				map[string]any{"type": "text", "text": "第二"},
			}},
			"finish_reason": "stop",
		}},
	}
	if got := workBuddyCompletionText(parts); got != "第一第二" {
		t.Fatalf("parts = %q", got)
	}
}

// A tool call written as text must not trigger continuation: the client parses the markup
// and runs it, so continuing asks for a second call and the client runs that too — the
// loop the reference implementation measured in production.
func TestWorkBuddyNeedsContinuationStopsAtToolCallMarkup(t *testing.T) {
	cases := []string{
		// DSML, both pipe spellings, closed.
		"任务做完了。</｜｜DSML｜｜ invoke> </｜｜DSML｜｜ calls>",
		"任务做完了。</|DSML| invoke> </|DSML| calls>",
		// Unclosed: the stream broke inside the markup.
		"准备调用：<｜｜DSML｜｜ invoke name=\"read\"",
		// XML forms a prompt may have taught.
		"done</function_calls>",
		"done<function_calls>",
		"done<antml:invoke name=\"x\">",
		"done<invoke name=\"x\">",
		"done</tool_calls>",
		"done<tool_calls>",
	}
	for _, text := range cases {
		completion := buildCompletion(text, "max_tokens")
		if workBuddyNeedsContinuation(completion) {
			t.Fatalf("%q triggered a continuation; the call is already there", text)
		}
	}
	// A plain truncated answer still continues: the guard is about markup, not length.
	if !workBuddyNeedsContinuation(buildCompletion("half a sentence", "max_tokens")) {
		t.Fatal("a plain cut answer stopped continuing")
	}
}

// The marker table itself, so a marker cannot be dropped silently.
func TestWorkBuddyHasToolCallMarkup(t *testing.T) {
	for _, marker := range workBuddyToolCallMarkupMarkers {
		if !workBuddyHasToolCallMarkup("text " + marker + " more") {
			t.Fatalf("marker %q is not detected", marker)
		}
	}
	for _, text := range []string{"", "普通回答", "mention tool_calls in prose without brackets"} {
		if workBuddyHasToolCallMarkup(text) {
			t.Fatalf("%q was treated as markup", text)
		}
	}
}
