package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Admission control per input provider.
//
// Without a bound, one chatty client can open every request it likes against a
// subscription account and the vendor answers 429 — which the gateway then has
// to read back as a provider failure, cool the account down and fail over. A
// lane refuses the request before it leaves, so the account is never punished
// for a burst the gateway could have shaped itself.
const (
	// providerRequestsPerMinuteCap guards a mistyped rate (values above this are
	// almost certainly a misconfigured TPM field).
	providerRequestsPerMinuteCap = 100000
	// providerQueueWaitCap bounds the configured wait: past a minute a client
	// has usually given up, and the queue would only be holding memory.
	providerQueueWaitCap = 60 * time.Second
	// providerConcurrencyCap guards a mistyped limit.
	providerConcurrencyCap = 1000
	// providerRetryAfterSeconds is what a refused request is told to wait when
	// nothing better is known. The lane does not know when a slot frees, so this
	// is a nudge, not a promise.
	providerRetryAfterSeconds = 1
	// providerBusyRetryAfterSeconds is the same nudge sized to a short stream: a
	// concurrency slot typically frees within one request, so retrying after a
	// second is right; the rate budget below overrides it with the real wait.
	providerBusyRetryAfterSeconds = 1
	// providerRetryAfterCap bounds what a client is told to sleep: past a minute
	// it has usually given up, and a longer header only invites it to pin a
	// connection open.
	providerRetryAfterCap = 60
)

// errProviderSaturated refuses a request at the gateway instead of letting the
// upstream answer 429. It is a distinct error because the failover loop must
// treat it as "this account is busy", never as "this account is broken": a
// saturated provider is healthy and must not be cooled down.
var errProviderSaturated = errors.New("provider is at its concurrent request limit")

// providerLane is one provider's concurrency budget plus the requests waiting
// for a slot. Waiters are served in arrival order: each request that has to
// queue takes the next ticket and may only take a slot while it holds the
// lowest live ticket, so a burst cannot starve the request that has been
// waiting longest. All waiters sleep on one broadcast channel — a release wakes
// them together and each re-checks under the lock whether the turn is theirs —
// but unlike a free-for-all that check is not "is there a slot", it is "is the
// turn mine", which is what makes the wake-up order the arrival order.
//
// A waiter that gives up or disconnects is struck out of queue by ticket, so
// the requests behind it keep the places they took: a request that never
// reached the front must not make the lane wait for a turn nobody will ever
// use.
type providerLane struct {
	mu         sync.Mutex
	inflight   int
	max        int
	queueCap   int
	queue      []uint64 // live tickets, ascending; the head holds the turn
	nextTicket uint64   // ticket the next arrival will take
	wake       chan struct{}
	// rpm is the per-minute request budget and admits holds the admission time
	// (in unix nanos) of the requests inside the last minute. A request is
	// counted on admission — that is when the vendor sees it — and the window
	// slides with wall clock: an admission older than a minute stops counting,
	// so the budget refilling never depends on a fixed window boundary that a
	// clock jump could skip or double.
	rpm    int
	admits []int64
	// lastSeen is the latest clock reading this lane has admitted at. A clock
	// that jumps backwards must not stretch the minute: without it, an
	// admission stamped in the future never ages out and the account stays
	// refused for as long as the clock is wrong.
	lastSeen int64
}

type providerLanes struct {
	mu    sync.Mutex
	lanes map[string]*providerLane
	// mixedWarned remembers the groups already reported as mixing a row that
	// declares no limit with rows that do, so the warning is written once per
	// group instead of once per request.
	mixedWarned map[string]bool
}

func newProviderLanes() *providerLanes {
	return &providerLanes{lanes: map[string]*providerLane{}, mixedWarned: map[string]bool{}}
}

// warnMixedLaneGroup logs, once per group, that a row without limits of its own
// is being admitted under the limits its siblings declare, and what those are.
func (s *Server) warnMixedLaneGroup(provider domain.Provider, max, rate int) {
	if s.logs == nil || s.providerLanes == nil {
		return
	}
	group := providerLaneGroup(provider)
	s.providerLanes.mu.Lock()
	warned := s.providerLanes.mixedWarned[group]
	if !warned {
		if s.providerLanes.mixedWarned == nil {
			s.providerLanes.mixedWarned = map[string]bool{}
		}
		s.providerLanes.mixedWarned[group] = true
	}
	s.providerLanes.mu.Unlock()
	if warned {
		return
	}
	s.logs.AddApp("warn", "lane group mixes limited and unlimited providers",
		fmt.Sprintf("account %q: %s has no limit of its own and is bounded by the group's %d concurrent, %d/min", group, provider.ID, max, rate))
}

