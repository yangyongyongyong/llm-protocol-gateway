package tunnel

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStartQuickParsesURL(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	var gotArgs []string
	m.run = func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
		gotArgs = args
		// cloudflared prints the URL to stderr amid banner lines.
		output := "2026-07-04 INF Requesting new quick Tunnel\n" +
			"2026-07-04 INF +---------------------------+\n" +
			"2026-07-04 INF |  https://happy-cat-42.trycloudflare.com  |\n" +
			"2026-07-04 INF +---------------------------+\n"
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, io.NopCloser(strings.NewReader(output)), nil
	}

	state, err := m.Start(Settings{Mode: ModeQuick})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if state.Status != StatusRunning {
		t.Fatalf("expected running, got %q", state.Status)
	}
	if state.PublicURL != "https://happy-cat-42.trycloudflare.com" {
		t.Fatalf("unexpected public URL: %q", state.PublicURL)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--config") {
		t.Fatalf("expected isolated --config for quick tunnel, got %v", gotArgs)
	}

	stopped := m.Stop()
	if stopped.Status != StatusStopped {
		t.Fatalf("expected stopped, got %q", stopped.Status)
	}
	if stopped.PublicURL != "" {
		t.Fatalf("expected empty URL after stop, got %q", stopped.PublicURL)
	}
}

func TestStartCustomWithoutTokenReturnsSetupError(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }

	state, err := m.Start(Settings{Mode: ModeCustom, CustomDomain: "lucadesign.uk"})
	if err == nil {
		t.Fatal("expected an error for custom mode without a token")
	}
	if state.Status != StatusError {
		t.Fatalf("expected error status, got %q", state.Status)
	}
	if !strings.Contains(state.Message, "连接 Cloudflare 并绑定") {
		t.Fatalf("expected cloudflare setup instructions, got %q", state.Message)
	}
}

