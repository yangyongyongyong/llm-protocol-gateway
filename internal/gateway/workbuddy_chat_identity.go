package gateway

import (
	"context"
	"net/http"
	"strings"
)

// The chat identity WorkBuddy's desktop client presents, gathered here so the
// pieces that are optional or realm-dependent sit next to the reason they exist.
//
// The shape follows an actively maintained reference gateway
// (ardeyouxipianyi/workbuddy2api-hub, wb_identity.py) whose constants come from
// captured traffic: the intl desktop build 5.8.4 (2026-10-16) and the CN build
// 5.8.3 (2026-10-18), each with the built-in CLI 2.161.0.

const (
	// workBuddyDeviceTokenErrorEnv opts a request into the OpenAI Node SDK's
	// x-stainless-* fingerprint (see applyWorkBuddyStainlessHeaders).
	workBuddyDesktopCompatEnv = "WORKBUDDY_DESKTOP_COMPAT"

	// workBuddyDeviceTokenError is what the desktop client sends in place of a
	// device token its security SDK could not produce. Captured clients always
	// send one of the two — the token, or this error — so a request carrying
	// neither is in a state the real client never reaches.
	workBuddyDeviceTokenError = "sdk_not_initialized"

	// The OpenAI Node SDK version and Node runtime the official CLI's requests
	// declare through x-stainless-*.
	workBuddySDKVersion  = "6.25.0"
	workBuddyNodeVersion = "v22.21.1"
)

// workBuddyTurnRequestIDKey carries one turn's X-Conversation-Request-ID.
type workBuddyTurnRequestIDKey struct{}

// withWorkBuddyTurnRequestID pins a turn's request id to ctx. The official
// client makes this id once, when a user send starts, and reuses it for every
// request of that turn — including retries and the failover to another account —
// while the message id is made fresh per request. Generating it inside the
// header builder would give every retry a new aggregation id and break the turn
// into several unrelated ones upstream.
func withWorkBuddyTurnRequestID(ctx context.Context, id string) context.Context {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, workBuddyTurnRequestIDKey{}, trimmed)
}

// workBuddyTurnRequestIDFrom is the id withWorkBuddyTurnRequestID pinned, or ""
// when the caller did not pin one.
func workBuddyTurnRequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, ok := ctx.Value(workBuddyTurnRequestIDKey{}).(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(id)
}

// applyWorkBuddyTraceHeaders adds the trace family the desktop client sends on a
// chat: the X-B3-* quartet (parent span included), the B3 single header with all
// four segments, and the W3C traceparent. The client sends both formats on every
// chat, so a request carrying only the trio is a shape it never produces.
func applyWorkBuddyTraceHeaders(request *http.Request) {
	trace := workBuddyRequestID()
	span := workBuddyRequestID()[:16]
	// No real span tree exists here; the upstream cannot verify parentage, but a
	// span without a parent is distinguishable from one with — so one is made.
	parent := workBuddyRequestID()[:16]
	request.Header.Set("X-Trace-ID", trace)
	request.Header.Set("traceparent", "00-"+trace+"-"+span+"-01")
	request.Header.Set("b3", trace+"-"+span+"-1-"+parent)
	request.Header.Set("X-B3-TraceId", trace)
	request.Header.Set("X-B3-ParentSpanId", parent)
	request.Header.Set("X-B3-SpanId", span)
	request.Header.Set("X-B3-Sampled", "1")
}

// applyWorkBuddyStainlessHeaders adds the SDK block the official chat carries.
//
// The chat path runs the OpenAI Node SDK, so the client's own captures have all
// of these; they are the client's constants rather than this host's, which is
// why they are fixed (see workBuddySDKOS / workBuddySDKArch).
func applyWorkBuddyStainlessHeaders(request *http.Request) {
	request.Header.Set("x-stainless-lang", workBuddySDKLang)
	request.Header.Set("x-stainless-package-version", workBuddySDKVersion)
	request.Header.Set("x-stainless-os", workBuddySDKOS)
	request.Header.Set("x-stainless-arch", workBuddySDKArch)
	request.Header.Set("x-stainless-runtime", workBuddySDKRuntime)
	request.Header.Set("x-stainless-runtime-version", workBuddyNodeVersion)
	request.Header.Set("x-stainless-retry-count", "0")
}
