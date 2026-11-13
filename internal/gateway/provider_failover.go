package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// failoverClass distinguishes transient upstream issues from hard quota/auth
// failures. Both still advance the backup chain; only hard (or repeated soft)
// failures mark the provider UI as "unavailable".
type failoverClass int

const (
	failoverNone failoverClass = iota
	failoverSoft
	failoverHard
	// failoverPromptRefusal advances the backup chain but rests nobody: the
	// vendor turned the request's own prompt away, as WorkBuddy's 11128 does to
	// another agent's system prompt. Every account of it answers the same, so
	// none of them is at fault.
	failoverPromptRefusal
	// failoverBusy advances the chain but rests nobody: the vendor said the
	// model is busy/queued (Qoder's 10605), which is a property of the model at
	// this moment, not of the account. Cooling the account down for it sends
	// the next request into the same queue with one fewer place to try, and the
	// recovery loop then walks it right back into the queue.
	failoverBusy
)

// upstreamBusyErrorType is the error.type an adapter writes when the vendor
// framed its refusal as "the model is busy". It is the one signal the
// classifier can read after the vendor's own codes are gone: a converted
// stream carries the refusal as an error event, and only its type survives.
const upstreamBusyErrorType = "upstream_busy"

const (
	// Soft failures must repeat within this window before the provider is
	// marked unavailable (avoids single 429/529/5xx blips painting "异常").
	//
	// The window has to outlast the shortest rest the count buys, or the ladder
	// could never climb: the third 429 rests a minute, and a window shorter
	// than that forgets the other two before the rest ends. Ten minutes covers
	// the climb (30s, 1m, 2m, 4m, 8m) up to the point where the doubling itself
	// is longer than the memory, after which the rest cap takes over anyway.
	providerSoftFailureWindow    = 10 * time.Minute
	providerSoftFailureThreshold = 3
	providerSoftUnavailableTTL   = 1 * time.Minute
	claudeUsageHardThresholdPct  = 95.0

	// providerRateRestBase is where a rate-limit rest starts, and each further
	// consecutive failure doubles it up to providerRateRestCap. Separate from
	// the quota ladder on purpose: a 429 that says "try again in 5s" and a
	// plan that is spent until next Tuesday are different problems, and only
	// the second one justifies a multi-hour rest.
	providerRateRestBase = 30 * time.Second
	providerRateRestCap  = 30 * time.Minute
	// providerQuotaRestFloor is the shortest rest for a confirmed quota
	// exhaustion: probing sooner only burns a request on a vendor that already
	// said no.
	providerQuotaRestFloor = 15 * time.Minute
)

// hardQuotaHints are narrow subscription/billing exhaustion signals. Broad
// tokens like bare "quota" / "exceeded" / "rate limit" are intentionally
// omitted — those usually mean short-lived throttling, not a dead provider.
//
// Bare "billing" is deliberately absent: "billing not enabled on this
// workspace", "billing address missing" and the like are configuration
// mistakes that the next account on the chain answers fine, and treating the
// word as quota death cooled a healthy provider down for a whole cycle.
var hardQuotaHints = []string{
	"insufficient_quota",
	"usage limit",
	"you've hit your",
	"you have hit your",
	"credit balance",
	"tokens exhausted",
	"out of credits",
	"quota exceeded",
	"exceeded your current quota",
	"plan limit",
}

// hardAuthHints mark credential death (not a transient token-refresh race).
var hardAuthHints = []string{
	"invalid_grant",
	"revoked",
	"invalid_api_key",
	"invalid x-api-key",
	"invalid api key",
	"api key not valid",
}

// softThrottleHints classify 400 bodies that are failover-worthy but not a
// hard account/quota kill.
var softThrottleHints = []string{
	"rate_limit",
	"rate limit",
	"overloaded",
	"capacity",
}

type providerUnavailableState struct {
	nextRetry time.Time
	hard      bool
	reason    string
}

type providerSoftFailureState struct {
	count       int
	windowStart time.Time
}

// shouldFailoverProvider reports whether an upstream failure should advance to
// the next backup provider for this API key.
func shouldFailoverProvider(status int, body []byte, transportErr error) bool {
	class, _ := classifyProviderFailover(status, body, transportErr, nil)
	return class != failoverNone
}

// isRequestContextCanceled reports client-gone / deadline errors. These must
// NOT trigger provider failover: the backup attempt reuses r.Context(), which
// is already done, so it would only produce a misleading "context canceled"
// against the fallback URL (and leave Claude Code looking "stuck").
func isRequestContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// classifyProviderFailover returns soft/hard/none plus a short reason string.
// fiveHourUtil, when non-nil, is the cached Claude OAuth 5h utilization
// (0-100). Hard "usage limit" signals are downgraded to soft when utilization
// is clearly under the hard threshold — matching the quota panel reality.
func classifyProviderFailover(status int, body []byte, transportErr error, fiveHourUtil *float64) (failoverClass, string) {
	if transportErr != nil {
		if isRequestContextCanceled(transportErr) {
			return failoverNone, "client_canceled"
		}
		// A refusal at the account's own lane is this gateway's admission
		// control, not the vendor failing: nothing was sent. Counting it as a
		// transport failure cooled a healthy account down for minutes after
		// three busy requests, which is the opposite of what the lane is for.
		if errors.Is(transportErr, errProviderSaturated) {
			return failoverNone, "lane_saturated"
		}
		// A converted stream carries the vendor's refusal as an error event, so
		// it reaches here as a transport error with the vendor's verdict inside
		// its text. A model that is busy is still not an account that failed.
		if busyRefusalText(transportErr.Error()) {
			return failoverBusy, "model_busy"
		}
		return failoverSoft, "transport: " + truncateReason(transportErr.Error())
	}

	msg := strings.ToLower(summarizeUpstreamHTTPError(status, body))
	hardQuota := containsAnyHint(msg, hardQuotaHints)
	hardAuth := containsAnyHint(msg, hardAuthHints)
	softThrottle := containsAnyHint(msg, softThrottleHints)
	if busyRefusalText(msg) {
		return failoverBusy, "model_busy"
	}

	switch {
	case status == http.StatusTooManyRequests || status == 529:
		if hardQuota {
			return maybeDowngradeHardUsage(failoverHard, fmt.Sprintf("http_%d_usage", status), fiveHourUtil)
		}
		return failoverSoft, fmt.Sprintf("http_%d", status)
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return failoverSoft, fmt.Sprintf("http_%d", status)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if hardQuota {
			return maybeDowngradeHardUsage(failoverHard, fmt.Sprintf("http_%d_usage", status), fiveHourUtil)
		}
		if hardAuth {
			return failoverHard, fmt.Sprintf("http_%d_auth", status)
		}
		// Transient auth (token refresh race, brief 403): failover yes, mark no
		// until consecutive soft failures accumulate.
		return failoverSoft, fmt.Sprintf("http_%d", status)
	case status == http.StatusBadRequest:
		if hardQuota {
			return maybeDowngradeHardUsage(failoverHard, "http_400_usage", fiveHourUtil)
		}
		if softThrottle {
			return failoverSoft, "http_400_throttle"
		}
		if workBuddyPromptRefused(status, body) {
			return failoverPromptRefusal, "prompt_refused"
		}
		return failoverNone, ""
	default:
		return failoverNone, ""
	}
}

// busyRefusalText reports whether a refusal message says the model is busy
// rather than the account being at fault. Matching is done on the lowercased
// text callers already build.
func busyRefusalText(msg string) bool {
	return strings.Contains(msg, upstreamBusyErrorType) ||
		strings.Contains(msg, "10605") ||
		strings.Contains(msg, "is busy") ||
		strings.Contains(msg, "排进队列")
}

func maybeDowngradeHardUsage(class failoverClass, reason string, fiveHourUtil *float64) (failoverClass, string) {
	if class != failoverHard || fiveHourUtil == nil {
		return class, reason
	}
	if *fiveHourUtil < claudeUsageHardThresholdPct {
		return failoverSoft, fmt.Sprintf("%s (five_hour=%.1f<%.0f, softed)", reason, *fiveHourUtil, claudeUsageHardThresholdPct)
	}
	return class, fmt.Sprintf("%s (five_hour=%.1f)", reason, *fiveHourUtil)
}

