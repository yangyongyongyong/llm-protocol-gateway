package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWritePassThroughResponseStreamsAndTees(t *testing.T) {
	upstreamBody := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}
	recorder := httptest.NewRecorder()
	status, _, logBody, err := writePassThroughResponse(recorder, response, true, ParseOpenAIUsage)
	if err != nil {
		t.Fatalf("writePassThroughResponse: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if got := recorder.Body.String(); got != upstreamBody {
		t.Fatalf("client body mismatch: %q", got)
	}
	if string(logBody) != upstreamBody {
		t.Fatalf("tee body mismatch: %q", logBody)
	}
	if recorder.Header().Get("Content-Length") != "" {
		t.Fatalf("stream response should not set Content-Length")
	}
}

func TestWritePassThroughResponseBuffersErrors(t *testing.T) {
	upstreamBody := `{"error":{"message":"boom"}}`
	response := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}
	recorder := httptest.NewRecorder()
	status, _, logBody, err := writePassThroughResponse(recorder, response, true, ParseOpenAIUsage)
	if err != nil {
		t.Fatalf("writePassThroughResponse: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d", status)
	}
	if string(logBody) != upstreamBody || recorder.Body.String() != upstreamBody {
		t.Fatalf("error body not preserved")
	}
}

func TestRequestBodyWantsStream(t *testing.T) {
	if !requestBodyWantsStream([]byte(`{"stream":true}`)) {
		t.Fatal("expected stream true")
	}
	if requestBodyWantsStream([]byte(`{"stream":false}`)) {
		t.Fatal("expected stream false")
	}
}

// The logged body is capped at passThroughLogBufferMax, but daily traffic must
// reflect the true streamed size, so limitedBuffer counts every byte written.
func TestLimitedBufferCountsBeyondCap(t *testing.T) {
	buf := &limitedBuffer{max: 16}
	chunk := make([]byte, 10)
	for i := 0; i < 5; i++ {
		if _, err := buf.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(buf.Bytes()) != 16 {
		t.Fatalf("buffered bytes should be capped at 16, got %d", len(buf.Bytes()))
	}
	if buf.Total() != 50 {
		t.Fatalf("total should count every byte written, got %d", buf.Total())
	}
}

func TestLimitedBufferTotalWithZeroCap(t *testing.T) {
	buf := &limitedBuffer{max: 0}
	if _, err := buf.Write(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if len(buf.Bytes()) != 0 {
		t.Fatalf("expected nothing buffered, got %d", len(buf.Bytes()))
	}
	if buf.Total() != 32 {
		t.Fatalf("total must still count, got %d", buf.Total())
	}
}

// recordStreamedTxBytes is called on paths where timing may be absent.
func TestRequestTimingStreamedTxNilSafe(t *testing.T) {
	var timing *requestTiming
	timing.recordStreamedTxBytes(123) // must not panic
	if got := timing.streamedTx(); got != 0 {
		t.Fatalf("nil timing should report 0, got %d", got)
	}
	real := newRequestTiming(time.Now())
	real.recordStreamedTxBytes(4096)
	if got := real.streamedTx(); got != 4096 {
		t.Fatalf("expected 4096, got %d", got)
	}
	// Zero/negative writes must not clobber a recorded value.
	real.recordStreamedTxBytes(0)
	if got := real.streamedTx(); got != 4096 {
		t.Fatalf("zero must not overwrite, got %d", got)
	}
}
