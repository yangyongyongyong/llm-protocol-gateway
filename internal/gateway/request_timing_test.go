package gateway

import (
	"testing"
	"time"
)

func TestRequestTimingSnapshot(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	rt := newRequestTiming(started)

	rt.prepReadyAt.Store(started.Add(100 * time.Millisecond).UnixNano())
	rt.upstreamDoStartAt.Store(started.Add(300 * time.Millisecond).UnixNano())
	rt.upstreamFirstByteAt.Store(started.Add(2300 * time.Millisecond).UnixNano())
	rt.clientFirstWriteAt.Store(started.Add(2500 * time.Millisecond).UnixNano())
	rt.addFlag(timingFlagTouchDB)
	rt.addFlag(timingFlagOAuthRefresh)

	prep, preUp, upTTFB, overhead, convertOut, post, flags := rt.snapshot(2500, 4000)
	if prep != 100 {
		t.Fatalf("prepMs=%d want 100", prep)
	}
	if preUp != 200 {
		t.Fatalf("preUpstreamMs=%d want 200", preUp)
	}
	if upTTFB != 2000 {
		t.Fatalf("upstreamTtfbMs=%d want 2000", upTTFB)
	}
	if overhead != 500 {
		t.Fatalf("gatewayOverheadMs=%d want 500", overhead)
	}
	if convertOut != 200 {
		t.Fatalf("convertOutMs=%d want 200", convertOut)
	}
	if post != 1500 {
		t.Fatalf("postMs=%d want 1500", post)
	}
	if flags != "oauth_refresh,touch_db" {
		t.Fatalf("flags=%q", flags)
	}
}

func TestRequestTimingResetUpstream(t *testing.T) {
	rt := newRequestTiming(time.Now())
	rt.markUpstreamDoStart()
	rt.markUpstreamHeader()
	rt.markUpstreamFirstByte()
	rt.resetUpstreamMarks()
	if rt.upstreamDoStartAt.Load() != 0 || rt.upstreamHeaderAt.Load() != 0 || rt.upstreamFirstByteAt.Load() != 0 {
		t.Fatal("expected upstream marks cleared")
	}
}
