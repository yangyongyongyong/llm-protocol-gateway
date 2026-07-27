package gateway

import (
	"net/http"
	"sync"
	"time"
)

// sseHeartbeatInterval is how often streaming responses emit an SSE comment
// keep-alive. Cloudflare (and similar reverse proxies) close idle streams
// around ~100s with no downstream bytes; long Claude thinking periods can
// exceed that while visibilityGatedSSEWriter is still buffering real events.
// 15s leaves comfortable margin under that idle limit.
var sseHeartbeatInterval = 15 * time.Second

// sseHeartbeatComment is an SSE comment frame. Clients and OpenAI/Codex SSE
// parsers ignore lines starting with ':'; it only exists to keep the TCP /
// proxy path from looking idle.
var sseHeartbeatComment = []byte(": ping\n\n")

// sseHeartbeater is implemented by response writers that can emit a keep-alive
// without counting it as a "real" SSE body event (so empty-stream /
// thinking-only retries stay possible until the first data frame).
type sseHeartbeater interface {
	WriteSSEHeartbeat() error
}

// startSSEHeartbeat launches a goroutine that periodically calls
// WriteSSEHeartbeat on w. stop() is idempotent and must be deferred by the
// caller; it returns immediately if w does not support heartbeats.
func startSSEHeartbeat(w http.ResponseWriter, interval time.Duration) (stop func()) {
	hb, ok := w.(sseHeartbeater)
	if !ok {
		return func() {}
	}
	if interval <= 0 {
		interval = sseHeartbeatInterval
	}
	done := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() { close(done) })
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := hb.WriteSSEHeartbeat(); err != nil {
					return
				}
			}
		}
	}()
	return stop
}

// sseHeartbeatResponseWriter serializes Write / Flush / heartbeat against a
// shared mutex so a background keep-alive goroutine can coexist with the
// stream copy loop (pass-through path).
type sseHeartbeatResponseWriter struct {
	mu     sync.Mutex
	base   http.ResponseWriter
	status int
}

func newSSEHeartbeatResponseWriter(base http.ResponseWriter) *sseHeartbeatResponseWriter {
	return &sseHeartbeatResponseWriter{base: base, status: http.StatusOK}
}

func (w *sseHeartbeatResponseWriter) Header() http.Header { return w.base.Header() }

func (w *sseHeartbeatResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = statusCode
	w.base.WriteHeader(statusCode)
}

func (w *sseHeartbeatResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.base.Write(p)
}

func (w *sseHeartbeatResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.base.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *sseHeartbeatResponseWriter) WriteSSEHeartbeat() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.base.Write(sseHeartbeatComment); err != nil {
		return err
	}
	if f, ok := w.base.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
