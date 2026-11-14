package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// maxFirstPacketTimeoutSec bounds the per-provider setting so one mistyped
// value cannot park a request longer than any client is willing to wait.
const maxFirstPacketTimeoutSec = 600

// upstreamHeaderWait matches Magpie's 10-minute wait for a vendor to begin
// answering. Long reasoning models can spend minutes before the first SSE
// event; a two-minute default cut healthy turns into provider faults.
//
// It only covers the wait for response headers. Per-provider and per-key
// first-packet overrides can shorten it when a provider is known to be flaky.
const upstreamHeaderWait = 10 * time.Minute

// upstreamIdleTimeout bounds how long a stream may go silent *after* its
// headers arrived. Without it a vendor that accepts the request and then stops
// sending leaves the turn hanging until the client gives up, and the gateway
// holds the connection (and the provider's lane slot) for as long as it does.
//
// The window is generous on purpose: a Claude turn can think for a while
// between frames, and the heartbeat the gateway itself sends every 15s does
// not count — this is the upstream's silence, not ours. Keep it aligned with
// Magpie's 10-minute first-byte patience.
const upstreamIdleTimeout = 10 * time.Minute

// errUpstreamIdleTimeout is what a silent upstream is reported as. Like
// errUpstreamFirstPacketTimeout it is not a context error, so the failover loop
// treats it as a provider fault rather than as the client hanging up.
var errUpstreamIdleTimeout = errors.New("upstream stopped sending mid-stream")

// maxUpstreamConns bounds the whole process: every silent stream now dies
// after upstreamIdleTimeout, so idle connection slots are freed far sooner
// than the default two-per-host pool expects.
const maxUpstreamConns = 400

// upstreamTransport is the one transport every provider request goes through.
// http.DefaultTransport is never used directly: its connection pools are sized
// for a browser, not for a gateway fanning out to many vendors, and its zero
// ResponseHeaderTimeout is exactly the "wait forever" this file exists to end.
//
// ForceAttemptHTTP2 keeps vendor endpoints on h2 where they offer it. Go gives
// no read timeout on an h2 connection, so a silent h2 stream is caught by the
// per-stream guard in idleGuardedBody rather than by the transport.
var upstreamTransport = newUpstreamTransport()

func newUpstreamTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Covers the wait for response headers on an HTTP/1 connection, which
		// is where a vendor that accepts the request and then never answers
		// would otherwise hold the caller forever.
		ResponseHeaderTimeout: upstreamHeaderWait,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxUpstreamConns,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// upstreamHTTPClient is the client a provider request is sent with. A caller's
// client that names no transport is given the shared one, so no request ever
// falls back to http.DefaultTransport and its unbounded header wait. A client
// with a transport of its own keeps it — a test server's TLS setup, a proxy.
func upstreamHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if client.Transport == nil {
		client.Transport = upstreamTransport
	}
	return client
}

// idleGuardedBody fails a read the upstream never answered. The window measures
// how long the wire itself was silent, not the gap between two Reads: between
// them the body is waiting on the caller (a slow client, back-pressure), and a
// consumer that simply does not read yet must never be mistaken for a vendor
// that stopped sending.
type idleGuardedBody struct {
	body    io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	mu      sync.Mutex
	reading bool
	idle    bool
}

func newIdleGuardedBody(body io.ReadCloser, timeout time.Duration) *idleGuardedBody {
	return &idleGuardedBody{body: body, timeout: timeout}
}

// Read arms the idle window for exactly as long as it is waiting on the wire.
func (b *idleGuardedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.idle {
		b.mu.Unlock()
		return 0, errUpstreamIdleTimeout
	}
	b.reading = true
	b.timer = time.AfterFunc(b.timeout, func() {
		b.mu.Lock()
		b.idle = true
		b.mu.Unlock()
		// Unblock the read that is waiting on the wire.
		_ = b.body.Close()
	})
	b.mu.Unlock()

	n, err := b.body.Read(p)

	b.mu.Lock()
	b.reading = false
	// A concurrent Close may already have stopped and dropped the timer.
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()

	if b.tripped() {
		return n, errUpstreamIdleTimeout
	}
	return n, err
}

func (b *idleGuardedBody) tripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.idle
}

func (b *idleGuardedBody) Close() error {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	return b.body.Close()
}

// errUpstreamFirstPacketTimeout replaces the raw context error when the
// first-packet timer fires. Returning context.Canceled would be a lie the
// failover loop cannot survive: isRequestContextCanceled treats it as "the
// client hung up" and stops there, so a provider that never answered would
// never be followed by the next one in the chain.
var errUpstreamFirstPacketTimeout = errors.New("upstream did not return response headers in time")

type firstPacketTimeoutKey struct{}

func withFirstPacketTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, firstPacketTimeoutKey{}, timeout)
}

func firstPacketTimeoutFrom(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	timeout, _ := ctx.Value(firstPacketTimeoutKey{}).(time.Duration)
	return timeout
}

