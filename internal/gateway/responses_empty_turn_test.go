package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 线上 14:29:05 的 Gemini 轮次：upstream 返回 response.completed，但整轮只有
// 思考 token，没有 output_text / function_call。Codex 会把它当成一次正常空回复
// 结束会话。网关必须把这种终态改写成 response.failed。
func TestResponsesEmptyCompletedTurnBecomesFailed(t *testing.T) {
	body := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_empty","object":"response","model":"gemini-4-hydrogen"}}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","sequence_number":1,"item":{"id":"msg_empty","type":"message","role":"assistant","content":[]}}`,
		``,
		`event: response.output_text.done`,
		`data: {"type":"response.output_text.done","sequence_number":2,"item_id":"msg_empty","output_index":0,"content_index":0,"text":""}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_empty","object":"response","status":"completed","model":"gemini-4-hydrogen","output":[],"output_text":"","usage":{"input_tokens":163385,"output_tokens":538,"total_tokens":163923}}}`,
		``,
	}, "\n")
	response := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamResponses, ParseResponsesUsage, responsesStreamInterrupter{}); err != nil {
		t.Fatalf("empty completed turn should be rewritten, not fail transport: %v", err)
	}
	out := recorder.Body.String()
	if strings.Contains(out, "response.completed") {
		t.Fatalf("empty completed turn must not reach the client as completed:\n%s", out)
	}
	if !strings.Contains(out, "response.failed") || !strings.Contains(out, "empty_response") {
		t.Fatalf("empty completed turn must become an explicit failure:\n%s", out)
	}
	if !strings.Contains(out, `"output_tokens":538`) && !strings.Contains(out, `"sequence_number":3`) {
		t.Fatalf("guard lost the stream identity/usage context:\n%s", out)
	}
}

// 正常的 Responses 文本流不能被改写。
func TestResponsesCompletedTurnWithTextStaysCompleted(t *testing.T) {
	body := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_ok","model":"gpt-5"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"hello"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_ok","status":"completed","model":"gpt-5","output_text":"hello"}}`,
		``,
	}, "\n")
	response := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamResponses, ParseResponsesUsage, responsesStreamInterrupter{}); err != nil {
		t.Fatal(err)
	}
	out := recorder.Body.String()
	if strings.Contains(out, "response.failed") || !strings.Contains(out, "response.completed") {
		t.Fatalf("a normal text turn was rewritten:\n%s", out)
	}
}

// 工具调用同样是可见输出，不能被当成空回复。
func TestResponsesCompletedTurnWithToolCallStaysCompleted(t *testing.T) {
	body := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_tool","model":"gpt-5"}}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","sequence_number":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{}"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_tool","status":"completed","model":"gpt-5","output":[]}}`,
		``,
	}, "\n")
	response := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          io.NopCloser(strings.NewReader(body)),
	}
	recorder := httptest.NewRecorder()
	if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamResponses, ParseResponsesUsage, responsesStreamInterrupter{}); err != nil {
		t.Fatal(err)
	}
	if out := recorder.Body.String(); strings.Contains(out, "response.failed") {
		t.Fatalf("a tool-call turn was mistaken for an empty answer:\n%s", out)
	}
}

// TestResponsesCompletedTurnWithTerminalOutputKeepsAssistantAnswer 验证部分兼容
// 上游只在 response.completed.response.output 里给出正文或工具时，不能误判为空回复。
func TestResponsesCompletedTurnWithTerminalOutputKeepsAssistantAnswer(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"terminal_message", `[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]`},
		{"terminal_function", `[{"id":"fc1","type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_terminal"}}` + "\n\n" +
				`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_terminal","status":"completed","output":` + tc.output + `}}` + "\n\n"
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}
			recorder := httptest.NewRecorder()
			if _, _, _, err := writePassThroughResponse(recorder, response, true, usageStreamResponses, ParseResponsesUsage, responsesStreamInterrupter{}); err != nil {
				t.Fatal(err)
			}
			out := recorder.Body.String()
			if strings.Contains(out, "response.failed") || !strings.Contains(out, "response.completed") {
				t.Fatalf("有效的末帧输出被误判为空回复: %s", out)
			}
		})
	}
}

