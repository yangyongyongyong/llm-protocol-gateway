package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func localDesktopSaveRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/__desktop/save-text-file", strings.NewReader(body))
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	return req
}

func TestDesktopSaveTextFileCallsNativeBridge(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	var gotFilename, gotContent string
	server.SetDesktopSaveTextFile(func(defaultFilename, content string) (bool, string, error) {
		gotFilename, gotContent = defaultFilename, content
		return true, "/tmp/tencent.custom-connector.json", nil
	})

	rec := httptest.NewRecorder()
	server.handleDesktopSaveTextFile(rec, localDesktopSaveRequest(`{"defaultFilename":"../../tencent.custom-connector.json","content":"{\"ok\":true}\n"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotFilename != "tencent.custom-connector.json" {
		t.Fatalf("default filename=%q, want sanitized basename", gotFilename)
	}
	if gotContent != "{\"ok\":true}\n" {
		t.Fatalf("content=%q", gotContent)
	}
	var got struct {
		Saved     bool   `json:"saved"`
		Cancelled bool   `json:"cancelled"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Saved || got.Cancelled || got.Path != "/tmp/tencent.custom-connector.json" {
		t.Fatalf("response=%+v", got)
	}
}

func TestDesktopSaveTextFileReportsCancel(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.SetDesktopSaveTextFile(func(string, string) (bool, string, error) {
		return false, "", nil
	})
	rec := httptest.NewRecorder()
	server.handleDesktopSaveTextFile(rec, localDesktopSaveRequest(`{"defaultFilename":"x.json","content":"{}"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Saved     bool `json:"saved"`
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Saved || !got.Cancelled {
		t.Fatalf("response=%+v", got)
	}
}

func TestDesktopSaveTextFileHeadlessReturnsConflict(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	rec := httptest.NewRecorder()
	server.handleDesktopSaveTextFile(rec, localDesktopSaveRequest(`{"defaultFilename":"x.json","content":"{}"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDesktopSaveTextFileRejectsNonLocalCaller(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.SetDesktopSaveTextFile(func(string, string) (bool, string, error) {
		t.Fatal("remote caller reached native save bridge")
		return false, "", nil
	})
	req := httptest.NewRequest(http.MethodPost, "/__desktop/save-text-file", strings.NewReader(`{"defaultFilename":"x.json","content":"{}"}`))
	req.Host = "gateway.example.com"
	req.RemoteAddr = "203.0.113.9:4321"
	rec := httptest.NewRecorder()
	server.handleDesktopSaveTextFile(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
