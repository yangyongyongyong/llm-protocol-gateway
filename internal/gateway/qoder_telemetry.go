package gateway

// What Qoder tells us about itself while it refuses.
//
// Two kinds of signal are worth counting, and neither changes what the gateway
// does about them:
//
//   - how often the vendor pushed back, split by what it pushed back with
//     (a queue, a rate limit, a quota, a version gate). Without this a pool that
//     is being rate-limited looks exactly like a pool that is working.
//   - errors the vendor hid inside an HTTP 200 envelope. Those never reach the
//     client as a status, so a counter is the only place they exist at all.

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// qoderRiskStats counts the vendor's pushback by kind. "last" keeps one detail
// line so an operator can see what the most recent refusal actually said.
type qoderRiskStats struct {
	mu          sync.Mutex
	queue       int
	rate        int
	rejected    int
	versionGate int
	lastKind    string
	lastAt      string
	lastDetail  string
}

var qoderRisk qoderRiskStats

// qoderRiskSnapshot is the counters as the console reads them.
type qoderRiskSnapshot struct {
	Queue       int    `json:"queue"`
	Rate        int    `json:"rate"`
	Rejected    int    `json:"rejected"`
	VersionGate int    `json:"versionGate"`
	LastKind    string `json:"lastKind,omitempty"`
	LastAt      string `json:"lastAt,omitempty"`
	LastDetail  string `json:"lastDetail,omitempty"`
}

func (r *qoderRiskStats) note(kind, detail string) {
	if kind == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case "queue":
		r.queue++
	case "rate":
		r.rate++
	case "version_gate":
		r.versionGate++
	default:
		r.rejected++
		kind = "rejected"
	}
	r.lastKind = kind
	r.lastAt = time.Now().Format("01-02 15:04:05")
	if len(detail) > 120 {
		detail = detail[:120] + "…"
	}
	r.lastDetail = detail
}

func (r *qoderRiskStats) snapshot() qoderRiskSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return qoderRiskSnapshot{
		Queue:       r.queue,
		Rate:        r.rate,
		Rejected:    r.rejected,
		VersionGate: r.versionGate,
		LastKind:    r.lastKind,
		LastAt:      r.lastAt,
		LastDetail:  r.lastDetail,
	}
}

// qoderNoteRefusal counts one refusal the client is about to see, under the kind
// its status and message actually name. A queued model is not a rate limit and
// neither is an empty balance; counting them together is what makes a pool look
// healthy while every request is failing.
func qoderNoteRefusal(status int, message string, wait time.Duration) {
	detail := fmt.Sprintf("http %d: %s", status, message)
	switch {
	case wait > 0 || strings.Contains(message, "排进队列") || strings.Contains(message, "is busy"):
		qoderRisk.note("queue", detail)
	case strings.Contains(message, "版本过低"):
		qoderRisk.note("version_gate", detail)
	case status == 429 || strings.Contains(message, "额度已用完"):
		qoderRisk.note("rate", detail)
	default:
		qoderRisk.note("rejected", detail)
	}
}

// qoderInnerErrors counts errors the vendor wrapped inside a 200. Each kind is
// warned about once: the point is to know they happen at all, not to log every
// one of them.
type qoderInnerErrors struct {
	mu     sync.Mutex
	total  int
	kinds  map[string]int
	warned map[string]bool
}

var qoderInner qoderInnerErrors

// qoderInnerErrorSnapshot is the counters as the console reads them.
type qoderInnerErrorSnapshot struct {
	Total  int            `json:"total"`
	Kinds  map[string]int `json:"kinds"`
	Warned []string       `json:"warned"`
}

func (e *qoderInnerErrors) note(kind string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.kinds == nil {
		e.kinds = map[string]int{}
	}
	if e.warned == nil {
		e.warned = map[string]bool{}
	}
	e.total++
	e.kinds[kind]++
	if e.warned[kind] {
		return false
	}
	e.warned[kind] = true
	return true
}

func (e *qoderInnerErrors) snapshot() qoderInnerErrorSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	kinds := make(map[string]int, len(e.kinds))
	for kind, count := range e.kinds {
		kinds[kind] = count
	}
	warned := make([]string, 0, len(e.warned))
	for kind := range e.warned {
		warned = append(warned, kind)
	}
	return qoderInnerErrorSnapshot{Total: e.total, Kinds: kinds, Warned: warned}
}

// qoderInnerErrorKind classifies one inner error by the words it carries. The
// classes are the ones an operator acts on differently: content refused by the
// vendor's filter is not something a retry fixes, while a throttle is.
func qoderInnerErrorKind(message string) string {
	lower := strings.ToLower(message)
	switch {
	case containsAnyHint(lower, []string{
		"datainspectionfailed", "inappropriate content", "contentfilter",
		"sensitivecontent", "data_inspection", "内容审核",
	}):
		return "content_policy"
	case containsAnyHint(lower, []string{
		"rate_limit", "throttl", "frequency limit", "10605", "too many",
	}):
		return "rate_limit"
	case containsAnyHint(lower, []string{
		"invalid_request", "invalid_parameter", "provider_error", "not found",
		"must be a response", "unsupported",
	}):
		return "invalid_request"
	case containsAnyHint(lower, []string{
		"unauthor", "forbidden", "permission", "expired", "token",
	}):
		return "auth"
	}
	return "other"
}

// noteQoderInnerError records one error the vendor hid inside a 200 and reports
// whether this is the first of its kind (the caller logs only the first).
func noteQoderInnerError(message string) (kind string, first bool) {
	kind = qoderInnerErrorKind(message)
	return kind, qoderInner.note(kind)
}
