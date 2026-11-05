package gateway

import (
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// Putting the official header order on the wire.
//
// This has to happen at the connection layer. net/http serialises request
// headers itself (Header.writeSubset sorts the keys), and a RoundTripper has no
// way to write those bytes in its own order short of replacing the whole
// HTTP/1.1 client. Wrapping a net.Conn and reordering the head block inside
// Write is the one place that keeps every Transport capability — pooling, TLS,
// timeouts, half-dead connection cleanup — and still changes the order.
//
// Failure safety is the red line: every parse here is best effort. Anything that
// cannot be decided passes through unchanged, so the worst outcome is "not
// reordered" (falling back to alphabetical), never a changed data stream. If the
// connection-reuse state machine guesses wrong, it only affects whether the next
// request gets reordered.

// workBuddyConnPhase is where a connection currently is.
type workBuddyConnPhase int

const (
	// workBuddyPhaseAwaitRequest: the next bytes start a request head.
	workBuddyPhaseAwaitRequest workBuddyConnPhase = iota
	// workBuddyPhaseInBody: the request or response body is flowing.
	workBuddyPhaseInBody
)

// workBuddyHeadCap bounds how long a request head may accumulate before the
// reorder is abandoned and the bytes pass through. A real head is about 1.8 KB;
// this only guards against endless buffering when the head is malformed or its
// boundary cannot be found.
const workBuddyHeadCap = 64 << 10

// workBuddyHeadOrderConn reorders request heads on the Write side and only
// parses (never alters) on the Read side.
type workBuddyHeadOrderConn struct {
	net.Conn

	mu      sync.Mutex
	phase   workBuddyConnPhase
	pending []byte // head bytes accumulated (meaningful only while awaiting one)

	// Read side: response parsing state, used only to tell when a response body
	// has ended so the next request may be reordered again.
	resp   workBuddyResponseTracker
	respMu sync.Mutex
}

// newWorkBuddyHeadOrderConn wraps a connection so its request heads go out in
// the client's order.
func newWorkBuddyHeadOrderConn(conn net.Conn) net.Conn {
	return &workBuddyHeadOrderConn{Conn: conn}
}

// Write reorders once the head block is complete and passes everything else
// through.
func (c *workBuddyHeadOrderConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase == workBuddyPhaseInBody {
		return c.Conn.Write(p)
	}

	c.pending = append(c.pending, p...)

	if end := bytes.Index(c.pending, []byte("\r\n\r\n")); end < 0 {
		// The head is not complete yet; the caller will keep writing or move on
		// to reading.
		if len(c.pending) > workBuddyHeadCap {
			// Degrade: release what was buffered as-is and stop trying.
			buffered := c.pending
			c.pending = nil
			c.phase = workBuddyPhaseInBody
			if _, err := c.Conn.Write(buffered); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	} else {
		head := c.pending[:end+4]
		rest := c.pending[end+4:]
		c.pending = nil
		c.phase = workBuddyPhaseInBody

		if _, err := c.Conn.Write(reorderWorkBuddyHeadBlock(head)); err != nil {
			return 0, err
		}
		if len(rest) > 0 {
			if _, err := c.Conn.Write(rest); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}
}

// Read passes bytes through and feeds the response tracker so the next request
// on a reused connection gets reordered too.
func (c *workBuddyHeadOrderConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.respMu.Lock()
		done := c.resp.feed(p[:n])
		c.respMu.Unlock()
		if done {
			c.mu.Lock()
			if c.phase == workBuddyPhaseInBody {
				c.phase = workBuddyPhaseAwaitRequest
				c.pending = nil
			}
			c.mu.Unlock()
		}
	}
	return n, err
}

// workBuddyResponseTracker follows one HTTP/1.1 response far enough to know when
// its body ends.
//
// It only cares about the boundary, never the content: after the response head
// it counts bytes per Content-Length, walks chunk framing, or gives up. Anything
// uncertain — a malformed field, layered transfer coding, a body delimited by
// connection close — counts as "cannot tell", which leaves the connection in the
// body phase forever: that just means later requests on it are not reordered,
// the safe degradation.
type workBuddyResponseTracker struct {
	inHead   bool
	headBuf  []byte
	remain   int64 // Content-Length still expected
	chunked  bool
	chunkRem int64
	chunkSt  int // 0 = want size line, 1 = read data, 2 = want trailing CRLF, 3 = trailers
	unknown  bool
	started  bool
}

// feed takes the bytes just read and reports whether this response is complete.
func (t *workBuddyResponseTracker) feed(b []byte) bool {
	if t.unknown {
		return false
	}
	if !t.started {
		t.started = true
		t.inHead = true
	}
	if t.inHead {
		t.headBuf = append(t.headBuf, b...)
		end := bytes.Index(t.headBuf, []byte("\r\n\r\n"))
		if end < 0 {
			if len(t.headBuf) > workBuddyHeadCap {
				t.unknown = true
			}
			return false
		}
		headBytes := t.headBuf[:end]
		over := t.headBuf[end+4:] // body bytes that arrived with the head
		t.headBuf = nil
		t.inHead = false

		lines := bytes.Split(headBytes, []byte("\r\n"))
		contentLength := int64(-1)
		for _, line := range lines[1:] {
			low := strings.ToLower(string(line))
			switch {
			case strings.HasPrefix(low, "content-length:"):
				contentLength = workBuddyParseInt64(low[len("content-length:"):])
				if contentLength < 0 {
					t.unknown = true
					return false
				}
			case strings.HasPrefix(low, "transfer-encoding:") && strings.Contains(low, "chunked"):
				t.chunked = true
			}
		}
		switch {
		case t.chunked:
			t.chunkSt = 0
		case contentLength >= 0:
			t.remain = contentLength
		default:
			// No length and not chunked: the connection closing delimits it, so
			// there is no end to find.
			t.unknown = true
			return false
		}
		if len(over) == 0 {
			return t.complete()
		}
		return t.feedBody(over)
	}
	return t.feedBody(b)
}

// feedBody consumes body bytes under the framing already decided.
func (t *workBuddyResponseTracker) feedBody(b []byte) bool {
	if t.chunked {
		return t.feedChunked(b)
	}
	if int64(len(b)) >= t.remain {
		t.remain = 0
		return true
	}
	t.remain -= int64(len(b))
	return false
}

// feedChunked walks chunked framing until the terminating chunk.
func (t *workBuddyResponseTracker) feedChunked(b []byte) bool {
	index := 0
	for index < len(b) {
		switch t.chunkSt {
		case 0: // chunk size line
			lineEnd := bytes.Index(b[index:], []byte("\r\n"))
			if lineEnd < 0 {
				// A half line; the next read continues. Size lines are short and
				// almost never split, so nothing is buffered for this.
				return false
			}
			line := string(b[index : index+lineEnd])
			if semi := strings.IndexByte(line, ';'); semi >= 0 {
				line = line[:semi]
			}
			size := workBuddyParseInt64Hex(line)
			if size < 0 {
				t.unknown = true
				return false
			}
			index += lineEnd + 2
			if size == 0 {
				t.chunkSt = 3
				continue
			}
			t.chunkRem = size
			t.chunkSt = 1
		case 1: // chunk data
			if int64(len(b)-index) < t.chunkRem {
				t.chunkRem -= int64(len(b) - index)
				return false
			}
			index += int(t.chunkRem)
			t.chunkRem = 0
			t.chunkSt = 2
		case 2: // CRLF after a chunk
			if len(b)-index < 2 {
				return false
			}
			index += 2
			t.chunkSt = 0
		case 3: // trailers until a blank line
			lineEnd := bytes.Index(b[index:], []byte("\r\n"))
			if lineEnd < 0 {
				return false
			}
			if lineEnd == 0 {
				return true
			}
			index += lineEnd + 2
		}
	}
	return false
}

// complete answers the empty-body case after a head was parsed.
func (t *workBuddyResponseTracker) complete() bool {
	if t.chunked {
		return false // a chunked body always has at least 0\r\n\r\n
	}
	return t.remain == 0
}

// The parsing helpers below use the standard library rather than hand-rolled
// loops: this runs on every response and "cannot get it wrong" is worth more
// than a few nanoseconds. Measured cost is inside the noise.

func workBuddyParseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

func workBuddyParseInt64Hex(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 16, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// workBuddyHeadOrderWrapped reports whether conn already reorders heads, so a
// connection that passed through two wrappers is not wrapped twice.
func workBuddyHeadOrderWrapped(conn net.Conn) (*workBuddyHeadOrderConn, bool) {
	wrapped, ok := conn.(*workBuddyHeadOrderConn)
	return wrapped, ok
}

// errWorkBuddyNilConn keeps a nil conn from panicking inside a dial hook.
var errWorkBuddyNilConn = fmt.Errorf("workbuddy: nil connection")
