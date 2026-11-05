package gateway

// Reading Qoder's answer.
//
// Each of Qoder's SSE frames carries an envelope whose "body" is a standard
// OpenAI chunk, so most of the reply is already in the shape the gateway
// relays. Two things are not: the envelope's own failure codes, and the fact
// that Qoder sometimes writes a tool call as XML inside the answer text rather
// than as a tool call. The splitter below lifts those back out while the text
// streams, and refuses to pass one on that it cannot read — a half-written call
// sent as the answer is a call the client would then try to run.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type qoderChunkReader struct {
	scanner   *bufio.Scanner
	id        string
	created   int64
	model     string
	out       [][]byte
	pos       int
	done      bool
	began     bool
	text      qoderText
	toolIndex int
	nextCall  int
	usage     map[string]any
	// pendingCall holds a native tool call until its name arrives, keyed by the
	// upstream's index; emittedCall and emittedIndex remember which of them have
	// already reached the client, and under which index this gateway gave them.
	pendingCall  map[int]*qoderNativeCall
	emittedCall  map[int]bool
	emittedIndex map[int]int
	// finishReason is what the turn ended on, remembered until the last frame:
	// Qoder sends its usage after the finish, in a chunk with no choices.
	finishReason string
	// ctx and reopen are what let a refusal be retried on the same account
	// rather than handed to the failover loop: a queue is the model being busy,
	// so another account would answer the same way, and the wait is something
	// the client can be told about while it happens.
	ctx    context.Context
	reopen func(context.Context) (io.Reader, error)
	// dataSent says a chunk with content or a call reached the client: once that
	// happened the answer is under way and retrying is no longer possible.
	dataSent bool
	// queueRounds, queueSince and reopenRounds bound the two kinds of retry.
	queueRounds  int
	queueSince   time.Time
	reopenRounds int
	// body is the upstream stream being read. A retry replaces it, so the reader
	// owns closing it: whoever abandons the reader must not leave a connection
	// open behind it.
	body io.Closer
}

// qoderQueue* bound how long a queued request waits before the refusal is passed
// on. The per-round wait is capped as well: a vendor that names an hour must not
// park the client for an hour, and the total budget is what finally decides.
const (
	qoderQueueMaxRounds   = 3
	qoderQueueWaitMax     = 180 * time.Second
	qoderQueueBudget      = 600 * time.Second
	qoderQueueSingleWait  = 60 * time.Second
	qoderQueueDefaultWait = 30 * time.Second
	qoderQueueJitterMax   = 2000 // milliseconds
	// qoderReopenRounds is how many times a transient upstream failure is
	// retried on the same account before the failover loop is given the turn.
	qoderReopenRounds = 2
)

var qoderSleep = time.Sleep

// qoderNativeCall is a tool call read off the upstream's own delta: the pieces
// arrive over several frames, in no fixed order.
type qoderNativeCall struct {
	id        string
	name      string
	arguments string
}

func newQoderChunkReader(body io.Reader, model string) *qoderChunkReader {
	return newQoderChunkReaderWithTools(body, model, nil)
}

// newQoderChunkReaderWithTools is newQoderChunkReader for a request that
// declared tools: the names are what let a leaked call be told from prose about
// one, so only a block naming a declared tool is lifted.
func newQoderChunkReaderWithTools(body io.Reader, model string, tools map[string]bool) *qoderChunkReader {
	reader := &qoderChunkReader{
		scanner:      newQoderScanner(body),
		id:           "chatcmpl-" + randomHex(6),
		created:      time.Now().Unix(),
		model:        model,
		toolIndex:    -1,
		text:         qoderText{allowed: tools},
		pendingCall:  map[int]*qoderNativeCall{},
		emittedCall:  map[int]bool{},
		emittedIndex: map[int]int{},
	}
	if closer, ok := body.(io.Closer); ok {
		reader.body = closer
	}
	return reader
}

// newQoderScanner reads one upstream stream line by line.
func newQoderScanner(body io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(body)
	// One frame can carry a whole tool call's argument blob.
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	return scanner
}

// pendingCall is the call being assembled for one upstream index.
func (r *qoderChunkReader) pendingCallFor(index int) *qoderNativeCall {
	if r.pendingCall == nil {
		r.pendingCall = map[int]*qoderNativeCall{}
	}
	call, ok := r.pendingCall[index]
	if !ok {
		call = &qoderNativeCall{}
		r.pendingCall[index] = call
	}
	return call
}