func containsAnyHint(msg string, hints []string) bool {
	for _, hint := range hints {
		if hint != "" && strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func truncateReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 160 {
		return s
	}
	return s[:160] + "…"
}

// failoverResponseWriter buffers error responses so the gateway can try the
// next provider. Successful (2xx) responses flush through immediately so SSE
// streaming still works.
type failoverResponseWriter struct {
	base        http.ResponseWriter
	header      http.Header
	status      int
	buf         bytes.Buffer
	canDefer    bool
	passthrough bool
	discarded   bool
	wroteHeader bool
	// body is the turn this attempt produced while it was still its own to
	// buffer: the bytes of a held commit followed by those written to it. A
	// stream through a failover writer goes to the client as it comes, so only
	// the opening of that stream is kept — the part a cut turn is closed on top
	// of, kept up to the log cap for one that never wrote it (a bare writer, or
	// the failover writer with no client behind it). delivered is how much of
	// body the client already has, and is all a cut turn may be closed on top of
	// without sending anything twice.
	body      bytes.Buffer
	delivered int
	// held is how much of body was held back, never sent. Those bytes open the
	// turn once the loop settles on this attempt (commitHeaders), or go out
	// whole for a stream through deliver. Everything else in body is a prefix
	// the client already has.
	held int
	// spoke says the attempt wrote part of its turn, sent or held back: a
	// commit after it carries that part, so a cut turn closes on top of it.
	spoke bool
	// abandoned says the client left and took the attempt's turn with it: what
	// it was closing on is no longer there, so nothing more is kept or closed.
	abandoned bool
}

// abandon drops the turn an attempt committed to a client that has gone: the
// bytes it reached are a prefix of a stream nobody will read, so a cut turn
// must not be closed on top of them.
func (w *failoverResponseWriter) abandon() {
	w.abandoned = true
	w.body.Reset()
	w.delivered = 0
	w.held = 0
}

func newFailoverResponseWriter(base http.ResponseWriter, canDefer bool) *failoverResponseWriter {
	return &failoverResponseWriter{
		base:     base,
		header:   make(http.Header),
		canDefer: canDefer,
	}
}

// UpstreamHeader returns a header the upstream sent with the buffered error
// attempt (Retry-After), empty once the writer took over the real response.
func (w *failoverResponseWriter) UpstreamHeader(name string) string {
	if w.passthrough || w.discarded {
		return ""
	}
	return strings.TrimSpace(w.header.Get(name))
}

// deferring reports whether this attempt's response can still be thrown away
// for a backup: nothing has been committed to the real client yet.
func (w *failoverResponseWriter) deferring() bool {
	return !w.passthrough && !w.discarded
}

// forward sends part of a stream the attempt did not buffer in buf straight to
// the client, and flushes it so it leaves now: these are not bytes for the loop
// to decide on, they are the bytes the client is waiting for.
func (w *failoverResponseWriter) forward(p []byte) {
	if _, err := w.base.Write(p); err != nil {
		return
	}
	if f, ok := w.base.(http.Flusher); ok {
		f.Flush()
	}
}

// startStream opens a streamed response: a success by definition, committed
// before its first byte so the bytes below it flush as they come.
func (w *failoverResponseWriter) startStream(statusCode int) {
	if !w.discarded && !w.passthrough {
		w.commitHeaders(statusCode)
	}
}

// streamOpened reports that the attempt committed its headers as the start of
// a stream: a converted request opens its turn with the upstream response, so
// an error response reaches here as a committed turn that carries nothing yet.
func (w *failoverResponseWriter) streamOpened() bool {
	return !w.passthrough && !w.discarded && w.wroteHeader && w.delivered == 0
}

// failoverUnavailable reports that this attempt is the last one: the loop has
// no backup left, so whatever the attempt wrote is the client's answer.
func (w *failoverResponseWriter) failoverUnavailable() bool {
	return !w.canDefer
}

// streamClosed reports that the buffered stream ends with its protocol's
// closing frame, i.e. the turn was closed (by the upstream or by a converter
// that closed a cut stream itself) and only needs to be delivered.
func (w *failoverResponseWriter) streamClosed() bool {
	return closesUpstreamStream(w.stream())
}

// Prefix returns what the attempt handed to the client when it committed its
// headers: the start of its turn. A cut turn is completed on top of it, since
// the stream after it never arrived. Empty when the commit carried no body.
func (w *failoverResponseWriter) Prefix() []byte {
	if !w.spoke {
		return nil
	}
	return append([]byte(nil), w.body.Bytes()[:w.delivered]...)
}

// stream is the stream this attempt took, prefix excluded.
func (w *failoverResponseWriter) stream() []byte {
	longest := max(w.delivered, w.held)
	if longest > w.body.Len() {
		return nil
	}
	return w.body.Bytes()[longest:]
}

// StreamLen is the size of the stream the attempt took, prefix excluded.
func (w *failoverResponseWriter) StreamLen() int {
	return max(0, w.body.Len()-max(w.delivered, w.held))
}

// heldStart is how much of the turn body the client already has, i.e. how much
// of a replayed copy of the body is already sent. A commit that carried bytes
// wrote all of them, so the whole prefix counts; a commit that carried nothing
// sent none of them, so the body starts over.
func (w *failoverResponseWriter) heldStart() int {
	return max(w.delivered, w.held)
}

// deliver sends the rest of a cut turn — the stream that was read but, because
// this attempt is the last one, never went out — and returns the attempt with
// it recorded, so the turn can be closed on top of everything sent so far.
func (w *failoverResponseWriter) deliver(streamBody []byte, statusCode int) *failoverResponseWriter {
	hw := newSSEHeartbeatResponseWriter(w)
	hw.WriteHeader(statusCode)
	start := w.heldStart()
	if start > len(streamBody) {
		// The commit delivered more than this body holds; nothing is left over.
		start = len(streamBody)
	}
	_, _ = hw.Write(streamBody[start:])
	return w
}

func (w *failoverResponseWriter) recordsForRetry() {}

func (w *failoverResponseWriter) Header() http.Header {
	if w.passthrough {
		return w.base.Header()
	}
	return w.header
}

func (w *failoverResponseWriter) WriteHeader(statusCode int) {
	if w.discarded {
		return
	}
	if w.passthrough {
		if !w.wroteHeader {
			w.base.WriteHeader(statusCode)
			w.wroteHeader = true
		}
		return
	}
	if !w.canDefer || (statusCode > 0 && statusCode < 400) {
		w.commitHeaders(statusCode)
		return
	}
	w.status = statusCode
	w.wroteHeader = true
}

func (w *failoverResponseWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		// A write is the turn saying something: from then on its commit carries
		// a prefix of it, which a cut turn is closed on top of.
		w.spoke = true
	}
	if w.discarded {
		return len(p), nil
	}
	if w.passthrough {
		// Committed already: everything from here on goes straight to the
		// client, so only the turn's opening — what a stream through this writer
		// writes before its first forward — is kept; the rest is a prefix after
		// it that no cut turn reads back.
		// A partial write is not a write: the bytes the client did not take are
		// reported as unwritten so the caller stops copying and the client-gone
		// path is reached instead of the stream looking fully delivered.
		if w.abandoned {
			return len(p), nil
		}
		n, err := w.base.Write(p)
		if !w.discarded && w.held < w.delivered && w.body.Len() < passThroughLogBufferMax {
			keep := min(n, passThroughLogBufferMax-w.body.Len())
			_, _ = w.body.Write(p[:keep])
		}
		return n, err
	}
	// A deferred attempt keeps its stream to itself until the loop settles on
	// it: a Write here must not commit the headers (FlushError does that), or a
	// discarded attempt would already be on the wire.
	return w.buf.Write(p)
}

func (w *failoverResponseWriter) Flush() {
	if w.passthrough {
		if f, ok := w.base.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// commitHeaders records this attempt's status; the bytes and the real
// WriteHeader wait for FlushError, once the failover loop has settled on this
// attempt. Error statuses never wait here: the failover decision (and the
// Retry-After it reads back) needs the status as soon as the upstream says it.
func (w *failoverResponseWriter) commitHeaders(statusCode int) {
	if w.discarded || w.passthrough {
		return
	}
	dst := w.base.Header()
	for key, values := range w.header {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	if statusCode != 0 {
		w.base.WriteHeader(statusCode)
		w.status = statusCode
	} else if w.status != 0 {
		w.base.WriteHeader(w.status)
	}
	w.wroteHeader = true
	w.passthrough = true
	if w.buf.Len() > 0 {
		held := w.buf.Bytes()
		// These bytes leave with the headers, ahead of anything streamed after
		// them. A prefix written before the commit answers what the client
		// already has and is not kept again; a turn that only had them held back
		// (no commit before them) keeps them as its opening, which a cut turn
		// closes on top of.
		_, _ = w.base.Write(held)
		if w.held < w.delivered {
			// The held bytes are the turn's opening, not a prefix of it; its
			// stream, read after them, is the rest of the turn.
			_, _ = w.body.Write(held)
			w.held = w.delivered
		}
		w.buf.Reset()
	}
}

func (w *failoverResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *failoverResponseWriter) BufferedBody() []byte {
	return append([]byte(nil), w.buf.Bytes()...)
}

func (w *failoverResponseWriter) Discard() {
	w.discarded = true
	w.buf.Reset()
	w.body.Reset()
	w.delivered = 0
	w.held = 0
	w.spoke = false
	w.abandoned = false
	w.header = make(http.Header)
	w.status = 0
	w.wroteHeader = false
	w.passthrough = false
}

func (w *failoverResponseWriter) FlushError() {
	if w.passthrough || w.discarded {
		return
	}
	status := w.Status()
	w.commitHeaders(status)
}

// markProviderUnavailable records that providerID should show as
// "unavailable"/"异常" until the background recovery loop re-probes it.
// hard failures use the longer recovery interval; soft (repeated transient)
// failures use a shorter TTL. Safe for concurrent use.
func (s *Server) markProviderUnavailable(providerID string, hard bool, reason, retryAfter string, body []byte) {
	s.setProviderUnavailable(providerID, hard, reason, retryAfter, body, true)
}

// refreshProviderUnavailableCountdown extends nextRetryAt without re-logging
// a fresh "marked unavailable" event (used by the recovery loop).
func (s *Server) refreshProviderUnavailableCountdown(providerID string, hard bool, reason, retryAfter string, body []byte) {
	s.setProviderUnavailable(providerID, hard, reason, retryAfter, body, false)
}

// setProviderUnavailableUntil holds providerID out of rotation until at, without
// re-logging and without recomputing the rest. The moment comes from the
// account's own usage reading, which is the vendor's answer rather than anything
// the failure body said, so it is used as it is.
func (s *Server) setProviderUnavailableUntil(providerID string, at time.Time, hard bool, reason string) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" || at.IsZero() {
		return
	}
	s.providerAvailabilityMu.Lock()
	if s.providerAvailability == nil {
		s.providerAvailability = make(map[string]providerUnavailableState)
	}
	s.providerAvailability[providerID] = providerUnavailableState{
		nextRetry: at,
		hard:      hard,
		reason:    truncateReason(reason),
	}
	s.providerAvailabilityMu.Unlock()
}

// markProviderAvailable clears any tracked "unavailable" cooldown for
// providerID, e.g. once a live request or background probe succeeds again.
func (s *Server) markProviderAvailable(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return
	}
	s.providerAvailabilityMu.Lock()
	if s.providerAvailability != nil {
		delete(s.providerAvailability, providerID)
	}
	if s.providerSoftFailures != nil {
		delete(s.providerSoftFailures, providerID)
	}
	s.providerAvailabilityMu.Unlock()
}

