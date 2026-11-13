package gateway

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestIsUpstreamStreamInterruption(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unexpected eof", fmt.Errorf("openai stream: %w", io.ErrUnexpectedEOF), true},
		{"wrapped eof", io.EOF, true},
		{"connection reset", errors.New("read tcp 10.0.0.1:443: read: connection reset by peer"), true},
		{"remote disconnect", errors.New("http2: server sent GOAWAY"), true},
		{"empty stream", fmt.Errorf("openai stream ended without any chunks"), true},
		{"thinking only", errThinkingOnlyEmptyResponse, true},
		// A converter-side failure is not an upstream interruption: the next
		// provider would fail the same way.
		{"converter bug", errors.New("json: cannot unmarshal array into Go value"), false},
	}
	for _, tc := range cases {
		if got := isUpstreamStreamInterruption(tc.err); got != tc.want {
			t.Fatalf("%s: err=%v got=%v want=%v", tc.name, tc.err, got, tc.want)
		}
	}
	if !isUpstreamStreamInterruptedResponse(&http.Response{}, false) {
		t.Fatal("a nil body means the upstream never finished sending")
	}
}

// A converted stream that reached the client with its first byte belongs to
// the account that started it: the cut is closed on the wire and reported, and
// the failover loop is told not to replay it — even though the attempt could
// still be deferred, it no longer can be.
func TestFinishConvertedProxyMidStreamDropIsNotRetriedAfterFirstByte(t *testing.T) {
	s := &Server{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	rec := httptest.NewRecorder()
	fw := newFailoverResponseWriter(rec, true)
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		if _, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")); err != nil {
			return TokenUsage{}, err
		}
		return TokenUsage{}, newUpstreamStreamInterruption(io.ErrUnexpectedEOF, 64)
	}
	status, _, _, err, retryable := s.finishConvertedProxy(fw, resp, "glm-4.5", true, nil, streamConvert)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if err == nil || !errors.Is(err, errUpstreamStreamInterrupted) {
		t.Fatalf("the cut must be reported, got err=%v", err)
	}
	if retryable {
		t.Fatalf("a turn the client already started must not be replayed (err=%v)", err)
	}
	if got := rec.Body.String(); !strings.Contains(got, "partial") {
		t.Fatalf("client should keep the prefix it received, got %q", got)
	}
}

// A request with no provider chain never gets a failover buffer; the stream
// goes straight to the client, so an interruption simply ends the turn.
func TestFinishConvertedProxyMidStreamDropEndsStreamWithoutFailoverWriter(t *testing.T) {
	s := &Server{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	rec := httptest.NewRecorder()
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		if _, err := w.Write([]byte("data: partial" + "\n\n")); err != nil {
			return TokenUsage{}, err
		}
		return TokenUsage{}, newUpstreamStreamInterruption(io.ErrUnexpectedEOF, 64)
	}
	if _, _, _, err, retryable := s.finishConvertedProxy(rec, resp, "glm-4.5", true, nil, streamConvert); retryable {
		t.Fatalf("unbuffered stream must not be retried, err=%v", err)
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("client body=%q", rec.Body.String())
	}
}

// The last attempt of the loop has no backup left; its cut turn is still the
// answer the client already holds, and it is delivered once, never replayed.
func TestFinishConvertedProxyMidStreamDropOnLastAttemptDeliversOnce(t *testing.T) {
	s := &Server{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	rec := httptest.NewRecorder()
	fw := newFailoverResponseWriter(rec, false)
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		if _, err := w.Write([]byte("data: partial" + "\n\n")); err != nil {
			return TokenUsage{}, err
		}
		return TokenUsage{}, newUpstreamStreamInterruption(io.ErrUnexpectedEOF, 64)
	}
	status, _, _, err, retryable := s.finishConvertedProxy(fw, resp, "glm-4.5", true, nil, streamConvert)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if err == nil || !errors.Is(err, errUpstreamStreamInterrupted) {
		t.Fatalf("the cut must be reported, got err=%v", err)
	}
	if retryable {
		t.Fatalf("the last attempt has nowhere to go, got retryable")
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("the half answer must not vanish: %q", rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "partial") != 1 {
		t.Fatalf("the half answer was replayed: %q", rec.Body.String())
	}
}

// Same-provider empty-stream retry is still bounded; an interruption that
// dropped headers only is retryable, but a converter bug is not.
func TestFinishConvertedProxyInterruptionClassification(t *testing.T) {
	s := &Server{}
	run := func(streamErr error) (bool, string) {
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}
		rec := httptest.NewRecorder()
		streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
			return TokenUsage{}, streamErr
		}
		_, _, _, _, retryable := s.finishConvertedProxy(rec, resp, "m", true, nil, streamConvert)
		return retryable, rec.Body.String()
	}
	if retryable, _ := run(newUpstreamStreamInterruption(io.ErrUnexpectedEOF, 0)); !retryable {
		t.Fatal("headers-only interruption should be retryable")
	}
	if retryable, _ := run(errors.New("json: cannot unmarshal array")); retryable {
		t.Fatal("a converter bug must surface, not retry")
	}
}

