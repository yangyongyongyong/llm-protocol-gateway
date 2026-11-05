package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// qoderFrame is one SSE frame of Qoder's reply, built the way Qoder sends it:
// the OpenAI chunk as a JSON string inside the envelope's body field.
func qoderFrame(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(raw) + "\n\n"
}

func qoderChunkFrame(t *testing.T, delta map[string]any, finish string, usage map[string]any) string {
	t.Helper()
	chunk := map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nilIfEmpty(finish)}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	inner, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	return qoderFrame(t, map[string]any{"statusCodeValue": 200, "body": string(inner)})
}

func drainQoderReader(t *testing.T, reply string) []string {
	t.Helper()
	reader := newQoderChunkReader(strings.NewReader(reply), "ultimate")
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the converted stream: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "data: ") {
			lines = append(lines, strings.TrimPrefix(line, "data: "))
		}
	}
	return lines
}

func TestQoderChunkReaderPassesTextAndClosesTheTurn(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"content": "hello"}, "", nil) +
		qoderChunkFrame(t, map[string]any{"content": " world"}, "stop", map[string]any{"prompt_tokens": 7, "completion_tokens": 3})
	lines := drainQoderReader(t, reply)
	if lines[len(lines)-1] != "[DONE]" {
		t.Fatalf("expected the sentinel, got %q", lines[len(lines)-1])
	}
	var text strings.Builder
	finish := ""
	usageSeen := false
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("chunk is not valid JSON: %v (%s)", err, line)
		}
		if len(chunk.Choices) > 0 {
			text.WriteString(chunk.Choices[0].Delta.Content)
			if chunk.Choices[0].FinishReason != "" {
				finish = chunk.Choices[0].FinishReason
			}
		}
		if chunk.Usage != nil {
			usageSeen = true
		}
	}
	if got := text.String(); got != "hello world" {
		t.Fatalf("reassembled text = %q", got)
	}
	if finish != "stop" {
		t.Fatalf("finish reason = %q, want stop", finish)
	}
	if !usageSeen {
		t.Fatal("usage did not reach the client, so the quota counters would see nothing")
	}
}

// Qoder writes some tool calls as XML inside the answer text. They have to
// arrive as tool calls, or the client would show a call as prose.
func TestQoderChunkReaderLiftsXMLToolCall(t *testing.T) {
	block := "\x3ctool_call\x3e\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.go\"}}\n\x3c/tool_call\x3e"
	reply := qoderChunkFrame(t, map[string]any{"content": "Reading now."}, "", nil) +
		qoderChunkFrame(t, map[string]any{"content": block}, "stop", nil)
	lines := drainQoderReader(t, reply)
	var names []string
	arguments := ""
	var prose strings.Builder
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		prose.WriteString(chunk.Choices[0].Delta.Content)
		for _, call := range chunk.Choices[0].Delta.ToolCalls {
			if call.Function.Name != "" {
				names = append(names, call.Function.Name)
			}
			arguments += call.Function.Arguments
		}
	}
	if len(names) != 1 || names[0] != "read_file" {
		t.Fatalf("tool call was not lifted: %v (stream was %q)", names, strings.Join(lines, "|"))
	}
	if !strings.Contains(arguments, `"path":"a.go"`) {
		t.Fatalf("arguments missing: %q", arguments)
	}
	// The markers themselves must not reach the client as answer text.
	if strings.Contains(prose.String(), "tool_call") {
		t.Fatalf("the XML marker leaked into the answer text: %q", prose.String())
	}
	if strings.Contains(prose.String(), "Reading now.") == false {
		t.Fatalf("the text beside the call was dropped: %q", prose.String())
	}
}

// A call that the stream ends inside, or whose JSON cannot be read, must end the
// turn as an error rather than be sent on as the answer: a client that ran a
// half-read call would act on arguments the model never finished.
func TestQoderChunkReaderTurnsUnreadableCallIntoAnError(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"content": "\x3ctool_call\x3e{\"name\":\"run\",\"arguments\":"}, "", nil)
	lines := drainQoderReader(t, reply)
	if len(lines) == 0 {
		t.Fatal("expected an error frame")
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &envelope) != nil || envelope.Error.Message == "" {
		t.Fatalf("last frame is not an error: %s", lines[len(lines)-1])
	}
	for _, line := range lines {
		if line == "[DONE]" {
			t.Fatal("an unreadable call must not close as a finished turn")
		}
	}
}

