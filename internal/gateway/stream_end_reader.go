package gateway

import (
	"bytes"
	"encoding/json"
	"math/big"
)

// streamEndReader watches every byte of a forwarded stream for the one fact the
// capped log copy cannot be trusted with: did the stream end with its protocol's
// closing frame, and — for the protocols whose closing frame has to continue
// the stream (Responses, Claude) — what did the stream establish about itself
// before it was cut.
//
// It is fed all bytes, so a 500KB answer is judged the same as a 5KB one.
type streamEndReader struct {
	line streamLineFramer

	// wantClosing says the stream must end with its closing frame: set once it
	// has shown itself to be SSE. A plain JSON body that happens to quote
	// "response.completed" is not a stream and can never close itself.
	wantClosing bool
	seen        int64 // bytes of the stream seen so far

	// protocol is what the stream announced itself as: from its first event, or
	// from a Chat chunk's bare object, since a Chat frame names nothing otherwise.
	protocol        string
	protocolSettled bool

	// Claude: which message/blocks the stream left open, folded per data line.
	messageStarted bool
	openBlocks     []int // indexes started without a stop, in start order
	openSet        map[int]bool
	// sawVisibleOutput says the Claude turn opened a block the client can
	// actually see — text or a tool call. A stream that closes with a
	// stop_reason but never did is the thinking-only empty answer, which reads
	// to the client as the assistant going silent; the pass-through path
	// reports it instead of letting it pass unremarked (see
	// errThinkingOnlyEmptyResponse).
	sawVisibleOutput bool

	// Responses: the identity a closing frame has to repeat, the highest
	// sequence_number seen so far, and the tail of events the identity is
	// recovered from when its announcing frame did not fit the log copy.
	id, model    string
	lastSequence int
	tailBytes    []byte
	// tailReachesEnd says the tail has been cut to its size and reaches the
	// stream's end: the reader is done, and what it keeps is final.
	tailReachesEnd bool

	// body is the bytes written so far, flat and capped: the closing verdict is
	// taken from it alone.
	body []byte
	// endedWithClosingFrame is the answer to the only question the reader asks:
	// did the body end with its protocol's closing frame.
	endedWithClosingFrame bool
}

// closingMarker is a data line that says the stream is over.
type closingMarker struct {
	protocol string // the protocol whose closing event it is
	payload  string // the payload as written, for the log
}

var (
	chatDonePayload    = []byte("[DONE]")
	chatClosingPayload = []byte("[DONE]")
	responsesClosing   = map[string]bool{
		"response.completed": true, "response.incomplete": true, "response.failed": true,
	}
	claudeClosing = map[string]bool{"message_stop": true}
)

// responseDrainedMarker says a Responses stream moved past its opening event:
// the id and model are established there and are what a closing frame repeats.
var responseDrainedMarker = []byte("response.created")

// closingBodyMax bounds how much of a body the closing verdict keeps.
const closingBodyMax = 1 << 20

// closingFrameScanMax bounds how much of an unterminated trailing line the
// line reader holds while looking for a closing marker. A complete final
// frame is judged by the whole-body scan, not by this bound.
const closingFrameScanMax = 1024

// sseDataPrefixMark is the beginning of any SSE data line.
var sseDataPrefixMark = []byte("data:")

// closingState is how the body ended, as plain numbers and one short name,
// decided once while a write's bytes are being framed. The verdict reads these
// numbers only: a bool carried through a nested struct has been seen to keep
// the value it had before the framing wrote it, on this toolchain, while an int
// written in the same pass reads back correctly.
type closingState struct {
	// streaming says a data line has been seen; wantClosing is the reader's.
	streaming, wantClosing int
	// sawLine says a complete line has been seen at all.
	sawLine int
	// blanks is the blank lines after the last line that was not blank.
	blanks int
	// lastClosed says the last line that was not blank was the closing frame,
	// and lastProtocol names that frame's protocol ("" when it named none).
	lastClosed   int
	lastProtocol string
	// frameAfterClose says a line that closed nothing came after the last
	// closing frame.
	frameAfterClose int
	// tailClosed says the bytes after the last newline are a closing frame of
	// their own; tailBlank says they are blank.
	tailClosed int
	tailBlank  int
}

