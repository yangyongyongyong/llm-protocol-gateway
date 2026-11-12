package gateway

import (
	"strings"
	"testing"
)

// newlines is the blank line that ends an SSE frame.
const newlines = "\n\n"

const (
	claudeTextDeltaStop = `event: content_block_delta` + newlines +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"your word is message_stop"}}` + newlines
	claudeTextDeltaDone = `event: content_block_delta` + newlines +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"your word is [DONE]"}}` + newlines
	chatChunkOK      = `data: {"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}` + newlines
	responsesCreated = `event: response.created` + newlines +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-x"}}` + newlines
)

// closeCase is one body and the verdict its own bytes demand.
type closeCase struct {
	name string
	body string
	want bool
}

// closingCases covers the two ways a stream can lie about being complete: its
// answer quoting the closing marker at the very end, and a real closing frame
// that is not the last frame of the body.
func closingCases() []closeCase {
	return []closeCase{
		// The answer's own text is not a closing frame.
		{name: "claude text ends with stop word", want: false,
			body: claudeTextDeltaStop},
		{name: "claude text ends with stop word then blank", want: false,
			body: claudeTextDeltaStop + newlines + "\n"},
		{name: "claude text ends with [DONE] then blank", want: false,
			body: chatChunkOK + claudeTextDeltaDone + newlines + "\n"},
		{name: "chat frame quoting [DONE] as its last line", want: false,
			body: `data: {"choices":[{"delta":{"content":"[DONE]"}}]}` + newlines + "\n\n"},
		{name: "responses frame quoting completed as its last line", want: false,
			body: responsesCreated + `data: {"type":"response.output_text.delta","delta":"response.completed"}` + newlines + "\n\n"},
		{name: "claude frame whose text carries a type member", want: false,
			body: `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"\"type\":\"message_stop\""}}` + newlines + "\n\n"},

		// A real closing frame, as the last frame of the body.
		{name: "claude message_stop event and data", want: true,
			body: claudeTextDeltaStop + newlines + "\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{name: "claude message_stop data only", want: true,
			body: claudeTextDeltaStop + newlines + "\ndata: {\"type\":\"message_stop\"}" + newlines + "\n\n"},
		{name: "chat [DONE]", want: true, body: chatChunkOK + "data: [DONE]\n\n"},
		{name: "chat [DONE] no blank line", want: true, body: chatChunkOK + "data: [DONE]\n"},
		{name: "chat [DONE] terminated by EOF", want: true, body: chatChunkOK + "data: [DONE]"},
		{name: "responses completed", want: true,
			body: responsesCreated + newlines + "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_1\"}}\n\n"},
		// OpenAI Responses 的结束帧把完整 response 放在前面，顶层 type 可能
		// 落在扫描窗口之后（OpenCode Go 的 response.completed 实测就是这样）。
		{name: "responses completed with type past the scan window", want: true,
			body: responsesCreated + newlines + `event: response.completed` + newlines +
				`data: {"response":{"id":"resp_1","output_text":"` + strings.Repeat("a", 2000) + `"},"type":"response.completed","sequence_number":2}` + newlines},
		{name: "responses incomplete", want: true,
			body: responsesCreated + newlines + "data: {\"type\":\"response.incomplete\",\"sequence_number\":2}\n\n"},
		{name: "responses failed", want: true,
			body: responsesCreated + newlines + "data: {\"type\":\"response.failed\",\"sequence_number\":2}\n\n"},

		// A closing frame that is not the last frame ends nothing by itself: the
		// words after it are text the client has to keep.
		{name: "chat chunk after [DONE]", want: false,
			body: chatChunkOK + "data: [DONE]\n\ndata: {\"id\":\"chatcmpl-y\",\"choices\":[{\"delta\":{\"content\":\"more\"}}]}\n\n"},
		{name: "chat [DONE] then a cut frame", want: false,
			body: chatChunkOK + "data: [DONE]\n\n" + `data: {"choices":[{"delta":{"content":"more"}}]}`},
		{name: "claude delta after message_stop", want: false,
			body: claudeTextDeltaStop + newlines + "\ndata: {\"type\":\"message_stop\"}\n\n" + claudeTextDeltaStop},
		{name: "claude message_stop then blank", want: true,
			body: claudeTextDeltaStop + newlines + "\ndata: {\"type\":\"message_stop\"}\n\n\n"},

		// A frame quoted on a line the upstream then cut is not the last frame.
		{name: "chat quoting done then cut mid line", want: false,
			body: chatChunkOK + "data: [DONE]\n\n" + `data: {"choices":[{"delta":{"content":"choi"`},

		// A body that is not a stream has no closing frame to wait for.
		{name: "empty", want: false, body: ""},
		{name: "plain json", want: true, body: `{"choices":[]}`},
	}
}

