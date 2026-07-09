package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const passThroughLogBufferMax = 256 * 1024

// limitedBuffer keeps up to max bytes for logging/usage parsing while the
// remainder of a stream is still forwarded to the client.
type limitedBuffer struct {
	buf []byte
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	if len(b.buf) < b.max {
		remain := b.max - len(b.buf)
		if len(p) > remain {
			b.buf = append(b.buf, p[:remain]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf
}

// flushWriter writes to the response and flushes after each Write so SSE
// chunks reach the client immediately.
type flushWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

func requestBodyWantsStream(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	stream, _ := req["stream"].(bool)
	return stream
}

func copyPassThroughHeaders(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
}

// writePassThroughResponse forwards an upstream response to the client.
// When stream is true and the upstream status is successful, bytes are copied
// with Flush and a limited tee buffer is kept for usage/error logging.
// Non-stream (or error) responses are fully buffered as before.
func writePassThroughResponse(w http.ResponseWriter, response *http.Response, stream bool, parseUsage func([]byte) TokenUsage) (int, TokenUsage, []byte, error) {
	copyPassThroughHeaders(w, response)

	// Error responses are always fully buffered so clients and logs get the body.
	if !stream || response.StatusCode >= 400 {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return 0, TokenUsage{}, nil, readErr
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
		w.WriteHeader(response.StatusCode)
		if _, writeErr := w.Write(responseBody); writeErr != nil {
			return response.StatusCode, TokenUsage{}, responseBody, writeErr
		}
		usage := TokenUsage{}
		if parseUsage != nil {
			usage = parseUsage(responseBody)
		}
		return response.StatusCode, usage, responseBody, nil
	}

	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if w.Header().Get("Connection") == "" {
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(response.StatusCode)

	var flusher http.Flusher
	if f, ok := w.(http.Flusher); ok {
		flusher = f
		flusher.Flush()
	}
	tee := &limitedBuffer{max: passThroughLogBufferMax}
	writer := io.MultiWriter(&flushWriter{w: w, flusher: flusher}, tee)
	if _, copyErr := io.Copy(writer, response.Body); copyErr != nil {
		return response.StatusCode, TokenUsage{}, tee.Bytes(), copyErr
	}
	usage := TokenUsage{}
	if parseUsage != nil {
		usage = parseUsage(tee.Bytes())
	}
	return response.StatusCode, usage, tee.Bytes(), nil
}
