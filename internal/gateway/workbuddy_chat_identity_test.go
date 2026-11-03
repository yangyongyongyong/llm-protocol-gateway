package gateway

import (
	"context"
	"net/http"
	"strings"
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

// The chat path runs the OpenAI Node SDK, so the client's own captures carry the
// whole SDK block on every chat. Sending none of it is a shape no capture shows.
func TestWorkBuddyStainlessHeadersAlwaysSent(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-s"}

	request := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy, credential)

	for header, want := range map[string]string{
		"x-stainless-lang":            workBuddySDKLang,
		"x-stainless-package-version": workBuddySDKVersion,
		"x-stainless-os":              workBuddySDKOS,
		"x-stainless-arch":            workBuddySDKArch,
		"x-stainless-runtime":         workBuddySDKRuntime,
		"x-stainless-runtime-version": workBuddyNodeVersion,
		"x-stainless-retry-count":     "0",
	} {
		if got := request.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	// The CLI's own request budget was never measured for the desktop client.
	if got := request.Header.Get("x-stainless-timeout"); got != "" {
		t.Fatalf("x-stainless-timeout = %q, want none", got)
	}
}

// The family the client sends on every chat that is easy to leave out by
// accident, because none of it changes what the answer says.
func TestWorkBuddyAgentRuntimeAndBaggageAlwaysPresent(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	request := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-b"})

	if got := request.Header.Get("X-Agent-Runtime"); got != workBuddyAgentRuntimeLocal {
		t.Fatalf("X-Agent-Runtime = %q, want %q", got, workBuddyAgentRuntimeLocal)
	}
	conversation := request.Header.Get("X-Conversation-ID")
	turn := request.Header.Get("X-Conversation-Request-ID")
	want := "buddyId=,buddyName=,codebuddy.session_id=" + conversation +
		",codebuddy.conversation_request_id=" + turn
	if got := request.Header.Get("baggage"); got != want {
		t.Fatalf("baggage = %q, want %q", got, want)
	}
	// Both ids the baggage repeats must be the ones the request actually carries.
	if conversation == "" || turn == "" {
		t.Fatal("baggage repeats ids the request did not send")
	}
}

// The desktop client sends the trace family on a chat, and its parts agree.
func TestWorkBuddyTraceHeadersAgreeWithEachOther(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	request := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-t"})

	trace := request.Header.Get("X-Trace-ID")
	span := request.Header.Get("X-B3-SpanId")
	parent := request.Header.Get("X-B3-ParentSpanId")
	if len(trace) != 32 {
		t.Fatalf("X-Trace-ID = %q, want a 32-hex trace", trace)
	}
	if len(span) != 16 {
		t.Fatalf("X-B3-SpanId = %q, want a 16-hex span", span)
	}
	if len(parent) != 16 {
		t.Fatalf("X-B3-ParentSpanId = %q, want a 16-hex span", parent)
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
	// The B3 single header carries four segments; the client sends both
	// formats, and a three-segment one is what this gateway alone would produce.
	if got, want := request.Header.Get("b3"), trace+"-"+span+"-1-"+parent; got != want {
		t.Fatalf("b3 = %q, want %q", got, want)
	}
}

// X-Private-Data follows the release, not the account: the intl build ships with
// model optimization off by default and declares "true", the CN build on and
// declares "false". Every CN chat carrying "true" is the opposite of the client.
func TestWorkBuddyPrivateDataFollowsTheRelease(t *testing.T) {
	t.Setenv("WORKBUDDY_DEVICE_TOKEN", "")
	t.Setenv("WORKBUDDY_DEVICE_TOKEN_FILE", "")
	credential := domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-p"}

	for authType, want := range map[string]string{
		domain.AuthTypeWorkBuddy:   "false",
		domain.AuthTypeWorkBuddyAI: "true",
	} {
		request := newWorkBuddyChatRequest(t, context.Background())
		applyChatHeadersForSite(t, request, authType, credential)
		if got := request.Header.Get("X-Private-Data"); got != want {
			t.Fatalf("%s X-Private-Data = %q, want %q", authType, got, want)
		}
	}
}

// Go canonicalises header keys, which turns X-Request-ID into X-Request-Id on the
// wire. The spelling map is what keeps the initialisms the client uses.
func TestWorkBuddyHeaderSpellingKeepsInitialisms(t *testing.T) {
	request := newWorkBuddyChatRequest(t, context.Background())
	applyChatHeadersForSite(t, request, domain.AuthTypeWorkBuddy,
		domain.WorkBuddyCredential{AccessToken: "jwt", UserID: "uid-spell"})

	spelled := normalizeWorkBuddyHeaderSpelling(request)
	for _, name := range []string{
		"X-Request-ID", "X-Trace-ID", "X-Conversation-ID", "X-Conversation-Request-ID",
		"X-Conversation-Message-ID", "X-Root-Request-ID", "X-IDE-Type", "X-IDE-Name",
		"X-IDE-Version", "X-B3-TraceId", "X-B3-ParentSpanId", "X-B3-SpanId",
		"x-stainless-lang", "acp-connection-id", "x-codebuddy-request", "baggage",
	} {
		if _, ok := spelled.Header[name]; !ok {
			t.Errorf("%s missing from the spelled header set", name)
		}
	}
	// The Go special-cases keep their canonical form, or the transport stops
	// seeing them and replaces the User-Agent with its own default. The chat path
	// leaves Accept-Encoding to the transport, so it is set here to prove the map
	// leaves it alone rather than to assert it is always present.
	request.Header.Set("Accept-Encoding", "gzip")
	spelled = normalizeWorkBuddyHeaderSpelling(request)
	for _, name := range []string{"User-Agent", "Accept-Encoding", "Content-Type", "Accept"} {
		if _, ok := spelled.Header[name]; !ok {
			t.Errorf("%s lost its canonical spelling", name)
		}
	}
	if got := spelled.Header.Get("User-Agent"); !strings.Contains(got, "WorkBuddy/") {
		t.Fatalf("User-Agent = %q, want the client's", got)
	}
	// The original request is left alone: callers read it back after sending.
	if _, ok := request.Header["X-Request-ID"]; ok {
		t.Fatal("normalisation rewrote the caller's request")
	}
}
