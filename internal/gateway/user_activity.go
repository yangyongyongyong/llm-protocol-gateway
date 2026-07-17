package gateway

import (
	"context"
	"strings"
	"time"
)

// 用户活跃时间追踪：内存里保存每个用户最近一次控制台接口请求的精确时间，
// 后台周期性落库；同一用户两次落库间隔 >= userActivityPersistMinGap，
// 避免每次请求都写 SQLite。
const (
	userActivityPersistMinGap = 5 * time.Minute
	userActivityFlushInterval = time.Minute
)

type userActivityEntry struct {
	current   time.Time // 内存精确值（每次请求更新）
	persisted time.Time // 最近一次写入 DB 的值
}

// noteUserActivity records "this user's browser just hit a console API" in
// memory. Cheap and lock-scoped; never touches the database directly.
func (s *Server) noteUserActivity(userID string) {
	userID = strings.TrimSpace(userID)
	if userID == "" || userID == legacyAdminUserID {
		return
	}
	now := time.Now()
	s.userActivityMu.Lock()
	if s.userActivity == nil {
		s.userActivity = make(map[string]*userActivityEntry)
	}
	entry, ok := s.userActivity[userID]
	if !ok {
		entry = &userActivityEntry{}
		s.userActivity[userID] = entry
	}
	entry.current = now
	s.userActivityMu.Unlock()
}

// userLastActive returns the accurate in-memory last-active time (zero when
// this process has not seen the user yet).
func (s *Server) userLastActive(userID string) time.Time {
	s.userActivityMu.Lock()
	defer s.userActivityMu.Unlock()
	if entry, ok := s.userActivity[userID]; ok {
		return entry.current
	}
	return time.Time{}
}

// StartUserActivityFlush periodically persists in-memory last-active times.
// A user's row is written when it has never been persisted by this process,
// or when the accurate value moved forward by at least
// userActivityPersistMinGap since the previous write.
func (s *Server) StartUserActivityFlush(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(userActivityFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flushUserActivity(true)
				return
			case <-ticker.C:
				s.flushUserActivity(false)
			}
		}
	}()
}

func (s *Server) flushUserActivity(force bool) {
	if s.userStore == nil {
		return
	}
	type writeItem struct {
		userID string
		at     time.Time
	}
	var writes []writeItem
	s.userActivityMu.Lock()
	for userID, entry := range s.userActivity {
		if entry.current.IsZero() || entry.current.Equal(entry.persisted) {
			continue
		}
		if force || entry.persisted.IsZero() || entry.current.Sub(entry.persisted) >= userActivityPersistMinGap {
			writes = append(writes, writeItem{userID: userID, at: entry.current})
			entry.persisted = entry.current
		}
	}
	s.userActivityMu.Unlock()
	for _, item := range writes {
		_ = s.userStore.TouchUserActive(item.userID, item.at.Format(time.RFC3339))
	}
}
