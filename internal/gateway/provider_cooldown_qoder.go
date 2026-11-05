package gateway

// How Qoder's failures are accounted for.
//
// The generic ladder in provider_failover.go answers one question — "how long
// does this provider rest?" — and it answers it well for a pool where every
// account is interchangeable. Qoder is not that, in three ways:
//
//  1. A pool of one is not a pool. Resting the only account for minutes turns
//     one throttled request into an outage, so a single-account pool rests for
//     seconds instead.
//  2. An exhausted balance is not a broken account. It is a fact about the
//     account that changes on its own schedule, so it rests for a fixed period
//     and is not escalated, not marked hard, and not fed to the failure counters
//     that would keep it out after the balance came back.
//  3. A failed attempt is not necessarily this account's fault. A request that
//     no account can serve (a malformed body, a prompt over the model's window)
//     fails on every account identically; charging each of them is how one bad
//     request parks the whole pool. So a Qoder failure is held until the same
//     request succeeds somewhere — only then is it that account's failure.
//
// None of this applies to other providers, whose behaviour must not change.

import (
	"context"
	"strings"
	"time"
)

const (
	// qoderSingleAccountRest is how long a pool of one account rests. Long
	// enough that the next request does not hit the same throttle, short enough
	// that the user does not see an outage.
	qoderSingleAccountRest = 3 * time.Second
	// qoderQuotaRest is how long an account with no balance left rests. The
	// balance does not come back because we waited, and it does not stay broken
	// because we asked again, so this is a pace, not a punishment.
	qoderQuotaRest = 30 * time.Minute
)

// qoderBlame is one failure waiting to be charged. It is recorded only if a
// later attempt of the same request succeeds.
type qoderBlame struct {
	providerID string
	reason     string
	retryAfter string
	body       []byte
}

// qoderBlameBucket is the per-request list of failures still on probation.
type qoderBlameBucket struct {
	blames []qoderBlame
}

type qoderBlameContextKey struct{}

// withQoderBlame installs the bucket a request's deferred failures land in.
func withQoderBlame(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, qoderBlameContextKey{}, &qoderBlameBucket{})
}

// qoderBlameBucketFrom is the bucket, or nil when the request did not install
// one (a direct call, a test): without it nothing is deferred.
func qoderBlameBucketFrom(ctx context.Context) *qoderBlameBucket {
	if ctx == nil {
		return nil
	}
	bucket, _ := ctx.Value(qoderBlameContextKey{}).(*qoderBlameBucket)
	return bucket
}

// hold keeps one failure on probation.
func (b *qoderBlameBucket) hold(blame qoderBlame) {
	if b == nil {
		return
	}
	b.blames = append(b.blames, blame)
}

// flush charges every failure the request left on probation, and empties the
// bucket. Called once, when some account has answered: at that moment the
// refusals really were those accounts'.
func (b *qoderBlameBucket) flush(server *Server) {
	if b == nil || len(b.blames) == 0 {
		return
	}
	held := b.blames
	b.blames = nil
	for _, blame := range held {
		server.chargeQoderBlame(blame)
	}
}

// qoderQuotaRefusal reports whether a refusal is about the account's balance
// rather than its health. Qoder's own client reads these three the same way.
func qoderQuotaRefusal(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(message, "额度已用完") ||
		strings.Contains(message, "次日恢复") ||
		strings.Contains(lower, "pricingurl") ||
		strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "credits used up")
}

// qoderPoolSize is how many enabled accounts of the same site this provider
// belongs to: the pool a failure would have to be covered by.
func (s *Server) qoderPoolSize(providerID string) int {
	if s.router == nil {
		return 1
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return 1
	}
	site := qoderSiteOf(provider)
	count := 0
	for _, candidate := range s.router.State().Providers {
		if candidate.Disabled || !isQoderProvider(candidate) {
			continue
		}
		if qoderSiteOf(candidate).AuthType != site.AuthType {
			continue
		}
		count++
	}
	if count < 1 {
		// The provider itself may not be in the state yet (a provider being
		// added, a test fixture): it is still one account.
		return 1
	}
	return count
}

