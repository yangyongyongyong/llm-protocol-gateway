package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A gateway that never times out a slow-loris style client holds a connection
// and a goroutine for as long as the client wants: the header read is bounded,
// but the body read and an idle keep-alive connection were not. Streaming
// responses are the reason WriteTimeout must stay off — a long answer is not a
// stalled one — so the three that can be bounded must actually be set.
func TestHTTPServerBoundsSlowClients(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", nil)
	defer srv.Close()

	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout must be set: a client that never finishes its headers would hold the connection forever")
	}
	if srv.ReadTimeout <= 0 {
		t.Fatal("ReadTimeout must be set: a client that dribbles its body would hold the connection forever")
	}
	if srv.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout must be set: an idle keep-alive connection would be held open forever")
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout must stay off (streams are long by design), got %v", srv.WriteTimeout)
	}
	// A body read bound shorter than the header bound would cut off legitimate
	// large uploads (tens of MB of tool results) for the same reason.
	if srv.ReadTimeout <= srv.ReadHeaderTimeout {
		t.Fatalf("ReadTimeout (%v) must outlast ReadHeaderTimeout (%v)", srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout > time.Minute {
		t.Fatalf("an idle connection should not survive a whole minute of client silence, got %v", srv.IdleTimeout)
	}
}

// A health probe must enforce its own timeout even when the TCP peer accepts
// the connection but never writes a response. With the old default http.Get
// this call waited forever; the updater therefore never wrote either a healthy
// marker or an explicit rollback request, and the detached watchdog eventually
// rolled the new build back as "timed out".
func TestWaitHealthyBoundsStalledHTTPResponse(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	rt := New()
	rt.port = server.Listener.Addr().(*net.TCPAddr).Port
	started := time.Now()
	if err := rt.WaitHealthy(150 * time.Millisecond); err == nil {
		t.Fatal("stalled health endpoint unexpectedly reported healthy")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("health probe ignored its deadline and blocked for %s", elapsed)
	}
}
