package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Scheduling modes a key can choose. An empty mode is today's behavior, so a
// key that was never touched keeps exactly the routing it has now.
const (
	scheduleModeOrder     = "order"
	scheduleModeRotate    = "rotate"
	scheduleModeLeastUsed = "least_used"
	scheduleModeQuota     = "quota"
	scheduleModeSpread    = "spread"
)

// ScheduleModes are the values the console may set ("" is accepted and means
// the same as "order").
var ScheduleModes = []string{"", scheduleModeOrder, scheduleModeRotate, scheduleModeLeastUsed, scheduleModeQuota, scheduleModeSpread}

func normalizeScheduleMode(mode string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(mode))
	switch trimmed {
	case "", scheduleModeOrder, scheduleModeRotate, scheduleModeLeastUsed, scheduleModeQuota, scheduleModeSpread:
		return trimmed, nil
	default:
		return "", fmt.Errorf("unknown schedule mode %q (expected order, rotate, least_used, quota or spread)", mode)
	}
}

// isScheduleModeSpread reports whether the mode balances separate conversations
// across accounts instead of picking by account-level load.
func isScheduleModeSpread(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), scheduleModeSpread)
}

// sessionPinTTL is how long a conversation keeps its account under mode. The
// spread mode holds on longer, because it is the only thing that assigns a
// conversation an account of its own.
func (s *Server) sessionPinTTL(mode string) time.Duration {
	if isScheduleModeSpread(mode) {
		return spreadSessionAffinityTTL
	}
	return sessionAffinityTTL
}

// scheduleInput is what the scheduler decides from. It is deliberately a plain
// value: the ordering is a pure function of it plus the server's runtime
// trackers, which is what makes it testable without any HTTP traffic.
type scheduleInput struct {
	chain  []string
	active string
	mode   string
	keyID  string
	// advance moves the round-robin cursor. Only the request that is about to
	// be sent may do that: the pre-flight attribution (decisionForAPIKey) asks
	// the same question one step earlier and must not consume a step, or every
	// request would skip an account.
	advance bool
}

// failoverAttemptOrder is the order one request tries the chain's providers in.
//
// The chain order is the administrator's priority ranking and ActiveProviderID
// is where this key's last request left off. "order" rotates the chain so the
// active provider comes first: a key that already moved to a backup stops
// re-burning the preferred provider on every request, while still trying the
// providers before it once the backup fails — the loop used to stop at the end
// of the chain, so a key parked on a dead backup could not reach a recovered
// primary until the background probe switched it back up to two minutes later.
// The other modes spread the traffic instead (see the mode constants).
//
// Providers currently tracked as unavailable go last in every mode: the mark
// comes from a real failure or a probe that can be wrong, and a cooled-down
// provider is sometimes the only one that can serve the requested model.
func (s *Server) failoverAttemptOrder(input scheduleInput) []string {
	chain := input.chain
	if len(chain) == 0 {
		return nil
	}
	var ordered []string
	switch strings.ToLower(strings.TrimSpace(input.mode)) {
	case scheduleModeRotate:
		ordered = s.rotateChain(chain, input.keyID, input.advance)
	case scheduleModeLeastUsed:
		ordered = s.leastUsedChain(chain)
	case scheduleModeQuota:
		ordered = s.quotaHeadroomChain(chain)
	case scheduleModeSpread:
		ordered = s.spreadChain(chain)
	default:
		ordered = rotateChainAt(chain, input.active)
	}
	// Credit preference, after the mode has decided the shape: the mode is the
	// administrator's ranking, this only decides who among equals is asked first
	// (drain the soonest-expiring, hold back the nearly-empty). Off by default —
	// see workbuddy_credit_route.go.
	ordered = s.applyWorkBuddyCreditOrder(ordered)
	return s.healthyFirst(ordered)
}

// rotateChainAt returns chain with activeProviderID moved to the front, keeping
// the relative order of everything else. An empty or unknown active id leaves
// the chain as the administrator wrote it.
func rotateChainAt(chain []string, activeProviderID string) []string {
	index := apiKeyEffectiveProviderIndex(strings.TrimSpace(activeProviderID), chain)
	if index <= 0 || index >= len(chain) {
		return append([]string(nil), chain...)
	}
	return rotateChainFrom(chain, index)
}