// providerNextRetry reports the next scheduled recovery-probe time for
// providerID and whether it is currently tracked as unavailable.
func (s *Server) providerNextRetry(providerID string) (time.Time, bool) {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	st, ok := s.providerAvailability[providerID]
	return st.nextRetry, ok
}

// providerUnavailableIDs snapshots the currently-tracked unavailable provider
// IDs, for the background recovery loop to re-probe each cycle.
func (s *Server) providerUnavailableIDs() []string {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	ids := make([]string, 0, len(s.providerAvailability))
	for id := range s.providerAvailability {
		ids = append(ids, id)
	}
	return ids
}

func (s *Server) providerUnavailableSnapshot(providerID string) (providerUnavailableState, bool) {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	st, ok := s.providerAvailability[providerID]
	return st, ok
}

func (s *Server) bumpSoftFailure(providerID string) int {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	if s.providerSoftFailures == nil {
		s.providerSoftFailures = make(map[string]providerSoftFailureState)
	}
	now := time.Now()
	st := s.providerSoftFailures[providerID]
	if st.count == 0 || now.Sub(st.windowStart) > providerSoftFailureWindow {
		st = providerSoftFailureState{count: 1, windowStart: now}
	} else {
		st.count++
	}
	s.providerSoftFailures[providerID] = st
	return st.count
}

func (s *Server) clearSoftFailures(providerID string) {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	if s.providerSoftFailures != nil {
		delete(s.providerSoftFailures, providerID)
	}
}

// cachedClaudeFiveHourUtilization returns the (possibly stale) Claude OAuth
// 5h utilization for providerID when a usage report is cached.
func (s *Server) cachedClaudeFiveHourUtilization(providerID string) (float64, bool) {
	if s == nil || s.oauthUsageCache == nil {
		return 0, false
	}
	cacheKey := s.oauthUsageCacheKeyFor("claude", providerID)
	cached, ok := s.oauthUsageCache.getAllowStale(cacheKey)
	if !ok {
		return 0, false
	}
	report, ok := cached.(ClaudeOAuthUsageReport)
	if !ok || !report.Available || report.FiveHour == nil {
		return 0, false
	}
	return report.FiveHour.Utilization, true
}

// classifyProviderFailover is the provider-aware classifier: it adds what only
// the provider row can say.
//
// WorkBuddy first: that upstream answers 400 for a dozen distinct conditions and
// its own numeric code is the only thing that separates "this model is limited"
// from "this account is out". A model-level code advances the chain without
// painting the account 异常 — otherwise one busy model rests every account that
// touches it.
func (s *Server) classifyProviderFailover(providerID string, status int, body []byte, err error) (failoverClass, string) {
	if err == nil && s.providerIsWorkBuddy(providerID) {
		if class, reason, ok := classifyWorkBuddyFailure(status, body); ok {
			return class, reason
		}
	}
	var utilPtr *float64
	if util, ok := s.cachedClaudeFiveHourUtilization(providerID); ok {
		u := util
		utilPtr = &u
	}
	return classifyProviderFailover(status, body, err, utilPtr)
}

// providerIsWorkBuddy reports whether this provider is a WorkBuddy account.
func (s *Server) providerIsWorkBuddy(providerID string) bool {
	if s.router == nil || strings.TrimSpace(providerID) == "" {
		return false
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return false
	}
	return isWorkBuddyProvider(provider)
}

const (
	// providerCooldownFloor is the shortest rest any cooldown lasts, even when
	// the upstream says "retry now".
	providerCooldownFloor = 5 * time.Second
	// providerCooldownCap bounds what an upstream may talk the gateway into: a
	// vendor that answers "reset in three days" must not take an account out of
	// rotation until then, because the recovery loop is what proves it back.
	providerCooldownCap = 60 * time.Minute
)

// providerCooldownHint is what an upstream says about when to come back.
type providerCooldownHint struct {
	retryAfter time.Duration
	// resetsAt is an absolute moment (ms epoch) when the account's window
	// reopens; zero when the vendor did not say.
	resetsAt time.Time
}

func (h providerCooldownHint) ok() bool {
	return h.retryAfter > 0 || !h.resetsAt.IsZero()
}

// usageBucket is what an exhausted-window scan needs from one usage bucket.
type usageBucket interface{ resetAt() (time.Time, bool) }

func (b *ClaudeOAuthUsageBucket) resetAt() (time.Time, bool)  { return parseResetsAt(b.ResetsAt) }
func (b *ChatGPTOAuthUsageBucket) resetAt() (time.Time, bool) { return parseResetsAt(b.ResetsAt) }

// exhaustedReset picks the reset moment of the window that is actually spent.
// The soonest reopening is not the answer: a weekly plan window at 100% with a
// 5h window at 5% told the account reopens in nine minutes when in fact the
// next request would be refused for days. When nothing is spent the soonest
// window is the best guess left.
func exhaustedReset[T usageBucket](current time.Time, spent, idle []T, now time.Time) time.Time {
	for _, bucket := range spent {
		at, ok := bucket.resetAt()
		if !ok || !at.After(now) {
			continue
		}
		if current.IsZero() || at.After(current) {
			current = at
		}
	}
	if current.IsZero() {
		for _, bucket := range idle {
			if at, ok := bucket.resetAt(); ok && at.After(now) && (current.IsZero() || at.Before(current)) {
				current = at
			}
		}
	}
	return current
}

// cooldownHint reads the upstream's own retry timing: the Retry-After header,
// plus what the vendor already told the usage panel — Claude's unified 5h/7d
// window and ChatGPT's primary/secondary rate-limit window both carry the exact
// moment the account reopens, which beats any guess when the 429 body is terse.
func (s *Server) cooldownHint(providerID, retryAfter string, body []byte, now time.Time) providerCooldownHint {
	hint := parseProviderCooldownHint(retryAfter, body, now)
	if s == nil || s.oauthUsageCache == nil {
		return hint
	}
	claudeKey := s.oauthUsageCacheKeyFor("claude", providerID)
	chatgptKey := s.oauthUsageCacheKeyFor("chatgpt", providerID)
	if cached, ok := s.oauthUsageCache.getAllowStale(claudeKey); ok {
		if report, ok := cached.(ClaudeOAuthUsageReport); ok && report.Available {
			var spent, idle []*ClaudeOAuthUsageBucket
			for _, bucket := range []*ClaudeOAuthUsageBucket{report.FiveHour, report.SevenDay, report.SevenDayOverageIncluded} {
				if bucket == nil {
					continue
				}
				if bucket.Utilization >= claudeUsageHardThresholdPct {
					spent = append(spent, bucket)
				} else {
					idle = append(idle, bucket)
				}
			}
			hint.resetsAt = exhaustedReset(hint.resetsAt, spent, idle, now)
		}
	}
	if cached, ok := s.oauthUsageCache.getAllowStale(chatgptKey); ok {
		if report, ok := cached.(ChatGPTOAuthUsageReport); ok && report.Available {
			var spent, idle []*ChatGPTOAuthUsageBucket
			for index := range report.Buckets {
				bucket := &report.Buckets[index]
				if bucket.Utilization >= claudeUsageHardThresholdPct {
					spent = append(spent, bucket)
				} else {
					idle = append(idle, bucket)
				}
			}
			hint.resetsAt = exhaustedReset(hint.resetsAt, spent, idle, now)
		}
	}
	return hint
}

// providerQuotaRestCap bounds a rest that the account's own usage reading
// named, as opposed to one the rate ladder guessed. A Claude subscription's
// window reopens at most five hours after it was spent, so that is the longest
// rest a spent window can justify. The rate cap (30m) is for request throttling:
// holding a spent quota to it made the gateway ask again four minutes later, be
// refused again, and double the ladder — the "consecutive=3, 4, 5" run in the
// log is one account being retried inside a window that had hours left.
const providerQuotaRestCap = 5 * time.Hour

// claudeSpentWindowReset is the moment a Claude subscription's spent window
// reopens, read from the usage panel's cache. spent is false when no window is
// known to be used up right now, which is the common case: most Cooldowns are a
// throttle, not a quota.
func (s *Server) claudeSpentWindowReset(providerID string, now time.Time) (time.Time, bool) {
	if s == nil || s.oauthUsageCache == nil {
		return time.Time{}, false
	}
	cached, ok := s.oauthUsageCache.getAllowStale(s.oauthUsageCacheKeyFor("claude", providerID))
	if !ok {
		return time.Time{}, false
	}
	report, ok := cached.(ClaudeOAuthUsageReport)
	if !ok || !report.Available {
		return time.Time{}, false
	}
	var spent, idle []*ClaudeOAuthUsageBucket
	for _, bucket := range []*ClaudeOAuthUsageBucket{report.FiveHour, report.SevenDay, report.SevenDayOverageIncluded} {
		if bucket == nil {
			continue
		}
		if bucket.Utilization >= claudeUsageHardThresholdPct {
			spent = append(spent, bucket)
		} else {
			idle = append(idle, bucket)
		}
	}
	if len(spent) == 0 {
		return time.Time{}, false
	}
	at := exhaustedReset(time.Time{}, spent, idle, now)
	if at.IsZero() || !at.After(now) {
		return time.Time{}, false
	}
	return at, true
}

