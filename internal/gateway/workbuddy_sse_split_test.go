package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// The shape issue #197 reports: two chunks in one data line. Both have to come out, in
// order, each parseable on its own.
func TestSplitWorkBuddyConcatenatedJSONSplitsTwoObjects(t *testing.T) {
	payload := `{"id":"a","choices":[]} {"id":"b","choices":[]}`
	parts := splitWorkBuddyConcatenatedJSON(payload)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 (%v)", len(parts), parts)
	}
	for index, part := range parts {
		if !json.Valid([]byte(part)) {
			t.Fatalf("part %d is not valid JSON: %q", index, part)
		}
	}
	if !strings.Contains(parts[0], `"a"`) || !strings.Contains(parts[1], `"b"`) {
		t.Fatalf("order or content lost: %v", parts)
	}
}

// No separator at all is the other concatenation the upstream produces.
func TestSplitWorkBuddyConcatenatedJSONHandlesNoSeparator(t *testing.T) {
	parts := splitWorkBuddyConcatenatedJSON(`{"a":1}{"b":2}{"c":3}`)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3 (%v)", len(parts), parts)
	}
}

// Braces inside strings, escapes, and nested objects must not confuse the scanner.
func TestSplitWorkBuddyConcatenatedJSONRespectsStringsAndNesting(t *testing.T) {
	payload := `{"text":"a } brace { and \" quote"} {"nested":{"deep":[1,2]},"s":"x"}`
	parts := splitWorkBuddyConcatenatedJSON(payload)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 (%v)", len(parts), parts)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(parts[0]), &first); err != nil {
		t.Fatalf("first part invalid: %v (%q)", err, parts[0])
	}
	if !strings.Contains(first["text"].(string), "brace") {
		t.Fatalf("string content mangled: %q", first["text"])
	}
}

// Anything it cannot cut cleanly comes back whole, so callers keep their old behaviour.
func TestSplitWorkBuddyConcatenatedJSONLeavesUnparsableAlone(t *testing.T) {
	cases := []string{
		``,
		`[DONE]`,
		`{"a":1`,
		`{"a":1} {"b":`,
		`not json at all`,
		`{"a":1} trailing`,
		`"bare string"`,
		`123`,
	}
	for _, payload := range cases {
		parts := splitWorkBuddyConcatenatedJSON(payload)
		if len(parts) != 1 || parts[0] != payload {
			t.Fatalf("%q -> %v, want it returned unchanged", payload, parts)
		}
	}
}

// A single object is not a split.
func TestSplitWorkBuddyConcatenatedJSONSingleValue(t *testing.T) {
	payload := `{"id":"only","choices":[{"delta":{"content":"hi"}}]}`
	parts := splitWorkBuddyConcatenatedJSON(payload)
	if len(parts) != 1 || parts[0] != payload {
		t.Fatalf("single object was rewritten: %v", parts)
	}
}

// A concatenated SSE line becomes one `data:` line per value, keeping the trailing
// newline so the client's frame splitter still works.
func TestSplitWorkBuddySSELineRewritesIntoFrames(t *testing.T) {
	line := []byte("data: {\"a\":1} {\"b\":2}\n")
	got := string(splitWorkBuddySSELine(line))
	want := "data: {\"a\":1}\ndata: {\"b\":2}\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A CRLF stream keeps CRLF.
	crlf := string(splitWorkBuddySSELine([]byte("data: {\"a\":1}{\"b\":2}\r\n")))
	if crlf != "data: {\"a\":1}\r\ndata: {\"b\":2}\r\n" {
		t.Fatalf("CRLF handling: %q", crlf)
	}
}

// Lines the splitter has no business in come back byte for byte.
func TestSplitWorkBuddySSELineLeavesOtherLinesAlone(t *testing.T) {
	for _, line := range []string{
		": keepalive\n",
		"event: message_start\n",
		"data: [DONE]\n",
		"data: {\"only\":1}\n",
		"\n",
		"data: \n",
	} {
		if got := string(splitWorkBuddySSELine([]byte(line))); got != line {
			t.Fatalf("%q was rewritten to %q", line, got)
		}
	}
}

// The aggregator used to drop a concatenated frame entirely, because a whole-line parse
// fails and the loop skipped it. Every value in the frame now reaches the answer.
func TestWorkBuddyStreamPayloadsKeepsEveryValue(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n" +
		"\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]} {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n" +
		"data: [DONE]\n")
	payloads := workBuddyStreamPayloads(body)
	if len(payloads) != 3 {
		t.Fatalf("payloads = %d, want 3 (%v)", len(payloads), payloads)
	}
	for index, payload := range payloads {
		if !json.Valid([]byte(payload)) {
			t.Fatalf("payload %d invalid: %q", index, payload)
		}
	}
}

// And the aggregate built from it carries the text from both halves of the frame.
func TestBuildWorkBuddyCompletionKeepsConcatenatedFrameText(t *testing.T) {
	stream := []byte(`data: {"id":"c1","model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":"前半"}}]}` + "\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"后半"}}]} {"choices":[{"index":0,"delta":{"content":"尾巴"},"finish_reason":"stop"}]}` + "\n" +
		`data: [DONE]` + "\n")
	encoded, err := workBuddyStreamAsCompletion(stream, "glm-5.3")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	var completion map[string]any
	if err := json.Unmarshal(encoded, &completion); err != nil {
		t.Fatalf("aggregate is not JSON: %v", err)
	}
	if text := workBuddyCompletionText(completion); text != "前半后半尾巴" {
		t.Fatalf("text = %q, want all three parts", text)
	}
}

// Comments and blank separators are not payloads; the heartbeat must not be parsed.
func TestWorkBuddyStreamPayloadsSkipsComments(t *testing.T) {
	body := []byte(": keepalive\n\ndata: {\"a\":1}\n")
	payloads := workBuddyStreamPayloads(body)
	if len(payloads) != 1 || payloads[0] != `{"a":1}` {
		t.Fatalf("payloads = %v", payloads)
	}
}

// looksLikeJSON only decides whether a parse failure is worth a warning.
func TestWorkBuddyLooksLikeJSON(t *testing.T) {
	for _, payload := range []string{`{"a":1}`, `[1,2]`, "  {\"a\":1}"} {
		if !workBuddyLooksLikeJSON(payload) {
			t.Fatalf("%q should look like JSON", payload)
		}
	}
	for _, payload := range []string{"", "  ", "text", ": ping"} {
		if workBuddyLooksLikeJSON(payload) {
			t.Fatalf("%q should not look like JSON", payload)
		}
	}
}
