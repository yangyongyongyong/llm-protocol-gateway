package gateway

import (
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// servingProviderTracker remembers, per API key, which provider last answered a
// request for it.
//
// Why this exists next to domain.APIKey.ActiveProviderID: that field means
// "where this key's sticky position is" and is written on every successful
// turn — except when a session pin chose the provider, because several
// conversations on one key would otherwise rewrite it on every turn and make
// the key flap. The console reads the same field to draw the green "在服务"
// dot, so a key whose traffic is pinned to a fallback kept showing the
// preferred provider as serving. The tracker is the runtime truth the console
// needs: the last provider that actually returned a response.
//
// Writes are debounced per key: a fast stream of successes on different
// accounts (round-robin, pinned conversations interleaved) only moves the
// marker once it has stayed put for servingProviderSettleDelay, so the dot
// does not strobe between accounts mid-flight. The value lives in memory on
// purpose — after a restart the first request repopulates it, and a stale
// marker must never outlive the process.
const servingProviderSettleDelay = 3 * time.Second

// servingWindow 是一个节点被判为「在服务」的活跃窗口。
//
// 会话分散（spread）下三条会话分别落在三个账号上是常态，这时三个节点都真的在
// 服务。只留"最近一个"会把另外两个显示成「待命」，等于把分散效果藏起来。所以
// 记的不是最后一个，而是窗口内所有答过请求的节点。
const servingWindow = 10 * time.Minute

type servingMark struct {
	// providerID 是最近一笔响应的节点（保留给旧字段 ServingProviderID）。
	providerID string
	// lastAnswerAt 是每个答过请求的节点最近一次答话的时刻，按它判活跃。
	lastAnswerAt map[string]time.Time
}

type servingProviderTracker struct {
	mu    sync.Mutex
	marks map[string]*servingMark
	now   func() time.Time
}

func newServingProviderTracker() *servingProviderTracker {
	return &servingProviderTracker{marks: map[string]*servingMark{}}
}

func (tracker *servingProviderTracker) nowTime() time.Time {
	if tracker != nil && tracker.now != nil {
		return tracker.now()
	}
	return time.Now()
}

// note records that providerID answered keyID. The first answer for a key is
// applied immediately (opening the page right after the first request already
// shows the right node); later changes take effect after the settle delay, so
// two interleaved conversations do not make the console dot strobe.
func (tracker *servingProviderTracker) note(keyID, providerID string, delay time.Duration) {
	keyID = strings.TrimSpace(keyID)
	providerID = strings.TrimSpace(providerID)
	if tracker == nil || keyID == "" || providerID == "" {
		return
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.nowTime()
	mark, ok := tracker.marks[keyID]
	if !ok {
		mark = &servingMark{lastAnswerAt: map[string]time.Time{}}
		tracker.marks[keyID] = mark
	}
	if mark.lastAnswerAt == nil {
		mark.lastAnswerAt = map[string]time.Time{}
	}
	mark.providerID = providerID
	mark.lastAnswerAt[providerID] = now
	// 过期条目顺手清掉：这张表按 Key 常驻，不清理会随历史节点一直长。
	for id, at := range mark.lastAnswerAt {
		if now.Sub(at) > servingWindow {
			delete(mark.lastAnswerAt, id)
		}
	}
}

// serving returns the provider the console should crown for keyID, "" when
// nothing has been recorded (no request yet since the process started).
func (tracker *servingProviderTracker) serving(keyID string) string {
	if tracker == nil {
		return ""
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if mark, ok := tracker.marks[keyID]; ok {
		return mark.providerID
	}
	return ""
}

// servingIDs 返回窗口内答过请求的全部节点，供控制台把它们都标成「在服务」。
func (tracker *servingProviderTracker) servingIDs(keyID string) []string {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	mark, ok := tracker.marks[keyID]
	if !ok || len(mark.lastAnswerAt) == 0 {
		return nil
	}
	now := tracker.nowTime()
	out := make([]string, 0, len(mark.lastAnswerAt))
	for id, at := range mark.lastAnswerAt {
		if now.Sub(at) <= servingWindow {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) noteKeyServingProvider(keyID, providerID string) {
	if s == nil || s.servingProviders == nil {
		return
	}
	s.servingProviders.note(keyID, providerID, servingProviderSettleDelay)
}

// servingProviderFor returns the provider currently answering for keyID. When
// no request has been served yet since start-up it falls back to the key's
// sticky provider (the previous meaning of the console dot), so a freshly
// restarted gateway does not blank every key.
func (s *Server) servingProviderFor(key domain.APIKey) string {
	if s != nil && s.servingProviders != nil {
		if providerID := s.servingProviders.serving(key.ID); providerID != "" {
			return providerID
		}
	}
	return strings.TrimSpace(key.ActiveProviderID)
}

// servingProvidersFor 是「在服务」的多节点形态：窗口内所有答过请求的节点。
// 没有任何记录时退回单节点口径（粘滞位），保持重启后的界面不空。
func (s *Server) servingProvidersFor(key domain.APIKey) []string {
	if s != nil && s.servingProviders != nil {
		if ids := s.servingProviders.servingIDs(key.ID); len(ids) > 0 {
			return ids
		}
	}
	if active := strings.TrimSpace(key.ActiveProviderID); active != "" {
		return []string{active}
	}
	return nil
}
