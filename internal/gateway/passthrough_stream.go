package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const passThroughLogBufferMax = 256 * 1024

// streamInterrupter closes a pass-through SSE stream whose upstream died after
// part of the answer was already forwarded.
//
// cut is what streamEndReader saw of the bytes that actually went out: which
// blocks a Claude turn left open, and the id, model and last sequence number a
// Responses stream had already used. It is read from every byte of the stream,
// not from the capped log copy, so a long answer is closed as accurately as a
// short one.
type streamInterrupter interface {
	closeInterruptedStream(w http.ResponseWriter, cut streamCut)
}

// streamTurnReporter is an interrupter that also wants the verdict of the
// streams it carried whole, not only of the ones it had to close. A turn the
// upstream finished on its own is nobody's error, yet a thinking-only one still
// leaves the client with an empty answer, and this is the only place on the
// pass-through path that can notice it.
type streamTurnReporter interface {
	reportStreamTurn(cut streamCut)
}

// claudeStreamInterrupter ends an Anthropic Messages stream that has no
// message_stop with a stop_reason, so the client shows the text it has and
// knows the turn did not finish cleanly.
type claudeStreamInterrupter struct {
	// onEmptyTurn, when set, is told that the upstream finished a Claude turn
	// with a stop_reason but never showed the client anything: the answer the
	// model chose to give was no answer at all.
	onEmptyTurn func()
}

func (claudeStreamInterrupter) closeInterruptedStream(w http.ResponseWriter, cut streamCut) {
	if !cut.messageStarted {
		// Nothing the client could be holding open: the turn ends as an error,
		// as magpie ends a cut reply, and not with a stop_reason the model never
		// chose.
		_ = writeClaudeSSE(w, "error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": streamCutMessage,
			},
		})
		_ = writeClaudeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
		return
	}
	// Every block left open is closed first: otherwise a client SDK waits for
	// the block's content_block_stop and never reads message_stop.
	_ = closeInterruptedClaudeStream(w, cut.turn(), cut.usage, int(cut.total))
}

// reportStreamTurn tells onEmptyTurn about a Claude turn that closed with no
// visible output. A cut stream is not this case — closeInterruptedStream owns
// it — so only a stream that carried its own closing frame counts.
func (c claudeStreamInterrupter) reportStreamTurn(cut streamCut) {
	if c.onEmptyTurn == nil || !cut.messageStarted || !cut.closed || cut.sawVisibleOutput {
		return
	}
	c.onEmptyTurn()
}

// firstStreamTurnReporter is the interrupter that wants every stream's verdict;
// nil when this protocol's interrupter does not report turns.
func firstStreamTurnReporter(end []streamInterrupter) streamTurnReporter {
	if len(end) == 0 || end[0] == nil {
		return nil
	}
	reporter, _ := end[0].(streamTurnReporter)
	return reporter
}

// streamCut is what the bytes of one forwarded stream say about how it ended:
// enough to close it in its own protocol, whatever its size.
type streamCut struct {
	usage          TokenUsage
	total          int64
	messageStarted bool
	openBlocks     []int
	// sawVisibleOutput says the Claude turn opened a block the client can read
	// (text or a tool call). A turn that closed without one is the thinking-only
	// empty answer.
	sawVisibleOutput bool
	// closed says the stream ended with its protocol's own closing frame.
	closed       bool
	id, model    string // Responses stream identity
	lastSequence int    // Responses: highest sequence_number already sent
}

// turn is the Claude view of the cut, in the shape the closing helper wants.
func (c streamCut) turn() claudeInterruptedTurn {
	turn := claudeInterruptedTurn{messageStarted: c.messageStarted, toolOpen: map[string]int{}}
	for _, index := range c.openBlocks {
		turn.toolOpen[fmt.Sprintf("block-%d", index)] = index
	}
	return turn
}

// openAIChatStreamInterrupter ends an OpenAI Chat Completions stream with a
// finish_reason=length chunk and [DONE], the only way a Chat client learns the
// answer stopped early.
type openAIChatStreamInterrupter struct {
	model string
}

