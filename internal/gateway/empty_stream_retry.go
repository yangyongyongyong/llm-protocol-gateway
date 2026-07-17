package gateway

import (
	"errors"
	"net/http"
	"strings"
)

// 同 Provider 上游空流最多尝试次数（含首次）。偶发 SSE 建连后无 chunk 时自动重试。
const maxUpstreamEmptyStreamAttempts = 2

func isEmptyUpstreamStreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "stream ended without any chunks") ||
		strings.Contains(msg, "stream ended without any events") ||
		strings.Contains(msg, "stream ended without any text deltas")
}

// errThinkingOnlyEmptyResponse marks an upstream turn that Anthropic/the
// provider reported as fully completed (not truncated by max_tokens/refusal)
// but which produced zero visible output: no text, no tool_use, only
// thinking/redacted_thinking blocks (or nothing at all). Seen in practice on a
// long session where the request history carried a stray trailing thinking
// block (see dropTrailingAssistantThinking in convert_responses_claude.go):
// some upstreams silently echo another "thinking-only, no answer" turn
// instead of erroring, which otherwise reads to the client as the assistant
// going silent. Call sites wrap this with fmt.Errorf("...: %w", ...) so
// errors.Is still matches through the wrapping.
var errThinkingOnlyEmptyResponse = errors.New("claude turn completed with no visible output (thinking-only)")

// isThinkingOnlyEmptyStreamError reports whether err (or anything it wraps)
// is errThinkingOnlyEmptyResponse.
func isThinkingOnlyEmptyStreamError(err error) bool {
	return errors.Is(err, errThinkingOnlyEmptyResponse)
}

// deferredSSEWriter 推迟 WriteHeader，直到首次 Write（或显式 Commit）。
// 用于流式转换：上游空流时尚未向客户端提交任何字节，可安全同 Provider 重试。
type deferredSSEWriter struct {
	base        http.ResponseWriter
	header      http.Header
	status      int
	wroteHeader bool
	wroteBody   bool
}

func newDeferredSSEWriter(base http.ResponseWriter) *deferredSSEWriter {
	return &deferredSSEWriter{
		base:   base,
		header: make(http.Header),
		status: http.StatusOK,
	}
}

func (w *deferredSSEWriter) Header() http.Header {
	if w.wroteHeader {
		return w.base.Header()
	}
	return w.header
}

func (w *deferredSSEWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.status = statusCode
}

func (w *deferredSSEWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.commit(w.status)
	}
	w.wroteBody = true
	return w.base.Write(p)
}

func (w *deferredSSEWriter) Flush() {
	if !w.wroteHeader {
		return
	}
	if f, ok := w.base.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *deferredSSEWriter) commit(statusCode int) {
	if w.wroteHeader {
		return
	}
	dst := w.base.Header()
	for key, values := range w.header {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	w.base.WriteHeader(statusCode)
	w.status = statusCode
	w.wroteHeader = true
}

func (w *deferredSSEWriter) Committed() bool {
	return w.wroteHeader || w.wroteBody
}

func (w *deferredSSEWriter) WroteBody() bool {
	return w.wroteBody
}
