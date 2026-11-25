package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// responsesEmptyTurnGuard rewrites a Responses stream that says it completed
// without ever carrying visible output. Some compatible upstreams do this when
// a thinking model spends its whole turn on reasoning and returns no text or
// tool call; Codex reads the completed event as a normal end and silently stops
// the conversation. The guard keeps the opening frames, but replaces the final
// response.completed with response.failed so the client knows the turn failed
// instead of accepting silence as an answer.
type responsesEmptyTurnGuard struct {
	dst http.ResponseWriter
	// buf is bytes written but not yet a whole SSE frame.
	buf bytes.Buffer
	// visible says the stream delivered something a client can read: text or a
	// tool call. Reasoning-only events do not count as an answer.
	visible bool
	// rewrote says the terminal completed event has already been rewritten.
	rewrote bool
	// mu serialises Write / Flush / close with the heartbeat goroutine.
	mu sync.Mutex
}

func (g *responsesEmptyTurnGuard) Header() http.Header { return g.dst.Header() }

func (g *responsesEmptyTurnGuard) WriteHeader(statusCode int) { g.dst.WriteHeader(statusCode) }

func (g *responsesEmptyTurnGuard) Flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushLocked()
}

// flushLocked 只刷新已经识别的完整帧；残缺帧须留待后续 Write/close，
// 否则心跳 Flush 可能拆开 SSE 帧并导致错判或漏判。
func (g *responsesEmptyTurnGuard) flushLocked() {
	if f, ok := g.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// WriteSSEHeartbeat 仅在没有半帧时透传心跳，避免心跳破坏数据帧。
func (g *responsesEmptyTurnGuard) WriteSSEHeartbeat() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buf.Len() != 0 {
		return nil
	}
	if _, err := g.dst.Write(sseHeartbeatComment); err != nil {
		return err
	}
	g.flushLocked()
	return nil
}

func (g *responsesEmptyTurnGuard) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.buf.Write(p)
	for {
		body := g.buf.Bytes()
		end := frameEnd(body)
		if end < 0 {
			break
		}
		frame := append([]byte(nil), body[:end]...)
		rest := append([]byte(nil), body[end:]...)
		g.buf.Reset()
		g.buf.Write(rest)
		if _, err := g.writeFrame(frame); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (g *responsesEmptyTurnGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buf.Len() == 0 {
		return
	}
	frame := append([]byte(nil), g.buf.Bytes()...)
	g.buf.Reset()
	_, _ = g.writeFrame(frame)
}

func (g *responsesEmptyTurnGuard) writeFrame(frame []byte) (int, error) {
	payload, ok := responsesFramePayload(frame)
	if !ok {
		return g.dst.Write(frame)
	}
	var event struct {
		Type            string         `json:"type"`
		SequenceNumber  int            `json:"sequence_number"`
		Response        map[string]any `json:"response"`
		Delta           string         `json:"delta"`
		Text            string         `json:"text"`
		Item            map[string]any `json:"item"`
		FunctionCallArg string         `json:"arguments"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return g.dst.Write(frame)
	}
	switch event.Type {
	case "response.completed":
		// 某些 Responses 兼容服务只在完成帧的 response.output 中给出完整
		// message/function_call，没有 output_item 事件；必须先检查终态。
		if !g.visible && !responsesTerminalIsVisible(event.Response) && !g.rewrote {
			g.rewrote = true
			return g.dst.Write(responsesEmptyTurnFailureFrame(event.Response, event.SequenceNumber))
		}
	case "response.output_text.delta":
		if event.Delta != "" {
			g.visible = true
		}
	case "response.output_text.done":
		if event.Text != "" {
			g.visible = true
		}
	case "response.refusal.delta":
		if event.Delta != "" {
			g.visible = true
		}
	case "response.refusal.done":
		if event.Text != "" {
			g.visible = true
		}
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		if strings.TrimSpace(event.FunctionCallArg) != "" {
			g.visible = true
		}
	case "response.output_item.added", "response.output_item.done":
		if responsesItemIsVisible(event.Item) {
			g.visible = true
		}
	}
	return g.dst.Write(frame)
}

func responsesFramePayload(frame []byte) ([]byte, bool) {
	for _, line := range bytes.Split(frame, newlineByte) {
		payload := dataPayload(line)
		if len(payload) > 0 && payload[0] == '{' {
			return payload, true
		}
	}
	return nil, false
}

func responsesItemIsVisible(item map[string]any) bool {
	kind := strings.TrimSpace(stringValue(item["type"]))
	switch kind {
	case "", "message", "reasoning":
		if kind == "message" {
			content, _ := item["content"].([]any)
			for _, raw := range content {
				part, _ := raw.(map[string]any)
				if stringValue(part["text"]) != "" || stringValue(part["refusal"]) != "" {
					return true
				}
			}
		}
		return false
	default:
		return true
	}
}

// responsesTerminalIsVisible 判断终态对象是否带有正文、拒绝内容或可执行的输出。
func responsesTerminalIsVisible(response map[string]any) bool {
	if stringValue(response["output_text"]) != "" {
		return true
	}
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if responsesItemIsVisible(item) {
			return true
		}
	}
	return false
}

// responsesEmptyTurnFailureFrame 构造显式失败终态，并保留可核对的上游用量。
func responsesEmptyTurnFailureFrame(response map[string]any, sequence int) []byte {
	failed := map[string]any{
		"object": "response",
		"status": "failed",
		"error": map[string]any{
			"code":    "empty_response",
			"message": "upstream completed without visible output",
		},
	}
	for _, key := range []string{"id", "model", "created_at", "usage", "output"} {
		if value, ok := response[key]; ok {
			failed[key] = value
		}
	}
	event := map[string]any{
		"type":            "response.failed",
		"sequence_number": sequence,
		"response":        failed,
	}
	data, _ := json.Marshal(event)
	return []byte("event: response.failed\ndata: " + string(data) + "\n\n")
}

func responsesEmptyTurnGuardFor(w http.ResponseWriter, end []streamInterrupter) (http.ResponseWriter, *responsesEmptyTurnGuard) {
	if len(end) == 0 {
		return w, nil
	}
	if _, ok := end[0].(responsesStreamInterrupter); !ok {
		return w, nil
	}
	guard := &responsesEmptyTurnGuard{dst: w}
	return guard, guard
}
