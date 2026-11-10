package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// qoderCheckinSite points a site's account host at a test server. Qoder CN is
// the shape these tests use: its campaigns pages take the device token and ask
// for no machine identity, which only an installed Qoder can produce.
func qoderCheckinSite(server *httptest.Server) qoderSite {
	return qoderSite{
		AuthType:           domain.AuthTypeQoderCN,
		Name:               "Qoder CN",
		Web:                server.URL,
		OpenAPI:            server.URL,
		API:                server.URL,
		ClientID:           "test-client",
		DeviceChatFallback: true,
	}
}

// qoderCampaignSeen is one request the campaigns pages received.
type qoderCampaignSeen struct {
	method  string
	path    string
	bearer  string
	sent    http.Header
	hasBody bool
}

// qoderCampaignServer answers the campaigns pages with what the handler returns
// and records every request.
func qoderCampaignServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]qoderCampaignSeen) {
	t.Helper()
	seen := []qoderCampaignSeen{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, qoderCampaignSeen{
			method:  r.Method,
			path:    r.URL.Path,
			bearer:  strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			sent:    r.Header.Clone(),
			hasBody: r.ContentLength > 0,
		})
		w.Header().Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

// qoderCampaignHandler answers the list with `list` and a claim with `claim`.
func qoderCampaignHandler(list, claim string, claimStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == qoderCampaignsPath:
			_, _ = w.Write([]byte(list))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			if claimStatus != 0 && claimStatus != http.StatusOK {
				w.WriteHeader(claimStatus)
			}
			_, _ = w.Write([]byte(claim))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"unexpected path"}`))
		}
	}
}

func qoderCampaignJSON(t *testing.T, campaigns ...any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"campaigns": campaigns})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The day's campaign is claimed once, with the device token and the same client
// headers Qoder's own desktop sends.
func TestQoderCheckinClaimsTheDailyCampaign(t *testing.T) {
	now := time.Now()
	list := qoderCampaignJSON(t,
		// A non-daily campaign must be left alone: only CLAIM_BENEFIT is the
		// day's credits.
		map[string]any{"campaignId": "invite", "actionType": "INVITE_FRIEND", "claimStatus": "CLAIMABLE", "benefit": map[string]any{"amount": 500}},
		map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMABLE",
			"startAt": now.Add(-time.Hour).Unix(), "endAt": now.Add(3 * time.Hour).Unix(), "benefit": map[string]any{"amount": 100}},
	)
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, `{"status":"CLAIMED","benefit":{"amount":120}}`, 0))
	credential := domain.QoderCredential{UID: "uid-1", DeviceToken: "dt-1", DeviceRefresh: "drt-1"}
	result, renewed := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server), credential, now)
	if result.Outcome != domain.CheckinClaimed {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	// The claim's own amount wins over the campaign's: it is what landed.
	if result.Credit != 120 {
		t.Fatalf("credit = %v", result.Credit)
	}
	if result.Until.IsZero() {
		t.Fatal("a claimed campaign's window end must be kept, so the day is re-read after it")
	}
	if result.AccountKey != domain.AuthTypeQoderCN+"|uid-1" {
		t.Fatalf("account key = %q", result.AccountKey)
	}
	if len(*seen) != 2 {
		t.Fatalf("expected a list and a claim, saw %d calls", len(*seen))
	}
	listCall, claimCall := (*seen)[0], (*seen)[1]
	if listCall.method != http.MethodGet || claimCall.method != http.MethodPost {
		t.Fatalf("methods = %s / %s", listCall.method, claimCall.method)
	}
	if claimCall.path != qoderCampaignsPath+"/daily/claim" {
		t.Fatalf("claim path = %q", claimCall.path)
	}
	for index, call := range []qoderCampaignSeen{listCall, claimCall} {
		if call.bearer != "dt-1" {
			t.Fatalf("call %d bearer = %q, want the device token", index, call.bearer)
		}
		if got := call.sent.Get("Cosy-ClientType"); got != "10" {
			t.Fatalf("call %d Cosy-ClientType = %q", index, got)
		}
		if got := call.sent.Get("User-Agent"); got != "Qoder" {
			t.Fatalf("call %d User-Agent = %q", index, got)
		}
	}
	// Qoder CN asks no machine identity — only the international site does.
	if listCall.sent.Get("Cosy-MachineToken") != "" {
		t.Fatal("Qoder CN must not send a machine identity")
	}
	if !claimCall.hasBody {
		t.Fatal("a claim posts an empty JSON body, not nothing")
	}
	if renewed.DeviceToken != credential.DeviceToken {
		t.Fatal("nothing rotated; the credential must come back as it was")
	}
}

// Qoder wraps some replies in {data}; a claim read only at the top level would
// answer "unknown" for a grant that landed.
func TestQoderCheckinReadsAWrappedClaim(t *testing.T) {
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMABLE", "benefit": map[string]any{"amount": 100}})
	server, _ := qoderCampaignServer(t, qoderCampaignHandler(list, `{"data":{"status":"CLAIMED","benefit":{"amount":88}}}`, 0))
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
		domain.QoderCredential{UID: "u", DeviceToken: "dt"}, time.Now())
	if result.Outcome != domain.CheckinClaimed || result.Credit != 88 {
		t.Fatalf("result = %+v", result)
	}
}

func TestQoderCheckinAnswers(t *testing.T) {
	now := time.Now()
	claimedIn := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMED", "startAt": now.Add(-time.Hour).Unix(), "endAt": now.Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	claimable := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMABLE", "startAt": now.Add(-time.Hour).Unix(), "endAt": now.Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	cases := []struct {
		name   string
		list   string
		claim  string
		status int
		want   string
	}{
		{"already in", claimedIn, "", 0, domain.CheckinDone},
		{"no daily campaign", qoderCampaignJSON(t, map[string]any{"campaignId": "x", "actionType": "SURVEY", "claimStatus": "CLAIMABLE"}), "", 0, domain.CheckinInactive},
		{"nothing offered yet", qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT", "claimStatus": "NOT_AVAILABLE"}), "", 0, domain.CheckinFailed},
		{"a claim the vendor refused", claimable, `{"status":"SOLD_OUT"}`, 0, domain.CheckinFailed},
		{"a claim that errored", claimable, `{"message":"上游炸了"}`, http.StatusInternalServerError, domain.CheckinFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := qoderCampaignServer(t, qoderCampaignHandler(tc.list, tc.claim, tc.status))
			result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
				domain.QoderCredential{UID: "u", DeviceToken: "dt"}, now)
			if result.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q (%s)", result.Outcome, tc.want, result.Message)
			}
		})
	}
}

// A device token Qoder has stopped accepting is rotated once and the page asked
// again — and the new pair has to come back to the caller, because the refresh
// half of it is good for exactly one more use.
func TestQoderCheckinRotatesARefusedDeviceToken(t *testing.T) {
	now := time.Now()
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMED", "startAt": now.Add(-time.Hour).Unix(), "endAt": now.Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	bearers := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case qoderCampaignsPath:
			bearers = append(bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if len(bearers) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"token invalid"}`))
				return
			}
			_, _ = w.Write([]byte(list))
		case qoderDeviceRefreshPath:
			_, _ = w.Write([]byte(`{"token":"dt-2","refresh_token":"drt-2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	stored := domain.QoderCredential{UID: "u", DeviceToken: "dt-1", DeviceRefresh: "drt-1"}
	gatewayServer, _ := checkinTestServer(domain.Provider{ID: "q1", AuthType: domain.AuthTypeQoderCN, QoderPAT: &stored})
	result, renewed := qoderCheckin(context.Background(), gatewayServer, domain.Provider{ID: "q1"}, qoderCheckinSite(server), stored, now)
	if result.Outcome != domain.CheckinDone {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	if len(bearers) != 2 || bearers[1] != "dt-2" {
		t.Fatalf("bearers seen = %v", bearers)
	}
	if renewed.DeviceToken != "dt-2" || renewed.DeviceRefresh != "drt-2" {
		t.Fatalf("rotated credential = %+v", renewed)
	}
}

// A rotation that Qoder itself refuses leaves the check-in reporting the original
// refusal, not the refresh's own complaint: the user asked for credits.
func TestQoderCheckinWithoutAnyToken(t *testing.T) {
	server, _ := qoderCampaignServer(t, qoderCampaignHandler(`{"campaigns":[]}`, "", 0))
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
		domain.QoderCredential{UID: "u"}, time.Now())
	if result.Outcome != domain.CheckinFailed || !strings.Contains(result.Message, "令牌") {
		t.Fatalf("result = %+v", result)
	}
}

// A pasted personal-access-token account has no device token; Qoder accepts the
// job token the exchange gave on these pages, and there is nothing to rotate.
func TestQoderCheckinUsesAPatAccountJobToken(t *testing.T) {
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMABLE", "benefit": map[string]any{"amount": 100}})
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, `{"status":"CLAIMED","benefit":{"amount":100}}`, 0))
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
		domain.QoderCredential{UID: "u", SignInMode: "pat", AccessToken: "jt-1"}, time.Now())
	if result.Outcome != domain.CheckinClaimed {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	for index, call := range *seen {
		if call.bearer != "jt-1" {
			t.Fatalf("call %d bearer = %q, want the job token", index, call.bearer)
		}
	}
}

// A headless server has no Qoder desktop install to read a machine identity
// from. The request still goes out — the vendor answers one without the machine
// headers — and if the list comes back empty the result says why, instead of
// reporting a plain "nothing to claim" the operator cannot act on.
func TestQoderGlobalCheckinAsksWithoutTheLocalRuntime(t *testing.T) {
	t.Setenv("GATEWAY_QODER_RUNTIME_INFO", filepath.Join(t.TempDir(), "not-here"))
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(`{"campaigns":[]}`, "", 0))
	site := qoderCheckinSite(server)
	site.AuthType = domain.AuthTypeQoderPAT
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, site,
		domain.QoderCredential{UID: "u", DeviceToken: "dt"}, time.Now())
	if len(*seen) == 0 {
		t.Fatal("the request must still be made: the vendor answers one without a machine identity")
	}
	call := (*seen)[0]
	if got := call.sent.Get("Cosy-ClientType"); got != qoderClientTypeDesktop {
		t.Fatalf("Cosy-ClientType = %q", got)
	}
	if got := call.sent.Get("Cosy-MachineToken"); got != "" {
		t.Fatalf("no machine identity exists here, so none may be invented: %q", got)
	}
	if result.Outcome != domain.CheckinInactive {
		t.Fatalf("outcome = %q", result.Outcome)
	}
	if !strings.Contains(result.Message, "设备身份") {
		t.Fatalf("an empty list must explain itself: %q", result.Message)
	}
}

// A helper that answers is enough: the identity goes on the international site's
// requests and nothing else.
func TestQoderGlobalCheckinSendsTheMachineIdentity(t *testing.T) {
	fake := fakeQoderRuntimeInfo(t)
	t.Setenv("GATEWAY_QODER_RUNTIME_INFO", fake)
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMED", "startAt": time.Now().Add(-time.Hour).Unix(), "endAt": time.Now().Add(time.Hour).Unix(),
		"benefit": map[string]any{"amount": 100}})
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, "", 0))
	site := qoderCheckinSite(server)
	site.AuthType = domain.AuthTypeQoderPAT
	// Each uid is read once per hour, so a test that already cached one would
	// never run the helper again.
	delete(qoderMachineIdentities, "uid-machine")
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, site,
		domain.QoderCredential{UID: "uid-machine", DeviceToken: "dt"}, time.Now())
	if result.Outcome != domain.CheckinDone {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	if len(*seen) == 0 {
		t.Fatal("no request went out")
	}
	if got := (*seen)[0].sent.Get("Cosy-MachineToken"); got != "mt-1" {
		t.Fatalf("Cosy-MachineToken = %q", got)
	}
	if got := (*seen)[0].sent.Get("Cosy-MachineType"); got != "5" {
		t.Fatalf("Cosy-MachineType = %q", got)
	}
}

func TestQoderMomentReadsBothUnits(t *testing.T) {
	seconds := float64(time.Now().Unix())
	if got := qoderMoment(seconds); time.Since(got) > time.Minute {
		t.Fatalf("seconds read as %s", got)
	}
	if got := qoderMoment(seconds * 1000); time.Since(got) > time.Minute {
		t.Fatalf("milliseconds read as %s", got)
	}
	if !qoderMoment(0).IsZero() {
		t.Fatal("zero must read as no time, not the epoch")
	}
}

// fakeQoderRuntimeInfo stands in for Qoder's native device-identity helper: a
// script that prints the same single line of JSON.
func fakeQoderRuntimeInfo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("本机验收用不到国际版签到：Windows 上没法直接 exec 一个 shell 脚本")
	}
	path := filepath.Join(t.TempDir(), "runtime-info")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '{\"machineToken\":\"mt-1\",\"machineType\":\"5\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQoderCampaignWindow(t *testing.T) {
	now := time.Now()
	campaign := qoderCampaign{StartAt: float64(now.Add(-time.Hour).Unix()), EndAt: float64(now.Add(time.Hour).Unix())}
	if !campaign.active(now) {
		t.Fatal("a campaign covering now is active")
	}
	if campaign.active(now.Add(2 * time.Hour)) {
		t.Fatal("its end is exclusive")
	}
	if campaign.active(now.Add(-2 * time.Hour)) {
		t.Fatal("its start is inclusive")
	}
}

// Qoder 把一次性礼包也挂在 CLAIM_BENEFIT 下（benefit.kind = REDEMPTION_CODE），
// 而且它会连续几周停在 CLAIMED。判断「今天签没签」只能看积分活动：拿礼包当签到
// 会出现「今日已签到 +1」而当天 100 积分还挂着没领的假象。
func TestQoderCheckinReadsTheDayFromCreditsOnly(t *testing.T) {
	now := time.Now()
	creditsEnd := time.Unix(now.Add(3*time.Hour).Unix(), 0)
	list := qoderCampaignJSON(t,
		// 礼包排在前面：结论不能被列表顺序左右。
		map[string]any{"campaignId": "gift", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"startAt": now.Add(-24 * time.Hour).Unix(), "endAt": now.Add(7 * 24 * time.Hour).Unix(),
			"benefit": map[string]any{"kind": "REDEMPTION_CODE", "amount": 1}},
		map[string]any{"campaignId": "daily", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"startAt": now.Add(-time.Hour).Unix(), "endAt": creditsEnd.Unix(),
			"benefit": map[string]any{"kind": "CREDITS", "amount": 100}},
	)
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, `{"status":"CLAIMED"}`, 0))
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
		domain.QoderCredential{UID: "u", DeviceToken: "dt"}, now)
	if result.Outcome != domain.CheckinDone {
		t.Fatalf("outcome = %q (%s)", result.Outcome, result.Message)
	}
	if result.Credit != 100 {
		t.Fatalf("the day's credits are 100, not the gift's %v", result.Credit)
	}
	// 窗口结束要取积分活动的，取成礼包的 7 天后会让这一周都不再复查。
	if !result.Until.Equal(creditsEnd) {
		t.Fatalf("until = %s, want the credits window end %s", result.Until, creditsEnd)
	}
	if len(*seen) != 1 {
		t.Fatalf("nothing claimable, so no claim should be posted; saw %d calls", len(*seen))
	}
}

// 两个积分窗口之间的空档（今天的还没放出来）不是签到：说成「今日已签到」就是
// 用户报的「并没有签到领取」。
func TestQoderCheckinDoesNotCallOneOffGiftsADay(t *testing.T) {
	now := time.Now()
	list := qoderCampaignJSON(t, map[string]any{"campaignId": "gift", "actionType": "CLAIM_BENEFIT",
		"claimStatus": "CLAIMED", "startAt": now.Add(-24 * time.Hour).Unix(), "endAt": now.Add(7 * 24 * time.Hour).Unix(),
		"benefit": map[string]any{"kind": "REDEMPTION_CODE", "amount": 1}})
	server, seen := qoderCampaignServer(t, qoderCampaignHandler(list, `{"status":"CLAIMED"}`, 0))
	result, _ := qoderCheckin(context.Background(), nil, domain.Provider{ID: "q1"}, qoderCheckinSite(server),
		domain.QoderCredential{UID: "u", DeviceToken: "dt"}, now)
	if result.Outcome != domain.CheckinInactive {
		t.Fatalf("a gift is not a check-in: outcome = %q, credit = %v", result.Outcome, result.Credit)
	}
	if result.Credit != 0 || !result.Until.IsZero() {
		t.Fatalf("nothing was claimed today: credit = %v, until = %s", result.Credit, result.Until)
	}
	if len(*seen) != 1 {
		t.Fatalf("no claim expected, saw %d calls", len(*seen))
	}
}