// A block that looks like a call but cannot be read is an error when it is the
// answer, and stays text when it only sits inside code — prose about a call is
// not one.
func TestQoderChunkReaderTellsAnUnreadableCallFromAQuotedOne(t *testing.T) {
	broken := qoderCallOpen + "\n{\"name\":\"run\",\"arguments\":[1,2]\n" + qoderCallClose
	cases := []struct {
		name      string
		content   string
		wantError bool
	}{
		{"in prose the turn fails so it can be retried", "Here you go:" + broken, true},
		{"inside code it stays text", "```json\n" + broken + "\n```\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := drainQoderReader(t, qoderChunkFrame(t, map[string]any{"content": tc.content}, "stop", nil))
			joined := strings.Join(lines, "|")
			var isError bool
			for _, line := range lines {
				var frame struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal([]byte(line), &frame) == nil && frame.Error.Message != "" {
					isError = true
				}
			}
			if isError != tc.wantError {
				t.Fatalf("error frame = %v, want %v\nstream: %s", isError, tc.wantError, joined)
			}
			if !tc.wantError && !strings.Contains(joined, "tool_call") {
				t.Fatalf("the quoted block should have been passed on as text: %s", joined)
			}
		})
	}
}

func TestQoderChunkReaderKeepsNativeToolCallIDs(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 0, "id": "call_keep_me", "type": "function",
		"function": map[string]any{"name": "search", "arguments": `{"q":"x"}`},
	}}}, "tool_calls", nil)
	lines := drainQoderReader(t, reply)
	joined := strings.Join(lines, "|")
	if !strings.Contains(joined, "call_keep_me") {
		t.Fatalf("Qoder's own call id must survive: %s", joined)
	}
	if !strings.Contains(joined, "tool_calls") || !strings.Contains(joined, `"search"`) {
		t.Fatalf("native call missing: %s", joined)
	}
}

func TestQoderChunkReaderSurfacesEnvelopeFailure(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"code": json.Number("110"), "message": "credits used up"})
	reply := qoderFrame(t, map[string]any{"statusCodeValue": 429, "body": string(payload)})
	lines := drainQoderReader(t, reply)
	if len(lines) != 1 {
		t.Fatalf("expected one frame, got %d: %v", len(lines), lines)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(lines[0]), &envelope) != nil {
		t.Fatalf("not an error frame: %s", lines[0])
	}
	if !strings.Contains(envelope.Error.Message, "额度已用完") {
		t.Fatalf("quota failure not mapped: %q", envelope.Error.Message)
	}
}

// A stream error frame whose body holds only the queue state names the model as
// busy even though isQueued is false: serviceAvailable is false and the vendor
// names a wait. Reading it as a plain 403 cooled the account down for a busy
// model (2026-10-29).
func TestQoderChunkReaderMarksQueueFrameAsBusy(t *testing.T) {
	queue := `{"isQueued":false,"modelKey":"dfmodel","queueCount":0,"queueType":"p3","retryAfterSeconds":30,"serviceAvailable":false,"subCode":"RESOURCE_LIMIT","waitTime":0}`
	reply := qoderFrame(t, map[string]any{"statusCodeValue": 403, "code": json.Number("10605"), "body": queue})
	lines := drainQoderReader(t, reply)
	if len(lines) != 1 {
		t.Fatalf("expected one frame, got %d: %v", len(lines), lines)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(lines[0]), &envelope) != nil {
		t.Fatalf("not an error frame: %s", lines[0])
	}
	if envelope.Error.Type != upstreamBusyErrorType {
		t.Fatalf("error type = %q, want %q", envelope.Error.Type, upstreamBusyErrorType)
	}
	if !strings.Contains(envelope.Error.Message, "is busy") {
		t.Fatalf("queue frame not mapped to busy: %q", envelope.Error.Message)
	}
}

// Qoder sends its usage in a chunk of its own after the finish reason, with no
// choices in it. Reading to the finish and stopping there is what made every
// Qoder request count as 0 tokens.
func TestQoderChunkReaderReadsUsageAfterTheFinishFrame(t *testing.T) {
	reply := qoderChunkFrame(t, map[string]any{"content": "done"}, "stop", nil) +
		qoderFrame(t, map[string]any{"statusCodeValue": 200, "body": `{"usage":{"prompt_tokens":120,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":64}}}`})
	lines := drainQoderReader(t, reply)
	var last map[string]any
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(line), &chunk) == nil {
			last = chunk
		}
	}
	usage, ok := last["usage"].(map[string]any)
	if !ok {
		t.Fatalf("the last chunk carries no usage: %v", last)
	}
	if usage["prompt_tokens"] != float64(120) || usage["completion_tokens"] != float64(8) {
		t.Fatalf("usage wrong: %v", usage)
	}
	// A reply cut off by the token budget must not read as a finished one.
	cut := drainQoderReader(t, qoderChunkFrame(t, map[string]any{"content": "partial"}, "length", nil))
	var finished map[string]any
	for _, line := range cut {
		var chunk map[string]any
		if line != "[DONE]" && json.Unmarshal([]byte(line), &chunk) == nil {
			finished = chunk
		}
	}
	choices, _ := finished["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no final chunk: %v", finished)
	}
	if reason := choices[0].(map[string]any)["finish_reason"]; reason != "length" {
		t.Fatalf("finish_reason = %v, want length", reason)
	}
}