// cooldownTTL is how long providerID rests: the upstream's own hint when it
// names a time, the class default otherwise. Only "how long" comes from the
// upstream — whether the mark shows as hard still comes from the classifier,
// so a vendor phrase alone never paints an account 异常.
//
// The two ladders are deliberately apart (see providerRateRestBase):
//
//   - hard quota: at least providerQuotaRestFloor, then the vendor's reset time
//     when it names one, bounded by providerCooldownCap.
//   - soft / rate limit: providerRateRestBase doubling per consecutive failure
//     up to providerRateRestCap. A vendor's reset time only *extends* it when
//     the reset falls inside providerRateRestCap; a "resets in 6 days" carried
//     by a plain 429 is a plan window, not a request throttle, and letting it
//     through here is what used to park a healthy account for an hour.
func (s *Server) cooldownTTL(providerID, retryAfter string, body []byte, hard bool, now time.Time) time.Duration {
	hint := s.cooldownHint(providerID, retryAfter, body, now)
	if hard {
		// The window the vendor names is the real answer — the floor only fills
		// in when it names nothing, so a "reopens in 9m" is not rounded up to a
		// guess while a terse refusal still rests long enough to be worth it.
		ttl := providerQuotaRestFloor
		switch {
		case !hint.resetsAt.IsZero():
			// The account reopens exactly then; the floor would only make the
			// gateway wait longer than the vendor said.
			if until := hint.resetsAt.Sub(now); until > 0 {
				ttl = until
			}
		case hint.retryAfter > ttl:
			ttl = hint.retryAfter
		}
		if ttl > providerCooldownCap {
			ttl = providerCooldownCap
		}
		return ttl
	}
	ttl := providerRateRestBase << min(s.softFailureCount(providerID), 6)
	if ttl > providerRateRestCap {
		ttl = providerRateRestCap
	}
	if ttl < providerSoftUnavailableTTL {
		ttl = providerSoftUnavailableTTL
	}
	// The vendor's own Retry-After replaces the ladder when it strays from it:
	// shorter means "the throttle really clears that soon" and shorter is the
	// honest answer; longer means the account would be refused anyway. Either
	// way the rate cap still bounds it.
	if d := hint.retryAfter; d > 0 && d != ttl {
		ttl = d
		if ttl > providerRateRestCap {
			ttl = providerRateRestCap
		}
	}
	if !hint.resetsAt.IsZero() {
		if until := hint.resetsAt.Sub(now); until > ttl {
			// A spent subscription window reopens when it reopens, however far
			// out: the rate cap is for throttling, not for a quota that is gone.
			// Bounded by providerQuotaRestCap, and only when the usage reading
			// confirms a window really is spent — a vendor's "resets in 6 days"
			// carried by a plain 429 is still a plan window, not this.
			limit := providerRateRestCap
			spent := false
			if _, isSpent := s.claudeSpentWindowReset(providerID, now); isSpent {
				limit = providerQuotaRestCap
				spent = true
			}
			switch {
			case until <= limit:
				ttl = until
			case spent:
				// The window reopens past the cap: rest the cap, not whatever
				// rung of the rate ladder the failure count happened to be on.
				ttl = limit
			}
		}
	}
	return ttl
}

// softFailureCount is how many soft failures providerID has in the current
// window, without counting a new one.
func (s *Server) softFailureCount(providerID string) int {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	st, ok := s.providerSoftFailures[providerID]
	if !ok || time.Since(st.windowStart) > providerSoftFailureWindow {
		return 0
	}
	return st.count
}

// resetConsumed reports whether a cooldown wanted to last up to the vendor's
// own reset minute, in which case the failure that set it was the window
// closing, not the request being throttled: the old reset must not be handed
// to the next failure as well.
func (s *Server) resetConsumed(providerID, retryAfter string, body []byte, hard bool, ttl time.Duration, now time.Time) bool {
	if ttl <= 0 || ttl == providerSoftUnavailableTTL {
		// An un-hinted rate rest: nothing was spent.
		return false
	}
	hint := s.cooldownHint(providerID, retryAfter, body, now)
	if hint.resetsAt.IsZero() {
		return false
	}
	return hint.resetsAt.Sub(now) <= ttl
}

// parseProviderCooldownHint extracts the upstream's retry timing from a
// Retry-After header (seconds or HTTP date) and from the body's reset hints:
// Anthropic's unified `resets_at` (RFC3339 or ms epoch), Codex's
// `resets_in_seconds` / `resets_at`, and the same fields under `error` or
// `error.details`. Bodies that name no time fall back to the class default.
func parseProviderCooldownHint(retryAfter string, body []byte, now time.Time) providerCooldownHint {
	var hint providerCooldownHint
	if d, ok := parseRetryAfterHeader(retryAfter, now); ok {
		hint.retryAfter = d
	}
	payload := decodeErrorPayload(body)
	resetCandidates := []string{
		payloadString(payload, "resets_at"),
		payloadString(payload, "error", "details", "resets_at"),
		payloadString(payload, "error", "resets_at"),
	}
	for _, raw := range resetCandidates {
		if at, ok := parseResetsAt(raw); ok && at.After(now) {
			hint.resetsAt = at
			break
		}
	}
	// Resets are usually carried beside the error code at the top level, but
	// some envelopes nest them one level down.
	for _, src := range []map[string]any{
		payload,
		payloadMap(payload, "error"),
		payloadMap(payload, "error", "details"),
	} {
		if secs, ok := numericSeconds(src["resets_in_seconds"]); ok && secs > 0 {
			if d := secondsDuration(secs); d > hint.retryAfter {
				hint.retryAfter = d
			}
			break
		}
	}
	return hint
}

// decodeErrorPayload unwraps the handful of envelope shapes upstreams use
// ({"error":{...}}, {"error":{"details":{...}}}, a bare object, an SSE
// "data:" line) down to the innermost error object worth reading hints from.
func decodeErrorPayload(body []byte) map[string]any {
	text := strings.TrimSpace(string(body))
	if idx := strings.Index(text, "data:"); idx >= 0 && !strings.HasPrefix(text, "{") {
		text = strings.TrimSpace(text[idx+len("data:"):])
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		// Some vendors wrap the JSON in prose; read what round-trips.
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start < 0 || end <= start {
			return map[string]any{}
		}
		if err := json.Unmarshal([]byte(text[start:end+1]), &top); err != nil {
			return map[string]any{}
		}
	}
	return top
}

func payloadMap(payload map[string]any, keys ...string) map[string]any {
	current := payload
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		current = next
	}
	return current
}

// payloadString walks keys as objects and returns the first string leaf found.
func payloadString(payload map[string]any, keys ...string) string {
	current := payload
	for i, key := range keys {
		value, ok := current[key]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			return strings.TrimSpace(stringValue(value))
		}
		nested, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		current = nested
	}
	return ""
}

// parseResetsAt accepts Anthropic's RFC3339 timestamp and the ms-epoch number
// (or numeric string) Codex and the Claude usage endpoint use.
func parseResetsAt(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		return at, true
	}
	if ms, ok := numericSeconds(raw); ok && ms > 0 {
		return time.UnixMilli(int64(ms)), true
	}
	return time.Time{}, false
}

// parseRetryAfterHeader reads the seconds or HTTP-date form of Retry-After.
func parseRetryAfterHeader(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if secs, ok := numericSeconds(raw); ok {
		if secs <= 0 {
			return 0, false
		}
		return secondsDuration(secs), true
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := at.Sub(now); d > 0 {
			return d, true
		}
		return 0, false
	}
	return 0, false
}

func numericSeconds(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func secondsDuration(secs float64) time.Duration {
	if secs > float64(providerCooldownCap/time.Second) {
		return providerCooldownCap
	}
	return time.Duration(secs * float64(time.Second))
}

// setProviderUnavailable cools providerID down until the recovery loop proves
// it back. retryAfter/body carry the upstream's own timing hint; hard failures
// rest at least providerFailoverRecoveryInterval (see cooldownTTL).
func (s *Server) setProviderUnavailable(providerID string, hard bool, reason, retryAfter string, body []byte, logMark bool) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return
	}
	now := time.Now()
	ttl := s.cooldownTTL(providerID, retryAfter, body, hard, now)
	reason = truncateReason(reason)
	s.providerAvailabilityMu.Lock()
	if s.providerAvailability == nil {
		s.providerAvailability = make(map[string]providerUnavailableState)
	}
	s.providerAvailability[providerID] = providerUnavailableState{
		nextRetry: now.Add(ttl),
		hard:      hard,
		reason:    reason,
	}
	s.providerAvailabilityMu.Unlock()
	if s.resetConsumed(providerID, retryAfter, body, hard, ttl, now) {
		// The rest ran until the window the vendor named: that reset is spent,
		// and the next failure starts a fresh countdown instead of inheriting
		// the one that just ended.
		s.clearSoftFailures(providerID)
	}
	if !logMark {
		return
	}
	kind := "soft"
	if hard {
		kind = "hard"
	}
	msg := fmt.Sprintf("provider=%s kind=%s reason=%s retry_in=%s", providerID, kind, reason, ttl.Round(time.Second))
	if s.logs != nil {
		s.logs.AddApp("warn", "provider marked unavailable", msg)
	}
	slog.Warn("provider marked unavailable", "provider", providerID, "hard", hard, "reason", reason, "retry_in", ttl)
}

