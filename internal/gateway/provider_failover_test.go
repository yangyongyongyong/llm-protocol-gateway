package gateway

import (
	"net/http"
	"testing"
)

func TestShouldFailoverProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		want   bool
	}{
		{name: "transport", err: http.ErrHandlerTimeout, want: true},
		{name: "429", status: 429, want: true},
		{name: "529", status: 529, want: true},
		{name: "503", status: 503, want: true},
		{name: "401 billing", status: 401, body: `{"error":{"message":"credit balance too low"}}`, want: true},
		{name: "400 quota", status: 400, body: `{"error":{"message":"insufficient_quota"}}`, want: true},
		{name: "400 bad schema", status: 400, body: `{"error":{"message":"tools[0].type is illegal"}}`, want: false},
		{name: "200", status: 200, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldFailoverProvider(tc.status, []byte(tc.body), tc.err)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
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