// retryAfterSeconds is what the proxy tells a client refused at admission for
// this provider. A provider that never built a lane (nothing to be saturated
// by) falls back to the constant nudge.
func (l *providerLanes) retryAfterSeconds(providerID string, now time.Time) int {
	if l == nil {
		return providerRetryAfterSeconds
	}
	l.mu.Lock()
	lane, ok := l.lanes[providerID]
	l.mu.Unlock()
	if !ok {
		return providerRetryAfterSeconds
	}
	return lane.retryAfterSeconds(now)
}

// retryAfterSecondsFor is retryAfterSeconds for call sites that hold the
// provider itself: the lookup goes through providerLaneKey, the same key the
// lane was stored under, so a grouped provider is told how long its account's
// lane will take and a row of its own reads back its own.
func (l *providerLanes) retryAfterSecondsFor(provider domain.Provider, now time.Time) int {
	return l.retryAfterSeconds(providerLaneKey(provider), now)
}

// laneFor returns the provider's lane, sized to the given configuration. A
// provider with no limit has no lane at all.
func (l *providerLanes) laneFor(providerID string, max, rpm int) *providerLane {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.laneLocked(providerID, max, rpm)
}

// laneForProvider is laneFor from the provider's own row: the lane size comes
// from the whole account, not from this row. Rows of one LaneGroup share one
// key but each carries its own limits — a console edit of one row does not
// rewrite its siblings — so taking whichever row arrived last would make the
// account's budget ping-pong. A grouped account's size is therefore derived from
// all its live members at once, independent of arrival order:
//
//   - each limit is the SMALLEST non-zero value any member declares. The
//     budget protects the account from being over-spent, so the tightest
//     declaration is the safe one: a member set to 10/min exists because that
//     account allows 10/min, and no sibling should talk it up to 50.
//   - a member declaring 0 (no limit) does not vote, but it is still bounded by
//     the limit the declaring members set — a 0/0 row must never buy the
//     account an unbounded lane (D29-4).
//   - when every member is 0/0 the group has no limit and keeps the free path.
func (l *providerLanes) laneForProvider(provider domain.Provider, providers []domain.Provider) (lane *providerLane, mixed bool) {
	max := providerConcurrentCap(provider)
	rate := providerRequestsPerMinute(provider)
	group := providerLaneGroup(provider)
	if group == "" {
		return l.laneFor(provider.ID, max, rate), false
	}
	seenMax, seenRate := false, false
	for _, member := range providers {
		if member.Deleted || !strings.EqualFold(providerLaneGroup(member), group) {
			continue
		}
		if memberMax := providerConcurrentCap(member); memberMax > 0 {
			if !seenMax {
				max = memberMax
				seenMax = true
			} else if memberMax < max {
				max = memberMax
			}
		}
		if memberRate := providerRequestsPerMinute(member); memberRate > 0 {
			if !seenRate {
				rate = memberRate
				seenRate = true
			} else if memberRate < rate {
				rate = memberRate
			}
		}
	}
	if !seenMax {
		max = 0
	}
	if !seenRate {
		rate = 0
	}
	mixed = (seenMax || seenRate) && providerConcurrentCap(provider) == 0 && providerRequestsPerMinute(provider) == 0
	return l.laneFor(providerLaneKey(provider), max, rate), mixed
}

