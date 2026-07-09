package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostSeparatedServingBlocksCrossSurface(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api:" + r.URL.Path))
	})
	role := func(host string) string {
		switch host {
		case "gateway.example.com":
			return "api"
		case "console.example.com":
			return "ui"
		default:
			return ""
		}
	}
	handler := withHostSeparatedServing(api, "", role)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("api host UI path status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "management UI is not available") {
		t.Fatalf("body=%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Host = "gateway.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "api:/v1/models" {
		t.Fatalf("api host model path status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/__state", nil)
	req.Host = "gateway.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("api host admin api should be blocked, status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Host = "console.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ui host model path status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model API is not available") {
		t.Fatalf("body=%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/__state", nil)
	req.Host = "console.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "api:/__state" {
		t.Fatalf("ui host admin api status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeriveUIDomain(t *testing.T) {
	if got := deriveUIDomain("gateway.lucadesign.uk"); got != "console.lucadesign.uk" {
		t.Fatalf("got %q", got)
	}
	if got := deriveUIDomain("console.lucadesign.uk"); got != "admin.lucadesign.uk" {
		t.Fatalf("got %q", got)
	}
	if got := deriveUIDomain("gateway.lucadesign.uk"); strings.EqualFold(got, "gateway.lucadesign.uk") {
		t.Fatal("ui domain must differ from api domain")
	}
}