func (r *qoderChunkReader) Read(p []byte) (int, error) {
	for {
		if len(r.out) > 0 {
			n := copy(p, r.out[0][r.pos:])
			r.pos += n
			if r.pos >= len(r.out[0]) {
				r.out, r.pos = r.out[1:], 0
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		if r.done {
			return 0, io.EOF
		}
		if err := r.scanner.Err(); err != nil {
			r.emitError(fmt.Sprintf("Qoder：读取响应流失败：%v", err))
			continue
		}
		if !r.scanner.Scan() {
			r.finish()
			continue
		}
		r.consume(strings.TrimSpace(r.scanner.Text()))
	}
}

// Close closes the upstream stream this reader was reading, whichever attempt
// that is by now: an abandoned turn must not leave a connection open.
func (r *qoderChunkReader) Close() error {
	if r.body == nil {
		return nil
	}
	err := r.body.Close()
	r.body = nil
	return err
}

// consume takes one SSE line. Qoder frames its replies as plain "data:" lines,
// and a line that is not one — an event name, a comment, a keep-alive — carries
// nothing an OpenAI client needs.
func (r *qoderChunkReader) consume(line string) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || !json.Valid([]byte(payload)) {
		return
	}
	var envelope struct {
		// The status arrives as a number on most frames and as a string on
		// some, so it is read as raw bytes: typed as an int, one quoted status
		// makes the whole envelope unreadable and the frame is dropped — which
		// looks exactly like a refusal that never happened.
		StatusCodeValue json.RawMessage `json:"statusCodeValue"`
		Code            json.Number     `json:"code"`
		Message         string          `json:"message"`
		Body            json.RawMessage `json:"body"`
	}
	if json.Unmarshal([]byte(payload), &envelope) != nil {
		return
	}
	// Qoder sends the chunk as a JSON *string* holding the chunk's own JSON, so
	// the field has to be unwrapped before it can be read; an envelope that
	// sends it as an object is taken as it stands.
	body := qoderEnvelopeBody(envelope.Body)
	status := qoderEnvelopeStatus(envelope.StatusCodeValue)
	if status != 0 && status != http.StatusOK {
		data := body
		if len(data) == 0 {
			data = []byte(payload)
		}
		answerStatus, message, wait := qoderFailure(status, data)
		// The queue verdict lives on the envelope, not always in its body: a
		// body that is only the queue state names no code, so qoderFailure
		// read from it alone answers a plain 403 and the account looks broken.
		// Any frame whose envelope says 10605 is the model being busy, never
		// the account — ask again with the whole frame and keep the wait.
		if qoderEnvelopeQueued(envelope.Code, data) && wait == 0 {
			if s2, m2, w2 := qoderFailure(status, []byte(payload)); w2 > 0 {
				answerStatus, message, wait = s2, m2, w2
			}
		}
		// A queued model answers every account the same way, so the wait is
		// worth taking here rather than handing the turn on. A transient fault
		// is retried a couple of times on this account before the failover loop
		// sees it. Neither is possible once the answer has started.
		if qoderEnvelopeQueued(envelope.Code, data) || wait > 0 {
			if r.retryQueued(wait) {
				return
			}
		} else if r.retryTransient(answerStatus) {
			return
		}
		r.emitErrorQueued(fmt.Sprintf("%s（HTTP %d）", message, answerStatus), wait)
		return
	}
	if strings.TrimSpace(string(body)) == "[DONE]" {
		r.finish()
		return
	}
	if len(body) == 0 || !json.Valid(body) {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
		// The vendor sometimes puts an error inside an otherwise normal chunk.
		// It is counted, not acted on: the frame's own status is what decides
		// how the turn ends.
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &chunk) != nil {
		return
	}
	if chunk.Error.Message != "" {
		message := chunk.Error.Message
		if chunk.Error.Type != "" {
			message += " " + chunk.Error.Type
		}
		// Counted only: which kinds have been seen is exposed through the
		// telemetry snapshot, and the frame's own status still decides the turn.
		_, _ = noteQoderInnerError(message)
	}
	if len(chunk.Choices) == 0 {
		if chunk.Usage != nil {
			r.usage = chunk.Usage
		}
		return
	}
	delta := chunk.Choices[0].Delta
	if chunk.Usage != nil {
		r.usage = chunk.Usage
	}
	if delta.ReasoningContent != "" {
		r.emit(map[string]any{"reasoning_content": delta.ReasoningContent})
	}
	if delta.Content != "" {
		for _, fragment := range r.text.feed(delta.Content) {
			if !r.emitFragment(fragment) {
				return
			}
		}
	}
	// Qoder also serves tool calls the OpenAI way. The id it sends is the one a
	// result has to be answered with, so it is kept rather than replaced.
	for _, call := range delta.ToolCalls {
		index := r.toolIndex
		if call.Index != nil {
			index = *call.Index
		}
		if index != r.toolIndex {
			r.flushText()
			r.toolIndex = index
		}
		pending := r.pendingCallFor(index)
		if id := strings.TrimSpace(call.ID); id != "" {
			pending.id = id
		}
		if name := strings.TrimSpace(call.Function.Name); name != "" {
			pending.name = name
		}
		pending.arguments += call.Function.Arguments
		if r.emittedCall[index] {
			if call.Function.Arguments != "" {
				r.emit(map[string]any{"tool_calls": []any{map[string]any{
					"index": r.emittedIndex[index],
					"function": map[string]any{
						"arguments": call.Function.Arguments,
					},
				}}})
			}
			continue
		}
		// A call whose name has not arrived is not a call yet: putting an empty
		// name on the wire gives the client a tool it cannot look up, and some
		// of them retry the same turn forever.
		if pending.name == "" {
			continue
		}
		r.emittedCall[index] = true
		r.emittedIndex[index] = r.nextCall
		id := pending.id
		if id == "" {
			id = "call_" + randomHex(16)
		}
		r.emit(map[string]any{"tool_calls": []any{map[string]any{
			"index": r.nextCall, "id": id, "type": "function",
			"function": map[string]any{"name": pending.name, "arguments": pending.arguments},
		}}})
		r.nextCall++
		r.text.sawTool = true
	}
	if chunk.Choices[0].FinishReason != "" {
		// The finish reason does not end the reading: Qoder sends its usage in
		// a chunk of its own afterwards, with no choices at all. Stopping here
		// is what made every Qoder request count as 0 tokens.
		r.finishReason = chunk.Choices[0].FinishReason
	}
}

// emitFragment passes on one piece of the split text: a lifted tool call, the
// text itself, or a call that cannot be read. It reports whether the turn may
// go on: an error ends it without a finish, so a client can try again.
func (r *qoderChunkReader) emitFragment(fragment qoderFragment) bool {
	switch {
	case fragment.err != "":
		r.emitError(fragment.err)
		return false
	case fragment.call != nil:
		r.emit(map[string]any{"tool_calls": []any{map[string]any{
			"index": r.nextCall, "id": "call_" + randomHex(16), "type": "function",
			"function": map[string]any{"name": fragment.call.name, "arguments": ""},
		}}})
		r.emit(map[string]any{"tool_calls": []any{map[string]any{
			"index":    r.nextCall,
			"function": map[string]any{"arguments": fragment.call.arguments},
		}}})
		r.nextCall++
		return true
	case fragment.text != "":
		r.emit(map[string]any{"content": fragment.text})
	}
	return true
}

// emit appends one OpenAI chunk carrying this delta.
func (r *qoderChunkReader) emit(delta map[string]any) {
	if !r.began {
		r.began = true
		r.writeChunk(map[string]any{"role": "assistant", "content": ""}, "", nil)
	}
	// Anything with substance ends the window in which a refusal could still be
	// retried: the client is already reading this answer.
	if _, ok := delta["content"]; ok {
		r.dataSent = true
	}
	if text, ok := delta["reasoning_content"].(string); ok && text != "" {
		r.dataSent = true
	}
	if _, ok := delta["tool_calls"]; ok {
		r.dataSent = true
	}
	r.writeChunk(delta, "", nil)
}

func (r *qoderChunkReader) writeChunk(delta map[string]any, finishReason string, usage map[string]any) {
	if delta == nil {
		delta = map[string]any{}
	}
	chunk := map[string]any{
		"id":      r.id,
		"object":  "chat.completion.chunk",
		"created": r.created,
		"model":   r.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nilIfEmpty(finishReason)}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	r.out = append(r.out, []byte("data: "+string(raw)+"\n\n"))
}

// flushText gives out whatever the splitter was still holding: a call that the
// stream ended inside is resolved here, not left unsaid.
func (r *qoderChunkReader) flushText() {
	for _, fragment := range r.text.flush() {
		if !r.emitFragment(fragment) {
			return
		}
	}
}

// finish closes the stream: the turn's last chunk, with its finish reason and
// the usage Qoder reported, then the sentinel.
func (r *qoderChunkReader) finish() {
	if r.done {
		return
	}
	r.flushText()
	if r.done {
		return
	}
	finish := "stop"
	switch {
	case r.text.sawTool || r.nextCall > 0:
		// nextCall counts the calls that actually reached the client: one held
		// back for a missing name is not a call, so a turn that only held one
		// still finished normally.
		finish = "tool_calls"
	case r.finishReason == "length":
		// A reply cut off by the token budget is not a finished one: the agent
		// has to see that it stopped early.
		finish = "length"
	}
	r.began = true
	r.writeChunk(map[string]any{}, finish, r.usage)
	r.out = append(r.out, []byte("data: [DONE]\n\n"))
	r.done = true
}

func (r *qoderChunkReader) emitError(message string) {
	if r.done {
		return
	}
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "api_error"}})
	r.out = append(r.out, []byte("data: "+string(payload)+"\n\n"))
	r.done = true
}

