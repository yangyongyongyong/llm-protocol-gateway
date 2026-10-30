# 监控中心阶段 1（后端基础）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改前端的前提下，交付一套可独立运行的监控后端：新增监控事件与状态表、60 秒采集器、16 条规则、飞书/钉钉/企业微信/Telegram 四渠道推送、旧数据迁移，以及 `/__monitor/*` 只读与配置 API。

> 历史执行记录：文中部署命令与 `ssh root@$VPS_HOST` 均为当时的原始记录。`deploy/deploy-linux.sh` 现在要求显式传入 `SERVER_HOST`（不再内置默认主机），照抄旧命令会因缺少该变量而中止。

**Architecture:** 新建 `internal/monitoring` 包承载纯类型、采集、规则引擎、调度与渠道适配，不依赖 `gateway` 或 `store`；`internal/store` 实现持久化接口；`internal/gateway` 实现数据源（复用现有 router/quota/usage/请求日志）并暴露 HTTP API；`internal/app/runtime.go` 负责启动。所有上游交互留在 gateway 侧，规则引擎只消费快照。

**Tech Stack:** Go 1.26（标准库 `net/http`、`database/sql`、`log/slog`）、SQLite（modernc.org/sqlite）、React 19（阶段 2 才动前端）。

**Spec:** `docs/superpowers/specs/2026-10-23-monitoring-center-design.md`

---

## 阶段边界

**本阶段交付（spec §17.1 第 1 步）**

- 表结构：`monitor_events`、`monitor_alert_states`、`monitor_event_pushes`、`checkin_history`、`provider_metric_samples`、`monitor_baselines`，以及 `request_logs` 新索引。
- 采集器：60 秒 tick，5/10/15 分钟窗口聚合，额度 15 分钟、模型列表 15 分钟、TTFT 基线每小时。
- 16 条规则：`oauth_expiring`、`oauth_refresh_failed`、`auth_invalid`、`quota_low`、`credit_expiring`、`key_budget_warn`、`key_budget_exhausted`、`provider_down`、`server_error_spike`、`rate_limit_spike`、`empty_response`、`ttft_regression`、`model_unavailable`、`multi_ip`、`concurrent_ip`、`collector_stale`。
- 四渠道即时推送、静默时段、冷却、抖动抑制、失败重试、投递记录。
- 旧数据迁移：`checkins → checkin_history`、`alerts → monitor_events`、旧 Telegram 设置 → 渠道。
- `/__monitor/*` 阶段 1 子集（spec §17.1）：`overview`、`accounts` 列表/详情/立即续期/额度刷新、`events`、`alerts` 列表/详情/ack/ignore/重试推送、`channels` CRUD/test、`dispatch` GET/PUT。
- 旧告警扫描继续写旧表，但 Telegram 推送交给新调度器。

**本阶段不做**

- 前端页面、导航替换（阶段 2）。
- `probe_failed`、`checkin_failed` 规则与 Prober（阶段 3）；`probes*`、`accounts/{id}/probe` 端点同阶段 3。
- P1 摘要、P2 事件流之外的推送策略（阶段 4；阶段 1 的 P1 即时发送）。
- `rules*` 编辑端点（阶段 4）；阶段 1 只内置默认参数，Service 仍从 `monitor_rules` 读取以支持运维改库。
- 渠道级令牌桶限流（spec §10.4；阶段 1 记录为已知缺口，P0 突发可能触发平台限流，按平台错误重试）。

---

## 执行约束

