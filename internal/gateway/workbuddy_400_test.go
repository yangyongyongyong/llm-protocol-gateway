package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// sendWorkBuddyOne400 sends one chat whose upstream answers 400 with body and
// returns the response sendWorkBuddyChat handed back and what was read from it.
func sendWorkBuddyOne400(t *testing.T, body string) (*http.Response, string, int) {
	t.Helper()
	sent := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		sent++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	send := func(_ domain.Provider, b []byte) (*http.Response, error) {
		request, err := http.NewRequest(http.MethodPost, upstream.URL, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(request)
	}
	chat := []byte(`{"model":"glm-5.3","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	response, err := server.sendWorkBuddyChat(domain.Provider{ID: "wb1"}, "glm-5.3", chat, nil, send, false)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	got, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("the restored body did not read back: %v", readErr)
	}
	response.Body.Close()
	return response, string(got), sent
}

// TestSendWorkBuddyChatKeepsUnmatched400Body: a 400 that is none of the
// refusals the send path repairs is the upstream's answer to the request, and
// the client is given that body as it arrived — not an empty one.
func TestSendWorkBuddyChatKeepsUnmatched400Body(t *testing.T) {
	const body = `{"error":{"message":"tools[0].type is illegal"}}`
	response, got, sent := sendWorkBuddyOne400(t, body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if got != body {
		t.Fatalf("client got %q, want the upstream's body %q", got, body)
	}
	if response.ContentLength != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", response.ContentLength, len(body))
	}
	if sent != 1 {
		t.Fatalf("sent %d times, want the bad request not retried", sent)
	}
}

// TestSendWorkBuddyChatQuota400StillFailsOver: the body a 400 carries is what
// classifyProviderFailover reads, so a quota 400 that is not rewritten must
// still reach the classifier and be failover-worthy.
func TestSendWorkBuddyChatQuota400StillFailsOver(t *testing.T) {
	const body = `{"code":40001,"message":"You exceeded your current quota, please check your plan"}`
	response, got, _ := sendWorkBuddyOne400(t, body)
	if got != body {
		t.Fatalf("client got %q, want the upstream's body %q", got, body)
	}
	class, reason := classifyProviderFailover(response.StatusCode, []byte(got), nil, nil)
	if class != failoverHard {
		t.Fatalf("class = %v reason = %q, want a quota failure worth failing over", class, reason)
	}
	if !shouldFailoverProvider(response.StatusCode, []byte(got), nil) {
		t.Fatal("a quota 400 did not advance the backup chain")
	}
	// The two controls: an ordinary bad request stays the client's own, and
	// the prompt refusal keeps its own class.
	if class, _ := classifyProviderFailover(http.StatusBadRequest, []byte(`{"error":{"message":"tools[0].type is illegal"}}`), nil, nil); class != failoverNone {
		t.Fatalf("a bad schema class = %v, want none", class)
	}
	refusal := []byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`)
	if class, reason := classifyProviderFailover(http.StatusBadRequest, refusal, nil, nil); class != failoverPromptRefusal || reason != "prompt_refused" {
		t.Fatalf("class = %v reason = %q, want the prompt refusal", class, reason)
	}
	if !strings.Contains(string(refusal), "unapproved channel") {
		t.Fatal("fixture drifted")
	}
}

// TestSendWorkBuddyChatRestored400IsTheOriginalBytes: the restored body is
// byte-for-byte the upstream's, JSON-shaped error or not.
func TestSendWorkBuddyChatRestored400IsTheOriginalBytes(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"context length exceeded: 200000 > 128000"}}`,
		`{"code":11121,"msg":"model not found"}`,
		`not json at all`,
	} {
		_, got, _ := sendWorkBuddyOne400(t, body)
		if got != body {
			t.Fatalf("body %q reached the client as %q", body, got)
		}
	}
}
