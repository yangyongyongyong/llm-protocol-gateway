package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// rewardsServer is a fake growth host that records what was asked for.
type rewardsServer struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string][]string
	handler  func(method, path string) (int, string)
}

func newRewardsServer(t *testing.T, handler func(method, path string) (int, string)) (*rewardsServer, func()) {
	t.Helper()
	state := &rewardsServer{bodies: map[string][]string{}, handler: handler}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		payload := make([]byte, 0, 256)
		buffer := make([]byte, 512)
		for {
			read, err := request.Body.Read(buffer)
			if read > 0 {
				payload = append(payload, buffer[:read]...)
			}
			if err != nil {
				break
			}
		}
		state.mu.Lock()
		state.requests = append(state.requests, request.Method+" "+request.URL.Path)
		state.bodies[request.URL.Path] = append(state.bodies[request.URL.Path], string(payload))
		state.mu.Unlock()
		status, body := state.handler(request.Method, request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	previousEndpoint, previousWeb := workBuddyCNEndpoint, workBuddyCNWebBase
	workBuddyCNEndpoint, workBuddyCNWebBase = server.URL, server.URL
	return state, func() {
		server.Close()
		workBuddyCNEndpoint, workBuddyCNWebBase = previousEndpoint, previousWeb
	}
}

func (state *rewardsServer) seen(method, path string) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, entry := range state.requests {
		if entry == method+" "+path {
			return true
		}
	}
	return false
}

// The gift endpoints sit on the bare billing root: asking with the /v2 prefix the
// rest of the billing surface uses answers 404, which is why the path is asserted
// here rather than assumed.
func TestWorkBuddyGiftStatusPathHasNoV2Prefix(t *testing.T) {
	state, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		if path != "/billing/meter/check-gift-claimed" {
			return http.StatusNotFound, `{"code":404,"msg":"Route Not Found"}`
		}
		return http.StatusOK, `{"code":0,"data":{"active":true,"claimed":false,"creditNum":100}}`
	})
	defer cleanup()

	status, err := workBuddyGiftStatusOf(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !state.seen(http.MethodPost, "/billing/meter/check-gift-claimed") {
		t.Fatalf("requests = %v", state.requests)
	}
	if !status.Active || status.Claimed || status.CreditNum != 100 {
		t.Fatalf("status = %+v", status)
	}
	if !status.Pending() {
		t.Fatal("a waiting gift was not reported as pending")
	}
}

// Pending needs all three: active, not claimed, and worth something.
func TestWorkBuddyGiftPendingNeedsAllThree(t *testing.T) {
	cases := []struct {
		status workBuddyGiftStatus
		want   bool
	}{
		{workBuddyGiftStatus{Active: true, Claimed: false, CreditNum: 100}, true},
		{workBuddyGiftStatus{Active: false, Claimed: false, CreditNum: 100}, false},
		{workBuddyGiftStatus{Active: true, Claimed: true, CreditNum: 100}, false},
		{workBuddyGiftStatus{Active: true, Claimed: false, CreditNum: 0}, false},
	}
	for _, testCase := range cases {
		if got := testCase.status.Pending(); got != testCase.want {
			t.Fatalf("%+v -> %v, want %v", testCase.status, got, testCase.want)
		}
	}
}

// A claim that reports nothing is the "already taken" shape: it must not be counted
// as income.
func TestWorkBuddyClaimGiftCountsOnlyRealReceipts(t *testing.T) {
	_, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		return http.StatusOK, `{"code":0,"data":{}}`
	})
	defer cleanup()
	credit, err := workBuddyClaimGift(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if credit != 0 {
		t.Fatalf("credit = %v, want 0 for an empty receipt", credit)
	}
}

// The streak oracle is parsed into days, tiers and the remaining-day window.
func TestWorkBuddyStreakNowParsesTiers(t *testing.T) {
	_, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		return http.StatusOK, `{"code":0,"data":{
			"streak":{"days":14,"month_total_days":20,"next_tier":28,"next_tier_remaining":14},
			"makeup_cards":{"balance":2,"max":3},
			"redemption_status":{"remaining_days":0,"tiers":[
				{"tier":7,"days":7,"credit":20,"chances":1,"status":"claimed"},
				{"tier":14,"days":14,"credit":50,"chances":2,"status":""},
				{"tier":28,"days":28,"credit":120,"chances":3,"status":"locked"}
			]}}}`
	})
	defer cleanup()
	streak, err := workBuddyStreakNow(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if err != nil {
		t.Fatalf("streak: %v", err)
	}
	if streak.Days != 14 || streak.MakeupBalance != 2 || streak.NextTier != 28 {
		t.Fatalf("streak = %+v", streak)
	}
	if len(streak.Tiers) != 3 {
		t.Fatalf("tiers = %+v", streak.Tiers)
	}
	// Only the tier with an empty status is tradeable: claimed and locked are answers.
	var redeemable []int
	for _, tier := range streak.Tiers {
		if workBuddyStreakRedeemable(tier) {
			redeemable = append(redeemable, tier.Tier)
		}
	}
	if len(redeemable) != 1 || redeemable[0] != 14 {
		t.Fatalf("redeemable = %v, want only tier 14", redeemable)
	}
}