func (o openAIChatStreamInterrupter) closeInterruptedStream(w http.ResponseWriter, _ streamCut) {
	// An error event, not a finish reason: "length" and [DONE] together tell the
	// client the model finished its answer, and a client that trusts them keeps
	// half a sentence as the whole of it. Magpie says the same thing here
	// (streamFailure, gateway.go:4940) and names the cause (cutMidReply, :4934).
	writeChatStreamFailure(w, o.model, streamCutMessage)
}

// streamCutMessage is what a client is told when the upstream connection died
// mid-reply: the same wording magpie uses, so a person reading the transcript
// knows it was the connection and not the model.
const streamCutMessage = "connection lost mid-reply"

// writeChatStreamFailure ends a Chat stream with the protocol's own error event.
func writeChatStreamFailure(w http.ResponseWriter, model, message string) {
	payload := map[string]any{
		"error": map[string]any{
			"message": model + ": " + message,
			"type":    "api_error",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(body)
	_, _ = io.WriteString(w, "\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// responsesStreamInterrupter ends an OpenAI Responses stream with
// response.incomplete instead of leaving the client at EOF.
type responsesStreamInterrupter struct{}

// closeInterruptedStream repeats the id, model and sequence number the stream
// it closes established. A Responses SDK keys its events on sequence_number and
// raises on one that repeats or skips, so a closing frame with a fresh number
// of its own would make the interruption look worse than the bare EOF it
// replaces.
func (responsesStreamInterrupter) closeInterruptedStream(w http.ResponseWriter, cut streamCut) {
	id, model, sequence := responsesStreamIdentity(cut)
	// response.failed, not response.incomplete: an incomplete answer is one the
	// model stopped on purpose (max_output_tokens), and a client that sees it
	// keeps the half sentence as the answer. Magpie reports a cut reply the same
	// way (streamFailure, gateway.go:4940).
	response := map[string]any{
		"id":     id,
		"object": "response",
		"status": "failed",
		"error": map[string]any{
			"code":    "server_error",
			"message": streamCutMessage,
		},
	}
	if model != "" {
		response["model"] = model
	}
	_ = writeResponsesSSEEvent(w, "response.failed", map[string]any{
		"type":            "response.failed",
		"sequence_number": sequence,
		"response":        response,
	})
}

// responsesStreamIdentity is the response id, model and the next unused
// sequence_number for the frame that closes a cut stream. A stream the upstream
// cut before it sent anything keeps the shape it would have had from the start.
func responsesStreamIdentity(cut streamCut) (id, model string, sequence int) {
	id, model, last := cut.id, cut.model, cut.lastSequence
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	if last < 0 {
		return id, model, 0
	}
	return id, model, last + 1
}

// escapeRune is the JSON string escape character, kept as a named constant so
// the scanner below does not have to spell it in a literal.
const escapeRune = 0x5C

// trimToEventEnd cuts a body at the end of its last JSON object, since the
// stream's last line may be a partial frame the upstream never finished.
func trimToEventEnd(stream []byte) []byte {
	depth := 0
	inString := false
	escaped := false
	for index, char := range stream {
		switch {
		case escaped:
			escaped = false
		case char == escapeRune && inString:
			escaped = true
		case char == '"':
			inString = !inString
		case inString:
		case char == '{':
			depth++
		case char == '}':
			depth--
			if depth == 0 {
				return stream[:index+1]
			}
		}
	}
	return stream
}

// limitedBuffer keeps up to max bytes for logging/usage parsing while the
// remainder of a stream is still forwarded to the client. It also counts every
// byte written so callers can report true forwarded size despite the cap.
//
// 写方是流拷贝与心跳两个 goroutine，读方是收尾时的日志/用量解析，所以要加锁；
// Bytes 返回副本，否则调用方读到的底层数组会被后续 append 改写。
type limitedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	total int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if b.max <= 0 {
		return len(p), nil
	}
	if len(b.buf) < b.max {
		remain := b.max - len(b.buf)
		if len(p) > remain {
			b.buf = append(b.buf, p[:remain]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

// Total is the full number of bytes streamed, including any beyond the cap.
func (b *limitedBuffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// release drops the capped copy once the end reader has what it needs.
func (b *limitedBuffer) release() {
	b.mu.Lock()
	b.buf = nil
	b.mu.Unlock()
}

// streamLogWriter forwards an SSE stream and keeps a capped copy for the
// request log, so a converted stream is logged like a pass-through one.
type streamLogWriter struct {
	base http.ResponseWriter
	log  *limitedBuffer
}

func newStreamLogWriter(base http.ResponseWriter) *streamLogWriter {
	return &streamLogWriter{base: base, log: &limitedBuffer{max: passThroughLogBufferMax}}
}

func (w *streamLogWriter) Header() http.Header { return w.base.Header() }

func (w *streamLogWriter) WriteHeader(statusCode int) { w.base.WriteHeader(statusCode) }

func (w *streamLogWriter) Write(p []byte) (int, error) {
	n, err := w.base.Write(p)
	if n > 0 {
		_, _ = w.log.Write(p[:n])
	}
	return n, err
}

func (w *streamLogWriter) Flush() {
	if f, ok := w.base.(http.Flusher); ok {
		f.Flush()
	}
}

// Bytes is the capped copy of what went out, for the request log.
func (w *streamLogWriter) Bytes() []byte { return w.log.Bytes() }

// flushWriter writes to the response and flushes after each Write so SSE
// chunks reach the client immediately. When w already flushes internally
// (e.g. sseHeartbeatResponseWriter), flusher may be nil.
type flushWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

func requestBodyWantsStream(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	stream, _ := req["stream"].(bool)
	return stream
}

func copyPassThroughHeaders(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
}

// writePassThroughResponse forwards an upstream response to the client.
// Non-stream (or error) responses are fully buffered.
//
// A stream goes to the client as it comes — the first byte is what streaming is
// for — whether or not a failover writer is in front of it, and a stream that is
// cut is closed on the wire by end.
//
// Otherwise the first byte goes straight through, so nothing can be retried.
// end then names the protocol's closing frame (see streamInterrupter): a bare
// EOF is indistinguishable from a finished answer for a client that only waits
// for the closing event, so the cut turn is closed properly instead.
func writePassThroughResponse(w http.ResponseWriter, response *http.Response, stream bool, kind usageStreamKind, parseUsage func([]byte) TokenUsage, end ...streamInterrupter) (int, TokenUsage, []byte, error) {
	if stream && response.StatusCode < 400 {
		// The logged body is capped, the true streamed size is reported
		// separately for daily traffic accounting. response.Request carries the
		// proxy request's context, which holds the requestTiming this rides on.
		tee := newStreamTee(response)
		if response.Request != nil {
			defer func() { requestTimingFrom(response.Request.Context()).recordStreamedTxBytes(tee.Total()) }()
		}
		return streamPassThroughResponse(w, tee, response, kind, parseUsage, end...)
	}

	// Not a stream, or a stream that failed: the body is buffered whole so
	// clients and logs get it.
	copyPassThroughHeaders(w, response)
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, TokenUsage{}, nil, readErr
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	w.WriteHeader(response.StatusCode)
	if _, writeErr := w.Write(responseBody); writeErr != nil {
		return response.StatusCode, TokenUsage{}, responseBody, writeErr
	}
	usage := TokenUsage{}
	if parseUsage != nil {
		usage = parseUsage(responseBody)
	}
	return response.StatusCode, usage, responseBody, nil
}

// streamTee keeps the capped log copy of a forwarded stream (and, through
// limitedBuffer.Total, its true size for daily traffic accounting). Usage does
// not come from here but from streamUsageReader, which sees every byte.
//
// The log copy is the first passThroughLogBufferMax bytes of the stream; the
// Responses reader keeps a later window of the same stream while it still needs
// the stream's identity, so the first copy can be let go once that window
// covers the stream's end.
type streamTee struct {
	*limitedBuffer
}

func newStreamTee(_ *http.Response) *streamTee {
	return &streamTee{limitedBuffer: &limitedBuffer{max: passThroughLogBufferMax}}
}

// releaseLogCopy lets the log copy go. Called once the stream is read and the
// end reader has kept what it needs of the stream's end.
func (t *streamTee) releaseLogCopy() {
	t.release()
}

// converterStreamWriter is the writer a converter streams through when the
// failover loop is in front of it. Its output goes straight to the client —
// the first byte of the answer is what streaming is for, so the turn is
// committed then and never replayed onto another account — while a capped copy
// is kept for the logs and the true size is counted.
//
// A converter that fails before writing anything commits nothing, so the
// device stays deferrable and the loop can still hand the turn to a backup.
type converterStreamWriter struct {
	fw   *failoverResponseWriter
	hw   *sseHeartbeatResponseWriter // nil until the first write commits
	tee  *limitedBuffer
	stop func()
	// namedStatus says the converter named the turn's status through
	// WriteHeader. The status itself is handed to the failover writer at once,
	// not kept here for Write to read back: a status held in a field and read
	// from the top of Write was not seen at the first byte (same shape that
	// lost providers' ids in round27). Without it, the stream opening's own
	// status — never a default 200 — is the attempt's.
	namedStatus bool
	// pending is what the turn wrote before it said anything of the answer:
	// sent as a whole at the first content event, or dropped with the attempt if
	// the account fails first.
	pending      bytes.Buffer
	holdingSince time.Time
}

// converterHoldLimit bounds how long a turn may hold its leading frames: past it
// the client would rather see them than wait for content.
const converterHoldLimit = 1500 * time.Millisecond

func newConverterStreamWriter(fw *failoverResponseWriter) *converterStreamWriter {
	return &converterStreamWriter{fw: fw, tee: &limitedBuffer{max: passThroughLogBufferMax}}
}

func (s *converterStreamWriter) Header() http.Header { return s.fw.Header() }

// WriteHeader is held back until the first byte: a converter sets its headers
// before it knows whether the upstream will deliver anything. The status is
// kept, not dropped — it is the answer's own status once the answer has bytes,
// and a stream that produced none is written out by finishConvertedProxy with
// the upstream's status instead.
func (s *converterStreamWriter) WriteHeader(statusCode int) {
	if s.hw == nil {
		if statusCode > 0 {
			s.fw.WriteHeader(statusCode)
			s.namedStatus = true
		}
		return
	}
	s.hw.WriteHeader(statusCode)
}

func (s *converterStreamWriter) Write(p []byte) (int, error) {
	if s.hw == nil {
		// A refusal is not a reply: hold it no matter how long the turn has
		// been waiting, so the failover loop can still hand the turn to the
		// next account. Releasing on the hold deadline sent the refusal to the
		// client as the answer (Qoder's queued model took 1.5–2.6s to say
		// "busy", exactly past converterHoldLimit).
		if s.turnIsRefusal(p) {
			_, _ = s.pending.Write(p)
			return len(p), nil
		}
		// Held until the turn says something: the leading frames of every
		// protocol (message_start, response.created, the first Chat role chunk)
		// say only that a reply began, and a failure behind them can still be
		// answered by the next account. Magpie holds the same way (holdWriter,
		// fallback.go:1779-1834) and lets go at the first content event.
		if !s.turnHasContent(p) && !s.overdue() {
			_, _ = s.pending.Write(p)
			if s.pending.Len() > closingBodyMax {
				// A reply that leads on and on is not a reply: let it go so no
				// memory is held for an answer that never starts.
				s.release()
			}
			return len(p), nil
		}
		s.release()
	}
	n, err := s.hw.Write(p)
	_, _ = s.tee.Write(p[:n])
	s.hw.Flush()
	return n, err
}

// release opens the turn and sends whatever was held before it.
func (s *converterStreamWriter) release() {
	held := append([]byte(nil), s.pending.Bytes()...)
	s.pending.Reset()
	s.commit()
	if len(held) > 0 {
		_, _ = s.hw.Write(held)
		_, _ = s.tee.Write(held)
		s.hw.Flush()
	}
}

// overdue says the turn has been held long enough that a client waiting on it
// would rather see the leading frames than keep waiting for content.
func (s *converterStreamWriter) overdue() bool {
	if s.holdingSince.IsZero() {
		s.holdingSince = time.Now()
		return false
	}
	return time.Since(s.holdingSince) > converterHoldLimit
}

// turnHasContent says the bytes the converter just wrote carry part of the
// answer itself, not only the opening of the reply.
func (s *converterStreamWriter) turnHasContent(p []byte) bool {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			line = strings.TrimSpace(payload)
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		// An event whose type names a delta is content; the openings are not.
		if kind := streamEventType([]byte(line)); kind != "" {
			if strings.Contains(kind, "delta") || strings.HasSuffix(kind, ".done") {
				return true
			}
			continue
		}
		// A Chat chunk: content only when it carries text or tool arguments.
		if strings.Contains(line, `"content":"`) || strings.Contains(line, `"arguments":"`) || strings.Contains(line, `"text":"`) {
			if !strings.Contains(line, `"content":""`) && !strings.Contains(line, `"text":""`) {
				return true
			}
		}
	}
	return false
}

// hasJSONValue reports whether a raw JSON field is present and not null.
func hasJSONValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// hasJSONObject reports whether a raw JSON field is a non-empty object.
func hasJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{") && trimmed != "{}"
}

// turnIsRefusal reports whether this chunk is the vendor refusing the request
// rather than answering it. Every protocol shapes it differently — Claude's
// "event: error" frame, an OpenAI chunk whose only field is "error", a
// Responses "response.failed" — but all of them are the turn saying no, and
// none of them may be treated as the client's answer while it can still go to
// another account.
func (s *converterStreamWriter) turnIsRefusal(p []byte) bool {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if kind, ok := strings.CutPrefix(line, "event:"); ok {
			if strings.TrimSpace(kind) == "error" {
				return true
			}
			continue
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			line = strings.TrimSpace(payload)
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		var chunk struct {
			Type    string          `json:"type"`
			Error   json.RawMessage `json:"error"`
			Choices []any           `json:"choices"`
			Delta   json.RawMessage `json:"delta"`
			Content json.RawMessage `json:"content"`
			Usage   json.RawMessage `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		// An error field only makes the frame a refusal when the frame carries
		// no part of an answer: a chunk that also has choices/delta/content is
		// a reply that happens to echo something, not a turned-away request.
		if len(chunk.Choices) > 0 || hasJSONValue(chunk.Delta) || hasJSONValue(chunk.Content) {
			continue
		}
		if strings.EqualFold(chunk.Type, "error") || strings.HasPrefix(strings.ToLower(chunk.Type), "response.failed") {
			return true
		}
		if hasJSONObject(chunk.Error) {
			return true
		}
	}
	return false
}

// heldTurn is what the writer holds before the turn says anything.
func (s *converterStreamWriter) heldTurn() []byte {
	return append([]byte(nil), s.pending.Bytes()...)
}

// sendHeld writes the held turn out as it stands, still holding nothing back.
func (s *converterStreamWriter) sendHeld() {
	if s.hw != nil {
		return
	}
	s.release()
}

// commit opens the turn on its first byte, through the failover writer so the
// attempt's status — not a default 200 — is what the client's response carries.
func (s *converterStreamWriter) commit() {
	if !s.namedStatus {
		// A stream is a success by definition; a converter that never named a
		// status is writing the answer of a successful upstream.
		s.fw.WriteHeader(http.StatusOK)
	}
	s.hw = newSSEHeartbeatResponseWriter(s.fw)
	s.fw.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	s.fw.Header().Set("Cache-Control", "no-cache")
	s.fw.Header().Set("Connection", "keep-alive")
	s.fw.FlushError()
	s.stop = startSSEHeartbeat(s.hw, sseHeartbeatInterval)
}

func (s *converterStreamWriter) Flush() {
	if s.hw != nil {
		s.hw.Flush()
	}
}

// Committed reports whether the client has seen part of this attempt.
func (s *converterStreamWriter) Committed() bool { return s.hw != nil && s.hw.Committed() }

// Finish stops the heartbeat and returns the capped copy of what went out.
func (s *converterStreamWriter) Finish() []byte {
	// A turn that never said anything but its opening still sent that opening:
	// it is not held back from a client that has nothing else. A refusal is the
	// exception — releasing it here would answer the client with the very
	// error the failover loop is about to retry elsewhere. It still belongs in
	// the log copy: the request log is where an operator reads what the vendor
	// said.
	if s.hw == nil && s.pending.Len() > 0 {
		if !s.turnIsRefusal(s.pending.Bytes()) {
			s.release()
		} else {
			_, _ = s.tee.Write(s.pending.Bytes())
		}
	}
	if s.stop != nil {
		s.stop()
	}
	return s.tee.Bytes()
}

func streamPassThroughResponse(w http.ResponseWriter, tee *streamTee, response *http.Response, kind usageStreamKind, parseUsage func([]byte) TokenUsage, end ...streamInterrupter) (int, TokenUsage, []byte, error) {
	copyPassThroughHeaders(w, response)
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if w.Header().Get("Connection") == "" {
		w.Header().Set("Connection", "keep-alive")
	}
	usage := newStreamUsageReader(kind)
	stream := &passThroughOpeningWriter{w: w, limit: passThroughLogBufferMax, tee: tee}
	stream.setEnd(&usage.streamEndReader)
	// A turn the Anthropic OAuth upstream closes with no visible output is
	// rewritten to an error frame here, so the client asks again instead of
	// taking silence as the answer (see claudeEmptyTurnGuard).
	out, emptyTurn := claudeEmptyTurnGuardFor(stream, end)
	hw := newSSEHeartbeatResponseWriter(out)
	hw.WriteHeader(response.StatusCode)
	stopHeartbeat := startSSEHeartbeat(hw, sseHeartbeatInterval)
	defer stopHeartbeat()
	hw.Flush()
	// The failover loop reads this attempt as answered from its first forwarded
	// byte: the client's time-to-first-token has been paid, and a cut turn closes
	// on the wire instead of being handed to a backup that cannot rewind it.
	startForwardedStream(w, response.StatusCode)

	reads := &clientGoneReader{src: response.Body}
	// hw.Write already serializes, the stream writer hands each byte to the
	// client at once, and usage sees every byte rather than the capped copy.
	_, copyErr := writeWithClientGone(io.MultiWriter(hw, usage), reads)
	if emptyTurn != nil {
		// The upstream's last frame may not have ended on a blank line: write
		// what is left so the closing rewrite below still happens.
		emptyTurn.close()
	}
	usage.streamEndReader.settleTail()
	cut := stream.cut(usage, parseUsage, tee.Bytes())
	cut.closed = usage.closed()
	if reporter := firstStreamTurnReporter(end); reporter != nil {
		reporter.reportStreamTurn(cut)
	}
	closeTurn := func() {
		if len(end) > 0 && end[0] != nil && tee.Total() > 0 {
			end[0].closeInterruptedStream(hw, cut)
			hw.Flush()
		}
	}
	// io.Copy reports nothing when the body simply ends, so the closing frame —
	// read from every byte that went out — is what says whether it was cut.
	// A client that went away cut nothing worth closing, and nothing is handed
	// to another account for it.
	if copyErr == nil && !reads.clientGone() && isUpstreamStreamInterruptedResponse(response, usage.closed()) {
		// Part of the answer already reached the client, so it cannot be
		// rewound onto another account; close the turn instead of hanging.
		closeTurn()
		// The tokens the stream did report are spent all the same.
		return response.StatusCode, cut.usage, tee.Bytes(),
			newUpstreamStreamInterruption(io.ErrUnexpectedEOF, int(tee.Total()))
	}
	if copyErr != nil {
		// The connection died mid-answer after part of it reached the client:
		// that prefix is the answer now, so its turn is closed.
		closeTurn()
		return response.StatusCode, cut.usage, tee.Bytes(),
			newUpstreamStreamInterruption(copyErr, int(tee.Total()))
	}
	return response.StatusCode, cut.usage, tee.Bytes(), nil
}

// startForwardedStream tells a failover writer that what it carries from here on
// is the client's answer, not a response to show the loop.
func startForwardedStream(w http.ResponseWriter, statusCode int) {
	if fw, ok := w.(*failoverResponseWriter); ok {
		fw.startStream(statusCode)
	}
}

// clientGoneReader is the stream a forwarded turn is read from, and the record
// of whether the client left while it was read: the bytes it asks for are the
// bytes that will be written, so a write that fails is its own question asked
// and refused. A client that left cut nothing worth closing, and nothing is
// handed to another account for it.
type clientGoneReader struct {
	src  io.Reader
	gone atomic.Bool
}

func (r *clientGoneReader) Read(p []byte) (int, error) { return r.src.Read(p) }

func (r *clientGoneReader) clientGone() bool { return r.gone.Load() }

// writeWithClientGone copies src to dst chunk by chunk, so a write that never
// reached the client — the one failure a stream to a client that has left looks
// like — is not mistaken for the upstream's.
func writeWithClientGone(dst io.Writer, src *clientGoneReader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			m, writeErr := dst.Write(buf[:n])
			written += int64(m)
			if writeErr != nil {
				src.gone.Store(true)
				return written, writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
	}
}

// passThroughOpeningWriter is the writer a forwarded stream goes through: every
// byte goes to the client as it comes, into two records — the opening of the
// turn, up to the log cap, which is all a cut turn is closed on top of, and the
// capped log copy.
type passThroughOpeningWriter struct {
	w       http.ResponseWriter
	limit   int
	opened  bool
	opening bytes.Buffer
	tee     *streamTee
	end     *streamEndReader // to tell when the log copy can be let go
	// mu serialises the copy loop's Write with the heartbeat goroutine's Flush
	// (and WriteHeader) on the one http.ResponseWriter underneath. net/http
	// forbids touching a ResponseWriter from two goroutines at once: a Flush
	// that overtakes a Write mid-frame makes the server re-emit bytes from its
	// chunk buffer, i.e. duplicated SSE.
	mu sync.Mutex
}

// setEnd names the reader that keeps the stream's end: once it does, the log
// copy in tee is not the only record left.
func (o *passThroughOpeningWriter) setEnd(end *streamEndReader) { o.end = end }

func (o *passThroughOpeningWriter) Header() http.Header { return o.w.Header() }

func (o *passThroughOpeningWriter) WriteHeader(statusCode int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.w.WriteHeader(statusCode)
}

// Flush forwards the flush to the client. Without it this writer is not an
// http.Flusher, so sseHeartbeatResponseWriter — which wraps it — wrote each
// keep-alive into a buffer that never left the process: a pass-through stream
// that went quiet (long thinking, a slow tool argument) sent the client nothing
// at all, and an edge proxy idle for its own limit (Cloudflare's is about 100 s)
// cut the turn. Every other wrapper in front of a stream forwards Flush; this
// one was the exception.
//
// The mutex is what makes this safe next to the copy loop's Write: the
// heartbeat goroutine flushes while the body is still being copied.
func (o *passThroughOpeningWriter) Flush() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if f, ok := o.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *passThroughOpeningWriter) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.opened && o.opening.Len() < o.limit {
		keep := min(len(p), o.limit-o.opening.Len())
		_, _ = o.opening.Write(p[:keep])
	}
	if len(p) > 0 {
		o.opened = true
	}
	o.tee.Write(p)
	if o.opening.Len() >= o.limit && o.end.reachesEnd() {
		// The log copy is full and the stream's end is kept elsewhere: it is
		// only held for a log that is written once the stream is over.
		o.tee.releaseLogCopy()
	}
	return o.w.Write(p)
}

// cut is what the stream wrote of its turn, as a turn cut after its opening:
// the usage the whole stream reported, and the opening a closing frame repeats.
// Nothing of the stream's log copy is needed once the stream has ended: the
// end reader keeps the identity a closing frame repeats, and the log copy may
// already have been let go.
func (o *passThroughOpeningWriter) cut(usage *streamUsageReader, parseUsage func([]byte) TokenUsage, logTail []byte) streamCut {
	cut := usage.cut(parseUsage, logTail)
	cut.total = int64(o.opening.Len())
	opening := cutFromBody(o.opening.Bytes())
	cut.messageStarted = opening.messageStarted
	cut.openBlocks = opening.openBlocks
	if cut.id == "" || cut.model == "" {
		cut.id, cut.model, _ = responsesStreamIdentityFrom(o.opening.Bytes())
	}
	if cut.id == "" || cut.model == "" {
		// A stream with no opening inside the log cap says nothing of who it
		// was; the end reader's own tail is the rest of it.
		id, model, _ := responsesStreamIdentityFrom(usage.tailBytes)
		if cut.id == "" {
			cut.id = id
		}
		if cut.model == "" {
			cut.model = model
		}
	}
	return cut
}