// End to end: the primary opens the stream and then aborts before finishing its
// turn. The client's time-to-first-token is what streaming is for, so the turn
// it received is the answer: it is closed on the wire (max_tokens) and the
// backup is not asked to replay it into a client that already has half of it.
func TestMidStreamInterruptionClosesTheTurnInsteadOfReplaying(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"half an ans\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Abort without message_delta/message_stop.
		panic(http.ErrAbortHandler)
	}))
	defer primary.Close()

	var backupHits int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"the whole answer\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer backup.Close()

	state := newStreamDropState(t, primary.URL, backup.URL)
	server := NewServer(NewRouter(state), monitor.NewStore())

	route, err := server.router.RouteByID("stream-drop-route")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	dropKey := streamDropKey(route.ID, "stream-drop-backup")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	status, _, _, _, model, execErr := server.executeProtocolFlowWithFailover(
		rec, req, route, streamDropDecision(route.ID, route.ProviderID),
		"client-model", map[string]any{"model": "client-model", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, dropKey, true,
	)
	if !errors.Is(execErr, errUpstreamStreamInterrupted) {
		t.Fatalf("a cut turn must be reported as an interruption, got %v", execErr)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if model != "client-model" {
		t.Fatalf("model=%s", model)
	}
	if backupHits != 0 {
		t.Fatalf("backup hits=%d, want 0: a started turn is not replayed", backupHits)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "half an ans") {
		t.Fatalf("client did not get the streamed prefix: %q", body)
	}
	if strings.Contains(body, "the whole answer") {
		t.Fatalf("client saw a replayed answer on top of its prefix: %q", body)
	}
	// A cut turn is told as an error event, not as a stop_reason the model never
	// chose (magpie's streamFailure), and still ends the stream properly.
	if !strings.Contains(body, "event: error") || strings.Count(body, "event: message_stop") != 1 {
		t.Fatalf("the cut turn was not closed: %q", body)
	}
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("the stream must not be repeated: %q", body)
	}
}

// Without any backup the same drop must end the turn instead of replaying the
// same provider and duplicating text.
func TestNoFailoverReplayAfterMidStreamInterruption(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"once\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Give the bytes time to travel before the connection dies; the abort
		// can otherwise discard what the server already queued.
		time.Sleep(50 * time.Millisecond)
		panic(http.ErrAbortHandler)
	}))
	defer upstream.Close()

	state := newStreamDropState(t, upstream.URL, "")
	server := NewServer(NewRouter(state), monitor.NewStore())

	route, err := server.router.RouteByID("stream-drop-route")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	dropKey := streamDropKey(route.ID)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	_, _, _, _, _, _ = server.executeProtocolFlowWithFailover(
		rec, req, route, streamDropDecision(route.ID, route.ProviderID),
		"client-model", map[string]any{"model": "client-model", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		domain.ProtocolClaude, true, dropKey, true,
	)
	if hits != 1 {
		t.Fatalf("upstream hits=%d, want 1 (no same-provider replay)", hits)
	}
	if got := strings.Count(rec.Body.String(), `"once"`); got != 1 {
		t.Fatalf("client saw the text %d times: %q", got, rec.Body.String())
	}
	// The opening frames leave with the committed headers, ahead of the stream
	// the client still lacks; closing the turn must not replay them.
	if got := strings.Count(rec.Body.String(), "event: message_start"); got != 1 {
		t.Fatalf("client saw message_start frames %d times: %q", got, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), "event: content_block_start"); got != 1 {
		t.Fatalf("client saw content_block_start %d times: %q", got, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), "event: message_stop"); got != 1 {
		t.Fatalf("cut turn should be closed once, got %d message_stop: %q", got, rec.Body.String())
	}
}

