package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The China build sends its X-Client-* identity on every request; the international build
// has no such injection layer and sends none. A gateway that mixes the two is claiming a
// client that does not exist (issue #257).
func TestWorkBuddyClientIdentityIsRealmIsolated(t *testing.T) {
	credential := &domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}

	cn := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/chat/completions", nil)
	applyWorkBuddyChatHeaders(cn, workBuddyCNSite, credential, workBuddyAgentTypeMain)
	if !workBuddyHeadersCarryDesktopIdentity(cn.Header, false) {
		version, platform, product := workBuddyClientIdentityOf(cn)
		t.Fatalf("CN chat head = %q/%q/%q, want the desktop identity", version, platform, product)
	}
	if got := cn.Header.Get("X-Client-Version"); got != workBuddyCNSite.clientVersion() {
		t.Fatalf("X-Client-Version = %q, want the account's client version", got)
	}

	intl := httptest.NewRequest(http.MethodPost, "https://www.workbuddy.ai/v2/chat/completions", nil)
	applyWorkBuddyChatHeaders(intl, workBuddyIntlSite, credential, workBuddyAgentTypeMain)
	if !workBuddyHeadersCarryDesktopIdentity(intl.Header, true) {
		version, platform, product := workBuddyClientIdentityOf(intl)
		t.Fatalf("intl chat head = %q/%q/%q, want none of the three", version, platform, product)
	}
}

// Account-facing calls carry the same identity on the CN build — the capture showed it on
// all 155 requests, not just chat.
func TestWorkBuddyAccountHeadersCarryIdentity(t *testing.T) {
	credential := domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}
	cn := workBuddyAccountHeaders(workBuddyCNSite, credential)
	for _, name := range []string{"X-Client-Version", "X-Client-Platform", "X-Client-Product"} {
		if cn[name] == "" {
			t.Fatalf("CN account head is missing %s: %v", name, cn)
		}
	}
	intl := workBuddyAccountHeaders(workBuddyIntlSite, credential)
	for _, name := range []string{"X-Client-Version", "X-Client-Platform", "X-Client-Product"} {
		if intl[name] != "" {
			t.Fatalf("intl account head carries %s = %q", name, intl[name])
		}
	}
}

// The mini-program speaks as itself: `5.11.3 / miniprogram / WorkBuddy` is a combination
// neither client produces, so the desktop values are replaced, not merely overridden.
func TestWorkBuddyMPCaliberReplacesDesktopIdentity(t *testing.T) {
	credential := domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}
	headers := workBuddyGrowthHeaders(workBuddyCNSite, credential, true)
	if got := headers["X-Client-Version"]; got != workBuddyMPVersion {
		t.Fatalf("X-Client-Version = %q, want the mp version %q", got, workBuddyMPVersion)
	}
	if got := headers["X-Client-Platform"]; got != workBuddyMPPlatform {
		t.Fatalf("X-Client-Platform = %q, want %q", got, workBuddyMPPlatform)
	}
	if got := headers["X-Client-Product"]; got != workBuddyMPProduct {
		t.Fatalf("X-Client-Product = %q, want %q", got, workBuddyMPProduct)
	}

	// A desktop-caliber growth call keeps the desktop identity.
	desktop := workBuddyGrowthHeaders(workBuddyCNSite, credential, false)
	if got := desktop["X-Client-Platform"]; got != workBuddyClientPlatform {
		t.Fatalf("desktop growth X-Client-Platform = %q", got)
	}
}

// The desktop report carries the identity too: /v2/report was in the captured set.
func TestWorkBuddyDesktopReportCarriesIdentity(t *testing.T) {
	credential := domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}
	cn := workBuddyDesktopReportHeaders(workBuddyCNSite, credential)
	if cn["X-Client-Platform"] != workBuddyClientPlatform || cn["X-Client-Product"] != workBuddyClientProduct {
		t.Fatalf("CN report head = %v", cn)
	}
	intl := workBuddyDesktopReportHeaders(workBuddyIntlSite, credential)
	if intl["X-Client-Platform"] != "" || intl["X-Client-Version"] != "" || intl["X-Client-Product"] != "" {
		t.Fatalf("intl report head carries the desktop identity: %v", intl)
	}
}

// Stripping removes all three and nothing else.
func TestStripWorkBuddyClientIdentity(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://www.workbuddy.cn/x", nil)
	request.Header.Set("X-Client-Version", "5.11.3")
	request.Header.Set("X-Client-Platform", "windows")
	request.Header.Set("X-Client-Product", "WorkBuddy")
	request.Header.Set("X-User-Id", "keep-me")
	stripWorkBuddyClientIdentity(request)
	if version, platform, product := workBuddyClientIdentityOf(request); version != "" || platform != "" || product != "" {
		t.Fatalf("not stripped: %q/%q/%q", version, platform, product)
	}
	if request.Header.Get("X-User-Id") != "keep-me" {
		t.Fatal("an unrelated header was removed")
	}
}

// The two builds are separate releases, so the CLI segment follows the realm: a CN UA
// reading CLI/2.165.0 claims a CLI the 5.11.3 build does not bundle.
func TestWorkBuddyCLIVersionFollowsRealm(t *testing.T) {
	if got := workBuddyCLIVersionFor(false); got != "2.176.0" {
		t.Fatalf("CN CLI = %q, want 2.176.0", got)
	}
	if got := workBuddyCLIVersionFor(true); got != "2.165.0" {
		t.Fatalf("intl CLI = %q, want 2.165.0", got)
	}
	// And it reaches the UA builders.
	cnUA := workBuddyCNSite.chatUserAgent()
	if !strings.Contains(cnUA, "CLI/2.176.0") || !strings.Contains(cnUA, "WorkBuddy/5.11.3") {
		t.Fatalf("CN UA = %q", cnUA)
	}
	intlUA := workBuddyIntlSite.chatUserAgent()
	if !strings.Contains(intlUA, "CLI/2.165.0") || !strings.Contains(intlUA, "WorkBuddy/5.9.4") {
		t.Fatalf("intl UA = %q", intlUA)
	}
}