// Trading a tier sends the tier and a uuid-shaped idempotency token.
func TestWorkBuddyRedeemTierSendsTierAndToken(t *testing.T) {
	state, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		if path == "/activity/growth/redeem" {
			return http.StatusOK, `{"code":0,"data":{"credit":50}}`
		}
		return http.StatusNotFound, `{"code":404}`
	})
	defer cleanup()

	token := workBuddyClientToken("redeem")
	credit, err := workBuddyRedeemTier(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"}, 14, token)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if credit != 50 {
		t.Fatalf("credit = %v", credit)
	}
	bodies := state.bodies["/activity/growth/redeem"]
	if len(bodies) != 1 {
		t.Fatalf("bodies = %v", bodies)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if sent["tier"] != float64(14) {
		t.Fatalf("tier = %v", sent["tier"])
	}
	if sent["client_token"] != token {
		t.Fatalf("client_token = %v, want %q", sent["client_token"], token)
	}
}

// The idempotency token has the client's own shape and never repeats.
func TestWorkBuddyClientTokenShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token := workBuddyClientToken("redeem")
		if !workBuddyUUIDv4Re.MatchString(token) {
			t.Fatalf("token is not uuid-v4 shaped: %q", token)
		}
		if seen[token] {
			t.Fatalf("token repeated: %q", token)
		}
		seen[token] = true
	}
}

// A full pass collects what is there and never lets one domain stop another.
func TestWorkBuddyRewardsPassCollectsIndependently(t *testing.T) {
	state, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		switch path {
		case "/billing/meter/check-gift-claimed":
			return http.StatusOK, `{"code":0,"data":{"active":true,"claimed":false,"creditNum":100}}`
		case "/billing/meter/claim-gift":
			return http.StatusOK, `{"code":0,"data":{"credit":100}}`
		case "/activity/growth/streak":
			return http.StatusOK, `{"code":0,"data":{
				"streak":{"days":14},
				"redemption_status":{"tiers":[
					{"tier":7,"days":7,"credit":20,"status":"claimed"},
					{"tier":14,"days":14,"credit":50,"status":""}
				]}}}`
		case "/activity/growth/redeem":
			return http.StatusOK, `{"code":0,"data":{"credit":50}}`
		case "/activity/growth/lottery/summary":
			return http.StatusOK, `{"code":0,"data":{"chances":2}}`
		case "/activity/growth/lottery/draw":
			return http.StatusOK, `{"code":0,"data":{"credit":5}}`
		}
		return http.StatusNotFound, `{"code":404,"msg":"Route Not Found"}`
	})
	defer cleanup()

	outcome := workBuddyRewardsPass(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"})
	if outcome.GiftClaimed != 100 {
		t.Fatalf("gift = %v, want 100", outcome.GiftClaimed)
	}
	if outcome.StreakCredit != 50 || len(outcome.TiersRedeemed) != 1 || outcome.TiersRedeemed[0] != 14 {
		t.Fatalf("streak = %v tiers = %v", outcome.StreakCredit, outcome.TiersRedeemed)
	}
	if outcome.Draws != 2 || outcome.LotteryCredit != 10 {
		t.Fatalf("draws = %d credit = %v, want 2 draws for 10", outcome.Draws, outcome.LotteryCredit)
	}
	if outcome.Total() != 160 {
		t.Fatalf("total = %v, want 160", outcome.Total())
	}
	// The claimed tier is never sent: the oracle already said so.
	bodies := state.bodies["/activity/growth/redeem"]
	if len(bodies) != 1 {
		t.Fatalf("redeem calls = %d, want exactly one", len(bodies))
	}
	if !strings.Contains(bodies[0], `"tier":14`) {
		t.Fatalf("redeem body = %s", bodies[0])
	}
}

// A refusing domain is recorded as a note and the pass continues.
func TestWorkBuddyRewardsPassSurvivesRefusals(t *testing.T) {
	_, cleanup := newRewardsServer(t, func(method, path string) (int, string) {
		switch path {
		case "/activity/growth/lottery/summary":
			return http.StatusOK, `{"code":0,"data":{"chances":1}}`
		}
		return http.StatusForbidden, `{"code":403,"msg":"not available"}`
	})
	defer cleanup()

	outcome := workBuddyRewardsPass(context.Background(), workBuddyCNSite, domain.WorkBuddyCredential{AccessToken: "t"})
	if outcome.Total() != 0 {
		t.Fatalf("total = %v, want 0", outcome.Total())
	}
	if len(outcome.Notes) < 2 {
		t.Fatalf("notes = %v, want a note for each refused domain", outcome.Notes)
	}
	if outcome.Draws != 0 {
		t.Fatalf("draws = %d, want none when the draw is refused", outcome.Draws)
	}
}

// Numbers arrive as numbers or as strings, and both are read.
func TestWorkBuddyAnyNumberAndBool(t *testing.T) {
	if got := workBuddyAnyFloat("12.5"); got != 12.5 {
		t.Fatalf("string number = %v", got)
	}
	if got := workBuddyAnyFloat(json.Number("7")); got != 7 {
		t.Fatalf("json.Number = %v", got)
	}
	if got := workBuddyAnyFloat(nil); got != 0 {
		t.Fatalf("nil = %v", got)
	}
	if !workBuddyAnyBool("true") || !workBuddyAnyBool(1) || !workBuddyAnyBool(true) {
		t.Fatal("a truthy value was read as false")
	}
	if workBuddyAnyBool("false") || workBuddyAnyBool(0) || workBuddyAnyBool(nil) {
		t.Fatal("a falsy value was read as true")
	}
}
