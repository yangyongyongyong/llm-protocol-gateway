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

// 机器状态动态采集开关要能从 HTTP 落到 Router 状态，并在关闭时真正挡住
// 机器状态接口，而不是只改一个界面复选框。
func TestHostMetricsToggleEndpointRoundTrip(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	server := NewServer(router, monitor.NewStore())
	server.SetAdminAuthStore(newMemoryAdminAuthStore())
	handler := server.Handler()

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/__settings/host-metrics", strings.NewReader(body))
		req.Host = "127.0.0.1:18093"
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	getMetrics := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/__host-metrics", nil)
		req.Host = "127.0.0.1:18093"
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := patch(`{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var disabledResp struct {
		HostMetricsDisabled bool `json:"hostMetricsDisabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &disabledResp); err != nil {
		t.Fatal(err)
	}
	if !disabledResp.HostMetricsDisabled || !router.HostMetricsDisabled() {
		t.Fatalf("disable not applied: resp=%v router=%v", disabledResp.HostMetricsDisabled, router.HostMetricsDisabled())
	}
	if rec := getMetrics(); rec.Code != http.StatusConflict {
		t.Fatalf("disabled metrics status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	if rec := patch(`{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if router.HostMetricsDisabled() {
		t.Fatal("enable not applied")
	}
	if rec := getMetrics(); rec.Code != http.StatusOK {
		t.Fatalf("enabled metrics status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	if rec := patch(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}