// applyProviderAvailability overlays the live-request availability tracker
// onto a provider destined for the client: a provider that just failed a real
// API call shows healthStatus "unavailable" (rendered "异常" in the console)
// with nextRetryAt, even if its persisted HealthStatus still reflects an
// earlier manual "获取模型" check.
func (s *Server) applyProviderAvailability(provider *domain.Provider) {
	if provider == nil {
		return
	}
	if provider.Disabled {
		// 禁用后不展示「异常 / N秒后重试」覆盖层，也不诱导后台重探。
		provider.NextRetryAt = ""
		return
	}
	nextRetry, unavailable := s.providerNextRetry(provider.ID)
	if !unavailable {
		return
	}
	provider.HealthStatus = "unavailable"
	provider.NextRetryAt = nextRetry.Format(time.RFC3339)
}

// recordProviderRequestOutcome updates the runtime availability tracker for
// providerID based on one completed upstream attempt. Failover-worthy soft
// failures only paint "异常" after consecutive hits; hard quota/auth failures
// mark immediately. Independent of whether a fallback chain exists.
func (s *Server) recordProviderRequestOutcome(providerID string, status int, body []byte, err error) {
	_ = s.applyProviderFailoverOutcome(providerID, status, body, "", err)
}

// applyProviderFailoverOutcome classifies the attempt, updates soft/hard
// availability tracking, and returns whether the caller should advance to the
// next backup provider. retryAfter is the attempt's upstream Retry-After header,
// which (with the body's reset hints) decides how long a cooldown lasts.
func (s *Server) applyProviderFailoverOutcome(providerID string, status int, body []byte, retryAfter string, err error) bool {
	return s.applyProviderFailoverOutcomeCtx(context.Background(), providerID, status, body, retryAfter, err)
}

// applyProviderFailoverOutcomeCtx is applyProviderFailoverOutcome with the
// request's own context, which is where a Qoder failure waits to be charged
// (see provider_cooldown_qoder.go).
func (s *Server) applyProviderFailoverOutcomeCtx(ctx context.Context, providerID string, status int, body []byte, retryAfter string, err error) bool {

	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	if s.router != nil {
		if provider, lookupErr := s.router.ProviderByID(providerID); lookupErr == nil && provider.Disabled {
			// 已禁用：不再写入 unavailable，也不参与后续重探刷日志。
			s.markProviderAvailable(providerID)
			class, _ := s.classifyProviderFailover(providerID, status, body, err)
			return class != failoverNone
		}
	}
	class, reason := s.classifyProviderFailover(providerID, status, body, err)
	if class == failoverNone {
		// The vendor's own quota wording outlives the classifier: a 400 that
		// spells out insufficient_quota advances the chain but is not worth a
		// cooldown mark, while a 429 that does is already soft here.
		if err == nil {
			s.markProviderAvailable(providerID)
		}
		return false
	}
	switch class {
	case failoverHard:
		if s.applyQoderCooldown(ctx, providerID, class, status, body, retryAfter, reason, err) {
			return true
		}
		s.clearSoftFailures(providerID)
		s.markProviderUnavailable(providerID, true, reason, retryAfter, body)
	case failoverSoft:
		if s.applyQoderCooldown(ctx, providerID, class, status, body, retryAfter, reason, err) {
			return true
		}
		count := s.bumpSoftFailure(providerID)
		if count >= providerSoftFailureThreshold {
			s.markProviderUnavailable(providerID, false, fmt.Sprintf("%s consecutive=%d", reason, count), retryAfter, body)
		} else if s.logs != nil {
			s.logs.AddApp("info", "provider soft failure", fmt.Sprintf(
				"provider=%s consecutive=%d/%d reason=%s",
				providerID, count, providerSoftFailureThreshold, reason,
			))
		}
	case failoverPromptRefusal:
		// The account is not the problem: ask the next provider, mark nothing.
	case failoverBusy:
		// The model is busy, the account is fine: keep it out of the cooldown
		// ladder and drop any soft-failure count it may have picked up from
		// earlier busy answers, or three of them would still rest it.
		s.clearSoftFailures(providerID)
		if s.logs != nil {
			s.logs.AddApp("info", "provider busy, next account tried", fmt.Sprintf("provider=%s reason=%s", providerID, reason))
		}
	}
	return true
}

func (s *Server) persistAPIKeyActiveProvider(keyID, providerID string) {
	updated, err := s.router.SetAPIKeyActiveProvider(keyID, providerID)
	if err != nil {
		return
	}
	if s.apiKeyStore != nil {
		_ = s.apiKeyStore.UpdateAPIKey(updated)
	}
}

// errProviderNotAssigned marks a request refused because the API key's owner is
// no longer granted any provider on the key's chain. Handlers map it to 403 so
// the client and the request log show a permission denial instead of a
// misleading 502 Bad Gateway.
var errProviderNotAssigned = errors.New("permission denied")