1. **不要新建 worktree。** 当前工作区可能混有用户未提交的前端改动；所有改动直接在当前分支进行，只 `git add` 本计划涉及的文件。是否提交按用户当轮授权执行；未授权时跳过 commit 步骤，仅保留工作区改动。
2. **真实 Claude 凭证只允许在 VPS 上验证。** 本机验收只用 mock 上游和假凭证；禁止从本机调用 Anthropic。
3. **不打印、不返回、不落库任何 token / API Key / webhook 明文。** 渠道 secret 只写不读；事件 `detail_json` 只放统计值和错误码。
4. **探测流量不花订阅额度。** 阶段 1 不新增真实上游探测，全部用请求日志、已有 usage 接口和单元测试。
5. **部署检查点：** Task 10、12、14 结束时各部署一次 VPS 并验收；纯内部任务（1–9、11、13）本地通过后即进入下一个任务，避免部署未接线的代码。
6. 每个任务结束前执行：`gofmt -w <改动文件>`、`go vet ./...`、`go test ./internal/monitoring/... ./internal/store/... ./internal/gateway/...`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/monitoring/types.go` | Severity、Event、AlertState、Snapshot、Observation、KeyObservation、Channel、Notification 等纯类型；窗口统计与派生方法 |
| `internal/monitoring/config.go` | 规则定义表、默认配置、JSON 配置读写、参数校验 |
| `internal/monitoring/store.go` | 采集器/引擎/调度器依赖的窄接口 |
| `internal/monitoring/collector.go` | 从 `Source` 构造快照、窗口聚合、额度/模型/基线调度、心跳 |
| `internal/monitoring/engine.go` | 16 条规则的求值、pending/firing/resolved 状态机、unavailable、冷却与抖动抑制 |
| `internal/monitoring/dispatcher.go` | 分级投递、静默时段、渠道过滤、重试、投递记录 |
| `internal/monitoring/channel.go` | `ChannelAdapter` 接口、工厂、共享 HTTP 发送与错误脱敏 |
| `internal/monitoring/channels.go` | 飞书、钉钉、企业微信、Telegram 的 payload 与签名 |
| `internal/monitoring/service.go` | 生命周期、goroutine、tick 编排、`RecordRenewal` 入口 |
| `internal/store/monitoring_schema.go` | 建表/索引 SQL 与 `ensureMonitoringSchema(tx)` |
| `internal/store/monitoring.go` | 事件/状态/投递/样本/基线/签到的 CRUD 与聚合查询 |
| `internal/store/monitoring_migration.go` | `monitor_migration_v1` 迁移与保留期清理 |
| `internal/gateway/monitoring_source.go` | `monitoring.Source` 实现：provider 快照、额度抓取、窗口、密钥额度、风险检测、模型失败计数 |
| `internal/gateway/monitoring_renewal.go` | 续期结果记录与最近事件查询 |
| `internal/gateway/monitoring_handlers.go` | `/__monitor/*` HTTP 层 |
| `internal/gateway/monitoring_runtime.go` | 组装 service、启动/停止、旧告警推送切换 |
| 修改 `internal/store/sqlite.go` | `migrate()` 末尾调用 `ensureMonitoringSchema(tx)` |
| 修改 `internal/gateway/server.go` | 注入 `MonitoringStore`、注册路由、启动方法 |
| 修改 `internal/app/runtime.go` | 启动 `StartMonitoring` |
| 修改 7 个文件的 8 处续期调用点 | 记录续期结果（见 Task 11 的调用点表） |

---

## Task 1: 监控领域类型与快照契约

**Files:**
- Create: `internal/monitoring/types.go`
- Test: `internal/monitoring/types_test.go`

- [ ] **Step 1: 写失败测试**

```go
package monitoring

import (
	"testing"
	"time"
)

func TestSeverityMeets(t *testing.T) {
	cases := []struct {
		have, min Severity
		want      bool
	}{
		{SeverityP0, SeverityP0, true},
		{SeverityP0, SeverityP1, true},
		{SeverityP1, SeverityP0, false},
		{SeverityP2, SeverityP2, true},
		{SeverityInfo, SeverityP2, false},
	}
	for _, tc := range cases {
		if got := tc.have.Meets(tc.min); got != tc.want {
			t.Fatalf("%s meets %s = %v, want %v", tc.have, tc.min, got, tc.want)
		}
	}
}

func TestWindowStatsRatios(t *testing.T) {
	w := WindowStats{Total: 100, Success: 80, Auth401403: 10, RateLimited: 5, ServerError: 5, Empty: 3}
	if got := w.SuccessRatio(); got != 0.8 {
		t.Fatalf("success ratio = %v", got)
	}
	if got := w.ErrorRatio(); got != 0.05 {
		t.Fatalf("error ratio = %v", got)
	}
	if got := w.AuthRatio(); got != 0.1 {
		t.Fatalf("auth ratio = %v", got)
	}
	if (WindowStats{}).SuccessRatio() != 0 {
		t.Fatal("empty window must report 0, callers gate on Total")
	}
}

func TestQuotaRatioUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	q := QuotaSnapshot{Remaining: -1, Limit: 0, Ratio: -1, ObservedAt: now}
	if q.Available() {
		t.Fatal("negative ratio must be unavailable")
	}
	q = QuotaSnapshot{Remaining: 5, Limit: 100, Ratio: 0.05, ObservedAt: now}
	if !q.Available() {
		t.Fatal("positive limit must be available")
	}
	if q.IsStale(now.Add(29*time.Minute), 30*time.Minute) {
		t.Fatal("fresh snapshot must not be stale")
	}
	if !q.IsStale(now.Add(31*time.Minute), 30*time.Minute) {
		t.Fatal("snapshot older than max age must be stale")
	}
	if !(QuotaSnapshot{}).IsStale(now, 30*time.Minute) {
		t.Fatal("zero ObservedAt must be stale")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/monitoring/ -run 'TestSeverity|TestWindow|TestQuota' -v`
Expected: FAIL，`undefined: Severity`。

- [ ] **Step 3: 实现类型**

```go
package monitoring

import "time"

type Severity string

const (
	SeverityP0   Severity = "p0"
	SeverityP1   Severity = "p1"
	SeverityP2   Severity = "p2"
	SeverityInfo Severity = "info"
)

func (s Severity) Rank() int {
	switch s {
	case SeverityP0:
		return 3
	case SeverityP1:
		return 2
	case SeverityP2:
		return 1
	default:
		return 0
	}
}

func (s Severity) Meets(min Severity) bool { return s.Rank() >= min.Rank() }

func SeverityFromLegacy(raw string) Severity {
	switch raw {
	case "error", "critical":
		return SeverityP0
	case "warn":
		return SeverityP1
	default:
		return SeverityP1
	}
}

const (
	EventKindAlert   = "alert"
	EventKindRenewal = "renewal"
	EventKindProbe   = "probe"
	EventKindCheckin = "checkin"
	EventKindQuota   = "quota"
	EventKindSystem  = "system"

	StatusFiring   = "firing"
	StatusResolved = "resolved"
	StatusInfo     = "info"

	DispositionOpen         = "open"
	DispositionAcknowledged = "acknowledged"
	DispositionIgnored      = "ignored"

	ResolutionRecovered       = "recovered"
	ResolutionDataUnavailable = "data_unavailable"
	ResolutionLegacy          = "legacy"

	PushPending    = "pending"
	PushSent       = "sent"
	PushPartial    = "partial"
	PushFailed     = "failed"
	PushSuppressed = "suppressed"
	PushSkipped    = "skipped"

	TargetProvider = "provider"
	TargetAPIKey   = "api_key"
	TargetModel    = "model"
	TargetGateway  = "gateway"
)

type Event struct {
	ID               int64          `json:"id"`
	Time             time.Time      `json:"time"`
	Kind             string         `json:"kind"`
	Severity         Severity       `json:"severity"`
	RuleKey          string         `json:"ruleKey,omitempty"`
	TargetType       string         `json:"targetType"`
	TargetID         string         `json:"targetId,omitempty"`
	TargetName       string         `json:"targetName,omitempty"`
	Title            string         `json:"title"`
	Detail           map[string]any `json:"detail"`
	Fingerprint      string         `json:"fingerprint,omitempty"`
	Status           string         `json:"status"`
	Count            int            `json:"count"`
	FirstSeen        time.Time      `json:"firstSeen"`
	LastSeen         time.Time      `json:"lastSeen"`
	ResolvedAt       time.Time      `json:"resolvedAt,omitempty"`
	ResolutionReason string         `json:"resolutionReason,omitempty"`
	PushStatus       string         `json:"pushStatus,omitempty"`
	PushError        string         `json:"pushError,omitempty"`
}

type AlertState struct {
	ID               int64          `json:"id"`
	Fingerprint      string         `json:"fingerprint"`
	RuleKey          string         `json:"ruleKey"`
	Severity         Severity       `json:"severity"`
	TargetType       string         `json:"targetType"`
	TargetID         string         `json:"targetId"`
	TargetName       string         `json:"targetName"`
	Status           string         `json:"status"`
	Disposition      string         `json:"disposition"`
	Count            int            `json:"count"`
	FirstSeen        time.Time      `json:"firstSeen"`
	LastSeen         time.Time      `json:"lastSeen"`
	LastNotifiedAt   time.Time      `json:"lastNotifiedAt,omitempty"`
	AcknowledgedAt   time.Time      `json:"acknowledgedAt,omitempty"`
	IgnoredAt        time.Time      `json:"ignoredAt,omitempty"`
	SuppressedUntil  time.Time      `json:"suppressedUntil,omitempty"`
	UnavailableSince time.Time      `json:"unavailableSince,omitempty"`
	ResolutionReason string         `json:"resolutionReason,omitempty"`
	Detail           map[string]any `json:"detail"`
}

// AlertView 是告警列表/详情返回给前端的形状：在状态行上补最近事件与推送状态。
type AlertView struct {
	AlertState
	LatestEventID   int64   `json:"latestEventId,omitempty"`
	PushStatus      string  `json:"pushStatus,omitempty"`
	PushError       string  `json:"pushError,omitempty"`
	RelatedEventIDs []int64 `json:"relatedEventIds"`
}

// RequestSummary 是 overview 的 24h 汇总，过滤口径与 WindowStatsByProvider 相同。
type RequestSummary struct {
	Total        int   `json:"total"`
	Success      int   `json:"success"`
	P95LatencyMs int64 `json:"p95LatencyMs"`
	CostMicros   int64 `json:"costMicros"`
}

type EventPush struct {
	EventID    int64
	ChannelID  string
	Status     string
	Attempts   int
	LastError  string
	UpdatedAt  time.Time
}

// TimelineEntry 追加到监听周期最新事件的 detail_json.timeline。
// Action 为 acknowledged / ignored / resolved 等，Time 为 UTC。
type TimelineEntry struct {
	Action string    `json:"action"`
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor,omitempty"`
}

type Channel struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Config      ChannelConfig  `json:"config"`
	MinSeverity Severity       `json:"minSeverity"`
	Enabled     bool           `json:"enabled"`
}

type ChannelConfig struct {
	Webhook  string `json:"webhook,omitempty"`
	Secret   string `json:"secret,omitempty"`
	BotToken string `json:"botToken,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
}

type Notification struct {
	Fingerprint string    `json:"fingerprint"`
	Severity    Severity  `json:"severity"`
	Title       string    `json:"title"`
	Target      string    `json:"target"`
	Markdown    string    `json:"markdown"`
	URL         string    `json:"url,omitempty"`
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"` // firing | resolved | digest | test
}

type WindowStats struct {
	Total       int   `json:"total"`
	Success     int   `json:"success"`
	Auth401403  int   `json:"auth401403"`
	RateLimited int   `json:"rateLimited"`
	ServerError int   `json:"serverError"`
	Pseudo200   int   `json:"pseudo200"`
	Empty       int   `json:"empty"`
	TTFTp50     int64 `json:"ttftP50"`
	TTFTp95     int64 `json:"ttftP95"`
	LatencyP95  int64 `json:"latencyP95"`
	CostMicros  int64 `json:"costMicros"`
}

func (w WindowStats) SuccessRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.Success) / float64(w.Total)
}

func (w WindowStats) ErrorRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.ServerError) / float64(w.Total)
}

func (w WindowStats) AuthRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.Auth401403) / float64(w.Total)
}

func (w WindowStats) RateLimitRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.RateLimited) / float64(w.Total)
}

func (w WindowStats) EmptyRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.Empty) / float64(w.Total)
}

type Capabilities struct {
	Quota   bool `json:"quota"`
	Checkin bool `json:"checkin"`
	Models  bool `json:"models"`
	OAuth   bool `json:"oauth"`
}

type QuotaSnapshot struct {
	Source     string    `json:"source"`
	Remaining  float64   `json:"remaining"`
	Limit      float64   `json:"limit"`
	Ratio      float64   `json:"ratio"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	Stale      bool      `json:"stale"`
	Error      string    `json:"error,omitempty"`
}

func (q QuotaSnapshot) Available() bool { return q.Limit > 0 && q.Ratio >= 0 }

func (q QuotaSnapshot) IsStale(now time.Time, maxAge time.Duration) bool {
	return q.ObservedAt.IsZero() || now.Sub(q.ObservedAt) > maxAge
}

type RenewalOutcome struct {
	Known       bool
	LastAt      time.Time
	LastOK      bool
	Consecutive int
	LastError   string
}

// RenewalRecord 是一次续期尝试的埋点输入（Task 11 在调用点构造）。
// Err 只用于本地日志与 classifyRenewalError，绝不进入事件 detail_json。
type RenewalRecord struct {
	ProviderID string
	Vendor     string
	StartedAt  time.Time
	Duration   time.Duration
	Err        error
	ExpiresAt  time.Time
}

type ProviderObservation struct {
	ID             string
	Name           string
	Vendor         string
	AuthType       string
	ExpiresAt      time.Time
	HealthStatus   string
	Capabilities   Capabilities
	Quota          *QuotaSnapshot
	Renewal        RenewalOutcome
	ModelFailures  int
	BaselineTTFTP95 int64
}

type KeyObservation struct {
	ID             string
	Name           string
	QuotaWindow    string
	TokenLimit     int64
	TokenUsed      int64
	CostLimitMicros int64
	CostUsedMicros int64
	MultiIPs       []string
	MultiIPCount   int
	ConcurrentIPs  int
	ConcurrentAt   time.Time
}

type Snapshot struct {
	Now                 time.Time
	Providers           []ProviderObservation
	Keys                []KeyObservation
	Windows             map[string]map[int]WindowStats
	CollectorLastSuccess time.Time
	DataErrors          []string
}

type EventQuery struct {
	Kind       string
	Severity   string
	TargetType string
	TargetID   string
	Status     string
	From       time.Time
	To         time.Time
	Page       int
	PageSize   int
}

type EventPage struct {
	Items    []Event `json:"items"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}

type AlertQuery struct {
	Status      string
	Disposition string
	Severity    string
	Rule        string
	TargetType  string
	TargetID    string
	From        time.Time
	To          time.Time
	Page        int
	PageSize    int
}

type AlertPage struct {
	Items    []AlertView `json:"items"`
	Total    int         `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
}

// MetricSample maps one provider_metric_samples row. Nil means "unknown",
// which is different from a zero quota and must never be rendered as 0.
type MetricSample struct {
	ProviderID      string
	Hour            string
	QuotaRatio      *float64
	Credits         *float64
	CreditValidUntil string
	AuthExpiresAt   string
	HealthStatus    string
	SourceError     string
	ObservedAt      time.Time
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/monitoring/ -v`
Expected: PASS。

---

## Task 2: 规则定义、默认配置与校验

**Files:**
- Create: `internal/monitoring/config.go`
- Test: `internal/monitoring/config_test.go`

- [ ] **Step 1: 写失败测试**

```go
package monitoring

import "testing"

func TestDefaultRulesCoverPhaseOne(t *testing.T) {
	rules := DefaultRules()
	if len(rules) != 16 {
		t.Fatalf("phase 1 rules = %d, want 16", len(rules))
	}
	byKey := map[string]RuleConfig{}
	for _, r := range rules {
		byKey[r.Key] = r
	}
	for _, key := range []string{"oauth_expiring", "auth_invalid", "key_budget_exhausted", "multi_ip", "collector_stale"} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("missing rule %s", key)
		}
	}
	if byKey["oauth_expiring"].Severity != SeverityP0 {
		t.Fatal("oauth_expiring must default to p0")
	}
}

func TestRuleValidationRejectsWindowAndCooldown(t *testing.T) {
	rules := DefaultRules()
	if err := ValidateRules(rules); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	// 每个子用例重新取 DefaultRules：RuleConfig 里的 Params 是 map，
	// append 浅拷贝会共享 map，前一个子用例的改动会污染后一个。
	bad := DefaultRules()
	for i := range bad {
		if bad[i].Key == "auth_invalid" {
			bad[i].Params["windowMinutes"] = 7
		}
	}
	if err := ValidateRules(bad); err == nil {
		t.Fatal("windowMinutes=7 must be rejected")
	}
	bad = DefaultRules()
	for i := range bad {
		if bad[i].Key == "provider_down" {
			bad[i].CooldownMinutes = 1
		}
	}
	if err := ValidateRules(bad); err == nil {
		t.Fatal("cooldown=1 must be rejected")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestDefaultRules -v`
Expected: FAIL，`undefined: DefaultRules`。

- [ ] **Step 3: 实现配置**

```go
package monitoring

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type RuleConfig struct {
	Key             string             `json:"key"`
	Enabled         bool               `json:"enabled"`
	Severity        Severity           `json:"severity"`
	Params          map[string]float64 `json:"params"`
	CooldownMinutes int                `json:"cooldownMinutes"`
}

type RuleDef struct {
	Key           string             `json:"key"`
	Name          string             `json:"name"`
	TargetType    string             `json:"targetType"`
	DefaultParams map[string]float64 `json:"defaultParams"`
	Interval      time.Duration      `json:"-"` // 阶段 4 的 rules API 用 intervalMinutes 输出，不直接序列化纳秒
	Consecutive   bool               `json:"consecutive"` // true: Params["consecutiveFailures"] / consecutiveDays instead of a duration
}

var ruleDefs = []RuleDef{
	{Key: "oauth_expiring", Name: "凭证即将过期", TargetType: TargetProvider, DefaultParams: map[string]float64{"leadMinutes": 120}, Interval: time.Minute},
	{Key: "oauth_refresh_failed", Name: "凭证连续续期失败", TargetType: TargetProvider, DefaultParams: map[string]float64{"consecutiveFailures": 2}, Interval: time.Minute, Consecutive: true},
	{Key: "auth_invalid", Name: "凭证失效", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 5, "minCount": 3, "minRatio": 0.5, "minSamples": 10}, Interval: time.Minute},
	{Key: "quota_low", Name: "额度不足", TargetType: TargetProvider, DefaultParams: map[string]float64{"maxRatio": 0.1}, Interval: 15 * time.Minute},
	{Key: "credit_expiring", Name: "积分即将到期", TargetType: TargetProvider, DefaultParams: map[string]float64{"leadHours": 72}, Interval: 24 * time.Hour},
	{Key: "key_budget_warn", Name: "密钥额度预警", TargetType: TargetAPIKey, DefaultParams: map[string]float64{"warnRatio": 0.8}, Interval: 5 * time.Minute},
	{Key: "key_budget_exhausted", Name: "密钥额度耗尽", TargetType: TargetAPIKey, DefaultParams: map[string]float64{"criticalRatio": 1.0}, Interval: 5 * time.Minute},
	{Key: "provider_down", Name: "Provider 成功率过低", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 5, "minSamples": 20, "maxSuccessRatio": 0.8}, Interval: time.Minute},
	{Key: "server_error_spike", Name: "上游 5xx 激增", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 5, "minSamples": 20, "maxErrorRatio": 0.1}, Interval: time.Minute},
	{Key: "rate_limit_spike", Name: "上游限流激增", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 5, "minSamples": 20, "maxRatio": 0.2, "maxCount": 10}, Interval: time.Minute},
	{Key: "empty_response", Name: "空响应", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 10, "minSamples": 10, "maxRatio": 0.05}, Interval: time.Minute},
	{Key: "ttft_regression", Name: "首包延迟回退", TargetType: TargetProvider, DefaultParams: map[string]float64{"windowMinutes": 15, "minSamples": 20, "floorMs": 5000, "factor": 2}, Interval: time.Minute},
	{Key: "model_unavailable", Name: "模型列表拉取失败", TargetType: TargetProvider, DefaultParams: map[string]float64{"consecutiveFailures": 3}, Interval: 15 * time.Minute, Consecutive: true},
	{Key: "multi_ip", Name: "API Key 多 IP 使用", TargetType: TargetAPIKey, DefaultParams: map[string]float64{"windowMinutes": 10, "minIPs": 5}, Interval: time.Minute},
	{Key: "concurrent_ip", Name: "API Key 并发 IP", TargetType: TargetAPIKey, DefaultParams: map[string]float64{"minIPs": 4}, Interval: time.Minute},
	{Key: "collector_stale", Name: "监控采集器停滞", TargetType: TargetGateway, DefaultParams: map[string]float64{"maxAgeMinutes": 5}, Interval: time.Minute},
}

// 阶段 3 才启用的规则，阶段 1 只登记定义，不进入默认配置。
var deferredRuleDefs = []RuleDef{
	{Key: "probe_failed", Name: "轻探测失败", TargetType: TargetProvider, DefaultParams: map[string]float64{"consecutiveFailures": 2}, Interval: time.Minute, Consecutive: true},
	{Key: "checkin_failed", Name: "签到连续失败", TargetType: TargetProvider, DefaultParams: map[string]float64{"consecutiveDays": 2}, Interval: 24 * time.Hour, Consecutive: true},
}

func RuleDefinition(key string) (RuleDef, bool) {
	for _, def := range ruleDefs {
		if def.Key == key {
			return def, true
		}
	}
	for _, def := range deferredRuleDefs {
		if def.Key == key {
			return def, true
		}
	}
	return RuleDef{}, false
}

func DefaultRules() []RuleConfig {
	out := make([]RuleConfig, 0, len(ruleDefs))
	for _, def := range ruleDefs {
		out = append(out, RuleConfig{
			Key:             def.Key,
			Enabled:         true,
			Severity:        defaultSeverity(def.Key),
			Params:          cloneParams(def.DefaultParams),
			CooldownMinutes: 60,
		})
	}
	return out
}

func defaultSeverity(key string) Severity {
	switch key {
	case "oauth_expiring", "oauth_refresh_failed", "auth_invalid", "multi_ip", "concurrent_ip":
		return SeverityP0
	case "key_budget_warn", "credit_expiring", "ttft_regression", "model_unavailable", "collector_stale":
		return SeverityP2
	default:
		return SeverityP1
	}
}

func cloneParams(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ValidateRules(rules []RuleConfig) error {
	seen := map[string]bool{}
	for _, rule := range rules {
		def, ok := RuleDefinition(rule.Key)
		if !ok {
			return fmt.Errorf("unknown rule %q", rule.Key)
		}
		if seen[rule.Key] {
			return fmt.Errorf("duplicate rule %q", rule.Key)
		}
		seen[rule.Key] = true
		if rule.Severity != SeverityP0 && rule.Severity != SeverityP1 && rule.Severity != SeverityP2 {
			return fmt.Errorf("rule %s: severity must be p0/p1/p2", rule.Key)
		}
		if rule.CooldownMinutes < 5 || rule.CooldownMinutes > 1440 {
			return fmt.Errorf("rule %s: cooldownMinutes must be 5..1440", rule.Key)
		}
		for key, value := range rule.Params {
			if _, known := def.DefaultParams[key]; !known {
				return fmt.Errorf("rule %s: unknown param %s", rule.Key, key)
			}
			if key == "windowMinutes" && value != 5 && value != 10 && value != 15 {
				return fmt.Errorf("rule %s: windowMinutes must be 5/10/15", rule.Key)
			}
			if value <= 0 {
				return fmt.Errorf("rule %s: param %s must be > 0", rule.Key, key)
			}
			if (key == "minSamples") && (value < 1 || value > 10000) {
				return fmt.Errorf("rule %s: minSamples must be 1..10000", rule.Key)
			}
			if (key == "minRatio" || key == "maxRatio" || key == "maxSuccessRatio" ||
				key == "maxErrorRatio" || key == "warnRatio" || key == "criticalRatio") && value > 1 {
				return fmt.Errorf("rule %s: ratio %s must be <= 1", rule.Key, key)
			}
		}
	}
	return nil
}

func NormalizeRules(stored []RuleConfig) []RuleConfig {
	byKey := make(map[string]RuleConfig, len(stored))
	for _, r := range stored {
		byKey[r.Key] = r
	}
	out := DefaultRules()
	for i := range out {
		stored, ok := byKey[out[i].Key]
		if !ok {
			continue
		}
		out[i].Enabled = stored.Enabled
		if stored.Severity == SeverityP0 || stored.Severity == SeverityP1 || stored.Severity == SeverityP2 {
			out[i].Severity = stored.Severity
		}
		if stored.CooldownMinutes >= 5 && stored.CooldownMinutes <= 1440 {
			out[i].CooldownMinutes = stored.CooldownMinutes
		}
		for key, value := range stored.Params {
			if _, known := out[i].Params[key]; known && value > 0 {
				out[i].Params[key] = value
			}
		}
	}
	return out
}

func MarshalRules(rules []RuleConfig) (string, error) {
	b, err := json.Marshal(rules)
	return string(b), err
}

func UnmarshalRules(raw string) []RuleConfig {
	if raw == "" {
		return DefaultRules()
	}
	var stored []RuleConfig
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return DefaultRules()
	}
	return NormalizeRules(stored)
}

func SortedRuleKeys(rules []RuleConfig) []string {
	keys := make([]string, 0, len(rules))
	for _, r := range rules {
		keys = append(keys, r.Key)
	}
	sort.Strings(keys)
	return keys
}
```

同时实现 `DispatchConfig`、`ProbeConfig`（阶段 1 仅存储）、`MigrationMarker`：

```go
type DispatchConfig struct {
	DigestMinutes  int    `json:"digestMinutes"`
	QuietEnabled   bool   `json:"quietEnabled"`
	QuietStart     string `json:"quietStart"`
	QuietEnd       string `json:"quietEnd"`
	RecoveryNotify bool   `json:"recoveryNotify"`
	Timezone       string `json:"timezone"`
	BaseURL        string `json:"baseUrl"`
}

func DefaultDispatchConfig() DispatchConfig {
	return DispatchConfig{
		DigestMinutes: 15, QuietEnabled: true, QuietStart: "23:00", QuietEnd: "08:00",
		RecoveryNotify: true, Timezone: "Asia/Shanghai",
	}
}

func (c DispatchConfig) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	return loc
}

type ProbeConfig struct {
	EventDriven struct {
		Enabled              bool `json:"enabled"`
		IdleMinutes          int  `json:"idleMinutes"`
		MaxPerDayPerAccount  int  `json:"maxPerDayPerAccount"`
		MinIntervalMinutes   int  `json:"minIntervalMinutes"`
	} `json:"eventDriven"`
	Daily struct {
		Enabled     bool   `json:"enabled"`
		WindowStart string `json:"windowStart"`
		WindowEnd   string `json:"windowEnd"`
	} `json:"daily"`
	WeeklyCLI struct {
		Enabled  bool   `json:"enabled"`
		Weekday  int    `json:"weekday"`
		Time     string `json:"time"`
	} `json:"weeklyCli"`
	QuotaStaleMinutes int `json:"quotaStaleMinutes"`
}

func DefaultProbeConfig() ProbeConfig {
	cfg := ProbeConfig{QuotaStaleMinutes: 30}
	cfg.EventDriven.Enabled = true
	cfg.EventDriven.IdleMinutes = 30
	cfg.EventDriven.MaxPerDayPerAccount = 4
	cfg.EventDriven.MinIntervalMinutes = 360
	cfg.Daily.Enabled = true
	cfg.Daily.WindowStart = "04:00"
	cfg.Daily.WindowEnd = "06:00"
	cfg.WeeklyCLI.Enabled = true
	cfg.WeeklyCLI.Weekday = 0
	cfg.WeeklyCLI.Time = "05:00"
	return cfg
}

type MigrationMarker struct {
	Checkins         bool  `json:"checkins"`
	Alerts           bool  `json:"alerts"`
	Telegram         bool  `json:"telegram"`
	LegacyMaxAlertID int64 `json:"legacyMaxAlertId"`
}

// Settings is the slice of the store the monitoring config needs. Both the
// store and test fakes implement it.
type Settings interface {
	Setting(key string) string
	SetSetting(key, value string) error
}

type Config struct {
	settings Settings
}

func NewConfig(settings Settings) *Config { return &Config{settings: settings} }

func (c *Config) Rules() []RuleConfig { return UnmarshalRules(c.settings.Setting("monitor_rules")) }

func (c *Config) SaveRules(rules []RuleConfig) error {
	if err := ValidateRules(rules); err != nil {
		return err
	}
	raw, err := MarshalRules(rules)
	if err != nil {
		return err
	}
	return c.settings.SetSetting("monitor_rules", raw)
}

func (c *Config) Dispatch() DispatchConfig {
	cfg := DefaultDispatchConfig()
	if raw := c.settings.Setting("monitor_dispatch"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return DefaultDispatchConfig()
		}
	}
	if cfg.DigestMinutes <= 0 {
		cfg.DigestMinutes = 15
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	return cfg
}

func (c *Config) SaveDispatch(cfg DispatchConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return c.settings.SetSetting("monitor_dispatch", raw)
}

func (c *Config) Channels() []Channel {
	raw := c.settings.Setting("monitor_channels")
	if raw == "" {
		return nil
	}
	var channels []Channel
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		return nil
	}
	return channels
}

func (c *Config) SaveChannels(channels []Channel) error {
	raw, err := json.Marshal(channels)
	if err != nil {
		return err
	}
	return c.settings.SetSetting("monitor_channels", raw)
}

func (c *Config) Probe() ProbeConfig {
	cfg := DefaultProbeConfig()
	if raw := c.settings.Setting("monitor_probe"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return DefaultProbeConfig()
		}
	}
	return cfg
}

func (c *Config) SaveProbe(cfg ProbeConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return c.settings.SetSetting("monitor_probe", raw)
}

func (c *Config) MigrationMarker() MigrationMarker {
	var marker MigrationMarker
	if raw := c.settings.Setting("monitor_migration_v1"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &marker)
	}
	return marker
}

func (c *Config) SaveMigrationMarker(marker MigrationMarker) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return c.settings.SetSetting("monitor_migration_v1", raw)
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -run 'TestDefaultRules|TestRuleValidation' -v`
Expected: PASS。

---

## Task 3: 表结构与 store 持久化

**Files:**
- Create: `internal/store/monitoring_schema.go`
- Create: `internal/store/monitoring.go`
- Modify: `internal/store/sqlite.go:114-365`（`migrate()` 末尾、`schemaVersion`）
- Test: `internal/store/monitoring_test.go`

- [ ] **Step 1: 写失败测试**

```go
package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitoring"
)

func openMonitoringTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMonitorStateRoundTrip(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	state := monitoring.AlertState{
		Fingerprint: "provider_down|provider:p1", RuleKey: "provider_down",
		Severity: monitoring.SeverityP1, TargetType: "provider", TargetID: "p1",
		TargetName: "P1", Status: monitoring.StatusFiring, Disposition: monitoring.DispositionOpen,
		Count: 1, FirstSeen: now, LastSeen: now, Detail: map[string]any{"successRatio": 0.5},
	}
	saved, err := s.UpsertMonitorState(state)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 {
		t.Fatal("expected id")
	}
	states, err := s.LoadMonitorStates()
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	if states[0].Detail["successRatio"] != 0.5 {
		t.Fatalf("detail lost: %#v", states[0].Detail)
	}
}

func TestMonitorEventsQueryAndPush(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	id, err := s.InsertMonitorEvent(monitoring.Event{
		Time: now, Kind: monitoring.EventKindAlert, Severity: monitoring.SeverityP0,
		RuleKey: "auth_invalid", TargetType: "provider", TargetID: "p1", Title: "t",
		Detail: map[string]any{"status": 401}, Fingerprint: "auth_invalid|provider:p1",
		Status: monitoring.StatusFiring, Count: 1, FirstSeen: now, LastSeen: now,
	})
	if err != nil || id == 0 {
		t.Fatalf("insert event id=%d err=%v", id, err)
	}
	if err := s.UpdateMonitorEventPush(id, monitoring.PushSent, ""); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryMonitorEvents(monitoring.EventQuery{Page: 1, PageSize: 50})
	if err != nil || page.Total != 1 || page.Items[0].PushStatus != monitoring.PushSent {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	push := monitoring.EventPush{EventID: id, ChannelID: "ch1", Status: monitoring.PushSent, Attempts: 1, UpdatedAt: now}
	if err := s.UpsertMonitorEventPush(push); err != nil {
		t.Fatal(err)
	}
	pushes, err := s.ListMonitorEventPushes(id)
	if err != nil || len(pushes) != 1 || pushes[0].ChannelID != "ch1" {
		t.Fatalf("pushes=%+v err=%v", pushes, err)
	}
}

func TestProviderWindowStats(t *testing.T) {
	s := openMonitoringTestStore(t)
	now := time.Now().UTC().Truncate(time.Minute)
	for i, status := range []int{200, 200, 401, 429, 500} {
		if err := s.InsertRequestLogRowForTest(now.Add(-time.Duration(i)*time.Minute), "p1", "convert", status, 10, 100, 200); err != nil {
			t.Fatal(err)
		}
	}
	// 6 分钟前的行落在 5 分钟窗口之外，只能计入 10/15 分钟窗口。
	if err := s.InsertRequestLogRowForTest(now.Add(-6*time.Minute), "p1", "convert", 200, 10, 100, 200); err != nil {
		t.Fatal(err)
	}
	windows, err := s.WindowStatsByProvider(now.Add(-15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if windows["p1"][5].Total != 5 {
		t.Fatalf("5m total = %d", windows["p1"][5].Total)
	}
	if windows["p1"][10].Total != 6 {
		t.Fatalf("10m total = %d", windows["p1"][10].Total)
	}
	if windows["p1"][5].Auth401403 != 1 || windows["p1"][5].RateLimited != 1 || windows["p1"][5].ServerError != 1 {
		t.Fatalf("window stats = %+v", windows["p1"][5])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run 'TestMonitor|TestProviderWindow' -v`
Expected: FAIL，缺少方法和表。

- [ ] **Step 3: 实现 schema 与方法**

`monitoring_schema.go` 直接复制 spec §6 的建表 SQL，并追加：

```go
func ensureMonitoringSchema(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS monitor_events (...)`,
		`CREATE TABLE IF NOT EXISTS monitor_alert_states (...)`,
		`CREATE TABLE IF NOT EXISTS monitor_event_pushes (...)`,
		`CREATE TABLE IF NOT EXISTS checkin_history (...)`,
		`CREATE TABLE IF NOT EXISTS provider_metric_samples (...)`,
		`CREATE TABLE IF NOT EXISTS monitor_baselines (...)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_events_time ON monitor_events(time DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_events_fp ON monitor_events(fingerprint, status)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_events_target ON monitor_events(target_type, target_id, time DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_states_status ON monitor_alert_states(status, severity)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_provider_time ON request_logs(provider_id, time DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("monitor schema: %w", err)
		}
	}
	return nil
}
```

在 `sqlite.go` 的 `migrate()` 里、schema version 写入之前调用：

```go
if err := ensureMonitoringSchema(tx); err != nil {
	return err
}
```

`monitoring.go` 的方法清单与关键 SQL：

```go
func (s *Store) LoadMonitorStates() ([]monitoring.AlertState, error)
func (s *Store) UpsertMonitorState(state monitoring.AlertState) (monitoring.AlertState, error)
// INSERT ... ON CONFLICT(fingerprint) DO UPDATE SET severity/status/disposition/count/
// first_seen/target_name/last_seen/last_notified_at/acknowledged_at/ignored_at/
// suppressed_until/unavailable_since/resolution_reason/detail_json
// RETURNING id
// first_seen 必须在更新列里：新一次 firing 周期（resolved 后重新触发）要重置为本次时间，
// 否则恢复时长和“5 分钟内恢复不发通知”的判断会跨周期累计。
func (s *Store) MonitorAlertByID(id int64) (monitoring.AlertState, bool, error)
func (s *Store) SetMonitorDisposition(id int64, disposition string, at time.Time) error
func (s *Store) AppendMonitorTimeline(fingerprint string, entry monitoring.TimelineEntry) error
func (s *Store) InsertMonitorEvent(event monitoring.Event) (int64, error)
func (s *Store) MonitorEventByID(id int64) (monitoring.Event, bool, error)
func (s *Store) LatestMonitorEventID(fingerprint string) (int64, error)
func (s *Store) MonitorEventIDsByFingerprint(fingerprint string, limit int) ([]int64, error)
func (s *Store) UpdateMonitorEventPush(eventID int64, status, message string) error
func (s *Store) QueryMonitorEvents(q monitoring.EventQuery) (monitoring.EventPage, error)
func (s *Store) QueryMonitorAlerts(q monitoring.AlertQuery) (monitoring.AlertPage, error) // Items 为 AlertView
func (s *Store) UpsertMonitorEventPush(push monitoring.EventPush) error
func (s *Store) ListMonitorEventPushes(eventID int64) ([]monitoring.EventPush, error)
func (s *Store) SettingsJSON(keys ...string) (map[string]string, error) // 一次读 4 个配置键
func (s *Store) UpsertProviderMetricSample(sample monitoring.MetricSample) error
func (s *Store) LatestProviderMetricSamples() (map[string]monitoring.MetricSample, error)
func (s *Store) RecomputeTTFTBaselines(now time.Time, loc *time.Location) error
func (s *Store) TTFTBaselines(loc *time.Location) (map[string]int64, error) // map[providerID]value for current hour
func (s *Store) WindowStatsByProvider(from, to time.Time) (map[string]map[int]monitoring.WindowStats, error)
func (s *Store) MonitorRequestSummary(from, to time.Time) (monitoring.RequestSummary, error)
func (s *Store) SaveCheckinHistory(record domain.CheckinRecord) error
func (s *Store) ListCheckinHistory(providerID string, days int) ([]domain.CheckinRecord, error)
func (s *Store) RecentRenewalOutcomes(providerID string, limit int) (monitoring.RenewalOutcome, error)
```

`WindowStatsByProvider` 一次读取 `from..to` 的原始行，在 Go 里分 5/10/15 分钟桶：

```sql
SELECT provider_id, time, status, protocol_flow, output_tokens, ttft_ms, latency_ms, cost_micros, error_description
FROM request_logs
WHERE time >= ? AND time <= ? AND action IN ('convert', 'pass_through')
  AND status <> 499
  AND COALESCE(protocol_flow, '') <> 'embeddings'
  AND COALESCE(access_source, '') <> 'monitor_probe'
```

过滤原因（已核对 `internal/gateway/router.go:1999-2005` 与 `internal/gateway/server.go`）：
- `action` 实际只会是 `convert` / `pass_through`（路由决定）或 `rejected` / `compact` / `test_*`（网关拒绝、压缩、测试），后三类不是正常上游流量。
- `499` 是客户端主动断开（`statusClientClosedRequest`），不是上游故障；计入会拉低成功率并制造假告警。
- `protocol_flow='embeddings'` 的响应没有 output tokens，计入 Empty 会误报 `empty_response`。

分桶规则：对每个 provider，计算 `age = now - row.Time`，落入 `age <= 5m`、`<=10m`、`<=15m` 三个窗口；每行在所有满足条件的桶里各计一次。状态映射：

- `200..299` 且 `error_description == ''` → Success；其中 `output_tokens == 0` 同时计入 Empty。
- `200..299` 且 `error_description != ''` → Pseudo200，不计 Success（spec §7.1 的伪 200）。
- `401/403` → Auth401403。
- `429` → RateLimited。
- `>=500`，或 `status == 0` 且 `error_description` 含 `timeout`/`deadline` → ServerError。
- 所有未被上面排除的行都计入 Total；`action='rejected'` 等网关自身拒绝不算 provider 失败。

P50/P95 只在有 TTFT 样本时计算；`CostMicros` 求和。

`MonitorRequestSummary` 用与上面完全相同的 WHERE 过滤，只 `SELECT status, error_description, latency_ms, cost_micros`，在 Go 里汇总 `Total`、`Success`（2xx 且 `error_description==''`）、P95 `latency_ms`、`CostMicros`；overview 传 `now-24h..now`。

`QueryMonitorAlerts` 的每条 `AlertView` 由一条查询带出（避免 handler 里 N+1）：

```sql
SELECT st.*,
  (SELECT e.id          FROM monitor_events e WHERE e.fingerprint = st.fingerprint ORDER BY e.id DESC LIMIT 1) AS latest_event_id,
  (SELECT e.push_status FROM monitor_events e WHERE e.fingerprint = st.fingerprint ORDER BY e.id DESC LIMIT 1) AS push_status,
  (SELECT e.push_error  FROM monitor_events e WHERE e.fingerprint = st.fingerprint ORDER BY e.id DESC LIMIT 1) AS push_error
FROM monitor_alert_states st
WHERE ... ORDER BY st.last_seen DESC LIMIT ? OFFSET ?
```

`relatedEventIds` 对当前页的 fingerprint 集合再查一次（窗口函数按 fingerprint 各取最近 10 条）：

```sql
SELECT fingerprint, id FROM (
  SELECT fingerprint, id, ROW_NUMBER() OVER (PARTITION BY fingerprint ORDER BY id DESC) AS rn
  FROM monitor_events WHERE fingerprint IN (/* page fingerprints */)
) WHERE rn <= 10 ORDER BY id DESC
```

`MonitorEventIDsByFingerprint(fp, 10)` 就是上面查询的单个 fingerprint 版本，供告警详情 handler 使用；`pushStatus/pushError` 取该周期**最近一条事件**的 `monitor_events.push_status/push_error` 列（Dispatcher 已在每次投递后聚合写回），不再单独聚合 `monitor_event_pushes`。

测试辅助 `InsertRequestLogRowForTest(...)` 放在 `monitoring_test.go`，直接 `INSERT INTO request_logs (...)` 最小列集（`time/provider_id/action/status/protocol_flow/output_tokens/ttft_ms/latency_ms`），`protocol_flow` 固定写 `''`，需要 embeddings 场景时用单独 helper 或直接 UPDATE。

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/store/ -run 'TestMonitor|TestProviderWindow' -v`
Expected: PASS。

