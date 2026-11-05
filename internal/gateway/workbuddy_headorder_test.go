package gateway

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A captured chat head, in the order net/http would write it (alphabetical by
// key), as the input side of the reorder.
const workBuddyTestUnsortedHead = "POST /v2/chat/completions HTTP/1.1\r\n" +
	"Accept: application/json\r\n" +
	"Authorization: Bearer tok\r\n" +
	"Content-Length: 2\r\n" +
	"Content-Type: application/json\r\n" +
	"Host: www.workbuddy.cn\r\n" +
	"User-Agent: WorkBuddy/5.9.3\r\n" +
	"X-Conversation-ID: 01a1815d-dacf-75f1-9d2a-4a4ea3d1c9f0\r\n" +
	"X-Domain: www.workbuddy.cn\r\n" +
	"X-Product: SaaS\r\n" +
	"X-Request-ID: 0123456789abcdef0123456789abcdef\r\n" +
	"X-User-Id: uid-1\r\n" +
	"x-stainless-lang: js\r\n" +
	"acp-connection-id: 01a1815d-dacf-75f1-8a11-1b2c3d4e5f60\r\n" +
	"baggage: buddyId=,buddyName=,codebuddy.session_id=c,codebuddy.conversation_request_id=r\r\n" +
	"\r\n"

// The reorder has to move the client's own headers into its order while leaving
// everything it does not know about at the end, untouched.
func TestReorderWorkBuddyHeadBlockPutsTheClientOrderOnTheWire(t *testing.T) {
	got := string(reorderWorkBuddyHeadBlock([]byte(workBuddyTestUnsortedHead)))
	if !strings.HasSuffix(got, "\r\n\r\n") {
		t.Fatalf("head block lost its terminator: %q", got)
	}
	if !strings.HasPrefix(got, "POST /v2/chat/completions HTTP/1.1\r\n") {
		t.Fatalf("request line moved: %q", got)
	}

	order := headOrderOf(t, got)
	want := []string{
		"Accept",
		"Content-Type",
		"x-stainless-lang",
		"X-Conversation-ID",
		"acp-connection-id",
		"X-Request-ID",
		"baggage",
		"X-Product",
		"User-Agent",
		"Authorization",
		"X-User-Id",
		"X-Domain",
		"Content-Length",
		"Host",
	}
	position := 0
	for _, name := range want {
		found := false
		for ; position < len(order); position++ {
			if order[position] == name {
				found = true
				position++
				break
			}
		}
		if !found {
			t.Fatalf("%s is out of order: %v", name, order)
		}
	}
}

// A header the table does not know survives the reorder: guessing at an unknown
// header's place would be worse than leaving it where it was.
func TestReorderWorkBuddyHeadBlockKeepsUnknownHeaders(t *testing.T) {
	head := "POST /v2/chat/completions HTTP/1.1\r\n" +
		"Accept: application/json\r\n" +
		"X-Something-New: 1\r\n" +
		"X-Product: SaaS\r\n" +
		"\r\n"
	got := string(reorderWorkBuddyHeadBlock([]byte(head)))
	if !strings.Contains(got, "X-Something-New: 1") {
		t.Fatalf("unknown header was dropped: %q", got)
	}
	order := headOrderOf(t, got)
	if order[len(order)-1] != "X-Something-New" {
		t.Fatalf("unknown header should be last, order = %v", order)
	}
}

// A path with no captured order keeps Go's own order — the tables are evidence,
// not a guess.
func TestReorderWorkBuddyHeadBlockLeavesUncapturedPathsAlone(t *testing.T) {
	head := "POST /v2/something/else HTTP/1.1\r\nAccept: */*\r\nX-Product: SaaS\r\n\r\n"
	if got := reorderWorkBuddyHeadBlock([]byte(head)); string(got) != head {
		t.Fatalf("uncaptured path was reordered: %q", string(got))
	}
}

// A malformed or headless block must pass through untouched: the safe failure is
// "not reordered", never a changed stream.
func TestReorderWorkBuddyHeadBlockPassesThroughGarbage(t *testing.T) {
	for _, block := range []string{"", "not a request\r\n\r\n", "POST /v2/chat/completions HTTP/1.1\r\nA: b"} {
		if got := reorderWorkBuddyHeadBlock([]byte(block)); string(got) != block {
			t.Fatalf("garbage was rewritten: %q -> %q", block, string(got))
		}
	}
}

// The client writes Connection explicitly on the chains it was captured on.
func TestWorkBuddyConnectionValueFollowsTheCaptures(t *testing.T) {
	cases := map[string]string{
		"/v2/chat/completions":      "keep-alive",
		"/v2/report":                "keep-alive",
		"/v2/chat/busy":             "keep-alive",
		"/v3/config":                "close",
		"/v2/activity/growth/tasks": "",
	}
	for path, want := range cases {
		if got := workBuddyConnectionValue(path); got != want {
			t.Fatalf("%s: Connection = %q, want %q", path, got, want)
		}
	}
}

// "close" has to go through req.Close, because net/http writes its own
// Connection line then; writing the header by hand duplicates it.
func TestApplyWorkBuddyConnectionHeaderSplitsCloseFromKeepAlive(t *testing.T) {
	closeRequest := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v3/config", nil)
	applyWorkBuddyConnectionHeader(closeRequest)
	if !closeRequest.Close {
		t.Fatal("close path did not set req.Close")
	}
	if got := closeRequest.Header.Get("Connection"); got != "" {
		t.Fatalf("close path wrote the header by hand: %q", got)
	}
	keepRequest := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/report", nil)
	applyWorkBuddyConnectionHeader(keepRequest)
	if got := keepRequest.Header.Get("Connection"); got != "keep-alive" {
		t.Fatalf("keep-alive path did not set the header: %q", got)
	}
}

// The connection wrapper has to reorder the head it sees and then let every
// following byte through unchanged, including a body that arrives in a later
// write.
func TestWorkBuddyHeadOrderConnReordersOnceThenPassesBodiesThrough(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	wrapped := newWorkBuddyHeadOrderConn(client)
	received := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 4096)
		var collected []byte
		for len(collected) < len(workBuddyTestUnsortedHead)+2 {
			n, err := server.Read(buffer)
			if n > 0 {
				collected = append(collected, buffer[:n]...)
			}
			if err != nil {
				break
			}
		}
		received <- collected
	}()

	if _, err := wrapped.Write([]byte(workBuddyTestUnsortedHead)); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if _, err := wrapped.Write([]byte("{}")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	select {
	case got := <-received:
		text := string(got)
		if !strings.Contains(text, "\r\n\r\n{}") {
			t.Fatalf("body was not appended after the head: %q", text)
		}
		if !strings.HasPrefix(text, "POST /v2/chat/completions") {
			t.Fatalf("head lost its request line: %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no bytes reached the far side")
	}
}

// A head split across two writes is still reordered: the wrapper buffers until
// the blank line arrives.
func TestWorkBuddyHeadOrderConnBuffersASplitHead(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	wrapped := newWorkBuddyHeadOrderConn(client)
	head := workBuddyTestUnsortedHead
	cut := bytes.Index([]byte(head), []byte("X-Product"))
	if cut <= 0 {
		t.Fatal("test head does not contain the split marker")
	}
	received := make(chan string, 1)
	go func() {
		buffer := make([]byte, 8192)
		var collected []byte
		for len(collected) < len(head) {
			n, err := server.Read(buffer)
			if n > 0 {
				collected = append(collected, buffer[:n]...)
			}
			if err != nil {
				break
			}
		}
		received <- string(collected)
	}()

	if _, err := wrapped.Write([]byte(head[:cut])); err != nil {
		t.Fatalf("write first half: %v", err)
	}
	if _, err := wrapped.Write([]byte(head[cut:])); err != nil {
		t.Fatalf("write second half: %v", err)
	}
	select {
	case got := <-received:
		order := headOrderOf(t, got)
		productAt, conversationAt := -1, -1
		for index, name := range order {
			switch name {
			case "X-Product":
				productAt = index
			case "X-Conversation-ID":
				conversationAt = index
			}
		}
		if productAt < 0 || conversationAt < 0 {
			t.Fatalf("split head lost headers: %v", order)
		}
		if productAt <= conversationAt {
			t.Fatalf("split head was not reordered: %v", order)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no bytes reached the far side")
	}
}

// The response tracker decides when a pooled connection may be reordered again.
// Content-Length, chunked, and "cannot tell" all have to behave.
func TestWorkBuddyResponseTrackerFramesResponses(t *testing.T) {
	contentLength := &workBuddyResponseTracker{}
	if contentLength.feed([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")) != true {
		t.Fatal("Content-Length response was not seen as complete")
	}

	chunked := &workBuddyResponseTracker{}
	if chunked.feed([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\n\r\n")) != true {
		t.Fatal("chunked response was not seen as complete")
	}

	open := &workBuddyResponseTracker{}
	if open.feed([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nab")) {
		t.Fatal("partial body reported complete")
	}

	unbounded := &workBuddyResponseTracker{}
	if unbounded.feed([]byte("HTTP/1.1 200 OK\r\n\r\nanything")) {
		t.Fatal("a close-delimited response was reported complete")
	}
	if !unbounded.unknown {
		t.Fatal("a close-delimited response should be marked unknown")
	}
}

// headOrderOf reads the header names out of a head block, in wire order.
func headOrderOf(t *testing.T, head string) []string {
	t.Helper()
	lines := strings.Split(head, "\r\n")
	var names []string
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			t.Fatalf("malformed header line: %q", line)
		}
		names = append(names, line[:colon])
	}
	return names
}
