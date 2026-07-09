package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