---

## Task 4: 旧数据迁移与保留期清理

**Files:**
- Create: `internal/store/monitoring_migration.go`
- Test: `internal/store/monitoring_migration_test.go`

- [ ] **Step 1: 写失败测试**

覆盖四个场景：

```go
func TestMigrationImportsCheckinsAndAlertsIdempotently(t *testing.T)
func TestMigrationCreatesLegacyTelegramChannelOnce(t *testing.T)
func TestMigrationImportsAlertsIncrementally(t *testing.T)
func TestMonitorRetentionDeletesOldRows(t *testing.T)
```

关键断言：

- 连续调用 `MigrateMonitoringV1()` 两次，`checkin_history` 行数不翻倍。
- 旧 `alerts` 中 `apikey_multi_ip` 映射为 `multi_ip`，`severity=warn → p1`，导入事件 `status=resolved`、`resolution_reason=legacy`、不生成 `monitor_alert_states`。
- 新增旧告警后再调用一次，只有新 id 被导入，`legacyMaxAlertId` 前移。
- 旧 `alerts` settings 含 Telegram token 时，`monitor_channels` 设置里只出现一个 `legacy-telegram`（解析 JSON 断言）。
- 插入 91 天前的事件/样本/签到历史，调用 `PruneMonitoringData(now)` 后旧行删除、边界内保留。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run 'TestMigration|TestMonitorRetention' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

迁移标记结构（与 spec §6.7 一致）：

```go
type migrationMarker struct {
	Checkins          bool `json:"checkins"`
	Alerts            bool `json:"alerts"`
	Telegram          bool `json:"telegram"`
	LegacyMaxAlertID  int64 `json:"legacyMaxAlertId"`
}
```

`MigrateMonitoringV1()` 流程：

1. `SELECT value FROM settings WHERE key='monitor_migration_v1'`，解析失败按零值。
2. `!marker.Checkins`：`INSERT OR IGNORE INTO checkin_history SELECT provider_id, day, at, outcome, credit, streak, msg, valid_until, offline, tries FROM checkins WHERE day != ''`，成功后置位并保存标记。
3. `!marker.Telegram`：读 `alerts` 设置 JSON（`domain.AlertSettings`）；有 token 和 chatId 时，用 `monitoring.NewConfig(s)` 读取现有 `monitor_channels`，若不存在固定 id `legacy-telegram` 就追加一个 `monitoring.Channel`（`type=telegram`、`minSeverity=p1`、`enabled=旧 Enabled`、`config={botToken,chatId}`），再 `SaveChannels` 写回；置位保存。渠道是 settings JSON，不是表。
4. 告警增量导入**每次启动都执行**：`SELECT ... FROM alerts WHERE id > marker.LegacyMaxAlertID ORDER BY id`，逐行映射（规则/级别/标题/detail/push_status），用 `monitor_events` 里 `json_extract(detail_json, '$.legacyAlertId')` 查重后插入。
5. 更新 `legacyMaxAlertId` 为本次最大 id，保存。

为避免给事件表加业务唯一列，导入前先查：

```sql
SELECT COUNT(1) FROM monitor_events
WHERE kind='alert' AND target_type='api_key'
  AND json_extract(detail_json, '$.legacyAlertId') = ?
```

modernc.org/sqlite v1.53.0 内置 SQLite 3.53，JSON1 可用；测试直接覆盖 `json_extract`，不做 LIKE 后备。

`detail_json` 必须含 `legacyAlertId`、`ips`、`ipCount`、`windowMinutes`、`requestCount`、`concurrentAt`、`legacyStatus`、`legacyRule`（仅未知规则）。

同时修改 `internal/store/checkin.go` 的 `SaveCheckin`：在同一事务里除 `checkins` 外执行 `INSERT OR REPLACE INTO checkin_history (...)`，确保迁移后新增签到也进入历史，避免 `checkin_failed`（阶段 3）出现数据缺口。`SaveCheckin` 目前直接 `s.db.Exec`，改为先 `Begin`，两条语句提交。

`PruneMonitoringData(now time.Time) error`：

```sql
DELETE FROM monitor_events WHERE time < ?             -- 90 天
DELETE FROM monitor_event_pushes WHERE event_id NOT IN (SELECT id FROM monitor_events);
DELETE FROM provider_metric_samples WHERE hour < ?    -- 30 天
DELETE FROM checkin_history WHERE day < ?             -- 90 天（day 为 YYYY-MM-DD）
DELETE FROM monitor_alert_states
WHERE status='resolved' AND last_seen < ?             -- 90 天
  AND id NOT IN (SELECT id FROM monitor_alert_states WHERE status='resolved' ORDER BY last_seen DESC LIMIT 1000);
```

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/store/ -run 'TestMigration|TestMonitorRetention' -v`
Expected: PASS。

---

## Task 5: 采集器、窗口与基线

**Files:**
- Create: `internal/monitoring/store.go`
- Create: `internal/monitoring/collector.go`
- Test: `internal/monitoring/collector_test.go`

- [ ] **Step 1: 写失败测试**

```go
type fakeSource struct {
	providers   []ProviderObservation
	keys        []KeyObservation
	windows     map[string]map[int]WindowStats
	baselines   map[string]int64
	quota       map[string]*QuotaSnapshot
	quotaCalls  int
	multiIP     []MultiIPHit
	concurrent  []ConcurrentIPHit
}

func (f *fakeSource) Providers(ctx context.Context, now time.Time) ([]ProviderObservation, error) {
	return f.providers, nil
}
func (f *fakeSource) Keys(ctx context.Context, now time.Time) ([]KeyObservation, error) {
	return f.keys, nil
}
func (f *fakeSource) Windows(ctx context.Context, now time.Time) (map[string]map[int]WindowStats, error) {
	return f.windows, nil
}
func (f *fakeSource) Baselines(ctx context.Context, now time.Time) (map[string]int64, error) {
	return f.baselines, nil
}
func (f *fakeSource) Quota(ctx context.Context, p ProviderObservation) (*QuotaSnapshot, error) {
	f.quotaCalls++
	return f.quota[p.ID], nil
}
func (f *fakeSource) MultiIP(ctx context.Context, now time.Time) ([]MultiIPHit, error) {
	return f.multiIP, nil
}
func (f *fakeSource) ConcurrentIP(ctx context.Context, now time.Time) ([]ConcurrentIPHit, error) {
	return f.concurrent, nil
}
func (f *fakeSource) LastRenewal(ctx context.Context, providerID string) (RenewalOutcome, error) {
	return RenewalOutcome{}, nil
}
func (f *fakeSource) ModelFailures(ctx context.Context, providerID string) (int, error) {
	return 0, nil
}

func TestCollectorBuildsSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	src := &fakeSource{
		providers: []ProviderObservation{{ID: "p1", Name: "P1", ExpiresAt: now.Add(time.Hour)}},
		keys:      []KeyObservation{{ID: "k1", MultiIPs: []string{"1.1.1.1", "2.2.2.2"}, MultiIPCount: 2}},
		windows:   map[string]map[int]WindowStats{"p1": {5: {Total: 10, Success: 9}}},
	}
	c := NewCollector(src, nil)
	snap, err := c.Snapshot(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Now != now {
		t.Fatal("snapshot time not set")
	}
	if snap.Windows["p1"][5].Success != 9 {
		t.Fatal("windows not merged")
	}
	if snap.Keys[0].MultiIPCount != 2 {
		t.Fatal("keys not merged")
	}
	if snap.CollectorLastSuccess.IsZero() {
		t.Fatal("collector heartbeat missing")
	}
}