// upstreamErrorStatus maps an execute-flow error to the client-facing status.
// Permission denials are 403, a provider at its own concurrent limit or a key at
// its own quota is 429 (both with Retry-After), everything else stays a 502
// upstream failure.
func upstreamErrorStatus(err error) int {
	if errors.Is(err, errProviderNotAssigned) {
		return http.StatusForbidden
	}
	if errors.Is(err, errProviderSaturated) {
		return http.StatusTooManyRequests
	}
	if errors.Is(err, errKeyQuotaExceeded) {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

// executeProtocolFlowWithFailover works through the key's whole provider chain
// (see failoverAttemptOrder) when an upstream failure looks like
// quota/auth/availability, instead of stopping at the end of the chain behind
// the sticky provider.
func (s *Server) executeProtocolFlowWithFailover(
	w http.ResponseWriter,
	r *http.Request,
	route domain.Route,
	decision domain.RouteDecision,
	model string,
	req map[string]any,
	clientProtocol domain.Protocol,
	skipIncomingAuth bool,
	matchedKey domain.APIKey,
	gatewayKeyMatched bool,
) (int, TokenUsage, []byte, domain.RouteDecision, string, error) {
	// One request, one ledger of failures on probation: a Qoder failure is only
	// charged once some account answers this turn (see provider_cooldown_qoder.go).
	r = r.WithContext(withQoderBlame(r.Context()))
	// The key's own quota is decided before any provider is touched: switching
	// accounts would not help, since every provider on the chain is refused by the
	// same key, and a refusal that reaches an upstream spends that account's
	// quota for nothing. Headers go on `w` here because the handlers still own the
	// body — they write it in their own protocol shape once WriteHeader is called.
	if gatewayKeyMatched && keyHasQuota(matchedKey) {
		admission := s.admitKeyQuota(matchedKey, estimateKeyQuotaPrompt(req), time.Now())
		if refusal := admission.refusal; refusal != nil {
			w.Header().Set("Retry-After", strconv.FormatInt(refusal.RetryAfterSeconds, 10))
			w.Header().Set("X-Quota-Reset-At", refusal.ResetAt)
			return http.StatusTooManyRequests, TokenUsage{}, nil, decision, model, refusal
		}
		defer admission.release()
	}
	// 这个 Key 自己设的等待窗口，沿 context 传到真正发请求的地方（见
	// effectiveFirstPacketTimeout）。挂在 r 上而不是当参数层层传：发送点在每种
	// 协议各自的 send 闭包深处，加一个参数要改一长串签名。
	if gatewayKeyMatched {
		if seconds := clampFirstPacketTimeoutSec(matchedKey.FirstPacketTimeoutSec); seconds > 0 {
			r = r.WithContext(withAPIKeyFirstPacketTimeout(r.Context(), time.Duration(seconds)*time.Second))
		}
	}

	if !gatewayKeyMatched {
		timer := startAttempt()
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		s.recordProviderRequestOutcome(decision.ProviderID, status, body, err)
		s.noteProviderUsage(decision.ProviderID, usage)
		noteSingleAttemptTrace(r.Context(), decision.ProviderID, model, status, usage, err, timer.millis())
		return status, usage, body, decision, model, err
	}

	chain := apiKeyProviderChain(route.ProviderID, matchedKey.FallbackProviderIDs)
	if len(chain) == 0 {
		timer := startAttempt()
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		s.recordProviderRequestOutcome(decision.ProviderID, status, body, err)
		s.noteProviderUsage(decision.ProviderID, usage)
		noteSingleAttemptTrace(r.Context(), decision.ProviderID, model, status, usage, err, timer.millis())
		return status, usage, body, decision, model, err
	}
	// 普通用户的 Key 只能使用管理员当前授权的 Provider。授权是在“用户”页面随时可
	// 改的，而 Key 的路由/兜底链是创建时写死的，所以必须在每次请求时重新校验，
	// 不能只依赖创建 Key 时的校验（否则撤销授权后旧 Key 会一直可用）。
	// 先过滤整条链再排尝试顺序，这样即使 ActiveProviderID 指向已被撤销的 Provider，
	// 也不会跳过链上仍被授权的首选 Provider。
	if allowedChain := s.allowedProviderChainForKey(matchedKey, chain); allowedChain != nil && len(allowedChain) != len(chain) {
		kept := make(map[string]bool, len(allowedChain))
		for _, providerID := range allowedChain {
			kept[providerID] = true
		}
		for _, providerID := range chain {
			if kept[providerID] {
				continue
			}
			if !s.providerAllowedForKeyOwner(matchedKey, providerID) {
				s.logs.AddApp("warn", "provider not assigned to key owner, skipped", fmt.Sprintf("key=%s owner=%s provider=%s", matchedKey.ID, matchedKey.OwnerUserID, providerID))
			}
		}
		chain = allowedChain
		if len(chain) == 0 {
			// 账号被禁用时整条链都会被滤空（见 principalForUserID）。必须在这里
			// 先认出来，否则报错会指向"没有分配 Provider"，运维会去查一份其实
			// 完全正常的授权表。
			if s.keyOwnerDisabled(matchedKey) {
				return http.StatusForbidden, TokenUsage{}, nil, decision, model,
					fmt.Errorf("%w: the account owning this API key is disabled", errProviderNotAssigned)
			}
			// 管理员 Key 没有授权表，整条链只剩禁用/已删除的 Provider 时，这是
			// 上游不可用，不是权限问题。
			if strings.TrimSpace(matchedKey.OwnerUserID) == "" {
				return http.StatusServiceUnavailable, TokenUsage{}, nil, decision, model,
					fmt.Errorf("no enabled provider on the key's provider chain")
			}
			return http.StatusForbidden, TokenUsage{}, nil, decision, model,
				fmt.Errorf("%w: provider %q is not assigned to your account", errProviderNotAssigned, strings.TrimSpace(route.ProviderID))
		}
	}
	// A conversation stays on the account that has its prompt cache, even when
	// another conversation on this same key has moved the key-level sticky
	// provider somewhere else.
	sessionKey := sessionAffinityKey(r, req, matchedKey.ID, route.ProviderID, matchedKey.ScheduleMode, matchedKey.FallbackProviderIDs...)
	attempts := s.failoverAttemptOrder(scheduleInput{
		chain:   chain,
		active:  matchedKey.ActiveProviderID,
		mode:    matchedKey.ScheduleMode,
		keyID:   matchedKey.ID,
		advance: true,
	})
	attempts, pinnedProviderID := s.applySessionPin(sessionKey, attempts)
	// 能直通就别转换：同一个上游同时支持多种协议时，让下游的协议决定走哪一条。
	attempts = s.preferNativeProtocol(attempts, clientProtocol, pinnedProviderID)
	// spread mode decides a conversation's account here, before the request is
	// sent: two conversations that start at the same moment would both read an
	// empty table and pick the same account. A turn that settles nothing (every
	// candidate skipped, or the whole chain failing) must not leave the
	// conversation anchored to an account that never answered it.
	reservedProviderID := ""
	if isScheduleModeSpread(matchedKey.ScheduleMode) && sessionKey != "" && pinnedProviderID == "" && len(attempts) > 0 {
		reservedProviderID = attempts[0]
		s.setSessionPinFor(sessionKey, reservedProviderID, spreadSessionAffinityTTL)
	}
	served := false
	defer func() {
		if reservedProviderID != "" && !served {
			s.clearSessionPin(sessionKey)
		}
	}()
	noteScheduleDecision(r.Context(), matchedKey.ScheduleMode, attempts, pinnedProviderID)

	var lastStatus int
	var lastUsage TokenUsage
	var lastBody []byte
	var lastErr error
	var lastDecision domain.RouteDecision = decision
	lastModel := model
	requestModel, _ := req["model"].(string)
	// 索引循环而不是 range：原地重试要把 i 退回去重打同一个节点。
	sameNodeRetries := 0
	for i := 0; i < len(attempts); i++ {
		providerID := attempts[i]
		// 换到下一个节点，重试配额重新算。
		if i > 0 && attempts[i-1] != providerID {
			sameNodeRetries = 0
		}
		// 被管理员停用的 Provider 对所有 Key 都不可用，管理员自己的 Key 也一样：
		// 停用是“这个账号先别发了”，不是按用户授权。
		if provider, err := s.router.ProviderByID(providerID); err == nil && provider.Disabled {
			lastErr = fmt.Errorf("provider %q is disabled by administrator", providerID)
			s.logs.AddApp("warn", "provider disabled, skipped", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, providerID))
			noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Reason: attemptReasonDisabled, Skipped: true})
			continue
		}
		// 按这次请求的协议判定这一跳该直通还是转换。用不带协议的老调用会把
		// route 的 OutputProtocol 捡回来——同一把 Key 换条端点就变成"转换未实现"。
		attemptDecision, err := s.router.DecideForProviderProtocol(route.ID, providerID, clientProtocol)
		if err != nil {
			lastErr = err
			noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Reason: attemptReasonUndecidable, Error: truncateReason(err.Error()), Skipped: true})
			continue
		}
		lastDecision = attemptDecision

		attemptModel := model
		attemptKey := matchedKey
		if providerID != strings.TrimSpace(route.ProviderID) {
			override := ""
			if matchedKey.FallbackModelOverrides != nil {
				override = strings.TrimSpace(matchedKey.FallbackModelOverrides[providerID])
			}
			// 跨协议备选不再强制要求锁定模型：模型解析会按备选自己的目录兜底
			// （见 applyProviderModelFallback）。但转换器本身仍然要看两端：这条
			// 链路真的要走转换、而对应的转换器没实现时，发出去只会换来一句 400，
			// 跳过比白打一次好（与 executeProtocolFlow 的 switch 表同一份事实）。
			if override != "" {
				attemptKey.ModelOverride = override
			}
			if !attemptServesClientProtocol(attemptDecision, clientProtocol) {
				s.logs.AddApp("warn", "provider failover skipped unsupported conversion", fmt.Sprintf("key=%s provider=%s %s->%s", matchedKey.ID, providerID, clientProtocol, attemptDecision.InputProtocol))
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Reason: attemptReasonUnsupportedConversion, Skipped: true})
				continue
			}
		}
		attemptProvider, _ := s.router.ProviderByID(providerID)
		if resolved, _ := resolveConsumerModelOnProvider(attemptProvider, attemptKey, true, requestModel); strings.TrimSpace(resolved) != "" {
			attemptModel = resolved
		}
		attemptReq := cloneRequestMap(req)
		attemptReq["model"] = attemptModel

		// 这台上游实测支持客户端协议时，先按客户端协议直通一次：直通只搬字节，
		// 不经过转换器，长尾字段与流式事件最不容易被改写。失败且尚未向客户端
		// 提交任何字节时，再退回它声明的协议做转换重试——转换是保底，不是首选。
		if nativeProtocol, ok := nativeProtocolForProvider(attemptProvider, clientProtocol); ok {
			nativeDecision := attemptDecision
			nativeDecision.Action = "pass_through"
			nativeDecision.InputProtocol = nativeProtocol
			nativeDecision.ConversionLabel = conversionLabel(nativeProtocol, nativeProtocol)
			nativeReq := cloneRequestMap(attemptReq)
			nativeReq["model"] = attemptModel
			nativeWriter := newFailoverResponseWriter(w, true)
			nativeTimer := startAttempt()
			nativeR := r.WithContext(withUpstreamProtocolOverride(r.Context(), nativeProtocol))
			status, usage, body, nativeErr := s.executeProtocolFlow(nativeWriter, nativeR, route, nativeDecision, attemptModel, nativeReq, clientProtocol, skipIncomingAuth)
			elapsed := nativeTimer.millis()
			if nativeErr == nil && status >= 200 && status < 300 {
				if !nativeWriter.passthrough {
					nativeWriter.FlushError()
				}
				respBody := body
				if len(respBody) == 0 {
					respBody = nativeWriter.BufferedBody()
				}
				s.noteKeyServingProvider(matchedKey.ID, providerID)
				s.setSessionPinFor(sessionKey, providerID, s.sessionPinTTL(matchedKey.ScheduleMode))
				served = true
				qoderBlameBucketFrom(r.Context()).flush(s)
				s.noteProviderUsage(providerID, usage)
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed, Reason: attemptReasonServed})
				return status, usage, respBody, nativeDecision, attemptModel, nil
			}
			if nativeWriter.deferring() {
				nativeWriter.Discard()
				detail := fmt.Sprintf("status=%d", status)
				if nativeErr != nil {
					detail += " err=" + truncateReason(nativeErr.Error())
				} else if len(body) > 0 {
					detail += " body=" + truncateReason(string(body))
				}
				s.logs.AddApp("warn", "native protocol pass-through failed, retrying conversion", fmt.Sprintf(
					"key=%s provider=%s client=%s %s", matchedKey.ID, providerID, clientProtocol, detail))
				errText := ""
				if nativeErr != nil {
					errText = nativeErr.Error()
				}
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{
					ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed,
					Reason:   attemptReasonNativePassthroughFail,
					Error:    truncateReason(firstNonEmpty(errText, string(body))),
					Deferred: true,
				})
				markTimingFlag(r.Context(), timingFlagFailoverRetry)
				if t := requestTimingFrom(r.Context()); t != nil {
					t.resetUpstreamMarks()
				}
				// 落到下面的转换尝试：同一个 provider，按它声明的协议再发一次。
			} else {
				// 已经开始向客户端写（流已提交），不能再换请求形状重放。
				nativeWriter.FlushError()
				if nativeErr == nil {
					nativeErr = fmt.Errorf("native pass-through ended with HTTP %d", status)
				}
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{
					ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed,
					Reason: attemptReasonNativePassthroughFail, Error: truncateReason(nativeErr.Error()),
				})
				return status, usage, body, nativeDecision, attemptModel, nativeErr
			}
		}

		canDefer := i < len(attempts)-1
		writer := newFailoverResponseWriter(w, canDefer)
		timer := startAttempt()
		status, usage, body, execErr := s.executeProtocolFlow(writer, r, route, attemptDecision, attemptModel, attemptReq, clientProtocol, skipIncomingAuth)
		elapsed := timer.millis()
		if execErr != nil {
			// Keep the attempt's body: a refusal that was held back (nothing
			// written yet) is what the last attempt answers with.
			lastStatus, lastUsage, lastBody, lastErr, lastModel = status, usage, body, execErr, attemptModel
			opened := writer.streamOpened()
			// Client already gone: never advance the chain (backup would reuse the
			// same canceled context and surface a fake GLM/fallback cancel).
			if isRequestContextCanceled(execErr) || (r.Context() != nil && r.Context().Err() != nil) {
				// Settling the attempt (its headers and held turn, if still
				// deferred) but dropping what it wrote after that: the bytes a
				// client that left had reached are a prefix a cut turn must not
				// be closed on top of.
				writer.FlushError()
				writer.abandon()
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed, Reason: attemptReasonClientGone, Error: truncateReason(execErr.Error())})
				return status, usage, body, attemptDecision, attemptModel, execErr
			}
			// A stream was opened with its upstream response: an error response
			// that arrives there is the upstream's answer, not a cut turn, and the
			// account never got the request — so the next one may still have it.
			// A converted turn holds its opening until the answer itself begins
			// (converterStreamWriter), so an attempt that reached the upstream and
			// failed before any content is still deferrable: nothing of it has
			// reached the client, and the next account can answer the turn
			// (magpie mayAskAgain, fallback.go:2345).
			if opened && writer.canDefer {
				writer.Discard()
				markTimingFlag(r.Context(), timingFlagFailoverRetry)
				if t := requestTimingFrom(r.Context()); t != nil {
					t.resetUpstreamMarks()
				}
				s.logs.AddApp("warn", "provider failover after error response", fmt.Sprintf("key=%s from=%s status=%d", matchedKey.ID, providerID, status))
				// The hop spent whatever the stream reported before it failed:
				// an answer nobody sees still costs the account (magpie records
				// every try's usage, gateway.go:3004).
				s.noteProviderUsage(providerID, usage)
				s.releaseSessionPin(sessionKey, pinnedProviderID, providerID)
				continue
			}
			// Busy, not broken: an admission refusal moves to the next account
			// and must never be counted as a provider failure, or a busy but
			// healthy account would be painted 异常 and cooled down.
			if errors.Is(execErr, errProviderSaturated) {
				lastStatus = http.StatusTooManyRequests
				noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: http.StatusTooManyRequests, Ms: elapsed, Reason: attemptReasonSaturated, Error: truncateReason(execErr.Error()), Deferred: canDefer})
				if canDefer {
					writer.Discard()
					markTimingFlag(r.Context(), timingFlagFailoverRetry)
					if t := requestTimingFrom(r.Context()); t != nil {
						t.resetUpstreamMarks()
					}
					s.logs.AddApp("info", "provider skipped at concurrent limit", fmt.Sprintf("key=%s provider=%s err=%s", matchedKey.ID, providerID, execErr.Error()))
					continue
				}
				writer.Discard()
				// The account's lane, not the row's: a grouped provider's
				// budget lives under its LaneGroup key.
				laneWait := providerRetryAfterSeconds
				if provider, lookupErr := s.router.ProviderByID(providerID); lookupErr == nil {
					laneWait = s.providerLanes.retryAfterSecondsFor(provider, time.Now())
				}
				w.Header().Set("Retry-After", strconv.Itoa(laneWait))
				return http.StatusTooManyRequests, TokenUsage{}, nil, attemptDecision, attemptModel, execErr
			}
			transportFailoverWorthy := s.applyProviderFailoverOutcomeCtx(r.Context(), providerID, 0, nil, "", execErr)
			// Transport errors are worth another account only while nothing of
			// this turn has reached the client. Once the first byte is out —
			// a streamed request, or a deferred attempt whose turn was
			// committed — the client's time-to-first-token has been paid, so a
			// cut is the answer: it stays with the account that started it.
			// A busy refusal is no exception: a stream that already carried
			// content cannot be replayed onto a second account.
			if !writer.deferring() {
				transportFailoverWorthy = false
			}
			busy := busyRefusalText(strings.ToLower(execErr.Error()))
			transportReason := attemptReasonTransportError
			if errors.Is(execErr, errUpstreamFirstPacketTimeout) {
				transportReason = attemptReasonFirstPacketTimeout
			}
			if busy {
				transportReason = attemptReasonModelBusy
			}
			noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Ms: elapsed, Reason: transportReason, Error: truncateReason(execErr.Error()), Deferred: canDefer && transportFailoverWorthy})
			if canDefer && transportFailoverWorthy {
				writer.Discard()
				markTimingFlag(r.Context(), timingFlagFailoverRetry)
				if t := requestTimingFrom(r.Context()); t != nil {
					t.resetUpstreamMarks()
				}
				// 原地重试：传输错误多半是瞬时抖动（连接被重置、上游一瞬间
				// 打嗝），换账号解决不了问题，反而把整条链路白推一格。次数由
				// Key 的 SameProviderRetries 决定，0 = 旧行为（直接换下一个）。
				// 模型繁忙不重试：那是上游在排队，再打一次只会排得更后面。
				if !busy && sameNodeRetries < matchedKey.SameProviderRetries {
					sameNodeRetries++
					s.logs.AddApp("info", "provider retried on the same node", fmt.Sprintf("key=%s provider=%s try=%d/%d err=%s", matchedKey.ID, providerID, sameNodeRetries, matchedKey.SameProviderRetries, execErr.Error()))
					i--
					continue
				}
				s.logs.AddApp("warn", "provider failover after transport error", fmt.Sprintf("key=%s from=%s err=%s", matchedKey.ID, providerID, execErr.Error()))
				s.noteProviderUsage(providerID, usage)
				s.releaseSessionPin(sessionKey, pinnedProviderID, providerID)
				continue
			}
			// 尚未向客户端提交任何字节时交给上层写错误响应（例如空流重试耗尽）。
			if !writer.passthrough && writer.buf.Len() == 0 && writer.status == 0 {
				writer.Discard()
			} else {
				writer.FlushError()
			}
			// 最后一家：拒绝没有写手去写它（转换链把它押在返回值里），在这里
			// 补上，客户端才拿得到上游的原话而不是一个空响应。
			if !canDefer && len(body) > 0 && !writer.wroteHeader && writer.status == 0 {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}
			return status, usage, body, attemptDecision, attemptModel, execErr
		}

		respBody := body
		if len(respBody) == 0 {
			respBody = writer.BufferedBody()
		}
		lastStatus, lastUsage, lastBody, lastErr, lastModel = status, usage, respBody, nil, attemptModel

		if r.Context() != nil && r.Context().Err() != nil {
			// Upstream may have returned an error status, but the client is
			// already gone — do not burn the fallback chain.
			if !writer.passthrough {
				writer.FlushError()
			}
			noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed, Reason: attemptReasonClientGone})
			return status, usage, respBody, attemptDecision, attemptModel, r.Context().Err()
		}

		attemptFailoverWorthy := s.applyProviderFailoverOutcomeCtx(r.Context(), providerID, status, respBody, writer.UpstreamHeader("Retry-After"), nil)
		if canDefer && attemptFailoverWorthy {
			noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed, Reason: attemptFailoverReason(respBody), Error: summarizeUpstreamHTTPError(status, respBody), Deferred: true})
			writer.Discard()
			markTimingFlag(r.Context(), timingFlagFailoverRetry)
			if t := requestTimingFrom(r.Context()); t != nil {
				t.resetUpstreamMarks()
			}
			// 同一节点先重试：5xx 和「模型繁忙」在这一层就是可重试的瞬时故障。
			// 4xx（参数错误、认证失败）不走这条路——重试多少次结果都一样，那是
			// 该换节点或该直接报错的场景。
			retryable := status >= 500 || status == http.StatusTooManyRequests
			if retryable && sameNodeRetries < matchedKey.SameProviderRetries {
				sameNodeRetries++
				s.logs.AddApp("info", "provider retried on the same node", fmt.Sprintf("key=%s provider=%s try=%d/%d status=%d", matchedKey.ID, providerID, sameNodeRetries, matchedKey.SameProviderRetries, status))
				i--
				continue
			}
			s.logs.AddApp("warn", "provider failover after upstream error", fmt.Sprintf("key=%s from=%s status=%d", matchedKey.ID, providerID, status))
			s.releaseSessionPin(sessionKey, pinnedProviderID, providerID)
			continue
		}

		// Record which provider actually served this key. Two writers share the
		// slot and they must not fight: a session pin (one conversation) choosing
		// its pinned account, and the failure/recovery path moving the key. The
		// immediate writer keeps the key-level ActiveProviderID in step with the
		// traffic; the debounced writer additionally marks the key "serving" for
		// the console, because the old rule never wrote anything when a session
		// pin was what chose the provider, and the console then kept crowning the
		// preferred provider while the logs showed a fallback serving every turn.
		desiredActive := ""
		if providerID != strings.TrimSpace(route.ProviderID) {
			desiredActive = providerID
		}
		pinnedThisTurn := pinnedProviderID != "" && providerID == pinnedProviderID
		// Spread mode is per conversation by definition: every conversation on
		// this key sits somewhere else, so letting each of them write the
		// key-level sticky provider would make it flip on every request (one
		// row write each) and the value would mean nothing.
		if isScheduleModeSpread(matchedKey.ScheduleMode) && sessionKey != "" {
			pinnedThisTurn = true
		}
		if !pinnedThisTurn && strings.TrimSpace(matchedKey.ActiveProviderID) != desiredActive {
			s.persistAPIKeyActiveProvider(matchedKey.ID, desiredActive)
			if desiredActive != "" {
				s.logs.AddApp("info", "api key active provider updated", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, desiredActive))
			} else {
				s.logs.AddApp("info", "api key restored preferred provider", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, route.ProviderID))
			}
		}
		s.noteKeyServingProvider(matchedKey.ID, providerID)
		// The conversation now belongs to the account that answered it, and that
		// account has now carried this request's work. An answer that was itself
		// failover-worthy (the last candidate is out of accounts to move to) is
		// not an answer: pinning the conversation to it would keep sending the
		// next turns to an account that just refused this one.
		if !attemptFailoverWorthy {
			s.setSessionPinFor(sessionKey, providerID, s.sessionPinTTL(matchedKey.ScheduleMode))
			served = true
			// An account answered this turn, so the refusals that came before it
			// really were those accounts' own.
			qoderBlameBucketFrom(r.Context()).flush(s)
		}
		s.noteProviderUsage(providerID, usage)
		noteScheduleAttempt(r.Context(), monitor.RequestAttempt{ProviderID: providerID, Model: attemptModel, Status: status, Ms: elapsed, Reason: attemptReasonServed})
		if !writer.passthrough {
			writer.FlushError()
		}
		return status, usage, respBody, attemptDecision, attemptModel, nil
	}

	if lastErr != nil {
		return lastStatus, lastUsage, lastBody, lastDecision, lastModel, lastErr
	}
	return lastStatus, lastUsage, lastBody, lastDecision, lastModel, nil
}

