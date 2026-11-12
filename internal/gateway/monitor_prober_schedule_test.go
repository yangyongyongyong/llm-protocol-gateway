package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 事件驱动的六个条件逐条钉住：任何一条松掉，探测就会从「他明天要用时别是坏的」
// 变成「替闲置账号刷存在感」或者「对同一个坏账号连环打」。
func TestProbeGateEventDrivenConditions(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	base := probeGate{
		Enabled: true, Now: now, UsedToday: 0, MaxPerDay: 4,
		MinInterval: 6 * time.Hour, Idle: 30 * time.Minute,
		LastRealSuccess: now.Add(-2 * time.Hour),
	}
	if !base.allowEventDriven() {
		t.Fatal("an idle account with real traffic must be probed")
	}
	cases := []struct {
		name   string
		mutate func(*probeGate)
		want   bool
	}{
		{"disabled", func(g *probeGate) { g.Enabled = false }, false},
		{"daily budget spent", func(g *probeGate) { g.UsedToday = 4 }, false},
		{"min interval not elapsed", func(g *probeGate) { g.LastProbeAt = now.Add(-time.Hour) }, false},
		{"no real traffic in lookback", func(g *probeGate) { g.LastRealSuccess = time.Time{} }, false},
		{"still busy", func(g *probeGate) { g.LastRealSuccess = now.Add(-5 * time.Minute) }, false},
		{"unavailable + probed 5min ago", func(g *probeGate) {
			g.MarkedUnavailable, g.LastProbeAt = true, now.Add(-5*time.Minute)
		}, false},
		{"unavailable + probed 15min ago", func(g *probeGate) {
			g.MarkedUnavailable, g.LastProbeAt = true, now.Add(-15*time.Minute)
		}, true},
		{"unavailable + never probed", func(g *probeGate) { g.MarkedUnavailable = true }, true},
		{"unavailable must not outlive the budget", func(g *probeGate) {
			g.MarkedUnavailable, g.UsedToday = true, 4
		}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			gate := base
			item.mutate(&gate)
			if got := gate.allowEventDriven(); got != item.want {
				t.Fatalf("allowEventDriven() = %v, want %v", got, item.want)
			}
		})
	}
}

// 每日轮与事件驱动共用当日计数：早上被催过一次的账号，晚上那一轮不该再花额度。
func TestProbeGateDailySharesBudget(t *testing.T) {
	gate := probeGate{Enabled: true, UsedToday: 0, MaxPerDay: 4}
	if !gate.allowDaily() {
		t.Fatal("an account untouched today must get its daily probe")
	}
	gate.UsedToday = 1
	if gate.allowDaily() {
		t.Fatal("one probe today already satisfies the daily round")
	}
	gate.UsedToday = 4
	if gate.allowDaily() {
		t.Fatal("the daily budget caps the daily round too")
	}
	gate.UsedToday, gate.MaxPerDay = 0, 0
	if !gate.allowDaily() {
		t.Fatal("maxPerDay=0 means unlimited, not blocked")
	}
}

func TestWithinClockWindow(t *testing.T) {
	utc := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return utc.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	if withinClockWindow("04:00", "06:00", at(3, 59)) {
		t.Fatal("before the window")
	}
	if !withinClockWindow("04:00", "06:00", at(4, 0)) {
		t.Fatal("window start is inclusive")
	}
	if !withinClockWindow("04:00", "06:00", at(5, 30)) {
		t.Fatal("inside the window")
	}
	if withinClockWindow("04:00", "06:00", at(6, 0)) {
		t.Fatal("window end is exclusive")
	}
	if !withinClockWindow("23:00", "01:00", at(0, 30)) {
		t.Fatal("a window crossing midnight must still open")
	}
	if withinClockWindow("04:00", "25:00", at(5, 0)) {
		t.Fatal("a malformed clock must never open the window")
	}
}

