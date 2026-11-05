package gateway

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// qoderEchoStream is what one converted stream amounted to, read back the way a
// client would read it.
type qoderEchoStream struct {
	text   string
	names  []string
	args   string
	finish string
	errs   []string
	done   bool
	raw    string
}

func qoderReadEcho(t *testing.T, reply string, tools map[string]bool) qoderEchoStream {
	t.Helper()
	reader := newQoderChunkReaderWithTools(strings.NewReader(reply), "ultimate", tools)
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the converted stream: %v", err)
	}
	var result qoderEchoStream
	result.raw = string(out)
	text := strings.Builder{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			result.done = true
			continue
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &frame) != nil {
			continue
		}
		if frame.Error.Message != "" {
			result.errs = append(result.errs, frame.Error.Message)
		}
		for _, choice := range frame.Choices {
			text.WriteString(choice.Delta.Content)
			for _, call := range choice.Delta.ToolCalls {
				if name := strings.TrimSpace(call.Function.Name); name != "" {
					result.names = append(result.names, name)
				}
				result.args += call.Function.Arguments
			}
			if choice.FinishReason != "" {
				result.finish = choice.FinishReason
			}
		}
	}
	result.text = text.String()
	return result
}

// qoderEchoFrame is one answer frame carrying content.
func qoderEchoFrame(t *testing.T, content string) string {
	t.Helper()
	return qoderChunkFrame(t, map[string]any{"content": content}, "", nil)
}

// The marker Qoder's own client writes into a conversation's history is what a
// model repeats in a long session. Read strictly, it becomes the call it was
// meant to be instead of being shown to the user as JSON.
func TestQoderLiftsTheClientsOwnToolCallMarker(t *testing.T) {
	echo := "[assistant 请求调用工具]\n" +
		`[{"name":"read_file","arguments":"{\"path\":\"a.go\"}"},{"name":"search","arguments":"{\"q\":\"x\"}"}]`
	stream := qoderReadEcho(t, qoderEchoFrame(t, "Reading it now.\n\n"+echo)+qoderChunkFrame(t, map[string]any{}, "stop", nil), nil)
	if len(stream.errs) > 0 {
		t.Fatalf("an echo that parses must not fail the turn: %v", stream.errs)
	}
	if len(stream.names) != 2 || stream.names[0] != "read_file" || stream.names[1] != "search" {
		t.Fatalf("calls lifted = %v, want read_file and search", stream.names)
	}
	if !strings.Contains(stream.args, "a.go") {
		t.Fatalf("arguments lost: %q", stream.args)
	}
	if stream.finish != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", stream.finish)
	}
	if strings.Contains(stream.text, "请求调用工具") || strings.Contains(stream.text, `"name"`) {
		t.Fatalf("the echoed bookkeeping leaked into the answer: %q", stream.text)
	}
}

// The model writes the marker with whichever brackets it feels like; every
// spelling the vendor client accepts has to be read.
func TestQoderReadsEveryMarkerSpelling(t *testing.T) {
	payload := `[{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}]`
	cases := []struct{ name, echo string }{
		{"plain", "[assistant 请求调用工具]\n" + payload},
		{"angle", "<assistant 请求调用工具>\n" + payload},
		{"nested", "[<assistant 请求调用工具]\n" + payload},
		{"no closing bracket", "[assistant 请求调用工具\n" + payload},
		{"parentheses", "(assistant 请求调用工具)\n" + payload},
		{"no brackets at all", "assistant 请求调用工具\n" + payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := qoderReadEcho(t, qoderEchoFrame(t, tc.echo), nil)
			if len(stream.names) != 1 || stream.names[0] != "read_file" {
				t.Fatalf("calls lifted = %v, text %q", stream.names, stream.text)
			}
		})
	}
}

// A marker split across frames still becomes a call: the block is read whole,
// however the stream chopped it up.
func TestQoderReadsAMarkerAcrossFrames(t *testing.T) {
	reply := qoderEchoFrame(t, "Let me look.\n\n[assistant 请求调用工") +
		qoderEchoFrame(t, `具][{"name":"read_file","arguments":`) +
		qoderEchoFrame(t, `"{\"path\":\"a.go\"}"}]`)
	stream := qoderReadEcho(t, reply, nil)
	if len(stream.names) != 1 {
		t.Fatalf("a marker split across frames was not lifted: %v (%q)", stream.names, stream.text)
	}
	if !strings.Contains(stream.text, "Let me look.") {
		t.Fatalf("the prose before the marker must survive: %q", stream.text)
	}
}

// The model sometimes writes the inner quotes unescaped, which makes the outer
// array invalid JSON. The vendor client repairs that, and so does this.
func TestQoderRepairsUnescapedArgumentQuotes(t *testing.T) {
	echo := `[assistant 请求调用工具][{"name":"read_file","arguments":"{"path":"a.go"}"}]`
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), nil)
	if len(stream.names) != 1 {
		t.Fatalf("the repaired call was not lifted: %v (%q)", stream.names, stream.text)
	}
	if !strings.Contains(stream.args, "a.go") {
		t.Fatalf("arguments lost in the repair: %q", stream.args)
	}
}

// DeepSeek writes its calls as DSML markup rather than JSON.
func TestQoderLiftsDSMLCalls(t *testing.T) {
	echo := "<|DSML|invoke name=\"read_file\">\n" +
		"<|DSML|parameter name=\"path\">a.go</|DSML|parameter>\n" +
		"</|DSML|invoke>"
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), nil)
	if len(stream.names) != 1 || stream.names[0] != "read_file" {
		t.Fatalf("DSML call not lifted: %v (%q)", stream.names, stream.text)
	}
	if !strings.Contains(stream.args, "a.go") {
		t.Fatalf("DSML arguments lost: %q", stream.args)
	}
	if strings.Contains(stream.text, "DSML") {
		t.Fatalf("the markup leaked into the answer: %q", stream.text)
	}
}