// attemptFailoverReason names why an otherwise successful-looking attempt went
// to the next account: a stream the upstream cut mid-answer is not an HTTP
// error, so it must not be reported as one.
func attemptFailoverReason(body []byte) string {
	if isUpstreamStreamInterruption(errors.New(string(body))) {
		return attemptReasonStreamInterrupted
	}
	if busyRefusalText(strings.ToLower(string(body))) {
		return attemptReasonModelBusy
	}
	return attemptReasonUpstreamError
}

func cloneRequestMap(req map[string]any) map[string]any {
	if req == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(req))
	for key, value := range req {
		out[key] = value
	}
	return out
}

// providerFailoverRecoveryInterval 控制回切探测周期：前置 Provider 恢复后，
// 最多等待一个周期即可自动切回，避免长时间滞留在备用 Provider。
//
// 这里和「上游冷却」用同一个常量（见 cooldownTTL），所以把它压到 30s 就等于让
// 被 429/5xx 打下线的账号最多 30s 就重新参与调度，会放大打爆上游账号的风险。
// 因此回切探测走 independent 的 providerRecoveryProbeInterval：它只发一次很轻的
// /models 探测，与真实请求的冷却计时互不影响。
const providerFailoverRecoveryInterval = 2 * time.Minute

// providerRecoveryProbeInterval 是回切探测的周期：比上游冷却短得多，这样
// 「首选已经恢复」在控制台上最迟半分钟内就能看到，而不是等两分钟。探测本身
// 只读 /models，不消耗生成额度；失败也不会延长上游的冷却时间。
const providerRecoveryProbeInterval = 30 * time.Second

