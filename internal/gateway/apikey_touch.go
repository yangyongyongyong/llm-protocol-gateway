package gateway

import (
	"sync"
	"time"
)

// apiKeyToucher coalesces api-key "last used" updates off the request hot path.
//
// Previously every proxied request synchronously called APIKeyStore.TouchAPIKey
// on the single shared SQLite connection (MaxOpenConns=1). Under contention with
// saveState / request-log writes that synchronous UPDATE added ~1s+ to the
// request prep phase. last_used_at is not latency-sensitive, so we buffer the
// latest timestamp per key and flush from one background goroutine.
type apiKeyToucher struct {
	store         APIKeyStore
	flushInterval time.Duration

	mu      sync.Mutex
	pending map[string]string // apiKeyID -> latest RFC3339 timestamp

	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	nowFn   func() time.Time
}

// newAPIKeyToucher starts a background flusher. flushInterval<=0 defaults to 2s.
func newAPIKeyToucher(store APIKeyStore, flushInterval time.Duration) *apiKeyToucher {
	if store == nil {
		return nil
	}
	if flushInterval <= 0 {
		flushInterval = 2 * time.Second
	}
	t := &apiKeyToucher{
		store:         store,
		flushInterval: flushInterval,
		pending:       make(map[string]string),
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),
		stopped:       make(chan struct{}),
		nowFn:         func() time.Time { return time.Now().UTC() },
	}
	go t.loop()
	return t
}

// Touch records that an api key was used. It is non-blocking: the DB write
// happens later on the background flusher. Repeated touches for the same key
// between flushes collapse into a single UPDATE with the newest timestamp.
func (t *apiKeyToucher) Touch(id string) {
	if t == nil || id == "" {
		return
	}
	ts := t.nowFn().Format(time.RFC3339)
	t.mu.Lock()
	t.pending[id] = ts
	t.mu.Unlock()
	// Non-blocking wake; loop coalesces multiple wakes.
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *apiKeyToucher) loop() {
	defer close(t.stopped)
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			t.flush()
			return
		case <-ticker.C:
			t.flush()
		case <-t.wake:
			// Coalesce a burst: wait one interval before flushing so many
			// near-simultaneous touches become a single batch.
			select {
			case <-t.stop:
				t.flush()
				return
			case <-time.After(t.flushInterval):
				t.flush()
			}
		}
	}
}

func (t *apiKeyToucher) flush() {
	t.mu.Lock()
	if len(t.pending) == 0 {
		t.mu.Unlock()
		return
	}
	batch := t.pending
	t.pending = make(map[string]string, len(batch))
	t.mu.Unlock()

	for id, ts := range batch {
		_ = t.store.TouchAPIKey(id, ts)
	}
}

// Close flushes remaining touches and stops the background goroutine.
func (t *apiKeyToucher) Close() {
	if t == nil {
		return
	}
	select {
	case <-t.stop:
		// already closed
	default:
		close(t.stop)
	}
	<-t.stopped
}
