package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 额度接口必须从 Provider 的 Base URL 回到官方 Go 用量路径。
func TestOpenCodeGoUsageURL(t *testing.T) {
	cases := map[string]string{
		"https://opencode.ai/zen/go/v1":       "https://opencode.ai/zen/go/v1/usage",
		"https://opencode.ai/zen/go":          "https://opencode.ai/zen/go/v1/usage",
		"https://relay.opencode.ai/zen/go/v1": "https://relay.opencode.ai/zen/go/v1/usage",
		"":                                    "https://opencode.ai/zen/go/v1/usage",
	}
	for input, want := range cases {
		if got := openCodeGoUsageURL(input); got != want {
			t.Fatalf("openCodeGoUsageURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// rolling / weekly / monthly 分别映射成 5 小时、周、月额度，且零百分比
// 必须保留，不能被当作缺失。
func TestOpenCodeGoUsageFromBody(t *testing.T) {
	report, err := openCodeGoUsageFromBody([]byte(`{
		"usage": {
			"rolling": {"status":"ok","percent":15,"resetsAt":"2026-11-12T06:08:52.000Z"},
			"weekly": {"status":"ok","percent":6,"resetsAt":"2026-11-16T00:00:00.000Z"},
			"monthly": {"status":"ok","percent":0,"resetsAt":"2026-12-11T18:01:25.000Z"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Available || report.Error != "" || report.FetchedAt == "" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.FiveHour == nil || report.FiveHour.Utilization != 15 || report.FiveHour.Status != "ok" {
		t.Fatalf("five hour = %+v", report.FiveHour)
	}
	if report.Weekly == nil || report.Weekly.Utilization != 6 {
		t.Fatalf("weekly = %+v", report.Weekly)
	}
	if report.Monthly == nil || report.Monthly.Utilization != 0 || report.Monthly.ResetsAt == "" {
		t.Fatalf("monthly = %+v", report.Monthly)
	}
}

// 真实请求要带 Bearer Key，并打到 Magpie 使用的官方用量接口。
func TestFetchOpenCodeGoUsageParsesResponse(t *testing.T) {
	var gotPath, gotAuth string
	probe := useProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":15,"resetsAt":"2026-11-12T06:08:52Z"},"weekly":{"status":"ok","percent":6,"resetsAt":"2026-11-16T00:00:00Z"},"monthly":{"status":"ok","percent":3,"resetsAt":"2026-12-11T18:01:25Z"}}}`))
	}))

	report, err := fetchOpenCodeGoUsage(context.Background(), "https://opencode.ai/zen/go/v1", "oc_sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if probe.lastHost != "opencode.ai" || gotPath != "/zen/go/v1/usage" {
		t.Fatalf("queried %q %q, want opencode.ai /zen/go/v1/usage", probe.lastHost, gotPath)
	}
	if gotAuth != "Bearer oc_sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !report.Available || report.FiveHour == nil || report.Weekly == nil || report.Monthly == nil {
		t.Fatalf("missing windows: %+v", report)
	}
}

// Bearer 前缀在 APIKeySource 里被重复写入时不能变成 "Bearer Bearer ..."。
func TestFetchOpenCodeGoUsageNormalizesBearerPrefix(t *testing.T) {
	var gotAuth string
	useProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":0}}}`))
	}))

	if _, err := fetchOpenCodeGoUsage(context.Background(), "https://opencode.ai/zen/go/v1", "Bearer oc_sk-test"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer oc_sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestFetchOpenCodeGoUsageAuthFailureAndEmptyKey(t *testing.T) {
	useProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	report, err := fetchOpenCodeGoUsage(context.Background(), "https://opencode.ai/zen/go/v1", "oc_sk-bad")
	if err != nil {
		t.Fatal(err)
	}
	if report.Available || !strings.Contains(report.Error, "401") {
		t.Fatalf("unexpected auth report: %+v", report)
	}
	missing, err := fetchOpenCodeGoUsage(context.Background(), "https://opencode.ai/zen/go/v1", " ")
	if err != nil || missing.Available || missing.Error == "" {
		t.Fatalf("unexpected missing-key report: %+v / %v", missing, err)
	}
}

// 监控快照取已用比例最高的窗口，并保留三档窗口明细。
func TestOpenCodeGoQuotaSnapshotUsesWorstWindow(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	report := OpenCodeGoUsageReport{Available: true,
		FiveHour: &OpenCodeGoUsageBucket{Utilization: 15, ResetsAt: "2026-11-12T06:08:52Z"},
		Weekly:   &OpenCodeGoUsageBucket{Utilization: 6, ResetsAt: "2026-11-16T00:00:00Z"},
		Monthly:  &OpenCodeGoUsageBucket{Utilization: 73, ResetsAt: "2026-12-11T18:01:25Z"},
	}
	snapshot := openCodeGoQuotaSnapshot(report, now)
	if snapshot == nil || snapshot.Source != "opencode-go" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Ratio < 0.2699 || snapshot.Ratio > 0.2701 {
		t.Fatalf("ratio = %f, want 0.27", snapshot.Ratio)
	}
	if len(snapshot.Windows) != 3 || snapshot.Windows[2].Key != "monthly" {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
}

func TestOpenCodeGoUsagePathAllowedForUsers(t *testing.T) {
	if !isUserProviderUsagePath("/__providers/p1/opencode-go/usage") {
		t.Fatal("expected the OpenCode Go usage path to be readable by owners")
	}
	if isUserProviderUsagePath("/__providers/p1/opencode-go/other") {
		t.Fatal("only the usage sub-path should be allowed")
	}
}

// 没有 Key 时只是不支持额度探测，不应把 Provider 能力画成可用。
func TestProviderHasQuotaHandlerOpenCodeGoNeedsKey(t *testing.T) {
	provider := domain.Provider{AuthType: domain.AuthTypeAPIKey, BaseURL: "https://opencode.ai/zen/go/v1"}
	if providerHasQuotaHandler(provider) {
		t.Fatal("an OpenCode Go provider without a key must not claim quota support")
	}
	provider.APIKeySource = "literal:oc_sk-test"
	if !providerHasQuotaHandler(provider) {
		t.Fatal("a configured OpenCode Go provider should expose quota support")
	}
}
