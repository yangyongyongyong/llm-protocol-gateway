package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/autostart"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

type fakeAutostartManager struct {
	status         autostart.Status
	setValues      []bool
	openLoginItems int
}

func (f *fakeAutostartManager) Status() autostart.Status { return f.status }

func (f *fakeAutostartManager) Set(enabled bool) (autostart.Status, error) {
	f.setValues = append(f.setValues, enabled)
	f.status.Enabled = enabled
	return f.status, nil
}

func (f *fakeAutostartManager) OpenLoginItems() error {
	f.openLoginItems++
	return nil
}

func (f *fakeAutostartManager) EnsureDefault() error { return nil }

func localAutostartRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:18093"
	return req
}

func TestDesktopAutostartAPIReadsAndWritesTheSystemRecord(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	fake := &fakeAutostartManager{status: autostart.Status{
		Available: true,
		Enabled:   false,
		Mechanism: "launchAgent",
		AppPath:   "/Applications/LLM Protocol Gateway.app",
	}}
	server.desktopAutostart = fake

	rec := httptest.NewRecorder()
	server.handleDesktopAutostart(rec, localAutostartRequest(http.MethodGet, "/__desktop/autostart", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got autostart.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Enabled || got.AppPath != fake.status.AppPath {
		t.Fatalf("GET = %+v", got)
	}

	rec = httptest.NewRecorder()
	server.handleSetDesktopAutostart(rec, localAutostartRequest(http.MethodPatch, "/__desktop/autostart", `{"enabled":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || len(fake.setValues) != 1 || !fake.setValues[0] {
		t.Fatalf("PATCH = %+v, set=%v", got, fake.setValues)
	}

	rec = httptest.NewRecorder()
	server.handleOpenDesktopLoginItems(rec, localAutostartRequest(http.MethodPost, "/__desktop/autostart/open-login-items", ""))
	if rec.Code != http.StatusOK || fake.openLoginItems != 1 {
		t.Fatalf("open login items = %d, calls=%d, body=%s", rec.Code, fake.openLoginItems, rec.Body.String())
	}
}

func TestDesktopAutostartAPIRejectsRemoteRequests(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.desktopAutostart = &fakeAutostartManager{status: autostart.Status{Available: true}}

	req := httptest.NewRequest(http.MethodPatch, "/__desktop/autostart", strings.NewReader(`{"enabled":true}`))
	req.RemoteAddr = "203.0.113.9:443"
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	server.handleSetDesktopAutostart(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote PATCH status = %d body=%s", rec.Code, rec.Body.String())
	}
}
