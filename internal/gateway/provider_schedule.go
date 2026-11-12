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

// preferNativeProtocol 在不越过主候选的前提下，把备选链中第一个能按
// clientProtocol 直通的 Provider 提到备选的第一位。
//
// 为什么仍要优化：转换是网关里最容易出错的一层——报文重写、流重打包、字段兼容，
// 每一处都可能吃掉一次本该成功的请求；直通则只是把字节搬过去。首选失败之后，
// 若后面的节点支持客户端协议，先试它可以少一次转换。
//
// 但协议直通只是成本优化，不能覆盖管理员显式配置的流量归属。首选 Provider、
// 会话 pin、rotate/least_used/spread 等调度模式选出的 chain[0] 必须先被尝试；
// 这里只调整 chain[1:] 的顺序。其它边界：
//   - 链上没有任何同协议的备选时，原样返回；
//   - 备选里的第一个已经是同协议时，原样返回；
//   - 会话粘性已经钉住某个 provider 时不动（见 applySessionPin）；
//   - 不可用节点会先被 healthyFirst 排到后面，这里不会凭空造出健康节点。
func (s *Server) preferNativeProtocol(chain []string, clientProtocol domain.Protocol, pinned string) []string {
	if clientProtocol == "" || len(chain) < 3 || pinned != "" || s.router == nil {
		return chain
	}
	// chain[1] 已经是直通节点时无需调整；只从备选的第二个位置开始找。
	if s.providerServesNative(chain[1], clientProtocol) {
		return chain
	}
	for index := 2; index < len(chain); index++ {
		if !s.providerServesNative(chain[index], clientProtocol) {
			continue
		}
		promoted := make([]string, 0, len(chain))
		promoted = append(promoted, chain[0], chain[index])
		promoted = append(promoted, chain[1:index]...)
		promoted = append(promoted, chain[index+1:]...)
		if s.logs != nil {
			s.logs.AddApp("info", "fallback ordered for native protocol", fmt.Sprintf(
				"client=%s primary=%s promoted=%s (仅优化备选顺序，直通代替转换)", clientProtocol, chain[0], chain[index]))
		}
		return promoted
	}
	return chain
}

// providerServesNative 报告这台上游能不能按客户端协议直通：声明协议一致，
// 或者实测支持清单里有它。后者是同一台 baseUrl 上多开一条协议路径的上游
// （OpenCode Go 这类），调度时同样应该优先直通。
func (s *Server) providerServesNative(providerID string, clientProtocol domain.Protocol) bool {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return false
	}
	if provider.Protocol == clientProtocol {
		return true
	}
	_, ok := nativeProtocolForProvider(provider, clientProtocol)
	return ok
}

// providerProtocol 返回这个 provider 的母语协议，查不到时返回空串。
func (s *Server) providerProtocol(providerID string) domain.Protocol {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return ""
	}
	return provider.Protocol
}

// nativeProtocolForProvider 返回这台上游可以直通客户端协议时该用的协议。
//
// 只认实测结论（ProtocolSupport），不靠猜：某条路径探到过（非 404）才会进
// 这个清单。声明协议已经一致时返回 false——那种情况本来就该直通，不需要
// 覆盖。当前只对 api_key 型上游启用：订阅 OAuth、对端网关、专有客户端的
// “多协议”不是同一条 baseUrl 派生多条路径的形态。
func nativeProtocolForProvider(provider domain.Provider, clientProtocol domain.Protocol) (domain.Protocol, bool) {
	if clientProtocol == "" || provider.Protocol == clientProtocol {
		return "", false
	}
	// 对端网关同样适用：上游把多协议能力透传下来后，下游也应该优先按客户端
	// 协议把请求送进父网关，由父网关挑它自己那台同协议的真实上游。
	if provider.AuthType != domain.AuthTypeAPIKey && provider.AuthType != domain.AuthTypePeer {
		return "", false
	}
	if providerSupportsProtocol(provider, clientProtocol) {
		return clientProtocol, true
	}
	return "", false
}

// providerSupportsProtocol 报告这台上游能否按 protocol 直通：声明协议一致，
// 或者实测支持清单里包含它。
func providerSupportsProtocol(provider domain.Provider, protocol domain.Protocol) bool {
	if protocol == "" || provider.Protocol == protocol {
		return true
	}
	for _, supported := range provider.ProtocolSupport {
		if domain.Protocol(strings.TrimSpace(supported)) == protocol {
			return true
		}
	}
	return false
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