// qoderCooldownPolicy decides what one Qoder failure means.
//
// apply says "rest this account now, for ttl"; hold says "keep it on probation
// until the request settles"; neither means "let the generic ladder decide" —
// which is what a hard failure (a lapsed credential) wants, since that really is
// this account's and no later attempt can change it.
func (s *Server) qoderCooldownPolicy(providerID string, class failoverClass, message string) (ttl time.Duration, apply, hold bool) {
	if qoderQuotaRefusal(message) {
		// An empty balance rests for a fixed period and is never escalated: the
		// balance is the reason, and the balance is not a fault.
		return qoderQuotaRest, true, false
	}
	if class == failoverHard {
		return 0, false, false
	}
	if s.qoderPoolSize(providerID) <= 1 {
		// Nothing else can carry the request, so the only sane rest is a short
		// one. Marked soft: one throttle is not proof the account is broken.
		return qoderSingleAccountRest, true, false
	}
	// A pool can cover this attempt, so the failure is charged only if the
	// request eventually succeeds somewhere.
	return 0, false, true
}

// applyQoderCooldown takes over the accounting for one Qoder failure, reporting
// whether it did. A false return means the generic ladder should handle it —
// either because this is not Qoder, or because the failure is the account's own
// and no policy makes it different.
func (s *Server) applyQoderCooldown(ctx context.Context, providerID string, class failoverClass, status int, body []byte, retryAfter, reason string, err error) bool {
	if s.router == nil {
		return false
	}
	provider, lookupErr := s.router.ProviderByID(providerID)
	if lookupErr != nil || !isQoderProvider(provider) {
		return false
	}
	message := ""
	if err != nil {
		message = err.Error()
	} else {
		message = summarizeUpstreamHTTPError(status, body)
	}
	ttl, apply, hold := s.qoderCooldownPolicy(providerID, class, message)
	if apply && ttl > 0 {
		s.restQoderProvider(providerID, ttl, reason)
		return true
	}
	if !hold {
		return false
	}
	// On probation: charged only if this request is served by some account.
	bucket := qoderBlameBucketFrom(ctx)
	if bucket == nil {
		// No bucket (a direct call, a test): the safest reading is that this
		// failure is real, so the generic ladder keeps its say.
		return false
	}
	bucket.hold(qoderBlame{providerID: providerID, reason: reason, retryAfter: retryAfter, body: body})
	return true
}

// chargeQoderBlame records one held failure, through the same soft-failure
// ladder every other provider uses: being held says when the failure is charged,
// not how much it counts.
func (s *Server) chargeQoderBlame(blame qoderBlame) {
	if strings.TrimSpace(blame.providerID) == "" {
		return
	}
	count := s.bumpSoftFailure(blame.providerID)
	if count >= providerSoftFailureThreshold {
		s.markProviderUnavailable(blame.providerID, false, blame.reason, blame.retryAfter, blame.body)
		return
	}
	if s.logs != nil {
		s.logs.AddApp("info", "provider soft failure", "provider="+blame.providerID+
			" reason="+blame.reason+" (charged once the request settled)")
	}
}

// restQoderProvider applies a policy decision that has to happen now.
func (s *Server) restQoderProvider(providerID string, ttl time.Duration, reason string) {
	if ttl <= 0 {
		return
	}
	s.providerAvailabilityMu.Lock()
	if s.providerAvailability == nil {
		s.providerAvailability = make(map[string]providerUnavailableState)
	}
	s.providerAvailability[providerID] = providerUnavailableState{
		nextRetry: time.Now().Add(ttl),
		hard:      false,
		reason:    truncateReason(reason),
	}
	s.providerAvailabilityMu.Unlock()
	if s.logs != nil {
		s.logs.AddApp("warn", "provider resting", "provider="+providerID+" retry_in="+ttl.Round(time.Second).String()+" reason="+truncateReason(reason))
	}
}