// emitErrorQueued is emitError for a refusal the vendor framed as "the model is
// busy" (Qoder's 10605). The type carries that verdict to the failover loop,
// which must move on to the next account without counting this one as failed:
// a queued model answers every account the same way, so cooling this account
// down would only send the next request into the same queue with fewer places
// left to try.
func (r *qoderChunkReader) emitErrorQueued(message string, wait time.Duration) {
	if r.done {
		return
	}
	// A refusal that arrives inside the stream is still a refusal the client
	// sees; it is counted here because no HTTP status carried it.
	qoderNoteRefusal(http.StatusServiceUnavailable, message, wait)
	errType := "api_error"
	if wait > 0 {
		errType = upstreamBusyErrorType
	}
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": errType}})
	r.out = append(r.out, []byte("data: "+string(payload)+"\n\n"))
	r.done = true
}

// retryQueued waits out a queued model and asks again. It reports whether the
// turn was handed to a fresh attempt; false means the refusal has to surface.
//
// The wait is something the client is told about, not a silent pause: Qoder's
// own client reports the queue, and a request that sits mute for minutes reads
// as a hang. Nothing else can be tried instead — a queue is the model, not the
// account — so the turn stays here while the budget lasts.
func (r *qoderChunkReader) retryQueued(wait time.Duration) bool {
	if r.reopen == nil || r.dataSent || r.done {
		return false
	}
	if r.queueRounds >= qoderQueueMaxRounds {
		return false
	}
	if wait <= 0 {
		wait = qoderQueueDefaultWait
	}
	if wait > qoderQueueWaitMax {
		return false
	}
	if r.queueSince.IsZero() {
		r.queueSince = time.Now()
	}
	if time.Since(r.queueSince)+wait > qoderQueueBudget {
		return false
	}
	r.queueRounds++
	r.emitQueueNotice(wait)
	sleep := wait
	if sleep > qoderQueueSingleWait {
		sleep = qoderQueueSingleWait
	}
	qoderSleep(sleep + time.Second + qoderQueueJitter())
	return r.reload()
}

