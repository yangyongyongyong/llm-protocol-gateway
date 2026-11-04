package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// thinkingOnlyStream is a Claude turn that only thought: exactly what the OAuth
// upstream was seen to close with stop_reason=end_turn and no text.
const thinkingOnlyStream = `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"claude-opus-5-5"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`

// answeredStream is the same turn with something a client can read in it.
const answeredStream = `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"claude-opus-5-5"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

func guardInterrupter() claudeStreamInterrupter {
	return claudeStreamInterrupter{onEmptyTurn: func() {}}
}

// A turn of nothing but thinking must not reach the client as a finished reply:
// the closing frame becomes an error, which the client answers by asking again.
func TestClaudeEmptyTurnGuardRewritesThinkingOnlyStop(t *testing.T) {
	rec := httptest.NewRecorder()
	guard := &claudeEmptyTurnGuard{dst: rec}
	if _, err := guard.Write([]byte(thinkingOnlyStream)); err != nil {
		t.Fatalf("write: %v", err)
	}
	guard.close()

	body := rec.Body.String()
	if strings.Contains(body, "message_stop") {
		t.Fatalf("expected the closing frame replaced, got %q", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected an error frame, got %q", body)
	}
	if !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("expected api_error, got %q", body)
	}
}

// A turn that says something keeps its own closing frame: the answer must not be
// turned into an error.
func TestClaudeEmptyTurnGuardKeepsAnsweredStop(t *testing.T) {
	rec := httptest.NewRecorder()
	guard := &claudeEmptyTurnGuard{dst: rec}
	if _, err := guard.Write([]byte(answeredStream)); err != nil {
		t.Fatalf("write: %v", err)
	}
	guard.close()

	body := rec.Body.String()
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("expected the closing frame kept, got %q", body)
	}
	if strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("an answered turn must not be rewritten, got %q", body)
	}
}

// A tool call is visible output too: a turn that only calls a tool is not empty.
func TestClaudeEmptyTurnGuardCountsToolUseAsVisible(t *testing.T) {
	stream := strings.Replace(answeredStream, `{"type":"text","text":""}`, `{"type":"tool_use","id":"t1","name":"Bash","input":{}}`, 1)
	rec := httptest.NewRecorder()
	guard := &claudeEmptyTurnGuard{dst: rec}
	if _, err := guard.Write([]byte(stream)); err != nil {
		t.Fatalf("write: %v", err)
	}
	guard.close()
	if body := rec.Body.String(); !strings.Contains(body, "message_stop") {
		t.Fatalf("a tool call is visible output, the stop must stay: %q", body)
	}
}

// Frames arrive in whatever pieces the upstream's socket delivers: the guard must
// see the same turn either way.
func TestClaudeEmptyTurnGuardHandlesSplitWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	guard := &claudeEmptyTurnGuard{dst: rec}
	for i := 0; i < len(thinkingOnlyStream); i += 7 {
		end := min(i+7, len(thinkingOnlyStream))
		if _, err := guard.Write([]byte(thinkingOnlyStream[i:end])); err != nil {
			t.Fatalf("write at %d: %v", i, err)
		}
	}
	guard.close()

	body := rec.Body.String()
	if strings.Contains(body, "message_stop") {
		t.Fatalf("expected the closing frame replaced across split writes, got %q", body)
	}
	if !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("expected an error frame across split writes, got %q", body)
	}
}

// Only the interrupter that reports empty Claude turns arms the guard; a relay's
// stream is passed through untouched.
func TestClaudeEmptyTurnGuardOnlyForReportingInterrupter(t *testing.T) {
	rec := httptest.NewRecorder()
	if out, guard := claudeEmptyTurnGuardFor(rec, nil); guard != nil || out != rec {
		t.Fatal("expected no guard without an interrupter")
	}
	if _, guard := claudeEmptyTurnGuardFor(rec, []streamInterrupter{claudeStreamInterrupter{}}); guard != nil {
		t.Fatal("expected no guard for a Claude relay (no empty-turn reporter)")
	}
	if _, guard := claudeEmptyTurnGuardFor(rec, []streamInterrupter{guardInterrupter()}); guard == nil {
		t.Fatal("expected a guard for the OAuth interrupter")
	}
}

// lockedRecorder makes the recorder safe for the two goroutines this test runs:
// the guard's own lock is what is under test, so the sink must not be the thing
// that races.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (r *lockedRecorder) Header() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Header()
}

func (r *lockedRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *lockedRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(statusCode)
}

func (r *lockedRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Flush()
}

// The copy loop writes the stream while the heartbeat goroutine flushes it: the
// guard sits in front of both, so it is written and flushed from two goroutines.
// Run with -race, this pins that it holds one lock over the frame buffer.
func TestClaudeEmptyTurnGuardConcurrentWriteAndFlush(t *testing.T) {
	sink := &lockedRecorder{rec: httptest.NewRecorder()}
	guard := &claudeEmptyTurnGuard{dst: sink}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				guard.Flush()
			}
		}
	}()
	for i := 0; i < 64; i++ {
		if _, err := guard.Write([]byte(thinkingOnlyStream)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	guard.close()
}
