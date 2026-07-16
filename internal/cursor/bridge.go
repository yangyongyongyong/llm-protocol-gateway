package cursor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

const (
	BridgeStatusStopped    = "stopped"
	BridgeStatusStarting   = "starting"
	BridgeStatusHealthy    = "healthy"
	BridgeStatusUnhealthy  = "unhealthy"
	BridgeStatusRestarting = "restarting"

	defaultProbeTimeout  = 2 * time.Second
	defaultWatchInterval = 45 * time.Second
	defaultStartTimeout  = 60 * time.Second
)

// Status is the live cursor-bridge subprocess snapshot (runtime-only).
type Status struct {
	Status    string `json:"status"`
	Port      int    `json:"port,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Message   string `json:"message,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// Bridge manages the local OpenAI-compatible Cursor gRPC bridge subprocess.
type Bridge struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	port      int
	tokenFile string
	repoRoot  string

	status    string
	message   string
	startedAt time.Time
	checkedAt time.Time
	exitCh    chan struct{}

	probeTimeout  time.Duration
	watchInterval time.Duration
	startTimeout  time.Duration
	httpClient    *http.Client

	watchOnce sync.Once
	watchStop chan struct{}
}

func NewBridge(repoRoot string) *Bridge {
	return &Bridge{
		repoRoot:      repoRoot,
		status:        BridgeStatusStopped,
		probeTimeout:  defaultProbeTimeout,
		watchInterval: defaultWatchInterval,
		startTimeout:  defaultStartTimeout,
	}
}

// DefaultTokenFilePath returns the user-level Cursor access-token path
// (~/Library/Application Support/llm-protocol-gateway/cursor/access-token on macOS).
// It does not create the directory.
func DefaultTokenFilePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
	}
	return filepath.Join(configDir, "llm-protocol-gateway", "cursor", "access-token")
}

func (b *Bridge) TokenFilePath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokenFilePathLocked()
}

func (b *Bridge) tokenFilePathLocked() string {
	if b.tokenFile != "" {
		return b.tokenFile
	}
	path := DefaultTokenFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	b.tokenFile = path
	return path
}

func (b *Bridge) writeToken(token string) error {
	path := b.TokenFilePath()
	return os.WriteFile(path, []byte(strings.TrimSpace(token)), 0o600)
}

func (b *Bridge) bridgeDir() string {
	if dir := packaged.CursorBridgeDir(); dir != "" {
		return dir
	}
	if b.repoRoot != "" {
		return filepath.Join(b.repoRoot, "scripts", "cursor-bridge")
	}
	return ""
}

func (b *Bridge) findBun() (string, error) {
	path, err := packaged.Bun()
	if err != nil {
		return "", fmt.Errorf("bun not found: install bun (https://bun.sh) or use a packaged App: %w", err)
	}
	return path, nil
}

func (b *Bridge) client() *http.Client {
	timeout := b.probeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	if b.httpClient != nil {
		return b.httpClient
	}
	return &http.Client{Timeout: timeout}
}

// Snapshot returns the current bridge status for UI / diagnostics.
func (b *Bridge) Snapshot() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := Status{
		Status:  b.status,
		Port:    b.port,
		Message: b.message,
	}
	if out.Status == "" {
		out.Status = BridgeStatusStopped
	}
	if b.cmd != nil && b.cmd.Process != nil {
		out.PID = b.cmd.Process.Pid
	}
	if !b.startedAt.IsZero() {
		out.StartedAt = b.startedAt.Format(time.RFC3339)
	}
	if !b.checkedAt.IsZero() {
		out.CheckedAt = b.checkedAt.Format(time.RFC3339)
	}
	return out
}

// EnsureRunning writes the access token and starts the bridge if needed.
// If a process is already tracked, it is HTTP-probed first and restarted when unhealthy.
// started is true when a new bridge process was launched.
func (b *Bridge) EnsureRunning(accessToken string) (port int, started bool, err error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return 0, false, fmt.Errorf("cursor access token is empty")
	}
	if err := b.writeToken(accessToken); err != nil {
		return 0, false, err
	}

	b.mu.Lock()
	if b.processAliveLocked() {
		port = b.port
		b.mu.Unlock()
		if err := b.probePort(port); err == nil {
			b.mu.Lock()
			b.status = BridgeStatusHealthy
			b.message = ""
			b.checkedAt = time.Now()
			port = b.port
			b.mu.Unlock()
			return port, false, nil
		}
		b.mu.Lock()
		b.status = BridgeStatusRestarting
		b.message = "health probe failed; restarting"
		b.stopLocked()
		if err := b.startLocked(); err != nil {
			b.mu.Unlock()
			return 0, false, err
		}
		port = b.port
		b.mu.Unlock()
		return port, true, nil
	}

	if err := b.startLocked(); err != nil {
		b.mu.Unlock()
		return 0, false, err
	}
	port = b.port
	b.mu.Unlock()
	return port, true, nil
}

func (b *Bridge) processAliveLocked() bool {
	if b.cmd == nil || b.cmd.Process == nil || b.port <= 0 || b.exitCh == nil {
		return false
	}
	select {
	case <-b.exitCh:
		return false
	default:
		return true
	}
}

func (b *Bridge) probePort(port int) error {
	if port <= 0 {
		return fmt.Errorf("invalid bridge port")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	resp, err := b.client().Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bridge probe HTTP %d", resp.StatusCode)
	}
	return nil
}