func TestStreamEndReaderClosingFrame(t *testing.T) {
	for _, testCase := range closingCases() {
		t.Run(testCase.name, func(t *testing.T) {
			if got := closesUpstreamStream([]byte(testCase.body)); got != testCase.want {
				t.Fatalf("closesUpstreamStream=%v, want %v for %q", got, testCase.want, testCase.body)
			}
		})
	}
}

// The verdict may not depend on where the transport cut the chunks: every
// boundary of a body is writing it in two parts.
func TestStreamEndReaderClosingFrameSplitAtEveryBoundary(t *testing.T) {
	bodies := []string{
		claudeTextDeltaStop + newlines + "\ndata: {\"type\":\"message_stop\"}\n\n",
		claudeTextDeltaStop,
		chatChunkOK + "data: [DONE]\n\n",
		chatChunkOK + `data: {"choices":[{"delta":{"content":"[DONE]"}}]}` + newlines + "\n\n",
		responsesCreated + newlines + "data: {\"type\":\"response.incomplete\",\"sequence_number\":2}\n\n",
	}
	for _, body := range bodies {
		want := closesUpstreamStream([]byte(body))
		for split := 0; split <= len(body); split++ {
			reader := &streamEndReader{}
			reader.Write([]byte(body[:split]))
			reader.Write([]byte(body[split:]))
			if got := reader.closed(); got != want {
				t.Fatalf("split at %d: closed()=%v, want %v for %q|%q", split, got, want, body[:split], body[split:])
			}
		}
		// The same body one byte at a time.
		reader := &streamEndReader{}
		for index := range body {
			reader.Write([]byte{body[index]})
		}
		if got := reader.closed(); got != want {
			t.Fatalf("byte at a time: closed()=%v, want %v for %q", got, want, body)
		}
	}
}

// A closing event belongs to the stream that writes it: a Responses terminal
// event does not close a body of Chat chunks, and a Chat [DONE] does not close a
// body of Responses events. The other direction — a body whose own protocol's
// terminal event follows its chunks — is a real close.

func TestStreamEndReaderClosingFrameTypeFollowsTheProtocol(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "chat chunks then a responses event", want: false,
			body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + newlines +
				`data: {"type":"response.completed"}` + newlines},
		{name: "chat chunks then a responses event at EOF", want: false,
			body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + newlines +
				`data: {"type":"response.completed"}`},
		{name: "responses events then a chat done", want: false,
			body: `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}` + newlines +
				`data: [DONE]` + newlines},
		{name: "responses events then their own terminal event", want: true,
			body: `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1"}}` + newlines +
				`data: {"type":"response.completed","sequence_number":2}` + newlines},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := closesUpstreamStream([]byte(testCase.body)); got != testCase.want {
				t.Fatalf("closesUpstreamStream=%v, want %v for %q", got, testCase.want, testCase.body)
			}
		})
	}
}

// The Claude fold still needs every data line, and the closing scan must not
// change what it learns.
func TestStreamEndReaderClosingScanKeepsClaudeFold(t *testing.T) {
	body := `event: message_start` + newlines + "\n" + `data: {"type":"message_start","message":{"id":"msg_1"}}` + newlines + "\n\n" +
		`data: {"type":"content_block_start","index":0}` + newlines + "\n\n" +
		`data: {"type":"content_block_start","index":1}` + newlines + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + newlines + "\n\n" +
		claudeTextDeltaStop
	reader := &streamEndReader{}
	reader.Write([]byte(body))
	if reader.closed() {
		t.Fatal("a text delta is not a closing frame")
	}
	cut := reader.cut(TokenUsage{}, int64(len(body)), []byte(body))
	if !cut.messageStarted || len(cut.openBlocks) != 1 || cut.openBlocks[0] != 1 {
		t.Fatalf("cut = started %v blocks %v, want started with [1]", cut.messageStarted, cut.openBlocks)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatal("fixture lost its stop word")
	}
}
