package gateway

import (
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestRequestStatsCacheTTL(t *testing.T) {
	cache := newRequestStatsCache()
	snapshot := monitor.UsageStatsSnapshot{From: "2026-07-10T00:00:00+08:00"}
	cache.set("today", snapshot)

	got, ok := cache.get("today")
	if !ok || got.From != snapshot.From {
		t.Fatalf("expected cached snapshot, got ok=%v snapshot=%+v", ok, got)
	}

	time.Sleep(requestStatsCacheTTL + 10*time.Millisecond)
	if _, ok := cache.get("today"); ok {
		t.Fatal("expected cache entry to expire")
	}
}
