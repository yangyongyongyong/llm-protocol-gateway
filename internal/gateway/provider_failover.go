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

// shouldFailoverProvider reports whether an upstream failure should advance to
// the next backup provider for this API key.
func shouldFailoverProvider(status int, body []byte, transportErr error) bool {
	if transportErr != nil {
		return true
	}
	if status == http.StatusTooManyRequests || status == 529 {
		return true
	}
	if status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout {
		return true
	}
	msg := strings.ToLower(summarizeUpstreamHTTPError(status, body))
	quotaHints := []string{
		"insufficient_quota",
		"quota",
		"rate_limit",
		"rate limit",
		"usage limit",
		"credit balance",
		"billing",
		"exceeded",
		"overloaded",
		"capacity",
		"tokens exhausted",
		"out of credits",
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		for _, hint := range quotaHints {
			if strings.Contains(msg, hint) {
				return true
			}
		}
		// Provider credential / account blocks are also failover-worthy.
		return true
	}
	if status == http.StatusBadRequest {
		for _, hint := range quotaHints {
			if strings.Contains(msg, hint) {
				return true
			}
		}
	}
	return false
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
) (int, TokenUsage, []byte, domain.RouteDecision, error) {
	if !gatewayKeyMatched {
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		return status, usage, body, decision, err
	}

	chain := apiKeyProviderChain(route.ProviderID, matchedKey.FallbackProviderIDs)
	if len(chain) == 0 {
		status, usage, body, err := s.executeProtocolFlow(w, r, route, decision, model, req, clientProtocol, skipIncomingAuth)
		return status, usage, body, decision, err
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
	requestModel, _ := req["model"].(string)

	for i := start; i < len(chain); i++ {
		providerID := chain[i]
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
			lastStatus, lastUsage, lastBody, lastErr = status, usage, nil, execErr
			if canDefer && shouldFailoverProvider(0, nil, execErr) {
				writer.Discard()
				markTimingFlag(r.Context(), timingFlagFailoverRetry)
				if t := requestTimingFrom(r.Context()); t != nil {
					t.resetUpstreamMarks()
				}
				s.logs.AddApp("warn", "provider failover after transport error", fmt.Sprintf("key=%s from=%s err=%s", matchedKey.ID, providerID, execErr.Error()))
				continue
			}
			writer.FlushError()
			return status, usage, body, attemptDecision, execErr
		}

		respBody := body
		if len(respBody) == 0 {
			respBody = writer.BufferedBody()
		}
		lastStatus, lastUsage, lastBody, lastErr = status, usage, respBody, nil

		if canDefer && shouldFailoverProvider(status, respBody, nil) {
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
		return status, usage, respBody, attemptDecision, nil
	}

	if lastErr != nil {
		return lastStatus, lastUsage, lastBody, lastDecision, lastErr
	}
	return lastStatus, lastUsage, lastBody, lastDecision, nil
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

// StartProviderFailoverRecovery hourly probes higher-priority providers for
// keys stuck on a fallback, and switches back when one becomes available.
func (s *Server) StartProviderFailoverRecovery(ctx context.Context) {
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.recoverAPIKeyPreferredProviders(context.Background())
				timer.Reset(time.Hour)
			}
		}
	}()
}

func (s *Server) recoverAPIKeyPreferredProviders(ctx context.Context) {
	keys := s.router.State().APIKeys
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
			continue
		}
		for i := 0; i < activeIdx; i++ {
			providerID := chain[i]
			provider, err := s.router.ProviderByID(providerID)
			if err != nil {
				continue
			}
			if !s.probeProviderAvailable(ctx, provider) {
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