// A block naming tools this request never offered is prose about tools: it is
// the answer, so it is passed on rather than swallowed or run.
func TestQoderLeavesAnUndeclaredEchoAsText(t *testing.T) {
	echo := `[assistant 请求调用工具][{"name":"delete_everything","arguments":"{}"}]`
	declared := map[string]bool{"read_file": true}
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), declared)
	if len(stream.names) != 0 {
		t.Fatalf("an undeclared tool must not be lifted: %v", stream.names)
	}
	if !strings.Contains(stream.text, "delete_everything") {
		t.Fatalf("the text has to reach the client instead: %q", stream.text)
	}
}

// An echo the token budget cut off mid-array is dropped: the user was never
// meant to read the gateway's own bookkeeping, and half an array is not an
// answer either.
func TestQoderDropsATruncatedEcho(t *testing.T) {
	echo := `[assistant 请求调用工具][{"name":"read_file","arguments":"{\"path\":`
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), nil)
	if len(stream.errs) > 0 {
		t.Fatalf("a truncated echo is not a protocol failure: %v", stream.errs)
	}
	if strings.Contains(stream.text, "请求调用工具") {
		t.Fatalf("the marker leaked: %q", stream.text)
	}
	if stream.finish != "stop" {
		t.Fatalf("finish reason = %q, want stop", stream.finish)
	}
}

// An answer that merely talks about the marker is an answer, not an echo: it
// has no JSON behind it, so it must reach the client untouched.
func TestQoderKeepsProseAboutTheMarker(t *testing.T) {
	prose := "The converter writes [assistant 请求调用工具] into the history before each call."
	stream := qoderReadEcho(t, qoderEchoFrame(t, prose), nil)
	if !strings.Contains(stream.text, "请求调用工具") {
		t.Fatalf("prose about the marker was swallowed: %q", stream.text)
	}
	if len(stream.errs) > 0 {
		t.Fatalf("prose must not fail the turn: %v", stream.errs)
	}
}

// An array that parses but whose arguments are not JSON is a protocol failure:
// the model meant to call something, and half a call must not be run.
func TestQoderFailsOnArgumentsThatAreNotJSON(t *testing.T) {
	echo := `[assistant 请求调用工具][{"name":"read_file","arguments":"not json at all"}]`
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), nil)
	if len(stream.errs) == 0 {
		t.Fatalf("expected a protocol failure, got text %q", stream.text)
	}
	if stream.done {
		t.Fatal("a failed turn must not close as finished")
	}
}

// Inside a code fence the marker is an example being discussed, not a call.
func TestQoderKeepsAFencedMarkerAsText(t *testing.T) {
	echo := "```json\n[assistant 请求调用工具]\n" + `[{"name":"read_file","arguments":"{}"}]` + "\n```\n"
	stream := qoderReadEcho(t, qoderEchoFrame(t, echo), nil)
	if len(stream.names) != 0 {
		t.Fatalf("a fenced example must not become a call: %v", stream.names)
	}
	if !strings.Contains(stream.text, "请求调用工具") {
		t.Fatalf("the example has to reach the client: %q", stream.text)
	}
}

// The upstream sends a call's name on one frame and its arguments on the next.
// A call is not put on the wire until the name arrives, so the client never sees
// an empty name it cannot resolve to a tool.
func TestQoderHoldsANativeCallUntilItsNameArrives(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 0, "id": "call_abc", "function": map[string]any{"arguments": `{"path":`},
	}}}, "", nil) +
		qoderChunkFrame(t, map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"name": "read_file", "arguments": `"a.go"}`},
		}}}, "tool_calls", nil)
	stream := qoderReadEcho(t, reply, nil)
	if len(stream.names) != 1 || stream.names[0] != "read_file" {
		t.Fatalf("the held call was not released with its name: %v", stream.names)
	}
	if !strings.Contains(stream.args, `"path"`) || !strings.Contains(stream.args, `"a.go"`) {
		t.Fatalf("the arguments that arrived first were lost: %q", stream.args)
	}
	if !strings.Contains(stream.raw, "call_abc") {
		t.Fatalf("the upstream's own id must survive: %s", stream.raw)
	}
}

// A call that never gets a name is not one: the turn finishes normally rather
// than handing the client a tool it cannot look up.
func TestQoderDropsANativeCallThatNeverGetsAName(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 0, "id": "call_x", "function": map[string]any{"arguments": `{}`},
	}}}, "tool_calls", nil)
	stream := qoderReadEcho(t, reply, nil)
	if len(stream.names) != 0 {
		t.Fatalf("a nameless call must not reach the client: %v", stream.names)
	}
	if stream.finish != "stop" {
		t.Fatalf("finish reason = %q, want stop", stream.finish)
	}
}

// A block that never closes is released as text once it has held back too much:
// the answer must not be swallowed by a marker the model never finished.
func TestQoderReleasesAnUnterminatedBlockPastTheHoldLimit(t *testing.T) {
	huge := qoderCallOpen + strings.Repeat("x", qoderEchoHoldMaxChars+1)
	stream := qoderReadEcho(t, qoderEchoFrame(t, huge), nil)
	if !strings.Contains(stream.text, qoderCallOpen) {
		t.Fatalf("the block was never released: %d chars of text", len(stream.text))
	}
	if len(stream.errs) > 0 {
		t.Fatalf("an over-long block is not a protocol failure: %v", stream.errs)
	}
}
