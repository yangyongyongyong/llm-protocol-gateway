package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCountingResponseWriterTalliesWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	c := newCountingResponseWriter(rec)
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(" world")); err != nil {
		t.Fatal(err)
	}
	if got := c.BytesWritten(); got != 11 {
		t.Fatalf("expected 11 bytes, got %d", got)
	}
	if rec.Body.String() != "hello world" {
		t.Fatalf("payload must pass through unchanged, got %q", rec.Body.String())
	}
}

// The streaming writers in this package type-assert http.Flusher on whatever
// writer they are handed; losing it would silently buffer SSE.
func TestCountingResponseWriterPreservesFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	c := newCountingResponseWriter(rec)
	if _, ok := interface{}(c).(http.Flusher); !ok {
		t.Fatal("counting writer must implement http.Flusher")
	}
	c.Flush()
	if !rec.Flushed {
		t.Fatal("Flush must reach the underlying writer")
	}
}

func TestCountingResponseWriterNilSafeTally(t *testing.T) {
	var c *countingResponseWriter
	if got := c.BytesWritten(); got != 0 {
		t.Fatalf("nil writer should report 0, got %d", got)
	}
}

// attachTxCounter must be nil-safe and make the tally visible via streamedTx.
func TestAttachTxCounterFeedsStreamedTx(t *testing.T) {
	var nilTiming *requestTiming
	rec := httptest.NewRecorder()
	if got := nilTiming.attachTxCounter(rec); got != http.ResponseWriter(rec) {
		t.Fatal("nil timing must return the original writer")
	}

	timing := newRequestTiming(time.Now())
	w := timing.attachTxCounter(rec)
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if got := timing.streamedTx(); got != 10 {
		t.Fatalf("expected the writer tally to surface, got %d", got)
	}
}
