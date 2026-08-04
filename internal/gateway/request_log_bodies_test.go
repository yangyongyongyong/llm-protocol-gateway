package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// recordAndFetch logs one request through the real record path and returns the
// resulting in-memory log entry.
func recordAndFetch(t *testing.T, server *Server, status int, requestBody, responseBody string) monitor.RequestLog {
	t.Helper()
	server.recordRequestLog(
		time.Now(), domain.APIKey{ID: "k1", Name: "main"}, true,
		"r1", "p1", "model-x", "chat", "openai_chat->openai_chat", "/v1/chat/completions",
		status, TokenUsage{InputTokens: 10, OutputTokens: 5},
		[]byte(requestBody), []byte(responseBody),
	)
	logs := server.logs.List(1)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	return logs[0]
}

// Default (Log2xxBodies off): a successful request stores no bodies at all —
// this is the write-volume reduction the setting exists for.
func TestRequestLogSkipsSuccessBodiesByDefault(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	if server.Log2xxBodies() {
		t.Fatal("Log2xxBodies must default to false")
	}
	entry := recordAndFetch(t, server, 200, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, `{"choices":[{"message":{"content":"ok"}}]}`)
	if entry.RequestBody != "" {
		t.Fatalf("2xx request body stored by default: %q", entry.RequestBody)
	}
	if entry.ResponseBody != "" {
		t.Fatalf("2xx response body stored by default: %q", entry.ResponseBody)
	}
	// Metadata must still be recorded — only the bodies are dropped.
	if entry.Status != 200 || entry.Model != "model-x" || entry.InputTokens != 10 {
		t.Fatalf("metadata lost: %+v", entry)
	}
}

// Failures always keep bodies, regardless of the setting.
func TestRequestLogAlwaysKeepsFailureBodies(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	entry := recordAndFetch(t, server, 502, `{"model":"m"}`, `{"error":{"message":"upstream exploded"}}`)
	if entry.RequestBody == "" || entry.ResponseBody == "" {
		t.Fatalf("failure bodies dropped: req=%q resp=%q", entry.RequestBody, entry.ResponseBody)
	}
	if !strings.Contains(entry.ErrorDescription, "upstream exploded") {
		t.Fatalf("error description = %q", entry.ErrorDescription)
	}
}

// A 2xx whose response body carries an error payload is a failure in disguise:
// it must keep its bodies even with the setting off, since that is exactly the
// case that needs the payload to debug.
func TestRequestLogKeepsBodiesFor2xxWithErrorPayload(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	entry := recordAndFetch(t, server, 200, `{"model":"m"}`, `{"error":{"message":"quota exceeded"}}`)
	if entry.RequestBody == "" || entry.ResponseBody == "" {
		t.Fatalf("bodies dropped for 2xx-with-error: req=%q resp=%q", entry.RequestBody, entry.ResponseBody)
	}
	if !strings.Contains(entry.ErrorDescription, "quota exceeded") {
		t.Fatalf("error description = %q", entry.ErrorDescription)
	}
}

// With the setting on, successful bodies come back — truncated at the smaller
// success cap rather than the error cap.
func TestRequestLogKeepsSuccessBodiesWhenEnabled(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	server.SetLog2xxBodies(true)

	// Must be valid JSON without an error payload: extractResponseErrorMessage
	// treats non-JSON bodies as error text, which would take the failure path
	// (and its much larger cap) instead of the success path under test.
	pad := strings.Repeat("a", logBodyCapOK*2)
	bigRequest := `{"model":"m","messages":[{"role":"user","content":"` + pad + `"}]}`
	bigResponse := `{"choices":[{"message":{"role":"assistant","content":"` + pad + `"}}]}`

	entry := recordAndFetch(t, server, 200, bigRequest, bigResponse)
	if entry.RequestBody == "" || entry.ResponseBody == "" {
		t.Fatal("success bodies missing while Log2xxBodies is on")
	}
	if entry.ErrorDescription != "" {
		t.Fatalf("clean success body misread as an error: %q", entry.ErrorDescription)
	}
	if len(entry.RequestBody) > logBodyCapOK+64 {
		t.Fatalf("success request body not truncated to the 2xx cap: %d bytes", len(entry.RequestBody))
	}
	if len(entry.ResponseBody) > logBodyCapOK+64 {
		t.Fatalf("success response body not truncated to the 2xx cap: %d bytes", len(entry.ResponseBody))
	}
}

// The toggle must round-trip through the HTTP endpoint and land in the state
// the console reads back.
func TestLog2xxBodiesEndpointRoundTrip(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	server := NewServer(router, monitor.NewStore())
	server.SetAdminAuthStore(newMemoryAdminAuthStore())
	handler := server.Handler()

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/__settings/log-2xx-bodies", strings.NewReader(body))
		req.Host = "127.0.0.1:18093"
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := patch(`{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Log2xxBodies bool `json:"log2xxBodies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Log2xxBodies || !server.Log2xxBodies() || !router.State().Log2xxBodies {
		t.Fatalf("enable not applied: resp=%v server=%v state=%v", resp.Log2xxBodies, server.Log2xxBodies(), router.State().Log2xxBodies)
	}

	if rec := patch(`{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if server.Log2xxBodies() || router.State().Log2xxBodies {
		t.Fatal("disable not applied")
	}

	// A missing "enabled" must be rejected rather than silently disabling.
	if rec := patch(`{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