// retryTransient asks again after a fault that says the vendor's own upstream
// stumbled rather than this account: a couple of tries on the same account, then
// the failover loop decides.
func (r *qoderChunkReader) retryTransient(status int) bool {
	if r.reopen == nil || r.dataSent || r.done {
		return false
	}
	if status < http.StatusInternalServerError && status != qoderTransientStatus {
		return false
	}
	if r.reopenRounds >= qoderReopenRounds {
		return false
	}
	r.reopenRounds++
	qoderSleep(time.Duration(r.reopenRounds) * time.Second)
	return r.reload()
}

// reload swaps in a fresh upstream stream. The one being abandoned is closed
// here: it belongs to this reader, not to the caller of reopen.
func (r *qoderChunkReader) reload() bool {
	if r.reopen == nil {
		return false
	}
	_ = r.Close()
	body, err := r.reopen(r.ctx)
	if err != nil || body == nil {
		// Nothing more can be asked of this account; the refusal that stopped
		// the retry is what the client has to see.
		r.reopen = nil
		return false
	}
	r.scanner = newQoderScanner(body)
	if closer, ok := body.(io.Closer); ok {
		r.body = closer
	}
	return true
}

// emitQueueNotice tells the client the model is busy and how long it has waited.
// It is an ordinary chunk with no content, so every client keeps reading it as
// an empty delta while the queue clears.
func (r *qoderChunkReader) emitQueueNotice(wait time.Duration) {
	waited := int(time.Since(r.queueSince).Seconds())
	notice := map[string]any{
		"id":      r.id,
		"object":  "chat.completion.chunk",
		"created": r.created,
		"model":   r.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"content": ""}, "finish_reason": nil,
		}},
		"qoder_queue": map[string]any{
			"queued":         true,
			"waited_seconds": waited,
			"wait_seconds":   int(wait.Seconds()),
			"budget_seconds": int(qoderQueueBudget.Seconds()),
			"message": fmt.Sprintf("上游模型排队中（10605 / RESOURCE_LIMIT）：已等待 %d 秒，仍在队列中",
				waited),
		},
	}
	raw, err := json.Marshal(notice)
	if err != nil {
		return
	}
	r.out = append(r.out, []byte("data: "+string(raw)+"\n\n"))
}

