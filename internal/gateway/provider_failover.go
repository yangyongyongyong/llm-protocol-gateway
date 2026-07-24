package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// failoverClass distinguishes transient upstream issues from hard quota/auth
// failures. Both still advance the backup chain; only hard (or repeated soft)
// failures mark the provider UI as "unavailable".
type failoverClass int

const (
	failoverNone failoverClass = iota
	failoverSoft
	failoverHard
)

const (
	// Soft failures must repeat within this window before the provider is
	// marked unavailable (avoids single 429/529/5xx blips painting "异常").
	providerSoftFailureWindow     = 60 * time.Second
	providerSoftFailureThreshold  = 3
	providerSoftUnavailableTTL    = 1 * time.Minute
	claudeUsageHardThresholdPct   = 95.0
)

// hardQuotaHints are narrow subscription/billing exhaustion signals. Broad
// tokens like bare "quota" / "exceeded" / "rate limit" are intentionally
// omitted — those usually mean short-lived throttling, not a dead provider.
var hardQuotaHints = []string{
	"insufficient_quota",
	"usage limit",
	"you've hit your",
	"you have hit your",
	"credit balance",
	"billing",
	"tokens exhausted",
	"out of credits",
	"quota exceeded",
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

// classifyProviderFailover returns soft/hard/none plus a short reason string.
// fiveHourUtil, when non-nil, is the cached Claude OAuth 5h utilization
// (0-100). Hard "usage limit" signals are downgraded to soft when utilization
// is clearly under the hard threshold — matching the quota panel reality.
func classifyProviderFailover(status int, body []byte, transportErr error, fiveHourUtil *float64) (failoverClass, string) {
	if transportErr != nil {
		return failoverSoft, "transport: " + truncateReason(transportErr.Error())
	}

	msg := strings.ToLower(summarizeUpstreamHTTPError(status, body))
	hardQuota := containsAnyHint(msg, hardQuotaHints)
	hardAuth := containsAnyHint(msg, hardAuthHints)
	softThrottle := containsAnyHint(msg, softThrottleHints)

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
		return failoverNone, ""
	default:
		return failoverNone, ""
	}
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
	base       http.ResponseWriter
	header     http.Header
	status     int
	buf        bytes.Buffer
	canDefer   bool
	passthrough bool
	discarded  bool
	wroteHeader bool
}

func newFailoverResponseWriter(base http.ResponseWriter, canDefer bool) *failoverResponseWriter {
	return &failoverResponseWriter{
		base:     base,
		header:   make(http.Header),
		canDefer: canDefer,
	}
}

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
	w.status = statusCode
	if !w.canDefer || statusCode < 400 {
		w.commitHeaders(statusCode)
		return
	}
	w.wroteHeader = true
}

func (w *failoverResponseWriter) Write(p []byte) (int, error) {
	if w.discarded {
		return len(p), nil
	}
	if !w.wroteHeader && !w.passthrough {
		w.WriteHeader(http.StatusOK)
	}
	if w.passthrough {
		return w.base.Write(p)
	}
	return w.buf.Write(p)
}

