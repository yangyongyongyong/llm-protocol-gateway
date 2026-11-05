package gateway

import (
	"bytes"
	"net/http"
	"strings"
)

// The order the desktop client's request headers go out in.
//
// Go's net/http writes headers in map order (Header.writeSubset sorts the keys),
// while the client is a Node app that writes them in the order its code sets
// them. The set of headers and their spelling can match perfectly and the
// request still reads as a different family byte for byte, which is why the
// spelling map in workbuddy_chat_headers.go is only half of the job — this file
// is the other half.
//
// The tables are the reference gateway's captured order, recorded per path from
// real-client fixtures (misakano7545/workbuddy2api-panel: internal/upstream/
// headorder.go, from testdata/official_chat_head.txt — 43 headers, CN 5.9.3 /
// intl 5.9.4):
//
//	/v2/chat/completions  the chat chain
//	/v2/report            the growth report chain
//	/v2/chat/busy         the model-load probe
//	/v3/config            the model catalog
//
// Per-path rather than one global order, because the client builds these header
// objects in different code paths and their orders genuinely differ; applying
// the chat order to /v2/report would be a worse lie than leaving it alone.
// Paths with no captured order keep Go's native order — no guessing.
//
// The trailing Host / Connection / Content-Length / Accept-Encoding entries are
// appended by net/http itself rather than set through Header, so they are listed
// too: the reorder runs on the finished head block and has to place them.

// workBuddyHeadOrder is each chain's official header order, spelled as it goes
// out on the wire.
var workBuddyHeadOrder = map[string][]string{
	"/v2/chat/completions": {
		"Accept",
		"Content-Type",
		"X-Requested-With",
		"x-stainless-arch",
		"x-stainless-lang",
		"x-stainless-os",
		"x-stainless-package-version",
		"x-stainless-retry-count",
		"x-stainless-runtime",
		"x-stainless-runtime-version",
		"X-Conversation-ID",
		"X-Agent-Purpose",
		"X-IDE-Type",
		"X-IDE-Name",
		"X-IDE-Version",
		"X-Agent-Intent",
		"X-Agent-Runtime",
		"X-Private-Data",
		"acp-connection-id",
		"x-codebuddy-request",
		"X-Request-ID",
		"X-Conversation-Message-ID",
		"X-Conversation-Request-ID",
		"X-Root-Request-ID",
		"X-Agent-Type",
		"baggage",
		"Content-Encoding",
		"traceparent",
		"b3",
		"X-B3-TraceId",
		"X-B3-ParentSpanId",
		"X-B3-SpanId",
		"X-B3-Sampled",
		"X-Trace-ID",
		"X-Product",
		"User-Agent",
		"Authorization",
		"X-User-Id",
		"X-Domain",
		"Content-Length",
		"Accept-Encoding",
		"Host",
		"Connection",
	},
	"/v2/report": {
		"Accept",
		"Content-Type",
		"X-Request-ID",
		"X-Product",
		"User-Agent",
		"Authorization",
		"X-User-Id",
		"X-Domain",
		"Content-Length",
		"Accept-Encoding",
		"Host",
		"Connection",
	},
	"/v2/chat/busy": {
		"Accept",
		"Content-Type",
		"User-Agent",
		"X-Request-ID",
		"X-Product",
		"Authorization",
		"X-User-Id",
		"X-Domain",
		"Content-Length",
		"Host",
		"Connection",
	},
	"/v3/config": {
		"Accept",
		"Connection",
		"User-Agent",
		"X-Request-ID",
		"X-Product",
		"Host",
	},
}

// workBuddyHeadRank compresses one path's order into lower-case name → rank.
//
// Case-insensitive on purpose: the transport writes the spellings from
// officialWorkBuddyHeaderSpelling, but the few headers net/http emits itself
// (Host, Content-Length) come out canonical.
func workBuddyHeadRank(path string) (map[string]int, bool) {
	order, ok := workBuddyHeadOrder[path]
	if !ok {
		return nil, false
	}
	rank := make(map[string]int, len(order))
	for index, name := range order {
		rank[strings.ToLower(name)] = index
	}
	return rank, true
}