// laneLocked is laneFor with the lanes lock already held.
func (l *providerLanes) laneLocked(providerID string, max, rpm int) *providerLane {
	lane, ok := l.lanes[providerID]
	if !ok {
		lane = &providerLane{max: max, rpm: rpm, queueCap: providerQueueCapacity(max, rpm), wake: make(chan struct{})}
		l.lanes[providerID] = lane
		return lane
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	lane.max = max
	lane.queueCap = providerQueueCapacity(max, rpm)
	// Tracking the configured rate here (not only in setRate) means clearing it
	// in the console takes effect on the next request, not on restart.
	lane.rpm = rpm
	return lane
}

// providerQueueCapacity is how many requests a lane may hold waiting for a
// slot. A rate-only lane (max 0) still needs a queue: waiting out the rate
// window is the only way such a lane ever serves another request.
func providerQueueCapacity(max, rpm int) int {
	if max < 1 && rpm < 1 {
		return 0
	}
	// Sized to what can arrive while one window turns over (at most rpm
	// requests may be waiting on the rate), then to concurrency, whichever is
	// larger — a single request must never be told "queue full".
	capacity := rpm
	if concurrency := max * 4; concurrency > capacity {
		capacity = concurrency
	}
	if capacity < 8 {
		capacity = 8
	}
	return capacity
}

// acquire takes a slot, waiting up to wait for one to free. A zero wait is the
// default: a queued request is a request whose client is already staring at a
// spinner.
func (lane *providerLane) acquire(ctx context.Context, wait time.Duration) (func(), error) {
	// A rate-full lane is not a hung one: waiting on the vendor's own minute
	// is meaningful even when the operator set no queue wait, so a rate-only
	// refusal is given until the oldest admission frees a slot (bounded anyway).
	// The wait refunds itself: a converted stream opens with its upstream
	// response, so a request parked here for the rest of a minute has committed
	// nothing if that response is an error, and the failover loop can still
	// hand the turn to a backup.
	budget := wait
	lane.mu.Lock()
	overWindow := lane.rpm > 0 && lane.admittedLocked() >= lane.rpm
	lane.mu.Unlock()
	if budget <= 0 && lane.max <= 0 {
		lane.mu.Lock()
		now := time.Now()
		lane.trimLocked(now)
		if lane.rpm > 0 && lane.admittedLocked() >= lane.rpm {
			budget = time.Duration(lane.admits[0]+int64(time.Minute)-now.UnixNano()) + time.Millisecond
			if budget > providerQueueWaitCap {
				budget = providerQueueWaitCap
			}
		}
		lane.mu.Unlock()
	}
	release, err := lane.acquireSlot(ctx, budget, time.Now())
	if err != nil {
		return nil, err
	}
	if wait <= 0 && overWindow {
		return func() { lane.refundAdmission(time.Now()) }, nil
	}
	return release, nil
}

// refundAdmission withdraws the count this request added, for one that waited
// out a full rate window and whose turn then never reached its upstream. The
// wait already paid the vendor's rate: keeping the count too would spend the
// account's budget on an attempt nobody made.
func (lane *providerLane) refundAdmission(now time.Time) {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if len(lane.admits) == 0 {
		return
	}
	lane.admits = lane.admits[:len(lane.admits)-1]
	lane.trimLocked(now)
	lane.wakeLocked()
}

// acquireSlot is acquire with the clock injected, so the per-minute budget can
// be tested without sleeping.
func (lane *providerLane) acquireSlot(ctx context.Context, wait time.Duration, now time.Time) (func(), error) {
	budget := now.Add(wait)
	// ticket is this request's place in the queue; it is taken on the first
	// wait and kept across retries so a request that was woken, found the rate
	// full and queued again does not lose the turn it had already waited for.
	var ticket uint64
	queued := false
	for {
		if err := ctx.Err(); err != nil {
			if queued {
				lane.retire(ticket)
			}
			return nil, lane.exhausted(err)
		}
		lane.mu.Lock()
		// A request holds no turn once it is admitted: a later attempt (the
		// retry after a wake) must not be gated on a ticket it already used.
		if !queued || lane.turnLocked(ticket) {
			if admitted, at := lane.admitLocked(now); admitted {
				if lane.max <= 0 || lane.inflight < lane.max {
					if queued {
						lane.passTurnLocked(ticket)
					}
					if lane.max <= 0 {
						lane.mu.Unlock()
						return func() {}, nil
					}
					lane.inflight++
					lane.mu.Unlock()
					return lane.release, nil
				}
				// The admission already counted this request; give it back so the
				// retry does not spend the budget twice.
				lane.uncountLocked(now, at)
			}
		}
		if wait <= 0 || !now.Before(budget) {
			if queued {
				lane.retireLocked(ticket)
			}
			lane.mu.Unlock()
			return nil, lane.saturatedLocked(now, wait)
		}
		if !queued {
			if len(lane.queue) >= lane.queueCap {
				waiting, limit := len(lane.queue), lane.queueCap
				lane.mu.Unlock()
				return nil, fmt.Errorf("%w (queue full, %d/%d waiting)", errProviderSaturated, waiting, limit)
			}
			ticket = lane.enqueueLocked()
			queued = true
		}
		// A request behind someone else's turn is not holding the CPU for
		// anything: it hands the lock back and retries when a release or a
		// retirement moves the turn on.
		wake := lane.wake
		lane.mu.Unlock()

		// Wait out the whole budget rather than guessing at a refill time: a
		// release wakes this at once, and a rate-only lane — whose release is
		// a no-op — then makes its last admission here, exactly when its
		// minute window has slid past the oldest admission.
		if err := lane.waitFor(ctx, wake, budget, now); err != nil {
			if !errors.Is(err, errProviderWaitExpired) {
				lane.retire(ticket)
				return nil, lane.exhausted(err)
			}
			now = budget
			continue
		}
		now = time.Now()
	}
}

// exhausted reports a wait the request's own context ended. A deadline is the
// caller's clock running out of patience on a saturated lane, which is what the
// caller answers a client with; anything else (a disconnect) is that error.
func (lane *providerLane) exhausted(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		lane.mu.Lock()
		defer lane.mu.Unlock()
		return lane.saturatedLocked(time.Now(), 0)
	}
	return err
}

