package gateway

// GET /__host-metrics/stream — the machine page's live feed.
//
// Before this, the page polled /__host-metrics every 3 s and the sampler quit
// after 30 s without requests, so the chart only existed while the tab was
// open and restarted from zero on every visit. Now:
//
//   - the sampler runs for the life of the process (slow cadence while nobody
//     watches, full cadence while a stream is open),
//   - the ring keeps one hour in memory and SQLite keeps 24 h,
//   - this endpoint sends the whole window on connect and then one frame per
//     new sample, with `id:` set to the sample's millisecond stamp so a
//     reconnecting EventSource resumes from Last-Event-ID.
//
// The frame vocabulary mirrors the monitoring stream so the console already
// knows how to read it:
//
//	event: snapshot  {serverTime, intervalMs, windowMs, points:[HostMetricSample], latest:HostMetrics}
//	event: sample    {point:HostMetricSample, latest?:HostMetrics}   id = point.at
//	": ping"                                                         heartbeat comment
//
// A subscriber that falls hostStreamQueue frames behind silently loses the
// oldest ones; the client never sees a hole for long, because the next
// snapshot on reconnect refills the whole window. (It reconnects on its own
// when the stream errors, and the console also resyncs after a long gap.)

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	hostStreamMaxSubscribers = 16
	hostStreamQueue          = 32
	// hostStreamPushInterval is how often the stream checks for a new sample.
	// It matches the fastest cadence the sampler can run at, so a frame is
	// never late by more than one tick.
	hostStreamPushInterval = time.Second
)

const (
	hostStreamSnapshot = "snapshot"
	hostStreamSample   = "sample"
)

type hostStreamEnvelope struct {
	Event string
	Data  json.RawMessage
	ID    int64
}

type hostStreamSubscriber struct {
	ch chan hostStreamEnvelope
}

// hostMetricsStreamHub fans samples out to consoles. Publishing never blocks:
// the oldest queued frame is dropped instead, so no client can slow the
// sampler down.
type hostMetricsStreamHub struct {
	mu   sync.Mutex
	subs map[*hostStreamSubscriber]struct{}
}

func newHostMetricsStreamHub() *hostMetricsStreamHub {
	return &hostMetricsStreamHub{subs: map[*hostStreamSubscriber]struct{}{}}
}

func (h *hostMetricsStreamHub) subscribe() (*hostStreamSubscriber, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= hostStreamMaxSubscribers {
		return nil, false
	}
	sub := &hostStreamSubscriber{ch: make(chan hostStreamEnvelope, hostStreamQueue)}
	h.subs[sub] = struct{}{}
	return sub, true
}

func (h *hostMetricsStreamHub) unsubscribe(sub *hostStreamSubscriber) {
	if sub == nil {
		return
	}
	h.mu.Lock()
	delete(h.subs, sub)
	h.mu.Unlock()
}

func (h *hostMetricsStreamHub) publish(envelope hostStreamEnvelope) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if len(sub.ch) >= hostStreamQueue {
			// Drop the oldest frame so the newest still arrives.
			select {
			case <-sub.ch:
			default:
			}
		}
		select {
		case sub.ch <- envelope:
		default:
		}
	}
}

func (s *Server) hostStreamHub() *hostMetricsStreamHub {
	s.hostStreamsOnce.Do(func() {
		if s.hostStreams == nil {
			s.hostStreams = newHostMetricsStreamHub()
		}
	})
	return s.hostStreams
}

// hostMetricsPump pushes every new sample to the hub. One goroutine per server,
// started on first use; it never stops (the sampler outlives pages too).
func (s *Server) hostMetricsPump() {
	s.hostPumpOnce.Do(func() {
		go func() {
			sampler := s.hostSampler()
			sampler.Start()
			lastSent := int64(0)
			ticker := time.NewTicker(hostStreamPushInterval)
			defer ticker.Stop()
			for range ticker.C {
				points := sampler.History()
				if len(points) == 0 {
					continue
				}
				latestAt := points[len(points)-1].AtMs
				if lastSent == 0 {
					// First tick: remember where we are; the snapshot frame
					// already carried everything up to here.
					lastSent = latestAt
					continue
				}
				if latestAt <= lastSent {
					continue
				}
				for _, point := range points {
					if point.AtMs <= lastSent {
						continue
					}
					payload := map[string]any{"point": point}
					if point.AtMs == latestAt {
						payload["latest"] = sampler.Latest()
					}
					s.hostStreamHub().publish(hostStreamEnvelope{
						Event: hostStreamSample,
						ID:    point.AtMs,
						Data:  mustStreamJSON(payload),
					})
				}
				lastSent = latestAt
			}
		}()
	})
}

// handleHostMetricsStream serves the machine page's live feed.
func (s *Server) handleHostMetricsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.router.HostMetricsDisabled() {
		writeOpenAIError(w, http.StatusConflict, "机器状态动态采集已关闭")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	hub := s.hostStreamHub()
	sub, ok := hub.subscribe()
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "机器状态流连接数已达上限")
		return
	}
	defer hub.unsubscribe(sub)
	s.hostMetricsPump()

	sampler := s.hostSampler()
	unsubscribe := sampler.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(envelope hostStreamEnvelope) bool {
		if _, err := w.Write(formatHostStreamFrame(envelope)); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// The snapshot frame carries the whole window, so the client needs nothing
	// else on connect. Frames published between here and the pump's next tick
	// arrive through the queue, and the client drops anything at or below the
	// newest point it already has — no duplicates, no gap.
	points := sampler.History()
	latest := sampler.Snapshot()
	snapshot := map[string]any{
		"serverTime": time.Now().Format(time.RFC3339Nano),
		"intervalMs": hostMetricsPollInterval.Milliseconds(),
		"windowMs":   hostMetricsHistoryWindow.Milliseconds(),
		"points":     points,
		"latest":     latest,
	}
	if !write(hostStreamEnvelope{Event: hostStreamSnapshot, Data: mustStreamJSON(snapshot)}) {
		return
	}

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if s.router.HostMetricsDisabled() {
				return
			}
			if _, err := w.Write(sseHeartbeatComment); err != nil {
				return
			}
			flusher.Flush()
		case envelope := <-sub.ch:
			if !write(envelope) {
				return
			}
		}
	}
}

// formatHostStreamFrame renders one SSE frame. No `id:` on the snapshot: the
// client resumes from the last *sample*, and a snapshot always carries the
// whole ring anyway.
func formatHostStreamFrame(envelope hostStreamEnvelope) []byte {
	var builder strings.Builder
	if envelope.ID > 0 {
		builder.WriteString("id: ")
		builder.WriteString(strconv.FormatInt(envelope.ID, 10))
		builder.WriteString("\n")
	}
	builder.WriteString("event: ")
	builder.WriteString(envelope.Event)
	builder.WriteString("\ndata: ")
	builder.Write(envelope.Data)
	builder.WriteString("\n\n")
	return []byte(builder.String())
}
