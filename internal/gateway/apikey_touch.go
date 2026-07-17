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
// apiKeyTouchPersistMinGap throttles last_used_at DB writes: the in-memory
// router state always holds the exact timestamp (shown by the console), while
// the SQLite row is refreshed at most once per gap per key. Close() force-
// flushes everything so shutdown loses nothing.
const apiKeyTouchPersistMinGap = 5 * time.Minute

type apiKeyToucher struct {
	store         APIKeyStore
	router        *Router
	flushInterval time.Duration

	mu        sync.Mutex
	pending   map[string]string    // apiKeyID -> latest RFC3339 timestamp (not yet persisted)
	persisted map[string]time.Time // apiKeyID -> last DB write time of that key

	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	nowFn   func() time.Time
}

// newAPIKeyToucher starts a background flusher. flushInterval<=0 defaults to 2s.
func newAPIKeyToucher(store APIKeyStore, router *Router, flushInterval time.Duration) *apiKeyToucher {
	if store == nil {
		return nil
	}
	if flushInterval <= 0 {
		flushInterval = 2 * time.Second
	}
	t := &apiKeyToucher{
		store:         store,
		router:        router,
		flushInterval: flushInterval,
		pending:       make(map[string]string),
		persisted:     make(map[string]time.Time),
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
	// 内存中的 router 状态立即更新（/__state 与控制台始终看到精确时间）；
	// 数据库写入由后台按 >=5min/key 节流。
	if t.router != nil {
		t.router.TouchAPIKeyLastUsed(id, ts)
	}
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
			t.flush(true)
			return
		case <-ticker.C:
			t.flush(false)
		case <-t.wake:
			// Coalesce a burst: wait one interval before flushing so many
			// near-simultaneous touches become a single batch.
			select {
			case <-t.stop:
				t.flush(true)
				return
			case <-time.After(t.flushInterval):
				t.flush(false)
			}
		}
	}
}

// flush persists pending touches. Without force, a key is written only when
// it has never been persisted by this process or its previous DB write is at
// least apiKeyTouchPersistMinGap old; skipped keys stay pending so shutdown
// (force=true) still lands their exact timestamps.
func (t *apiKeyToucher) flush(force bool) {
	now := t.nowFn()
	t.mu.Lock()
	if len(t.pending) == 0 {
		t.mu.Unlock()
		return
	}
	batch := make(map[string]string, len(t.pending))
	for id, ts := range t.pending {
		last, seen := t.persisted[id]
		if force || !seen || now.Sub(last) >= apiKeyTouchPersistMinGap {
			batch[id] = ts
			t.persisted[id] = now
			delete(t.pending, id)
		}
	}
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