// errProviderWaitExpired says a wait ran out at its deadline; the loop makes
// its last admission attempt there before refusing.
var errProviderWaitExpired = errors.New("provider wait expired")

// waitFor blocks until the lane broadcasts, the deadline passes or the context
// ends.
func (lane *providerLane) waitFor(ctx context.Context, wake <-chan struct{}, deadline time.Time, now time.Time) error {
	timer := time.NewTimer(deadline.Sub(now))
	defer timer.Stop()
	select {
	case <-wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errProviderWaitExpired
	}
}

// saturatedLocked explains which budget refuses the request. The caller holds
// the lock.
func (lane *providerLane) saturatedLocked(now time.Time, wait time.Duration) error {
	if lane.max > 0 && lane.inflight >= lane.max {
		return fmt.Errorf("%w (%d/%d already in flight)", errProviderSaturated, lane.inflight, lane.max)
	}
	if lane.rpm > 0 {
		return fmt.Errorf("%w (%d/%d requests this minute)", errProviderSaturated, lane.admittedLocked(), lane.rpm)
	}
	return fmt.Errorf("%w (waited %s for a free slot)", errProviderSaturated, wait)
}

// admitLocked applies the per-minute budget. It returns false when the window
// is full; otherwise it counts the request and returns the entry it stored (0
// for a lane with no rate), which is what withdraws the count again.
func (lane *providerLane) admitLocked(now time.Time) (bool, int64) {
	if lane.rpm <= 0 {
		return true, 0
	}
	lane.trimLocked(now) // sync lane.admits with the current window first
	if lane.admittedLocked() >= lane.rpm {
		return false, 0
	}
	at := now.UnixNano()
	lane.admits = append(lane.admits, at)
	lane.trimLocked(now)
	return true, at
}

// uncountLocked withdraws the admission made for a request that then found the
// concurrency budget full. The caller passes the entry to withdraw: the count
// is not always the newest one, because trimming can slide an older admission
// out in the same pass that counts this one, and withdrawing "the last" then
// takes a different request's place in the window.
func (lane *providerLane) uncountLocked(now time.Time, at int64) {
	if lane.rpm <= 0 {
		return
	}
	for i := len(lane.admits) - 1; i >= 0; i-- {
		if lane.admits[i] == at {
			lane.admits = append(lane.admits[:i], lane.admits[i+1:]...)
			break
		}
	}
	lane.trimLocked(now)
}