// apiKeyFirstPacketTimeoutKey 携带「这个 Key 自己设的」窗口。
//
// 单独一个 context 键而不是直接折进 firstPacketTimeoutKey：Key 级是覆盖，
// Provider 级是缺省，两者的优先级要在真正发请求的那一刻才合并（见
// effectiveFirstPacketTimeout）。塞进同一个值里就分不出「没设」和「设成 0」。
type apiKeyFirstPacketTimeoutKey struct{}

func withAPIKeyFirstPacketTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, apiKeyFirstPacketTimeoutKey{}, timeout)
}

func apiKeyFirstPacketTimeoutFrom(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	timeout, _ := ctx.Value(apiKeyFirstPacketTimeoutKey{}).(time.Duration)
	return timeout
}

// effectiveFirstPacketTimeout 合并 Key 级与 Provider 级的等待窗口：Key 设了就
// 用 Key 的（调用方最清楚自己这把钥匙要等多久），否则用 Provider 的。
func effectiveFirstPacketTimeout(ctx context.Context, provider domain.Provider) time.Duration {
	if timeout := apiKeyFirstPacketTimeoutFrom(ctx); timeout > 0 {
		return timeout
	}
	return providerFirstPacketTimeout(provider)
}

// clampFirstPacketTimeoutSec 把任意输入夹到允许区间，0 表示「不设」。
func clampFirstPacketTimeoutSec(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	if seconds > maxFirstPacketTimeoutSec {
		return maxFirstPacketTimeoutSec
	}
	return seconds
}

// maxSameProviderRetries 是同一个节点最多再试几次。3 已经足够扛过瞬时抖动，
// 再多只是想用一个坏掉的账号把请求拖到超时。
const maxSameProviderRetries = 3

// clampSameProviderRetries 把重试次数夹到 0..3；0 = 一失败就换下一个节点。
func clampSameProviderRetries(n int) int {
	if n <= 0 {
		return 0
	}
	if n > maxSameProviderRetries {
		return maxSameProviderRetries
	}
	return n
}

func providerFirstPacketTimeout(provider domain.Provider) time.Duration {
	if provider.FirstPacketTimeoutSec <= 0 {
		return 0
	}
	if provider.FirstPacketTimeoutSec > maxFirstPacketTimeoutSec {
		return time.Duration(maxFirstPacketTimeoutSec) * time.Second
	}
	return time.Duration(provider.FirstPacketTimeoutSec) * time.Second
}

// headerDeadlineState decides, under one lock, whether the deadline won the
// race with the response headers. Without it a timer firing while the upstream
// was answering would cancel the context of a response the caller already owns
// and cut a healthy stream short.
type headerDeadlineState struct {
	mu       sync.Mutex
	headers  bool
	timedOut bool
}

// fire returns true when the deadline should cancel the request, i.e. the
// headers have not arrived yet.
func (s *headerDeadlineState) fire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.headers {
		return false
	}
	s.timedOut = true
	return true
}

func (s *headerDeadlineState) arrived() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers = true
}

func (s *headerDeadlineState) didTimeOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timedOut
}

// doUpstreamHTTP sends the request and enforces the first-packet deadline
// carried by ctx, if there is one. The deadline covers only the wait for the
// upstream's response headers; once they arrive the timer is stopped, so a long
// reasoning stream or a slow body is never cut short.
func doUpstreamHTTP(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	timeout := firstPacketTimeoutFrom(ctx)
	if timeout <= 0 {
		return guardUpstreamIdleClient(client, request)
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	request = request.WithContext(attemptCtx)
	state := &headerDeadlineState{}
	timer := time.AfterFunc(timeout, func() {
		if state.fire() {
			cancel()
		}
	})
	response, err := client.Do(request)
	state.arrived()
	timer.Stop()
	if err != nil {
		cancel()
		if state.didTimeOut() {
			return nil, errUpstreamFirstPacketTimeout
		}
		return nil, err
	}
	// The cancel is deliberately not called on the success path: the body still
	// streams under attemptCtx, which is released when the request's own
	// context ends.
	return guardUpstreamIdle(response), nil
}

// guardUpstreamIdleClient sends a request whose response body will be guarded:
// the callers that do not go through doUpstreamHTTP (usage probes, OAuth
// exchanges, alert webhooks) still get the idle guard.
func guardUpstreamIdleClient(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := upstreamHTTPClient(client).Do(request)
	if err != nil {
		return nil, err
	}
	return guardUpstreamIdle(response), nil
}

// guardUpstreamIdle makes sure a response whose headers already arrived cannot
// then go silent forever. Every caller that reads an upstream body goes through
// here, so a vendor that answers and then stalls is reported as a provider
// fault (errUpstreamIdleTimeout) instead of hanging the turn until the client
// gives up, with the connection still occupied on this end.
func guardUpstreamIdle(response *http.Response) *http.Response {
	if response == nil || response.Body == nil || response.Body == http.NoBody {
		return response
	}
	response.Body = newIdleGuardedBody(response.Body, upstreamIdleTimeout)
	return response
}
