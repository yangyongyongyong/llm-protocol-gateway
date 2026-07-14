package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIsEmptyUpstreamStreamError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("openai stream ended without any chunks"), true},
		{fmt.Errorf("responses stream ended without any events"), true},
		{fmt.Errorf("responses stream ended without any text deltas"), true},
		{fmt.Errorf("connection reset"), false},
	}
	for _, tc := range cases {
		if got := isEmptyUpstreamStreamError(tc.err); got != tc.want {
			t.Fatalf("err=%v got=%v want=%v", tc.err, got, tc.want)
		}
	}
}

func TestDeferredSSEWriterDefersUntilWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := newDeferredSSEWriter(rec)
	dw.Header().Set("Content-Type", "text/event-stream")
	dw.WriteHeader(http.StatusOK)

	if dw.Committed() {
		t.Fatal("header-only should not commit to client")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("unexpected body before commit: %q", rec.Body.String())
	}

	if _, err := dw.Write([]byte("data: hi\n\n")); err != nil {
		t.Fatal(err)
	}
	if !dw.Committed() || !dw.WroteBody() {
		t.Fatal("expected commit after first write")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "data: hi") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestFinishConvertedProxyEmptyStreamRetryable(t *testing.T) {
	s := &Server{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}
	rec := httptest.NewRecorder()
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		_, _ = io.Copy(io.Discard, reader)
		return TokenUsage{}, fmt.Errorf("openai stream ended without any chunks")
	}
	status, _, _, err, retryable := s.finishConvertedProxy(rec, resp, "glm-4.5", true, nil, streamConvert)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if err == nil || !retryable {
		t.Fatalf("want retryable empty-stream err, got err=%v retryable=%v", err, retryable)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("client should not receive body on retryable empty stream, got %q", rec.Body.String())
	}
}

func TestFinishConvertedProxyWithEmptyStreamRetry(t *testing.T) {
	s := &Server{}
	var calls atomic.Int32
	send := func() (*http.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
			}, nil
		}
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	}
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		data, err := io.ReadAll(reader)
		if err != nil {
			return TokenUsage{}, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return TokenUsage{}, fmt.Errorf("openai stream ended without any chunks")
		}
		_, _ = w.Write([]byte("event: message\ndata: ok\n\n"))
		return TokenUsage{OutputTokens: 1}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	status, usage, _, err := s.finishConvertedProxyWithEmptyStreamRetry(rec, req, "glm-4.5", true, send, nil, streamConvert)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d want 2", calls.Load())
	}
	if usage.OutputTokens != 1 {
		t.Fatalf("usage=%+v", usage)
	}
	if !strings.Contains(rec.Body.String(), "data: ok") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestFinishConvertedProxyWithEmptyStreamRetryExhausted(t *testing.T) {
	s := &Server{}
	var calls atomic.Int32
	send := func() (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	}
	streamConvert := func(w http.ResponseWriter, reader io.Reader, model string) (TokenUsage, error) {
		_, _ = io.Copy(io.Discard, reader)
		return TokenUsage{}, fmt.Errorf("openai stream ended without any chunks")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	_, _, _, err := s.finishConvertedProxyWithEmptyStreamRetry(rec, req, "glm-4.5", true, send, nil, streamConvert)
	if !isEmptyUpstreamStreamError(err) {
		t.Fatalf("want empty stream err, got %v", err)
	}
	if int(calls.Load()) != maxUpstreamEmptyStreamAttempts {
		t.Fatalf("calls=%d want %d", calls.Load(), maxUpstreamEmptyStreamAttempts)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("exhausted retry must not commit body, got %q", rec.Body.String())
	}
}
