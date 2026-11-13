package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestProviderLaneAcquireRelease(t *testing.T) {
	t.Parallel()
	lane := &providerLane{max: 2, queueCap: 8, wake: make(chan struct{})}
	releaseA, err := lane.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := lane.inFlight(); got != 1 {
		t.Fatalf("expected one in flight, got %d", got)
	}
	releaseB, err := lane.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	// Full now: with no wait configured the refusal is immediate.
	started := time.Now()
	_, err = lane.acquire(context.Background(), 0)
	if !errors.Is(err, errProviderSaturated) {
		t.Fatalf("expected saturation, got %v", err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatalf("wait=0 must refuse immediately, took %v", time.Since(started))
	}
	if !strings.Contains(err.Error(), "2/2 already in flight") {
		t.Fatalf("the refusal should say why, got %q", err.Error())
	}
	releaseA()
	if got := lane.inFlight(); got != 1 {
		t.Fatalf("release should free a slot, got %d", got)
	}
	if _, err := lane.acquire(context.Background(), 0); err != nil {
		t.Fatalf("a freed slot must be grantable, got %v", err)
	}
}

func TestProviderLaneWakesWaiters(t *testing.T) {
	t.Parallel()
	lane := &providerLane{max: 1, queueCap: 8, wake: make(chan struct{})}
	release, err := lane.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	granted := make(chan struct{})
	go func() {
		waitRelease, err := lane.acquire(context.Background(), 2*time.Second)
		if err != nil {
			t.Errorf("waiter failed: %v", err)
			return
		}
		close(granted)
		waitRelease()
	}()
	// The waiter is queued, not refused: it must still be holding nothing.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && lane.waiterCount() != 1 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := lane.waiterCount(); got != 1 {
		t.Fatalf("expected one queued request, got %d", got)
	}
	release()
	select {
	case <-granted:
	case <-time.After(2 * time.Second):
		t.Fatal("the queued request was never woken")
	}
}

func TestProviderLaneWaitTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	lane := &providerLane{max: 1, queueCap: 8, wake: make(chan struct{})}
	release, err := lane.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = lane.acquire(context.Background(), 80*time.Millisecond)
	if !errors.Is(err, errProviderSaturated) {
		t.Fatalf("expected saturation after the wait, got %v", err)
	}
	if elapsed := time.Since(started); elapsed < 60*time.Millisecond {
		t.Fatalf("the request should have waited its full budget, gave up after %v", elapsed)
	}
	if got := lane.waiterCount(); got != 0 {
		t.Fatalf("a timed-out waiter must leave the queue, got %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := lane.acquire(ctx, 5*time.Second)
		canceled <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && lane.waiterCount() != 1 {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("a gone client must surface the context error, got %v", err)
	}
	if got := lane.waiterCount(); got != 0 {
		t.Fatalf("a canceled waiter must leave the queue, got %d", got)
	}
	release()
}

func TestProviderLaneQueueCap(t *testing.T) {
	t.Parallel()
	lane := &providerLane{max: 1, queueCap: 1, wake: make(chan struct{})}
	release, err := lane.acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	firstQueued := make(chan struct{})
	go func() {
		waitRelease, err := lane.acquire(context.Background(), 3*time.Second)
		if err != nil {
			t.Errorf("the queued request should have been granted once a slot freed: %v", err)
		} else {
			waitRelease()
		}
		close(firstQueued)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && lane.waiterCount() != 1 {
		time.Sleep(2 * time.Millisecond)
	}
	// The queue holds one: the second arrival is refused instead of piling up.
	_, err = lane.acquire(context.Background(), time.Second)
	if !errors.Is(err, errProviderSaturated) || !strings.Contains(err.Error(), "queue full") {
		t.Fatalf("expected a queue-full refusal, got %v", err)
	}
	release()
	<-firstQueued
}

// A provider with no cap has no queue either: clearing the limit clears the
// wait, so the console never shows a setting that does nothing.
func TestSetProviderConcurrencyClearsWaitWithCap(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1"},
	}})
	updated, err := router.SetProviderConcurrency("p1", 4, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.MaxConcurrentRequests != 4 || updated.QueueWaitSeconds != 10 {
		t.Fatalf("got %d/%d", updated.MaxConcurrentRequests, updated.QueueWaitSeconds)
	}
	cleared, err := router.SetProviderConcurrency("p1", 0, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MaxConcurrentRequests != 0 || cleared.QueueWaitSeconds != 0 {
		t.Fatalf("clearing the cap must clear the wait, got %d/%d", cleared.MaxConcurrentRequests, cleared.QueueWaitSeconds)
	}
	capped, err := router.SetProviderConcurrency("p1", 100000, 100000, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if capped.MaxConcurrentRequests != providerConcurrencyCap || capped.QueueWaitSeconds != int(providerQueueWaitCap/time.Second) {
		t.Fatalf("expected the caps, got %d/%d", capped.MaxConcurrentRequests, capped.QueueWaitSeconds)
	}
	if _, err := router.SetProviderConcurrency("ghost", 1, 1, 0, ""); err == nil {
		t.Fatal("an unknown provider must be rejected")
	}
}

func TestProviderConcurrencyConfigClamps(t *testing.T) {
	t.Parallel()
	if max, wait := providerConcurrencyConfig(domain.Provider{MaxConcurrentRequests: -5, QueueWaitSeconds: -2}); max != 0 || wait != 0 {
		t.Fatalf("negatives must read as no lane, got %d/%v", max, wait)
	}
	max, wait := providerConcurrencyConfig(domain.Provider{MaxConcurrentRequests: 100000, QueueWaitSeconds: 100000})
	if max != providerConcurrencyCap || wait != providerQueueWaitCap {
		t.Fatalf("expected the caps, got %d/%v", max, wait)
	}
	if got := providerQueueCapacity(1, 0); got < 8 {
		t.Fatalf("a single-slot lane still needs a waiting room, got %d", got)
	}
	// A server built without NewServer has no lanes and must not block traffic.
	bare := &Server{}
	release, err := bare.acquireProviderSlot(context.Background(), domain.Provider{MaxConcurrentRequests: 1})
	if err != nil {
		t.Fatalf("no lane store must mean no admission, got %v", err)
	}
	release()
}

// admissionFixture puts a controllable upstream behind a limited provider, with
// an unlimited backup available.
type admissionFixture struct {
	server    *Server
	router    *Router
	primary   *atomic.Int64
	backup    *atomic.Int64
	enter     chan struct{}
	proceed   chan struct{}
	primaryID string
	backupID  string
	key       string
	once      sync.Once
}

func newAdmissionFixture(t *testing.T, maxConcurrent int, queueWaitSeconds int, withBackup bool) *admissionFixture {
	t.Helper()
	primaryHits := &atomic.Int64{}
	backupHits := &atomic.Int64{}
	enter := make(chan struct{}, 8)
	proceed := make(chan struct{})
	upstream := func(hits *atomic.Int64, gate bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if gate {
				enter <- struct{}{}
				<-proceed
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-admission", "object": "chat.completion",
				"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "done"}}},
				"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
			})
		}))
	}
	slow := upstream(primaryHits, true)
	fast := upstream(backupHits, false)
	t.Cleanup(slow.Close)
	t.Cleanup(fast.Close)

	primary := domain.Provider{
		ID: "p-slow", Name: "p-slow", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: slow.URL + "/v1", APIKeySource: "sk-a",
		MaxConcurrentRequests: maxConcurrent, QueueWaitSeconds: queueWaitSeconds,
	}
	backup := domain.Provider{
		ID: "p-fast", Name: "p-fast", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: fast.URL + "/v1", APIKeySource: "sk-b",
	}
	route := domain.Route{ID: "r-adm", Name: "r-adm", ProviderID: primary.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	apiKey := domain.APIKey{
		ID: "k-adm", Name: "k-adm", Key: "sk-gw-admission", RouteID: route.ID,
		Enabled: true, StreamEnabled: true,
	}
	if withBackup {
		apiKey.FallbackProviderIDs = []string{backup.ID}
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{primary, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	return &admissionFixture{
		server: NewServer(router, monitor.NewStore()), router: router,
		primary: primaryHits, backup: backupHits, enter: enter, proceed: proceed,
		primaryID: primary.ID, backupID: backup.ID, key: apiKey.Key,
	}
}

func (f *admissionFixture) send(t *testing.T) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func (f *admissionFixture) sendAsync(t *testing.T, status chan<- int) {
	t.Helper()
	go func() {
		code, _, _ := f.send(t)
		status <- code
	}()
}

func (f *admissionFixture) waitUntilUpstream(t *testing.T) {
	t.Helper()
	select {
	case <-f.enter:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream never received the request")
	}
}

func (f *admissionFixture) letThrough() {
	f.once.Do(func() { close(f.proceed) })
}

// Over the cap the gateway refuses the request itself: the client gets a 429
// with Retry-After, and the account is never painted 异常 for being busy.
func TestAdmissionRefusesOverCap(t *testing.T) {
	f := newAdmissionFixture(t, 1, 0, false)
	statuses := make(chan int, 1)
	f.sendAsync(t, statuses)
	f.waitUntilUpstream(t)

	code, body, header := f.send(t)
	if code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for the over-cap request (body %s)", body)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("a refusal must tell the client when to come back")
	}
	if !strings.Contains(strings.ToLower(body), "concurrent") {
		t.Fatalf("the refusal should say why, got %s", body)
	}
	f.letThrough()
	if got := <-statuses; got != http.StatusOK {
		t.Fatalf("the request inside the cap should have succeeded, got %d", got)
	}
	if f.backup.Load() != 0 {
		t.Fatalf("the backup must stay untouched when the primary only refused, hits=%d", f.backup.Load())
	}
	if _, unavailable := f.server.providerNextRetry(f.primaryID); unavailable {
		t.Fatal("a busy provider is healthy: it must not be marked unavailable")
	}
}

// A refusal is a reason to try another account, which is the whole point of
// having a chain: the client still gets its answer.
func TestAdmissionSkipsBusyProviderToFallback(t *testing.T) {
	f := newAdmissionFixture(t, 1, 0, true)
	statuses := make(chan int, 1)
	f.sendAsync(t, statuses)
	f.waitUntilUpstream(t)

	code, body, _ := f.send(t)
	f.letThrough()
	first := <-statuses
	if code != http.StatusOK {
		t.Fatalf("expected the fallback to answer, got %d: %s", code, body)
	}
	if first != http.StatusOK {
		t.Fatalf("the held request should still succeed, got %d", first)
	}
	if f.backup.Load() != 1 {
		t.Fatalf("expected exactly one fallback hit, got %d", f.backup.Load())
	}
	if _, unavailable := f.server.providerNextRetry(f.primaryID); unavailable {
		t.Fatal("the busy primary must not be cooled down by a refusal")
	}
}

// With a wait configured, an over-cap request queues instead of being refused —
// which is what a client that cannot back off needs.
func TestAdmissionQueueWaitsForASlot(t *testing.T) {
	f := newAdmissionFixture(t, 1, 5, true)
	statuses := make(chan int, 1)
	f.sendAsync(t, statuses)
	f.waitUntilUpstream(t)

	done := make(chan int, 1)
	go func() {
		code, _, _ := f.send(t)
		done <- code
	}()
	select {
	case code := <-done:
		t.Fatalf("the queued request finished too early with %d", code)
	case <-time.After(200 * time.Millisecond):
	}
	f.letThrough()
	if got := <-statuses; got != http.StatusOK {
		t.Fatalf("the held request failed with %d", got)
	}
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("the queued request should have been served, got %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued request never woke up")
	}
	if f.backup.Load() != 0 {
		t.Fatalf("a waiting request should not be pushed to the fallback, hits=%d", f.backup.Load())
	}
}