// qoderQueueJitter is the spread added to a queue wait: every client of a busy
// model waking at the same instant would just rebuild the queue.
func qoderQueueJitter() time.Duration {
	if qoderQueueJitterMax <= 0 {
		return 0
	}
	return time.Duration(rand.Intn(qoderQueueJitterMax)) * time.Millisecond
}

// qoderEnvelopeQueued reports whether a failure frame names the model as busy.
// Qoder puts the code on the envelope and sometimes leaves only the queue state
// inside body, so both places are read.
func qoderEnvelopeQueued(code json.Number, body []byte) bool {
	if strings.TrimSpace(code.String()) == "10605" {
		return true
	}
	var state struct {
		IsQueued          bool  `json:"isQueued"`
		ServiceAvailable  *bool `json:"serviceAvailable"`
		RetryAfterSeconds int   `json:"retryAfterSeconds"`
	}
	if json.Unmarshal(body, &state) != nil {
		return false
	}
	return state.IsQueued || state.RetryAfterSeconds > 0 || (state.ServiceAvailable != nil && !*state.ServiceAvailable)
}

// qoderEnvelopeBody unwraps the chunk a Qoder frame carries: Qoder sends it as
// a JSON string holding the chunk's own JSON.
func qoderEnvelopeBody(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return []byte(asString)
	}
	return raw
}

