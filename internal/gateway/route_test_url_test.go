package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestGatewayOutputURLUsesActualListener(t *testing.T) {
	state := domain.GatewayState{
		Endpoints: []domain.OutputEndpoint{{
			Protocol:   domain.ProtocolOpenAIChat,
			BasePath:   "/v1",
			ListenHost: "192.168.199.227",
			ListenPort: 18093,
		}},
	}
	cases := []struct {
		name       string
		listenAddr string
		want       string
	}{
		{name: "loopback listener", listenAddr: "127.0.0.1:23001", want: "http://127.0.0.1:23001/v1/chat/completions"},
		{name: "all-interface listener uses loopback", listenAddr: "0.0.0.0:23002", want: "http://127.0.0.1:23002/v1/chat/completions"},
		{name: "missing listener falls back to default", listenAddr: "", want: "http://127.0.0.1:18093/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{router: NewRouter(state), listenAddr: tc.listenAddr}
			got, err := server.gatewayOutputURL(domain.ProtocolOpenAIChat)
			if err != nil {
				t.Fatalf("gatewayOutputURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("gatewayOutputURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRouteTestReachesLoopbackAndKeepsUpstreamDiagnostics(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":6004,"msg":"usage limit exceeded"}`))
	}))
	defer upstream.Close()

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "Provider", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: upstream.URL, AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-upstream",
			DefaultModel: "model-1",
		}},
		Models: []domain.Model{{ID: "model-1", ProviderID: "p1", Protocol: domain.ProtocolOpenAIChat, InMenu: true}},
		Routes: []domain.Route{{ID: "r1", Name: "Route", ProviderID: "p1", OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}},
		Endpoints: []domain.OutputEndpoint{{
			ID: "out", Protocol: domain.ProtocolOpenAIChat, BasePath: "/v1",
			// This is the address the old implementation dialled. It must not be
			// used for a request this process makes to itself.
			ListenHost: "192.0.2.10", ListenPort: 9,
		}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	gateway := httptest.NewServer(server.Handler())
	defer gateway.Close()
	server.SetListenAddr(strings.TrimPrefix(gateway.URL, "http://"))

	request := httptest.NewRequest(http.MethodPost, "/__routes/r1/test", strings.NewReader(`{"model":"model-1","message":"hi"}`))
	result, status := server.testRoute(request, "r1", "model-1", "hi", time.Now())
	if status != http.StatusOK {
		t.Fatalf("testRoute status = %d, result = %#v", status, result)
	}
	if success, _ := result["success"].(bool); success {
		t.Fatalf("route test unexpectedly succeeded: %#v", result)
	}
	if got, _ := result["status"].(int); got != http.StatusTooManyRequests {
		t.Fatalf("inner status = %#v, want 429; result = %#v", result["status"], result)
	}
	if got, _ := result["model"].(string); got != "model-1" {
		t.Fatalf("model = %#v, want model-1", result["model"])
	}
	diagnostics, _ := result["diagnostics"].(map[string]any)
	if diagnostics == nil {
		t.Fatalf("missing diagnostics: %#v", result)
	}
	if got, _ := diagnostics["status"].(int); got != http.StatusTooManyRequests {
		t.Fatalf("diagnostic status = %#v, want 429", diagnostics["status"])
	}
	if got, _ := diagnostics["errorMessage"].(string); !strings.Contains(got, "usage limit exceeded") {
		t.Fatalf("diagnostic errorMessage = %q", got)
	}
	if got, _ := diagnostics["responseBody"].(string); !strings.Contains(got, "6004") {
		t.Fatalf("diagnostic responseBody = %q", got)
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
}
