package gateway

import (
	"bytes"
	"net/http"
	"sync"
)

// claudeEmptyTurnErrorFrame is what a Claude client is sent in place of the
// closing frame of a turn that produced no visible output.
//
// It is an error event on purpose: an Anthropic client (Claude Code among them)
// treats message_stop with stop_reason=end_turn as the model having finished a
// reply, and an empty reply is then a reply — the turn ends, the agent loop
// stops, and the operator sees "it answered one line and stopped". The same
// client retries the request when the stream ends in an error event instead,
// which is the only repair available here: every byte of the turn is already on
// the wire by the time it turns out to be empty.
//
// Magpie ends a cut reply the same way (claudeStreamInterrupter
// closeInterruptedStream, "as magpie ends a cut reply"), and refuses to end a
// turn with a stop_reason the model never chose.
var claudeEmptyTurnErrorFrame = []byte(`event: error
data: {"type":"error","error":{"type":"api_error","message":"upstream returned a turn with no visible output"}}

`)

// claudeEmptyTurnGuard rewrites the closing frame of a Claude stream that carried
// no visible output, so the client retries instead of accepting silence.
//
// It sits in front of the response writer and works frame by frame: SSE frames
// end with a blank line, so a frame is only ever written once it is whole. A turn
// counts as having said something once it opens a block a client can read — text
// or a tool call — which is the same rule streamEndReader applies when it closes a
// cut turn (sawVisibleOutput).
type claudeEmptyTurnGuard struct {
	dst http.ResponseWriter
	// buf is bytes written but not yet a whole frame.
	buf bytes.Buffer
	// visible says a block the client can read was opened.
	visible bool
	// rewrote says the closing frame has already been dealt with.
	rewrote bool
	// mu serialises Write / Flush / close. The copy loop writes here while the
	// heartbeat goroutine flushes, and both reach this writer: without the lock
	// the frame buffer is read and appended from two goroutines at once.
	mu sync.Mutex
}

// Header and WriteHeader pass straight through; the guard only rebuilds frames.
func (g *claudeEmptyTurnGuard) Header() http.Header { return g.dst.Header() }

func (g *claudeEmptyTurnGuard) WriteHeader(statusCode int) { g.dst.WriteHeader(statusCode) }

// Flush forwards any partial frame and the flush itself. A partial frame is
// written as it is: the stream is over and a client waiting for its last frames
// must not be left with nothing.
func (g *claudeEmptyTurnGuard) Flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushLocked()
}

func (g *claudeEmptyTurnGuard) flushLocked() {
	if g.buf.Len() > 0 {
		_, _ = g.dst.Write(g.buf.Bytes())
		g.buf.Reset()
	}
	if f, ok := g.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// Write cuts the write into whole SSE frames and hands each to writeFrame.
func (g *claudeEmptyTurnGuard) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.buf.Write(p)
	for {
		body := g.buf.Bytes()
		end := frameEnd(body)
		if end < 0 {
			break
		}
		frame := append([]byte(nil), body[:end]...)
		rest := append([]byte(nil), body[end:]...)
		g.buf.Reset()
		g.buf.Write(rest)
		if _, err := g.writeFrame(frame); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// close flushes whatever is left of a stream that has ended.
func (g *claudeEmptyTurnGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buf.Len() == 0 {
		return
	}
	frame := append([]byte(nil), g.buf.Bytes()...)
	g.buf.Reset()
	_, _ = g.writeFrame(frame)
}

// frameEnd is the offset just past the blank line that ends the first whole SSE
// frame in b, or -1 when b holds no whole frame yet.
func frameEnd(b []byte) int {
	if i := bytes.Index(b, []byte("\n\n")); i >= 0 {
		return i + 2
	}
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		return i + 4
	}
	return -1
}

// writeFrame notes a visible block and rewrites the closing frame of an empty
// turn.
func (g *claudeEmptyTurnGuard) writeFrame(frame []byte) (int, error) {
	if bytes.Contains(frame, []byte(`"message_stop"`)) {
		if !g.visible && !g.rewrote {
			g.rewrote = true
			// The turn said nothing the client can read: an error frame ends it
			// so the client asks again, and no message_stop follows (a client
			// that saw both would take the turn as finished).
			return g.dst.Write(claudeEmptyTurnErrorFrame)
		}
		g.rewrote = true
		return g.dst.Write(frame)
	}
	if !g.visible && opensVisibleBlock(frame) {
		g.visible = true
	}
	return g.dst.Write(frame)
}

// opensVisibleBlock reports a content_block_start opening text, a tool call or an
// Anthropic server-tool call — the blocks a client can read. A thinking block is
// not one: a turn of nothing but thinking is exactly the empty turn this guards.
func opensVisibleBlock(frame []byte) bool {
	index := bytes.Index(frame, []byte(`"content_block":`))
	if index < 0 {
		return false
	}
	block := frame[index:]
	for _, kind := range []string{`"type":"text"`, `"type":"tool_use"`, `"type":"server_tool_use"`} {
		if bytes.Contains(block, []byte(kind)) {
			return true
		}
	}
	return false
}

// claudeEmptyTurnGuardFor wraps w in a guard when this response's interrupter is
// the one that reports empty Claude turns (an Anthropic OAuth upstream), and
// returns w unchanged otherwise. Only that upstream is known to close a turn with
// no visible output; a Claude-compatible relay reports its own failures as errors,
// and rewriting its closing frame would hide a real answer.
func claudeEmptyTurnGuardFor(w http.ResponseWriter, end []streamInterrupter) (http.ResponseWriter, *claudeEmptyTurnGuard) {
	if len(end) == 0 {
		return w, nil
	}
	interrupter, ok := end[0].(claudeStreamInterrupter)
	if !ok || interrupter.onEmptyTurn == nil {
		return w, nil
	}
	guard := &claudeEmptyTurnGuard{dst: w}
	return guard, guard
}
