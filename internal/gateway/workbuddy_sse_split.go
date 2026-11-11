package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// One `data:` frame carrying several JSON values.
//
// Upstream occasionally packs two chunks into one data line — `data: {a} {b}` — and a
// client reading by the SSE spec sees a single text with two JSON values in it, which
// no parser accepts: it fails on the second object and the whole turn is lost
// (misakano7545/workbuddy2api-panel issue #197, fixed there in sse_frame_split.go).
//
// For this gateway the shape is worse than a client parse error, because the streaming
// path is a byte pass-through: whatever upstream concatenated goes out concatenated. The
// aggregated path is worse still — buildWorkBuddyCompletion unmarshals each data line
// and **skips what will not parse**, so a concatenated frame is dropped silently and the
// answer loses that text with nothing in the logs.
//
// So the split happens here, in two places: the aggregator splits a frame before parsing
// it, and the stream wrapper rewrites a concatenated line into one `data:` line per
// value so the client never sees the shape at all.
//
// The splitter itself is deliberately conservative. It only cuts when every piece is a
// complete, valid JSON value and the depth returns to zero between them; anything else
// comes back as a single element and the caller keeps its previous behaviour.

const (
	// workBuddyMaxSplitParts bounds how many values one frame may be split into. The
	// real shape is "two", and this only exists so a malformed frame cannot turn into
	// hundreds of writes.
	workBuddyMaxSplitParts = 64
)

// splitWorkBuddyConcatenatedJSON cuts a payload of back-to-back JSON values into
// separate strings.
//
// It scans bytes while tracking string state (including backslash escapes) and brace /
// bracket depth, and cuts when depth returns to zero and the next non-space byte begins
// another value. Every candidate is re-validated as JSON: cutting less — returning the
// payload unchanged — is always preferred over emitting a half object.
//
// A return of length > 1 means the payload really was several values.
func splitWorkBuddyConcatenatedJSON(payload string) []string {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return []string{payload}
	}
	var (
		parts   []string
		depth   int
		inStr   bool
		escaped bool
		start   = -1
	)
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if start < 0 {
			// Space is allowed between values; anything else opens a new one.
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				continue
			}
			if c != '{' && c != '[' && c != '"' && c != '-' && (c < '0' || c > '9') &&
				c != 't' && c != 'f' && c != 'n' {
				return []string{payload} // not a sequence of JSON values
			}
			start = i
		}
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
				if depth == 0 { // a bare string value; cut after the closing quote
					part, ok := workBuddyTrimJSONValue(trimmed[start : i+1])
					if !ok {
						return []string{payload}
					}
					parts = append(parts, part)
					if len(parts) >= workBuddyMaxSplitParts {
						return []string{payload}
					}
					start = -1
				}
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return []string{payload}
			}
			if depth == 0 {
				part, ok := workBuddyTrimJSONValue(trimmed[start : i+1])
				if !ok {
					return []string{payload}
				}
				parts = append(parts, part)
				if len(parts) >= workBuddyMaxSplitParts {
					return []string{payload}
				}
				start = -1
			}
		}
	}
	if start >= 0 || inStr || depth != 0 {
		return []string{payload} // a trailing half value: trust none of it
	}
	if len(parts) == 0 {
		return []string{payload}
	}
	return parts
}

// workBuddyTrimJSONValue validates one candidate as a single JSON value.
func workBuddyTrimJSONValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || !json.Valid([]byte(value)) {
		return "", false
	}
	return value, true
}

// workBuddyLooksLikeJSON reports whether a payload was *meant* to be JSON.
//
// Used only to decide whether a parse failure deserves a warning: some upstream lines are
// legitimately not JSON (a text heartbeat), and those passing through silently is right.
func workBuddyLooksLikeJSON(payload string) bool {
	trimmed := strings.TrimLeft(payload, " \t\r\n")
	if trimmed == "" {
		return false
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}

// splitWorkBuddySSELine rewrites one SSE line so each JSON value gets its own frame.
//
// The line is returned unchanged unless it carries two or more values, so the common
// path costs one Unmarshal-free scan and changes nothing. The trailing newline is
// preserved, and the `data:` prefix (with its optional space) is kept on every value.
func splitWorkBuddySSELine(line []byte) []byte {
	text := strings.TrimRight(string(line), "\r\n")
	suffix := string(line[len(text):])
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return line
	}
	parts := splitWorkBuddyConcatenatedJSON(payload)
	if len(parts) < 2 {
		return line
	}
	var out bytes.Buffer
	for _, part := range parts {
		out.WriteString("data: ")
		out.WriteString(part)
		out.WriteString(suffix)
		if suffix == "" {
			out.WriteString("\n")
		}
	}
	return out.Bytes()
}

// workBuddyFrameSplittingBody rewrites a stream's frames on the way out, sharing the
// line-based shape of toolNameRestoringBody.
//
// A frame that cannot be split passes through untouched: the wrapper never drops or
// buffers more than one line.
type workBuddyFrameSplittingBody struct {
	src    io.ReadCloser
	reader *bufio.Reader
	out    bytes.Buffer
	done   bool
}

// wrapWorkBuddyFrameSplitting makes a response's stream emit one JSON value per frame.
func wrapWorkBuddyFrameSplitting(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	response.Body = &workBuddyFrameSplittingBody{
		src:    response.Body,
		reader: bufio.NewReader(response.Body),
	}
}

func (b *workBuddyFrameSplittingBody) Read(p []byte) (int, error) {
	for b.out.Len() == 0 {
		if b.done {
			return 0, io.EOF
		}
		line, err := b.reader.ReadBytes('\n')
		if len(line) > 0 {
			b.out.Write(splitWorkBuddySSELine(line))
		}
		if err != nil {
			b.done = true
			if b.out.Len() == 0 && err != io.EOF {
				return 0, err
			}
		}
	}
	return b.out.Read(p)
}

func (b *workBuddyFrameSplittingBody) Close() error { return b.src.Close() }