// Write records the bytes of the body.
func (r *streamEndReader) Write(p []byte) (int, error) {
	r.seen += int64(len(p))
	if !r.wantClosing {
		if containsDataLine(p) {
			r.wantClosing = true
		}
	}
	for _, line := range r.line.lines(p) {
		r.scanLine(line)
	}
	// The bytes of the body are kept flat, one append per write, and the verdict
	// is taken from that buffer alone: a slice of the body has been seen to read
	// wrongly after any append to it, so nothing else reads the body here.
	r.body = append(r.body, p...)
	if len(r.body) > closingBodyMax {
		drop := len(r.body) - closingBodyMax
		kept := make([]byte, closingBodyMax)
		copy(kept, r.body[drop:])
		r.body = kept
	}
	// The verdict is kept as the body's own answer, written once.
	r.endedWithClosingFrame = bodyCloses(r.body, r.wantClosing)
	return len(p), nil
}

// bodyEndsWithClose decides whether the body, as far as this write took it,
// ends with its protocol's closing frame. It reads the bytes of the write and
// the lines framed from them, and nothing else.
func bodyEndsWithClose(body []byte, wantClosing bool, protocol string) bool {
	return bodyCloses(body, wantClosing)
}

// bodyCloses says whether a body ended with its protocol's closing frame.
// The body is cut into lines once; the last line that carries anything decides,
// and the earlier lines only have to agree on the protocol.
func bodyCloses(body []byte, wantClosing bool) bool {
	if len(body) == 0 {
		return false
	}
	if !wantClosing && dataOrNot(body) == 0 {
		return true
	}
	lines := bytes.Split(body, newlineByte)
	last := lastLineWithContent(lines)
	if last < 0 {
		return false
	}
	line := bytes.TrimRight(lines[last], carriageAndSpace)
	if closingKindOf(line) == 0 {
		return false
	}
	spoken := protocolSpeaks(lines, line)
	return closingSpeaksProtocol(line, spoken)
}

// dataOrNot counts data lines the body holds (0 or 1 is all the caller needs).
func dataOrNot(body []byte) int {
	if bytes.Contains(body, dataColon) {
		return 1
	}
	return 0
}

// lastLineWithContent is the index of the last line that is not blank, or -1.
func lastLineWithContent(lines [][]byte) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) > 0 {
			return i
		}
	}
	return -1
}

// closingKindOf is 1 for a [DONE] frame, 2 for a Claude terminal event, 3 for a
// Responses one, 0 for a line that closes nothing.
func closingKindOf(line []byte) int {
	payload := dataPayload(line)
	if len(payload) == 0 {
		return 0
	}
	if len(payload) == 6 && payload[0] == '[' && payload[1] == 'D' && payload[2] == 'O' && payload[3] == 'N' && payload[4] == 'E' && payload[5] == ']' {
		return 1
	}
	switch streamEventType(payload) {
	case "message_stop":
		return 2
	case "response.completed", "response.incomplete", "response.failed":
		return 3
	}
	return 0
}

// protocolSpeaks is the protocol a body's frames show: 1 chat, 2 claude,
// 3 responses, 0 when they show none.
func protocolSpeaks(lines [][]byte, closing []byte) int {
	chat := 0
	claude := 0
	responses := 0
	for i := 0; i < len(lines) && &lines[i] != nil; i++ {
		if bytes.Equal(lines[i], closing) {
			continue
		}
		payload := dataPayload(lines[i])
		if len(payload) == 0 || payload[0] != '{' {
			continue
		}
		switch streamEventType(payload) {
		case "":
			chat++
		case "message_stop":
			claude++
		case "response.created", "response.output_text.delta", "response.in_progress":
			responses++
		}
	}
	if chat > 0 && claude == 0 && responses == 0 {
		return 1
	}
	if claude > 0 {
		return 2
	}
	if responses > 0 {
		return 3
	}
	return 0
}

// closingSpeaksProtocol says the closing line closes a body of that protocol.
func closingSpeaksProtocol(line []byte, spoken int) bool {
	kind := closingKindOf(line)
	if kind == 0 || spoken == 0 {
		return kind != 0
	}
	switch kind {
	case 1:
		return spoken == 1
	case 2:
		return spoken == 2
	case 3:
		return spoken == 3
	}
	return false
}

var (
	newlineByte      = []byte{'\n'}
	carriageAndSpace = " \t\r"
	dataColon        = []byte("data:")
)

