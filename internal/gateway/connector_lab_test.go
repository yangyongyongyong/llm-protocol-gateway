package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestConnectorLabThreeProtocolsAndCleanup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = w.Write([]byte(`{"data":[{"id":"ocean-test"}]}`))
		case strings.Contains(r.URL.Path, "/chat/completions"):
			_, _ = w.Write([]byte(`{"id":"chatcmpl-lab","object":"chat.completion","created":1,"model":"ocean-test","choices":[{"index":0,"message":{"role":"assistant","content":"lab-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
		case strings.Contains(r.URL.Path, "/responses"):
			_, _ = w.Write([]byte(`{"id":"resp_lab","object":"response","status":"completed","model":"ocean-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"lab-ok"}]}],"output_text":"lab-ok","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`))
		case strings.Contains(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"id":"msg_lab","type":"message","role":"assistant","model":"ocean-test","content":[{"type":"text","text":"lab-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	gateway := httptest.NewServer(server.Handler())
	defer gateway.Close()
	// gatewayOutputURL uses the real listener port; the test server does not
	// use the production runtime's SetListenAddr, so set the same address here.
	server.listenAddr = strings.TrimPrefix(gateway.URL, "http://")
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(connectorLabRequest{
		Provider: domain.Provider{
			Name: "ocean lab", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: upstream.URL, APIKeySource: "literal:sk-lab", DefaultModel: "ocean-test",
			AuthType: domain.AuthTypeAPIKey,
		},
		Protocols: []domain.Protocol{domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude},
		Model:     "ocean-test", Message: "ping",
	})
	req := httptest.NewRequest(http.MethodPost, "/__connectors/lab", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	server.handleConnectorLab(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report connectorLabReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !report.Success {
		t.Fatalf("report failed: %+v", report)
	}
	for _, id := range []string{"protocol_openai_chat", "protocol_openai_responses", "protocol_claude"} {
		found := false
		for _, step := range report.Steps {
			if step.ID == id && step.Passed {
				found = true
			}
		}
		if !found {
			t.Fatalf("step %s did not pass: %+v", id, report.Steps)
		}
	}
	if !report.Cleaned {
		t.Fatalf("cleanup: cleaned=%v", report.Cleaned)
	}
	state := server.router.State()
	for _, provider := range state.Providers {
		if provider.ID == report.ProviderID {
			t.Fatalf("temporary provider leaked: %+v", provider)
		}
	}
	for _, route := range state.Routes {
		if route.ID == report.RouteID {
			t.Fatalf("temporary route leaked: %+v", route)
		}
	}
	for _, key := range state.APIKeys {
		if key.ID == report.APIKeyID {
			t.Fatalf("temporary api key leaked: %+v", key)
		}
	}
}

func TestConnectorLabRejectsPrivateProtocol(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	body := `{"provider":{"name":"bad","protocol":"ocean","baseUrl":"http://127.0.0.1:1","apiKeySource":"literal:x"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/__connectors/lab", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	server.handleConnectorLab(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report connectorLabReport
	_ = json.Unmarshal(rec.Body.Bytes(), &report)
	if report.Success || len(report.Steps) == 0 || report.Steps[0].ID != "validate" {
		t.Fatalf("report = %+v", report)
	}
}

func TestConnectorLabKeepOnFailure(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	gateway := httptest.NewServer(server.Handler())
	defer gateway.Close()
	server.listenAddr = strings.TrimPrefix(gateway.URL, "http://")
	body, _ := json.Marshal(connectorLabRequest{
		Provider: domain.Provider{
			Name: "bad upstream", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "http://127.0.0.1:1", APIKeySource: "literal:sk-bad",
			DefaultModel: "bad", AuthType: domain.AuthTypeAPIKey,
		},
		KeepOnFailure: true,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/__connectors/lab", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	server.handleConnectorLab(rec, req)
	var report connectorLabReport
	_ = json.Unmarshal(rec.Body.Bytes(), &report)
	if report.Success || !report.KeptOnFailure || report.ProviderID == "" {
		t.Fatalf("report = %+v", report)
	}
	if _, err := server.router.ProviderByID(report.ProviderID); err != nil {
		t.Fatalf("kept provider not found: %v", err)
	}
}