func rotateChainFrom(chain []string, index int) []string {
	if index <= 0 || index >= len(chain) {
		return append([]string(nil), chain...)
	}
	rotated := make([]string, 0, len(chain))
	rotated = append(rotated, chain[index:]...)
	return append(rotated, chain[:index]...)
}

// rotateCounters remember where each key is in its round-robin. In-memory on
// purpose: a restart starting the walk over again is not a correctness problem,
// and persisting a counter on every request would put a write on the hot path.
type rotateCounters struct {
	mu      sync.Mutex
	indices map[string]uint64
}

func (s *Server) rotateChain(chain []string, keyID string, advance bool) []string {
	if len(chain) < 2 {
		return append([]string(nil), chain...)
	}
	if s.rotate == nil {
		return append([]string(nil), chain...)
	}
	s.rotate.mu.Lock()
	if s.rotate.indices == nil {
		s.rotate.indices = map[string]uint64{}
	}
	index := s.rotate.indices[keyID] % uint64(len(chain))
	if advance {
		s.rotate.indices[keyID] = index + 1
	}
	s.rotate.mu.Unlock()
	return rotateChainFrom(chain, int(index))
}

// leastUsedChain puts the account carrying the least work first. Requests in
// flight outrank the recent window: they are the live signal, and the window is
// what keeps a just-freed account from being hammered again immediately. A
// provider that has served nothing scores zero on both, so a freshly added
// account gets used at once rather than punished for being new.
func (s *Server) leastUsedChain(chain []string) []string {
	ordered := append([]string(nil), chain...)
	type load struct {
		inflight int
		work     int64
	}
	loads := make(map[string]load, len(ordered))
	for _, providerID := range ordered {
		// A grouped row's lane lives under its account, so in-flight is read
		// through the provider itself; the recent window stays per row.
		provider := domain.Provider{ID: providerID}
		if stored, err := s.router.ProviderByID(providerID); err == nil {
			provider = stored
		}
		loads[providerID] = load{inflight: s.providerInFlight(provider), work: s.providerWorkload(providerID)}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := loads[ordered[i]], loads[ordered[j]]
		if left.inflight != right.inflight {
			return left.inflight < right.inflight
		}
		return left.work < right.work
	})
	return ordered
}

// spreadChain puts the account holding the fewest live conversations first, so
// separate conversations land on separate accounts instead of all starting on
// the preferred one. Once a conversation has answered a turn its session pin
// keeps it there (applySessionPin runs after this ordering), so the balance is
// per conversation, not per request. Ties fall back to the live load signals
// least_used uses, and finally to the administrator's chain order.
//
// Only conversations are counted, not accounts: the point of the mode is "one
// account per conversation", which rotate cannot promise because its cursor
// advances per request.
func (s *Server) spreadChain(chain []string) []string {
	ordered := append([]string(nil), chain...)
	type slot struct {
		sessions int
		inflight int
		work     int64
	}
	held := s.sessionPins.countsByProvider()
	slots := make(map[string]slot, len(ordered))
	for _, providerID := range ordered {
		provider := domain.Provider{ID: providerID}
		if stored, err := s.router.ProviderByID(providerID); err == nil {
			provider = stored
		}
		slots[providerID] = slot{sessions: held[providerID], inflight: s.providerInFlight(provider), work: s.providerWorkload(providerID)}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := slots[ordered[i]], slots[ordered[j]]
		if left.sessions != right.sessions {
			return left.sessions < right.sessions
		}
		if left.inflight != right.inflight {
			return left.inflight < right.inflight
		}
		return left.work < right.work
	})
	return ordered
}

// quotaHeadroomChain prefers the subscription with the most quota left. Only
// accounts whose quota panel has been read recently have a number: everything
// else sits in the middle (0.5), so a mixed pool of subscription and API-key
// accounts still works and an unread quota never looks empty.
func (s *Server) quotaHeadroomChain(chain []string) []string {
	ordered := append([]string(nil), chain...)
	headroom := make(map[string]float64, len(ordered))
	for _, providerID := range ordered {
		score, ok := s.providerQuotaHeadroom(providerID)
		if !ok {
			score = 0.5
		}
		headroom[providerID] = score
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return headroom[ordered[i]] > headroom[ordered[j]]
	})
	return ordered
}

