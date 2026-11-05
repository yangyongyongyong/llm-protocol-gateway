package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// errWorkBuddyCodeNotNumeric is returned when a code field is not a decimal
// number, so it is ignored rather than guessed at.
var errWorkBuddyCodeNotNumeric = errors.New("workbuddy: code is not numeric")

// WorkBuddy's own failure codes, and who each one blames.
//
// A generic classifier sees only an HTTP status and some words, which is not
// enough for this upstream: WorkBuddy answers 400 for a dozen different
// conditions, and treating them alike either punishes a healthy account or
// retries a request that can never succeed. The two reference implementations
// both keep an explicit per-code table for exactly this reason
// (misakano7545/workbuddy2api-panel internal/upstream/client.go Classify;
// magpie-community/plugins packages/workbuddy index.mjs).
//
// The distinction that matters most is **model-level vs account-level**: a model
// that is rate-limited or filtered for one account says nothing about the
// account's health, so its code must advance the chain without painting the
// provider 异常 — otherwise one busy model rests every account that touches it.
//
// Codes were collected from both references' tables; the verdict column is this
// gateway's mapping onto its own failover classes, which already mean:
//
//	failoverHard           the account is out (marks 异常, cooldown)
//	failoverSoft           one strike (marks after N consecutive)
//	failoverBusy           the model is busy/limited, the account is fine
//	failoverPromptRefusal  the prompt was refused, the account is fine
//	failoverNone           do not advance the chain
type workBuddyFailureVerdict int

const (
	workBuddyVerdictNone workBuddyFailureVerdict = iota
	workBuddyVerdictModelLimited
	workBuddyVerdictBusy
	workBuddyVerdictPromptRefused
	workBuddyVerdictSessionDead
	workBuddyVerdictPayment
	workBuddyVerdictTransient
	workBuddyVerdictClientShape
)

// workBuddyFailureCodes maps the upstream's numeric code to a verdict.
//
// Left out on purpose: 11153 (conversation must end with a user message) and
// 11159 (a thinking block bound to another conversation) are repaired in the
// request path — sendWorkBuddyChat inserts the empty assistant turn and the
// messages route drops unsigned thinking — so by the time one is still seen the
// body is a client's own malformed chat, which is a failoverNone.
var workBuddyFailureCodes = map[int]struct {
	kind   workBuddyFailureVerdict
	reason string
}{
	// Model-level: the account is healthy, this model is not available for it.
	11102: {workBuddyVerdictModelLimited, "wb_model_filtered"},
	11115: {workBuddyVerdictModelLimited, "wb_model_unavailable"},
	11135: {workBuddyVerdictModelLimited, "wb_model_unsupported"},
	11148: {workBuddyVerdictModelLimited, "wb_model_limited"},
	6000:  {workBuddyVerdictModelLimited, "wb_model_rate_limited"},
	6004:  {workBuddyVerdictModelLimited, "wb_model_rate_limited"},
	14003: {workBuddyVerdictModelLimited, "wb_model_rate_limited"},

	// The upstream itself is busy: wait, do not penalise the account.
	6020: {workBuddyVerdictBusy, "wb_upstream_busy"},
	6021: {workBuddyVerdictBusy, "wb_upstream_busy"},

	// Channel / prompt refusals: nothing about the account.
	11128: {workBuddyVerdictPromptRefused, "prompt_refused"},
	11140: {workBuddyVerdictPromptRefused, "prompt_refused"},

	// The account's own session or billing is the problem.
	12153: {workBuddyVerdictSessionDead, "wb_session_dead"},
	14017: {workBuddyVerdictPayment, "wb_payment_required"},
	14018: {workBuddyVerdictPayment, "wb_payment_required"},
	// 10085 "请求不合法" is the upstream refusing an unrecognised User-Agent. It
	// cannot fix itself, and retrying the same build keeps answering it.
	10085: {workBuddyVerdictSessionDead, "wb_ua_rejected"},

	// Client-shape problems this gateway already repairs; advancing the chain
	// would only repeat the same request elsewhere.
	11101: {workBuddyVerdictClientShape, "wb_non_stream"},
	11133: {workBuddyVerdictClientShape, "wb_max_tokens_too_small"},
	11153: {workBuddyVerdictClientShape, "wb_conversation_shape"},
	11159: {workBuddyVerdictClientShape, "wb_thinking_signature"},
}

