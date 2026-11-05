package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The credit-earning surface that is not a conversation.
//
// Three domains sit beside the growth tasks this gateway already runs, all of them
// plain GET/POST with no chat cost, and all of them credits the account can collect
// every day (misakano7545/workbuddy2api-panel internal/upstream/benefit.go,
// streak.go, streak_extras.go):
//
//	gift      POST /billing/meter/check-gift-claimed   is there a starter gift?
//	          POST /billing/meter/claim-gift           take it
//	streak    GET  /activity/growth/streak             streak days, tiers, makeup cards
//	          POST /activity/growth/redeem             trade a streak tier for credits
//	lottery   GET  /activity/growth/lottery/summary    draws available
//	          POST /activity/growth/lottery/draw       spend one
//
// Two path details are load-bearing and were measured the hard way upstream:
//
//   - the gift endpoints sit on the **bare** billing root, without the `/v2` prefix
//     the rest of the billing surface uses — asking `/v2/billing/meter/check-gift-claimed`
//     answers 404;
//   - redeem and draw take a client-generated token so a retry is not a second
//     claim. It has to look like the client's own (uuid v4), and it must be the same
//     value across retries of the same action.

const (
	workBuddyGiftStatusPath = "/billing/meter/check-gift-claimed"
	workBuddyGiftClaimPath  = "/billing/meter/claim-gift"
	workBuddyStreakPath     = "/activity/growth/streak"
	workBuddyRedeemPath     = "/activity/growth/redeem"
	workBuddyLotteryPath    = "/activity/growth/lottery/summary"
	workBuddyLotteryDraw    = "/activity/growth/lottery/draw"
)

// workBuddyGiftStatus is the starter gift's state.
//
// The claim endpoint answers 200 with no credit field when the gift was already
// taken, so "already claimed" has to be read from the status endpoint rather than
// inferred from a successful claim — the same discipline the scheduler follows
// upstream ("只认回执 credit，<=0 静默").
type workBuddyGiftStatus struct {
	Active    bool
	Claimed   bool
	CreditNum float64
}

// Pending reports whether there is a gift worth claiming.
func (status workBuddyGiftStatus) Pending() bool {
	return status.Active && !status.Claimed && status.CreditNum > 0
}

// workBuddyStreakTier is one streak redemption tier.
type workBuddyStreakTier struct {
	Tier    int
	Days    int
	Credit  float64
	Chances int
	Status  string
}

// workBuddyStreak is the streak oracle's answer.
type workBuddyStreak struct {
	Days           int
	MonthTotalDays int
	NextTier       int
	NextTierRemain int
	MakeupBalance  int
	MakeupMax      int
	RemainingDays  int
	Tiers          []workBuddyStreakTier
}

// workBuddyLottery is the draw summary.
type workBuddyLottery struct {
	Chances int
	Credit  float64
}

// workBuddyRewardsOutcome is what one pass collected, for the console and logs.
type workBuddyRewardsOutcome struct {
	GiftClaimed   float64  `json:"giftClaimed,omitempty"`
	StreakCredit  float64  `json:"streakCredit,omitempty"`
	LotteryCredit float64  `json:"lotteryCredit,omitempty"`
	TiersRedeemed []int    `json:"tiersRedeemed,omitempty"`
	Draws         int      `json:"draws,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

// Total is everything this pass brought in.
func (outcome workBuddyRewardsOutcome) Total() float64 {
	return outcome.GiftClaimed + outcome.StreakCredit + outcome.LotteryCredit
}

// workBuddyGiftStatusOf asks whether a starter gift is waiting.
func workBuddyGiftStatusOf(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (workBuddyGiftStatus, error) {
	var data struct {
		Active    any `json:"active"`
		Claimed   any `json:"claimed"`
		CreditNum any `json:"creditNum"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyGiftStatusPath, map[string]any{}, &data); err != nil {
		return workBuddyGiftStatus{}, err
	}
	return workBuddyGiftStatus{
		Active:    workBuddyAnyBool(data.Active),
		Claimed:   workBuddyAnyBool(data.Claimed),
		CreditNum: workBuddyAnyFloat(data.CreditNum),
	}, nil
}