// TestResponsesEmptyTurnGuardPreservesPartialFrameAcrossFlush 验证心跳刷新不能
// 将未读完的 SSE 帧拆散，否则带输出的完成帧会漏掉检查或被误判为空。
func TestResponsesEmptyTurnGuardPreservesPartialFrameAcrossFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	guard := &responsesEmptyTurnGuard{dst: recorder}
	part1 := `data: {"type":"response.output_text.delta","delta":"hell`
	part2 := `o"}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	if _, err := guard.Write([]byte(part1)); err != nil {
		t.Fatal(err)
	}
	guard.Flush()
	if recorder.Body.Len() != 0 {
		t.Fatalf("未完成帧不应被 Flush 提前写出: %q", recorder.Body.String())
	}
	if _, err := guard.Write([]byte(part2)); err != nil {
		t.Fatal(err)
	}
	guard.close()
	if out := recorder.Body.String(); strings.Contains(out, "response.failed") || !strings.Contains(out, "response.completed") {
		t.Fatalf("带正文的分片帧被误判: %s", out)
	}
}

// TestResponsesEmptyFailureRetainsUpstreamUsage 验证失败终态仍保留输出用量，
// 便于客户端区分确实空回复与网关没有收到上游正文。
func TestResponsesEmptyFailureRetainsUpstreamUsage(t *testing.T) {
	recorder := httptest.NewRecorder()
	guard := &responsesEmptyTurnGuard{dst: recorder}
	body := `data: {"type":"response.completed","sequence_number":2,"response":{"id":"r1","model":"gemini-4-hydrogen","usage":{"input_tokens":100,"output_tokens":9},"output":[]}}` + "\n\n"
	if _, err := guard.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	guard.close()
	if out := recorder.Body.String(); !strings.Contains(out, "response.failed") || !strings.Contains(out, `"output_tokens":9`) {
		t.Fatalf("空回复失败帧未保留原始用量: %s", out)
	}
}

// TestResponsesCompletedRefusalIsVisible 验证安全拒绝和仅有空白字符的合法
// 文本均不应被空回复防护改造成上游失败。
func TestResponsesCompletedRefusalIsVisible(t *testing.T) {
	cases := []struct {
		name   string
		frames string
	}{
		{"refusal_delta", `data: {"type":"response.refusal.delta","delta":"cannot"}` + "\n\n"},
		{"terminal_refusal", `data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot"}]}]}}` + "\n\n"},
		{"whitespace", `data: {"type":"response.output_text.delta","delta":" "}` + "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			guard := &responsesEmptyTurnGuard{dst: recorder}
			body := tc.frames
			if tc.name != "terminal_refusal" {
				body += `data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"
			}
			if _, err := guard.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
			guard.close()
			if out := recorder.Body.String(); strings.Contains(out, "response.failed") {
				t.Fatalf("有效回答被误判为空: %s", out)
			}
		})
	}
}

// TestResponsesEmptyTurnGuardHeartbeatDuringPartialFrame 验证心跳不能插入到
// 被拆分的数据帧中，否则合法正文会被解析成坏 JSON 并被误判为空。
func TestResponsesEmptyTurnGuardHeartbeatDuringPartialFrame(t *testing.T) {
	recorder := httptest.NewRecorder()
	guard := &responsesEmptyTurnGuard{dst: recorder}
	heartbeat := newSSEHeartbeatResponseWriter(guard)
	part1 := `data: {"type":"response.output_text.delta","delta":"hell`
	part2 := `o"}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	if _, err := heartbeat.Write([]byte(part1)); err != nil {
		t.Fatal(err)
	}
	if err := heartbeat.WriteSSEHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, err := heartbeat.Write([]byte(part2)); err != nil {
		t.Fatal(err)
	}
	guard.close()
	out := recorder.Body.String()
	if strings.Contains(out, "response.failed") || !strings.Contains(out, `"delta":"hello"`) {
		t.Fatalf("心跳破坏了数据帧: %s", out)
	}
}

// TestResponsesEmptyTurnGuardParsesMixedLineEndings 验证偶尔混用 LF/CRLF
// 的 SSE 流也按最早的帧边界解析，不能吞掉前面的文本而误判终态。
func TestResponsesEmptyTurnGuardParsesMixedLineEndings(t *testing.T) {
	recorder := httptest.NewRecorder()
	guard := &responsesEmptyTurnGuard{dst: recorder}
	body := `data: {"type":"response.output_text.delta","delta":"hello"}` + "\r\n\r\n" +
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	if _, err := guard.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	guard.close()
	if out := recorder.Body.String(); strings.Contains(out, "response.failed") || !strings.Contains(out, "response.completed") || !guard.visible {
		t.Fatalf("混合换行符破坏了回复: visible=%v %s", guard.visible, out)
	}
	recorder = httptest.NewRecorder()
	guard = &responsesEmptyTurnGuard{dst: recorder}
	body = `data: {"type":"response.created","response":{"id":"r1"}}` + "\r\n\r\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\r\n\r\n" +
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	if _, err := guard.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	guard.close()
	if out := recorder.Body.String(); strings.Contains(out, "response.failed") || !guard.visible {
		t.Fatalf("混合换行符导致有效文本被吞: visible=%v %s", guard.visible, out)
	}
}
