package gateway

// Reading the tool calls a model writes as text.
//
// Qoder's endpoint sometimes answers with a call written as prose instead of as
// a structured tool_call. Three shapes reach us:
//
//   - this gateway's own <tool_call>{json}</tool_call> marker,
//   - the marker Qoder's own client injects into a conversation's history,
//     "[assistant 请求调用工具]" followed by a JSON array of {name, arguments} —
//     in a long session the model occasionally repeats it verbatim as the
//     answer, and then the client shows a tool call as text and never runs it,
//   - DeepSeek's native DSML markup.
//
// Every shape is read strictly: a block only becomes a call when all of it
// parses and every name in it is a tool the request declared. A block that
// merely looks like one — prose about tool calls, a fenced example — stays
// text, because guessing here would either swallow an answer or run a call the
// model never meant.

import (
	"encoding/json"
	"regexp"
	"strings"
)

// qoderEchoMarkerRe matches every bracket spelling of the client's marker:
// [[assistant …]], [<assistant …], a missing closing bracket, or parentheses.
var qoderEchoMarkerRe = regexp.MustCompile(`[\[<(]*\s*assistant\s*请求调用工具\s*[\]>)]?`)

// qoderEchoMarkerCore is the part every spelling shares.
const qoderEchoMarkerCore = "assistant请求调用工具"

// qoderEchoMarker is the marker the vendor client writes into a conversation's
// history, and what a text-envelope history carries. qoderToolResultOpen is the
// opening of the envelope a tool's output travels in.
const (
	qoderEchoMarkerText = "[assistant 请求调用工具]"
	qoderToolResultOpen = "[工具结果"
)