// The Responses client path answers through the same converter and must not
// leave the client hanging at EOF: a truncated upstream turn is closed with
// response.incomplete instead of a normal response.completed.
func TestClaudeToResponsesInterruptedStreamEmitsIncomplete(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":5,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
		``,
	}, "\n")
	rec := httptest.NewRecorder()
	gated := newVisibilityGatedSSEWriter(rec)
	_, err := streamClaudeToResponsesEventsDirect(gated, io.MultiReader(strings.NewReader(upstreamSSE), errReader{}), "claude-x", nil)
	if !isUpstreamStreamInterruption(err) {
		t.Fatalf("want interruption err, got %v", err)
	}
	gated.markVisible()
	out := rec.Body.String()
	if strings.Contains(out, "response.completed") {
		t.Fatalf("truncated turn must not look complete: %q", out)
	}
	if !strings.Contains(out, "response.output_text.delta") || !strings.Contains(out, "cut") {
		t.Fatalf("client should keep the text it got: %q", out)
	}
	if !strings.Contains(out, "response.failed") {
		t.Fatalf("truncated turn must end response.failed, got %q", out)
	}
	if !strings.Contains(out, `"code":"server_error"`) {
		t.Fatalf("missing failure reason: %q", out)
	}
}
func TestUpstreamStreamInterruptedResponse(t *testing.T) {
	open := &http.Response{StatusCode: 200, Body: io.NopCloser(&errReader{}), ContentLength: -1}
	if !isUpstreamStreamInterruptedResponse(open, false) {
		t.Fatal("a stream without its closing frame should count as interrupted")
	}
	if isUpstreamStreamInterruptedResponse(open, true) {
		t.Fatal("a stream that sent its closing frame is complete")
	}
	chunked, err := http.ReadResponse(bufio.NewReader(strings.NewReader(
		"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n"+
			"11\r\ndata: [DONE]\n\n\r\n0\r\n\r\n")), nil)
	if err != nil {
		t.Fatalf("read chunked response: %v", err)
	}
	if !isUpstreamStreamInterruptedResponse(chunked, false) {
		t.Fatal("a live chunked body is not known to be truncated")
	}
	for _, body := range []string{
		"data: {\"type\":\"message_stop\"}\n\n",
		"data: [DONE]\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	} {
		if !closesUpstreamStream([]byte(body)) {
			t.Fatalf("closing frame not recognised: %q", body)
		}
	}
	for _, body := range []string{"", "data: {\"delta\":\"half", "data: {\"a\":1}\n\ndata: [DO"} {
		if closesUpstreamStream([]byte(body)) {
			t.Fatalf("partial body taken for complete: %q", body)
		}
	}
	// A body that is not a stream has no closing frame to wait for; a heartbeat
	// alone is a live stream that stops before its first event.
	for _, body := range []string{": ping\n\n", `{"choices":[]}`} {
		if !closesUpstreamStream([]byte(body)) {
			t.Fatalf("a body that never spoke SSE has nothing to close: %q", body)
		}
	}
}

// newStreamDropState builds a two-account chain: the primary (Claude-protocol
// passthrough target for the test's own URL) and an optional backup.
func newStreamDropState(t *testing.T, primaryURL, backupURL string) domain.GatewayState {
	t.Helper()
	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "stream-drop-primary", Name: "stream-drop-primary", Protocol: domain.ProtocolClaude,
			BaseURL: primaryURL, AuthType: "api_key", APIKeySource: "static", HealthStatus: "healthy",
		}},
		Routes: []domain.Route{{
			ID: "stream-drop-route", Name: "stream-drop-route", ProviderID: "stream-drop-primary",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		}},
	}
	if backupURL != "" {
		state.Providers = append(state.Providers, domain.Provider{
			ID: "stream-drop-backup", Name: "stream-drop-backup", Protocol: domain.ProtocolClaude,
			BaseURL: backupURL, AuthType: "api_key", APIKeySource: "static", HealthStatus: "healthy",
		})
		state.Routes = append(state.Routes, domain.Route{
			ID: "stream-drop-backup-route", Name: "stream-drop-backup-route", ProviderID: "stream-drop-backup",
			OutputProtocol: domain.ProtocolClaude, Enabled: true,
		})
	}
	return state
}