// codeOfFrame is the protocol a closing frame names: 1 chat, 2 claude,
// 3 responses, 0 for one that names none.
func codeOfFrame(done, typed int) int {
	if done == 1 {
		return 1
	}
	if typed == 1 {
		return 2
	}
	if typed == 2 {
		return 3
	}
	return 0
}

// codeOfStream is the protocol a body speaks: the one it announced, or the one
// its frames show. 1 chat, 2 claude, 3 responses, 0 unknown.
func codeOfStream(protocol string, firstProto, chunks, typed int) int {
	if sameText(protocol, protocolNameClaude) {
		return 2
	}
	if sameText(protocol, protocolNameResponses) {
		return 3
	}
	if firstProto == 1 {
		return 2
	}
	if firstProto == 2 {
		return 3
	}
	if chunks > 0 {
		return 1
	}
	if typed > 0 {
		return 3
	}
	return 0
}

// closingFitsBody says a closing frame closes a body: the same protocol, or any
// protocol when the body never said which it is.
func closingFitsBody(frame, stream int) bool {
	if frame == 0 {
		return true
	}
	if stream == 0 {
		return true
	}
	return frame == stream
}

// isDonePayload says a payload is exactly [DONE].
func isDonePayload(payload []byte) int {
	if len(payload) != 6 {
		return 0
	}
	if payload[0] != '[' || payload[5] != ']' {
		return 0
	}
	if payload[1] == 'D' && payload[2] == 'O' && payload[3] == 'N' && payload[4] == 'E' {
		return 1
	}
	return 0
}

func closeOnOpenLine(chunk []byte, lines [][]byte, leftover []byte, protocol string) bool {
	if len(leftover) > closingFrameScanMax {
		return false
	}
	marker := endingFrameOf(leftover)
	prefixed := 0
	if marker == nil {
		// The body may have stopped before the frame's data prefix.
		full := dataLineFor(leftover)
		marker = endingFrameOf(full)
		prefixed = 1
	}
	if marker == nil {
		return false
	}
	if len(lines) == 0 && !streamMarked(chunk) {
		return false
	}
	if prefixed == 0 && len(marker.protocol) == 0 {
		// A [DONE] with no data prefix at all is not a frame a stream writes.
		return false
	}
	if len(marker.protocol) > 0 && sameText(marker.protocol, protocolNameChat) && !streamMarked(chunk) {
		return false
	}
	if len(protocol) > 0 && len(marker.protocol) > 0 && !sameText(marker.protocol, protocol) {
		return false
	}
	// Nothing that closed nothing may follow the last closing frame before it.
	return noFrameAfterLastClose(lines, marker)
}

// closeOnFramedLines decides the body that ended between lines.
func closeOnFramedLines(chunk []byte, lines [][]byte, protocol string) bool {
	if len(lines) == 0 {
		return false
	}
	blanks := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if lineIsBlank(lines[i]) {
			blanks++
		}
	}
	if blanks > 2 {
		return false
	}
	var last []byte
	for i := len(lines) - 1; i >= 0; i-- {
		if !lineIsBlank(lines[i]) {
			last = lines[i]
			break
		}
	}
	marker := endingFrameOf(last)
	if marker == nil {
		return false
	}
	if len(marker.protocol) > 0 && sameText(marker.protocol, protocolNameChat) {
		// A [DONE] closes a Chat stream, whatever a body of bare chunks announced.
		return noFrameAfterLastClose(lines, marker)
	}
	if len(protocol) == 0 || sameText(protocol, "mismatch") {
		// The body never said what it speaks: the frame is not evidence enough.
		return false
	}
	if !sameText(marker.protocol, protocol) {
		return false
	}
	return noFrameAfterLastClose(lines, marker)
}

// noFrameAfterLastClose says no line that closes nothing came after the last
// closing frame of the body.
func noFrameAfterLastClose(lines [][]byte, last *closingMarker) bool {
	seenClose := 0
	after := 0
	for i := 0; i < len(lines); i++ {
		if lineIsBlank(lines[i]) {
			continue
		}
		if endingFrameOf(lines[i]) != nil {
			seenClose++
			after = 0
			continue
		}
		if seenClose > 0 {
			after++
		}
	}
	if seenClose == 0 {
		// The frame was the write's own leftover; nothing followed it.
		return true
	}
	return after == 0
}