// trimLocked drops admissions that fell out of the last minute and compacts the
// ring so the slice cannot grow without bound.
func (lane *providerLane) trimLocked(now time.Time) {
	stamp := now.UnixNano()
	if stamp > lane.lastSeen {
		lane.lastSeen = stamp
	} else if lane.lastSeen-stamp > int64(time.Minute) {
		// The clock went back by more than the window: everything counted in
		// the old window belongs to a past that the clock now says is far away.
		// Drop it rather than keep the account refused until the clock recovers.
		lane.admits = nil
	}
	// The window is measured from the latest reading, never from an earlier
	// one: a small backwards jump keeps the minute it already served.
	if lane.lastSeen > stamp {
		stamp = lane.lastSeen
	}
	now = time.Unix(0, stamp)
	cutoff := now.Add(-time.Minute).UnixNano()
	drop := 0
	for drop < len(lane.admits) && lane.admits[drop] <= cutoff {
		drop++
	}
	lane.admits = lane.admits[drop:]
}

// admittedLocked is how many requests the current minute window has counted.
func (lane *providerLane) admittedLocked() int {
	return len(lane.admits)
}

// enqueueLocked gives the next arrival its place in the queue. The caller holds
// the lock and has already checked the queue has room. Tickets are never
// reused, so a struck-out ticket can never come back to mean someone else.
func (lane *providerLane) enqueueLocked() uint64 {
	ticket := lane.nextTicket
	lane.nextTicket++
	lane.queue = append(lane.queue, ticket)
	return ticket
}

// strikeLocked removes one ticket from the queue, wherever it is. The caller
// holds the lock.
func (lane *providerLane) strikeLocked(ticket uint64) bool {
	for i, queued := range lane.queue {
		if queued == ticket {
			lane.queue = append(lane.queue[:i], lane.queue[i+1:]...)
			return i == 0
		}
	}
	return false
}

// retireLocked strikes a waiter that left the queue out of it. Only the head
// holds the turn anyone is waiting on, so only the head has to move the queue
// on; a waiter further back is simply removed and the requests behind it keep
// the places they took. The caller holds the lock.
func (lane *providerLane) retireLocked(ticket uint64) {
	if lane.strikeLocked(ticket) {
		lane.wakeLocked()
	}
}

// retire is retireLocked for the paths that are not holding the lock.
func (lane *providerLane) retire(ticket uint64) {
	lane.mu.Lock()
	lane.retireLocked(ticket)
	lane.mu.Unlock()
}

// turnLocked reports whether this ticket may take a slot: it is the live head
// of the queue.
func (lane *providerLane) turnLocked(ticket uint64) bool {
	return len(lane.queue) > 0 && lane.queue[0] == ticket
}

// passTurnLocked takes an admitted ticket out of the queue, handing the turn to
// the next live waiter, and wakes the sleepers to re-check. The caller holds the
// lock.
func (lane *providerLane) passTurnLocked(ticket uint64) {
	lane.strikeLocked(ticket)
	lane.wakeLocked()
}

// wakeLocked releases every waiter to re-check its turn. The caller holds the
// lock.
func (lane *providerLane) wakeLocked() {
	close(lane.wake)
	lane.wake = make(chan struct{})
}

func (lane *providerLane) release() {
	lane.mu.Lock()
	if lane.inflight > 0 {
		lane.inflight--
	}
	lane.wakeLocked()
	lane.mu.Unlock()
}

func (lane *providerLane) inFlight() int {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	return lane.inflight
}

