package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A vendor that accepts the request and then stops sending must not hang the
// turn: the idle guard fails the read so the request ends as a provider fault
// instead of waiting for the client to give up.
func TestUpstreamIdleGuardFailsASilentStream(t *testing.T) {
	reader, writer := io.Pipe()
	guard := newIdleGuardedBody(reader, 60*time.Millisecond)
	defer guard.Close()
	go func() {
		_, _ = io.WriteString(writer, "event: message_start\ndata: {}\n\n")
		// Then silence, with the pipe still open.
	}()
	buf := make([]byte, 64)
	if n, err := guard.Read(buf); n == 0 || err != nil {
		t.Fatalf("first read: n=%d err=%v", n, err)
	}
	_, err := guard.Read(buf)
	if err == nil {
		t.Fatal("a silent stream should have failed the read")
	}
	if !strings.Contains(err.Error(), errUpstreamIdleTimeout.Error()) {
		t.Fatalf("want idle timeout, got %v", err)
	}
}

// A stream that keeps sending must never be cut: every read re-arms the timer.
func TestUpstreamIdleGuardLeavesAnActiveStreamAlone(t *testing.T) {
	reader, writer := io.Pipe()
	guard := newIdleGuardedBody(reader, 40*time.Millisecond)
	defer guard.Close()
	go func() {
		for i := 0; i < 6; i++ {
			time.Sleep(15 * time.Millisecond)
			if _, err := io.WriteString(writer, ": ping\n\n"); err != nil {
				return
			}
		}
		writer.Close()
	}()
	if _, err := io.Copy(io.Discard, guard); err != nil {
		t.Fatalf("an active stream was cut: %v", err)
	}
}

// doUpstreamHTTP is the path every converted request takes; the guard has to be
// attached there too, not only on the raw client calls.
func TestDoUpstreamHTTPGuardsTheBody(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})}
	go server.Serve(listener)
	defer server.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := doUpstreamHTTP(context.Background(), &http.Client{Transport: upstreamTransport}, request)
	if err != nil {
		t.Fatalf("doUpstreamHTTP: %v", err)
	}
	defer response.Body.Close()
	if _, ok := response.Body.(*idleGuardedBody); !ok {
		t.Fatalf("response body is %T, want the idle guard", response.Body)
	}
}

// The transport every provider request uses must not wait forever for headers.
func TestUpstreamTransportHasHeaderTimeout(t *testing.T) {
	if upstreamTransport.ResponseHeaderTimeout != upstreamHeaderWait {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", upstreamTransport.ResponseHeaderTimeout, upstreamHeaderWait)
	}
	if upstreamTransport.MaxIdleConns != maxUpstreamConns {
		t.Fatalf("MaxIdleConns = %d, want %d", upstreamTransport.MaxIdleConns, maxUpstreamConns)
	}
}

// A consumer that pauses between reads is not a silent upstream: the window
// covers the wait on the wire only, so back-pressure never trips the guard.
func TestUpstreamIdleGuardIgnoresGapsBetweenReads(t *testing.T) {
	reader, writer := io.Pipe()
	guard := newIdleGuardedBody(reader, 30*time.Millisecond)
	defer guard.Close()
	// Nobody reads for three windows; the upstream has nothing to say yet.
	time.Sleep(100 * time.Millisecond)
	go func() { _, _ = writer.Write([]byte("hello")) }()
	buffer := make([]byte, 16)
	n, err := guard.Read(buffer)
	if err != nil || string(buffer[:n]) != "hello" {
		t.Fatalf("a paused consumer must not look like a stall: n=%d err=%v", n, err)
	}
	// And the guard still works on a read that really does wait.
	if _, err := guard.Read(buffer); !errors.Is(err, errUpstreamIdleTimeout) {
		t.Fatalf("a silent wire must still trip the guard, got %v", err)
	}
}

// A caller's client without a transport talks to the vendor through the shared
// upstream transport: http.DefaultTransport would wait for headers forever.
func TestUpstreamClientAdoptsTheSharedTransport(t *testing.T) {
	t.Parallel()
	client := upstreamHTTPClient(&http.Client{})
	if client.Transport != http.RoundTripper(upstreamTransport) {
		t.Fatalf("a client without a transport must adopt the upstream one, got %T", client.Transport)
	}
	// A client that named its own transport keeps it (a test server's TLS
	// setup, a proxy).
	custom := &http.Transport{}
	if got := upstreamHTTPClient(&http.Client{Transport: custom}); got.Transport != custom {
		t.Fatalf("a named transport must be kept, got %T", got.Transport)
	}
	var nilClient *http.Client
	if got := upstreamHTTPClient(nilClient); got == nil || got.Transport == nil {
		t.Fatalf("a nil client must still get a working transport: %#v", got)
	}
}