// healthyFirst keeps the incoming order inside each group and moves providers
// currently marked unavailable to the end. They are still tried, just last.
func (s *Server) healthyFirst(chain []string) []string {
	if len(chain) < 2 {
		return chain
	}
	healthy := make([]string, 0, len(chain))
	unavailable := make([]string, 0, len(chain))
	for _, providerID := range chain {
		if _, down := s.providerUnavailableSnapshot(providerID); down {
			unavailable = append(unavailable, providerID)
			continue
		}
		healthy = append(healthy, providerID)
	}
	return append(healthy, unavailable...)
}

// preferNativeProtocol moves the first provider that speaks clientProtocol
// (pass-through) to the front, when the chain currently starts with one that
// would need a conversion.
//
// 为什么值得为它改顺序：转换是本网关里最容易出错的一层——报文重写、流重打包、
// 字段兼容，每一处都可能吃掉一次本该成功的请求；直通则只是把字节搬过去。所以
// 同一个上游既支持 Claude 又支持 OpenAI 时（OpenCode Go 就是），让下游的协议
// 决定走哪一条，出问题的概率明显更低。
//
// 这是一条 preference，不是硬规则：
//   - 链上没有任何同协议的 provider 时，原样返回；
//   - 已经是同协议开头时，原样返回；
//   - 会话粘性已经钉住某个 provider 时不动（那是有意的选择，见 applySessionPin）；
//   - 同协议的节点如果全被标记不可用，healthyFirst 会把它排到最后，这里也不会
//     凭空造出一个健康节点——真正的可用性仍由那一层决定。
func (s *Server) preferNativeProtocol(chain []string, clientProtocol domain.Protocol, pinned string) []string {
	if clientProtocol == "" || len(chain) < 2 || pinned != "" || s.router == nil {
		return chain
	}
	first := s.providerProtocol(chain[0])
	if first == "" || first == clientProtocol {
		return chain
	}
	for index := 1; index < len(chain); index++ {
		if s.providerProtocol(chain[index]) != clientProtocol {
			continue
		}
		promoted := make([]string, 0, len(chain))
		promoted = append(promoted, chain[index])
		promoted = append(promoted, chain[:index]...)
		promoted = append(promoted, chain[index+1:]...)
		if s.logs != nil {
			s.logs.AddApp("info", "provider chosen for native protocol", fmt.Sprintf(
				"client=%s promoted=%s over=%s (直通代替转换)", clientProtocol, chain[index], chain[0]))
		}
		return promoted
	}
	return chain
}

// providerProtocol 返回这个 provider 的母语协议，查不到时返回空串。
func (s *Server) providerProtocol(providerID string) domain.Protocol {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return ""
	}
	return provider.Protocol
}

// handleSetProviderFirstPacketTimeout is the admin-only per-provider knob for
// how long one attempt may wait for the upstream's response headers before the
// chain moves on. It is a dedicated endpoint like /rtk and /checkin/auto
// because the provider PATCH is partial and an int cannot tell "not sent" from
// "cleared".
func (s *Server) handleSetProviderFirstPacketTimeout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var payload struct {
		Seconds *int `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Seconds == nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"seconds": N}`)
		return
	}
	updated, err := s.router.SetProviderFirstPacketTimeout(r.PathValue("id"), *payload.Seconds)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.saveState(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save configuration: "+err.Error())
		return
	}
	if s.logs != nil {
		if updated.FirstPacketTimeoutSec == 0 {
			s.logs.AddApp("info", "provider first-packet timeout cleared", updated.ID)
		} else {
			s.logs.AddApp("info", "provider first-packet timeout set", updated.ID+": "+strconv.Itoa(updated.FirstPacketTimeoutSec)+"s")
		}
	}
	writeJSON(w, http.StatusOK, redactProviderForClient(updated))
}
