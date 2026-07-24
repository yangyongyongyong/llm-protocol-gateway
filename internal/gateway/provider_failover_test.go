package gateway

import (
	"net/http"
	"testing"
	"time"
)

func TestClassifyProviderFailover(t *testing.T) {
	t.Parallel()
	utilLow := 40.0
	utilHigh := 97.0
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		util   *float64
		want   failoverClass
	}{
		{name: "transport", err: http.ErrHandlerTimeout, want: failoverSoft},
		{name: "429 soft", status: 429, want: failoverSoft},
		{name: "529 soft", status: 529, want: failoverSoft},
		{name: "503 soft", status: 503, want: failoverSoft},
		{name: "429 usage hard when util high", status: 429, body: `{"error":{"message":"You've hit your usage limit"}}`, util: &utilHigh, want: failoverHard},
		{name: "429 usage softed when util low", status: 429, body: `{"error":{"message":"You've hit your usage limit"}}`, util: &utilLow, want: failoverSoft},
		{name: "401 billing hard", status: 401, body: `{"error":{"message":"credit balance too low"}}`, want: failoverHard},
		{name: "401 plain soft", status: 401, body: `{"error":{"message":"unauthorized"}}`, want: failoverSoft},
		{name: "403 invalid_grant hard", status: 403, body: `{"error":{"message":"invalid_grant"}}`, want: failoverHard},
		{name: "400 quota hard", status: 400, body: `{"error":{"message":"insufficient_quota"}}`, want: failoverHard},
		{name: "400 rate limit soft", status: 400, body: `{"error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`, want: failoverSoft},
		{name: "400 bare exceeded no longer hard", status: 400, body: `{"error":{"message":"max tokens exceeded context"}}`, want: failoverNone},
		{name: "400 bad schema", status: 400, body: `{"error":{"message":"tools[0].type is illegal"}}`, want: failoverNone},
		{name: "200", status: 200, want: failoverNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyProviderFailover(tc.status, []byte(tc.body), tc.err, tc.util)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if shouldFailoverProvider(tc.status, []byte(tc.body), tc.err) != (tc.want != failoverNone) {
				// shouldFailover ignores util; only check when util is nil.
				if tc.util == nil {
					t.Fatalf("shouldFailover mismatch for %s", tc.name)
				}
			}
		})
	}
}

func TestShouldFailoverProviderCompat(t *testing.T) {
	t.Parallel()
	if !shouldFailoverProvider(429, nil, nil) {
		t.Fatal("expected 429 failover")
	}
	if shouldFailoverProvider(200, nil, nil) {
		t.Fatal("expected 200 no failover")
	}
}

func TestApplyProviderFailoverOutcomeSoftThreshold(t *testing.T) {
	t.Parallel()
	s := &Server{}
	providerID := "hj-claude"

	for i := 1; i <= providerSoftFailureThreshold-1; i++ {
		if !s.applyProviderFailoverOutcome(providerID, 429, nil, nil) {
			t.Fatalf("soft failure %d should still failover", i)
		}
		if _, ok := s.providerNextRetry(providerID); ok {
			t.Fatalf("soft failure %d must not mark unavailable yet", i)
		}
	}
	if !s.applyProviderFailoverOutcome(providerID, 429, nil, nil) {
		t.Fatal("threshold hit should still failover")
	}
	if _, ok := s.providerNextRetry(providerID); !ok {
		t.Fatal("expected unavailable after consecutive soft failures")
	}
	st, ok := s.providerUnavailableSnapshot(providerID)
	if !ok || st.hard {
		t.Fatalf("expected soft unavailable, got %#v ok=%v", st, ok)
	}
}

func TestApplyProviderFailoverOutcomeHardImmediate(t *testing.T) {
	t.Parallel()
	s := &Server{}
	body := []byte(`{"error":{"message":"insufficient_quota"}}`)
	if !s.applyProviderFailoverOutcome("p1", 400, body, nil) {
		t.Fatal("expected failover")
	}
	st, ok := s.providerUnavailableSnapshot("p1")
	if !ok || !st.hard {
		t.Fatalf("expected hard unavailable, got %#v ok=%v", st, ok)
	}
}

