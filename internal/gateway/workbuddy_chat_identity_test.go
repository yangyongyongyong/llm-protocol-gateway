package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// newWorkBuddyChatRequest builds the outbound request the chat path hands to
// applyWorkBuddyHeaders, with ctx carrying whatever the turn pinned.
func newWorkBuddyChatRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://copilot.tencent.com/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func applyChatHeadersForSite(t *testing.T, request *http.Request, authType string, credential domain.WorkBuddyCredential) {
	t.Helper()
	applyWorkBuddyHeaders(request, domain.Provider{AuthType: authType, WorkBuddy: &credential})
}

// A turn's aggregation id is made once per user send; every request and retry of
// that turn must carry the same one, or the upstream sees several unrelated
// turns where the client sent one.
func TestWorkBuddyTurnRequestIDIsReusedAcrossAttempts(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	t.Setenv(workBuddyDesktopCompatEnv, "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-turn"}

	turnID := workBuddyRequestID()
	ctx := withWorkBuddyTurnRequestID(context.Background(), turnID)

	for attempt := 0; attempt < 3; attempt++ {
		request := newWorkBuddyChatRequest(t, ctx)
		applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy, credential)
		if got := request.Header.Get("X-Conversation-Request-ID"); got != turnID {
			t.Fatalf("attempt %d X-Conversation-Request-ID = %q, want the turn's %q", attempt, got, turnID)
		}
		if got := request.Header.Get("X-Root-Request-ID"); got != turnID {
			t.Fatalf("attempt %d X-Root-Request-ID = %q, want the turn's %q", attempt, got, turnID)
		}
		// The message id is per request, so attempts must differ here.
		if got := request.Header.Get("X-Request-ID"); len(got) != 32 {
			t.Fatalf("attempt %d X-Request-ID = %q, want a 32-hex id", attempt, got)
		}
	}
}

// Without a pinned turn id the builder still produces a usable request, which is
// the per-request behaviour a caller that never pins one gets.
func TestWorkBuddyTurnRequestIDFallsBackPerRequest(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	t.Setenv(workBuddyDesktopCompatEnv, "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-anon"}

	first := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, first, domain.AuthTypeWorkBuddy, credential)
	second := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, second, domain.AuthTypeWorkBuddy, credential)

	if first.Header.Get("X-Conversation-Request-ID") == second.Header.Get("X-Conversation-Request-ID") {
		t.Fatal("two unpinned requests shared an aggregation id")
	}
}

// Token or error, always one — on both builds, and never both at once.
func TestWorkBuddyDeviceTokenOrErrorAlwaysOne(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	t.Setenv(workBuddyDesktopCompatEnv, "")

	withoutToken := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, withoutToken, domain.AuthTypeWorkBuddy,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-a"})
	if got := withoutToken.Header.Get("X-Device-Token-Error"); got != workBuddyDeviceTokenError {
		t.Fatalf("X-Device-Token-Error = %q, want %q", got, workBuddyDeviceTokenError)
	}
	if got := withoutToken.Header.Get("X-Device-Token"); got != "" {
		t.Fatalf("X-Device-Token = %q, want none", got)
	}

	withToken := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, withToken, domain.AuthTypeWorkBuddyAI,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-b", DeviceToken: "dev-secret"})
	if got := withToken.Header.Get("X-Device-Token"); got != "dev-secret" {
		t.Fatalf("X-Device-Token = %q, want the account's", got)
	}
	if got := withToken.Header.Get("X-Device-Token-Error"); got != "" {
		t.Fatalf("X-Device-Token-Error = %q, want none when a token was sent", got)
	}
}

// The SDK fingerprint is the CLI identity's, not the desktop client's: it is only
// sent when the operator opts in, on either build.
func TestWorkBuddyStainlessHeadersAreOptIn(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-s"}

	t.Setenv(workBuddyDesktopCompatEnv, "")
	off := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, off, domain.AuthTypeWorkBuddy, credential)
	for _, header := range []string{
		"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS",
		"X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count",
	} {
		if got := off.Header.Get(header); got != "" {
			t.Fatalf("%s = %q, want none by default", header, got)
		}
	}

	t.Setenv(workBuddyDesktopCompatEnv, "1")
	on := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, on, domain.AuthTypeWorkBuddy, credential)
	for header, want := range map[string]string{
		"X-Stainless-Lang":            "js",
		"X-Stainless-Package-Version": workBuddySDKVersion,
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Retry-Count":     "0",
	} {
		if got := on.Header.Get(header); got != want {
			t.Fatalf("opted in %s = %q, want %q", header, got, want)
		}
	}
	// The CLI's own request budget was never measured for the desktop client.
	if got := on.Header.Get("X-Stainless-Timeout"); got != "" {
		t.Fatalf("X-Stainless-Timeout = %q, want none", got)
	}
}

// The desktop client sends the trace family on a chat, and its parts agree.
func TestWorkBuddyTraceHeadersAgreeWithEachOther(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	t.Setenv(workBuddyDesktopCompatEnv, "")
	request := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-t"})

	trace := request.Header.Get("X-Trace-ID")
	span := request.Header.Get("X-B3-SpanId")
	if len(trace) != 32 {
		t.Fatalf("X-Trace-ID = %q, want a 32-hex trace", trace)
	}
	if len(span) != 16 {
		t.Fatalf("X-B3-SpanId = %q, want a 16-hex span", span)
	}
	if got, want := request.Header.Get("X-B3-TraceId"), trace; got != want {
		t.Fatalf("X-B3-TraceId = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("X-B3-Sampled"), "1"; got != want {
		t.Fatalf("X-B3-Sampled = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("traceparent"), "00-"+trace+"-"+span+"-01"; got != want {
		t.Fatalf("traceparent = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("b3"), trace+"-"+span+"-1"; got != want {
		t.Fatalf("b3 = %q, want %q", got, want)
	}
}

// Both builds answer for their own data usage the same way; the true/false split
// belongs to the CLI identity.
func TestWorkBuddyPrivateDataIsTrueOnBothBuilds(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	t.Setenv(workBuddyDesktopCompatEnv, "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-p"}

	for _, authType := range []string{domain.AuthTypeWorkBuddy, domain.AuthTypeWorkBuddyAI} {
		request := newWorkBuddyChatRequest(t, context.Background())
		applyChatHeadersForSite(t, request, authType, credential)
		if got := request.Header.Get("X-Private-Data"); got != "true" {
			t.Fatalf("%s X-Private-Data = %q, want true", authType, got)
		}
		if got := request.Header.Get("X-Agent-Runtime"); got != "" {
			t.Fatalf("%s X-Agent-Runtime = %q, want none on the desktop identity", authType, got)
		}
	}
}
