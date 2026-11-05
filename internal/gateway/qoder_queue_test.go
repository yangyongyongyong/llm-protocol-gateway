package gateway

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// qoderQuietSleep removes the real waiting from a retry test: the waits are the
// behaviour, not the thing under test.
func qoderQuietSleep(t *testing.T) {
	t.Helper()
	previous := qoderSleep
	qoderSleep = func(time.Duration) {}
	t.Cleanup(func() { qoderSleep = previous })
}

// qoderQueuedFrame is one envelope naming the model as busy.
func qoderQueuedFrame(t *testing.T, wait int) string {
	t.Helper()
	payload := `{"code":10605,"message":"busy","isQueued":true,"retryAfterSeconds":` + strconv.Itoa(wait) + `}`
	return qoderFrame(t, map[string]any{
		"statusCodeValue": 403,
		"code":            json.Number("10605"),
		"body":            payload,
	})
}

// A queued model is the model, not the account: the turn waits where it is and
// tells the client so, instead of being handed to another account that would
// queue just the same.
func TestQoderWaitsOutAQueueAndThenAnswers(t *testing.T) {
	qoderQuietSleep(t)
	success := qoderChunkFrame(t, map[string]any{"content": "hello"}, "stop", nil)
	reader := newQoderChunkReaderWithTools(strings.NewReader(qoderQueuedFrame(t, 5)), "ultimate", nil)
	reopened := 0
	reader.reopen = func(context.Context) (io.Reader, error) {
		reopened++
		return strings.NewReader(success), nil
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	stream := string(out)
	if reopened != 1 {
		t.Fatalf("the queued call was reopened %d times, want 1", reopened)
	}
	if !strings.Contains(stream, "qoder_queue") {
		t.Fatalf("the client was not told it was queued: %s", stream)
	}
	if !strings.Contains(stream, "hello") {
		t.Fatalf("the answer after the queue never arrived: %s", stream)
	}
	if strings.Contains(stream, `"error"`) {
		t.Fatalf("a queue that cleared must not be reported as a failure: %s", stream)
	}
	if !strings.Contains(stream, `"finish_reason":"stop"`) {
		t.Fatalf("the turn did not close normally: %s", stream)
	}
}

// The budget is finite: a model that stays busy for every round ends as a
// refusal the client can act on, not as an endless wait.
func TestQoderGivesUpAfterTheQueueRoundsRunOut(t *testing.T) {
	qoderQuietSleep(t)
	queue := qoderQueuedFrame(t, 5)
	reader := newQoderChunkReaderWithTools(strings.NewReader(queue), "ultimate", nil)
	reopened := 0
	reader.reopen = func(context.Context) (io.Reader, error) {
		reopened++
		return strings.NewReader(queue), nil
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	stream := string(out)
	if reopened != qoderQueueMaxRounds {
		t.Fatalf("reopened %d times, want the %d rounds the budget allows", reopened, qoderQueueMaxRounds)
	}
	if !strings.Contains(stream, `"error"`) {
		t.Fatalf("a queue that never cleared must end as an error: %s", stream)
	}
	if !strings.Contains(stream, upstreamBusyErrorType) {
		t.Fatalf("the refusal must still read as a busy model: %s", stream)
	}
}

// A transient upstream fault is retried on the same account before the turn is
// given to the failover loop.
func TestQoderRetriesATransientFault(t *testing.T) {
	qoderQuietSleep(t)
	fault := qoderFrame(t, map[string]any{
		"statusCodeValue": 502,
		"body":            `{"message":"provider_error"}`,
	})
	success := qoderChunkFrame(t, map[string]any{"content": "recovered"}, "stop", nil)
	reader := newQoderChunkReaderWithTools(strings.NewReader(fault), "ultimate", nil)
	reopened := 0
	reader.reopen = func(context.Context) (io.Reader, error) {
		reopened++
		return strings.NewReader(success), nil
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	stream := string(out)
	if reopened != 1 {
		t.Fatalf("reopened %d times, want 1", reopened)
	}
	if !strings.Contains(stream, "recovered") {
		t.Fatalf("the retried answer never arrived: %s", stream)
	}
	if strings.Contains(stream, "qoder_queue") {
		t.Fatalf("a transient fault is not a queue: %s", stream)
	}
}

// Once the answer has started, a refusal cannot be retried: the client is
// already reading this turn, and a second answer would arrive on top of it.
func TestQoderDoesNotRetryAfterTheAnswerStarted(t *testing.T) {
	qoderQuietSleep(t)
	reply := qoderChunkFrame(t, map[string]any{"content": "first words"}, "", nil) +
		qoderQueuedFrame(t, 5)
	reader := newQoderChunkReaderWithTools(strings.NewReader(reply), "ultimate", nil)
	reopened := 0
	reader.reopen = func(context.Context) (io.Reader, error) {
		reopened++
		return strings.NewReader(""), nil
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if reopened != 0 {
		t.Fatalf("a started answer must not be reopened, got %d attempts", reopened)
	}
	if !strings.Contains(string(out), `"error"`) {
		t.Fatalf("the refusal has to reach the client: %s", out)
	}
}

// The wait a vendor names is honoured, but a single round is capped: a model
// that asks for an hour must not park the client for an hour.
func TestQoderQueueWaitIsCappedAndJittered(t *testing.T) {
	var slept []time.Duration
	previous := qoderSleep
	qoderSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { qoderSleep = previous })

	success := qoderChunkFrame(t, map[string]any{"content": "ok"}, "stop", nil)
	reader := newQoderChunkReaderWithTools(strings.NewReader(qoderQueuedFrame(t, 120)), "ultimate", nil)
	reader.reopen = func(context.Context) (io.Reader, error) {
		return strings.NewReader(success), nil
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 {
		t.Fatalf("slept %d times, want 1", len(slept))
	}
	// The round is the cap plus a second plus the jitter, never the 120s asked.
	if slept[0] < qoderQueueSingleWait+time.Second {
		t.Fatalf("wait = %v, want at least the capped round", slept[0])
	}
	if slept[0] > qoderQueueSingleWait+time.Second+qoderQueueJitterMax*time.Millisecond {
		t.Fatalf("wait = %v, beyond the cap and the jitter", slept[0])
	}
}

// A vendor that names a wait longer than one round allows is not waited for: the
// queue would outlast the request anyway, so the refusal is passed on.
func TestQoderRefusesAnAbsurdQueueWait(t *testing.T) {
	qoderQuietSleep(t)
	reader := newQoderChunkReaderWithTools(strings.NewReader(qoderQueuedFrame(t, int(qoderQueueWaitMax.Seconds())+60)), "ultimate", nil)
	reopened := 0
	reader.reopen = func(context.Context) (io.Reader, error) {
		reopened++
		return strings.NewReader(""), nil
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if reopened != 0 {
		t.Fatalf("an out-of-range wait must not be retried, got %d attempts", reopened)
	}
	if !strings.Contains(string(out), `"error"`) {
		t.Fatalf("the refusal has to reach the client: %s", out)
	}
}

// qoderTrackedBody is an upstream stream that remembers being closed.
type qoderTrackedBody struct {
	io.Reader
	closed bool
}

func (b *qoderTrackedBody) Close() error {
	b.closed = true
	return nil
}

// The reader owns the upstream stream: an abandoned turn must not leave the
// connection open, and a retry must close the attempt it walked away from.
func TestQoderReaderClosesTheUpstreamStream(t *testing.T) {
	qoderQuietSleep(t)
	body := &qoderTrackedBody{Reader: strings.NewReader(
		qoderChunkFrame(t, map[string]any{"content": "done"}, "stop", nil))}
	reader := newQoderChunkReaderWithTools(body, "ultimate", nil)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("the stream was closed while it was still being read")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("the upstream stream was never closed")
	}
}

func TestQoderReloadClosesTheAbandonedStream(t *testing.T) {
	qoderQuietSleep(t)
	first := &qoderTrackedBody{Reader: strings.NewReader(qoderQueuedFrame(t, 5))}
	second := &qoderTrackedBody{Reader: strings.NewReader(
		qoderChunkFrame(t, map[string]any{"content": "second try"}, "stop", nil))}
	reader := newQoderChunkReaderWithTools(first, "ultimate", nil)
	reader.reopen = func(context.Context) (io.Reader, error) { return second, nil }
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !first.closed {
		t.Fatal("the queued attempt was abandoned without being closed")
	}
	if !strings.Contains(string(out), "second try") {
		t.Fatalf("the retried answer never arrived: %s", out)
	}
}