func TestCollectorKeepsQuotaCacheUntilRefresh(t *testing.T) {
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	src := &fakeSource{
		providers: []ProviderObservation{{ID: "p1", Vendor: "claude", Capabilities: Capabilities{Quota: true}}},
		quota:     map[string]*QuotaSnapshot{"p1": {Remaining: 5, Limit: 100, Ratio: 0.05, ObservedAt: now}},
	}
	c := NewCollector(src, nil)
	for _, at := range []time.Time{now, now.Add(5 * time.Minute)} {
		snap, err := c.Snapshot(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Providers[0].Quota == nil || snap.Providers[0].Quota.Remaining != 5 {
			t.Fatalf("quota not attached at %s", at)
		}
	}
	if src.quotaCalls != 1 {
		t.Fatalf("quota fetched %d times inside cache window, want 1", src.quotaCalls)
	}
	if _, err := c.Snapshot(context.Background(), now.Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if src.quotaCalls != 2 {
		t.Fatalf("quota fetched %d times after cache expiry, want 2", src.quotaCalls)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestCollector -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

`store.go` 定义窄接口：

```go
type Source interface {
	Providers(ctx context.Context, now time.Time) ([]ProviderObservation, error)
	Keys(ctx context.Context, now time.Time) ([]KeyObservation, error)
	Windows(ctx context.Context, now time.Time) (map[string]map[int]WindowStats, error)
	Baselines(ctx context.Context, now time.Time) (map[string]int64, error)
	Quota(ctx context.Context, provider ProviderObservation) (*QuotaSnapshot, error)
	MultiIP(ctx context.Context, now time.Time) ([]MultiIPHit, error)
	ConcurrentIP(ctx context.Context, now time.Time) ([]ConcurrentIPHit, error)
	LastRenewal(ctx context.Context, providerID string) (RenewalOutcome, error)
	ModelFailures(ctx context.Context, providerID string) (int, error)
}

type StateStore interface {
	LoadMonitorStates() ([]AlertState, error)
	UpsertMonitorState(AlertState) (AlertState, error)
	InsertMonitorEvent(Event) (int64, error)
	LatestMonitorEventID(fingerprint string) (int64, error)
	UpdateMonitorEventPush(int64, string, string) error
	UpsertMonitorEventPush(EventPush) error
	ListMonitorEventPushes(int64) ([]EventPush, error)
	SetMonitorDisposition(int64, string, time.Time) error
	MonitorAlertByID(int64) (AlertState, bool, error)
}

type ConfigStore interface {
	Setting(string) string
	SetSetting(string, string) error
	InsertMonitorEvent(Event) (int64, error)
}

type MetricSink interface {
	UpsertProviderMetricSample(MetricSample) error
}

type BaselineComputer interface {
	RecomputeTTFTBaselines(now time.Time, loc *time.Location) error
}
```

`MultiIPHit` / `ConcurrentIPHit` 在 monitoring 包内声明（与 `internal/monitor/store.go` 同名字段），gateway source 负责转换：

```go
type MultiIPHit struct {
	APIKeyID     string
	APIKeyName   string
	IPs          []string
	IPCount      int
	RequestCount int
}

type ConcurrentIPHit struct {
	APIKeyID     string
	APIKeyName   string
	At           time.Time
	IPs          []string
	IPCount      int
	RequestCount int
}
```

`Collector`：

```go
type Collector struct {
	source Source
	store  StateStore
	quota  map[string]*QuotaSnapshot
	quotaAt time.Time
	modelFailures map[string]int
	modelAt time.Time
	lastSuccess time.Time
	mu sync.Mutex
	now func() time.Time
}

func NewCollector(source Source, store StateStore) *Collector
func (c *Collector) Snapshot(ctx context.Context, now time.Time) (Snapshot, error)
```

`Snapshot` 逻辑：

1. `Providers` + `Keys` + `Windows` + `Baselines` 并发不超过 2 路（避免 SQLite 读池争用），单个失败记录 `DataErrors`，其余继续。
2. 额度缓存：`now.Sub(quotaAt) >= 15m` 才逐个调用 `Quota`；失败时保留上次快照并标 `Stale=true`、`Error`，写入 `provider_metric_samples` 由 store 实现（通过可选接口 `SampleSink`，没有则跳过）。
3. 将 `Baselines`、`Renewal`、`ModelFailures` 合并进对应 `ProviderObservation`。
4. `MultiIP` / `ConcurrentIP` 合并进 `KeyObservation`（按 APIKeyID 匹配）。
5. 核心数据（`Providers` + `Windows`）任一失败时返回 snapshot + error，**不更新** `lastSuccess`；其余子源失败只写入 `DataErrors`，仍更新心跳。这样采集器卡住或核心查询连续失败时，`CollectorLastSuccess` 会超过 5 分钟，watchdog 能发现。
6. 成功后 `lastSuccess = now`，快照带 `CollectorLastSuccess`；Service 同时把心跳写到设置 `monitor_collector_last_success`（UTC RFC3339）。
7. `panic` 由 `Service` 外层 recover；Collector 内不 recover。

`Source` 的 `Providers/Keys/Windows/Baselines` 每次 tick 都调用；`Quota` 只在 15 分钟窗口调用；模型失败计数由 gateway source 自己维护（Task 10）。

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -v`
Expected: PASS。

---

## Task 6: 规则引擎核心与全部 16 条规则

**Files:**
- Create: `internal/monitoring/engine.go`
- Test: `internal/monitoring/engine_test.go`

- [ ] **Step 1: 写失败测试（表驱动）**

至少覆盖：

```go
func TestAuthInvalidNeedsSamplesAndRatio(t *testing.T)
func TestProviderDownNeedsTenMinutes(t *testing.T)
func TestRecoveryNeedsTwoCleanTicks(t *testing.T)
func TestCooldownAndFlapSuppression(t *testing.T)
func TestUnavailableGraceResolvesDataUnavailable(t *testing.T)
func TestQuotaLowIgnoresZeroLimit(t *testing.T)
func TestOAuthExpiringRequiresFailedRenewal(t *testing.T)
func TestKeyBudgetPicksTokenOrCostRatio(t *testing.T)
func TestMultiIPAndConcurrentIP(t *testing.T)
func TestCollectorStale(t *testing.T)
func TestTTFTRegressionRequiresBaseline(t *testing.T)
```

用可控时钟：

```go
type fakeStore struct {
	states map[string]AlertState
	events []Event
	nextID int64
}

func (f *fakeStore) LoadMonitorStates() ([]AlertState, error) { ... }
func (f *fakeStore) UpsertMonitorState(s AlertState) (AlertState, error) { ... }
func (f *fakeStore) InsertMonitorEvent(e Event) (int64, error) { ... }
```

示例断言：

```go
func TestProviderDownNeedsTenMinutes(t *testing.T) {
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	st := newFakeStore()
	engine := NewEngine(st, DefaultRules())
	win := map[string]map[int]WindowStats{
		"p1": {5: {Total: 20, Success: 10}},
	}
	var transitions []Transition
	for i := 0; i < 9; i++ {
		tr, err := engine.Evaluate(context.Background(), snapshotAt(now.Add(time.Duration(i)*time.Minute), win))
		if err != nil { t.Fatal(err) }
		transitions = append(transitions, tr...)
	}
	if len(transitions) != 0 {
		t.Fatalf("fired before 10 minutes: %+v", transitions)
	}
	tr, err := engine.Evaluate(context.Background(), snapshotAt(now.Add(10*time.Minute), win))
	if err != nil { t.Fatal(err) }
	if len(tr) != 1 || tr[0].State.RuleKey != "provider_down" || !tr[0].Firing {
		t.Fatalf("expected provider_down firing, got %+v", tr)
	}
}

// snapshotAt 必须把 CollectorLastSuccess 设为同一模拟时刻，否则 collector_stale
// 在 5 分钟后会额外返回一条 transition，污染断言的条数。
func snapshotAt(now time.Time, win map[string]map[int]WindowStats) Snapshot {
	return Snapshot{Now: now, Windows: win, CollectorLastSuccess: now}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestProviderDown -v`
Expected: FAIL。

- [ ] **Step 3: 实现引擎**

核心结构：

```go
type Transition struct {
	State    AlertState
	Event    Event
	Firing   bool
	Resolved bool
	Upgraded bool
	Reminder bool
	Suppressed bool
}

type pendingState struct {
	Since       time.Time
	Consecutive int
	FalseStreak int
}

type flapRecord struct {
	Times []time.Time
}

type Engine struct {
	store    StateStore
	rules    []RuleConfig
	pending  map[string]pendingState
	flaps    map[string]flapRecord
	now      func() time.Time
}

func NewEngine(store StateStore, rules []RuleConfig) *Engine
func (e *Engine) Evaluate(ctx context.Context, snap Snapshot) ([]Transition, error)
func (e *Engine) SetRules(rules []RuleConfig) // 配置热更新
```

`Evaluate` 流程：

1. `states, _ := store.LoadMonitorStates()`，建 `map[fingerprint]AlertState`。
2. 对每个 provider / key / gateway 生成 `[]evaluation`（规则 + 目标 + 条件 + 可用性 + 级别 + 标题 + detail）。
3. 对每个 evaluation 调用 `apply`，得到状态与事件。
4. 清理已禁用规则的 pending。

`evaluation`：

```go
type evaluation struct {
	RuleKey      string
	TargetType   string
	TargetID     string
	TargetName   string
	Fingerprint  string
	Available    bool
	Hit          bool
	Severity     Severity
	Title        string
	Detail       map[string]any
	Params       map[string]float64
}
```

fingerprint 固定为 `ruleKey + "|" + targetType + ":" + targetID`。

规则条件实现（全部在 `evaluateAll` 中）：

| 规则 | 条件 |
|---|---|
| `oauth_expiring` | `!ExpiresAt.IsZero()` 且 `ExpiresAt.Sub(now) < lead` 且 `Renewal.Known && !Renewal.LastOK` |
| `oauth_refresh_failed` | `Renewal.Consecutive >= consecutiveFailures` |
| `auth_invalid` | 5m 窗口 `Total >= minSamples && Auth401403 >= minCount && AuthRatio >= minRatio` |
| `quota_low` | `Quota != nil && Quota.Available()` 且 `Quota.Ratio < maxRatio`；`Limit==0` 或 `Ratio<0` → unavailable |
| `credit_expiring` | `Quota != nil && Quota.Remaining > 0` 且 `ExpiresAt` 在 lead 内；不要求 `Limit > 0`（积分接口常无总额）。`ExpiresAt` 为零时用签到 `validUntil`（Source 填） |
| `key_budget_warn` | 取 `tokenRatio`（TokenLimit>0）与 `costRatio`（CostLimit>0）最大值，`>= warnRatio` |
| `key_budget_exhausted` | 同上，`>= criticalRatio` |
| `provider_down` | 5m 窗口 `Total >= minSamples && SuccessRatio < maxSuccessRatio` |
| `server_error_spike` | 5m 窗口 `Total >= minSamples && ErrorRatio > maxErrorRatio` |
| `rate_limit_spike` | 5m 窗口 `Total >= minSamples && (RateLimitRatio > maxRatio || RateLimited >= maxCount)` |
| `empty_response` | 10m 窗口 `Total >= minSamples && EmptyRatio > maxRatio` |
| `ttft_regression` | 15m 窗口 `Total >= minSamples && BaselineTTFTP95 > 0 && TTFTp95 > max(floorMs, factor*Baseline)` |
| `model_unavailable` | `ModelFailures >= consecutiveFailures` |
| `multi_ip` | `Key.MultiIPCount >= minIPs`，detail 带 IP 列表（最多 12 个） |
| `concurrent_ip` | `Key.ConcurrentIPs >= minIPs`，detail 带 `concurrentAt` |
| `collector_stale` | `snap.CollectorLastSuccess.IsZero()` 或 `now.Sub(last) > maxAge` |

**可用性判定（每条规则必须显式设置 `Available`）：**

- 窗口类规则：`windows[provider][windowMinutes]` 不存在或 `Total < minSamples` → `Available=false`；样本不足不是“条件为假”，不能借此让 firing 状态走恢复分支。
- `auth_invalid`：同样要求 `Total >= minSamples`。
- `quota_low`：`Quota == nil`、`Limit <= 0` 或 `Ratio < 0` → `Available=false`。
- `credit_expiring`：`Quota == nil`、`Remaining <= 0` 或 `ExpiresAt` 为零 → `Available=false`；不要求 `Limit > 0`（积分接口通常没有总额，`quota_low` 不适用但到期提醒仍应生效）。
- `oauth_expiring` / `oauth_refresh_failed`：凭证无到期时间或 `Renewal.Known=false` → `Available=false`。
- `ttft_regression`：`BaselineTTFTP95 <= 0` 或样本不足 → `Available=false`。
- `key_budget_*`：`TokenLimit<=0 && CostLimitMicros<=0` → `Available=false`。
- `multi_ip` / `concurrent_ip`：`Key` 不存在或检测查询失败 → `Available=false`。
- `collector_stale`：始终 `Available=true`。

`forMinutes` 不是每条规则的参数；从规则定义派生：

```go
func sustainedFor(ruleKey string) time.Duration {
	switch ruleKey {
	case "auth_invalid": return 3 * time.Minute
	case "provider_down": return 10 * time.Minute
	case "server_error_spike", "rate_limit_spike": return 5 * time.Minute
	case "empty_response": return 10 * time.Minute
	case "ttft_regression": return 15 * time.Minute
	case "collector_stale": return 5 * time.Minute
	case "key_budget_warn", "key_budget_exhausted": return 5 * time.Minute
	default: return 0
	}
}
```

`apply` 状态机：

```go
func (e *Engine) apply(ev evaluation, current AlertState, exists bool, now time.Time) *Transition {
	if !ev.Available {
		e.pending[ev.Fingerprint] = pendingState{} // 不累计
		if exists && current.Status == StatusFiring {
			if current.UnavailableSince.IsZero() {
				current.UnavailableSince = now
				current.Detail = ev.Detail
				e.store.UpsertMonitorState(current)
			} else if now.Sub(current.UnavailableSince) > graceFor(ev.RuleKey) {
				current.Status = StatusResolved
				current.ResolutionReason = ResolutionDataUnavailable
				current.LastSeen = now
				saved, _ := e.store.UpsertMonitorState(current)
				event := e.newEvent(saved, now, StatusResolved)
				event.ResolutionReason = ResolutionDataUnavailable
				id, _ := e.store.InsertMonitorEvent(event)
				event.ID = id
				return &Transition{State: saved, Event: event, Resolved: true}
			}
		}
		return nil
	}

	if exists && current.Status == StatusFiring {
		if ev.Hit {
			current.LastSeen = now
			current.Count++
			current.UnavailableSince = time.Time{}
			current.Detail = ev.Detail
			// 条件重新为真：恢复计数归零，避免“真-假-真-假”被算成连续 2 次为假。
			delete(e.pending, ev.Fingerprint)
			if ev.Severity.Rank() > current.Severity.Rank() {
				current.Severity = ev.Severity
				saved, _ := e.store.UpsertMonitorState(current)
				return &Transition{State: saved, Event: e.newEvent(saved, now, StatusFiring), Firing: true, Upgraded: true}
			}
			saved, _ := e.store.UpsertMonitorState(current)
			return nil // 持续 firing 不产生新事件
		}
		p := e.pending[ev.Fingerprint]
		p.FalseStreak++
		e.pending[ev.Fingerprint] = p
		if p.FalseStreak < 2 {
			return nil
		}
		delete(e.pending, ev.Fingerprint)
		current.Status = StatusResolved
		current.Disposition = DispositionOpen
		current.ResolutionReason = ResolutionRecovered
		current.LastSeen = now
		saved, _ := e.store.UpsertMonitorState(current)
		event := e.newEvent(saved, now, StatusResolved)
		event.ResolutionReason = ResolutionRecovered
		id, _ := e.store.InsertMonitorEvent(event)
		event.ID = id
		saved = e.recordFlap(saved, now)
		return &Transition{State: saved, Event: event, Resolved: true}
	}

	// inactive / resolved
	if !ev.Hit {
		delete(e.pending, ev.Fingerprint)
		return nil
	}
	p := e.pending[ev.Fingerprint]
	if p.Since.IsZero() {
		p.Since = now
	}
	p.Consecutive++
	e.pending[ev.Fingerprint] = p
	if !e.ready(ev, p, now) {
		return nil
	}
	delete(e.pending, ev.Fingerprint)
	state := AlertState{
		Fingerprint: ev.Fingerprint, RuleKey: ev.RuleKey, Severity: ev.Severity,
		TargetType: ev.TargetType, TargetID: ev.TargetID, TargetName: ev.TargetName,
		Status: StatusFiring, Disposition: DispositionOpen, Count: 1,
		FirstSeen: now, LastSeen: now, Detail: ev.Detail,
	}
	saved, err := e.store.UpsertMonitorState(state)
	if err != nil { return nil }
	event := e.newEvent(saved, now, StatusFiring)
	event.Title = ev.Title
	event.Detail = ev.Detail
	id, _ := e.store.InsertMonitorEvent(event)
	event.ID = id
	saved = e.recordFlap(saved, now)
	return &Transition{State: saved, Event: event, Firing: true}
}
```

`ready`：

```go
func (e *Engine) ready(ev evaluation, p pendingState, now time.Time) bool {
	def, _ := RuleDefinition(ev.RuleKey)
	if def.Consecutive {
		want := int(ev.Params["consecutiveFailures"])
		if ev.RuleKey == "checkin_failed" {
			want = int(ev.Params["consecutiveDays"])
		}
		return p.Consecutive >= want
	}
	return now.Sub(p.Since) >= sustainedFor(ev.RuleKey)
}
```

冷却不在这里判断，交给 Dispatcher；但 `recordFlap` 抖动检测在这里：

```go
func (e *Engine) recordFlap(state AlertState, now time.Time) AlertState {
	rec := e.flaps[state.Fingerprint]
	cutoff := now.Add(-15 * time.Minute)
	kept := rec.Times[:0]
	for _, t := range rec.Times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	rec.Times = append(kept, now)
	e.flaps[state.Fingerprint] = rec
	if len(rec.Times) >= 3 {
		state.SuppressedUntil = now.Add(time.Hour)
		if saved, err := e.store.UpsertMonitorState(state); err == nil {
			state = saved
		}
	}
	return state
}
```

阶段 1 简化：`recordFlap` 在 firing 与 recovered resolved 两处各记一次（data_unavailable 的 resolved 不计翻转，避免数据抖动与真实抖动混淆），使 15 分钟内 3 次翻转成立。`recordFlap` 接收刚保存的 `saved AlertState`，直接在过渡里设置 `saved.SuppressedUntil` 并再次 upsert。

`unavailable` 宽限：

```go
func graceFor(ruleKey string) time.Duration {
	def, _ := RuleDefinition(ruleKey)
	grace := 2 * def.Interval
	if grace < 15*time.Minute { grace = 15 * time.Minute }
	return grace
}
```

指标聚合规则每 60 秒评估，所以请求类宽限为 15 分钟（spec 下限）；额度规则 30 分钟；签到（阶段 3）48 小时。

**持续提醒与采集器看门狗**

- firing 期间每个 tick 在更新 `last_seen` 之后，若 `Disposition==open`、`SuppressedUntil` 已过、且 `now.Sub(LastNotifiedAt) >= cooldown`，产出 `Transition{Reminder:true}`；`Event` 复用 `LatestMonitorEventID(state.Fingerprint)` 查到的事件（事件不新增），Dispatcher 只补发通知并更新 `last_notified_at`。
- 新增 `func (e *Engine) EvaluateCollectorHealth(ctx context.Context, now, lastSuccess time.Time) ([]Transition, error)`：只评估 `collector_stale` 一条规则，供 Service 的独立 watchdog goroutine 调用。**不能**用普通 `Evaluate`，否则采集失败时 provider 为空会把所有 firing 状态误判为恢复。
- `StateStore` 增加 `LatestMonitorEventID(fingerprint string) (int64, error)`；Task 3 的 store 方法已列出。

**标题与 detail** 统一在 `evaluation` 构造时生成，示例：

```go
Title: fmt.Sprintf("%s 成功率跌破 %.0f%%", p.Name, maxSuccess*100),
Detail: map[string]any{"successRatio": stats.SuccessRatio(), "total": stats.Total, "windowMinutes": 5},
```

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -run Test -v`
Expected: PASS。若某条规则测试失败，先修规则，不要放宽断言。

---

## Task 7: 渠道适配与签名

**Files:**
- Create: `internal/monitoring/channel.go`
- Create: `internal/monitoring/channels.go`
- Test: `internal/monitoring/channels_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestFeishuSignAndPayload(t *testing.T)
func TestDingTalkSignInQuery(t *testing.T)
func TestWeComMarkdown(t *testing.T)
func TestTelegramPayloadAndTokenScrub(t *testing.T)
```

用 `httptest.NewServer` 捕获请求，断言：

- 飞书：body 含 `timestamp`、`sign`，`sign == base64(HMAC-SHA256(key = ts+"\n"+secret, data = ""))`；`msg_type=interactive`；卡片标题含 `[P0]`。
- 钉钉：query 含毫秒 `timestamp` 和 `sign`，签名原文为 `timestamp+"\n"+secret`；body `msgtype=actionCard`。
- 企微：body `msgtype=markdown`，标题以 `[P0]` 开头；URL 不落日志。
- Telegram：token 在 URL；上游返回 500 时错误信息不含 token。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestFeishu -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

```go
type ChannelAdapter interface {
	Send(ctx context.Context, ch Channel, n Notification) error
}

func AdapterFor(channelType string) (ChannelAdapter, bool) {
	switch channelType {
	case "feishu": return feishuAdapter{}, true
	case "dingtalk": return dingtalkAdapter{}, true
	case "wecom": return wecomAdapter{}, true
	case "telegram": return telegramAdapter{}, true
	default: return nil, false
	}
}
```

共享发送：

```go
var channelHTTPClient = &http.Client{Timeout: 15 * time.Second}

func postJSON(ctx context.Context, url string, payload any) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := channelHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, limited, nil
}
```

**注意：** `monitoring` 包不能引用 gateway 的内部函数（`guardUpstreamIdleClient` 等）。共享客户端直接用 `http.Client{Timeout: 15s}`，不需要 idle guard。`postJSON` 只负责传输；非 2xx 的判定和脱敏由各适配器在拿到 `(status, body, err)` 后统一走 `channelError(channelType, ch, status, body)` 处理。

飞书 payload：

```go
func feishuPayload(ch Channel, n Notification, now time.Time) (map[string]any, error) {
	ts := strconv.FormatInt(now.Unix(), 10)
	payload := map[string]any{
		"timestamp": ts,
		"msg_type":  "interactive",
		"card": map[string]any{
			"schema": "2.0",
			"header": map[string]any{
				"title": map[string]any{"tag": "plain_text", "content": "[" + strings.ToUpper(string(n.Severity)) + "] " + n.Title},
				"template": feishuTemplate(n.Severity),
			},
			"body": map[string]any{"elements": []any{
				map[string]any{"tag": "markdown", "content": n.Markdown},
			}},
		},
	}
	if secret := strings.TrimSpace(ch.Config.Secret); secret != "" {
		// 飞书自定义机器人：key = timestamp+"\n"+secret，message 为空。
		mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
		mac.Write(nil)
		payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return payload, nil
}
```

钉钉签名：

```go
func dingtalkURL(webhook, secret string, now time.Time) (string, error) {
	if secret == "" { return webhook, nil }
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	sep := "?"
	if strings.Contains(webhook, "?") { sep = "&" }
	return webhook + sep + "timestamp=" + ts + "&sign=" + sign, nil
}
```

错误处理：非 2xx 时读取最多 8KB，返回 `fmt.Errorf("%s HTTP %d: %s", channelType, status, truncate(body, 500))`；错误文本要替换 webhook/secret/token 为 `***`（`scrubChannelSecret`）。Telegram 的 token 在 URL 中，错误路径只返回状态码和脱敏后的 body。

消息正文：`n.Markdown` 由 Dispatcher 生成；渠道层不拼接业务字段，只加级别前缀和卡片颜色。

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -run 'TestFeishu|TestDingTalk|TestWeCom|TestTelegram' -v`
Expected: PASS。

---

## Task 8: 调度器（分级、静默、重试、冷却）

**Files:**
- Create: `internal/monitoring/dispatcher.go`
- Test: `internal/monitoring/dispatcher_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestP1ImmediateInPhaseOne(t *testing.T)
func TestP2NeverPushed(t *testing.T)
func TestCooldownSuppressesRepeat(t *testing.T)
func TestUpgradeBypassesCooldown(t *testing.T)
func TestQuietHoursDeferP1ButNotP0(t *testing.T)
func TestChannelMinSeverityFilter(t *testing.T)
func TestRecoveryShortLivedSkipped(t *testing.T)
func TestPushRetryThenFailedRecorded(t *testing.T)
```

用 fake adapter 记录调用次数；`now` 注入。总共最多 3 次尝试：第 1、2 次失败后分别等待 `retryWait[0]`、`retryWait[1]`（默认 1s、5s）。重试测试注入短退避，避免真等 6 秒：构造后用 `d.retryWait = []time.Duration{time.Millisecond, time.Millisecond}` 覆盖默认 `{time.Second, 5 * time.Second}`；`TestPushRetryThenFailedRecorded` 断言 adapter 被调用 3 次、`ListMonitorEventPushes` 状态为 `failed` 且 `LastError` 已脱敏。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestP1 -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

```go
type ChannelStore interface {
	UpsertMonitorState(AlertState) (AlertState, error)
	UpsertMonitorEventPush(EventPush) error
	UpdateMonitorEventPush(eventID int64, status, message string) error
	ListMonitorEventPushes(eventID int64) ([]EventPush, error)
}

type Dispatcher struct {
	store     ChannelStore
	adapters  map[string]ChannelAdapter
	channels  []Channel
	dispatch  DispatchConfig
	rules     map[string]RuleConfig
	now       func() time.Time
	retryWait []time.Duration // 默认 {1s, 5s}；测试注入短值。最多 3 次尝试，不是 3 次重试
	deferred  []queuedNotification
}

type queuedNotification struct {
	Event    Event
	State    AlertState
	Channel  Channel
	Message  Notification
	Attempts int
}

func NewDispatcher(store ChannelStore, channels []Channel, cfg DispatchConfig, rules []RuleConfig) *Dispatcher
func (d *Dispatcher) Reload(channels []Channel, cfg DispatchConfig, rules []RuleConfig) // Service 每 tick 调用，渠道 CRUD 立即生效（≤60s）
func (d *Dispatcher) Dispatch(ctx context.Context, tr Transition) error
func (d *Dispatcher) Flush(ctx context.Context) error // 静默结束后重发排队的 P1
```

渠道不在 `ChannelStore` 里读取：channels 是 settings JSON，由 Service 通过 `monitoring.Config` 读出后经 `Reload` 推给 Dispatcher。这样 `ChannelStore` 只负责状态与投递记录，store 不需要实现 `ListChannels`。

决策顺序：

1. `tr.Event.Kind != alert` 或 `severity == info` → 跳过。
2. `P2` → 标记事件 `pushStatus=skipped`，不调用渠道。
3. `state.Disposition == ignored` → `suppressed`。
4. `state.SuppressedUntil.After(now)` → `suppressed`。
5. 冷却：`!tr.Upgraded && !tr.Reminder && tr.Firing && !state.LastNotifiedAt.IsZero() && now.Sub(state.LastNotifiedAt) < cooldown` → `skipped`。冷却值从 `d.rules[state.RuleKey].CooldownMinutes` 取；`Reminder` 本身表示已到冷却，直接放行。恢复事件不受冷却限制。
6. 渠道过滤：`Enabled && event.Severity.Meets(ch.MinSeverity)`。
7. 静默：P0 放行；P1 在静默窗口内把 `queuedNotification` 追加到 `d.deferred`，事件 `pushStatus` 先保持 `pending`，不记为 skipped。`Flush` 在每个 tick 和静默结束后的第一次 tick 调用，取出队列发送。
8. 恢复：`recoveryNotify=false` 或持续 <5 分钟 → `skipped`。持续时间 = `state.LastSeen - state.FirstSeen`。
9. 发送：每个渠道最多 3 次尝试（第 1 次失败后等 1s，第 2 次失败后等 5s，第 3 次失败即记 `failed`），`attempts` 累加；成功记 `sent`，失败记 `failed` + 脱敏错误。
10. 任一渠道成功则更新 `state.LastNotifiedAt=now` 并 `UpsertMonitorState`；事件 `push_status` 聚合为 `sent/partial/failed/skipped`。

通知正文：

```go
func buildNotification(state AlertState, ev Event, baseURL string, loc *time.Location) Notification {
	md := fmt.Sprintf("- 对象：%s\n- 级别：%s\n- 时间：%s\n- 详情：%s",
		state.TargetName, strings.ToUpper(string(ev.Severity)), ev.Time.In(loc).Format("2006-01-02 15:04:05"), summaryDetail(ev.Detail))
	n := Notification{
		Fingerprint: state.Fingerprint,
		Severity:    ev.Severity,
		Title:       ev.Title,
		Target:      state.TargetName,
		Markdown:    md,
		At:          ev.Time,
		Kind:        ev.Kind,
	}
	if baseURL != "" {
		n.URL = baseURL + "/monitoring?tab=alerts&alert=" + strconv.FormatInt(state.ID, 10)
	}
	return n
}
```

`summaryDetail` 只接受数字、字符串、`[]string`（IP 列表截 12 个），并过滤键名含 `token`/`secret`/`key`（大小写不敏感）的字段。

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -run TestDispatcher -v` 或 `go test ./internal/monitoring/ -v`
Expected: PASS。

---

## Task 9: Service 编排与配置热更新

**Files:**
- Create: `internal/monitoring/service.go`
- Test: `internal/monitoring/service_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestServiceEvaluatesAndDispatches(t *testing.T)
func TestServiceReloadsRulesOnSettingChange(t *testing.T)
func TestServiceRecordsRenewalEvent(t *testing.T)
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/monitoring/ -run TestService -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

```go
type Service struct {
	collector  *Collector
	engine     *Engine
	dispatcher *Dispatcher
	store      ConfigStore
	baselines  BaselineComputer
	now        func() time.Time
	lastRules  string
	lastChannels string
	lastDispatch string
	lastSuccess time.Time
	lastBaselineAt time.Time
	mu         sync.Mutex
}

func NewService(collector *Collector, engine *Engine, dispatcher *Dispatcher, store ConfigStore, baselines BaselineComputer) *Service
func (s *Service) Run(ctx context.Context) {
	go s.watchdog(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.Tick(ctx) // 立即跑一次，避免重启后等 60 秒
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C: s.Tick(ctx)
		}
	}
}

// Tick 用真实时钟跑一轮。TickAt 供测试推进模拟时间，生产只用 Tick。
func (s *Service) Tick(ctx context.Context) error { return s.tickAt(ctx, s.now()) }
func (s *Service) TickAt(ctx context.Context, now time.Time) error { return s.tickAt(ctx, now) }

func (s *Service) watchdog(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
			s.mu.Lock()
			last := s.lastSuccess
			s.mu.Unlock()
			s.evaluateCollectorHealth(ctx, last)
		}
	}
}

func (s *Service) tickAt(ctx context.Context, now time.Time) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("monitoring tick panic", "panic", r)
		}
	}()
	s.reloadConfigIfChanged()
	snap, err := s.collector.Snapshot(ctx, now)
	if err != nil {
		slog.Warn("monitoring snapshot failed", "error", err)
		return err
	}
	s.mu.Lock()
	s.lastSuccess = now
	s.mu.Unlock()
	_ = s.store.SetSetting("monitor_collector_last_success", now.UTC().Format(time.RFC3339Nano))
	s.maybeRecomputeBaselines(now)
	transitions, evalErr := s.engine.Evaluate(ctx, snap)
	if evalErr != nil {
		slog.Warn("monitoring evaluate failed", "error", evalErr)
		return evalErr
	}
	for _, tr := range transitions {
		if err := s.dispatcher.Dispatch(ctx, tr); err != nil {
			slog.Warn("monitoring dispatch failed", "fingerprint", tr.State.Fingerprint, "error", err)
		}
	}
	return s.dispatcher.Flush(ctx)
}