func TestApplyProviderFailoverOutcomeUsageSoftedByCache(t *testing.T) {
	t.Parallel()
	s := &Server{oauthUsageCache: newOAuthUsageCache()}
	s.oauthUsageCache.set("claude:hj-claude", ClaudeOAuthUsageReport{
		Available: true,
		FiveHour:  &ClaudeOAuthUsageBucket{Utilization: 42},
	})
	body := []byte(`{"error":{"message":"You've hit your usage limit"}}`)
	if !s.applyProviderFailoverOutcome("hj-claude", 429, body, nil) {
		t.Fatal("expected failover")
	}
	if _, ok := s.providerNextRetry("hj-claude"); ok {
		t.Fatal("usage-limit 429 with low five_hour must not hard-mark on first hit")
	}
}

func TestApplyProviderFailoverOutcomeSuccessClears(t *testing.T) {
	t.Parallel()
	s := &Server{}
	_ = s.applyProviderFailoverOutcome("p1", 400, []byte(`{"error":{"message":"insufficient_quota"}}`), nil)
	if s.applyProviderFailoverOutcome("p1", 200, nil, nil) {
		t.Fatal("200 must not failover")
	}
	if _, ok := s.providerNextRetry("p1"); ok {
		t.Fatal("success must clear unavailable")
	}
}

func TestSoftFailureWindowResets(t *testing.T) {
	t.Parallel()
	s := &Server{}
	providerID := "p-soft"
	_ = s.applyProviderFailoverOutcome(providerID, 503, nil, nil)
	s.providerAvailabilityMu.Lock()
	st := s.providerSoftFailures[providerID]
	st.windowStart = time.Now().Add(-providerSoftFailureWindow - time.Second)
	s.providerSoftFailures[providerID] = st
	s.providerAvailabilityMu.Unlock()

	_ = s.applyProviderFailoverOutcome(providerID, 503, nil, nil)
	s.providerAvailabilityMu.Lock()
	count := s.providerSoftFailures[providerID].count
	s.providerAvailabilityMu.Unlock()
	if count != 1 {
		t.Fatalf("window expiry should reset count to 1, got %d", count)
	}
}

func TestAPIKeyProviderChainAndActiveIndex(t *testing.T) {
	t.Parallel()
	chain := apiKeyProviderChain("p1", []string{"p2", "p1", "p3", ""})
	if len(chain) != 3 || chain[0] != "p1" || chain[1] != "p2" || chain[2] != "p3" {
		t.Fatalf("unexpected chain: %#v", chain)
	}
	if got := apiKeyEffectiveProviderIndex("p3", chain); got != 2 {
		t.Fatalf("active index=%d want 2", got)
	}
	if got := apiKeyEffectiveProviderIndex("", chain); got != 0 {
		t.Fatalf("empty active index=%d want 0", got)
	}
	if got := sanitizeActiveProviderID("p2", "p1", []string{"p2", "p3"}); got != "p2" {
		t.Fatalf("sanitize keep=%q", got)
	}
	if got := sanitizeActiveProviderID("p1", "p1", []string{"p2"}); got != "" {
		t.Fatalf("sanitize preferred should clear, got %q", got)
	}
	if got := sanitizeActiveProviderID("px", "p1", []string{"p2"}); got != "" {
		t.Fatalf("sanitize unknown should clear, got %q", got)
	}
}

func TestFailoverResponseWriterDefersErrors(t *testing.T) {
	t.Parallel()
	base := &captureResponseWriter{header: make(http.Header)}
	w := newFailoverResponseWriter(base, true)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"rate"}`))
	if base.status != 0 {
		t.Fatalf("deferred writer should not flush yet, status=%d", base.status)
	}
	if !shouldFailoverProvider(w.Status(), w.BufferedBody(), nil) {
		t.Fatal("expected failover")
	}
	w.Discard()
	w2 := newFailoverResponseWriter(base, false)
	w2.WriteHeader(http.StatusOK)
	_, _ = w2.Write([]byte(`ok`))
	if base.status != http.StatusOK || string(base.body) != "ok" {
		t.Fatalf("passthrough failed status=%d body=%s", base.status, base.body)
	}
}

type captureResponseWriter struct {
	header http.Header
	status int
	body   []byte
}

func (c *captureResponseWriter) Header() http.Header { return c.header }
func (c *captureResponseWriter) WriteHeader(statusCode int) {
	if c.status == 0 {
		c.status = statusCode
	}
}
func (c *captureResponseWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	c.body = append(c.body, p...)
	return len(p), nil
}
