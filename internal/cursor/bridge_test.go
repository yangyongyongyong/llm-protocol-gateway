package cursor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSnapshotDefaultStopped(t *testing.T) {
	t.Parallel()
	bridge := NewBridge("")
	snap := bridge.Snapshot()
	if snap.Status != BridgeStatusStopped {
		t.Fatalf("status = %q, want %q", snap.Status, BridgeStatusStopped)
	}
}

func TestProbePortSuccessAgainstServer(t *testing.T) {
	t.Parallel()
	ln := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[]}`)
	}))
	defer ln.Close()

	port := 0
	u := ln.URL
	for i := len(u) - 1; i >= 0; i-- {
		if u[i] == ':' {
			_, _ = fmt.Sscanf(u[i+1:], "%d", &port)
			break
		}
	}
	if port <= 0 {
		t.Fatalf("could not parse port from %s", ln.URL)
	}

	bridge := NewBridge("")
	bridge.httpClient = ln.Client()
	if err := bridge.probePort(port); err != nil {
		t.Fatalf("probePort: %v", err)
	}
}

func TestProbePortFailsOnClosedPort(t *testing.T) {
	t.Parallel()
	bridge := NewBridge("")
	bridge.httpClient = &http.Client{}
	if err := bridge.probePort(1); err == nil {
		t.Fatal("expected probe on closed port to fail")
	}
}