// qoderMarkerOpensJSON reports whether a marker is followed by JSON rather than
// by prose. It is how a cut-off echo is told from an answer that merely talks
// about the marker: only one of them may be dropped.
func qoderMarkerOpensJSON(block string) bool {
	span := qoderEchoMarkerRe.FindStringIndex(block)
	if span == nil {
		return false
	}
	for index := span[1]; index < len(block); index++ {
		switch block[index] {
		case ' ', '\t', '\r', '\n', ']', '>', '=', ':', ')', ',':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	// Nothing at all follows the marker: a bare marker is an echo too.
	return true
}

// The DSML markup DeepSeek writes, matched the way the vendor client reads it:
// an opening tag that names DSML anywhere before the attribute list, then the
// closing tag of the same element.
var (
	qoderDSMLStart  = regexp.MustCompile(`(?s)<[^>]*DSML[^>]*invoke\s+name=`)
	qoderDSMLInvoke = regexp.MustCompile(`(?s)<[^>]*DSML[^>]*invoke\s+name=["']([^"']+)["'][^>]*>(.*?)</[^>]*invoke>`)
	qoderDSMLParam  = regexp.MustCompile(`(?s)<[^>]*DSML[^>]*parameter\s+name=["']([^"']+)["'][^>]*>(.*?)</[^>]*parameter>`)
)

// qoderEchoLooseRe reads a single-call array whose inner quotes were never
// escaped — `"arguments": "{"path":"a.go"}"` — which is not valid JSON at all.
var qoderEchoLooseRe = regexp.MustCompile(`(?s)^\[\s*\{\s*"name"\s*:\s*"([^"]{1,200})"\s*,\s*"arguments"\s*:\s*"(.*)"\s*\}\s*\]$`)

const (
	// qoderEchoHoldMaxChars and qoderEchoHoldMaxFrames bound how much a block
	// that never closes may hold back. Past either limit the text is sent on:
	// an unterminated block must not cost the client its whole answer.
	qoderEchoHoldMaxChars  = 32768
	qoderEchoHoldMaxFrames = 200
	// qoderMarkerHoldMax bounds how much of a frame is held back while it might
	// still grow into the marker, and qoderMarkerHoldWords how many words of it
	// have to have arrived before a bare (unbracketed) tail counts as one.
	qoderMarkerHoldMax   = 32
	qoderMarkerHoldMin   = 2
	qoderMarkerHoldWords = 8
)

// qoderEchoKind is which shape the text is carrying.
type qoderEchoKind int

const (
	qoderEchoNone qoderEchoKind = iota
	qoderEchoTag
	qoderEchoMarker
	qoderEchoDSML
)

// qoderEchoStart is the earliest position in text where a call block could
// begin, and which shape it is. -1 means there is none.
func qoderEchoStart(text string) (qoderEchoKind, int) {
	kind, at := qoderEchoNone, -1
	consider := func(candidate qoderEchoKind, index int) {
		if index < 0 {
			return
		}
		if at < 0 || index < at {
			kind, at = candidate, index
		}
	}
	consider(qoderEchoTag, strings.Index(text, qoderCallOpen))
	if span := qoderEchoMarkerRe.FindStringIndex(text); span != nil {
		consider(qoderEchoMarker, span[0])
	}
	if span := qoderDSMLStart.FindStringIndex(text); span != nil {
		consider(qoderEchoDSML, span[0])
	}
	return kind, at
}

// qoderEchoHoldTail is the length of the tail that could still turn into one of
// the markers: a frame ending mid-tag must not leak the partial tag as text.
func qoderEchoHoldTail(text string) int {
	keep := 0
	consider := func(size int) {
		if size > keep {
			keep = size
		}
	}
	for size := len(qoderCallOpen) - 1; size > 0; size-- {
		if strings.HasSuffix(text, qoderCallOpen[:size]) {
			consider(size)
			break
		}
	}
	for _, opener := range []string{"<|D", "<|DS", "<|DSM", "<|DSML", "<|DSML|"} {
		if strings.HasSuffix(text, opener) {
			consider(len(opener))
		}
	}
	// The marker's spellings differ in brackets and in spacing, so its tail is
	// matched on the words alone.
	limit := len(text)
	if limit > qoderMarkerHoldMax {
		limit = qoderMarkerHoldMax
	}
	for size := limit; size >= qoderMarkerHoldMin; size-- {
		if qoderMarkerTailPrefix(text[len(text)-size:]) {
			consider(size)
			break
		}
	}
	return keep
}

// qoderMarkerTailPrefix reports whether a tail could still grow into the marker.
// Its brackets and spaces are set aside and what remains has to be a prefix of
// the marker's words — and it has to have begun like a marker, not like any word
// that happens to start with the same letters.
func qoderMarkerTailPrefix(tail string) bool {
	trimmed := strings.TrimLeft(tail, "[<("+" \t\r\n")
	bracketed := len(trimmed) != len(tail)
	words := strings.Join(strings.Fields(trimmed), "")
	if words == "" || !strings.HasPrefix(qoderEchoMarkerCore, words) {
		return false
	}
	return bracketed || len([]rune(words)) >= qoderMarkerHoldWords
}

// qoderEchoJSONSpan is where a JSON value starts after a marker, and where it
// ends. A marker is sometimes followed by punctuation the model added
// (`]`, `>`, `:`) before the array, and the array sometimes lost its opening
// bracket — both are read.
func qoderEchoJSONSpan(text string) (start int, end int, ok bool) {
	opener := -1
	for index := 0; index < len(text); index++ {
		switch c := text[index]; c {
		case '[', '{':
			opener = index
		case ' ', '\t', '\r', '\n', ']', '>', '=', ':', ')', ',', '"', '\'':
			continue
		default:
			// Real prose before any JSON: this is not a call.
			if opener < 0 {
				return 0, 0, false
			}
		}
		if opener >= 0 {
			break
		}
	}
	if opener < 0 {
		return 0, 0, false
	}
	end, complete := qoderBalancedEnd(text[opener:])
	if !complete {
		return opener, 0, false
	}
	return opener, opener + end, true
}

// qoderBalancedEnd is the index just past the balanced JSON value text starts
// with, and whether it closed. It walks bytes and understands strings and the
// escapes inside them, so a brace in an argument value does not end the value.
func qoderBalancedEnd(text string) (int, bool) {
	depth := 0
	inString, escaped := false, false
	for index := 0; index < len(text); index++ {
		c := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return index + 1, true
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}

// qoderCopyJSONQuotes escapes the bare quotes inside a value the model wrote
// without escaping them, so the outer document can be parsed.
func qoderCopyJSONQuotes(text string) string {
	const placeholder = "\x00"
	repaired := strings.ReplaceAll(text, `\"`, placeholder)
	repaired = strings.ReplaceAll(repaired, `"`, `\"`)
	return strings.ReplaceAll(repaired, placeholder, `\"`)
}

// qoderEchoCall is one call lifted out of the text.
type qoderEchoCall = qoderLiftedCall

// qoderEchoVerdict is what one candidate block turned out to be.
type qoderEchoVerdict struct {
	calls []qoderEchoCall
	// declared says every call named a tool this request declared. When false
	// the block is prose about tools and has to be sent on as text.
	declared bool
	// unreadable marks a block whose outer array parsed but whose arguments did
	// not: the turn is a protocol failure rather than a silent swallow, because
	// the model meant to call something and the arguments are unusable.
	unreadable bool
}

// qoderParseEchoArray reads the marker's payload: a JSON array of
// {name, arguments}. A payload whose inner quotes were never escaped is read
// through a repair pass, which is what the vendor client does with it too.
func qoderParseEchoArray(payload string, allowed map[string]bool) (qoderEchoVerdict, bool) {
	trimmed := strings.TrimSpace(payload)
	if match := qoderEchoLooseRe.FindStringSubmatch(trimmed); len(match) == 3 {
		name := strings.TrimSpace(match[1])
		raw := strings.TrimSpace(match[2])
		// The captured arguments are themselves JSON; they only need the repair
		// pass when the quotes inside them were never escaped. Taking the
		// repaired copy first would escape quotes that were already fine.
		for _, candidate := range []string{raw, qoderCopyJSONQuotes(raw)} {
			if !json.Valid([]byte(candidate)) {
				continue
			}
			verdict := qoderEchoVerdict{declared: true}
			verdict.calls = append(verdict.calls, qoderEchoCall{name: name, arguments: candidate})
			if len(allowed) > 0 && !allowed[name] {
				verdict.declared = false
			}
			return verdict, true
		}
	}
	var entries []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(trimmed), &entries) != nil || len(entries) == 0 {
		return qoderEchoVerdict{}, false
	}
	verdict := qoderEchoVerdict{declared: true}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return qoderEchoVerdict{}, false
		}
		arguments, ok := qoderEchoArguments(entry.Arguments)
		if !ok {
			verdict.unreadable = true
			return verdict, true
		}
		verdict.calls = append(verdict.calls, qoderEchoCall{name: name, arguments: arguments})
		if len(allowed) > 0 && !allowed[name] {
			verdict.declared = false
		}
	}
	return verdict, true
}