func (w *failoverResponseWriter) Flush() {
	if w.passthrough {
		if f, ok := w.base.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (w *failoverResponseWriter) commitHeaders(statusCode int) {
	dst := w.base.Header()
	for key, values := range w.header {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	w.base.WriteHeader(statusCode)
	w.status = statusCode
	w.wroteHeader = true
	w.passthrough = true
	if w.buf.Len() > 0 {
		_, _ = w.base.Write(w.buf.Bytes())
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
func (s *Server) markProviderUnavailable(providerID string, hard bool, reason string) {
	s.setProviderUnavailable(providerID, hard, reason, true)
}

// refreshProviderUnavailableCountdown extends nextRetryAt without re-logging
// a fresh "marked unavailable" event (used by the recovery loop).
func (s *Server) refreshProviderUnavailableCountdown(providerID string, hard bool, reason string) {
	s.setProviderUnavailable(providerID, hard, reason, false)
}

func (s *Server) setProviderUnavailable(providerID string, hard bool, reason string, logMark bool) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return
	}
	ttl := providerFailoverRecoveryInterval
	if !hard {
		ttl = providerSoftUnavailableTTL
	}
	reason = truncateReason(reason)
	s.providerAvailabilityMu.Lock()
	if s.providerAvailability == nil {
		s.providerAvailability = make(map[string]providerUnavailableState)
	}
	s.providerAvailability[providerID] = providerUnavailableState{
		nextRetry: time.Now().Add(ttl),
		hard:      hard,
		reason:    reason,
	}
	s.providerAvailabilityMu.Unlock()
	if !logMark {
		return
	}
	kind := "soft"
	if hard {
		kind = "hard"
	}
	msg := fmt.Sprintf("provider=%s kind=%s reason=%s retry_in=%s", providerID, kind, reason, ttl)
	if s.logs != nil {
		s.logs.AddApp("warn", "provider marked unavailable", msg)
	}
	slog.Warn("provider marked unavailable", "provider", providerID, "hard", hard, "reason", reason)
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
	cached, ok := s.oauthUsageCache.getAllowStale("claude:" + providerID)
	if !ok {
		return 0, false
	}
	report, ok := cached.(ClaudeOAuthUsageReport)
	if !ok || !report.Available || report.FiveHour == nil {
		return 0, false
	}
	return report.FiveHour.Utilization, true
}

func (s *Server) classifyProviderFailover(providerID string, status int, body []byte, err error) (failoverClass, string) {
	var utilPtr *float64
	if util, ok := s.cachedClaudeFiveHourUtilization(providerID); ok {
		u := util
		utilPtr = &u
	}
	return classifyProviderFailover(status, body, err, utilPtr)
}

// applyProviderAvailability overlays the live-request availability tracker
// onto a provider destined for the client: a provider that just failed a real
// API call shows healthStatus "unavailable" (rendered "异常" in the console)
// with nextRetryAt, even if its persisted HealthStatus still reflects an
// earlier manual "获取模型" check.
func (s *Server) applyProviderAvailability(provider *domain.Provider) {
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
	_ = s.applyProviderFailoverOutcome(providerID, status, body, err)
}

// applyProviderFailoverOutcome classifies the attempt, updates soft/hard
// availability tracking, and returns whether the caller should advance to the
// next backup provider.
func (s *Server) applyProviderFailoverOutcome(providerID string, status int, body []byte, err error) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	class, reason := s.classifyProviderFailover(providerID, status, body, err)
	if class == failoverNone {
		if err == nil {
			s.markProviderAvailable(providerID)
		}
		return false
	}
	switch class {
	case failoverHard:
		s.clearSoftFailures(providerID)
		s.markProviderUnavailable(providerID, true, reason)
	case failoverSoft:
		count := s.bumpSoftFailure(providerID)
		if count >= providerSoftFailureThreshold {
			s.markProviderUnavailable(providerID, false, fmt.Sprintf("%s consecutive=%d", reason, count))
		} else if s.logs != nil {
			s.logs.AddApp("info", "provider soft failure", fmt.Sprintf(
				"provider=%s consecutive=%d/%d reason=%s",
				providerID, count, providerSoftFailureThreshold, reason,
			))
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

// executeProtocolFlowWithFailover tries the sticky/active provider then ordered
// backups when the upstream failure looks like quota/auth/availability.
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
	if !gatewayKeyMatched {
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		s.recordProviderRequestOutcome(decision.ProviderID, status, body, err)
		return status, usage, body, decision, model, err
	}

	chain := apiKeyProviderChain(route.ProviderID, matchedKey.FallbackProviderIDs)
	if len(chain) == 0 {
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		s.recordProviderRequestOutcome(decision.ProviderID, status, body, err)
		return status, usage, body, decision, model, err
	}
	start := apiKeyEffectiveProviderIndex(matchedKey.ActiveProviderID, chain)
	if start < 0 || start >= len(chain) {
		start = 0
	}

	var lastStatus int
	var lastUsage TokenUsage
	var lastBody []byte
	var lastErr error
	var lastDecision domain.RouteDecision = decision
	lastModel := model
	requestModel, _ := req["model"].(string)
	// 普通用户的 Key（有归属用户）不能使用被管理员禁用的 Provider；
	// 管理员自己的 Key（无归属）不受限制。
	ownerRestricted := strings.TrimSpace(matchedKey.OwnerUserID) != ""

	for i := start; i < len(chain); i++ {
		providerID := chain[i]
		if ownerRestricted {
			if provider, err := s.router.ProviderByID(providerID); err == nil && provider.Disabled {
				lastErr = fmt.Errorf("provider %q is disabled by administrator", providerID)
				s.logs.AddApp("warn", "provider disabled, skipped for user key", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, providerID))
				continue
			}
		}
		attemptDecision, err := s.router.DecideForProvider(route.ID, providerID)
		if err != nil {
			lastErr = err
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
			if override == "" {
				s.logs.AddApp("warn", "provider failover skipped missing model override", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, providerID))
				continue
			}
			attemptKey.ModelOverride = override
		}
		if resolved, _ := resolveConsumerModel(s.router, route, attemptKey, true, requestModel); strings.TrimSpace(resolved) != "" {
			attemptModel = resolved
		}
		attemptReq := cloneRequestMap(req)
		attemptReq["model"] = attemptModel

		canDefer := i < len(chain)-1
		writer := newFailoverResponseWriter(w, canDefer)
		status, usage, body, execErr := s.executeProtocolFlow(writer, r, route, attemptDecision, attemptModel, attemptReq, clientProtocol, skipIncomingAuth)
		if execErr != nil {
			lastStatus, lastUsage, lastBody, lastErr, lastModel = status, usage, nil, execErr, attemptModel
			transportFailoverWorthy := s.applyProviderFailoverOutcome(providerID, 0, nil, execErr)
			if canDefer && transportFailoverWorthy {
				writer.Discard()
				markTimingFlag(r.Context(), timingFlagFailoverRetry)
				if t := requestTimingFrom(r.Context()); t != nil {
					t.resetUpstreamMarks()
				}
				s.logs.AddApp("warn", "provider failover after transport error", fmt.Sprintf("key=%s from=%s err=%s", matchedKey.ID, providerID, execErr.Error()))
				continue
			}
			// 尚未向客户端提交任何字节时交给上层写错误响应（例如空流重试耗尽）。
			if !writer.passthrough && writer.buf.Len() == 0 && writer.status == 0 {
				writer.Discard()
			} else {
				writer.FlushError()
			}
			return status, usage, body, attemptDecision, attemptModel, execErr
		}

		respBody := body
		if len(respBody) == 0 {
			respBody = writer.BufferedBody()
		}
		lastStatus, lastUsage, lastBody, lastErr, lastModel = status, usage, respBody, nil, attemptModel

		attemptFailoverWorthy := s.applyProviderFailoverOutcome(providerID, status, respBody, nil)
		if canDefer && attemptFailoverWorthy {
			writer.Discard()
			markTimingFlag(r.Context(), timingFlagFailoverRetry)
			if t := requestTimingFrom(r.Context()); t != nil {
				t.resetUpstreamMarks()
			}
			s.logs.AddApp("warn", "provider failover after upstream error", fmt.Sprintf("key=%s from=%s status=%d", matchedKey.ID, providerID, status))
			continue
		}

		// Stick to the provider that served this request.
		desiredActive := ""
		if providerID != strings.TrimSpace(route.ProviderID) {
			desiredActive = providerID
		}
		if strings.TrimSpace(matchedKey.ActiveProviderID) != desiredActive {
			s.persistAPIKeyActiveProvider(matchedKey.ID, desiredActive)
			if desiredActive != "" {
				s.logs.AddApp("info", "api key active provider updated", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, desiredActive))
			} else {
				s.logs.AddApp("info", "api key restored preferred provider", fmt.Sprintf("key=%s provider=%s", matchedKey.ID, route.ProviderID))
			}
		}
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
const providerFailoverRecoveryInterval = 2 * time.Minute

// StartProviderFailoverRecovery periodically probes higher-priority providers
// for keys stuck on a fallback, and switches back when one becomes available.
func (s *Server) StartProviderFailoverRecovery(ctx context.Context) {
	go func() {
		// 启动后先等一小段，避开冷启动阶段的探测抖动。
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.recoverAPIKeyPreferredProviders(context.Background())
				s.reprobeUnavailableProviders(context.Background())
				timer.Reset(providerFailoverRecoveryInterval)
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
				if err != nil {
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
		if s.probeProviderAvailable(ctx, provider) {
			s.markProviderAvailable(id)
			s.logs.AddApp("info", "provider recovered", fmt.Sprintf("provider=%s", id))
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
		s.refreshProviderUnavailableCountdown(id, prev.hard, reason)
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

func (s *Server) decisionForAPIKey(route domain.Route, key domain.APIKey, fallback domain.RouteDecision) domain.RouteDecision {
	chain := apiKeyProviderChain(route.ProviderID, key.FallbackProviderIDs)
	if len(chain) == 0 {
		return fallback
	}
	idx := apiKeyEffectiveProviderIndex(key.ActiveProviderID, chain)
	if idx < 0 || idx >= len(chain) {
		idx = 0
	}
	decision, err := s.router.DecideForProvider(route.ID, chain[idx])
	if err != nil {
		return fallback
	}
	return decision
}
