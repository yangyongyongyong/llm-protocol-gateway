package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type timingFlag string

const (
	timingFlagOAuthRefresh      timingFlag = "oauth_refresh"
	timingFlagSaveState         timingFlag = "save_state"
	timingFlagCursorBridgeStart timingFlag = "cursor_bridge_start"
	timingFlagDualHopBuffer     timingFlag = "dual_hop_buffer"
	timingFlagFailoverRetry     timingFlag = "failover_retry"
	timingFlagEmptyStreamRetry  timingFlag = "empty_stream_retry"
	timingFlagTouchDB           timingFlag = "touch_db"
	timingFlagThinkingRectify   timingFlag = "thinking_rectify"
	timingFlagThinkingOnlyRetry timingFlag = "thinking_only_retry"
)

type timingContextKey struct{}

// requestTiming collects observational latency marks for one model request.
// It must not change proxy behavior; missing marks simply yield zero fields.
type requestTiming struct {
	started time.Time

	prepReadyAt         atomic.Int64 // unix nano; 0 = unset
	upstreamDoStartAt   atomic.Int64
	upstreamHeaderAt    atomic.Int64
	upstreamFirstByteAt atomic.Int64
	clientFirstWriteAt  atomic.Int64

	// streamedTxBytes is the true byte count forwarded to the client on streaming
	// paths, where the logged response body is capped at passThroughLogBufferMax
	// and would otherwise understate daily traffic.
	streamedTxBytes atomic.Int64
	// txCounter wraps the client ResponseWriter to tally delivered bytes. It is
	// set once at handler entry and read at log time, so it needs no lock.
	txCounter *countingResponseWriter

	flagsMu sync.Mutex
	flags   map[timingFlag]struct{}
}

func newRequestTiming(started time.Time) *requestTiming {
	if started.IsZero() {
		started = time.Now()
	}
	return &requestTiming{started: started, flags: make(map[timingFlag]struct{}, 4)}
}

func withRequestTiming(ctx context.Context, t *requestTiming) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, timingContextKey{}, t)
}

func requestTimingFrom(ctx context.Context) *requestTiming {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(timingContextKey{}).(*requestTiming)
	return t
}

func attachRequestTiming(r *http.Request, started time.Time) (*http.Request, *requestTiming) {
	t := newRequestTiming(started)
	return r.WithContext(withRequestTiming(r.Context(), t)), t
}

func markTimingFlag(ctx context.Context, flag timingFlag) {
	if t := requestTimingFrom(ctx); t != nil {
		t.addFlag(flag)
	}
}

func (t *requestTiming) addFlag(flag timingFlag) {
	if t == nil || flag == "" {
		return
	}
	t.flagsMu.Lock()
	t.flags[flag] = struct{}{}
	t.flagsMu.Unlock()
}

func (t *requestTiming) markPrepReady() {
	if t == nil {
		return
	}
	t.prepReadyAt.CompareAndSwap(0, time.Now().UnixNano())
}

// recordStreamedTxBytes reports the true bytes streamed to the client. Nil-safe
// like the other marks, so non-proxy paths can call it unconditionally.
func (t *requestTiming) recordStreamedTxBytes(n int64) {
	if t == nil || n <= 0 {
		return
	}
	t.streamedTxBytes.Store(n)
}

// attachTxCounter wraps w so bytes delivered to the client are tallied, and
// returns the wrapper to use in place of w. Nil-safe: without timing the
// original writer is returned unchanged.
func (t *requestTiming) attachTxCounter(w http.ResponseWriter) http.ResponseWriter {
	if t == nil || w == nil {
		return w
	}
	counter := newCountingResponseWriter(w)
	t.txCounter = counter
	return counter
}

// streamedTx returns the bytes delivered to the client. Prefers the writer tally
// (covers every path); falls back to an explicitly recorded stream count.
func (t *requestTiming) streamedTx() int64 {
	if t == nil {
		return 0
	}
	if n := t.txCounter.BytesWritten(); n > 0 {
		return n
	}
	return t.streamedTxBytes.Load()
}

func (t *requestTiming) markUpstreamDoStart() {
	if t == nil {
		return
	}
	t.upstreamDoStartAt.Store(time.Now().UnixNano())
}

func (t *requestTiming) markUpstreamHeader() {
	if t == nil {
		return
	}
	t.upstreamHeaderAt.CompareAndSwap(0, time.Now().UnixNano())
}

func (t *requestTiming) markUpstreamFirstByte() {
	if t == nil {
		return
	}
	t.upstreamFirstByteAt.CompareAndSwap(0, time.Now().UnixNano())
}