// tick 级集成：真实 store + mock 上游。每日窗口整日开放，两个账号今天都没跑过 →
// 一个 tick 只补 probeDailyBatchPerTick 发，剩下的顺延到下一个 tick，
// 这样 20 个账号不会把一次 tick 拖成几分钟的串行外呼。
func TestRunProbeTickDailyRoundIsBatched(t *testing.T) {
	// 只数 chat case：tools case 一律 400，好让「一次上游调用 = 一个账号的一轮」
	// 仍然成立，batch 计数不会被每轮多出的那发请求打乱。
	calls := 0
	upstream := chatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"tools"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"tools not supported"}}`)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})
	db := newProberTestStore(t)
	raw, err := json.Marshal(map[string]any{
		"eventDriven": map[string]any{"enabled": false},
		"daily":       map[string]any{"enabled": true, "windowStart": "00:00", "windowEnd": "23:59"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("monitor_probe", string(raw)); err != nil {
		t.Fatal(err)
	}

	server, _ := newQuotaTestServer(t, upstream.URL,
		domain.Provider{ID: "a", Name: "A", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey,
			BaseURL: upstream.URL + "/v1", APIKeySource: "literal:sk", DefaultModel: "claude-sonnet-4-5"},
		domain.Provider{ID: "b", Name: "B", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey,
			BaseURL: upstream.URL + "/v1", APIKeySource: "literal:sk", DefaultModel: "claude-sonnet-4-5"},
		domain.Provider{ID: "c", Name: "C", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey,
			BaseURL: upstream.URL + "/v1", APIKeySource: "literal:sk", DefaultModel: "claude-sonnet-4-5"},
	)
	server.monitoringStore = db
	// 每日轮只催「有真实流量、明天要用」的账号（见 probeGate.allowEventDriven 的
	// LastRealSuccess 条件）：三个账号各给一个一分钟前的成功请求，否则它们会因为
	// 从未成功过而被判成闲置账号，这一轮一发都不发。用一个固定时间的测试存储，
	// 不依赖本机请求日志库的行为。
	server.requestLogStore = &quotaStubLogStore{query: func(query monitor.RequestLogQuery) (monitor.RequestLogPage, error) {
		if query.Status != "2xx" || query.ProviderID == "" {
			return monitor.RequestLogPage{}, nil
		}
		return monitor.RequestLogPage{Items: []monitor.RequestLog{{ProviderID: query.ProviderID, Status: 200, Time: time.Now().Add(-time.Minute)}}}, nil
	}}

	now := time.Now().UTC()
	server.runProbeTick(context.Background(), now)
	if calls != probeDailyBatchPerTick {
		t.Fatalf("first tick sent %d probes, want the batch size %d", calls, probeDailyBatchPerTick)
	}
	// 第二个 tick 补上剩下的那个账号；已经跑过的今天不再重复。
	server.runProbeTick(context.Background(), now.Add(time.Minute))
	if calls != probeDailyBatchPerTick+1 {
		t.Fatalf("second tick total = %d, want %d", calls, probeDailyBatchPerTick+1)
	}
	// 第三个 tick：三个账号今天都跑过了，必须一发不发。
	server.runProbeTick(context.Background(), now.Add(2*time.Minute))
	if calls != probeDailyBatchPerTick+1 {
		t.Fatalf("third tick total = %d, want no further probes", calls)
	}
	// 两个 case 一轮：三个账号共 6 行探测记录，但当日配额只该扣 3 次。
	for _, id := range []string{"a", "b", "c"} {
		if _, used, err := db.MonitorProbeOutcome(id, monitorProbeDay(server, now)); err != nil || used != 1 {
			t.Fatalf("provider %s used = %d err = %v, want one round", id, used, err)
		}
	}
}

// 没写配置时用的是默认值（探测全开），但一个 24h 内没有真实成功请求的账号
// 不该被催：探测的目的是「他明天要用时别是坏的」，不是替闲置账号刷存在感。
func TestRunProbeTickWithoutTrafficSendsNothing(t *testing.T) {
	calls := 0
	upstream := chatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	db := newProberTestStore(t)
	server, _ := newQuotaTestServer(t, upstream.URL)
	server.monitoringStore = db

	// 固定到一个**不在**默认每日窗口（本地 04:00–06:00）里的时刻。
	// 以前这里传 time.Now()，于是这条测试在每天 04:00–06:00 之间必红：
	// 那时每日轮是开着的，而 allowDaily 只看当日用量、不看有没有真实流量，
	// 于是一个"从没跑过"的账号会被补一发。那是每日轮本来的语义，不是这条
	// 测试要断言的东西——它要断言的是「没流量就没事件驱动探测」。
	at := time.Date(2026, 1, 15, 13, 0, 0, 0, time.UTC)
	server.runProbeTick(context.Background(), at)
	if calls != 0 {
		t.Fatalf("default config must not probe from the gateway: %d calls", calls)
	}
	if raw := db.Setting("monitor_probe"); raw != "" {
		t.Fatalf("the tick must not write a probe config: %s", raw)
	}
}