// StartProviderFailoverRecovery periodically probes higher-priority providers
// for keys stuck on a fallback, and switches back when one becomes available.
// 回切探测（每 30s）与被标记不可用的 Provider 重探（每 2min，与冷却同拍）
// 分开计时：前者要让控制台尽快看到「首选已恢复」，后者决定账号能否接流量。
func (s *Server) StartProviderFailoverRecovery(ctx context.Context) {
	go func() {
		// 启动后先等一小段，避开冷启动阶段的探测抖动。
		recoveryTimer := time.NewTimer(providerRecoveryProbeInterval)
		defer recoveryTimer.Stop()
		reprobeTimer := time.NewTimer(providerFailoverRecoveryInterval)
		defer reprobeTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-recoveryTimer.C:
				s.recoverAPIKeyPreferredProviders(context.Background())
				recoveryTimer.Reset(providerRecoveryProbeInterval)
			case <-reprobeTimer.C:
				s.reprobeUnavailableProviders(context.Background())
				reprobeTimer.Reset(providerFailoverRecoveryInterval)
			}
		}
	}()
}

func (s *Server) recoverAPIKeyPreferredProviders(ctx context.Context) {
	keys := s.router.State().APIKeys
	// 同一周期内每个 Provider 只探测一次，多个 Key 共用同一前置 Provider 时复用结果。
	probeCache := make(map[string]bool)
	for _, key := range keys {
		if !key.Enabled {
			continue
		}
		route, err := s.router.RouteByID(key.RouteID)
		if err != nil {
			continue
		}
		chain := apiKeyProviderChain(route.ProviderID, key.FallbackProviderIDs)
		if len(chain) <= 1 {
			continue
		}
		activeIdx := apiKeyEffectiveProviderIndex(key.ActiveProviderID, chain)
		if activeIdx <= 0 {
			// 未处于备用状态：无需探测，零开销跳过。
			continue
		}
		for i := 0; i < activeIdx; i++ {
			providerID := chain[i]
			available, cached := probeCache[providerID]
			if !cached {
				provider, err := s.router.ProviderByID(providerID)
				if err != nil || provider.Disabled {
					// 已删除或管理员禁用：不当作可回切目标，也不发探测流量。
					probeCache[providerID] = false
					continue
				}
				available = s.probeProviderAvailable(ctx, provider)
				probeCache[providerID] = available
			}
			if !available {
				continue
			}
			desiredActive := ""
			if providerID != route.ProviderID {
				desiredActive = providerID
			}
			s.persistAPIKeyActiveProvider(key.ID, desiredActive)
			s.logs.AddApp("info", "api key recovered higher-priority provider", fmt.Sprintf("key=%s provider=%s", key.ID, providerID))
			slog.Info("api key recovered higher-priority provider", "key", key.ID, "provider", providerID)
			break
		}
	}
}

// reprobeUnavailableProviders re-checks every provider currently marked
// unavailable by a live request failure (recordProviderRequestOutcome) and
// clears the mark once a probe succeeds. Providers still down get their
// retry countdown pushed to the next cycle, matching the console's
// "N秒后重试" hint. Runs on the same cadence as recoverAPIKeyPreferredProviders.
func (s *Server) reprobeUnavailableProviders(ctx context.Context) {
	for _, id := range s.providerUnavailableIDs() {
		provider, err := s.router.ProviderByID(id)
		if err != nil {
			// Provider no longer exists: stop tracking it.
			s.markProviderAvailable(id)
			continue
		}
		if provider.Disabled {
			// 管理员已禁用：停止可用性重探，避免无效 /models 刷日志。
			s.markProviderAvailable(id)
			if s.logs != nil {
				s.logs.AddApp("info", "provider unavailable cleared (disabled)", fmt.Sprintf("provider=%s", id))
			}
			continue
		}
		if s.probeProviderAvailable(ctx, provider) {
			// A Claude subscription's /models probe costs no allowance and
			// answers even while every window is spent: the account is not back
			// until its usage reading says a window reopened. Trusting the probe
			// alone is what recovered a spent account a second after it was
			// marked (the log's "marked unavailable 20:00:05" / "provider
			// recovered 20:00:06"), and the next request was refused again.
			if at, spent := s.claudeSpentWindowReset(id, time.Now()); spent {
				prev, _ := s.providerUnavailableSnapshot(id)
				reason := prev.reason
				if strings.TrimSpace(reason) == "" {
					reason = "quota window spent"
				}
				s.setProviderUnavailableUntil(id, at, prev.hard, reason)
				continue
			}
			s.markProviderAvailable(id)
			if s.logs != nil {
				s.logs.AddApp("info", "provider recovered", fmt.Sprintf("provider=%s", id))
			}
			slog.Info("provider recovered", "provider", id)
			continue
		}
		// Still unavailable: refresh the countdown for the next cycle,
		// preserving hard/soft classification from the original mark.
		prev, _ := s.providerUnavailableSnapshot(id)
		reason := prev.reason
		if reason == "" {
			reason = "reprobe_failed"
		}
		// A vendor-named reset time is kept: it survives cycles of failed
		// probes, whose own bodies carry no hint at all.
		s.refreshProviderUnavailableCountdown(id, prev.hard, reason, "", nil)
	}
}

func (s *Server) probeProviderAvailable(ctx context.Context, provider domain.Provider) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/__probe", nil)
	if err != nil {
		return false
	}
	result := s.fetchProviderModels(req, provider, time.Now())
	return result.Success
}

// decisionForAPIKey is the provider the request is attributed to before it is
// sent: the conversation's pinned account when there is one, otherwise the
// first provider the failover loop would try.
func (s *Server) decisionForAPIKey(route domain.Route, key domain.APIKey, fallback domain.RouteDecision, sessionKey string) domain.RouteDecision {
	return s.decisionForAPIKeyProtocol(route, key, fallback, sessionKey, "")
}

// decisionForAPIKeyProtocol 是 decisionForAPIKey 的「按这次请求的协议」版本。
//
// 协议必须一路传到底：这里重算的那份 decision 决定了日志归属，也决定了
// pass_through 还是 convert。用不带协议的老调用会把 route 上的 OutputProtocol
// 又捡回来，等于把「一把 Key 多种协议」在临门一脚处废掉。
func (s *Server) decisionForAPIKeyProtocol(route domain.Route, key domain.APIKey, fallback domain.RouteDecision, sessionKey string, clientProtocol domain.Protocol) domain.RouteDecision {
	chain := apiKeyProviderChain(route.ProviderID, key.FallbackProviderIDs)
	// Mirror the request-time authorization filter in
	// executeProtocolFlowWithFailover so the pre-flight decision (used for
	// request-log attribution) never names a provider the owner lost access to.
	if allowed := s.allowedProviderChainForKey(key, chain); len(allowed) > 0 {
		chain = allowed
	}
	if len(chain) == 0 {
		return fallback
	}
	// Attribute the request to the provider the failover loop will actually try
	// first, so the log line matches the traffic.
	attempts := s.failoverAttemptOrder(scheduleInput{
		chain:  chain,
		active: key.ActiveProviderID,
		mode:   key.ScheduleMode,
		keyID:  key.ID,
	})
	attempts, _ = s.applySessionPin(sessionKey, attempts)
	// 与执行循环保持同一口径，否则日志里记的节点会和真正服务的那个不一样。
	attempts = s.preferNativeProtocol(attempts, clientProtocol, "")
	decision, err := s.router.DecideForProviderProtocol(route.ID, attempts[0], clientProtocol)
	if err != nil {
		return fallback
	}
	return decision
}