func streamDropKey(routeID string, fallbacks ...string) domain.APIKey {
	return domain.APIKey{ID: "stream-drop-key", RouteID: routeID, FallbackProviderIDs: fallbacks}
}

func streamDropDecision(routeID, providerID string) domain.RouteDecision {
	return domain.RouteDecision{RouteID: routeID, ProviderID: providerID}
}

// errReader fails the way a dropped connection does, before EOF.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// The attempt committed the start of its turn, then streamed the rest to the
// client before the upstream died. Closing that turn must add the closing
// frames only — never a second copy of what the client already has.
func TestFailoverWriterCutTurnClosesAfterCommittedPrefix(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := newFailoverResponseWriter(rec, false)
	fw.Header().Set("Content-Type", "text/event-stream")
	opening := "event: message_start\ndata: {}\n\n"
	if _, err := io.WriteString(fw, opening); err != nil {
		t.Fatal(err)
	}
	fw.FlushError()
	// The attempt is the answer from its first byte: what went out is a prefix
	// of the turn, not something a cut turn is closed on top of.
	if got := fw.Prefix(); len(got) != 0 {
		t.Fatalf("a turn already sent reported a replayable prefix %q", got)
	}
	if got := rec.Body.String(); got != opening {
		t.Fatalf("client got %q, want the opening %q", got, opening)
	}
	// The attempt keeps streaming after its commit; the client gets those
	// bytes as they come.
	rest := "event: content_block_delta\ndata: {\"type\":\"text_delta\",\"text\":\"once\"}\n\n"
	if _, err := io.WriteString(fw, rest); err != nil {
		t.Fatal(err)
	}
	// The upstream then died without closing the turn: the attempt already
	// delivered opening+rest, so closing it must add only the closing frames.
	claudeStreamInterrupter{}.closeInterruptedStream(fw, cutFromBody(fw.body.Bytes()))
	fw.FlushError()
	want := opening + rest +
		"event: error" + "\n" +
		`data: {"error":{"message":"connection lost mid-reply","type":"api_error"},"type":"error"}` + "\n\n" +
		"event: message_stop" + "\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	if out := rec.Body.String(); out != want {
		t.Fatalf("client body %q, want %q", out, want)
	}
}

// A commit that carried nothing sent nothing of the turn: the stream that
// follows it is the whole turn, so it is delivered whole, once.
func TestFailoverWriterCutTurnAfterEmptyCommitSendsWholeBody(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := newFailoverResponseWriter(rec, false)
	fw.Header().Set("Content-Type", "text/event-stream")
	fw.WriteHeader(http.StatusOK)
	if got := fw.Prefix(); len(got) != 0 {
		t.Fatalf("empty commit reported a prefix %q", got)
	}
	body := "event: message_start\ndata: {}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	turn := fw.deliver([]byte(body), http.StatusOK)
	claudeStreamInterrupter{}.closeInterruptedStream(turn, cutFromBody(turn.body.Bytes()))
	turn.FlushError()
	out := rec.Body.String()
	if got := strings.Count(out, "event: message_start"); got != 1 {
		t.Fatalf("client saw the opening %d times: %q", got, out)
	}
	if !strings.HasSuffix(out, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("cut turn not closed: %q", out)
	}
}

// A cut turn leaves its open blocks in the recorded body: closing it must stop
// every block it started, and only those, before ending the turn.
func TestClaudeReplayClosesOnlyBlocksLeftOpen(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"read"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`,
		``,
	}, "\n")
	rec := httptest.NewRecorder()
	claudeStreamInterrupter{}.closeInterruptedStream(rec, cutFromBody([]byte(body)))
	out := rec.Body.String()
	if got := strings.Count(out, "event: content_block_stop"); got != 1 {
		t.Fatalf("closing must stop the single open block, got %d: %q", got, out)
	}
	if !strings.Contains(out, `{"index":1,"type":"content_block_stop"}`) && !strings.Contains(out, `"index":1`) {
		t.Fatalf("the open tool block was not stopped: %q", out)
	}
	if strings.Contains(out, `"index":0,"type":"content_block_stop"`) {
		t.Fatalf("an already stopped block was stopped twice: %q", out)
	}
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("turn not ended: %q", out)
	}
}
