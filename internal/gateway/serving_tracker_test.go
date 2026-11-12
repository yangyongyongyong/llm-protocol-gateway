package gateway

import (
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 会话粘性命中的备选在服务时，粘滞位（ActiveProviderID）不会被改写；控制台的
// 「在服务」必须跟着 tracker 走，否则日志显示备选、界面却一直标着首选。
func TestServingProviderBeatsStaleActiveProvider(t *testing.T) {
	t.Parallel()
	tracker := newServingProviderTracker()
	key := domain.APIKey{ID: "k1", ActiveProviderID: ""}

	if got := (&Server{servingProviders: tracker}).servingProviderFor(key); got != "" {
		t.Fatalf("no traffic yet: serving = %q, want empty", got)
	}
	// 第一次响应立即生效：刚发完请求就去控制台看，应该已经是备选。
	tracker.note("k1", "backup-7", 0)
	if got := tracker.serving("k1"); got != "backup-7" {
		t.Fatalf("first answer: serving = %q, want backup-7", got)
	}
	stale := domain.APIKey{ID: "k1", ActiveProviderID: ""}
	if got := (&Server{servingProviders: tracker}).servingProviderFor(stale); got != "backup-7" {
		t.Fatalf("serving should win over sticky slot: %q", got)
	}
}

func TestServingProviderDebouncesFlapping(t *testing.T) {
	t.Parallel()
	tracker := newServingProviderTracker()
	tracker.note("k1", "p1", 0)
	// 两个会话交替服务：只影响"最近一个"的口径，窗口内的节点都要记下来。
	tracker.note("k1", "p2", 200*time.Millisecond)
	if got := tracker.serving("k1"); got != "p2" {
		t.Fatalf("latest answer = %q, want p2", got)
	}
	ids := tracker.servingIDs("k1")
	if len(ids) != 2 {
		t.Fatalf("serving ids = %v, want both p1 and p2 in the window", ids)
	}
	// 稳定切到 p3 后才落定。
	tracker.note("k1", "p3", 50*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	if got := tracker.serving("k1"); got != "p3" {
		t.Fatalf("settled move: %q, want p3", got)
	}
}

// 会话分散下三台同时服务是常态：窗口内答过请求的每一台都要报出来，否则控制台
// 只标一台，另外两台显示成「待命」，看不出分散生效。
func TestServingProviderReportsEveryActiveNode(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tracker := newServingProviderTracker()
	tracker.now = func() time.Time { return now }
	tracker.note("k1", "p1", 0)
	tracker.note("k1", "p2", 0)
	tracker.note("k1", "p3", 0)
	ids := tracker.servingIDs("k1")
	if len(ids) != 3 {
		t.Fatalf("serving ids = %v, want all three", ids)
	}
	// 超出窗口的节点不再算「在服务」。
	now = now.Add(servingWindow + time.Minute)
	if ids := tracker.servingIDs("k1"); len(ids) != 0 {
		t.Fatalf("stale ids = %v, want none outside the window", ids)
	}
}

func TestServingProviderFallsBackToActiveProvider(t *testing.T) {
	t.Parallel()
	server := &Server{servingProviders: newServingProviderTracker()}
	key := domain.APIKey{ID: "k2", ActiveProviderID: "backup-2"}
	if got := server.servingProviderFor(key); got != "backup-2" {
		t.Fatalf("restart fallback: %q, want backup-2", got)
	}
}