// reloadConfigIfChanged 每 tick 对比 settings 原文；变化时同时刷新规则、渠道与调度配置。
// 阶段 1 不提供 rules HTTP 编辑接口，但运维可以直接改库，热更新仍然生效。
func (s *Service) reloadConfigIfChanged() {
	cfg := NewConfig(s.store)
	rawRules := s.store.Setting("monitor_rules")
	if rawRules != s.lastRules {
		if rules, err := UnmarshalRules(rawRules); err == nil {
			s.engine.SetRules(rules)
			s.lastRules = rawRules
		} else {
			slog.Warn("monitoring rules parse failed; keep previous", "error", err)
		}
	}
	rawChannels := s.store.Setting("monitor_channels")
	rawDispatch := s.store.Setting("monitor_dispatch")
	if rawChannels != s.lastChannels || rawDispatch != s.lastDispatch {
		s.dispatcher.Reload(cfg.Channels(), cfg.Dispatch(), s.engine.Rules())
		s.lastChannels = rawChannels
		s.lastDispatch = rawDispatch
	}
}

func (s *Service) evaluateCollectorHealth(ctx context.Context, lastSuccess time.Time) {
	transitions, err := s.engine.EvaluateCollectorHealth(ctx, s.now(), lastSuccess)
	if err != nil {
		slog.Warn("collector health evaluate failed", "error", err)
		return
	}
	for _, tr := range transitions {
		_ = s.dispatcher.Dispatch(ctx, tr)
	}
}

func (s *Service) maybeRecomputeBaselines(now time.Time) {
	if s.baselines == nil {
		return
	}
	if !s.lastBaselineAt.IsZero() && now.Sub(s.lastBaselineAt) < time.Hour {
		return
	}
	loc := monitoringConfigDispatchLocation(s.store)
	if err := s.baselines.RecomputeTTFTBaselines(now, loc); err != nil {
		slog.Warn("ttft baseline recompute failed", "error", err)
		return
	}
	s.lastBaselineAt = now
}
```

`monitoringConfigDispatchLocation` 是 package-level helper：`NewConfig(s.store).Dispatch().Location()`。

```go
func monitoringConfigDispatchLocation(store ConfigStore) *time.Location {
	return NewConfig(store).Dispatch().Location()
}
```

`RecordRenewal` 不落原始错误，只落分类：

```go
func classifyRenewalError(err error) string {
	if err == nil { return "" }
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "403"), strings.Contains(text, "unauthorized"),
		strings.Contains(text, "invalid_grant"), strings.Contains(text, "sign in"), strings.Contains(text, "登录"):
		return "auth_failed"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"), strings.Contains(text, "connection"):
		return "network"
	default:
		return "unknown"
	}
}

func (s *Service) RecordRenewal(ctx context.Context, rec RenewalRecord) {
	severity := SeverityInfo
	title := "凭证续期成功"
	detail := map[string]any{"vendor": rec.Vendor, "durationMs": rec.Duration.Milliseconds()}
	if rec.Err != nil {
		severity = SeverityP0
		title = "凭证续期失败"
		errorKind := classifyRenewalError(rec.Err)
		detail["errorKind"] = errorKind
		// 只记分类：原始错误可能带 token/请求体，而调用点已经负责本地详细日志。
		slog.Warn("monitoring: credential renewal failed", "provider", rec.ProviderID, "errorKind", errorKind)
	}
	event := Event{
		Time: rec.StartedAt, Kind: EventKindRenewal, Severity: severity,
		Title: title, TargetType: TargetProvider, TargetID: rec.ProviderID,
		Detail: detail, Status: StatusInfo,
	}
	if _, err := s.store.InsertMonitorEvent(event); err != nil {
		slog.Warn("record renewal event failed", "error", err)
	}
}
```

`ConfigStore` 增加 `InsertMonitorEvent`（合并到接口）。配置热更新：每 tick 读取 `monitor_rules` 字符串，变化时 `UnmarshalRules` + `engine.SetRules`；解析失败记日志并保留旧配置。

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/monitoring/ -v`
Expected: PASS。