// workBuddyClaimGift takes the starter gift and returns what it paid.
//
// A successful claim that reports nothing is the "already taken" shape, so the
// returned amount is the only evidence used; 0 means nothing was collected.
func workBuddyClaimGift(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (float64, error) {
	var data struct {
		Credit any `json:"credit"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyGiftClaimPath, map[string]any{}, &data); err != nil {
		return 0, err
	}
	credit := workBuddyAnyFloat(data.Credit)
	if credit <= 0 {
		return 0, nil
	}
	return credit, nil
}

// workBuddyStreakNow reads the streak oracle.
//
// Every numeric field goes through workBuddyAnyFloat instead of a typed int: the
// upstream writes the same field as a number on one build and as a string on
// another — measured live, `streak.next_tier` came back as a string, and a typed
// decode refuses the whole answer over one field.
func workBuddyStreakNow(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (workBuddyStreak, error) {
	var raw struct {
		Streak struct {
			Days           any `json:"days"`
			MonthTotalDays any `json:"month_total_days"`
			NextTier       any `json:"next_tier"`
			NextTierRemain any `json:"next_tier_remaining"`
		} `json:"streak"`
		MakeupCards struct {
			Balance any `json:"balance"`
			Max     any `json:"max"`
		} `json:"makeup_cards"`
		Redemption struct {
			RemainingDays any `json:"remaining_days"`
			Tiers         []struct {
				Tier    any    `json:"tier"`
				Days    any    `json:"days"`
				Credit  any    `json:"credit"`
				Chances any    `json:"chances"`
				Status  string `json:"status"`
			} `json:"tiers"`
		} `json:"redemption_status"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodGet,
		workBuddyStreakPath, nil, &raw); err != nil {
		return workBuddyStreak{}, err
	}
	streak := workBuddyStreak{
		Days:           int(workBuddyAnyFloat(raw.Streak.Days)),
		MonthTotalDays: int(workBuddyAnyFloat(raw.Streak.MonthTotalDays)),
		NextTier:       int(workBuddyAnyFloat(raw.Streak.NextTier)),
		NextTierRemain: int(workBuddyAnyFloat(raw.Streak.NextTierRemain)),
		MakeupBalance:  int(workBuddyAnyFloat(raw.MakeupCards.Balance)),
		MakeupMax:      int(workBuddyAnyFloat(raw.MakeupCards.Max)),
		RemainingDays:  int(workBuddyAnyFloat(raw.Redemption.RemainingDays)),
	}
	for _, tier := range raw.Redemption.Tiers {
		streak.Tiers = append(streak.Tiers, workBuddyStreakTier{
			Tier:    int(workBuddyAnyFloat(tier.Tier)),
			Days:    int(workBuddyAnyFloat(tier.Days)),
			Credit:  workBuddyAnyFloat(tier.Credit),
			Chances: int(workBuddyAnyFloat(tier.Chances)),
			Status:  strings.TrimSpace(tier.Status),
		})
	}
	return streak, nil
}

// workBuddyStreakRedeemable reports whether a tier can be traded now.
//
// "locked" means the streak has not reached the tier; "claimed" means it was
// already traded. Anything else is attempted, because a status this gateway does
// not know is more likely a new successful state than a refusal.
func workBuddyStreakRedeemable(tier workBuddyStreakTier) bool {
	switch strings.ToLower(strings.TrimSpace(tier.Status)) {
	case "locked", "claimed", "redeemed", "used", "unavailable":
		return false
	}
	return tier.Tier > 0
}

// workBuddyRedeemTier trades one streak tier for its credits.
func workBuddyRedeemTier(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, tier int, token string) (float64, error) {
	var data struct {
		Credit any `json:"credit"`
	}
	body := map[string]any{"tier": tier, "client_token": token}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyRedeemPath, body, &data); err != nil {
		return 0, err
	}
	credit := workBuddyAnyFloat(data.Credit)
	if credit <= 0 {
		return 0, nil
	}
	return credit, nil
}

// workBuddyLotteryNow reads the draws available.
func workBuddyLotteryNow(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) (workBuddyLottery, error) {
	var data struct {
		Chances any `json:"chances"`
		Credit  any `json:"credit"`
	}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodGet,
		workBuddyLotteryPath, nil, &data); err != nil {
		return workBuddyLottery{}, err
	}
	return workBuddyLottery{
		Chances: int(workBuddyAnyFloat(data.Chances)),
		Credit:  workBuddyAnyFloat(data.Credit),
	}, nil
}

