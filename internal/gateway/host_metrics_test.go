package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestHostMetricsSamplerStartsAndStopsWithClients(t *testing.T) {
	h := newHostMetricsSampler()
	snap := h.Snapshot()
	if snap.CPUCount <= 0 {
		t.Fatalf("cpuCount=%d", snap.CPUCount)
	}
	if snap.CollectedAt.IsZero() {
		t.Fatal("expected collectedAt")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.collecting.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !h.collecting.Load() {
		t.Fatal("expected background sampler to be running after Snapshot")
	}

	// Simulate idle clients: backdate lastRequest and wait for auto-stop.
	h.mu.Lock()
	h.lastRequest = time.Now().Add(-hostMetricsIdleStopAfter - time.Second)
	h.mu.Unlock()

	deadline = time.Now().Add(hostMetricsPollInterval + 3*time.Second)
	for time.Now().Before(deadline) {
		if !h.collecting.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("expected sampler to stop after idle window")
}

func TestHandleHostMetricsRequiresAdmin(t *testing.T) {
	s := &Server{hostMetrics: newHostMetricsSampler()}
	rec := httptest.NewRecorder()
	s.handleHostMetrics(rec, httptest.NewRequest(http.MethodGet, "/__host-metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__host-metrics", nil)
	req = req.WithContext(context.WithValue(req.Context(), authContextKey{},
		sessionIdentity{UserID: "u1", Username: "bob", Role: domain.UserRoleUser}))
	s.handleHostMetrics(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("normal user status=%d", rec.Code)
	}
}

func TestHandleHostMetricsJSON(t *testing.T) {
	s := &Server{hostMetrics: newHostMetricsSampler()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__host-metrics", nil)
	req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, adminIdentity()))
	s.handleHostMetrics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var m HostMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.CPUCount <= 0 {
		t.Fatalf("cpuCount=%d", m.CPUCount)
	}
}

func TestParsePowermetricsCPUTemp(t *testing.T) {
	in := []byte("Something\nCPU die temperature: 52.25 C\nOther\n")
	v, ok := parsePowermetricsCPUTemp(in)
	if !ok || v < 52 || v > 53 {
		t.Fatalf("got %v ok=%v", v, ok)
	}
}

func TestParsePowermetricsThermalPressure(t *testing.T) {
	in := []byte("**** Thermal pressure ****\n\nCurrent pressure level: Nominal\n")
	level, ok := parsePowermetricsThermalPressure(in)
	if !ok || level != "Nominal" {
		t.Fatalf("got %q ok=%v", level, ok)
	}
	if thermalPressureLabelZH("Nominal") != "正常" {
		t.Fatalf("label=%q", thermalPressureLabelZH("Nominal"))
	}
}

func TestSumInterfaceCountersSkipsVirtual(t *testing.T) {
	got := sumInterfaceCounters([]interfaceCounter{
		{Name: "lo0", RX: 100, TX: 100},
		{Name: "utun3", RX: 500, TX: 500},
		{Name: "en0", RX: 1000, TX: 200},
		{Name: "en1", RX: 0, TX: 0}, // link down, ignored
		{Name: "eth0", RX: 7, TX: 3},
	})
	if !got.ok || got.interfaces != 2 || got.rx != 1007 || got.tx != 203 {
		t.Fatalf("got %+v", got)
	}
}

func TestNetRates(t *testing.T) {
	base := time.Now()
	prev := netCounters{rx: 1000, tx: 500, at: base, ok: true}
	cur := netCounters{rx: 3000, tx: 1500, at: base.Add(2 * time.Second), ok: true}
	rx, tx, ok := netRates(prev, cur)
	if !ok || rx != 1000 || tx != 500 {
		t.Fatalf("rx=%v tx=%v ok=%v", rx, tx, ok)
	}
	// Counter reset must not produce a spike.
	if _, _, ok := netRates(cur, netCounters{rx: 10, tx: 10, at: cur.at.Add(time.Second), ok: true}); ok {
		t.Fatal("expected reset to be rejected")
	}
	// First sample has no baseline.
	if _, _, ok := netRates(netCounters{}, cur); ok {
		t.Fatal("expected no rate without baseline")
	}
}

func TestApplyMemPercent(t *testing.T) {
	var m HostMetrics
	m.applyMem(8<<30, 2<<30)
	if !m.MemAvailable || m.MemPercent < 24.9 || m.MemPercent > 25.1 {
		t.Fatalf("got %+v", m)
	}
}