---

## Task 10: gateway 数据源与首次 VPS 部署

**Files:**
- Create: `internal/gateway/monitoring_source.go`
- Create: `internal/gateway/monitoring_runtime.go`
- Modify: `internal/gateway/server.go`（struct 字段、`NewServer` 注入、`StartMonitoring`）
- Modify: `internal/app/runtime.go:327-338`（启动）
- Test: `internal/gateway/monitoring_source_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestMonitoringSourceProviders(t *testing.T)
func TestMonitoringSourceKeysUsesWindowUsage(t *testing.T)
func TestMonitoringSourceMultiIPMapsHits(t *testing.T)
func TestMonitoringSourceQuotaUnsupportedIsNil(t *testing.T)
func TestQuotaFromWindowsPicksWorstWindow(t *testing.T) // 80% 用掉的窗口优先于 20% 的；无 Limit 窗口被跳过
func TestWorkBuddyCreditsDeadline(t *testing.T)         // RunsOut>0 的最早 RunsOutAt
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/gateway/ -run TestMonitoringSource -v`
Expected: FAIL。

- [ ] **Step 3: 实现数据源**

`monitoringSource` 持有 `*Server`，全部 `Source` 方法都定义在 `*monitoringSource` 上：

```go
type monitoringSource struct {
	server *Server
	modelFailures map[string]int // set by model probe loop
	modelMu sync.Mutex
}

func newMonitoringSource(s *Server) *monitoringSource {
	return &monitoringSource{server: s, modelFailures: map[string]int{}}
}

func (m *monitoringSource) Providers(ctx context.Context, now time.Time) ([]monitoring.ProviderObservation, error) {
	state := m.server.router.State()
	out := make([]monitoring.ProviderObservation, 0, len(state.Providers))
	for _, p := range state.Providers {
		if p.Deleted { continue }
		_, canCheckin := checkinVendorOf(p)
		obs := monitoring.ProviderObservation{
			ID: p.ID, Name: p.Name, Vendor: providerVendorKey(p),
			AuthType: string(p.AuthType), HealthStatus: p.HealthStatus,
			Capabilities: monitoring.Capabilities{
				OAuth: providerHasExpiry(p),
				Quota: providerHasQuotaHandler(p),
				Models: providerHasModelSync(p),
				Checkin: canCheckin,
			},
		}
		if expiry := providerCredentialExpiry(p); !expiry.IsZero() {
			obs.ExpiresAt = expiry
		}
		if renewal, err := m.server.monitoringStore.RecentRenewalOutcomes(p.ID, 10); err == nil {
			obs.Renewal = renewal
		}
		obs.ModelFailures, _ = m.ModelFailures(ctx, p.ID)
		out = append(out, obs)
	}
	return out, nil
}
```

`providerVendorKey` 在仓库里不存在，**新建于本文件**：一个按 `BaseURL`/`AuthType` 返回前端图标厂商键的 switch（`anthropic`/`openai`/`cursor`/`qoder`/`workbuddy`/`zcode`/`trae`/`glm`/`deepseek`/其他）。`providerHasExpiry`、`providerHasQuotaHandler`、`providerHasModelSync`、`providerCredentialExpiry` 也都是新建的本文件 helper。`providerCredentialExpiry` 只读这几个真实存在的字段：`ClaudeOAuth.ExpiresAt`、`ChatGPTOAuth.ExpiresAt`、`CursorOAuth.ExpiresAt`、`QoderPAT.ExpiresAt`、`WorkBuddy.ExpiresAt`、`Trae.ExpiresAt`（`ZCodeCredential` 没有 ExpiresAt，返回零值），只读不刷新。该函数**只在 `monitoring_source.go` 声明一次**，Task 11 的 `recordRenewal` 直接复用，不要在 `monitoring_renewal.go` 里重复声明。

`Keys`：

```go
for _, key := range m.server.router.State().APIKeys {
	obs := monitoring.KeyObservation{ID: key.ID, Name: key.Name, QuotaWindow: key.QuotaWindow,
		TokenLimit: key.QuotaTokens, CostLimitMicros: key.QuotaCostMicros}
	if key.QuotaTokens > 0 || key.QuotaCostMicros > 0 {
		window, err := normalizeQuotaWindow(key.QuotaWindow)
		if err == nil && window != quotaWindowUnlimited {
			from, to := window.bounds(now)
			used := m.server.logs.KeyWindowUsage(key.ID, from, to)
			obs.TokenUsed = used.Tokens
			obs.CostUsedMicros = used.CostMicros
		}
	}
	out = append(out, obs)
}
```

`Windows` 调用 `s.stateSaver.(interface{ WindowStatsByProvider(...) })`；若 store 不支持返回空 map + error，让采集器标记数据错误但不 panic。

`Baselines` 调用 `TTFTBaselines(loc)`；`loc` 从 `monitor_dispatch.timezone` 读，默认 `Asia/Shanghai`。

`Quota`：`Providers` 返回的是监控 DTO，不含凭证；这里按 `obs.ID` 重新从 router 取完整 `domain.Provider`，再按 vendor 分支调用已有 fetcher：

```go
func (m *monitoringSource) Quota(ctx context.Context, obs monitoring.ProviderObservation) (*monitoring.QuotaSnapshot, error) {
	p, err := m.server.router.ProviderByID(obs.ID)
	if err != nil {
		return nil, nil
	}
	now := time.Now().UTC()
	snapshot, err := m.quotaForProvider(ctx, p, now)
	if err != nil {
		slog.Warn("monitoring: quota fetch failed", "provider", p.ID, "vendor", obs.Vendor, "error", err)
		return &monitoring.QuotaSnapshot{Source: obs.Vendor, Ratio: -1, ObservedAt: now, Error: classifyQuotaError(err)}, nil
	}
	return snapshot, nil
}

// quotaForProvider 只映射已有 fetcher 能给出的字段；没有总额的接口 Limit=0。
func (m *monitoringSource) quotaForProvider(ctx context.Context, p domain.Provider, now time.Time) (*monitoring.QuotaSnapshot, error) {
	switch {
	case p.ClaudeOAuth != nil:
		report, err := fetchClaudeOAuthUsage(ctx, p.ClaudeOAuth.AccessToken)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "claude usage unavailable"))
		}
		// 五个窗口取 utilization 最大者：overage > seven_day > five_hour。
		return quotaFromUsedPercent("claude", maxClaudeUtilization(report), time.Time{}, now), nil
	case p.ChatGPTOAuth != nil:
		report, err := fetchChatGPTOAuthUsage(ctx, p)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "chatgpt usage unavailable"))
		}
		return quotaFromUsedPercent("chatgpt", maxChatGPTUtilization(report), time.Time{}, now), nil
	case p.CursorOAuth != nil:
		report, err := fetchCursorOAuthUsage(ctx, p.CursorOAuth.AccessToken)
		if err != nil {
			return nil, err
		}
		if !report.Available {
			return nil, errors.New(firstNonEmpty(report.Error, "cursor usage unavailable"))
		}
		return quotaFromUsedPercent("cursor", maxCursorUtilization(report), time.Time{}, now), nil
	case p.QoderPAT != nil:
		_, quota := m.server.qoderQuotaFor(ctx, p)
		if quota.Error != "" {
			return nil, errors.New(quota.Error)
		}
		return quotaFromWindows("qoder", qoderWindowViews(quota.Windows), parseQuotaTime(quota.Until), now), nil
	case p.WorkBuddy != nil:
		quota := m.server.workBuddyQuota(ctx, p)
		if quota.Error != "" {
			return nil, errors.New(quota.Error)
		}
		return quotaFromWindows("workbuddy", workBuddyWindowViews(quota.Windows), workBuddyCreditsDeadline(quota.Windows), now), nil
	default:
		return nil, nil // 阶段 1 不支持额度采集：能力标记 Quota=false，规则 unavailable
	}
}

func maxClaudeUtilization(report ClaudeOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range []*ClaudeOAuthUsageBucket{
		report.SevenDayOverageIncluded, report.SevenDay, report.FiveHour, report.SevenDaySonnet, report.SevenDayOpus,
	} {
		if bucket != nil && bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

func maxChatGPTUtilization(report ChatGPTOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range report.Buckets {
		if bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

func maxCursorUtilization(report CursorOAuthUsageReport) float64 {
	best := 0.0
	for _, bucket := range report.Buckets {
		if bucket.Utilization > best {
			best = bucket.Utilization
		}
	}
	return math.Min(100, best)
}

// 百分比窗口统一折算成 Remain=100 的配额，Ratio 仍是剩余比例。
func quotaFromUsedPercent(source string, usedPercent float64, expiresAt, now time.Time) *monitoring.QuotaSnapshot {
	ratio := 1 - math.Min(100, math.Max(0, usedPercent))/100
	return &monitoring.QuotaSnapshot{
		Source: source, Remaining: ratio * 100, Limit: 100, Ratio: ratio,
		ExpiresAt: expiresAt, ObservedAt: now,
	}
}

// 两个厂商的窗口类型不同，但都给出 Used（已用百分比 0-100）与 Limit（总量）。
type quotaWindowView struct {
	UsedPercent float64
	Limit       float64
}

func qoderWindowViews(windows []qoderQuotaWindow) []quotaWindowView {
	out := make([]quotaWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, quotaWindowView{UsedPercent: w.Used, Limit: w.Limit})
	}
	return out
}

func workBuddyWindowViews(windows []workBuddyQuotaWin) []quotaWindowView {
	out := make([]quotaWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, quotaWindowView{UsedPercent: w.Used, Limit: w.Limit})
	}
	return out
}

// quotaFromWindows 取「已用比例最高且 Limit>0」的窗口作为账号剩余比例：
// Remaining=(1-Used/100)*Limit，Ratio=Remaining/Limit。没有可用窗口时 Ratio=-1。
func quotaFromWindows(source string, windows []quotaWindowView, expiresAt, now time.Time) *monitoring.QuotaSnapshot {
	var worst quotaWindowView
	found := false
	for _, w := range windows {
		if w.Limit <= 0 {
			continue
		}
		if !found || w.UsedPercent > worst.UsedPercent {
			worst, found = w, true
		}
	}
	if !found {
		return &monitoring.QuotaSnapshot{Source: source, Ratio: -1, ExpiresAt: expiresAt, ObservedAt: now}
	}
	ratio := 1 - math.Min(100, worst.UsedPercent)/100
	return &monitoring.QuotaSnapshot{
		Source: source, Remaining: ratio * worst.Limit, Limit: worst.Limit, Ratio: ratio,
		ExpiresAt: expiresAt, ObservedAt: now,
	}
}

func parseQuotaTime(raw string) time.Time {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return at
}

// WorkBuddy 的积分按批过期：取 RunsOut>0 的最早 RunsOutAt 作为 credit_expiring 的输入。
func workBuddyCreditsDeadline(windows []workBuddyQuotaWin) time.Time {
	var earliest time.Time
	for _, w := range windows {
		if w.RunsOut <= 0 || strings.TrimSpace(w.RunsOutAt) == "" {
			continue
		}
		at := parseQuotaTime(w.RunsOutAt)
		if at.IsZero() {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}
```

失败时返回 `Ratio=-1` 的快照，不返回 error（避免整轮快照失败）；`source_error` 只放 `classifyQuotaError` 的分类（`auth_failed`/`rate_limited`/`network`/`unknown`），真实错误只写本地日志，绝不落原始响应体。

`QuotaSnapshot.ExpiresAt` 只用于 `credit_expiring`：Claude/ChatGPT/Cursor 的窗口 `resets_at` 是限额重置，不是积分到期，阶段 1 不填；Qoder 取 `qoderQuota.Until`（积分窗口结束），WorkBuddy 取各窗口 `RunsOutAt` 中 `RunsOut>0` 的最早值（某一批积分实际作废时间），都没有则零值。

`MultiIP` / `ConcurrentIP`：调用 `s.alertStore.DetectMultiIPKeys(now.Add(-10m), 5)` / `DetectConcurrentIPKeys(now.Add(-10m), 4)`，阈值与规则默认值对齐（`multi_ip.minIPs=5`、`concurrent_ip.minIPs=4`）；检测阈值只决定候选集，规则层仍按各 key 的参数再判一次。转换成 monitoring 类型时逐字段复制：`MultiIPHit{APIKeyID, APIKeyName, IPCount, RequestCount, IPs}`；`ConcurrentIPHit{APIKeyID, APIKeyName, IPCount, RequestCount, IPs, At}`。

`LastRenewal`：查 `monitor_events` 最近 renewal 事件（见 Task 11 的查询方法）。

`ModelFailures`：从 `monitoringSource.modelFailures` 读；该 map 由 `StartModelMonitor` 每 15 分钟更新（见下）。

**模型监控循环：**

```go
func (s *Server) startModelMonitor(ctx context.Context) {
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done(): return
			case <-timer.C:
				s.runModelMonitor(ctx)
				timer.Reset(15 * time.Minute)
			}
		}
	}()
}
```

`runModelMonitor` 对支持模型同步的 vendor 调用现有 `sync*Models`，成功清零失败计数，失败 +1。Cursor/Qoder/ZCode/Trae/WorkBuddy 的 sync 函数签名不一致，逐个调用并 catch error；其他 vendor 跳过（能力 `Models=false`）。模型列表拉取不推送消息，只作为 `model_unavailable` 输入。

**启动：**

```go
func (s *Server) StartMonitoring(ctx context.Context) {
	if s.monitoringStore == nil { return }
	cfg := monitoring.NewConfig(s.monitoringStore)
	src := newMonitoringSource(s)
	s.monitoringSrc = src
	collector := monitoring.NewCollector(src, s.monitoringStore)
	engine := monitoring.NewEngine(s.monitoringStore, cfg.Rules())
	dispatcher := monitoring.NewDispatcher(s.monitoringStore, cfg.Channels(), cfg.Dispatch(), cfg.Rules())
	svc := monitoring.NewService(collector, engine, dispatcher, s.monitoringStore, s.monitoringStore)
	s.monitoring = svc
	// 写入启动心跳事件，便于部署验收确认调度器真的在跑。
	if _, err := s.monitoringStore.InsertMonitorEvent(monitoring.Event{
		Time: time.Now().UTC(), Kind: monitoring.EventKindSystem, Severity: monitoring.SeverityInfo,
		Title: "监控中心已启动", TargetType: monitoring.TargetGateway, Status: monitoring.StatusInfo,
		Detail: map[string]any{"version": 1},
	}); err != nil {
		slog.Warn("monitoring start event failed", "error", err)
	}
	go svc.Run(ctx)
	go s.startModelMonitor(ctx)
}
```

`NewServer` 增加（必须放在现有 `if len(stateSaver) > 0 {` 块内，`stateSaver[0]` 不能裸索引）：

```go
if ms, ok := stateSaver[0].(MonitoringStore); ok {
	server.monitoringStore = ms
}
```

`MonitoringStore` 接口在 `server.go` 或新文件里声明，包含 Task 3 的全部方法与 `Setting/SetSetting`；`*store.Store` 必须同时满足 `monitoring.ConfigStore`、`monitoring.StateStore`、`monitoring.ChannelStore` 与 `monitoring.BaselineComputer`。

**部署检查点：**

- [x] 本地 `go build ./... && go test ./...`
- [ ] 部署：`bash deploy/deploy-linux.sh --skip-web`
- [ ] VPS 验证：
  - `journalctl -u llm-protocol-gateway -n 100 | grep -i monitor`，确认采集器启动、无 panic。
  - `sqlite3 /var/lib/llm-protocol-gateway/gateway.db "select count(*) from monitor_events; select count(*) from monitor_alert_states;"`，确认表存在。
  - 等待 2 分钟，确认 `monitor_alert_states` 出现真实规则状态或至少采集器心跳事件。
  - 确认旧 Telegram 推送没有重复发送。

---

## Task 11: 续期埋点与旧告警推送切换

**Files:**
- Create: `internal/gateway/monitoring_renewal.go`
- Modify: `internal/gateway/chatgpt_oauth.go:437`
- Modify: `internal/gateway/claude_oauth.go:559`
- Modify: `internal/gateway/cursor_oauth.go:343`
- Modify: `internal/gateway/qoder_oauth.go:627`
- Modify: `internal/gateway/qoder_pat_handlers.go:175`
- Modify: `internal/gateway/trae.go:735`
- Modify: `internal/gateway/workbuddy.go:673`
- Modify: `internal/gateway/alerts.go:284`（`pushAlert`）
- Test: `internal/gateway/monitoring_renewal_test.go`

- [x] **Step 1: 写失败测试**

```go
func TestRecordRenewalWritesEvent(t *testing.T)
func TestLegacyAlertPushDisabledWhenMonitoringRuns(t *testing.T)
```

- [ ] **Step 2: 运行确认失败**

（本步未单独跑红：测试与实现同批完成，见下方运行结果。）

Run: `go test ./internal/gateway/ -run 'TestRecordRenewal|TestLegacyAlertPush' -v`
Expected: FAIL。

- [x] **Step 3: 实现**

`monitoring_renewal.go`：

```go
func (s *Server) recordRenewal(provider domain.Provider, vendor string, started time.Time, err error) {
	if s == nil || s.monitoring == nil {
		return
	}
	expiresAt := providerCredentialExpiry(provider)
	s.monitoring.RecordRenewal(context.Background(), monitoring.RenewalRecord{
		ProviderID: provider.ID, Vendor: vendor, StartedAt: started,
		Duration: time.Since(started), Err: err, ExpiresAt: expiresAt,
	})
}
```

