package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// claudeSSE joins frames into the wire shape Anthropic sends.
func claudeSSE(frames ...string) string {
	var out strings.Builder
	for _, frame := range frames {
		out.WriteString(frame)
		out.WriteString("\n\n")
	}
	return out.String()
}

func claudeMessageStart() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"
}

func claudeThinkingBlock() []string {
	return []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
	}
}

func claudeClose(stopReason string) []string {
	return []string{
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":9}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	}
}

// The exact turn the operator saw as "the assistant said one line and stopped":
// a completed turn carrying a thinking block and nothing the client can read.
func TestPassThroughReportsThinkingOnlyEmptyTurn(t *testing.T) {
	frames := append([]string{claudeMessageStart()}, claudeThinkingBlock()...)
	frames = append(frames, claudeClose("end_turn")...)
	body := claudeSSE(frames...)

	reported := 0
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamClaude, ParseClaudeUsage,
		claudeStreamInterrupter{onEmptyTurn: func() { reported++ }}); err != nil {
		t.Fatalf("writePassThroughResponse: %v", err)
	}
	if reported != 1 {
		t.Fatalf("empty turn reported %d times, want 1", reported)
	}
}

// A turn that answered is not an empty one, however much it thought first.
func TestPassThroughDoesNotReportAnsweredTurn(t *testing.T) {
	frames := append([]string{claudeMessageStart()}, claudeThinkingBlock()...)
	frames = append(frames,
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"PONG\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}",
	)
	frames = append(frames, claudeClose("end_turn")...)
	body := claudeSSE(frames...)

	reported := 0
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamClaude, ParseClaudeUsage,
		claudeStreamInterrupter{onEmptyTurn: func() { reported++ }}); err != nil {
		t.Fatalf("writePassThroughResponse: %v", err)
	}
	if reported != 0 {
		t.Fatalf("answered turn reported as empty %d times, want 0", reported)
	}
}

// A tool call is visible output too: an agent turn that decided to call a tool
// is not the silent case.
func TestPassThroughToolUseIsVisibleOutput(t *testing.T) {
	body := claudeSSE(append(append([]string{claudeMessageStart()},
		claudeThinkingBlock()...),
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\",\"input\":{}}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}",
	)...)
	body += claudeSSE(claudeClose("tool_use")...)

	reported := 0
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamClaude, ParseClaudeUsage,
		claudeStreamInterrupter{onEmptyTurn: func() { reported++ }}); err != nil {
		t.Fatalf("writePassThroughResponse: %v", err)
	}
	if reported != 0 {
		t.Fatalf("tool turn reported as empty %d times, want 0", reported)
	}
}

// A stream the upstream cut is closeInterruptedStream's business, not this one.
func TestClaudeStreamInterrupterDoesNotReportCutStream(t *testing.T) {
	reported := 0
	interrupter := claudeStreamInterrupter{onEmptyTurn: func() { reported++ }}
	interrupter.reportStreamTurn(streamCut{messageStarted: true, closed: false})
	if reported != 0 {
		t.Fatalf("cut stream reported as an empty turn %d times, want 0", reported)
	}
	interrupter.reportStreamTurn(streamCut{messageStarted: true, closed: true})
	if reported != 1 {
		t.Fatalf("closed empty turn reported %d times, want 1", reported)
	}
	interrupter.reportStreamTurn(streamCut{messageStarted: true, closed: true, sawVisibleOutput: true})
	if reported != 1 {
		t.Fatalf("answered turn reported, count = %d, want 1", reported)
	}
}

// The reader is the one place that sees every byte, so the verdict has to come
// from it rather than from the capped log copy.
func TestStreamEndReaderTracksVisibleOutput(t *testing.T) {
	read := func(frames ...string) bool {
		reader := &streamEndReader{}
		_, _ = reader.Write([]byte(claudeSSE(frames...)))
		return reader.visibleOutput()
	}

	thinkingOnly := append(append([]string{claudeMessageStart()}, claudeThinkingBlock()...), claudeClose("end_turn")...)
	if read(thinkingOnly...) {
		t.Error("a thinking-only stream counted as visible output")
	}

	withText := append(append([]string{claudeMessageStart()},
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
	), claudeClose("end_turn")...)
	if !read(withText...) {
		t.Error("a text block was not counted as visible output")
	}
}
