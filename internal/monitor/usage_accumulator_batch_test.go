package monitor

import (
	"sync"
	"testing"
	"time"
)

// fakeUsageDailyStore records every ApplyUsageDelta[s] call so tests can
// assert on batching behavior without touching a real DB.
type fakeUsageDailyStore struct {
	mu         sync.Mutex
	callCount  int
	totalItems int
}

func (f *fakeUsageDailyStore) ApplyUsageDelta(delta UsagePersistDelta) error {
	return f.ApplyUsageDeltas([]UsagePersistDelta{delta})
}

func (f *fakeUsageDailyStore) ApplyUsageDeltas(deltas []UsagePersistDelta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.totalItems += len(deltas)
	return nil
}

func (f *fakeUsageDailyStore) LoadUsageSince(time.Time) (map[string]UsageDayBuckets, *RequestLog, error) {
	return nil, nil, nil
}
func (f *fakeUsageDailyStore) ClearUsageSince(time.Time) error  { return nil }
func (f *fakeUsageDailyStore) PruneUsageBefore(time.Time) error { return nil }
func (f *fakeUsageDailyStore) ClearUsageDaily() error           { return nil }

func (f *fakeUsageDailyStore) snapshot() (calls, items int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount, f.totalItems
}

// The async usage worker must coalesce a burst of same-tick events into far
// fewer ApplyUsageDeltas transactions than events received — that collapsing
// is the entire point of batching (fewer WAL commits/fsyncs under load).
func TestUsageWorkerBatchesBurstIntoFewCommits(t *testing.T) {
	store := NewStore()
	fake := &fakeUsageDailyStore{}
	store.SetUsageDailyStore(fake)
	// Short window so the test does not wait out the production 60s default.
	store.SetUsageBatchConfig(1000, 100*time.Millisecond)

	const eventCount = 20
	now := time.Now()
	for i := 0; i < eventCount; i++ {
		store.EnqueueUsage(UsageEvent{
			Time: now, APIKeyID: "k1", APIKeyName: "main", Status: 200,
			InputTokens: 1, OutputTokens: 1,
		})
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, items := fake.snapshot(); items >= eventCount {
			break
		}
		if time.Now().After(deadline) {
			calls, items := fake.snapshot()
			t.Fatalf("timed out waiting for all events to persist: calls=%d items=%d, want items=%d", calls, items, eventCount)
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls, items := fake.snapshot()
	if items != eventCount {
		t.Fatalf("total persisted items = %d, want %d", items, eventCount)
	}
	if calls >= eventCount {
		t.Fatalf("no batching occurred: %d commits for %d events", calls, eventCount)
	}
}

// Batching only pays off if a clean shutdown still writes the pending batch:
// otherwise every restart silently drops the current window's stats.
func TestCloseUsageWorkerDrainsPendingBatch(t *testing.T) {
	store := NewStore()
	fake := &fakeUsageDailyStore{}
	store.SetUsageDailyStore(fake)
	// Thresholds high enough that nothing flushes on its own during the test.
	store.SetUsageBatchConfig(10000, time.Hour)

	const eventCount = 7
	now := time.Now()
	for i := 0; i < eventCount; i++ {
		store.EnqueueUsage(UsageEvent{
			Time: now, APIKeyID: "k1", APIKeyName: "main", Status: 200,
			InputTokens: 2, OutputTokens: 1,
		})
	}

	store.CloseUsageWorker()

	calls, items := fake.snapshot()
	if items != eventCount {
		t.Fatalf("shutdown dropped usage events: persisted %d of %d (calls=%d)", items, eventCount, calls)
	}
	// Idempotent: shutdown paths may call it more than once.
	store.CloseUsageWorker()
}

// A never-started worker must not block or panic on shutdown.
func TestCloseUsageWorkerWithoutWorker(t *testing.T) {
	NewStore().CloseUsageWorker()
}

func TestNormalizeUsageBatchConfig(t *testing.T) {
	cases := []struct {
		name     string
		size     int
		wait     time.Duration
		wantSize int
		wantWait time.Duration
	}{
		{"zero falls back to defaults", 0, 0, defaultUsageBatchMaxSize, defaultUsageBatchMaxWait},
		{"negative falls back to defaults", -5, -time.Second, defaultUsageBatchMaxSize, defaultUsageBatchMaxWait},
		{"explicit values kept", 120, 30 * time.Second, 120, 30 * time.Second},
		{"oversized values clamped", 999999, 24 * time.Hour, usageBatchMaxSizeCap, usageBatchMaxWaitCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size, wait := NormalizeUsageBatchConfig(tc.size, tc.wait)
			if size != tc.wantSize || wait != tc.wantWait {
				t.Fatalf("got (%d, %v), want (%d, %v)", size, wait, tc.wantSize, tc.wantWait)
			}
		})
	}
}
