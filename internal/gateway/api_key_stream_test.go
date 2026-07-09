package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestJSONHasKey(t *testing.T) {
	t.Parallel()
	if !jsonHasKey([]byte(`{"streamEnabled":false}`), "streamEnabled") {
		t.Fatal("expected streamEnabled key to be present")
	}
	if jsonHasKey([]byte(`{"enabled":true}`), "streamEnabled") {
		t.Fatal("expected streamEnabled key to be absent")
	}
}

func TestRejectIfStreamDisabledForKey(t *testing.T) {
	t.Parallel()
	server := &Server{}

	t.Run("allows when stream false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if server.rejectIfStreamDisabledForKey(rec, true, domain.APIKey{StreamEnabled: false}, false, domain.ProtocolOpenAIChat) {
			t.Fatal("non-stream request should not be rejected")
		}
	})

	t.Run("allows when key unmatched", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if server.rejectIfStreamDisabledForKey(rec, false, domain.APIKey{StreamEnabled: false}, true, domain.ProtocolOpenAIChat) {
			t.Fatal("unmatched key should not reject streaming")
		}
	})

	t.Run("allows when key stream enabled", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if server.rejectIfStreamDisabledForKey(rec, true, domain.APIKey{StreamEnabled: true}, true, domain.ProtocolOpenAIChat) {
			t.Fatal("stream-enabled key should allow streaming")
		}
	})

	t.Run("rejects when key stream disabled", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if !server.rejectIfStreamDisabledForKey(rec, true, domain.APIKey{StreamEnabled: false}, true, domain.ProtocolOpenAIChat) {
			t.Fatal("stream-disabled key should reject streaming")
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		errObj, _ := body["error"].(map[string]any)
		message, _ := errObj["message"].(string)
		if !strings.Contains(message, "该 API Key 已关闭流式响应") {
			t.Fatalf("message = %q, want API key stream disabled hint", message)
		}
	})
}
