package gateway

import (
	"context"
	"net/http"
	"os"
	"runtime"
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
// chat: a traceparent, its B3 counterpart, and the X-B3-* trio. The trace is
// fresh per request (the reference implementation does the same), so no state
// carries between turns.
func applyWorkBuddyTraceHeaders(request *http.Request) {
	trace := workBuddyRequestID()
	span := workBuddyRequestID()[:16]
	request.Header.Set("X-Trace-ID", trace)
	request.Header.Set("traceparent", "00-"+trace+"-"+span+"-01")
	request.Header.Set("b3", trace+"-"+span+"-1")
	request.Header.Set("X-B3-TraceId", trace)
	request.Header.Set("X-B3-SpanId", span)
	request.Header.Set("X-B3-Sampled", "1")
}

// applyWorkBuddyStainlessHeaders adds the x-stainless-* group the official CLI's
// requests carry, which the OpenAI Node SDK writes by itself.
//
// It is off by default. Those headers belong to the CLI identity: the captured
// desktop client sends none of them, and a request claiming an Electron desktop
// User-Agent while declaring a Node SDK and its runtime is a combination no real
// client produces — a newer fingerprint of its own rather than a better disguise.
// WORKBUDDY_DESKTOP_COMPAT=1 turns them on for an operator who has measured that
// their upstream wants them.
func applyWorkBuddyStainlessHeaders(request *http.Request) {
	if strings.TrimSpace(os.Getenv(workBuddyDesktopCompatEnv)) != "1" {
		return
	}
	request.Header.Set("X-Stainless-Lang", "js")
	request.Header.Set("X-Stainless-Package-Version", workBuddySDKVersion)
	request.Header.Set("X-Stainless-OS", workBuddyStainlessOS())
	request.Header.Set("X-Stainless-Arch", workBuddyStainlessArch())
	request.Header.Set("X-Stainless-Runtime", "node")
	request.Header.Set("X-Stainless-Runtime-Version", workBuddyNodeVersion)
	request.Header.Set("X-Stainless-Retry-Count", "0")
}

// workBuddyStainlessOS names this host's OS the way the Node SDK does.
func workBuddyStainlessOS() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "MacOS"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

// workBuddyStainlessArch names this host's architecture the way the Node SDK does.
func workBuddyStainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}