func TestStartCustomWithTokenRunsNamedTunnel(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	var gotArgs []string
	m.run = func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
		gotArgs = args
		// cloudflared prints connection-registration lines once the named tunnel is live.
		output := "2026-07-06 INF Starting tunnel tunnelID=abc\n" +
			"2026-07-06 INF Registered tunnel connection connIndex=0 location=SJC\n"
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, io.NopCloser(strings.NewReader(output)), nil
	}

	state, err := m.Start(Settings{Mode: ModeCustom, CustomDomain: "gateway.lucadesign.uk", TunnelToken: "tok-123"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if state.Status != StatusRunning {
		t.Fatalf("expected running, got %q (msg=%q)", state.Status, state.Message)
	}
	if state.PublicURL != "https://gateway.lucadesign.uk" {
		t.Fatalf("unexpected public URL: %q", state.PublicURL)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "run --token tok-123") {
		t.Fatalf("expected token-based run args, got %v", gotArgs)
	}
	m.Stop()
}

func TestStartMissingBinary(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	_, err := m.Start(Settings{Mode: ModeQuick})
	if err == nil {
		t.Fatal("expected error when cloudflared missing")
	}
	if !strings.Contains(err.Error(), "cloudflared not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithCloudflaredProtocolInsertsFlag(t *testing.T) {
	got := withCloudflaredProtocol([]string{"tunnel", "--config", "c.yml", "run"}, "http2")
	want := "tunnel --protocol http2 --config c.yml run"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	if unchanged := withCloudflaredProtocol([]string{"tunnel", "run"}, ""); strings.Join(unchanged, " ") != "tunnel run" {
		t.Fatalf("empty protocol should leave args unchanged, got %v", unchanged)
	}
}

func TestIsQUICDialFailure(t *testing.T) {
	quicLine := `2026-07-09T09:19:21Z ERR Failed to dial a quic connection error="failed to dial to edge with quic: timeout: no recent network activity" connIndex=0`
	if !isQUICDialFailure(quicLine) {
		t.Fatal("expected QUIC dial failure to be detected")
	}
	http2Line := `ERR Failed to dial to edge error="DialContext error: dial tcp 198.41.200.33:7844: i/o timeout"`
	if isQUICDialFailure(http2Line) {
		t.Fatal("non-QUIC edge failure should not trigger QUIC abort")
	}
}

func TestStartCustomConfigFallsBackToHTTP2OnQUICFail(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	var calls []string
	m.run = func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		var output string
		if strings.Contains(joined, "--protocol http2") {
			output = "2026-07-09 INF Initial protocol http2\n" +
				"2026-07-09 INF Registered tunnel connection connIndex=0 location=LAX protocol=http2\n"
		} else {
			output = "2026-07-09 INF Initial protocol quic\n" +
				"2026-07-09 ERR Failed to dial a quic connection error=\"failed to dial to edge with quic: timeout: no recent network activity\" connIndex=0\n"
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, io.NopCloser(strings.NewReader(output)), nil
	}

	state, err := m.Start(Settings{
		Mode:         ModeCustom,
		CustomDomain: "gateway.lucadesign.uk",
		ConfigFile:   "/tmp/tunnel-config.yml",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if state.Status != StatusRunning {
		t.Fatalf("expected running after HTTP/2 fallback, got %q (msg=%q)", state.Status, state.Message)
	}
	if state.PublicURL != "https://gateway.lucadesign.uk" {
		t.Fatalf("unexpected public URL: %q", state.PublicURL)
	}
	if len(calls) != 2 {
		t.Fatalf("expected auto then http2 attempts, got %d: %v", len(calls), calls)
	}
	if strings.Contains(calls[0], "--protocol http2") {
		t.Fatalf("first attempt should use default protocol, got %q", calls[0])
	}
	if !strings.Contains(calls[1], "--protocol http2") {
		t.Fatalf("second attempt should force http2, got %q", calls[1])
	}
	if !strings.Contains(state.Message, "HTTP/2") {
		t.Fatalf("expected HTTP/2 success message, got %q", state.Message)
	}
	m.Stop()
}

func TestNamedTunnelTimeoutMessageIncludesActionableHint(t *testing.T) {
	msg := namedTunnelTimeoutMessage("failed to dial to edge with quic")
	if !strings.Contains(msg, "TCP 7844") || !strings.Contains(msg, "HTTP/2") {
		t.Fatalf("expected actionable Chinese hint, got %q", msg)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	m := NewManager(18093)
	// Stopping when nothing is running should be safe and report stopped.
	state := m.Stop()
	if state.Status != StatusStopped {
		t.Fatalf("expected stopped, got %q", state.Status)
	}
	// A second stop must not panic and must remain stopped.
	if second := m.Stop(); second.Status != StatusStopped {
		t.Fatalf("expected stopped on repeat, got %q", second.Status)
	}
}

func TestSnapshotReflectsRunningProcess(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	m.run = func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
		output := "INF |  https://spark-lake-9.trycloudflare.com  |\n"
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, io.NopCloser(strings.NewReader(output)), nil
	}
	if _, err := m.Start(Settings{Mode: ModeQuick}); err != nil {
		t.Fatalf("start error: %v", err)
	}
	snap := m.Snapshot()
	if snap.PID == 0 {
		t.Fatal("expected a non-zero PID while running")
	}
	if snap.StartedAt == "" {
		t.Fatal("expected startedAt to be set while running")
	}
	m.Stop()
}

func TestScrubProxyEnvRemovesProxyVars(t *testing.T) {
	got := scrubProxyEnv([]string{
		"PATH=/usr/bin",
		"HTTP_PROXY=http://127.0.0.1:7890",
		"https_proxy=http://127.0.0.1:7890",
		"ALL_PROXY=socks5://127.0.0.1:7890",
		"NO_PROXY=localhost",
		"HOME=/Users/me",
	})
	joined := strings.Join(got, "\n")
	for _, banned := range []string{"HTTP_PROXY=", "https_proxy=", "ALL_PROXY=", "NO_PROXY=localhost"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("expected %q scrubbed from env, got %v", banned, got)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "HOME=/Users/me") {
		t.Fatalf("expected non-proxy vars preserved, got %v", got)
	}
	if !strings.Contains(joined, "NO_PROXY=*") {
		t.Fatalf("expected NO_PROXY=* override, got %v", got)
	}
}

func TestHealLoopRestartsOnPublicProbeFailure(t *testing.T) {
	m := NewManager(18093)
	m.lookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	m.healInterval = 40 * time.Millisecond
	m.healFailThreshold = 1

	var mu sync.Mutex
	starts := 0
	m.run = func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		output := "2026-07-10 INF Registered tunnel connection connIndex=0 location=SJC\n"
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5")
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, io.NopCloser(strings.NewReader(output)), nil
	}
	m.probePublic = func(ctx context.Context, publicURL string) error {
		return fmt.Errorf("public health returned HTTP 530")
	}

	state, err := m.Start(Settings{
		Mode:         ModeCustom,
		CustomDomain: "api.lucadesign.uk",
		UIDomain:     "user.lucadesign.uk",
		ConfigFile:   "/tmp/tunnel-config.yml",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if state.Status != StatusRunning {
		t.Fatalf("expected running, got %q", state.Status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := starts
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	mu.Lock()
	n := starts
	mu.Unlock()
	if n < 2 {
		t.Fatalf("expected heal loop to restart tunnel, starts=%d", n)
	}
	m.Stop()
}
