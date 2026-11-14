package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestDesktopOpenExternalURLAllowsCloudflareAndProjectRelease(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	opened := ""
	server.externalURLOpener = func(raw string) error {
		opened = raw
		return nil
	}
	for _, target := range []string{
		"https://dash.cloudflare.com/argotunnel?callback=abc",
		"https://github.com/yangyongyongyong/llm-protocol-gateway/releases/tag/v1.0.23",
	} {
		opened = ""
		req := httptest.NewRequest(http.MethodPost, "/__desktop/open-url", strings.NewReader(`{"url":"`+target+`"}`))
		req.Host = "127.0.0.1:18093"
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		server.handleDesktopOpenExternalURL(rec, req)
		if rec.Code != http.StatusOK || opened != target {
			t.Fatalf("open %q: status=%d opened=%q body=%s", target, rec.Code, opened, rec.Body.String())
		}
	}
}

func TestDesktopOpenExternalURLRejectsForeignHost(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.externalURLOpener = func(string) error { t.Fatal("foreign URL reached opener"); return nil }
	req := httptest.NewRequest(http.MethodPost, "/__desktop/open-url", strings.NewReader(`{"url":"https://evil.example/phish"}`))
	req.Host = "127.0.0.1:18093"
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	server.handleDesktopOpenExternalURL(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}