// workBuddyLotteryDrawOnce spends one draw.
func workBuddyLotteryDrawOnce(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, token string) (float64, error) {
	var data struct {
		Credit any `json:"credit"`
	}
	body := map[string]any{"client_token": token}
	if err := workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost,
		workBuddyLotteryDraw, body, &data); err != nil {
		return 0, err
	}
	credit := workBuddyAnyFloat(data.Credit)
	if credit <= 0 {
		return 0, nil
	}
	return credit, nil
}

// workBuddyRewardsPass collects everything that is free to collect, one domain at a
// time.
//
// The three are independent on purpose: a refusal in one must not stop the others,
// and the outcome records what actually came back rather than what was attempted.
// Nothing here is retried on a rejection — a locked tier or a spent gift is an
// answer, not a failure.
func workBuddyRewardsPass(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) workBuddyRewardsOutcome {
	outcome := workBuddyRewardsOutcome{}

	// 1. The starter gift.
	if status, err := workBuddyGiftStatusOf(ctx, site, credential); err != nil {
		outcome.Notes = append(outcome.Notes, "gift status: "+workBuddyShortError(err))
	} else if status.Pending() {
		if credit, claimErr := workBuddyClaimGift(ctx, site, credential); claimErr != nil {
			outcome.Notes = append(outcome.Notes, "gift claim: "+workBuddyShortError(claimErr))
		} else if credit > 0 {
			outcome.GiftClaimed = credit
		}
	}

	// 2. Streak tiers.
	if streak, err := workBuddyStreakNow(ctx, site, credential); err != nil {
		outcome.Notes = append(outcome.Notes, "streak: "+workBuddyShortError(err))
	} else {
		for _, tier := range streak.Tiers {
			if !workBuddyStreakRedeemable(tier) {
				continue
			}
			credit, redeemErr := workBuddyRedeemTier(ctx, site, credential, tier.Tier, workBuddyClientToken("redeem"))
			if redeemErr != nil {
				outcome.Notes = append(outcome.Notes, fmt.Sprintf("redeem tier %d: %s", tier.Tier, workBuddyShortError(redeemErr)))
				continue
			}
			if credit > 0 {
				outcome.StreakCredit += credit
				outcome.TiersRedeemed = append(outcome.TiersRedeemed, tier.Tier)
			}
		}
	}

	// 3. Lottery draws, one at a time, never in a burst: the draws are the
	// account's own chances and a failure mid-way must not lose the rest.
	if lottery, err := workBuddyLotteryNow(ctx, site, credential); err != nil {
		outcome.Notes = append(outcome.Notes, "lottery: "+workBuddyShortError(err))
	} else {
		for draw := 0; draw < lottery.Chances && draw < 20; draw++ {
			credit, drawErr := workBuddyLotteryDrawOnce(ctx, site, credential, workBuddyClientToken("draw"))
			if drawErr != nil {
				outcome.Notes = append(outcome.Notes, "draw: "+workBuddyShortError(drawErr))
				break
			}
			outcome.Draws++
			if credit > 0 {
				outcome.LotteryCredit += credit
			}
		}
	}
	return outcome
}

// workBuddyClientToken makes the idempotency token these endpoints expect.
//
// A uuid v4 shape, because that is what the official client sends; random rather
// than derived, because two different actions must never collide.
func workBuddyClientToken(purpose string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on the platforms this runs on; falling back to a
		// time-derived value keeps the field filled rather than empty.
		sum := workBuddyStableHex(purpose + time.Now().Format(time.RFC3339Nano))
		decoded, _ := hex.DecodeString(sum)
		copy(raw[:], decoded)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// workBuddyAnyBool reads a boolean that may arrive as a bool or a number.
func workBuddyAnyBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}

// workBuddyAnyFloat reads a number that may arrive as a number or a string.
func workBuddyAnyFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		number, err := typed.Float64()
		if err == nil {
			return number
		}
	case string:
		number, ok := workBuddyJSONNumber(typed)
		if ok {
			return number
		}
	}
	return 0
}

// workBuddyShortError keeps a note readable in a log line.
func workBuddyShortError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 160 {
		return text[:160] + "…"
	}
	return text
}
