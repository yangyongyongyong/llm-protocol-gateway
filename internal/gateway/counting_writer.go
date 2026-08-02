package gateway

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// countingResponseWriter tallies bytes written to the client so daily traffic
// accounting works on every path — pass-through and all six protocol
// conversions alike. Counting here rather than at the log body is deliberate:
// successful responses log an empty body, and streamed bodies are capped at
// passThroughLogBufferMax, so neither reflects real bytes.
//
// It forwards Flush/Hijack/ReadFrom because the streaming writers in this
// package type-assert them on the writer they are handed; dropping them would
// turn SSE into a buffered response.
type countingResponseWriter struct {
	http.ResponseWriter
	written int64
}

func newCountingResponseWriter(w http.ResponseWriter) *countingResponseWriter {
	return &countingResponseWriter{ResponseWriter: w}
}

func (c *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	if n > 0 {
		c.written += int64(n)
	}
	return n, err
}

func (c *countingResponseWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := c.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("response writer does not support hijacking")
}

// BytesWritten is the total payload delivered to the client.
func (c *countingResponseWriter) BytesWritten() int64 {
	if c == nil {
		return 0
	}
	return c.written
}