func (b *Bridge) startLocked() error {
	bun, err := b.findBun()
	if err != nil {
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}
	bridgeDir := b.bridgeDir()
	if bridgeDir == "" {
		err := fmt.Errorf("cursor bridge sources missing (expected App Resources/cursor-bridge or scripts/cursor-bridge)")
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}
	if _, err := os.Stat(filepath.Join(bridgeDir, "standalone.ts")); err != nil {
		err := fmt.Errorf("cursor bridge sources missing at %s", bridgeDir)
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}

	b.stopLocked()
	b.status = BridgeStatusStarting
	b.message = ""

	cmd := exec.Command(bun, "run", "standalone.ts")
	cmd.Dir = bridgeDir
	cmd.Env = append(os.Environ(),
		"CURSOR_TOKEN_FILE="+b.tokenFilePathLocked(),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}
	if err := cmd.Start(); err != nil {
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}

	readyCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var payload struct {
				Event   string `json:"event"`
				Port    int    `json:"port"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				continue
			}
			if payload.Event == "ready" && payload.Port > 0 {
				readyCh <- payload.Port
				return
			}
			if payload.Event == "error" {
				errCh <- fmt.Errorf("%s", payload.Message)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- fmt.Errorf("cursor bridge exited before ready")
	}()
	go func() {
		data, _ := io.ReadAll(stderr)
		if len(strings.TrimSpace(string(data))) > 0 {
			_, _ = os.Stderr.WriteString("cursor-bridge: " + string(data))
		}
	}()

	startTimeout := b.startTimeout
	if startTimeout <= 0 {
		startTimeout = defaultStartTimeout
	}

	select {
	case port := <-readyCh:
		exitCh := make(chan struct{})
		b.cmd = cmd
		b.port = port
		b.exitCh = exitCh
		b.startedAt = time.Now()
		b.checkedAt = time.Now()
		b.status = BridgeStatusHealthy
		b.message = ""
		go b.reap(cmd, exitCh)
		return nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	case <-time.After(startTimeout):
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		err := fmt.Errorf("timed out waiting for cursor bridge to start")
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		return err
	}
}

func (b *Bridge) reap(cmd *exec.Cmd, exitCh chan struct{}) {
	_ = cmd.Wait()
	close(exitCh)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd == cmd {
		b.cmd = nil
		b.port = 0
		b.exitCh = nil
		if b.status != BridgeStatusRestarting && b.status != BridgeStatusStarting {
			b.status = BridgeStatusStopped
			b.message = "bridge process exited"
			b.checkedAt = time.Now()
		}
	}
}

func (b *Bridge) stopLocked() {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		// Wait is owned by reap goroutine; just clear refs after signaling kill.
	}
	b.cmd = nil
	b.port = 0
	b.exitCh = nil
}

func (b *Bridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
	b.status = BridgeStatusStopped
	b.message = ""
	b.checkedAt = time.Now()
	if b.watchStop != nil {
		close(b.watchStop)
		b.watchStop = nil
	}
}

func (b *Bridge) BaseURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/v1", b.port)
}

// StartHealthWatch periodically probes a running bridge and restarts it when hung.
// Safe to call multiple times; only the first call starts the loop.
func (b *Bridge) StartHealthWatch(ctx context.Context) {
	b.watchOnce.Do(func() {
		b.mu.Lock()
		stop := make(chan struct{})
		b.watchStop = stop
		interval := b.watchInterval
		b.mu.Unlock()
		if interval <= 0 {
			interval = defaultWatchInterval
		}
		go b.watchLoop(ctx, stop, interval)
	})
}

func (b *Bridge) watchLoop(ctx context.Context, stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			b.healthCheckAndRepair()
		}
	}
}

func (b *Bridge) healthCheckAndRepair() {
	b.mu.Lock()
	if !b.processAliveLocked() {
		// Lazy-start: nothing to repair until EnsureRunning launches a process.
		if b.status == BridgeStatusHealthy || b.status == BridgeStatusUnhealthy || b.status == BridgeStatusRestarting {
			b.status = BridgeStatusStopped
			b.message = "bridge process not running"
			b.checkedAt = time.Now()
		}
		b.mu.Unlock()
		return
	}
	port := b.port
	tokenPath := b.tokenFilePathLocked()
	b.mu.Unlock()

	if err := b.probePort(port); err == nil {
		b.mu.Lock()
		b.status = BridgeStatusHealthy
		b.message = ""
		b.checkedAt = time.Now()
		b.mu.Unlock()
		return
	} else {
		b.mu.Lock()
		b.status = BridgeStatusRestarting
		b.message = fmt.Sprintf("health probe failed: %v", err)
		b.checkedAt = time.Now()
		b.stopLocked()
		b.mu.Unlock()
	}

	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		b.mu.Lock()
		b.status = BridgeStatusUnhealthy
		b.message = "health probe failed and access token unavailable for restart"
		b.checkedAt = time.Now()
		b.mu.Unlock()
		return
	}
	if err := b.writeToken(string(tokenBytes)); err != nil {
		b.mu.Lock()
		b.status = BridgeStatusUnhealthy
		b.message = err.Error()
		b.checkedAt = time.Now()
		b.mu.Unlock()
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startLocked(); err != nil {
		// startLocked already set unhealthy status/message
		return
	}
}

// FindRepoRoot locates the repository root containing scripts/cursor-bridge,
// or returns "" when running from a packaged App that embeds the bridge.
func FindRepoRoot() string {
	if root := packaged.RepoRoot(); root != "" {
		return root
	}
	return ""
}