// An envelope that quotes its status is still a refusal: read as an int, one
// quoted value made the whole frame unparseable and the refusal vanished into
// a silently dropped line.
func TestQoderStreamReadsAQuotedEnvelopeStatus(t *testing.T) {
	reply := qoderFrame(t, map[string]any{
		"statusCodeValue": "403",
		"code":            "10605",
		"body":            `{"isQueued":true,"retryAfterSeconds":7}`,
	})
	reader := newQoderChunkReader(strings.NewReader(reply), "ultimate")
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "error") {
		t.Fatalf("a quoted status must still be reported, got %q", string(out))
	}
	if !strings.Contains(string(out), upstreamBusyErrorType) {
		t.Fatalf("a queued refusal must be labelled busy, got %q", string(out))
	}
}

func TestQoderFailureMapsUpstreamCodes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		payload    string
		wantStatus int
		wantWait   time.Duration
		contains   string
	}{
		{"queued", 403, `{"code":10605,"message":"busy","queue":{"isQueued":true,"retryAfterSeconds":12,"modelKey":"ultimate"}}`, http.StatusServiceUnavailable, 12 * time.Second, "排进队列"},
		{"queued without a wait", 503, `{"code":10605}`, http.StatusServiceUnavailable, 30 * time.Second, "busy"},
		{"queue state inside the message", 403, `{"code":10605,"message":"{\"isQueued\":true,\"retryAfterSeconds\":5}"}`, http.StatusServiceUnavailable, 5 * time.Second, "排进队列"},
		// The live 2026-10-29 frame: a stream error frame carries the queue
		// state as its own body, with isQueued=false and serviceAvailable=false.
		// Read literally that is not "queued", and the account got cooled down
		// for a busy model; the code on the envelope is what names it.
		{
			"queue state as the body with source limit",
			403,
			`{"code":"10605","message":"{\"isQueued\":false,\"modelKey\":\"dfmodel\",\"queueCount\":0,\"queueType\":\"p3\",\"retryAfterSeconds\":30,\"serviceAvailable\":false,\"subCode\":\"RESOURCE_LIMIT\",\"waitTime\":0}","subCode":"RESOURCE_LIMIT"}`,
			http.StatusServiceUnavailable,
			30 * time.Second,
			"is busy",
		},
		{"quota", 400, `{"code":112,"message":"credits used up"}`, http.StatusTooManyRequests, 0, "额度已用完"},
		{"request refusal", 400, `{"code":406,"message":"内容不适合"}`, http.StatusBadRequest, 0, "拒绝了这个请求"},
		{"lapsed sign-in by code", 401, `{"code":105,"message":"loginExpired"}`, http.StatusUnauthorized, 0, "重新登录"},
		// A 403 that carries the queue's code is the model being busy, not the
		// account being out of standing: reading it as a lapsed sign-in pushed
		// users to re-login for nothing (magpie plugins #98).
		{"403 with another code is not a lapsed sign-in", 403, `{"code":10606,"message":"nope"}`, http.StatusForbidden, 0, "nope"},
		{"401 with no code", 401, `not json`, http.StatusUnauthorized, 0, "重新登录"},
		{"server side", 500, `{"message":"upstream blew up"}`, http.StatusInternalServerError, 0, "upstream blew up"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, message, wait := qoderFailure(tc.status, []byte(tc.payload))
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", status, tc.wantStatus, message)
			}
			if wait != tc.wantWait {
				t.Fatalf("wait = %v, want %v", wait, tc.wantWait)
			}
			if !strings.Contains(message, tc.contains) {
				t.Fatalf("message = %q, want it to contain %q", message, tc.contains)
			}
		})
	}
}

func TestQoderFailureResponseCarriesRetryAfter(t *testing.T) {
	response := qoderFailureResponse(http.StatusServiceUnavailable, "Qoder 忙", 12*time.Second)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Retry-After"); got != "13" {
		t.Fatalf("Retry-After = %q, want the wait the client was told about", got)
	}
	payload, _ := io.ReadAll(response.Body)
	var envelope struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.Error.Type == "" {
		t.Fatalf("error body wrong: %s", payload)
	}
}

// A refusal of one HTTP status must not read as a sign-in problem: the gateway's
// failover marks an account lapsed on a 401, so a queue that came back as a 403
// would take a working account out of rotation.
func TestQoderFailureResponseKeepsPlainStatuses(t *testing.T) {
	response := qoderFailureResponse(http.StatusTooManyRequests, "额度已用完", 0)
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a quota must stay 429, got %d", response.StatusCode)
	}
}
