package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// availabilityScope says what a failed attempt proved unavailable. It is kept
// separate from failoverClass: failure/hardness decides whether to advance the
// chain, while scope decides which ledger entry must be cooled down.
type availabilityScope int

const (
	availabilityScopeRequest availabilityScope = iota
	availabilityScopeProvider
	availabilityScopeModel
)

const (
	// Model-level limits are cheaper to re-check than a whole account, but they
	// still must not turn into a probe loop. A normal model restriction starts
	// at ten minutes; a model that is merely busy starts at two. Repeated failed
	// probes double the wait, capped at half an hour.
	modelAvailabilityDefaultRest = 10 * time.Minute
	modelAvailabilityBusyRest    = 2 * time.Minute
	modelAvailabilityBackoffCap  = 30 * time.Minute
	// A vendor may name a reset window (WorkBuddy's daily free-model window is an
	// example). Keep the named instant, but stop a malformed/hostile body from
	// parking one model for an unbounded amount of time.
	modelAvailabilityResetCap = 7 * 24 * time.Hour
	modelAvailabilityMaxRows  = 4096
	// Keep a recovery tick bounded even when many keys learned many model
	// restrictions. The remaining pairs keep their original nextRetry and are
	// picked up on the next cycle.
	modelAvailabilityProbesPerCycle = 8
)

// modelAvailabilityKey is deliberately Provider + canonical upstream model.
// It is not keyed by API key: a provider account and a model entitlement are
// shared, so every Key using that pair benefits from one successful probe.
type modelAvailabilityKey struct {
	providerID string
	model      string
}

type modelUnavailableState struct {
	nextRetry time.Time
	hard      bool
	reason    string
	model     string
	attempts  int
}

func normalizeModelAvailabilityName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func makeModelAvailabilityKey(providerID, model string) modelAvailabilityKey {
	return modelAvailabilityKey{
		providerID: strings.TrimSpace(providerID),
		model:      normalizeModelAvailabilityName(model),
	}
}

// markModelUnavailable records a failure that only the exact Provider+Model
// pair proved. It never paints the Provider's other models unavailable.
func (s *Server) markModelUnavailable(providerID, model string, hard bool, reason, retryAfter string, body []byte) {
	attempts := 0
	key := makeModelAvailabilityKey(providerID, model)
	s.providerAvailabilityMu.Lock()
	if previous, ok := s.modelAvailability[key]; ok {
		attempts = previous.attempts + 1
	}
	s.providerAvailabilityMu.Unlock()
	s.markModelUnavailableWithAttempts(providerID, model, hard, reason, retryAfter, body, attempts)
}

func (s *Server) markModelUnavailableWithAttempts(providerID, model string, hard bool, reason, retryAfter string, body []byte, attempts int) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return
	}
	now := time.Now()
	ttl := modelAvailabilityRestTTL(reason, retryAfter, body, attempts, now)
	state := modelUnavailableState{
		nextRetry: now.Add(ttl),
		hard:      hard,
		reason:    truncateReason(reason),
		model:     model,
		attempts:  attempts,
	}
	s.providerAvailabilityMu.Lock()
	if s.modelAvailability == nil {
		s.modelAvailability = make(map[modelAvailabilityKey]modelUnavailableState)
	}
	s.modelAvailability[makeModelAvailabilityKey(providerID, model)] = state
	pruneModelAvailabilityLocked(s.modelAvailability)
	s.providerAvailabilityMu.Unlock()

	msg := fmt.Sprintf("provider=%s model=%s scope=model kind=%s reason=%s next_probe=%s",
		providerID, model, modelAvailabilityKind(hard), state.reason, state.nextRetry.Format(time.RFC3339))
	if s.logs != nil {
		s.logs.AddApp("warn", "model marked unavailable", msg)
	}
	slog.Warn("model marked unavailable", "provider", providerID, "model", model,
		"hard", hard, "reason", state.reason, "next_probe", state.nextRetry)
}

func modelAvailabilityKind(hard bool) string {
	if hard {
		return "hard"
	}
	return "soft"
}