// qoderEnvelopeStatus reads the envelope's own status, which Qoder sends as a
// number on most frames and as a quoted number on some. Zero means it named
// none, which the caller reads as a normal frame.
func qoderEnvelopeStatus(raw json.RawMessage) int {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return 0
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// qoderText is the streaming tool-call splitter. It holds text back until a
// whole call block arrives, then emits it as a call. A block on its way to being
// a call that cannot be read, or that the stream ends inside, is an error and
// never the answer; a block whose JSON is whole when the stream simply stops is
// still the call. A tag inside code or prose stays text.
type qoderText struct {
	// buf is what has arrived and is not yet decided: either the tail that might
	// still become a marker, or a block being read.
	buf     string
	kind    qoderEchoKind
	sawTool bool
	sent    string
	// allowed is the tool names the request declared. A block naming anything
	// else is prose about tools, so it is passed on as text rather than lifted.
	allowed map[string]bool
	// held counts frames that arrived without resolving the block, so a block
	// that never closes cannot hold the answer back forever.
	held int
}

type qoderFragment struct {
	text string
	call *qoderLiftedCall
	err  string
}

type qoderLiftedCall struct {
	name      string
	arguments string
}

const (
	qoderCallUnreadable = "Qoder：模型把工具调用写成了文本，但读不出来；不把它作为回答发出"
	qoderCallCutShort   = "Qoder：模型把工具调用写成了文本，还没写完流就结束了；不把它作为回答发出"
)

func (a *qoderText) feed(input string) []qoderFragment {
	a.buf += input
	var out []qoderFragment
	for {
		if a.kind == qoderEchoNone {
			kind, at := qoderEchoStart(a.buf)
			if at < 0 {
				keep := qoderEchoHoldTail(a.buf)
				if keep < len(a.buf) {
					give := a.buf[:len(a.buf)-keep]
					a.buf = a.buf[len(a.buf)-keep:]
					a.text(give)
					out = append(out, qoderFragment{text: give})
				}
				return out
			}
			if at > 0 {
				give := a.buf[:at]
				a.buf = a.buf[at:]
				a.text(give)
				out = append(out, qoderFragment{text: give})
			}
			a.kind = kind
			a.held = 0
			continue
		}
		block, rest, resolved := a.take()
		if !resolved {
			a.held++
			if len(a.buf) <= qoderEchoHoldMaxChars && a.held <= qoderEchoHoldMaxFrames {
				return out
			}
			// Past either limit the text is sent on: a block that never closes
			// must not cost the client its whole answer.
			give := a.buf
			a.buf, a.kind, a.held = "", qoderEchoNone, 0
			a.text(give)
			out = append(out, qoderFragment{text: give})
			continue
		}
		a.buf, a.held = rest, 0
		fragments := a.resolve(block)
		a.kind = qoderEchoNone
		out = append(out, fragments...)
		for _, fragment := range fragments {
			if fragment.err != "" {
				return out
			}
		}
	}
}

// take reads the block the current kind is waiting for: the block's bytes, what
// follows it, and whether the block arrived whole.
func (a *qoderText) take() (block, rest string, resolved bool) {
	switch a.kind {
	case qoderEchoTag:
		if end := strings.Index(a.buf[len(qoderCallOpen):], qoderCallClose); end >= 0 {
			cut := len(qoderCallOpen) + end + len(qoderCallClose)
			return a.buf[:cut], a.buf[cut:], true
		}
		return "", "", false
	case qoderEchoMarker:
		span := qoderEchoMarkerRe.FindStringIndex(a.buf)
		if span == nil {
			return "", "", false
		}
		_, end, ok := qoderEchoJSONSpan(a.buf[span[1]:])
		if !ok {
			return "", "", false
		}
		cut := span[1] + end
		return a.buf[:cut], a.buf[cut:], true
	case qoderEchoDSML:
		matches := qoderDSMLInvoke.FindAllStringSubmatchIndex(a.buf, -1)
		if len(matches) == 0 {
			return "", "", false
		}
		// Every invoke that closed is taken; the rest stays for the next frame.
		cut := matches[len(matches)-1][1]
		return a.buf[:cut], a.buf[cut:], true
	}
	return "", "", false
}

// resolve turns one whole block into fragments for the current kind.
func (a *qoderText) resolve(block string) []qoderFragment {
	// Inside a code fence the block is an example being discussed, not a call;
	// the <tool_call> path below decides that for itself.
	if a.kind != qoderEchoTag && qoderQuoted(a.sent) {
		a.text(block)
		return []qoderFragment{{text: block}}
	}
	switch a.kind {
	case qoderEchoTag:
		body := strings.TrimSuffix(strings.TrimPrefix(block, qoderCallOpen), qoderCallClose)
		if call, ok := qoderParseCall(body); ok {
			a.sawTool = true
			return []qoderFragment{{call: &call}}
		}
		if qoderCallish(body) && !qoderQuoted(a.sent) {
			return []qoderFragment{{err: qoderCallUnreadable}}
		}
	case qoderEchoMarker:
		span := qoderEchoMarkerRe.FindStringIndex(block)
		if span == nil {
			break
		}
		start, end, ok := qoderEchoJSONSpan(block[span[1]:])
		if !ok {
			break
		}
		payload := block[span[1]+start : span[1]+end]
		verdict, parsed := qoderParseEchoArray(payload, a.allowed)
		if !parsed {
			break
		}
		if verdict.unreadable && !qoderQuoted(a.sent) {
			// The array parsed and named real tools, but an entry's arguments
			// are not JSON: the model meant to call something this turn, so the
			// turn fails rather than pass a半-readable call on.
			return []qoderFragment{{err: qoderCallUnreadable}}
		}
		if verdict.declared && len(verdict.calls) > 0 {
			a.sawTool = true
			fragments := make([]qoderFragment, 0, len(verdict.calls))
			for index := range verdict.calls {
				fragments = append(fragments, qoderFragment{call: &verdict.calls[index]})
			}
			return fragments
		}
	case qoderEchoDSML:
		verdict, ok := qoderParseEchoDSML(block)
		if !ok || !verdict.declared || len(verdict.calls) == 0 {
			break
		}
		a.sawTool = true
		fragments := make([]qoderFragment, 0, len(verdict.calls))
		for index := range verdict.calls {
			fragments = append(fragments, qoderFragment{call: &verdict.calls[index]})
		}
		return fragments
	}
	// Anything that did not turn out to be a call is the answer, verbatim.
	a.text(block)
	return []qoderFragment{{text: block}}
}

// text remembers what has been given out: whether the next block is inside code
// is told from what came before it.
func (a *qoderText) text(given string) { a.sent += given }

func (a *qoderText) flush() []qoderFragment {
	kind, block := a.kind, a.buf
	a.kind, a.buf, a.held = qoderEchoNone, "", 0
	if block == "" {
		return nil
	}
	switch kind {
	case qoderEchoTag:
		body := strings.TrimPrefix(block, qoderCallOpen)
		if qoderCallish(body) && !qoderQuoted(a.sent) {
			if call, ok := qoderParseCall(body); ok {
				a.sawTool = true
				return []qoderFragment{{call: &call}}
			}
			return []qoderFragment{{err: qoderCallCutShort}}
		}
	case qoderEchoMarker:
		// A marker that never completed is either the gateway's own bookkeeping
		// echoed back (dropped: the user was never meant to read it) or an
		// answer that merely talks about the marker (sent on). Only the first
		// has JSON behind it.
		if !qoderQuoted(a.sent) && qoderMarkerOpensJSON(block) {
			return nil
		}
	case qoderEchoDSML:
		if verdict, ok := qoderParseEchoDSML(block); ok && verdict.declared && len(verdict.calls) > 0 {
			a.sawTool = true
			fragments := make([]qoderFragment, 0, len(verdict.calls))
			for index := range verdict.calls {
				fragments = append(fragments, qoderFragment{call: &verdict.calls[index]})
			}
			return fragments
		}
		if !qoderQuoted(a.sent) {
			return nil
		}
	}
	a.text(block)
	return []qoderFragment{{text: block}}
}

// qoderCallish says a block was on its way to being a call: its JSON began, or
// the XML function form appeared.
func qoderCallish(value string) bool {
	return strings.HasPrefix(strings.TrimLeft(value, " \t\r\n"), "{") || strings.Contains(value, "\x3cfunction=")
}

// qoderQuoted says the text ends inside a code fence or an inline code span: a
// tool-call block there is prose about one, not a call.
func qoderQuoted(text string) bool {
	if strings.Count(text, "```")%2 == 1 {
		return true
	}
	line := text[strings.LastIndex(text, "\n")+1:]
	return strings.Count(strings.ReplaceAll(line, "```", ""), "`")%2 == 1
}

// qoderParseCall reads one tool-call block: a JSON {name, arguments}, or the
// XML function/parameter form.
func qoderParseCall(value string) (qoderLiftedCall, bool) {
	var framed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &framed) == nil && strings.TrimSpace(framed.Name) != "" {
		var arguments map[string]json.RawMessage
		if json.Unmarshal(framed.Arguments, &arguments) == nil && arguments != nil {
			return qoderLiftedCall{name: strings.TrimSpace(framed.Name), arguments: string(framed.Arguments)}, true
		}
	}
	match := qoderFuncRe.FindStringSubmatch(value)
	if len(match) != 3 {
		return qoderLiftedCall{}, false
	}
	name := strings.TrimSpace(match[1])
	if name == "" {
		return qoderLiftedCall{}, false
	}
	arguments := map[string]any{}
	for _, parameter := range qoderParamRe.FindAllStringSubmatch(match[2], -1) {
		key := strings.TrimSpace(parameter[1])
		if key == "" {
			continue
		}
		raw := strings.TrimSpace(parameter[2])
		var decoded any
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			decoded = raw
		}
		arguments[key] = decoded
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		return qoderLiftedCall{}, false
	}
	return qoderLiftedCall{name: name, arguments: string(payload)}, true
}