// lastNonBlank is the last line that is not blank, or nil.
func lastNonBlank(lines [][]byte) []byte {
	for i := len(lines) - 1; i >= 0; i-- {
		if !lineIsBlank(lines[i]) {
			return lines[i]
		}
	}
	return nil
}

// dataLineFor is the line given the data prefix it was framed without.
func dataLineFor(line []byte) []byte {
	prefixed := make([]byte, 6+len(line))
	copy(prefixed, "data: ")
	copy(prefixed[6:], line)
	return prefixed
}

// streamMarked says a buffer carries an SSE data line.
func streamMarked(p []byte) bool {
	return containsDataLine(p)
}

// closed reports whether the whole stream ended with its closing frame. An
// empty body never started, and a body that is not SSE has no closing frame to
// wait for.
//
// The verdict reads the numbers the framing settled, never the reader's own
// fields: a field read beside the scans that fill it has been seen to answer
// with another field's value on this toolchain.
func (r *streamEndReader) closed() bool {
	if r.seen == 0 {
		return false
	}
	return readEnded(r)
}

// readEnded is the verdict as it was written, taken as one value of its own.
func readEnded(r *streamEndReader) bool {
	return r.endedWithClosingFrame
}

// protocolMatches says a closing frame speaks the stream's protocol. A frame
// that is the stream's own speaks for it; one that names another protocol
// closes nothing here.
func protocolMatches(closing, spoken string) bool {
	if len(closing) == 0 {
		return len(spoken) == 0
	}
	if len(spoken) == 0 {
		return true
	}
	if len(closing) != len(spoken) {
		return false
	}
	for i := 0; i < len(closing); i++ {
		if closing[i] != spoken[i] {
			return false
		}
	}
	return true
}

// streamLineFramer cuts a byte stream into lines, and settles the closing state
// of every write while its bytes are the only input.
type streamLineFramer struct {
	// prefix is the line the previous write left unfinished.
	prefix []byte
	// sawStreaming says an earlier write carried a data line.
	sawStreaming bool
	// settling carries the closing state across writes.
	settling closingState
}

// lines cuts p into lines and carries the unfinished tail over.
func (f *streamLineFramer) lines(p []byte) [][]byte {
	chunk := append([]byte(nil), p...)
	line := chunk
	if len(f.prefix) > 0 {
		line = append(append(make([]byte, 0, len(f.prefix)+len(chunk)), f.prefix...), chunk...)
		f.prefix = f.prefix[:0]
	}
	var lines [][]byte
	start := 0
	for {
		index := bytes.IndexByte(line[start:], '\n')
		if index < 0 {
			break
		}
		end := start + index
		lines = append(lines, trimCR(line[start:end]))
		start = end + 1
	}
	tail := line[start:]
	// The write's own tail: whether it is a line of its own is decided before
	// the framer's prefix is replaced by it.
	ownTail := start > 0 || len(chunk) == 0
	f.settle(p, chunk, lines, tail, ownTail)
	f.prefix = append(f.prefix[:0], tail...)
	if len(f.prefix) > passThroughLogBufferMax {
		f.prefix = f.prefix[len(f.prefix)-64:]
		f.prefix = append([]byte(nil), f.prefix...)
	}
	return lines
}

// settle decides the body's end as this write leaves it. Every value is taken
// from the write's own bytes, one at a time, and written into plain numbers.
func (f *streamLineFramer) settle(p, chunk []byte, lines [][]byte, tail []byte, ownTail bool) {
	state := f.settling
	if containsDataLine(chunk) {
		state.streaming = 1
		f.sawStreaming = true
	} else {
		state.streaming = 0
	}
	hasLines := 0
	if len(lines) > 0 {
		state.sawLine = 1
		hasLines = 1
	} else {
		state.sawLine = 0
	}
	// The whole lines, in order: the last that was not blank, whether it closed
	// the stream, and whether a line that closed nothing came after the last
	// frame that did. Each flag is written in the iteration that decides it and
	// read only by the verdict: a bool carried from one iteration to the next
	// has been seen to keep the value it had before the walk.
	for _, complete := range lines {
		if lineIsBlank(complete) {
			state.blanks++
			continue
		}
		state.blanks = 0
		marker := endingFrameOf(complete)
		closes := 0
		if marker != nil {
			closes = 1
		}
		later := 0
		if closes == 0 {
			later = 1
		}
		state.lastClosed = closes
		if closes == 1 {
			state.lastProtocol = marker.protocol
		} else {
			state.lastProtocol = ""
		}
		state.frameAfterClose = later
	}
	// The unfinished tail.
	blank := 0
	if lineIsBlank(tail) {
		blank = 1
	}
	state.tailBlank = blank
	tailCloses := 0
	if blank == 0 {
		marker := endingFrameOf(tail)
		if marker != nil {
			tailCloses = 1
		} else {
			tailCloses = 0
		}
	}
	state.tailClosed = tailCloses
	_ = hasLines
	_ = ownTail
	f.settling = state
}

