package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/sleepguard"
)

type fakeSleepGuard struct {
	status    sleepguard.Status
	setValues []bool
	closed    int
}

func (f *fakeSleepGuard) Status() sleepguard.Status { return f.status }

func (f *fakeSleepGuard) Set(enabled bool) (sleepguard.Status, error) {
	f.setValues = append(f.setValues, enabled)
	f.status.Active = enabled
	return f.status, nil
}

func (f *fakeSleepGuard) Close() error {
	f.closed++
	f.status.Active = false
	return nil
}

func localSleepRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:18093"
	return req
}

func TestSleepPreventionAPIEnablesAndDisablesTheOSTarget(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	server := NewServer(router, monitor.NewStore())
	fake := &fakeSleepGuard{status: sleepguard.Status{Available: true, Platform: "macos", Message: "未开启"}}
	server.sleepGuard = fake

	rec := httptest.NewRecorder()
	server.handleSleepPrevention(rec, localSleepRequest(http.MethodGet, "/__desktop/sleep-prevention", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status sleepPreventionStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.Enabled || status.Active {
		t.Fatalf("initial status=%+v", status)
	}

	rec = httptest.NewRecorder()
	server.handleSetSleepPrevention(rec, localSleepRequest(http.MethodPatch, "/__desktop/sleep-prevention", `{"enabled":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !router.PreventSleep() || len(fake.setValues) != 1 || !fake.setValues[0] {
		t.Fatalf("enable persisted=%v calls=%v", router.PreventSleep(), fake.setValues)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.Active {
		t.Fatalf("enabled status=%+v", status)
	}

	rec = httptest.NewRecorder()
	server.handleSetSleepPrevention(rec, localSleepRequest(http.MethodPatch, "/__desktop/sleep-prevention", `{"enabled":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if router.PreventSleep() || len(fake.setValues) != 2 || fake.setValues[1] {
		t.Fatalf("disable persisted=%v calls=%v", router.PreventSleep(), fake.setValues)
	}
}

func TestSleepPreventionAPIRejectsRemoteAndUnsupportedWrites(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	server := NewServer(router, monitor.NewStore())
	server.sleepGuard = &fakeSleepGuard{status: sleepguard.Status{Available: true, Platform: "windows"}}

	remote := httptest.NewRequest(http.MethodPatch, "/__desktop/sleep-prevention", strings.NewReader(`{"enabled":true}`))
	remote.RemoteAddr = "203.0.113.9:443"
	remote.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	server.handleSetSleepPrevention(rec, remote)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d body=%s", rec.Code, rec.Body.String())
	}

	server.sleepGuard = &fakeSleepGuard{status: sleepguard.Status{Available: false, Platform: "linux", Message: "不支持"}}
	rec = httptest.NewRecorder()
	server.handleSetSleepPrevention(rec, localSleepRequest(http.MethodPatch, "/__desktop/sleep-prevention", `{"enabled":true}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsupported status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSleepPreventionRestoresPersistedChoiceAndClosesWithServer(t *testing.T) {
	router := NewRouter(domain.GatewayState{PreventSleep: true})
	server := NewServer(router, monitor.NewStore())
	fake := &fakeSleepGuard{status: sleepguard.Status{Available: true, Platform: "macos"}}
	server.sleepGuard = fake

	server.RestoreSleepPrevention()
	if len(fake.setValues) != 1 || !fake.setValues[0] {
		t.Fatalf("restore calls=%v", fake.setValues)
	}
	server.CloseSleepPrevention()
	if fake.closed != 1 {
		t.Fatalf("close calls=%d", fake.closed)
	}
}
