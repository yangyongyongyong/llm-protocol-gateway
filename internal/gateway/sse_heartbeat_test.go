package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeferredSSEWriterHeartbeatDoesNotSetWroteBody(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := newDeferredSSEWriter(rec)
	dw.Header().Set("Content-Type", "text/event-stream")
	dw.WriteHeader(http.StatusOK)

	if err := dw.WriteSSEHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if !dw.Committed() {
		t.Fatal("heartbeat should commit headers")
	}
	if dw.WroteBody() {
		t.Fatal("heartbeat must not mark wroteBody (keeps short empty-stream retryable)")
	}
	if !strings.Contains(rec.Body.String(), ": ping") {
		t.Fatalf("body=%q", rec.Body.String())
	}

	if _, err := dw.Write([]byte("data: hi\n\n")); err != nil {
		t.Fatal(err)
	}
	if !dw.WroteBody() {
		t.Fatal("expected wroteBody after real SSE frame")
	}
}

func TestStartSSEHeartbeatEmitsPing(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := newDeferredSSEWriter(rec)
	dw.Header().Set("Content-Type", "text/event-stream")
	dw.WriteHeader(http.StatusOK)

	stop := startSSEHeartbeat(dw, 20*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), ": ping") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected heartbeat comment, body=%q", rec.Body.String())
}

func TestFinishConvertedProxyHeartbeatBlocksEmptyRetry(t *testing.T) {
	s := &Server{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	rec := httptest.NewRecorder()
	old := sseHeartbeatInterval
	sseHeartbeatInterval = 15 * time.Millisecond
	defer func() { sseHeartbeatInterval = old }()

	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		_, _ = io.Copy(io.Discard, reader)
		// Stay open long enough for at least one heartbeat to commit headers.
		time.Sleep(60 * time.Millisecond)
		return TokenUsage{}, fmt.Errorf("openai stream ended without any chunks")
	}
	status, _, _, err, retryable := s.finishConvertedProxy(rec, resp, "glm-4.5", true, nil, streamConvert)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if err == nil {
		t.Fatal("expected empty-stream error")
	}
	if retryable {
		t.Fatal("after heartbeat commit, empty stream must not be retryable")
	}
	if !strings.Contains(rec.Body.String(), ": ping") {
		t.Fatalf("expected heartbeat in body, got %q", rec.Body.String())
	}
}

func TestTTFTIgnoresSSECommentHeartbeat(t *testing.T) {
	rec := httptest.NewRecorder()
	var ttft int64
	w := wrapTTFTWriter(rec, time.Now().Add(-time.Second), &ttft)
	if _, err := w.Write(sseHeartbeatComment); err != nil {
		t.Fatal(err)
	}
	if ttft != 0 {
		t.Fatalf("heartbeat must not set TTFT, got %d", ttft)
	}
	if _, err := w.Write([]byte("data: hi\n\n")); err != nil {
		t.Fatal(err)
	}
	if ttft <= 0 {
		t.Fatal("expected TTFT after real SSE data")
	}
}
