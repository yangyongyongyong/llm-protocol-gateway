package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// qoderDeadHost is a host that refuses connections: served once, then taken
// away, which is what a gateway with a broken listener looks like.
func qoderDeadHost(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	return url
}

// qoderLiveHost serves one canned answer and counts how often it was asked.
func qoderLiveHost(t *testing.T, status int, body string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func qoderTestAccount() qoderAccount {
	return qoderAccount{UID: "uid-fallback", Token: "token-fallback"}
}

// The international deployment names three gateways; the domestic one has a
// single host and no fallback.
func TestQoderSiteHostCandidates(t *testing.T) {
	global := qoderGlobalSite.qoderHosts()
	want := []string{"https://api3.qoder.sh", "https://api2.qoder.sh", "https://api1.qoder.sh"}
	if strings.Join(global, ",") != strings.Join(want, ",") {
		t.Fatalf("global hosts = %v, want %v", global, want)
	}
	cn := qoderCNSite.qoderHosts()
	if len(cn) != 1 || cn[0] != "https://gateway.qoder.com.cn" {
		t.Fatalf("Qoder CN hosts = %v, want its one gateway", cn)
	}
	// A host named twice is tried once, and a trailing slash is not a host.
	deduped := qoderSite{API: "https://a.example/", APIFallbacks: []string{"https://a.example", "", "https://b.example"}}
	if got := deduped.qoderHosts(); len(got) != 2 || got[0] != "https://a.example" || got[1] != "https://b.example" {
		t.Fatalf("hosts = %v, want the two distinct hosts", got)
	}
}

// A gateway that cannot be reached at the transport layer is walked past: the
// next candidate answers the same request.
func TestQoderFallsBackToTheNextHostOnATransportFailure(t *testing.T) {
	live, calls := qoderLiveHost(t, http.StatusOK, "data: {}\n\n")
	site := qoderSite{API: qoderDeadHost(t), APIFallbacks: []string{live.URL}}
	response, err := qoderPost(context.Background(), site, qoderChatPath, "$kwm", "k", "system", qoderTestAccount())
	if err != nil {
		t.Fatalf("the fallback host was not used: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the live host's answer", response.StatusCode)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("the live host was asked %d times, want 1", atomic.LoadInt32(calls))
	}
}

// An HTTP answer came from Qoder and would be the same on every host: a refusal
// is reported rather than retried elsewhere.
func TestQoderDoesNotRotateHostsOnAnHTTPRefusal(t *testing.T) {
	refusing, refusedCalls := qoderLiveHost(t, http.StatusForbidden, `{"code":10605}`)
	spare, spareCalls := qoderLiveHost(t, http.StatusOK, "data: {}\n\n")
	site := qoderSite{API: refusing.URL, APIFallbacks: []string{spare.URL}}
	response, err := qoderPost(context.Background(), site, qoderChatPath, "$kwm", "k", "system", qoderTestAccount())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want the refusal the primary host sent", response.StatusCode)
	}
	if atomic.LoadInt32(refusedCalls) != 1 || atomic.LoadInt32(spareCalls) != 0 {
		t.Fatalf("a refusal must not be replayed: primary %d, spare %d",
			atomic.LoadInt32(refusedCalls), atomic.LoadInt32(spareCalls))
	}
}

// With every candidate unreachable the next account in the pool is the right
// move, so the error has to reach the caller.
func TestQoderReportsWhenEveryHostIsUnreachable(t *testing.T) {
	site := qoderSite{API: qoderDeadHost(t), APIFallbacks: []string{qoderDeadHost(t)}}
	if _, err := qoderPost(context.Background(), site, qoderChatPath, "$kwm", "k", "system", qoderTestAccount()); err == nil {
		t.Fatal("expected an error when no host answers")
	}
}

// A client that hung up must not be retried on another host.
func TestQoderStopsHostRotationWhenTheClientLeaves(t *testing.T) {
	live, calls := qoderLiveHost(t, http.StatusOK, "data: {}\n\n")
	site := qoderSite{API: qoderDeadHost(t), APIFallbacks: []string{live.URL}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := qoderPost(ctx, site, qoderChatPath, "$kwm", "k", "system", qoderTestAccount()); err == nil {
		t.Fatal("expected the cancellation to surface")
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatalf("a canceled request still asked %d hosts", atomic.LoadInt32(calls))
	}
}

// The model listing walks the candidates too: any failure moves on, because the
// next gateway lists the same models.
func TestQoderListingFallsBackThroughTheHosts(t *testing.T) {
	listing := `{"app":[{"key":"qwen3.8-max","enable":true}]}`
	live, _ := qoderLiveHost(t, http.StatusOK, listing)
	site := qoderSite{API: qoderDeadHost(t), APIFallbacks: []string{live.URL}}
	raw, err := qoderFetchListing(context.Background(), site, qoderTestAccount())
	if err != nil {
		t.Fatalf("the listing was not read from the fallback: %v", err)
	}
	if !strings.Contains(string(raw), "qwen3.8-max") {
		t.Fatalf("listing = %s", raw)
	}
}