调用点统一改为（成功时传续期后的 provider，让事件的到期时间反映新凭证；失败时传原 provider）：

```go
started := time.Now()
refreshed, err := refreshXxx(...)
s.recordRenewal(refreshed, "claude", started, err)
if err != nil { ... }
```

逐个调用点（行号为当前工作树实测，改动后以函数名为准）：

| 文件:行 | 调用 | vendor | 到期字段 | 失败行为 |
|---|---|---|---|---|
| `chatgpt_oauth.go:437` | `refreshChatGPTToken` | `chatgpt` | `provider.ChatGPTOAuth.ExpiresAt` | 返回 error |
| `claude_oauth.go:559` | `refreshClaudeOAuthToken` | `claude` | `provider.ClaudeOAuth.ExpiresAt` | 返回 error |
| `cursor_oauth.go:343` | `refreshCursorOAuthToken` | `cursor` | `provider.CursorOAuth.ExpiresAt` | 返回 error |
| `qoder_oauth.go:627` | `renewQoderCredential` | `qoder` | `provider.QoderPAT.ExpiresAt` | 非登录失效错误被吞，返回 `(provider, nil)` |
| `qoder_pat_handlers.go:173` | `exchangeQoderJobToken`（手填 PAT 重连） | `qoder` | `provider.QoderPAT.ExpiresAt` | 返回 error 给 HTTP 层 |
| `qoder_pat_handlers.go:175` | `renewQoderCredential`（设备码重连） | `qoder` | `provider.QoderPAT.ExpiresAt` | 返回 error 给 HTTP 层 |
| `trae.go:735` | `traeRenew` | `trae` | `provider.Trae.ExpiresAt` | 非登录失效错误被吞，返回 `(provider, nil)` |
| `workbuddy.go:673` | `refreshWorkBuddyCredential`（在 `refreshWorkBuddyTokenLocked` 内） | `workbuddy` | `provider.WorkBuddy.ExpiresAt` | 助手返回 error，被 `ensureFreshWorkBuddyToken:723` / `forceRefreshWorkBuddyToken:767` 吞 |

`providerCredentialExpiry(provider domain.Provider) time.Time` 按 `AuthType` 取上面表格的字段并 `time.Parse(time.RFC3339)`；解析失败或该 AuthType 没有到期字段（api_key、zcode、opencode_zen）返回零值，`RecordRenewal` 原样把零值写进事件。

表中 `renewQoderCredential`、`traeRenew`、`refreshWorkBuddyCredential` 返回的是 credential 而不是 provider：在 `SetProviderQoderPAT` / `SetProviderTrae` / `SetProviderWorkBuddy` 成功后用返回的 `updated domain.Provider` 调 `recordRenewal`，失败时用进入函数时的 `provider`。`qoder_pat_handlers.go` 的重连路径没有可直接用的 `updated` 时，同样按这个规则取。

注意：`qoder_oauth.go`、`trae.go`、`workbuddy.go` 的失败分支会把错误吞掉继续服务，埋点必须在判断前记录原始 `err`，否则 `oauth_refresh_failed` 永远收集不到失败。

`internal/gateway/alerts.go` 的 `pushAlert` 开头增加：

```go
if s.monitoring != nil {
	_ = s.alertStore.UpdateAlertPush(alert.ID, domain.AlertPushDisabled, "")
	return
}
```

旧扫描仍写 `alerts` 表；新迁移每次启动增量导入，因此旧规则产生的历史不会丢。

**最近续期查询**（供 Source 使用）：

```go
func (s *Store) RecentRenewalOutcomes(providerID string, limit int) (monitoring.RenewalOutcome, error) {
	// SELECT severity, time FROM monitor_events
	// WHERE kind='renewal' AND target_id=? ORDER BY time DESC LIMIT ?
	// 从最新向前数连续 severity='p0' 的条数
}
```

在 `internal/store/monitoring.go` 实现并在 Task 3 的接口中补上。

- [x] **Step 4: 运行测试**

Run: `go test ./internal/gateway/ -run 'TestRecordRenewal|TestLegacyAlertPush' -v`
Expected: PASS。

---

## Task 12: `/__monitor/*` HTTP API 与第二次 VPS 部署

**Files:**
- Create: `internal/gateway/monitoring_handlers.go`
- Modify: `internal/gateway/server.go:340-345`（注册新路由，旧路由保留）
- Test: `internal/gateway/monitoring_handlers_test.go`

- [x] **Step 1: 写失败测试**

只覆盖阶段 1 端点：

```go
func TestMonitorChannelsCRUDHidesSecrets(t *testing.T)
func TestMonitorOverviewAndAccountsReadOnly(t *testing.T)
func TestMonitorAccountRefreshAndQuotaRefresh(t *testing.T)
func TestMonitorEventsAndAlertsQuery(t *testing.T)
func TestMonitorAlertAckIgnore(t *testing.T)
func TestMonitorDispatchGetPut(t *testing.T)
func TestMonitorEndpointsRequireAdmin(t *testing.T)
```

关键安全断言：GET/POST/PUT channels 的 JSON 都不含 `webhook`/`secret`/`botToken` 明文，只含 `secretSet`/`webhookSet`/`tokenSet`。

**阶段边界（与 spec §17.1 对齐）：** 阶段 1 不提供 `rules*` 与 `probes*` 接口；规则使用内置默认参数，探测页随阶段 3、规则设置随阶段 4 上线。后端 `monitoring.Config` 仍会读 `monitor_rules`，便于阶段 4 直接接上。

- [ ] **Step 2: 运行确认失败**

（本步未单独跑红：测试与实现同批完成，见下方运行结果。）

Run: `go test ./internal/gateway/ -run TestMonitor -v`
Expected: FAIL。

- [x] **Step 3: 实现**

路由：

```go
mux.HandleFunc("GET /__monitor/overview", s.handleMonitorOverview)
mux.HandleFunc("GET /__monitor/accounts", s.handleMonitorAccounts)
mux.HandleFunc("GET /__monitor/accounts/{providerId}", s.handleMonitorAccount)
mux.HandleFunc("POST /__monitor/accounts/{providerId}/refresh", s.handleMonitorAccountRefresh)
mux.HandleFunc("POST /__monitor/accounts/{providerId}/quota/refresh", s.handleMonitorQuotaRefresh)
mux.HandleFunc("GET /__monitor/events", s.handleMonitorEvents)
mux.HandleFunc("GET /__monitor/alerts", s.handleMonitorAlerts)
mux.HandleFunc("GET /__monitor/alerts/{id}", s.handleMonitorAlertByID)
mux.HandleFunc("POST /__monitor/alerts/{id}/ack", s.handleMonitorAlertAck)
mux.HandleFunc("POST /__monitor/alerts/{id}/ignore", s.handleMonitorAlertIgnore)
mux.HandleFunc("POST /__monitor/events/{eventId}/retry-push", s.handleMonitorRetryPush)
mux.HandleFunc("GET /__monitor/channels", s.handleMonitorChannels)
mux.HandleFunc("POST /__monitor/channels", s.handleMonitorCreateChannel)
mux.HandleFunc("PUT /__monitor/channels/{id}", s.handleMonitorUpdateChannel)
mux.HandleFunc("DELETE /__monitor/channels/{id}", s.handleMonitorDeleteChannel)
mux.HandleFunc("POST /__monitor/channels/{id}/test", s.handleMonitorTestChannel)
mux.HandleFunc("GET /__monitor/dispatch", s.handleMonitorDispatch)
mux.HandleFunc("PUT /__monitor/dispatch", s.handleMonitorUpdateDispatch)
```

`overview` 返回阶段 1 能算出的字段：24h 请求总数/成功率/P95/成本（`s.monitoringStore.MonitorRequestSummary(now-24h, now)`）、provider 健康计数、当前 firing 告警（按级别）、最近 20 条事件；`accounts` 返回 provider 列表 + 最新状态/额度/到期时间/能力；`accounts/{id}` 返回同样字段的单项版（provider id 不存在 404）。三者都只读，不触发续期或额度刷新；额度取最新 `provider_metric_samples` 并标注 `stale`。

`alerts` 列表直接返回 `AlertPage.Items`，其中每条 `AlertView` 已带 `latestEventId`/`pushStatus`/`pushError`/`relatedEventIds`（Task 3 的 `QueryMonitorAlerts` 已填充）；`alerts/{id}` 用 `MonitorAlertByID` + `MonitorEventIDsByFingerprint(fp, 10)` 组装同样的字段，再附最近事件的 `detail.timeline` 供详情页展示确认/忽略记录。

`accounts/{id}/refresh`：按凭证类型调用已有 `ensureFresh*Token`（Claude 用 `ensureFreshClaudeTokenWithin(p, 365*24*time.Hour)` 强制刷新；ChatGPT/Cursor/Qoder/WorkBuddy/Trae 调用各自 `ensureFresh*Token`，未到刷新窗口时返回 `{renewed:false, expiresAt}`，绝不为满足“立即”而强行轮换无需轮换的 refresh token）。返回 `{ok, renewed, providerId, expiresAt, errorKind}`；错误只返回分类。

`accounts/{id}/quota/refresh`：`s.monitoringSrc.Quota(ctx, obs)` 强制拉取（15 秒超时），成功后 `UpsertProviderMetricSample` 并返回 `QuotaSnapshot`，失败返回 502 + `{errorKind}`；secret 不返回。

所有 handler 第一行：

```go
if _, ok := s.requireAdmin(w, r); !ok { return }
```

响应统一 `writeJSON`（现有 helper）；错误统一 `writeOpenAIError(w, status, msg)`（现有 helper，不存在 `writeError`）。

规则：阶段 1 没有 `rules*` 路由，`monitor_rules` 只由 Service 读取；不要在此阶段实现编辑接口。

**配置读写统一走 `monitoring.NewConfig(s.monitoringStore)`**：channels/dispatch 两组 handler 通过 `cfg.Channels()/SaveChannels()`、`cfg.Dispatch()/SaveDispatch()` 读写，handler 不直接拼 settings key；`s.monitoringStore` 为 nil 时返回 503（监控未启用），不要 panic。保存后最多 60 秒由 Service 的 `reloadConfigIfChanged` 推到运行中的 Dispatcher。

渠道 GET / POST / PUT 的响应一律用同一个 `channelView` 脱敏（不是只有 GET）：

```go
type channelView struct {
	ID string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	MinSeverity string `json:"minSeverity"`
	Enabled bool `json:"enabled"`
	WebhookSet bool `json:"webhookSet,omitempty"`
	SecretSet bool `json:"secretSet,omitempty"`
	TokenSet bool `json:"tokenSet,omitempty"`
	ChatID string `json:"chatId,omitempty"`
}
```

渠道 PUT：空 `webhook`/`secret`/`botToken` 保留原值；`id` 不可改；`type` 不可改（改类型请新建）。

渠道测试：调用 `AdapterFor(type).Send` 发固定 `Notification{Kind:"test", Severity:"info", Title:"监控中心测试消息"}`，返回 `{ok, status, latencyMs, error}`；错误脱敏。

事件重试：`handleMonitorRetryPush` 用 `MonitorEventByID(eventId)` 取事件、从 body 读 `channelId`，找到渠道后调用 adapter；成功更新 `monitor_event_pushes` 和事件聚合 `push_status`，渠道不存在 404，事件不存在 404。

告警 ack/ignore：只允许 `status=firing`；404 不存在；409 已 resolved。ack 把 `disposition=acknowledged`、`acknowledged_at=now`；ignore 类似。然后调用 `AppendMonitorTimeline(state.Fingerprint, monitoring.TimelineEntry{Action:"acknowledged"|"ignored", Time:now})`，把处置追加到该周期最新事件的 `detail_json.timeline`（spec §8.2：不单独建事件）。

**部署检查点：**

- [x] 本地 `go build ./... && go test ./...`
- [x] 部署 `bash deploy/deploy-linux.sh --skip-web`
- [x] VPS 用管理员会话（不打印 cookie/token）逐项 curl：
  - `GET /__monitor/overview` 200。
  - `GET /__monitor/accounts` 200，返回账号矩阵。
  - `GET /__monitor/channels` 200，无明文 secret。
  - `GET /__monitor/events?range=24h` 200。
  - 未登录访问返回 401/403。
- [x] `journalctl` 无敏感信息。

---

## Task 13: 运行时接线、旧路由保留与端到端集成

**Files:**
- Modify: `internal/app/runtime.go:327-338`（启动顺序 + `MigrateMonitoringV1` + prune）
- Modify: `internal/gateway/server.go`（`Server` 字段 `monitoring *monitoring.Service`、`monitoringStore MonitoringStore`、`monitoringSrc *monitoringSource`）
- Test: `internal/gateway/monitoring_integration_test.go`

- [x] **Step 1: 写集成测试（scratch 风格）**

在 `httptest` 上构造 gateway（现有测试模式），使用临时 SQLite + fake 上游，验证：

1. 插入失败请求日志：10 条窗口样本中 6 条 401（占比 60%，满足 `minCount=3 && minRatio=0.5 && minSamples=10`），时间戳为测试基准时间。
2. 用 `svc.TickAt(ctx, base)`、`base+1m`、`base+2m`、`base+3m` 推进（`auth_invalid` 需要持续 3 分钟），**不要 sleep**：`TickAt` 把模拟时间传给 Collector，`Engine` 用 `Snapshot.Now`。
3. 事件落库、状态 firing、mock 渠道收到请求。
4. 恢复：在 `base+4m` 再插入一批 10×401 + 4×200（与首批叠加后 401 占比仍 >50%，保证规则持续 firing），继续 `TickAt(base+4m)…base+9m`。`state.FirstSeen` 约为 `base+3m`、`LastSeen` 约为 `base+9m`，满足恢复通知要求的持续 ≥5 分钟。然后在 `base+10m` 插入 30 条 200 成功行（5 分钟窗口内 401 占比降到 10/44 ≈ 23% < 50%，样本仍 ≥10），`TickAt(base+10m)`、`TickAt(base+11m)` 两个假 tick → resolved 事件 + 恢复推送。**注意**：只稀释不删除失败行；若失败行老化出窗口导致零样本，规则会走 `data_unavailable` 而不是 recovered。
5. ack 后不再重复推送（冷却期内再 `TickAt` 一次，mock 渠道计数不增）。

- [ ] **Step 2: 运行确认失败**

（本步未单独跑红：测试与实现同批完成，见下方运行结果。）

Run: `go test ./internal/gateway/ -run TestMonitoringIntegration -v`
Expected: FAIL。

- [x] **Step 3: 实现**

`Service` 的 `Tick`/`TickAt` 已在 Task 9 定义，这里只接线，不再重复声明。

`runtime.go` 启动序列调整：

```go
server.StartAlertScan(context.Background())       // 保留
server.StartMonitoring(context.Background())      // 新增，放在 StartAlertScan 之后
```

在 `runtime.go` 启动早期调用 `db.MigrateMonitoringV1()`（需要在 `StartMonitoring` 之前，确保旧数据可查）：

```go
if err := db.MigrateMonitoringV1(); err != nil {
	slog.Warn("monitoring migration failed", "error", err)
}
```

同时 `PruneMonitoringData` 接入 `rt.startStorageMaintenance` 的 6 小时循环（在现有 prune 调用后追加）。

`MonitoringStore` 接口断言：`NewServer` 注入；若 store 未实现，`StartMonitoring` 直接 return，不影响旧功能。

- [x] **Step 4: 运行全量测试**（本计划相关全绿；并发会话在途的流式用例与 Windows 下 internal/tunnel 的 /bin/sh 依赖为已知基线失败，详见文末验收记录）

Run: `go test ./...`
Expected: PASS（除既有的、与本计划无关的失败；如有，记录并确认基线）。

---

## Task 14: VPS 部署与验收

**Files:** 无代码改动。

- [x] **Step 1: 本地构建**

Run:
```bash
cd web && npm run build && cd ..
go build ./...
go test ./...
```

- [x] **Step 2: 部署**

Run: `bash deploy/deploy-linux.sh --skip-web`
（阶段 1 没有前端改动；若工作区混有用户未提交的前端改动，按「整包一起发」处理，不要只发后端。）

- [x] **Step 3: VPS 健康与表结构**

> **2026-10-24 实测环境事实**：VPS 上没有 `sqlite3` CLI，只有 `/usr/bin/python3`（自带 `sqlite3`
> 模块，SQLite 3.45.1）。查库一律走 python3 且用 `file:...?mode=ro` 只读 URI 连接，不给生产写者添锁。
> 发布前已确认的基线：`monitor_events` 5 张表 + `provider_metric_samples` + `checkin_history` 在
> Task 10 那次发布就已建好；`monitor_events` 0 行、`settings` 里没有任何 `monitor_*` 键（监控运行时
> 从未启动，正是 Task 13 接线前的预期状态）；旧 `alerts` 0 行、`checkins` 17 行、`alerts` 设置项里没有
> Telegram 配置 → 迁移预期只补 17 条签到历史，不产生 legacy 告警事件、不建 legacy Telegram 渠道。
> 现网 20 条活跃 provider（17 条订阅）、近 1 小时 418 条请求：有真实流量，发布必须依赖脚本的健康检查
> 与自动回滚。

```bash
ssh root@$VPS_HOST 'systemctl status llm-protocol-gateway --no-pager | head -20'
ssh root@$VPS_HOST 'python3 - <<PY
import sqlite3
db = sqlite3.connect("file:/var/lib/llm-protocol-gateway/gateway.db?mode=ro", uri=True)
print("tables:", [r[0] for r in db.execute("select name from sqlite_master where type=\"table\" and (name like \"monitor%\" or name in (\"provider_metric_samples\",\"checkin_history\")) order by name")])
print("events by kind:", db.execute("select kind, count(*) from monitor_events group by kind").fetchall())
print("settings:", [r[0] for r in db.execute("select key from settings where key like \"monitor%\" order by key")])
print("states:", db.execute("select rule_key, status, severity, target_id, count from monitor_alert_states order by last_seen desc limit 30").fetchall())
PY'
```