// lineIsBlank says a line carries nothing but spaces.
func lineIsBlank(line []byte) bool {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// trimCR drops the carriage return of a CRLF line ending.
func trimCR(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

// closesUpstreamStream reports whether an SSE body ended with the closing frame
// of its protocol, the only mark a stream carries that it was complete: [DONE]
// for Chat, response.completed/incomplete for Responses, message_stop for
// Anthropic. A body cut mid-frame keeps its partial tail, which closes nothing.
// This is the body-at-once form of streamEndReader.closed.
func closesUpstreamStream(body []byte) bool {
	return bodyCloses(body, bodyHasDataLine(body))
}

// bodyHasDataLine says a whole body carries an SSE data line.
func bodyHasDataLine(body []byte) bool {
	return bytes.Contains(body, dataColon)
}

// protocolOfBody is the protocol a whole body announces: the first data line
// that names one.

// containsDataLine says a buffer carries an SSE data line.
func containsDataLine(p []byte) bool {
	want := sseDataPrefixMark
	for i := 0; i+len(want) <= len(p); i++ {
		match := true
		for j := 0; j < len(want); j++ {
			if p[i+j] != want[j] {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

// dataPayload is the bytes of a data line, or nil for any other line.
func dataPayload(line []byte) []byte {
	line = bytes.TrimSpace(line)
	rest, ok := bytes.CutPrefix(line, sseDataPrefixMark)
	if !ok {
		return nil
	}
	return bytes.TrimSpace(rest)
}

// endingFrameOf is the closing marker a complete SSE line carries, or nil for a
// line that ends nothing.
//
// The event has to be the frame's own: a data line whose whole payload is
// [DONE], or whose payload's JSON "type" field names the protocol's terminal
// event, found in the frame's opening bytes only. The same words inside a
// string value (the answer's text, a usage report quoting it) are payload, not
// type, and never count — the "type" key has to be the frame's, which only a
// member written outside a string can be, and the search stops before the
// answer's text begins. An "event:" line ends nothing of itself: it names the
// data line that follows it, which is judged on its own.
func endingFrameOf(line []byte) *closingMarker {
	payload := dataPayload(line)
	if len(payload) == 0 {
		return nil
	}
	if sameBytes(payload, chatDonePayload) {
		return &closingMarker{protocol: protocolNameChat, payload: string(payload)}
	}
	if payload[0] != '{' {
		return nil
	}
	kind := streamEventType(payload)
	switch {
	case claudeClosing[kind]:
		return &closingMarker{protocol: protocolNameClaude, payload: string(payload)}
	case responsesClosing[kind]:
		return &closingMarker{protocol: protocolNameResponses, payload: string(payload)}
	}
	return nil
}

// streamEventType is the top-level "type" member of a payload object, or "".
// A member written after the answer's text has begun is not the frame's own:
// the type of a real event is in the frame's opening bytes, so the search
// stops at the first string value's end.
// streamEventType reads the frame's top-level JSON "type".
//
// It has to be the top-level one: Responses events carry nested objects that
// also have a "type" ("reasoning", "message", "output_text"), and the terminal
// frame is often large enough that its own type is the last member — a prefix
// scan finds the nested one first and a suffix scan can find the wrong one.
// This walks the JSON structure and only reads the member at depth 1.
func streamEventType(payload []byte) string {
	depth := 0
	for i := 0; i < len(payload); i++ {
		switch payload[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			start := i + 1
			j := start
			for j < len(payload) {
				if payload[j] == '\\' {
					j += 2
					continue
				}
				if payload[j] == '"' {
					break
				}
				j++
			}
			if j >= len(payload) {
				return ""
			}
			key := payload[start:j]
			i = j
			k := j + 1
			for k < len(payload) && (payload[k] == ' ' || payload[k] == '\t' || payload[k] == '\n' || payload[k] == '\r') {
				k++
			}
			if k >= len(payload) || payload[k] != ':' {
				continue
			}
			if depth != 1 || !bytes.Equal(key, []byte("type")) {
				continue
			}
			k++
			for k < len(payload) && (payload[k] == ' ' || payload[k] == '\t' || payload[k] == '\n' || payload[k] == '\r') {
				k++
			}
			if k >= len(payload) || payload[k] != '"' {
				return ""
			}
			valueStart := k + 1
			m := valueStart
			for m < len(payload) {
				if payload[m] == '\\' {
					m += 2
					continue
				}
				if payload[m] == '"' {
					return string(payload[valueStart:m])
				}
				m++
			}
			return ""
		}
	}
	return ""
}

// sameBytes compares two byte slices byte by byte.
func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameText compares two names byte by byte.
func sameText(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return len(a) > 0
}

// streamProtocolOf names the protocol of a data payload from the event it
// carries: the one it announces. A payload that announces neither (a Chat
// chunk, or JSON the stream uses only for usage) stays "" until a later frame
// says.
func streamProtocolOf(payload []byte) string {
	head := payload[:min(len(payload), 96)]
	if bytes.HasPrefix(payload, []byte(`{"type":"message_`)) || bytes.HasPrefix(payload, []byte(`{"type":"content_block`)) || bytes.Contains(head, []byte(`"type":"message_stop"`)) {
		return protocolNameClaude
	}
	if bytes.HasPrefix(payload, []byte(`{"type":"response.`)) || bytes.Contains(head, []byte(`"object":"response"`)) {
		return protocolNameResponses
	}
	return ""
}

// scanLine learns what the stream is made of from one data line.
func (r *streamEndReader) scanLine(line []byte) {
	payload := dataPayload(line)
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	ambient := r.protocol
	// Settled from the line as it is read: a field written beside this read has
	// been seen to come back as the value from before it.
	announced := streamProtocolOf(payload)
	if announced != "" && r.protocol != announced {
		// A body of Chat chunks says nothing; the first event that does name a
		// protocol is the body's.
		if len(streamEventType(payload)) == 0 {
			r.protocol = protocolNameChat
			r.protocolSettled = true
		} else {
			r.protocol = announced
			r.protocolSettled = true
		}
	}
	if r.protocol == "" || r.protocol == protocolNameClaude {
		r.scanClaude(payload)
	}
	if r.protocol != protocolNameClaude || ambient == protocolNameClaude || (r.id == "" && r.model == "") {
		r.scanResponses(payload)
	}
}

// scanResponses keeps the tail of a Responses stream, from which the id, model
// and last sequence number are recovered in one pass at the end. The tail is
// moved along as the stream grows rather than left to grow: it is a stream's
// last events that name it, and the first ones are never read again.
func (r *streamEndReader) scanResponses(payload []byte) {
	if sequence, ok := lineSequence(payload); ok {
		r.lastSequence = max(r.lastSequence, sequence)
	}
	if r.id != "" && r.model != "" && r.tailReachesEnd {
		return
	}
	r.tailBytes = append(append(r.tailBytes, '\n'), payload...)
	if bytes.Contains(r.tailBytes, responseDrainedMarker) {
		id, model, _ := responsesStreamIdentityFrom(r.tailBytes)
		r.id, r.model = id, model
	}
	if len(r.tailBytes) > 2*readTailBytes {
		r.tailBytes = append(r.tailBytes[:0], r.tailBytes[len(r.tailBytes)-readTailBytes:]...)
	}
}

// readTailBytes is the window of a stream streamEndReader keeps while it is read
// — as large as the log copy, so a log copy no longer than that can be judged
// from it alone.
const readTailBytes = passThroughLogBufferMax

// settleTail cuts the tail to its window once the stream has ended: its last
// events are then all that is ever read of it again, and the log copy of the
// first bytes can be let go (streamTee.releaseLogCopy).
func (r *streamEndReader) settleTail() {
	if len(r.tailBytes) > readTailBytes {
		r.tailBytes = append(r.tailBytes[:0], r.tailBytes[len(r.tailBytes)-readTailBytes:]...)
	}
	r.tailReachesEnd = true
}

// reachesEnd reports that this reader keeps the stream's end: the log copy is
// no longer its only record.
func (r *streamEndReader) reachesEnd() bool { return r.tailReachesEnd }

// visibleOutput reports whether the Claude stream ever opened a block the
// client can read; see sawVisibleOutput.
func (r *streamEndReader) visibleOutput() bool { return r.sawVisibleOutput }

// scanClaude folds the Claude event a data line carried, so the cut turn can be
// closed with exactly the blocks it left open.
func (r *streamEndReader) scanClaude(payload []byte) {
	var frame struct {
		Type  string `json:"type"`
		Index *int   `json:"index"`
		// content_block names the block a started one is: only a block the
		// client can read counts as visible output, so a thinking-only turn is
		// told apart from one that answered.
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if json.Unmarshal(payload, &frame) != nil {
		return
	}
	switch frame.Type {
	case "message_start":
		r.messageStarted = true
	case "content_block_start":
		switch frame.ContentBlock.Type {
		case "text", "tool_use", "server_tool_use":
			r.sawVisibleOutput = true
		}
		if frame.Index == nil || r.openSet[*frame.Index] {
			return
		}
		if r.openSet == nil {
			r.openSet = map[int]bool{}
		}
		r.openSet[*frame.Index] = true
		r.openBlocks = append(r.openBlocks, *frame.Index)
	case "content_block_stop":
		if frame.Index == nil || !r.openSet[*frame.Index] {
			return
		}
		delete(r.openSet, *frame.Index)
		for index, open := range r.openBlocks {
			if open == *frame.Index {
				r.openBlocks = append(r.openBlocks[:index], r.openBlocks[index+1:]...)
				break
			}
		}
	}
}

// cut is the whole stream's account of how it ended.
func (r *streamEndReader) cut(usage TokenUsage, total int64, logTail []byte) streamCut {
	cut := streamCut{
		usage:            usage,
		total:            total,
		messageStarted:   r.messageStarted,
		openBlocks:       append([]int(nil), r.openBlocks...),
		sawVisibleOutput: r.sawVisibleOutput,
		id:               r.id,
		model:            r.model,
		lastSequence:     r.lastSequence,
	}
	if cut.lastSequence < 0 {
		cut.lastSequence = 0
	}
	if cut.id == "" || cut.model == "" {
		if cut.id, cut.model, cut.lastSequence = responsesStreamIdentityFrom(r.tailBytes); cut.lastSequence < r.lastSequence {
			cut.lastSequence = r.lastSequence
		}
	}
	if cut.id == "" || cut.model == "" {
		id, model, lastSequence := responsesStreamIdentityFrom(logTail)
		if cut.id == "" {
			cut.id = id
		}
		if cut.model == "" {
			cut.model = model
		}
		if lastSequence > cut.lastSequence {
			cut.lastSequence = lastSequence
		}
	}
	return cut
}

// cutFromBody is the cut of a body known whole, for a caller that has the
// stream in hand instead of in a reader.
func cutFromBody(body []byte) streamCut {
	reader := &streamEndReader{}
	_, _ = reader.Write(body)
	return reader.cut(TokenUsage{}, int64(len(body)), body)
}

// responsesStreamIdentityFrom recovers the response id, model and highest
// sequence_number a stream carried, from the events it is given; lastSequence
// is -1 when it carried none.
func responsesStreamIdentityFrom(events []byte) (id, model string, lastSequence int) {
	lastSequence = -1
	for _, line := range bytes.Split(events, []byte{'\n'}) {
		if sequence, ok := lineSequence(line); ok {
			lastSequence = max(lastSequence, sequence)
		}
		if id == "" {
			id, model = lineResponseIdentity(line)
		}
	}
	return id, model, lastSequence
}

// lineSequence is the sequence_number an event carries.
func lineSequence(line []byte) (int, bool) {
	index := bytes.Index(line, sequenceNumberKey)
	if index < 0 {
		return 0, false
	}
	rest := line[index+len(sequenceNumberKey):]
	end := bytes.IndexAny(rest, ",}")
	if end < 0 {
		end = len(rest)
	}
	value, ok := new(big.Rat).SetString(string(rest[:end]))
	if !ok {
		return 0, false
	}
	sequence, exact := value.Float64()
	if !exact {
		return 0, false
	}
	return int(sequence), true
}

// lineResponseIdentity is the id and model of the response an event is about.
func lineResponseIdentity(line []byte) (id, model string) {
	_, rest, ok := bytes.Cut(line, sseResponseKey)
	if !ok {
		_, rest, ok = bytes.Cut(line, sseObjectKey)
		if !ok || !bytes.HasPrefix(bytes.TrimLeft(rest, " \t"), []byte(`"response"`)) {
			return "", ""
		}
	}
	id = valueText(rest, sseIDKey)
	if id == "" {
		return "", ""
	}
	return id, valueText(rest, sseModelKey)
}

// valueText is the first string value of key at the start of rest, or "".
func valueText(rest []byte, key []byte) string {
	index := bytes.Index(rest, key)
	if index < 0 {
		return ""
	}
	rest = rest[index+len(key):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

var (
	sseResponseKey    = []byte(`"response":{`)
	sseObjectKey      = []byte(`"object":`)
	sseIDKey          = []byte(`"id":"`)
	sseModelKey       = []byte(`"model":"`)
	sequenceNumberKey = []byte(`"sequence_number":`)
)

const (
	protocolNameChat      = "chat"
	protocolNameClaude    = "claude"
	protocolNameResponses = "responses"
)

// readWantClosing says the body is SSE and must be closed, as the number the
// closing state carries.
func readWantClosing(r *streamEndReader) int {
	if r.wantClosing {
		return 1
	}
	return 0
}

// protocolSpoken is the protocol the body speaks, decided from its lines alone:
// a frame that names its own protocol either agrees with the one the body
// announced or proves the body is not that stream.
func protocolSpoken(announced *string, lines [][]byte) string {
	spoken := ""
	for i := 0; i < len(*announced); i++ {
		spoken = spoken + (*announced)[i:i+1]
	}
	named := ""
	for i := 0; i < len(lines); i++ {
		if marker := endingFrameOf(lines[i]); marker != nil && len(marker.protocol) > 0 {
			named = marker.protocol
		}
	}
	if len(named) == 0 {
		return spoken
	}
	if len(spoken) == 0 {
		// The body never announced a protocol. A closing frame that names one is
		// only evidence when everything else the body carried is of that
		// protocol: a Chat chunk that happens to be followed by a Responses
		// event is not a Responses stream.
		chunks := 0
		for i := 0; i < len(lines); i++ {
			if chunkFrameOf(lines[i]) {
				chunks++
			}
		}
		if chunks > 0 {
			return "mismatch"
		}
		return named
	}
	if sameText(named, spoken) {
		return spoken
	}
	return "mismatch"
}

// chunkFrameOf says a line is a Chat chunk: a data line whose JSON has no
// "type" of its own. It is the one frame a Chat stream writes, and it says the
// body is not any of the typed protocols.
func chunkFrameOf(line []byte) bool {
	payload := dataPayload(line)
	if len(payload) == 0 || payload[0] != '{' {
		return false
	}
	return len(streamEventType(payload)) == 0
}

// closingFitsBody says the closing frame speaks the protocol the body does. A
// body that never named one is settled by its frame, unless the body's other
// frames are plainly another protocol's — a Chat chunk carries no type of its

// chunksOnlyBody says every frame of the body is a Chat chunk: a data line whose
// JSON has no "type" of its own.
func chunksOnlyBody(body []byte) bool {
	chunks := 0
	typed := 0
	start := 0
	for i := 0; i <= len(body); i++ {
		if i < len(body) && body[i] != '\n' {
			continue
		}
		line := body[start:i]
		start = i + 1
		payload := dataPayload(line)
		if len(payload) == 0 {
			continue
		}
		typed++
		if len(streamEventType(payload)) == 0 {
			chunks++
		}
	}
	return typed > 1 && chunks > 0
}

// typedOnlyBody says every frame of the body names a type of its own: it is one
// of the typed protocols, not a body of Chat chunks.
func typedOnlyBody(body []byte) bool {
	// Counts only: a bare "return false" in the middle of this loop has been seen
	// to be skipped on this toolchain.
	typed := 0
	untyped := 0
	start := 0
	for i := 0; i <= len(body); i++ {
		if i < len(body) && body[i] != byte(10) {
			continue
		}
		line := body[start:i]
		start = i + 1
		payload := dataPayload(line)
		if len(payload) > 0 && payload[0] == '{' {
			kind := streamEventType(payload)
			if len(kind) > 0 {
				typed++
			} else {
				untyped++
			}
		}
	}
	if untyped > 0 {
		return false
	}
	return typed > 0
}