// workBuddyCodePattern reads the numeric code out of an upstream envelope,
// whether it arrives as a number or a string.
var workBuddyCodePattern = regexp.MustCompile(`"code"\s*:\s*"?(-?\d+)"?`)

// workBuddyFailureCode extracts the upstream's code, or reports that there is
// none. The pattern is used rather than a full decode because the body reaching
// the classifier has already been summarised, and a refused request's body is not
// always well-formed JSON.
func workBuddyFailureCode(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}
	// A perfectly formed envelope is the common case; take it when it parses.
	var envelope struct {
		Code json.Number `json:"code"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Code != "" {
		if code, err := envelope.Code.Int64(); err == nil && code != 0 {
			return int(code), true
		}
	}
	if match := workBuddyCodePattern.FindSubmatch(body); match != nil {
		if code, err := parseWorkBuddyCode(string(match[1])); err == nil && code != 0 {
			return code, true
		}
	}
	return 0, false
}

// parseWorkBuddyCode parses a decimal code of any width.
func parseWorkBuddyCode(raw string) (int, error) {
	value := 0
	negative := false
	for index, ch := range raw {
		if index == 0 && ch == '-' {
			negative = true
			continue
		}
		if ch < '0' || ch > '9' {
			return 0, errWorkBuddyCodeNotNumeric
		}
		value = value*10 + int(ch-'0')
	}
	if negative {
		value = -value
	}
	return value, nil
}

// workBuddyFailureVerdictOf looks a code up.
func workBuddyFailureVerdictOf(code int) (workBuddyFailureVerdict, string, bool) {
	entry, ok := workBuddyFailureCodes[code]
	if !ok {
		return workBuddyVerdictNone, "", false
	}
	return entry.kind, entry.reason, true
}

// workBuddyFailoverClass maps a verdict onto this gateway's failover classes.
//
// failoverBusy is the class that means "advance the chain, penalise nobody",
// which is precisely what a model-level refusal needs: the next provider gets a
// chance and the account keeps its record clean.
func workBuddyFailoverClass(verdict workBuddyFailureVerdict) (failoverClass, bool) {
	switch verdict {
	case workBuddyVerdictModelLimited, workBuddyVerdictBusy:
		return failoverBusy, true
	case workBuddyVerdictPromptRefused:
		return failoverPromptRefusal, true
	case workBuddyVerdictSessionDead, workBuddyVerdictPayment:
		return failoverHard, true
	case workBuddyVerdictTransient:
		return failoverSoft, true
	case workBuddyVerdictClientShape:
		return failoverNone, true
	}
	return failoverNone, false
}

// classifyWorkBuddyFailure decides a WorkBuddy attempt from its own code table.
// It answers ok=false when no known code is present, so the generic classifier
// still gets the last word on everything else.
func classifyWorkBuddyFailure(status int, body []byte) (failoverClass, string, bool) {
	// A 402 is the one status the vendor uses for a spent plan; it carries no code
	// this table knows, and keeping it here avoids a second special case elsewhere.
	if status == http.StatusPaymentRequired {
		return failoverHard, "http_402_payment", true
	}
	// The free model's daily window: the account is healthy, the allowance is spent
	// until a stated instant. Before this branch a 429 here was read as a soft
	// throttle and rested the whole account after a few hits.
	if status == http.StatusTooManyRequests && workBuddyIsFreeWindowRefusal(body) {
		return failoverBusy, "wb_free_model_window", true
	}
	code, ok := workBuddyFailureCode(body)
	if !ok {
		return failoverNone, "", false
	}
	verdict, reason, known := workBuddyFailureVerdictOf(code)
	if !known {
		return failoverNone, "", false
	}
	class, mapped := workBuddyFailoverClass(verdict)
	if !mapped {
		return failoverNone, "", false
	}
	return class, reason, true
}

// ---- model-level rests ------------------------------------------------------
//
// A model that is rate-limited or filtered for one account is rested on its own,
// apart from the account's state. The reference implementation keeps exactly this
// ledger (`modelCooldowns`, per account per model) and it is what keeps one busy
// model from marking a whole account unhealthy.
//
// Rests are keyed on provider + model and held in memory: they are a routing hint,
// not account state, so losing them on restart costs one request.

const (
	// workBuddyModelRestDefault is how long a model-level refusal rests a model.
	workBuddyModelRestDefault = 10 * time.Minute
	// workBuddyModelRestBusy is the shorter rest for "the upstream is busy".
	workBuddyModelRestBusy = 2 * time.Minute
	// workBuddyModelRestMemory bounds the ledger so a long-lived process cannot
	// grow it one model name at a time.
	workBuddyModelRestMemory = 4096
)

// workBuddyModelRests holds providerID + "\x00" + model → restUntil (time.Time).
var workBuddyModelRests sync.Map

// workBuddyModelRestKey is the ledger key for one provider's model.
func workBuddyModelRestKey(providerID, model string) string {
	return strings.TrimSpace(providerID) + "\x00" + strings.ToLower(strings.TrimSpace(model))
}

// restWorkBuddyModel keeps a model out of rotation for this provider for a while.
func restWorkBuddyModel(providerID, model string, now, until time.Time) {
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(model) == "" {
		return
	}
	workBuddyModelRests.Store(workBuddyModelRestKey(providerID, model), until)
	// Prune against the caller's clock: pruning with the entry's own deadline would
	// drop the entry just written, and pruning with the wall clock would drop
	// entries a caller set for a simulated instant.
	pruneWorkBuddyModelRests(now)
}

// pruneWorkBuddyModelRests drops entries that have expired, and enforces the
// memory bound when the ledger has grown past it.
func pruneWorkBuddyModelRests(now time.Time) {
	count := 0
	workBuddyModelRests.Range(func(key, value any) bool {
		count++
		until, ok := value.(time.Time)
		if !ok || !until.After(now) {
			workBuddyModelRests.Delete(key)
		}
		return true
	})
	if count <= workBuddyModelRestMemory {
		return
	}
	workBuddyModelRests.Range(func(key, _ any) bool {
		workBuddyModelRests.Delete(key)
		count--
		return count > workBuddyModelRestMemory
	})
}

// workBuddyModelRested reports whether this provider's model is resting.
func workBuddyModelRested(providerID, model string, now time.Time) bool {
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(model) == "" {
		return false
	}
	cached, ok := workBuddyModelRests.Load(workBuddyModelRestKey(providerID, model))
	if !ok {
		return false
	}
	until, ok := cached.(time.Time)
	if !ok {
		return false
	}
	if !until.After(now) {
		workBuddyModelRests.Delete(workBuddyModelRestKey(providerID, model))
		return false
	}
	return true
}

// noteWorkBuddyModelRest records a rest for a model-level verdict and reports
// whether it did. A code that blames the account never rests a model.
func noteWorkBuddyModelRest(providerID, model string, status int, body []byte, now time.Time) bool {
	// The free model's window states its own reset instant, which is more precise
	// than any fixed rest this file could guess.
	if status == http.StatusTooManyRequests && workBuddyIsFreeWindowRefusal(body) {
		restWorkBuddyModel(providerID, model, now, now.Add(workBuddyFrequencyRest(body, now)))
		return true
	}
	code, ok := workBuddyFailureCode(body)
	if !ok {
		return false
	}
	verdict, _, known := workBuddyFailureVerdictOf(code)
	if !known {
		return false
	}
	switch verdict {
	case workBuddyVerdictModelLimited:
		restWorkBuddyModel(providerID, model, now, now.Add(workBuddyModelRestDefault))
		return true
	case workBuddyVerdictBusy:
		restWorkBuddyModel(providerID, model, now, now.Add(workBuddyModelRestBusy))
		return true
	}
	return false
}

// workBuddyModelState describes a model's availability for one provider, for the
// console and for routing diagnostics.
type workBuddyModelState struct {
	Rested   bool      `json:"rested"`
	RestTill time.Time `json:"restTill,omitempty"`
}

// workBuddyModelStateOf reports one model's rest state.
func workBuddyModelStateOf(providerID, model string, now time.Time) workBuddyModelState {
	cached, ok := workBuddyModelRests.Load(workBuddyModelRestKey(providerID, model))
	if !ok {
		return workBuddyModelState{}
	}
	until, ok := cached.(time.Time)
	if !ok || !until.After(now) {
		return workBuddyModelState{}
	}
	return workBuddyModelState{Rested: true, RestTill: until}
}