func modelAvailabilityBaseRest(reason string) time.Duration {
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "busy") || strings.Contains(lower, "queue") || strings.Contains(lower, "upstream_busy") {
		return modelAvailabilityBusyRest
	}
	return modelAvailabilityDefaultRest
}

func modelAvailabilityRestTTL(reason, retryAfter string, body []byte, attempts int, now time.Time) time.Duration {
	hint := parseProviderCooldownHint(retryAfter, body, now)
	if !hint.resetsAt.IsZero() && hint.resetsAt.After(now) {
		ttl := hint.resetsAt.Sub(now)
		if ttl > modelAvailabilityResetCap {
			ttl = modelAvailabilityResetCap
		}
		return ttl
	}
	if hint.retryAfter > 0 {
		ttl := hint.retryAfter
		if ttl > modelAvailabilityResetCap {
			ttl = modelAvailabilityResetCap
		}
		if ttl > 0 {
			return ttl
		}
	}
	base := modelAvailabilityBaseRest(reason)
	shift := attempts
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4
	}
	ttl := base << shift
	if ttl > modelAvailabilityBackoffCap {
		ttl = modelAvailabilityBackoffCap
	}
	return ttl
}

// refreshModelUnavailableCountdown extends one pair's countdown after a failed
// exact-model probe. It preserves the original reason and only moves the pair's
// retry time; no other model or Provider-wide mark is touched.
func (s *Server) refreshModelUnavailableCountdown(providerID, model, reason, retryAfter string, body []byte) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return
	}
	key := makeModelAvailabilityKey(providerID, model)
	s.providerAvailabilityMu.Lock()
	old, ok := s.modelAvailability[key]
	if !ok {
		s.providerAvailabilityMu.Unlock()
		return
	}
	old.attempts++
	old.nextRetry = time.Now().Add(modelAvailabilityRestTTL(reason, retryAfter, body, old.attempts, time.Now()))
	old.reason = truncateReason(reason)
	s.modelAvailability[key] = old
	s.providerAvailabilityMu.Unlock()
}

func (s *Server) markModelAvailable(providerID, model string) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if providerID == "" || model == "" {
		return
	}
	key := makeModelAvailabilityKey(providerID, model)
	s.providerAvailabilityMu.Lock()
	_, existed := s.modelAvailability[key]
	delete(s.modelAvailability, key)
	s.providerAvailabilityMu.Unlock()
	if !existed {
		return
	}
	msg := fmt.Sprintf("provider=%s model=%s scope=model action=recovered", providerID, model)
	if s.logs != nil {
		s.logs.AddApp("info", "model recovered", msg)
	}
	slog.Info("model recovered", "provider", providerID, "model", model)
}

func (s *Server) modelUnavailableSnapshot(providerID, model string) (modelUnavailableState, bool) {
	key := makeModelAvailabilityKey(providerID, model)
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	state, ok := s.modelAvailability[key]
	return state, ok
}

// modelUnavailableForRequest reports whether the pair is still inside its
// cooldown. Once nextRetry has passed the state stays in the ledger so the exact
// model can be probed, while a real request for that model is allowed through
// as the natural probe. No other model is affected.
func (s *Server) modelUnavailableForRequest(providerID, model string, now time.Time) (time.Time, bool) {
	state, ok := s.modelUnavailableSnapshot(providerID, model)
	if !ok || state.nextRetry.IsZero() || !now.Before(state.nextRetry) {
		return time.Time{}, false
	}
	return state.nextRetry, true
}

func (s *Server) modelUnavailableKeys() []modelAvailabilityKey {
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	type candidate struct {
		key   modelAvailabilityKey
		until time.Time
	}
	candidates := make([]candidate, 0, len(s.modelAvailability))
	for key, state := range s.modelAvailability {
		candidates = append(candidates, candidate{key: key, until: state.nextRetry})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].until.Equal(candidates[j].until) {
			return candidates[i].until.Before(candidates[j].until)
		}
		if candidates[i].key.providerID != candidates[j].key.providerID {
			return candidates[i].key.providerID < candidates[j].key.providerID
		}
		return candidates[i].key.model < candidates[j].key.model
	})
	keys := make([]modelAvailabilityKey, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.key)
	}
	return keys
}

