package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The campaign pages are served to the app, and the app is not the CLI: its own
// version goes on the request, and the six machine headers travel together or
// not at all.
//
// The identity is seeded rather than read from a helper: which binary answers is
// not what this test is about, and the helper cannot run on every platform.
func TestQoderGlobalCampaignCarriesTheAppIdentity(t *testing.T) {
	const uid = "uid-headers"
	machine := map[string]string{
		"Cosy-MachineId":       qoderDeriveID(uid, "machine"),
		"Cosy-MachineToken":    "mt-seeded",
		"Cosy-MachineType":     "mt-type",
		"Cosy-MachineCode":     "mt-code",
		"Cosy-MachineOS":       qoderMachineOS(),
		"Cosy-MachineHostname": "DESKTOP-SEEDED",
	}
	qoderMachineMu.Lock()
	qoderMachineIdentities[uid] = qoderMachineCacheEntry{headers: machine, at: time.Now()}
	qoderMachineMu.Unlock()
	t.Cleanup(func() {
		qoderMachineMu.Lock()
		delete(qoderMachineIdentities, uid)
		qoderMachineMu.Unlock()
	})

	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMED", "startAt": time.Now().Add(-time.Hour).Unix(), "endAt": time.Now().Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, "", 0))
	site := qoderCheckinSite(server)
	site.AuthType = domain.AuthTypeQoderPAT
	if result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, site,
		domain.QoderCredential{UID: uid, DeviceToken: "dt"}, time.Now()); result.Outcome != domain.CheckinDone {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	listCall := (*seen)[0]
	// The app's version, not the CLI's: the campaign pages are the app's.
	if got := listCall.sent.Get("Cosy-Version"); got != qoderDesktopVersion {
		t.Fatalf("Cosy-Version = %q, want the app's %q", got, qoderDesktopVersion)
	}
	if got := listCall.sent.Get("Cosy-ClientType"); got != qoderClientTypeDesktop {
		t.Fatalf("Cosy-ClientType = %q, want the app's %q", got, qoderClientTypeDesktop)
	}
	// A listing is a read: it posts no body and declares no content type.
	if listCall.hasBody {
		t.Fatal("a campaign listing must not post a body")
	}
	if got := listCall.sent.Get("Content-Type"); got != "" {
		t.Fatalf("a campaign listing declared Content-Type %q", got)
	}
	// The six machine headers travel as one identity.
	for _, header := range []string{
		"Cosy-MachineId", "Cosy-MachineToken", "Cosy-MachineType",
		"Cosy-MachineCode", "Cosy-MachineOS", "Cosy-MachineHostname",
	} {
		if strings.TrimSpace(listCall.sent.Get(header)) == "" {
			t.Errorf("%s is missing from the campaign request", header)
		}
	}
	if got := listCall.sent.Get("Cosy-MachineId"); got != qoderDeriveID(uid, "machine") {
		t.Errorf("Cosy-MachineId = %q, want the account's derived id", got)
	}
	// A claim is the one call that posts.
	if len(*seen) > 1 && !(*seen)[1].hasBody {
		t.Error("a claim posts an empty JSON body")
	}
}

// A pending claim with a body does carry a content type: the only campaign call
// that has one.
func TestQoderCampaignClaimDeclaresItsContentType(t *testing.T) {
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMABLE", "startAt": time.Now().Add(-time.Hour).Unix(), "endAt": time.Now().Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	claimed := qoderCampaignJSON(t, map[string]any{"status": "CLAIMED", "benefit": map[string]any{"amount": 100}})
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, claimed, 0))
	site := qoderCheckinSite(server)
	site.AuthType = domain.AuthTypeQoderCN
	if _, _ = qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, site,
		domain.QoderCredential{UID: "uid-cn", DeviceToken: "dt"}, time.Now()); len(*seen) != 2 {
		t.Fatalf("expected a listing and a claim, saw %d", len(*seen))
	}
	claim := (*seen)[1]
	if got := claim.sent.Get("Content-Type"); got != "application/json" {
		t.Fatalf("claim Content-Type = %q", got)
	}
	// Qoder CN's pages are served without a machine identity.
	for _, header := range []string{"Cosy-MachineId", "Cosy-MachineToken", "Cosy-MachineType"} {
		if got := claim.sent.Get(header); got != "" {
			t.Errorf("Qoder CN must not send %s, got %q", header, got)
		}
	}
	// But it does claim to be the app.
	if got := claim.sent.Get("Cosy-Version"); got != qoderDesktopVersion {
		t.Fatalf("Cosy-Version = %q", got)
	}
}

// The plain account identity is a different client from the app: no Cosy headers
// at all, and the ids an account endpoint is served by.
func TestQoderAccountHeadersAreThePlainIdentity(t *testing.T) {
	credential := domain.QoderCredential{UID: "uid-plain"}
	headers := qoderAccountHeaders(qoderGlobalSite, credential)
	if got := headers["X-Machine-ID"]; got != qoderDeriveID("uid-plain", "machine") {
		t.Errorf("X-Machine-ID = %q", got)
	}
	if got := headers["X-Session-ID"]; got != qoderDeriveID("uid-plain", "session") {
		t.Errorf("X-Session-ID = %q", got)
	}
	if got := headers["User-Agent"]; got != "Qoder/"+qoderCOSYVersion {
		t.Errorf("international user agent = %q", got)
	}
	if got := qoderAccountHeaders(qoderCNSite, credential)["User-Agent"]; got != "QoderWork/"+qoderCOSYVersion {
		t.Errorf("domestic user agent = %q", got)
	}
	if got := headers["Origin"]; got != "https://qoder.com" {
		t.Errorf("Origin = %q", got)
	}
	if got := headers["Referer"]; got != "https://qoder.com/" {
		t.Errorf("Referer = %q", got)
	}
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "cosy-") {
			t.Errorf("the account identity carries %s, which belongs to the app's", key)
		}
	}
	// Two calls must not look like one request replayed.
	if qoderAccountHeaders(qoderGlobalSite, credential)["X-Request-ID"] == headers["X-Request-ID"] {
		t.Error("X-Request-ID has to be fresh per request")
	}
}
