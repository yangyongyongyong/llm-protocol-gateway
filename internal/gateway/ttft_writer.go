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
	if !w.wrote && len(p) > 0 {
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
