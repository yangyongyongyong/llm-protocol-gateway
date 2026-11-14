package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// The scheduling trace answers the question an operator actually asks when a
// request went wrong: which account was chosen, why, and who was tried before
// it. It rides the request timing carrier because that object is already created
// at handler entry and read again when the request log is assembled, so no
// signature in the request path has to change to carry it.
//
// It records provider ids, models, status codes and reasons. Nothing secret
// reaches it: no keys, no tokens, no client bodies.

// Reason codes for an attempt that did not serve the client. They are stable
// strings because the console labels them, and a log reader needs to tell
// "moved on because the account was busy" from "moved on because it was out".
const (
	attemptReasonServed                = "served"
	attemptReasonUpstreamError         = "upstream_error"
	attemptReasonModelBusy             = "model_busy"
	attemptReasonModelUnavailable      = "model_unavailable"
	attemptReasonStreamInterrupted     = "stream_interrupted"
	attemptReasonTransportError        = "transport_error"
	attemptReasonFirstPacketTimeout    = "first_packet_timeout"
	attemptReasonSaturated             = "saturated"
	attemptReasonMissingModel          = "missing_model_override"
	attemptReasonUnsupportedConversion = "unsupported_conversion"
	attemptReasonDisabled              = "disabled"
	attemptReasonNotAssigned           = "not_assigned"
	attemptReasonUndecidable           = "decision_failed"
	attemptReasonClientGone            = "client_gone"
	attemptReasonNativePassthroughFail = "native_passthrough_failed"
)

type scheduleTrace struct {
	mode       string
	pinned     string
	candidates []string
	attempts   []monitor.RequestAttempt
}

func (t *requestTiming) noteScheduleDecision(mode string, candidates []string, pinned string) {
	if t == nil {
		return
	}
	t.scheduleMu.Lock()
	defer t.scheduleMu.Unlock()
	if t.schedule == nil {
		t.schedule = &scheduleTrace{}
	}
	t.schedule.mode = mode
	t.schedule.candidates = append([]string(nil), candidates...)
	t.schedule.pinned = pinned
}

func (t *requestTiming) noteScheduleAttempt(attempt monitor.RequestAttempt) {
	if t == nil || attempt.ProviderID == "" {
		return
	}
	t.scheduleMu.Lock()
	defer t.scheduleMu.Unlock()
	if t.schedule == nil {
		t.schedule = &scheduleTrace{}
	}
	t.schedule.attempts = append(t.schedule.attempts, attempt)
}

// applyTo fills in the scheduling part of an already captured analysis.
func (trace *scheduleTrace) applyTo(analysis *monitor.RequestAnalysis) {
	if trace == nil || analysis == nil {
		return
	}
	analysis.ScheduleMode = trace.mode
	analysis.SessionPinned = trace.pinned
	analysis.Candidates = trace.candidates
	analysis.Attempts = trace.attempts
}

// noteScheduleDecision / noteScheduleAttempt are the context-shaped entry
// points used by the failover loop.
func noteScheduleDecision(ctx context.Context, mode string, candidates []string, pinned string) {
	if t := requestTimingFrom(ctx); t != nil {
		t.noteScheduleDecision(mode, candidates, pinned)
	}
}

func noteScheduleAttempt(ctx context.Context, attempt monitor.RequestAttempt) {
	if t := requestTimingFrom(ctx); t != nil {
		t.noteScheduleAttempt(attempt)
	}
}

// noteSingleAttemptTrace records the one attempt a request without a fallback
// chain makes, so the trace is never blank simply because nothing could be
// failed over to.
func noteSingleAttemptTrace(ctx context.Context, providerID, model string, status int, usage TokenUsage, err error, elapsed int64) {
	if providerID == "" {
		return
	}
	attempt := monitor.RequestAttempt{ProviderID: providerID, Model: model, Status: status, Ms: elapsed, Reason: attemptReasonServed}
	if err != nil {
		attempt.Reason = attemptReasonTransportError
		attempt.Error = err.Error()
		if errors.Is(err, errUpstreamFirstPacketTimeout) {
			attempt.Reason = attemptReasonFirstPacketTimeout
		}
		if errors.Is(err, errProviderSaturated) {
			attempt.Reason = attemptReasonSaturated
			attempt.Status = http.StatusTooManyRequests
		}
		if isRequestContextCanceled(err) || (ctx != nil && ctx.Err() != nil) {
			attempt.Reason = attemptReasonClientGone
		}
	}
	noteScheduleAttempt(ctx, attempt)
}

// scheduleTraceOf returns the trace collected so far, for the request log.
func scheduleTraceOf(ctx context.Context) *scheduleTrace {
	t := requestTimingFrom(ctx)
	if t == nil {
		return nil
	}
	t.scheduleMu.Lock()
	defer t.scheduleMu.Unlock()
	return t.schedule
}

// attemptTimer measures one provider attempt; the elapsed time is what makes a
// trace readable ("this one hung for 2s, then we moved on").
type attemptTimer struct {
	started time.Time
}

func startAttempt() attemptTimer {
	return attemptTimer{started: time.Now()}
}

func (a attemptTimer) millis() int64 {
	return time.Since(a.started).Milliseconds()
}