// No limit means no lane: nothing is refused and nothing queues.
func TestAdmissionDisabledByDefault(t *testing.T) {
	f := newAdmissionFixture(t, 0, 0, false)
	f.letThrough()
	statuses := make(chan int, 2)
	f.sendAsync(t, statuses)
	f.sendAsync(t, statuses)
	for i := 0; i < 2; i++ {
		if got := <-statuses; got != http.StatusOK {
			t.Fatalf("second concurrent request got %d", got)
		}
	}
	if f.server.providerInFlight(domain.Provider{ID: f.primaryID}) != 0 {
		t.Fatal("requests must release their slot when they finish")
	}
}

// A rate-only provider keeps its budget and its wait bound; a provider with a
// rate but no concurrency cap must not be mistaken for "no lane".
func TestSetProviderConcurrencyKeepsRateWithoutConcurrencyCap(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1"},
	}})
	updated, err := router.SetProviderConcurrency("p1", 0, 5, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequestsPerMinute != 30 {
		t.Fatalf("rate = %d, want 30", updated.RequestsPerMinute)
	}
	if updated.QueueWaitSeconds != 5 {
		t.Fatalf("wait = %d, want 5 (a rate-only lane can queue too)", updated.QueueWaitSeconds)
	}
	cleared, err := router.SetProviderConcurrency("p1", 0, 5, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.RequestsPerMinute != 0 || cleared.QueueWaitSeconds != 0 {
		t.Fatalf("clearing both caps must clear the wait, got %d/%d", cleared.RequestsPerMinute, cleared.QueueWaitSeconds)
	}
}