// retryAfterSeconds is what a request this lane just refused is told to wait.
// The rate budget knows exactly when its minute window restarts, so it answers
// with that; a plain concurrency refusal has no such knowledge and keeps the
// short nudge, capped either way.
func (lane *providerLane) retryAfterSeconds(now time.Time) int {
	if lane == nil {
		return providerRetryAfterSeconds
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.rpm <= 0 {
		return providerBusyRetryAfterSeconds
	}
	// The oldest admission still inside the window is what frees the next slot:
	// that is when one request falls out of the last minute.
	lane.trimLocked(now)
	remaining := time.Minute
	if lane.admittedLocked() > 0 {
		remaining = time.Duration(lane.admits[0] + int64(time.Minute) - now.UnixNano())
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	if seconds > providerRetryAfterCap {
		seconds = providerRetryAfterCap
	}
	return seconds
}

// waiterCount is how many requests are waiting for a slot. It exists for the
// console's view of a saturated provider and for tests that need to know a
// waiter is actually queued before releasing.
func (lane *providerLane) waiterCount() int {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	return len(lane.queue)
}

// providerConcurrencyConfig reads the two knobs off a provider with their
// guards applied.
func providerConcurrencyConfig(provider domain.Provider) (max int, wait time.Duration) {
	max = providerConcurrentCap(provider)
	seconds := provider.QueueWaitSeconds
	// A WorkBuddy account stored before this default existed has no wait either;
	// give it one, so a burst waits for a slot instead of failing over onto
	// another account and spreading the burst (see providerConcurrentCap).
	if seconds == 0 && max > 0 && provider.MaxConcurrentRequests == 0 && isWorkBuddyAuthType(provider.AuthType) {
		seconds = workBuddyDefaultQueueWaitSeconds
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > int(providerQueueWaitCap/time.Second) {
		seconds = int(providerQueueWaitCap / time.Second)
	}
	return max, time.Duration(seconds) * time.Second
}

// providerConcurrentCap is the concurrency knob alone, guards applied.
func providerConcurrentCap(provider domain.Provider) int {
	max := provider.MaxConcurrentRequests
	if max < 0 {
		return 0
	}
	// A WorkBuddy account's own client sends one request at a time, so an
	// account left unconfigured gets a small in-flight cap rather than none.
	// This is applied where the cap is read, not only where a provider is
	// saved: accounts already in the database read 0 (= unlimited) and are
	// never re-saved, so a save-time default never reached them.
	if max == 0 && provider.RequestsPerMinute == 0 && isWorkBuddyAuthType(provider.AuthType) {
		max = workBuddyDefaultConcurrency(provider.AuthType)
	}
	if max > providerConcurrencyCap {
		return providerConcurrencyCap
	}
	return max
}

// providerRequestsPerMinute reads the per-minute request budget with the same
// guards as the concurrency knobs.
func providerRequestsPerMinute(provider domain.Provider) int {
	limit := provider.RequestsPerMinute
	if limit < 0 {
		return 0
	}
	if limit > providerRequestsPerMinuteCap {
		return providerRequestsPerMinuteCap
	}
	return limit
}

// providerLaneGroupMaxLen bounds the account name a console user may type; it
// only has to be longer than any plausible account label.
const providerLaneGroupMaxLen = 120

// providerLaneKey is the account whose admission budget the provider spends.
//
// One Provider row is normally one credential, so its own ID is the account.
// When several rows are really the same subscription they name the same
// LaneGroup and share one lane: without it each row would get the full
// concurrency and per-minute budget, which is how a 5-request-a-minute account
// ends up answering 429 on its own quota.
//
// The group name is matched case-insensitively: "Acme" and "acme" in the admin
// UI mean the same subscription, so they must land on the same lane.
func providerLaneKey(provider domain.Provider) string {
	if group := providerLaneGroup(provider); group != "" {
		return "group:" + strings.ToLower(group)
	}
	return provider.ID
}

// providerLaneGroup is the account name the provider shares, or "" for a row
// with a budget of its own.
func providerLaneGroup(provider domain.Provider) string {
	return strings.TrimSpace(provider.LaneGroup)
}

func providerGrouped(provider domain.Provider) bool {
	return providerLaneGroup(provider) != ""
}

// acquireProviderSlot reserves one upstream slot for the whole exchange. The
// release is safe to call more than once per request path because it is only
// ever called by the deferred closure the caller got back.
//
// The account is resolved before the "no limit" shortcut: a row with 0/0 in a
// group that does have a budget must still spend the group's, otherwise the
// cheapest row to add silently buys every client an unbounded lane.
func (s *Server) acquireProviderSlot(ctx context.Context, provider domain.Provider) (func(), error) {
	if s.providerLanes == nil {
		return func() {}, nil
	}
	// Without a router there is no account list to size a group from; the row's
	// own limits are all there is (tests build such a Server).
	var providers []domain.Provider
	if s.router != nil {
		providers = s.router.State().Providers
	}
	lane, mixed := s.providerLanes.laneForProvider(provider, providers)
	lane.mu.Lock()
	max, rate := lane.max, lane.rpm
	lane.mu.Unlock()
	if max <= 0 && rate <= 0 {
		return func() {}, nil
	}
	if mixed {
		// A row that declares no limit of its own spends one its siblings set.
		// It is throttled on purpose (D29-4), but the operator has to be able
		// to see why, so say it the first time this row is admitted.
		s.warnMixedLaneGroup(provider, max, rate)
	}
	return lane.acquire(ctx, providerLaneQueueWait(provider, providers))
}

// providerLaneQueueWait is how long a request on this row may wait for a slot.
// 额度按最紧的成员算，但等待不是额度：按本行的值会让"同一账号的另一行写了
// 等 5 秒"失效，请求刚到就被拒。等多久都不会让账号超支，所以取组内最大。
func providerLaneQueueWait(provider domain.Provider, providers []domain.Provider) time.Duration {
	_, wait := providerConcurrencyConfig(provider)
	group := providerLaneGroup(provider)
	if group == "" {
		return wait
	}
	for _, member := range providers {
		if member.Deleted || !strings.EqualFold(providerLaneGroup(member), group) {
			continue
		}
		if _, memberWait := providerConcurrencyConfig(member); memberWait > wait {
			wait = memberWait
		}
	}
	return wait
}

// providerInFlight is how many requests are in flight for the provider's
// account. The lane is stored under providerLaneKey, so a grouped row has to be
// read back through the same key — its own ID names no lane at all.
func (s *Server) providerInFlight(provider domain.Provider) int {
	if s == nil || s.providerLanes == nil {
		return 0
	}
	s.providerLanes.mu.Lock()
	lane, ok := s.providerLanes.lanes[providerLaneKey(provider)]
	s.providerLanes.mu.Unlock()
	if !ok {
		return 0
	}
	return lane.inFlight()
}

// handleSetProviderConcurrency is the admin-only per-provider admission knob, on
// its own endpoint for the same reason the first-packet timeout is: the provider
// PATCH is partial, and an int there cannot tell "not sent" from "cleared".
func (s *Server) handleSetProviderConcurrency(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload struct {
		MaxConcurrentRequests *int `json:"maxConcurrentRequests"`
		QueueWaitSeconds      *int `json:"queueWaitSeconds"`
		RequestsPerMinute     *int `json:"requestsPerMinute"`
		// Omitted keeps the provider's current account grouping; "" is how the
		// console puts a provider back on a budget of its own.
		LaneGroup *string `json:"laneGroup"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.MaxConcurrentRequests == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"maxConcurrentRequests": N, "queueWaitSeconds": N?, "requestsPerMinute": N?, "laneGroup": "name"?}`)
		return
	}
	wait := 0
	if payload.QueueWaitSeconds != nil {
		wait = *payload.QueueWaitSeconds
	}
	rpm := 0
	if payload.RequestsPerMinute != nil {
		rpm = *payload.RequestsPerMinute
	}
	laneGroup := ""
	if current, err := s.router.ProviderByID(r.PathValue("id")); err == nil {
		laneGroup = current.LaneGroup
	}
	if payload.LaneGroup != nil {
		laneGroup = strings.TrimSpace(*payload.LaneGroup)
		if len(laneGroup) > providerLaneGroupMaxLen {
			writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("laneGroup is at most %d characters", providerLaneGroupMaxLen))
			return
		}
	}
	updated, err := s.router.SetProviderConcurrency(r.PathValue("id"), *payload.MaxConcurrentRequests, wait, rpm, laneGroup)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if s.logs != nil {
		if updated.MaxConcurrentRequests == 0 && updated.RequestsPerMinute == 0 {
			s.logs.AddApp("info", "provider concurrency limit cleared", updated.ID)
		} else {
			target := updated.ID
			if group := strings.TrimSpace(updated.LaneGroup); group != "" {
				target = fmt.Sprintf("%s (shares account lane %q)", updated.ID, group)
			}
			s.logs.AddApp("info", "provider concurrency limit set", fmt.Sprintf("%s: %d concurrent, wait %ds, %d/min", target, updated.MaxConcurrentRequests, updated.QueueWaitSeconds, updated.RequestsPerMinute))
		}
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}