// qoderEchoArguments is one entry's arguments as a JSON string. The marker's own
// shape is a string holding JSON; an object is taken as it stands.
func qoderEchoArguments(raw json.RawMessage) (string, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return "{}", true
	}
	if strings.HasPrefix(text, `"`) {
		var inner string
		if json.Unmarshal(raw, &inner) != nil {
			return "", false
		}
		inner = strings.TrimSpace(inner)
		if inner == "" {
			return "{}", true
		}
		if !json.Valid([]byte(inner)) {
			return "", false
		}
		return inner, true
	}
	if !json.Valid([]byte(text)) {
		return "", false
	}
	return text, true
}

// qoderParseEchoDSML reads DeepSeek's markup: invoke blocks, each naming a tool
// and holding parameter elements whose text becomes the arguments.
func qoderParseEchoDSML(block string) (qoderEchoVerdict, bool) {
	invokes := qoderDSMLInvoke.FindAllStringSubmatch(block, -1)
	if len(invokes) == 0 {
		return qoderEchoVerdict{}, false
	}
	verdict := qoderEchoVerdict{declared: true}
	for _, invoke := range invokes {
		name := strings.TrimSpace(invoke[1])
		if name == "" {
			return qoderEchoVerdict{}, false
		}
		arguments := map[string]any{}
		for _, parameter := range qoderDSMLParam.FindAllStringSubmatch(invoke[2], -1) {
			key := strings.TrimSpace(parameter[1])
			if key == "" {
				continue
			}
			arguments[key] = strings.TrimSpace(parameter[2])
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return qoderEchoVerdict{}, false
		}
		verdict.calls = append(verdict.calls, qoderEchoCall{name: name, arguments: string(encoded)})
	}
	return verdict, true
}

// qoderDeclaredToolNames is the set of tools a request offered, read off its
// OpenAI body. It is what tells a call apart from prose about calls: a block
// naming anything else never becomes one.
func qoderDeclaredToolNames(body []byte) map[string]bool {
	var request struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if len(body) == 0 || json.Unmarshal(body, &request) != nil {
		return nil
	}
	if len(request.Tools) == 0 {
		return nil
	}
	names := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		if name := strings.TrimSpace(tool.Function.Name); name != "" {
			names[name] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}
