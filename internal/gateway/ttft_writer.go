package gateway

import (
	"bufio"
	"net"
	"net/http"
	"time"
)

// ttftResponseWriter records milliseconds until the first successful Write of
// response body bytes (first token / first SSE chunk for streaming paths).
type ttftResponseWriter struct {
	http.ResponseWriter
	started time.Time
	ttftMs  *int64
	timing  *requestTiming
	wrote   bool
}

func wrapTTFTWriter(w http.ResponseWriter, started time.Time, ttftMs *int64) http.ResponseWriter {
	return wrapTTFTWriterWithTiming(w, started, ttftMs, nil)
}

func wrapTTFTWriterWithTiming(w http.ResponseWriter, started time.Time, ttftMs *int64, timing *requestTiming) http.ResponseWriter {
	return &ttftResponseWriter{ResponseWriter: w, started: started, ttftMs: ttftMs, timing: timing}
}

func (w *ttftResponseWriter) Write(p []byte) (int, error) {
	// SSE comment keep-alives (": ping") are not a model first-token.
	if !w.wrote && len(p) > 0 && !isSSECommentPayload(p) {
		w.wrote = true
		if w.ttftMs != nil && *w.ttftMs <= 0 {
			*w.ttftMs = time.Since(w.started).Milliseconds()
		}
		if w.timing != nil {
			w.timing.markClientFirstWrite()
		}
	}
	return w.ResponseWriter.Write(p)
}

// isSSECommentPayload reports whether p is solely an SSE comment frame
// (lines starting with ':'). Used so streaming heartbeats do not pollute TTFT.
func isSSECommentPayload(p []byte) bool {
	i := 0
	for i < len(p) && (p[i] == ' ' || p[i] == '\t' || p[i] == '\n' || p[i] == '\r') {
		i++
	}
	return i < len(p) && p[i] == ':'
}

func (w *ttftResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *ttftResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (w *ttftResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}