func (s *Server) providerHasModelAvailability(providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	s.providerAvailabilityMu.Lock()
	defer s.providerAvailabilityMu.Unlock()
	for key := range s.modelAvailability {
		if key.providerID == providerID {
			return true
		}
	}
	return false
}

func (s *Server) clearModelAvailabilityForProvider(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return
	}
	s.providerAvailabilityMu.Lock()
	for key := range s.modelAvailability {
		if key.providerID == providerID {
			delete(s.modelAvailability, key)
		}
	}
	s.providerAvailabilityMu.Unlock()
}

// modelAvailabilityRows returns the console overlay for one Provider. It is
// read at response time so the UI can show a short-lived model restriction
// without mutating the persisted model catalog.
func (s *Server) modelAvailabilityRows(providerID string) []domain.ProviderModelAvailability {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return nil
	}
	s.providerAvailabilityMu.Lock()
	rows := make([]domain.ProviderModelAvailability, 0)
	for key, state := range s.modelAvailability {
		if key.providerID != providerID {
			continue
		}
		rows = append(rows, domain.ProviderModelAvailability{
			Model:       state.model,
			Reason:      state.reason,
			NextRetryAt: state.nextRetry.Format(time.RFC3339),
		})
	}
	s.providerAvailabilityMu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows
}

func pruneModelAvailabilityLocked(rows map[modelAvailabilityKey]modelUnavailableState) {
	// Do not time-expire a pair: after nextRetry it is still waiting for an
	// exact-model probe, and dropping it would let one real request discover the
	// same limit again without a scheduled check. Bound the ledger instead.
	if len(rows) <= modelAvailabilityMaxRows {
		return
	}
	for key := range rows {
		delete(rows, key)
		if len(rows) <= modelAvailabilityMaxRows {
			return
		}
	}
}

// probeProviderModelAvailable is the recovery probe for one exact model. The
// request body is intentionally tiny: a short user turn and a 16-token output
// cap, matching the gateway's existing light-probe budget. It never substitutes
// the provider default or another model for the one recorded at failure time.
func (s *Server) probeProviderModelAvailable(ctx context.Context, provider domain.Provider, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return s.probeProviderAvailable(ctx, provider)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/__availability-probe", nil)
	if err != nil {
		return false
	}
	result, _ := s.testProviderChat(req, provider.ID, providerChatTestRequest{
		Model:      model,
		UserPrompt: "ok",
		MaxTokens:  monitorProbeMaxTokens,
	}, time.Now())
	success, _ := result["success"].(bool)
	return success
}

// resetTimestampPattern accepts the human timestamp several Chinese upstreams
// put in a 429 body, including WorkBuddy's "2026-11-14 20:09:02 UTC+8" form.
var resetTimestampPattern = regexp.MustCompile(`(?i)(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})\s*(?:UTC|GMT)?\s*([+-]\d{1,2})(?::?(\d{2}))?`)

func parseHumanResetTime(text string, now time.Time) (time.Time, bool) {
	match := resetTimestampPattern.FindStringSubmatch(text)
	if len(match) == 0 {
		return time.Time{}, false
	}
	hours, err := strconv.Atoi(match[3])
	if err != nil {
		return time.Time{}, false
	}
	minutes := 0
	if match[4] != "" {
		minutes, err = strconv.Atoi(match[4])
		if err != nil {
			return time.Time{}, false
		}
	}
	if hours < -14 || hours > 14 || minutes < 0 || minutes > 59 {
		return time.Time{}, false
	}
	offset := hours*3600 + minutes*60
	if hours < 0 {
		offset = hours*3600 - minutes*60
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05", match[1]+" "+match[2], time.FixedZone("reset", offset))
	if err != nil {
		return time.Time{}, false
	}
	if !at.After(now) {
		return time.Time{}, false
	}
	return at, true
}
