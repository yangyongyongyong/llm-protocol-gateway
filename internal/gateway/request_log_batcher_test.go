package gateway

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// fakeRequestLogStore records how rows arrived: one at a time (write-through)
// or as a batch.
type fakeRequestLogStore struct {
	mu      sync.Mutex
	singles []monitor.RequestLog
	batches [][]monitor.RequestLog
	failErr error
}

func (f *fakeRequestLogStore) AppendRequestLog(log monitor.RequestLog) error {
	return f.AppendRequestLogWithRetention(log, 7)
}

func (f *fakeRequestLogStore) AppendRequestLogWithRetention(log monitor.RequestLog, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.singles = append(f.singles, log)
	return nil
}

func (f *fakeRequestLogStore) AppendRequestLogs(logs []monitor.RequestLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.batches = append(f.batches, append([]monitor.RequestLog(nil), logs...))
	return nil
}

func (f *fakeRequestLogStore) ListRequestLogs(int) ([]monitor.RequestLog, error) {
	return nil, nil
}

func (f *fakeRequestLogStore) QueryRequestLogs(monitor.RequestLogQuery) (monitor.RequestLogPage, error) {
	return monitor.RequestLogPage{}, nil
}

func (f *fakeRequestLogStore) PruneRequestLogs(int) error { return nil }

func (f *fakeRequestLogStore) snapshot() (singles int, batches [][]monitor.RequestLog) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.singles), append([][]monitor.RequestLog(nil), f.batches...)
}

func newTestBatcher(t *testing.T, store RequestLogStore, interval time.Duration, maxBatch int) *requestLogBatcher {
	t.Helper()
	b := newRequestLogBatcher(store, func() int { return 7 }, nil, interval, maxBatch)
	if b == nil {
		t.Fatal("newRequestLogBatcher returned nil")
	}
	t.Cleanup(b.Close)
	return b
}

// Failures must hit the DB immediately: they are what someone needs when
// debugging, which is exactly when the process may be about to die.
func TestRequestLogBatcherWritesFailuresThrough(t *testing.T) {
	store := &fakeRequestLogStore{}
	b := newTestBatcher(t, store, time.Hour, 100)

	b.Append(monitor.RequestLog{Status: 502}, true)

	singles, batches := store.snapshot()
	if singles != 1 {
		t.Fatalf("failure not written through: singles=%d", singles)
	}
	if len(batches) != 0 {
		t.Fatalf("failure should not be batched: %v", batches)
	}
}

// Successes wait for the window instead of committing per request.
func TestRequestLogBatcherHoldsSuccessesUntilFlush(t *testing.T) {
	store := &fakeRequestLogStore{}
	b := newTestBatcher(t, store, time.Hour, 100)

	for i := 0; i < 5; i++ {
		b.Append(monitor.RequestLog{Status: 200}, false)
	}
	if singles, batches := store.snapshot(); singles != 0 || len(batches) != 0 {
		t.Fatalf("successes written before flush: singles=%d batches=%v", singles, batches)
	}

	b.Flush()
	singles, batches := store.snapshot()
	if singles != 0 {
		t.Fatalf("successes must not use the single-row path: singles=%d", singles)
	}
	if len(batches) != 1 || len(batches[0]) != 5 {
		t.Fatalf("expected one batch of 5, got %v", batches)
	}
}

// Hitting the size cap flushes without waiting out the (long) window, so a
// traffic burst cannot grow the buffer unbounded.
func TestRequestLogBatcherFlushesWhenFull(t *testing.T) {
	store := &fakeRequestLogStore{}
	b := newTestBatcher(t, store, time.Hour, 3)

	for i := 0; i < 3; i++ {
		b.Append(monitor.RequestLog{Status: 200}, false)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, batches := store.snapshot(); len(batches) > 0 {
			if len(batches[0]) != 3 {
				t.Fatalf("expected a batch of 3, got %v", batches)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("size-triggered flush never happened")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The timer path must flush on its own, with no external trigger.
func TestRequestLogBatcherFlushesOnInterval(t *testing.T) {
	store := &fakeRequestLogStore{}
	b := newTestBatcher(t, store, 50*time.Millisecond, 1000)

	b.Append(monitor.RequestLog{Status: 200}, false)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, batches := store.snapshot(); len(batches) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("interval flush never happened")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close must land buffered rows — otherwise a graceful restart loses the last
// window's worth of logs.
func TestRequestLogBatcherFlushesOnClose(t *testing.T) {
	store := &fakeRequestLogStore{}
	b := newRequestLogBatcher(store, func() int { return 7 }, nil, time.Hour, 1000)

	b.Append(monitor.RequestLog{Status: 200}, false)
	b.Close()

	if _, batches := store.snapshot(); len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("close did not flush: %v", batches)
	}
	// Close must be idempotent (shutdown paths may call it more than once).
	b.Close()
}

// A failing store must not wedge the batcher or lose the error report.
func TestRequestLogBatcherReportsErrors(t *testing.T) {
	store := &fakeRequestLogStore{failErr: errors.New("disk on fire")}
	var mu sync.Mutex
	var reported []string
	b := newRequestLogBatcher(store, func() int { return 7 }, func(message, context string) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, message+": "+context)
	}, time.Hour, 1000)
	t.Cleanup(b.Close)

	b.Append(monitor.RequestLog{Status: 200}, false)
	b.Flush()
	b.Append(monitor.RequestLog{Status: 500}, true)

	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 2 {
		t.Fatalf("expected both failures reported, got %v", reported)
	}
}

// A nil batcher (no store configured, e.g. in tests) must be safe to use.
func TestRequestLogBatcherNilSafe(t *testing.T) {
	var b *requestLogBatcher
	b.Append(monitor.RequestLog{}, false)
	b.Append(monitor.RequestLog{}, true)
	b.Flush()
	b.Close()
	if got := newRequestLogBatcher(nil, nil, nil, 0, 0); got != nil {
		t.Fatal("batcher without a store should be nil")
	}
}
