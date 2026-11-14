package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/desktopupdate"
	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

type fakeDesktopUpdater struct {
	status   desktopupdate.Status
	check    int
	download int
	install  int
	settings desktopupdate.Settings
}

func (f *fakeDesktopUpdater) Status() desktopupdate.Status { return f.status }
func (f *fakeDesktopUpdater) CheckNow() bool {
	f.check++
	f.status.State = desktopupdate.StateChecking
	return true
}
func (f *fakeDesktopUpdater) DownloadNow() bool {
	f.download++
	f.status.State = desktopupdate.StateDownloading
	return true
}
func (f *fakeDesktopUpdater) InstallNow() bool {
	f.install++
	f.status.State = desktopupdate.StateInstalling
	return true
}
func (f *fakeDesktopUpdater) SetSettings(s desktopupdate.Settings) (desktopupdate.Status, error) {
	f.settings = s
	f.status.Settings = s
	return f.status, nil
}

func localDesktopUpdateRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "127.0.0.1:18093"
	return req
}

func TestDesktopUpdateAPIReadsAndStartsActions(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	fake := &fakeDesktopUpdater{status: desktopupdate.Status{
		Available: true,
		State:     desktopupdate.StateUpToDate,
		Current:   "1.0.0",
		Settings:  desktopupdate.DefaultSettings(),
	}}
	server.desktopUpdater = fake
	opened := ""
	server.externalURLOpener = func(raw string) error {
		opened = raw
		return nil
	}

	rec := httptest.NewRecorder()
	server.handleDesktopUpdate(rec, localDesktopUpdateRequest(http.MethodGet, "/__desktop/update", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}
	var got desktopupdate.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Current != "1.0.0" {
		t.Fatalf("GET = %+v", got)
	}

	rec = httptest.NewRecorder()
	server.handleDesktopUpdateCheck(rec, localDesktopUpdateRequest(http.MethodPost, "/__desktop/update/check", ""))
	if rec.Code != http.StatusAccepted || fake.check != 1 {
		t.Fatalf("check = %d %s calls=%d", rec.Code, rec.Body.String(), fake.check)
	}

	rec = httptest.NewRecorder()
	server.handleDesktopUpdateDownload(rec, localDesktopUpdateRequest(http.MethodPost, "/__desktop/update/download", ""))
	if rec.Code != http.StatusAccepted || fake.download != 1 {
		t.Fatalf("download = %d %s calls=%d", rec.Code, rec.Body.String(), fake.download)
	}

	rec = httptest.NewRecorder()
	server.handleDesktopUpdateInstall(rec, localDesktopUpdateRequest(http.MethodPost, "/__desktop/update/install", ""))
	if rec.Code != http.StatusAccepted || fake.install != 1 {
		t.Fatalf("install = %d %s calls=%d", rec.Code, rec.Body.String(), fake.install)
	}

	rec = httptest.NewRecorder()
	server.handleDesktopUpdateSettings(rec, localDesktopUpdateRequest(http.MethodPatch, "/__desktop/update/settings", `{"autoCheck":false,"autoDownload":true,"autoInstall":true,"checkIntervalHours":24}`))
	if rec.Code != http.StatusOK || !fake.settings.AutoInstall || fake.settings.CheckIntervalHours != 24 {
		t.Fatalf("settings = %d %s settings=%+v", rec.Code, rec.Body.String(), fake.settings)
	}

	fake.status.ReleaseURL = "https://github.com/yangyongyongyong/llm-protocol-gateway/releases/tag/v1.0.20"
	rec = httptest.NewRecorder()
	server.handleDesktopUpdateOpenRelease(rec, localDesktopUpdateRequest(http.MethodPost, "/__desktop/update/open-release", ""))
	if rec.Code != http.StatusOK || opened != fake.status.ReleaseURL {
		t.Fatalf("open release = %d %s opened=%q", rec.Code, rec.Body.String(), opened)
	}
}

func TestDesktopUpdateAPIRejectsRemoteRequests(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.desktopUpdater = &fakeDesktopUpdater{status: desktopupdate.Status{Available: true}}
	req := httptest.NewRequest(http.MethodPost, "/__desktop/update/check", nil)
	req.RemoteAddr = "203.0.113.9:443"
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	server.handleDesktopUpdateCheck(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote check = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDesktopUpdateOpenReleaseRejectsForeignURL(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.desktopUpdater = &fakeDesktopUpdater{status: desktopupdate.Status{
		Available:  true,
		ReleaseURL: "https://evil.example/releases/tag/v1.0.20",
	}}
	server.externalURLOpener = func(string) error { t.Fatal("foreign URL reached opener"); return nil }
	rec := httptest.NewRecorder()
	server.handleDesktopUpdateOpenRelease(rec, localDesktopUpdateRequest(http.MethodPost, "/__desktop/update/open-release", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign release URL = %d %s", rec.Code, rec.Body.String())
	}
}