// workBuddyRequestPathOf reads the path out of a request line, dropping the
// query, so the order table can be looked up.
func workBuddyRequestPathOf(line string) string {
	space := strings.IndexByte(line, ' ')
	if space <= 0 {
		return ""
	}
	rest := line[space+1:]
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		rest = rest[:end]
	}
	if query := strings.IndexByte(rest, '?'); query >= 0 {
		rest = rest[:query]
	}
	// absolute-form (http://host/path) keeps only the path.
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(rest, scheme) {
			if slash := strings.IndexByte(rest[len(scheme):], '/'); slash >= 0 {
				rest = rest[len(scheme)+slash:]
			}
		}
	}
	return rest
}

// reorderWorkBuddyHeadBlock reorders one complete request head block (its final
// blank line included) into the official order.
//
// Behaviour:
//   - headers in the table are written in table order;
//   - headers not in the table (one the upstream adds later, a temporary
//     injection) keep their original relative order and go last;
//   - a repeated header name keeps its own occurrences in order;
//   - an unrecognised request line or an empty table returns the block
//     unchanged — this never errors, because a failure here must degrade to
//     "not reordered", never to a changed data stream.
//
// Pure function with no connection state, so it is the one piece of this
// mechanism that has to be exactly right.
func reorderWorkBuddyHeadBlock(block []byte) []byte {
	if len(block) == 0 || !bytes.HasSuffix(block, []byte("\r\n\r\n")) {
		return block
	}
	body := block[:len(block)-4] // the terminating blank line
	lines := strings.Split(string(body), "\r\n")
	if len(lines) < 2 {
		return block
	}
	rank, ok := workBuddyHeadRank(workBuddyRequestPathOf(lines[0]))
	if !ok {
		return block
	}

	type entry struct {
		name string
		raw  string
	}
	entries := make([]entry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		entries = append(entries, entry{name: line[:colon], raw: line})
	}

	ordered := make([]entry, 0, len(entries))
	used := make([]bool, len(entries))
	for want := 0; want < len(rank); want++ {
		for index, item := range entries {
			if used[index] {
				continue
			}
			if got, known := rank[strings.ToLower(item.name)]; known && got == want {
				ordered = append(ordered, item)
				used[index] = true
			}
		}
	}
	for index, item := range entries {
		if !used[index] {
			ordered = append(ordered, item)
		}
	}

	var out bytes.Buffer
	out.Grow(len(block))
	out.WriteString(lines[0])
	out.WriteString("\r\n")
	for _, item := range ordered {
		out.WriteString(item.raw)
		out.WriteString("\r\n")
	}
	out.WriteString("\r\n")
	return out.Bytes()
}

// workBuddyConnectionValue is the Connection value the client sends on a path.
//
// Both builds were captured writing Connection explicitly on every RPC:
// /v3/config says close, the rest say keep-alive. HTTP/1.1 reuse is the default,
// so Go never emits this header at all — but a header set that is missing one
// the client always sends reads as just as different a family as one carrying an
// extra. Paths that were never captured return "", which leaves Go's default
// alone rather than inventing a value.
func workBuddyConnectionValue(path string) string {
	switch path {
	case "/v3/config":
		return "close"
	case "/v2/chat/completions", "/v2/chat/busy", "/v2/report":
		return "keep-alive"
	}
	return ""
}

// applyWorkBuddyConnectionHeader puts the client's explicit Connection back.
//
// "close" never goes into Header: net/http's transferWriter writes one
// Connection: close line of its own when req.Close is set, and a hand-written
// one would duplicate it. "keep-alive" is the opposite — Go treats it as the
// default and never writes it, so it has to be set by hand to appear on the
// wire at all.
func applyWorkBuddyConnectionHeader(request *http.Request) {
	if request == nil || request.URL == nil {
		return
	}
	switch workBuddyConnectionValue(request.URL.Path) {
	case "close":
		request.Close = true
	case "keep-alive":
		request.Header.Set("Connection", "keep-alive")
	}
}