func (t *requestTiming) markClientFirstWrite() {
	if t == nil {
		return
	}
	t.clientFirstWriteAt.CompareAndSwap(0, time.Now().UnixNano())
}

func (t *requestTiming) resetUpstreamMarks() {
	if t == nil {
		return
	}
	t.upstreamDoStartAt.Store(0)
	t.upstreamHeaderAt.Store(0)
	t.upstreamFirstByteAt.Store(0)
}

func msSinceStarted(started time.Time, nano int64) int64 {
	if nano <= 0 || started.IsZero() {
		return 0
	}
	delta := time.Unix(0, nano).Sub(started).Milliseconds()
	if delta < 0 {
		return 0
	}
	return delta
}

func msBetween(startNano, endNano int64) int64 {
	if startNano <= 0 || endNano <= 0 || endNano < startNano {
		return 0
	}
	return (endNano - startNano) / int64(time.Millisecond)
}

// snapshot computes persisted timing fields. ttftMs/latencyMs are authoritative
// values already measured by the handler; zeros in marks fall back gracefully.
func (t *requestTiming) snapshot(ttftMs, latencyMs int64) (
	prepMs, preUpstreamMs, upstreamTtfbMs, gatewayOverheadMs, convertOutMs, postMs int64, flags string,
) {
	if t == nil {
		if ttftMs > 0 && latencyMs > ttftMs {
			postMs = latencyMs - ttftMs
		}
		return 0, 0, 0, 0, 0, postMs, ""
	}

	prepReady := t.prepReadyAt.Load()
	doStart := t.upstreamDoStartAt.Load()
	firstByte := t.upstreamFirstByteAt.Load()
	if firstByte <= 0 {
		firstByte = t.upstreamHeaderAt.Load()
	}
	clientWrite := t.clientFirstWriteAt.Load()

	prepMs = msSinceStarted(t.started, prepReady)
	if doStart > 0 && prepReady > 0 {
		preUpstreamMs = msBetween(prepReady, doStart)
	} else if doStart > 0 {
		preUpstreamMs = msSinceStarted(t.started, doStart) - prepMs
		if preUpstreamMs < 0 {
			preUpstreamMs = 0
		}
	}
	if doStart > 0 && firstByte > 0 {
		upstreamTtfbMs = msBetween(doStart, firstByte)
	}
	if ttftMs > 0 && upstreamTtfbMs > 0 {
		gatewayOverheadMs = ttftMs - upstreamTtfbMs
		if gatewayOverheadMs < 0 {
			gatewayOverheadMs = 0
		}
	}
	if firstByte > 0 && clientWrite > 0 {
		convertOutMs = msBetween(firstByte, clientWrite)
	} else if firstByte > 0 && ttftMs > 0 {
		firstByteMs := msSinceStarted(t.started, firstByte)
		if ttftMs > firstByteMs {
			convertOutMs = ttftMs - firstByteMs
		}
	}
	if latencyMs > 0 && ttftMs > 0 && latencyMs > ttftMs {
		postMs = latencyMs - ttftMs
	}

	t.flagsMu.Lock()
	if len(t.flags) > 0 {
		parts := make([]string, 0, len(t.flags))
		for flag := range t.flags {
			parts = append(parts, string(flag))
		}
		// Stable-ish order for logs/SQL grouping.
		for i := 0; i < len(parts); i++ {
			for j := i + 1; j < len(parts); j++ {
				if parts[j] < parts[i] {
					parts[i], parts[j] = parts[j], parts[i]
				}
			}
		}
		flags = strings.Join(parts, ",")
	}
	t.flagsMu.Unlock()
	return prepMs, preUpstreamMs, upstreamTtfbMs, gatewayOverheadMs, convertOutMs, postMs, flags
}

type timingBody struct {
	io.ReadCloser
	timing *requestTiming
	once   sync.Once
}

func wrapTimingBody(body io.ReadCloser, t *requestTiming) io.ReadCloser {
	if body == nil || t == nil {
		return body
	}
	return &timingBody{ReadCloser: body, timing: t}
}

func (b *timingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { b.timing.markUpstreamFirstByte() })
	}
	return n, err
}

func doHTTPWithTiming(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	t := requestTimingFrom(ctx)
	if t != nil {
		t.markUpstreamDoStart()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if t != nil {
		t.markUpstreamHeader()
		response.Body = wrapTimingBody(response.Body, t)
	}
	return response, nil
}