Expected：4 张 `monitor_*` 表（`monitor_events`、`monitor_alert_states`、`monitor_event_pushes`、`monitor_baselines`）+ `provider_metric_samples` + `checkin_history`，共 6 张；`monitor_migration_v1` 存在；`monitor_events` 有一条 `kind=system` 的“监控中心已启动”事件；`settings` 出现 `monitor_migration_v1`、`monitor_collector_last_success`；`checkin_history` 有 17 条历史。

- [x] **Step 4: 采集器心跳与规则状态**

```bash
ssh root@$VPS_HOST 'python3 - <<PY
import sqlite3, time
db = sqlite3.connect("file:/var/lib/llm-protocol-gateway/gateway.db?mode=ro", uri=True)
first = db.execute("select value from settings where key=\"monitor_collector_last_success\"").fetchone()
time.sleep(75)
second = db.execute("select value from settings where key=\"monitor_collector_last_success\"").fetchone()
print("heartbeat moved:", first, "->", second, first != second)
print("events:", db.execute("select kind, severity, rule_key, title, time from monitor_events order by id desc limit 30").fetchall())
print("samples:", db.execute("select provider_id, quota_ratio, source_error, observed_at from provider_metric_samples order by observed_at desc limit 20").fetchall())
PY'
```

Expected：心跳设置在一分多钟内被推进（证明 tick 循环真的在跑，而不是只有启动事件）；`monitor_events` 有 `kind=system` 启动事件；至少 `collector_stale` 没有 firing；真实账号若有到期/额度/鉴权问题，应出现对应状态。只读，不改任何账号。

- [x] **Step 5: 渠道验收（mock webhook）**

在 VPS 起一个临时接收器（仅监听 127.0.0.1，验完即删）：

```bash
ssh root@$VPS_HOST 'cat > /tmp/mockhook.py <<EOF
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n=int(self.headers.get("Content-Length","0")); b=self.rfile.read(n)
        open("/tmp/mockhook.log","ab").write(b"\n---\n"+b)
        self.send_response(200); self.end_headers(); self.wfile.write(b"{\"code\":0}")
    def log_message(self,*a): pass
HTTPServer(("127.0.0.1",18999),H).serve_forever()
EOF
nohup python3 /tmp/mockhook.py >/dev/null 2>&1 & echo $!'
```

通过管理员 API 新建 `type=feishu`、`webhook=http://127.0.0.1:18999/hook` 的临时渠道，触发 `POST /__monitor/channels/{id}/test`：

- 断言 `/tmp/mockhook.log` 收到 `msg_type=interactive`，标题含 `[INFO]` 或 `[P1]`。
- 无 secret 时不带 `sign`；配置 secret 后 `sign` 可复算。
- 测试完成后删除临时渠道、kill 接收器、删除日志。

- [x] **Step 6: 推送与恢复演练（auth_invalid）**

> **2026-10-24 改法（重要）**：原计划用外部 `sqlite3` 直接往生产库 `request_logs` 插合成行，并把
> `provider_id` 挂到 `select id from providers ... limit 1` 那个真实账号上。这会污染真实账号的指标
> 窗口、可能给它触发一条假的 P0 并推给运维，而且现网有近 1 小时 418 条的真实流量——外部写事务还要
> 跟网关写者抢锁。改成**全程走管理员 API + 本机 mock 上游**：新建一条一次性 provider 指向 VPS 自己的
> `127.0.0.1:18998`（恒 401），用它自己的 route 与密钥打真实请求，日志由网关自己写（口径天然正确），
> 验完按 API 删除。零外部写库、零真实账号牵连。

在 VPS 起一个只监听 127.0.0.1 的 401 mock 上游：

```bash
ssh root@$VPS_HOST 'cat > /tmp/mock401.py <<EOF
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(401); self.end_headers()
        self.wfile.write(b"{\"error\":{\"type\":\"authentication_error\",\"message\":\"mock\"}}")
    def do_GET(self):
        self.do_POST()
    def log_message(self,*a): pass
HTTPServer(("127.0.0.1",18998),H).serve_forever()
EOF
nohup python3 /tmp/mock401.py >/dev/null 2>&1 & echo $!'
```

再通过 API 建一次性 provider / route / key，并用该密钥打 `/v1/chat/completions` 若干条（401 占比 >50%
且窗口内样本 ≥10），使其在持续 3 分钟后 firing：

```bash
# 建 provider（指向 mock 上游）
curl -s -X POST http://127.0.0.1:18093/__providers -H 'content-type: application/json' \
  -d '{"name":"monitor-selftest","protocol":"openai_chat","baseUrl":"http://127.0.0.1:18998/v1","apiKeySource":"selftest","defaultModel":"gpt-4o-mini"}'
# 建 route（outputProtocol=openai_chat，providerId=上面返回的 id）
curl -s -X POST http://127.0.0.1:18093/__routes -H 'content-type: application/json' \
  -d '{"name":"monitor-selftest","providerId":"<PID>","outputProtocol":"openai_chat"}'
# 建消费密钥（key 自己定，验完删）
curl -s -X POST http://127.0.0.1:18093/__apikeys -H 'content-type: application/json' \
  -d '{"name":"monitor-selftest","routeId":"<RID>","key":"sk-monitor-selftest","streamEnabled":false}'
# 打流量：for i in $(seq 1 12); do curl -s -o /dev/null -X POST http://127.0.0.1:18093/v1/chat/completions \
#   -H "authorization: Bearer sk-monitor-selftest" -H 'content-type: application/json' \
#   -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'; done
```

约 4 分钟后确认（全部走 `/__monitor/*` 只读接口，不查库）：

- `GET /__monitor/alerts?status=firing` 出现 `auth_invalid`（P0，targetId=演练 provider）；
- `GET /__monitor/overview` 的 `firing.p0` 计数同步；
- mock webhook 收到 `[P0] monitor-selftest 凭证失效` 卡片，详情里的 `authCount/total/windowMinutes` 与真实日志一致；
- `GET /__monitor/accounts` 里该 provider 的 `activeAlerts=1`，且**真实账号没有被牵连出告警**。

**维持 firing 直到持续 ≥5 分钟**（恢复通知对 <5 分钟自愈不发，见 spec §10；同时首批 401 行 5 分钟后会
老化出窗口，只等不补会让规则变 unavailable）：firing 后约第 2 分钟再打一批 401，使窗口内仍 >50%。

再等约 5 分钟后稀释：把演练 provider 的 `baseUrl` 改指到一个恒 200 的 mock 上游（`PATCH /__providers/<PID>`），
再打 30 条成功请求。401 占比降到阈值以下、连续 2 个 tick 为假 → 状态 resolved（`reason=recovered`），
收到恢复消息（P0 恢复通知默认开启）。恢复推送详情里的计数必须是**证明恢复那一次窗口**的数字，
不能是 `total=0`。

完成后清理：按 API 依次删除密钥、route、演练 provider，再 `DELETE /__monitor/channels/{id}` 删临时渠道、
kill 两个 mock 进程、删 `/tmp/mockhook*`、`/tmp/mock401*`。演练 provider 留下的 `request_logs` 行属于
已删除账号，会随保留期自然淘汰——**不要**为了"干净"去 DELETE 生产表。

- [x] **Step 7: 安全回归**

```bash
ssh root@$VPS_HOST 'journalctl -u llm-protocol-gateway --since "10 min ago" | grep -Ei "token|secret|webhook|sk-|refresh" || true'
```

Expected：无明文；仅有脱敏后的错误。

- [ ] **Step 8: 记录验收结果**

在 `docs/superpowers/plans/2026-10-23-monitoring-center-phase-1.md` 末尾追加「验收记录」（日期、部署版本、观察到的规则、渠道结果、未决问题）。阶段 1 完成后进入阶段 2（前端监控中心壳）。

---

## 自检清单

- [x] 16 条阶段 1 规则都有触发/不触发/恢复测试（`engine_rules_test.go` 补 server_error_spike / rate_limit_spike（含 maxCount 独立入口）/ empty_response 的达标-边缘-缺样本-恢复，以及 oauth_refresh_failed / credit_expiring；仅剩阶段 3 的 probe_failed / checkin_failed 未测）。
- [ ] 不可用语义：无额度能力、额度接口失败、基线缺失都不产生误报。
- [x] 冷却、P1→P0 升级、抖动抑制、确认/忽略都有测试。
- [x] 四渠道签名与 payload 有 golden 测试；错误不泄露 secret。
- [x] 旧 alerts/checkins/Telegram 迁移幂等；旧告警不会变成活跃状态。
- [x] 采集器 panic 不影响网关主流程（`TestServiceTickPanicIsContained`：panic 只损失当次 tick、心跳不前进、恢复后继续记录）；SQLite 读失败不阻塞请求。
- [x] 所有 `/__monitor/*` 仅管理员可用。
- [x] 本机从未调用真实 Claude 凭证。
- [x] VPS 部署后心跳、事件、推送、恢复、安全五项验收通过。

---

## 验收记录

### 2026-10-24 · Task 11-13 本地验收（VPS 发布待工作区稳定）

**部署版本**：尚未发布。工作区另一会话正在实时改写 `internal/gateway/stream_end_reader.go` /
`passthrough_stream.go`（验收时点 00:40–00:41 仍在写入，`go test ./internal/gateway/` 处于
`setup failed`），发布等于把别人未完成的流式收尾推上生产，因此本轮只做本地验收。

**已完成代码**：Task 11 续期埋点（8 个 refresh 调用点）+ 旧告警推送切换；Task 12
`/__monitor/*` 18 条路由与 handler；Task 13 `runtime.go` 接线（`MigrateMonitoringV1` 前置、
`StartMonitoring` 紧随 `StartAlertScan`、`PruneMonitoringData` 进首轮与 6 小时循环）。

**单元/集成测试**：

- `go test ./internal/monitoring/`、`./internal/store/`、`./internal/app/` 全绿。
- `go test ./internal/gateway/ -run 'TestMonitor|TestRecordRenewal|TestLegacyAlertPush'` 全绿
  （含 7 个计划要求的 handler 测试 + 数据源测试 + 事故全生命周期集成测试）。
- 基线失败（与本计划无关）：`TestStreamEndReader*`、`TestWritePassThroughResponseJudgesLongStreamWhole`、
  `TestUpstreamStreamInterruptedResponse`（并发会话在途）；`internal/tunnel` 全部用例
  （Windows 无 `/bin/sh`，环境限制）。

**隔离实例端到端**（`C:\...\Temp\gwmon\gateway.db`，端口 18097，本机 mock 上游，未触碰生产库，
全程无真实 Claude 凭证）：

| 项 | 结果 |
| --- | --- |
| 启动事件 | `monitor_events` 写入 `kind=system`「监控中心已启动」 |
| 采集器心跳 | `monitor_collector_last_success` 每分钟推进 |
| 18 条路由 | 管理员 200；普通用户 403；无身份 401；无持久层 503 |
| 渠道脱敏 | POST/PUT/GET 响应仅 `webhookSet`，无 webhook token / secret / botToken 明文 |
| 渠道测试 | 飞书卡片 `[INFO] 监控中心测试消息` 8ms 送达 mock 接收端 |
| 事故触发 | 12 条真实 401 → 持续 3 分钟后 `[P0] Drill 上游 凭证失效`（`authCount=12，authRatio=1，total=12，windowMinutes=5`）红色卡片推送 |
| 确认处置 | `POST /__monitor/alerts/1/ack` → `disposition=acknowledged`，时间线进最新事件 `detail.timeline` |
| 重启持久 | 换新二进制重启：告警状态、`acknowledged` 处置、`pushStatus=sent` 全部保留，未重复推送 |
| 恢复通知 | 上游转 200 并补 40 条成功 → `status=resolved reason=recovered`，收到恢复卡片 |
| 账号矩阵 | `activeAlerts` 随事故 1→0；API Key 账号不编造额度/到期；`quota/refresh` 与 `refresh` 对不支持的账号回 400 |

**验收中发现并修掉的两处问题**：

1. `model_unavailable` 原先对所有 provider 生效，纯 API Key 厂商的零计数会把已建立的告警判为恢复；
   改为按 `Capabilities.Models` 门控（引擎侧），补 `TestModelUnavailableOnlyForSyncingProviders`。
2. 恢复推送的详情沿用上一次（可能是宽限期无数据那次）写入的计数，运维读到
   `authCount=0，total=0` 的恢复消息没有信息量；`apply()` 恢复分支改为写入证明恢复那次窗口的
   详情，补 `TestRecoveredEventCarriesCleanWindow`。
3. 账号矩阵的健康列读的是持久化值，真实请求失败只显示「未检测」；改用与控制台同一的
   `applyProviderAvailability` 内存覆盖层。

| 角色边界（非回环 192.168.10.59:18098） | 19/19 通过：匿名 401；普通用户 8 条端点（含 POST/PUT 写操作）全部 403「permission denied」；管理员 200；建渠道/列表响应不含 webhook 明文 token；响应不回显口令 |

**未决**：VPS 发布与 Step 3-7 线上验收（表结构、心跳、mock 渠道、事故演练、journalctl 无明文）。

### 2026-10-24 · Task 12-14 VPS 发布与线上验收（通过）

**发布**：`bash deploy/deploy-linux.sh --skip-web`，溯源 `vcs.revision=f51e17e7…` / `vcs.modified=false`，
`HEALTH_OK`，公网 302 正常。发布时另一会话的流式改动已测完并全绿（用户选择等它测完再发），工作区在
发布时完全干净——本轮监控代码已在 `fix/round26-followup` 分支的提交里。

**门禁**：`go build ./...` OK、`go vet ./...` 无输出、`go test ./...` 除 `internal/tunnel`
（Windows 无 `/bin/sh`，Linux 目标机上不存在此问题）外全绿。

| 验收项 | 结果 |
| --- | --- |
| Step 3 表结构与迁移 | 6 张表齐；`settings` 出现 `monitor_migration_v1`、`monitor_collector_last_success`；`checkin_history` 导入 20 条历史；旧 `alerts` 0 行 → 未产生任何 legacy 告警事件；`alerts` 设置项无 Telegram 配置 → 未建 legacy 渠道。与发布前只读侦察的预测完全一致 |
| Step 4 心跳与状态 | 启动即写 `kind=system`「监控中心已启动」；`collectorLastSuccess` 与请求窗口同分钟推进；`provider_metric_samples` 写入真实比率（cursor 0.434、workbuddy 0.635/0.928、qoder-cn 0.0…） |
| Step 5 渠道 | 建渠道响应仅 `webhookSet/secretSet`，`VPS-WEBHOOK-SECRET`/`VPS-SIGN-SECRET` 在 create/list 响应里都不出现；渠道测试 2ms `ok:true`；收到的卡片 `msg_type=interactive` + `[INFO]`；**签名用飞书算法（key=hmac_sha256(timestamp+"\n"+secret)，空 message）在验收脚本里独立复算并逐字节一致**；验完删渠道、杀接收端、删日志 |
| Step 6 事故演练 | 演练 provider 指向 VPS 本机 127.0.0.1 mock（恒 401），12 条真实消费请求全部由网关写出 401 日志 → 200 秒后 `auth_invalid` firing（P0，`authCount=12，authRatio=1，total=12`），推送 `[P0] monitor-selftest 凭证失效`；补 14 条维持窗口；切 200 上游再打 30 条成功 → **恰好在 firstSeen→lastSeen = 5 分钟门槛**上 `resolved / reason=recovered`，恢复卡片 `[P0] 凭证失效 已恢复` 带的是 `authCount=14，authRatio=0.318，total=44`（本轮修的「恢复详情沿用旧计数」缺陷在线复核生效，不再是全零）；全程只收到 2 条推送（minSeverity=p0 正确挡住 P1/P2）；真实账号无牵连（仅原有的 `quota_low`/`credit_expiring`）；密钥/route/provider/渠道按 API 全部删除，账号数回到 20 |
| Step 7 安全 | `journalctl` 自发布以来扫描 `selftest-secret`/`sk-monitor`/`webhook`/`Bearer`/`oauth_token`/`refresh_token`/`sk-ant` 全部为空；无任何 monitoring 错误或告警日志；从公网 IP 与公网域名未登录访问 6 条读端点全部 401（本机 127.0.0.1 为 200 属本地管理员旁路，符合既有设计） |

**线上真实发现（监控中心第一次跑就抓到的）**：

- `quota_low` **P1**：`Qoder CN 账号` 额度比 0.0 —— 积分已用尽，之前没有任何机制会告这个警。
- `credit_expiring` **P2**：`WorkBuddy 订阅-lxw` 两批积分将在 72 小时内到期。
- 24h 汇总：11728 条请求、成功率 99.36%、P95 首包 12.2s、成本 $153.24。

**遗留**：`monitor_events` 里有 12 条「监控中心已启动」——发布窗口内另一会话连续重启服务所致，
每次进程启动一条属预期行为，不是重复告警（system 类不推送）。前端「监控中心」壳属阶段 2。

**发布后稳定性（spec §17.2「采集器稳定运行 30 分钟」一项）**：进程 01:39:09Z 起（发布后另一会话于该时点
又发了一次带它自己提交的重启）连续运行 34 分钟以上；`monitor_collector_last_success` 每分钟推进
（01:58:10 → 02:01:10 → 02:11:10 → 02:12:10）；`journalctl` 自重启起扫描 `database is locked` /
`SQLITE_BUSY` / `monitoring * failed` / `panic` 命中 0；24h 请求 11836 条、成功率 99.28%（发布前 99.06%
同量级，未见请求路径受影响）；`firing` 稳定为 3 条真实告警（quota_low P1 + credit_expiring P2 ×2）。
