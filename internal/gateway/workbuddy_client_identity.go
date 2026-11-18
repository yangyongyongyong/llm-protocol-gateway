package gateway

import (
	"net/http"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The X-Client-* identity the 5.11.3 China desktop client attaches to every request.
//
// Measured on a real CN desktop 5.11.3 capture (2026-11-18): the build injects three
// headers in its shared HTTP layer (app.asar `proxy-env2` /
// `registerOutboundHeaderProvider`), by origin, on **155 of 155** API requests —
// including `/v2/chat/completions` and `/v3/config`, which earlier captures had read as
// "header set unchanged". So they are not an RPC-only addition; a gateway that sends some
// of them on some paths is producing a combination the real client never sends
// (issue #257, upstream `injectClientIdentityHeaders`).
//
// Two rules decide the shape here:
//
//   - **Realm-isolated.** The China build sends them; the international distribution is
//     still on 5.9.4 and has no such injection layer, so sending them there would be
//     adding a fingerprint the official client does not have.
//   - **The mini-program caliber must not carry them.** Its requests are
//     `2.4.0 / mp-weixin / workbuddy-mp` (see workbuddy_growth.go), and a request that
//     inherited the desktop values would read `5.11.3 / miniprogram / WorkBuddy` — a
//     fourth combination that is neither client.
//
// `X-Device-Token` is deliberately still not injected: its value only the client's
// Turing SDK can produce, so it travels only when an account's credential carries one.
const (
	// workBuddyClientPlatform / workBuddyClientProduct are the two fixed segments of the
	// X-Client-* block. The real CN samples are windows / WorkBuddy throughout (a mac
	// build would say macos); the version segment follows the account's client version.
	workBuddyClientPlatform = "windows"
	workBuddyClientProduct  = "WorkBuddy"
)

// applyWorkBuddyClientIdentity sets the three headers on a desktop-caliber request.
//
// A no-op for the international build, which has no such layer.
func applyWorkBuddyClientIdentity(request *http.Request, site workBuddySite) {
	if request == nil || site.intl {
		return
	}
	request.Header.Set("X-Client-Version", site.clientVersion())
	request.Header.Set("X-Client-Platform", workBuddyClientPlatform)
	request.Header.Set("X-Client-Product", workBuddyClientProduct)
}

// stripWorkBuddyClientIdentity removes them, for a request that speaks as the
// mini-program instead.
//
// Needed because the growth paths apply the desktop head first (a CN account gets the
// three headers) and then override X-Client-Platform with the mp value; without this the
// request would mix the two clients.
func stripWorkBuddyClientIdentity(request *http.Request) {
	if request == nil {
		return
	}
	request.Header.Del("X-Client-Version")
	request.Header.Del("X-Client-Platform")
	request.Header.Del("X-Client-Product")
}

// workBuddyClientIdentityOf reports the three values a request currently carries, for
// tests and diagnostics.
func workBuddyClientIdentityOf(request *http.Request) (string, string, string) {
	if request == nil {
		return "", "", ""
	}
	return request.Header.Get("X-Client-Version"),
		request.Header.Get("X-Client-Platform"),
		request.Header.Get("X-Client-Product")
}

// applyWorkBuddyMPClientIdentity sets the mini-program's own three headers.
//
// Its values are the mp client's, never the desktop ones — this is the counterpart of
// stripWorkBuddyClientIdentity above.
func applyWorkBuddyMPClientIdentity(request *http.Request, credential domain.WorkBuddyCredential) {
	if request == nil {
		return
	}
	stripWorkBuddyClientIdentity(request)
	request.Header.Set("X-Client-Version", workBuddyMPVersion)
	request.Header.Set("X-Client-Platform", workBuddyMPWebPlatform)
	request.Header.Set("X-Client-Product", workBuddyMPProduct)
}

// workBuddyIsMPCaliber reports whether a growth request speaks as the mini-program.
func workBuddyIsMPCaliber(mp bool) bool { return mp }

// workBuddyHeadersCarryDesktopIdentity is the assertion the golden tests use: a desktop
// head on the CN build carries all three, the intl head carries none.
func workBuddyHeadersCarryDesktopIdentity(header http.Header, intl bool) bool {
	version := strings.TrimSpace(header.Get("X-Client-Version"))
	platform := strings.TrimSpace(header.Get("X-Client-Platform"))
	product := strings.TrimSpace(header.Get("X-Client-Product"))
	present := version != "" && platform != "" && product != ""
	if intl {
		return !present
	}
	return present && platform == workBuddyClientPlatform && product == workBuddyClientProduct
}
