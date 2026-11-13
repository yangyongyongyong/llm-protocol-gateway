package gateway

import (
	"encoding/json"
	"io"
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

func TestProviderFirstPacketTimeout(t *testing.T) {
	t.Parallel()
	if got := providerFirstPacketTimeout(domain.Provider{}); got != 0 {
		t.Fatalf("zero must mean no limit, got %v", got)
	}
	if got := providerFirstPacketTimeout(domain.Provider{FirstPacketTimeoutSec: -5}); got != 0 {
		t.Fatalf("negative must mean no limit, got %v", got)
	}
	if got := providerFirstPacketTimeout(domain.Provider{FirstPacketTimeoutSec: 3}); got != 3*time.Second {
		t.Fatalf("got %v", got)
	}
	if got := providerFirstPacketTimeout(domain.Provider{FirstPacketTimeoutSec: 100000}); got != time.Duration(maxFirstPacketTimeoutSec)*time.Second {
		t.Fatalf("must clamp, got %v", got)
	}
}

func TestNormalizeProviderClampsFirstPacketTimeout(t *testing.T) {
	t.Parallel()
	provider := domain.Provider{Name: "x", BaseURL: "https://example.com", FirstPacketTimeoutSec: 999999}
	normalizeProvider(&provider)
	if provider.FirstPacketTimeoutSec != maxFirstPacketTimeoutSec {
		t.Fatalf("expected the cap, got %d", provider.FirstPacketTimeoutSec)
	}
}

// timeoutFixture builds a two-provider chain: the preferred one is `slow`
// (it stalls before its response headers, or stalls mid-stream when
// stallAfterHeaders is set), the backup answers normally.
type timeoutFixture struct {
	server      *Server
	router      *Router
	preferred   *atomic.Int64
	backup      *atomic.Int64
	preferredID string
	backupID    string
}

func newTimeoutFixture(t *testing.T, timeoutSec int, stall time.Duration, stallAfterHeaders bool) *timeoutFixture {
	t.Helper()
	preferredHits := &atomic.Int64{}
	backupHits := &atomic.Int64{}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preferredHits.Add(1)
		if stallAfterHeaders {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Everything after the headers outlives the configured deadline: a
			// timer still running here would cut the stream mid-reply.
			time.Sleep(stall)
			_, _ = w.Write([]byte("data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		select {
		case <-time.After(stall):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"never reached"}}`))
	}))
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-backup", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "backup"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(fast.Close)

	preferred := domain.Provider{
		ID: "p-slow", Name: "p-slow", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: slow.URL + "/v1", APIKeySource: "sk-a",
		HealthStatus: "healthy", FirstPacketTimeoutSec: timeoutSec,
	}
	backup := domain.Provider{
		ID: "p-fast", Name: "p-fast", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, BaseURL: fast.URL + "/v1", APIKeySource: "sk-b", HealthStatus: "healthy",
	}
	route := domain.Route{ID: "r-to", Name: "r-to", ProviderID: preferred.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	apiKey := domain.APIKey{
		ID: "k-to", Name: "k-to", Key: "sk-gw-test-timeout", RouteID: route.ID,
		Enabled: true, StreamEnabled: true, FallbackProviderIDs: []string{backup.ID},
	}
	router := NewRouter(domain.GatewayState{
		Providers: []domain.Provider{preferred, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{apiKey},
	})
	return &timeoutFixture{
		server: NewServer(router, monitor.NewStore()), router: router,
		preferred: preferredHits, backup: backupHits,
		preferredID: preferred.ID, backupID: backup.ID,
	}
}

func (f *timeoutFixture) do(t *testing.T, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"gpt-requested","messages":[{"role":"user","content":"hi"}]}`
	if stream {
		body = `{"model":"gpt-requested","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-gw-test-timeout")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec
}

// A provider that never gets its headers out is abandoned after its own
// deadline and the chain continues, instead of the request hanging until every
// client gives up.
func TestFirstPacketTimeoutAbandonsStalledProviderAndFailsOver(t *testing.T) {
	f := newTimeoutFixture(t, 1, 5*time.Second, false)
	started := time.Now()
	rec := f.do(t, false)
	elapsed := time.Since(started)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the backup to serve, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backup") {
		t.Fatalf("expected the backup reply, got %s", rec.Body.String())
	}
	if f.preferred.Load() != 1 || f.backup.Load() != 1 {
		t.Fatalf("expected one attempt each, preferred=%d backup=%d", f.preferred.Load(), f.backup.Load())
	}
	if elapsed > 4*time.Second {
		t.Fatalf("the deadline should cut the stalled attempt short, took %v", elapsed)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("the deadline fired too early (%v), it is configured for 1s", elapsed)
	}
}

// The deadline covers the wait for headers only. A provider that answers and
// then thinks for longer than the deadline must keep its stream.
func TestFirstPacketTimeoutNeverCutsAStreamedReply(t *testing.T) {
	f := newTimeoutFixture(t, 1, 1600*time.Millisecond, true)
	rec := f.do(t, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "a") || !strings.Contains(body, "DONE") {
		t.Fatalf("the streamed reply was cut short: %s", body)
	}
	if f.backup.Load() != 0 {
		t.Fatalf("a provider that already answered headers must not be failed over, backup hits=%d", f.backup.Load())
	}
}

// Providers with no configured deadline keep today's behavior: they are never
// wrapped in a timer.
func TestFirstPacketTimeoutDisabledByDefault(t *testing.T) {
	f := newTimeoutFixture(t, 0, 300*time.Millisecond, false)
	rec := f.do(t, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the reply from the (merely slow) preferred provider, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.backup.Load() != 0 {
		t.Fatalf("no deadline configured means no failover, backup hits=%d", f.backup.Load())
	}
}

func TestSetProviderFirstPacketTimeout(t *testing.T) {
	t.Parallel()
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{
		{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", FirstPacketTimeoutSec: 5},
	}})
	if _, err := router.SetProviderFirstPacketTimeout("p1", 20); err != nil {
		t.Fatal(err)
	}
	provider, _ := router.ProviderByID("p1")
	if provider.FirstPacketTimeoutSec != 20 {
		t.Fatalf("expected 20, got %d", provider.FirstPacketTimeoutSec)
	}
	if _, err := router.SetProviderFirstPacketTimeout("p1", 0); err != nil {
		t.Fatal(err)
	}
	provider, _ = router.ProviderByID("p1")
	if provider.FirstPacketTimeoutSec != 0 {
		t.Fatalf("0 must clear the limit, got %d", provider.FirstPacketTimeoutSec)
	}
	if _, err := router.SetProviderFirstPacketTimeout("p1", -3); err != nil {
		t.Fatal(err)
	}
	provider, _ = router.ProviderByID("p1")
	if provider.FirstPacketTimeoutSec != 0 {
		t.Fatalf("a negative must read as no limit, got %d", provider.FirstPacketTimeoutSec)
	}
	if _, err := router.SetProviderFirstPacketTimeout("p1", 100000); err != nil {
		t.Fatal(err)
	}
	provider, _ = router.ProviderByID("p1")
	if provider.FirstPacketTimeoutSec != maxFirstPacketTimeoutSec {
		t.Fatalf("expected the cap, got %d", provider.FirstPacketTimeoutSec)
	}
	if _, err := router.SetProviderFirstPacketTimeout("nope", 5); err == nil {
		t.Fatal("expected an unknown provider to be rejected")
	}
	// The dedicated setter exists because a partial PATCH must not move this
	// value: the console's provider save would otherwise reset a tuned limit.
	if _, err := router.UpdateProvider("p1", domain.Provider{Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	provider, _ = router.ProviderByID("p1")
	if provider.FirstPacketTimeoutSec != maxFirstPacketTimeoutSec {
		t.Fatalf("a bare patch reset the limit to %d", provider.FirstPacketTimeoutSec)
	}
}

// A Close that lands while a Read is waiting on the wire stops and drops the
// timer; the Read must not stop a timer it no longer holds.
func TestIdleGuardedBodyReadAfterConcurrentClose(t *testing.T) {
	release := make(chan struct{})
	body := &blockingReadCloser{release: release}
	guarded := newIdleGuardedBody(body, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 8)
		_, _ = guarded.Read(buffer)
	}()
	for {
		guarded.mu.Lock()
		reading := guarded.reading
		guarded.mu.Unlock()
		if reading {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := guarded.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the read did not return after Close")
	}
}

// blockingReadCloser returns nothing until released, like a provider whose
// answer is still on the wire.
type blockingReadCloser struct {
	release chan struct{}
	once    sync.Once
}

func (b *blockingReadCloser) Read(p []byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() {})
	return nil
}
