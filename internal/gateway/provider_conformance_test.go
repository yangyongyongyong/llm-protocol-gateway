package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestIsSelfCheckPathIncludesConformance(t *testing.T) {
	t.Parallel()
	if !isSelfCheckPath(http.MethodPost, "/__providers/p1/self-check/conformance") {
		t.Fatal("expected conformance path to bypass session auth")
	}
}

func TestValidateConformanceShapes(t *testing.T) {
	t.Parallel()

	ok, _, _ := validateConformanceNonStream(domain.ProtocolOpenAIChat, []byte(`{"choices":[{"message":{"role":"assistant","content":"4"}}]}`))
	if !ok {
		t.Fatal("chat nonstream should pass")
	}
	ok, detail, _ := validateConformanceNonStream(domain.ProtocolOpenAIChat, []byte(`{"choices":[]}`))
	if ok || !strings.Contains(detail, "choices") {
		t.Fatalf("chat empty choices should fail: ok=%v detail=%q", ok, detail)
	}

	chatSSE := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"4"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	ok, _, _ = validateConformanceStream(domain.ProtocolOpenAIChat, []byte(chatSSE))
	if !ok {
		t.Fatal("chat stream should pass")
	}
	ok, detail, _ = validateConformanceStream(domain.ProtocolOpenAIChat, []byte(`data: {"choices":[{"delta":{"content":"4"}}]}`+"\n\n"))
	if ok || !strings.Contains(detail, "[DONE]") {
		t.Fatalf("chat stream without DONE should fail: ok=%v detail=%q", ok, detail)
	}

	respSSE := strings.Join([]string{
		`data: {"type":"response.created"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"4"}`,
		``,
		`data: {"type":"response.completed"}`,
		``,
	}, "\n")
	ok, _, _ = validateConformanceStream(domain.ProtocolOpenAIResponses, []byte(respSSE))
	if !ok {
		t.Fatal("responses stream should pass")
	}

	ok, detail, hint := validateConformanceUsage(domain.ProtocolOpenAIResponses, []byte(`{"usage":{"input_tokens":10,"output_tokens":1}}`))
	if ok || !strings.Contains(detail, "input_tokens_details") {
		t.Fatalf("responses usage without details should fail: ok=%v detail=%q hint=%q", ok, detail, hint)
	}
	ok, _, _ = validateConformanceUsage(domain.ProtocolOpenAIResponses, []byte(`{"usage":{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`))
	if !ok {
		t.Fatal("responses usage with details should pass even when cached=0")
	}
}

func TestProviderSelfCheckConformanceResponsesRequiredPassRecommendedCacheFail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5"}]}`))
			return
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/responses"):
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if stream, _ := payload["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(strings.Join([]string{
					`data: {"type":"response.created"}`,
					``,
					`data: {"type":"response.output_text.delta","delta":"4"}`,
					``,
					`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}`,
					``,
				}, "\n")))
				return
			}
			// Non-stream: return usage without cache hits (recommended cache_hit fails).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"output":[{"type":"message","content":[{"type":"output_text","text":"4"}]}],
				"status":"completed",
				"usage":{"input_tokens":12,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}
			}`))
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	handler, token, _ := newSelfCheckTestServer(t, domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIResponses,
		BaseURL: upstream.URL + "/v1/responses", APIKeySource: "literal:test-secret",
		DefaultModel: "gpt-5.5",
	})

	rec := selfCheckRequest(handler, "/__providers/p1/self-check/conformance", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report conformanceReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !report.Success || !report.PassedRequired {
		t.Fatalf("expected required pass, got %s", rec.Body.String())
	}
	if report.PassedAll {
		t.Fatalf("expected recommended cache_hit to fail so passedAll=false, got %s", rec.Body.String())
	}
	byID := map[string]conformanceCaseResult{}
	for _, c := range report.Cases {
		byID[c.ID] = c
	}
	for _, id := range []string{conformanceCaseModels, conformanceCaseNonStream, conformanceCaseStream, conformanceCaseUsage} {
		if !byID[id].Passed {
			t.Fatalf("case %s should pass: %+v", id, byID[id])
		}
	}
	if byID[conformanceCaseCacheHit].Passed {
		t.Fatalf("cache_hit should fail when cached_tokens=0: %+v", byID[conformanceCaseCacheHit])
	}
}

func TestProviderSelfCheckConformanceChatStreamMissingDoneFailsRequired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if stream, _ := payload["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				// Intentionally omit [DONE]
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"4\"}}]}\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"4"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	handler, token, _ := newSelfCheckTestServer(t, domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: upstream.URL + "/v1/chat/completions", APIKeySource: "literal:test-secret",
		DefaultModel: "gpt-4o",
	})
	rec := selfCheckRequest(handler, "/__providers/p1/self-check/conformance", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var report conformanceReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Success {
		t.Fatalf("missing [DONE] must fail required suite: %s", rec.Body.String())
	}
	found := false
	for _, c := range report.Cases {
		if c.ID == conformanceCaseStream {
			found = true
			if c.Passed {
				t.Fatalf("stream_shape should fail: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("missing stream_shape case")
	}
}

func TestProviderSelfCheckConformanceUnauthorized(t *testing.T) {
	handler, _, _ := newSelfCheckTestServer(t, domain.Provider{
		ID: "p1", Name: "P1", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "http://127.0.0.1:9/v1/chat/completions", APIKeySource: "literal:x",
	})
	rec := selfCheckRequest(handler, "/__providers/p1/self-check/conformance", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
