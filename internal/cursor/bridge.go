package cursor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

// Bridge manages the local OpenAI-compatible Cursor gRPC bridge subprocess.
type Bridge struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	port      int
	tokenFile string
	repoRoot  string
}

func NewBridge(repoRoot string) *Bridge {
	return &Bridge{repoRoot: repoRoot}
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
	if b.tokenFile != "" {
		return b.tokenFile
	}
	path := DefaultTokenFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	b.tokenFile = path
	return b.tokenFile
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

// EnsureRunning writes the access token and starts the bridge if needed.
func (b *Bridge) EnsureRunning(accessToken string) (int, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return 0, fmt.Errorf("cursor access token is empty")
	}
	if err := b.writeToken(accessToken); err != nil {
		return 0, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cmd != nil && b.cmd.Process != nil && b.port > 0 {
		if b.cmd.ProcessState == nil {
			return b.port, nil
		}
	}

	if err := b.startLocked(); err != nil {
		return 0, err
	}
	return b.port, nil
}

func (b *Bridge) startLocked() error {
	bun, err := b.findBun()
	if err != nil {
		return err
	}
	bridgeDir := b.bridgeDir()
	if bridgeDir == "" {
		return fmt.Errorf("cursor bridge sources missing (expected App Resources/cursor-bridge or scripts/cursor-bridge)")
	}
	if _, err := os.Stat(filepath.Join(bridgeDir, "standalone.ts")); err != nil {
		return fmt.Errorf("cursor bridge sources missing at %s", bridgeDir)
	}

	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}

	cmd := exec.Command(bun, "run", "standalone.ts")
	cmd.Dir = bridgeDir
	cmd.Env = append(os.Environ(),
		"CURSOR_TOKEN_FILE="+b.TokenFilePath(),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
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

	select {
	case port := <-readyCh:
		b.cmd = cmd
		b.port = port
		return nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return err
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("timed out waiting for cursor bridge to start")
	}
}

func (b *Bridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	b.cmd = nil
	b.port = 0
}

func (b *Bridge) BaseURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/v1", b.port)
}

// FindRepoRoot locates the repository root containing scripts/cursor-bridge,
// or returns "" when running from a packaged App that embeds the bridge.
func FindRepoRoot() string {
	if root := packaged.RepoRoot(); root != "" {
		return root
	}
	return ""
}
