// Package tunnel manages the lifecycle of a cloudflared process that exposes the
// local gateway publicly. It supports two modes:
//
//   - "quick": runs `cloudflared tunnel --url http://127.0.0.1:<port>` and parses
//     the assigned https://*.trycloudflare.com URL from cloudflared's output. This
//     path is fully implemented and requires no Cloudflare account.
//   - "custom": exposes a user-owned Cloudflare domain (e.g. lucadesign.uk) via a
//     named tunnel. Token-based runs are automated: given a tunnel token (created
//     once in the Cloudflare Zero Trust dashboard, where the public hostname ->
//     http://127.0.0.1:<port> ingress is also configured), this runs
//     `cloudflared tunnel run --token <token>` and reports the configured custom
//     domain as the live public URL. Without a token, custom mode returns a clear,
//     actionable error describing the one-time setup instead of faking a URL.
package tunnel

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

// errQUICEdgeDial signals that the default QUIC transport could not reach
// Cloudflare edge; callers may retry with --protocol http2.
var errQUICEdgeDial = errors.New("quic edge dial failed")

//go:embed quick_tunnel_config.yml
var quickTunnelConfig []byte

var (
	quickConfigPath string
	quickConfigOnce sync.Once
	quickConfigErr  error
)

// Mode selects how the public tunnel is established.
type Mode string

const (
	// ModeQuick uses a Cloudflare quick tunnel with a random trycloudflare.com URL.
	ModeQuick Mode = "quick"
	// ModeCustom uses a named tunnel bound to a user-owned Cloudflare domain.
	ModeCustom Mode = "custom"
)

// Status describes the current tunnel state for the UI.
type Status string

const (
	StatusStopped  Status = "stopped"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusError    Status = "error"
)

// Settings is the self-contained public-access configuration owned by this
// package. It is intentionally decoupled from domain.PublicAccessSettings so the
// tunnel lifecycle can be reasoned about in isolation; the gateway layer maps
// between the two.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Mode is "quick" or "custom".
	Mode Mode `json:"mode"`
	// CustomDomain is the user-owned hostname for model API traffic in custom
	// mode, e.g. "gateway.lucadesign.uk".
	CustomDomain string `json:"customDomain,omitempty"`
	// UIDomain is the user-owned hostname for the management UI in custom mode,
	// e.g. "console.lucadesign.uk". Must differ from CustomDomain.
	UIDomain string `json:"uiDomain,omitempty"`
	// TunnelName is the cloudflared named-tunnel to run in custom mode.
	TunnelName string `json:"tunnelName,omitempty"`
	// TunnelToken is an optional cloudflared tunnel token (token-based run).
	TunnelToken string `json:"tunnelToken,omitempty"`
	// CredentialsFile is an optional path to the tunnel credentials JSON.
	CredentialsFile string `json:"credentialsFile,omitempty"`
	// ConfigFile is the cloudflared YAML config for named-tunnel runs.
	ConfigFile string `json:"configFile,omitempty"`
}

// State is a snapshot of the manager for status responses.
type State struct {
	Status      Status `json:"status"`
	Mode        Mode   `json:"mode"`
	PublicURL   string `json:"publicUrl"`
	UIPublicURL string `json:"uiPublicUrl,omitempty"`
	Message     string `json:"message"`
	StartedAt   string `json:"startedAt,omitempty"`
	PID         int    `json:"pid,omitempty"`
}

// runnerFunc launches cloudflared. It is a field so tests can inject a fake.
type runnerFunc func(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error)

// Manager owns a single cloudflared process at a time.
type Manager struct {
	mu sync.Mutex

	localPort int
	setup     *CloudflareSetup

	cmd         *exec.Cmd
	cancel      context.CancelFunc
	done        chan struct{}
	status      Status
	mode        Mode
	publicURL   string
	uiPublicURL string
	message     string
	startedAt   time.Time
	pid         int

	// lastSettings is retained so a dead/zombie tunnel can be restarted after
	// macOS proxy toggles (Shadowrocket etc.) drop the edge connection while
	// the local cloudflared process is still alive.
	lastSettings Settings
	wantRunning  bool

	healCancel context.CancelFunc
	healDone   chan struct{}

	// lookPath resolves the cloudflared binary; overridable in tests.
	lookPath func(string) (string, error)
	// run launches cloudflared; overridable in tests.
	run runnerFunc
	// probePublic checks whether a public tunnel URL still reaches this gateway.
	// Overridable in tests. Default uses a direct (no-proxy) HTTP client.
	probePublic func(ctx context.Context, publicURL string) error
	// healInterval / healFailThreshold control the background health loop.
	healInterval      time.Duration
	healFailThreshold int
}

// NewManager creates a manager that will forward public traffic to
// http://127.0.0.1:<localPort>.
func NewManager(localPort int) *Manager {
	m := &Manager{
		localPort:         localPort,
		status:            StatusStopped,
		message:           "Public access is stopped.",
		lookPath:          lookPathCloudflared,
		setup:             NewCloudflareSetup(),
		healInterval:      15 * time.Second,
		healFailThreshold: 2,
	}
	m.run = m.defaultRun
	m.probePublic = m.defaultProbePublic
	return m
}

func lookPathCloudflared(name string) (string, error) {
	if name == "cloudflared" {
		return packaged.Cloudflared()
	}
	return exec.LookPath(name)
}

// Cloudflare returns the Cloudflare setup helper used for browser login flows.
func (m *Manager) Cloudflare() *CloudflareSetup {
	if m.setup == nil {
		m.setup = NewCloudflareSetup()
	}
	return m.setup
}

// quickURLPattern matches the trycloudflare.com URL cloudflared prints on stderr.
var quickURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// customReadyPattern matches a line indicating a named tunnel has registered its
// connections with Cloudflare's edge, i.e. the tunnel is live.
var customReadyPattern = regexp.MustCompile(`(?i)(Registered tunnel connection|Connection [0-9a-f-]+ registered|Updated to new configuration)`)

// cloudflaredEdgeDialFailPattern captures common edge-connectivity failures so
// the UI can show a clearer message than a generic start timeout.
var cloudflaredEdgeDialFailPattern = regexp.MustCompile(`(?i)(failed to dial to edge|Unable to establish connection with Cloudflare edge|DialContext error|timeout: no recent network activity|i/o timeout)`)

// cloudflaredQUICDialFailPattern matches QUIC-specific edge dial failures that
// warrant an automatic retry with --protocol http2 (TCP 7844).
var cloudflaredQUICDialFailPattern = regexp.MustCompile(`(?i)(failed to dial to edge with quic|dial to edge with quic|Initial protocol quic[\s\S]{0,400}failed to dial)`)

const (
	protocolAuto  = ""
	protocolHTTP2 = "http2"
)

func withCloudflaredProtocol(args []string, protocol string) []string {
	if protocol == "" || len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args[0], "--protocol", protocol)
	out = append(out, args[1:]...)
	return out
}

func isQUICDialFailure(line string) bool {
	if line == "" {
		return false
	}
	if cloudflaredQUICDialFailPattern.MatchString(line) {
		return true
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "quic") && cloudflaredEdgeDialFailPattern.MatchString(line)
}

func (m *Manager) defaultRun(ctx context.Context, args ...string) (*exec.Cmd, io.ReadCloser, error) {
	bin, err := m.lookPath("cloudflared")
	if err != nil {
		return nil, nil, fmt.Errorf("cloudflared not found on PATH: install it (brew install cloudflared) and retry: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// Never inherit macOS / Shadowrocket HTTP(S)/SOCKS proxies. cloudflared must
	// dial Cloudflare edge directly; routing QUIC/HTTP2 through a local proxy
	// commonly leaves a zombie process that still looks "running" while public
	// hostnames return Cloudflare 530.
	cmd.Env = scrubProxyEnv(os.Environ())
	// cloudflared logs (including the quick-tunnel URL) go to stderr.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	// Merge stdout+stderr so URL parsing works regardless of stream.
	return cmd, struct {
		io.Reader
		io.Closer
	}{io.MultiReader(stderr, stdout), stderr}, nil
}

// scrubProxyEnv removes proxy-related variables so child processes (cloudflared)
// ignore system / app proxies such as Shadowrocket on 127.0.0.1:7890.
func scrubProxyEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy",
			"socks_proxy", "socks5_proxy", "socks5h_proxy":
			continue
		default:
			out = append(out, entry)
		}
	}
	// Belt-and-suspenders: even if a library reads proxy settings elsewhere,
	// force "no proxy" for anything that still honors NO_PROXY.
	out = append(out, "NO_PROXY=*", "no_proxy=*")
	return out
}

// Available reports whether the cloudflared binary can be located.
func (m *Manager) Available() error {
	if _, err := m.lookPath("cloudflared"); err != nil {
		return fmt.Errorf("cloudflared not found on PATH: install it (e.g. `brew install cloudflared`) to enable public access")
	}
	return nil
}

// Snapshot returns the current manager state.
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() State {
	state := State{
		Status:      m.status,
		Mode:        m.mode,
		PublicURL:   m.publicURL,
		UIPublicURL: m.uiPublicURL,
		Message:     m.message,
		PID:         m.pid,
	}
	if !m.startedAt.IsZero() {
		state.StartedAt = m.startedAt.UTC().Format(time.RFC3339)
	}
	return state
}

// Start launches cloudflared for the given settings. It stops any existing
// process first. For quick mode it blocks briefly to capture the assigned URL.
func (m *Manager) Start(settings Settings) (State, error) {
	if err := m.Available(); err != nil {
		m.setError(err.Error())
		return m.Snapshot(), err
	}

	m.mu.Lock()
	m.lastSettings = settings
	m.wantRunning = true
	m.mu.Unlock()

	state, err := m.startWithSettings(settings)
	if err == nil && state.Status == StatusRunning {
		m.startHealLoop()
	}
	return state, err
}

func (m *Manager) startWithSettings(settings Settings) (State, error) {
	mode := settings.Mode
	if mode == "" {
		mode = ModeQuick
	}
	if mode == ModeCustom {
		return m.startCustom(settings)
	}
	return m.startQuick()
}

func isolatedCloudflaredConfig() (string, error) {
	quickConfigOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "llm-protocol-gateway-quick-cloudflared.yml")
		quickConfigErr = os.WriteFile(path, quickTunnelConfig, 0o644)
		if quickConfigErr == nil {
			quickConfigPath = path
		}
	})
	return quickConfigPath, quickConfigErr
}

func (m *Manager) startQuick() (State, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d", m.localPort)
	configPath, err := isolatedCloudflaredConfig()
	if err != nil {
		m.setError(err.Error())
		return m.Snapshot(), err
	}
	baseArgs := []string{"tunnel", "--config", configPath, "--no-autoupdate", "--url", url}

	state, err := m.startQuickWithProtocol(baseArgs, protocolAuto)
	if errors.Is(err, errQUICEdgeDial) {
		m.mu.Lock()
		m.status = StatusStarting
		m.message = "QUIC 连接 Cloudflare 边缘失败，正在自动改用 HTTP/2…"
		m.mu.Unlock()
		return m.startQuickWithProtocol(baseArgs, protocolHTTP2)
	}
	return state, err
}

func (m *Manager) startQuickWithProtocol(baseArgs []string, protocol string) (State, error) {
	m.stopProcess()

	ctx, cancel := context.WithCancel(context.Background())
	args := withCloudflaredProtocol(baseArgs, protocol)
	cmd, out, err := m.run(ctx, args...)
	if err != nil {
		cancel()
		m.setError(err.Error())
		return m.Snapshot(), err
	}

	done := make(chan struct{})
	m.mu.Lock()
	m.cmd = cmd
	m.cancel = cancel
	m.done = done
	m.status = StatusStarting
	m.mode = ModeQuick
	m.publicURL = ""
	m.uiPublicURL = ""
	if protocol == protocolHTTP2 {
		m.message = "Starting Cloudflare quick tunnel (HTTP/2)..."
	} else {
		m.message = "Starting Cloudflare quick tunnel..."
	}
	m.startedAt = time.Now()
	if cmd.Process != nil {
		m.pid = cmd.Process.Pid
	}
	m.mu.Unlock()

	urlCh := make(chan string, 1)
	diagCh := make(chan string, 4)
	go m.scanForQuickURL(out, urlCh, diagCh)
	go m.waitForExit(cmd, cancel, done)

	abortOnQUIC := protocol == protocolAuto
	outcome, payload := waitQuickURL(urlCh, done, diagCh, 30*time.Second, abortOnQUIC)
	switch outcome {
	case waitOutcomeReady:
		m.mu.Lock()
		m.publicURL = payload
		m.status = StatusRunning
		if protocol == protocolHTTP2 {
			m.message = "Cloudflare quick tunnel is live (HTTP/2)."
		} else {
			m.message = "Cloudflare quick tunnel is live."
		}
		m.mu.Unlock()
		return m.Snapshot(), nil
	case waitOutcomeQUICFail:
		m.stopProcess()
		err := fmt.Errorf("%w: %s", errQUICEdgeDial, payload)
		m.setError(err.Error())
		return m.Snapshot(), err
	case waitOutcomeExited:
		err := fmt.Errorf("cloudflared exited before reporting a trycloudflare.com URL")
		m.setError(err.Error())
		return m.Snapshot(), err
	default:
		err := fmt.Errorf("%s", quickTunnelTimeoutMessage(payload))
		m.stopProcess()
		m.setError(err.Error())
		return m.Snapshot(), err
	}
}

// startCustom launches a named tunnel bound to a user-owned Cloudflare domain.
//
// It supports the token-based flow, which is the only path that can be fully
// automated locally: the user creates the tunnel once in the Cloudflare Zero
// Trust dashboard (which also configures the public hostname -> local ingress)
// and provides the resulting token. We then run
// `cloudflared tunnel run --token <token>` and wait for the tunnel to register
// its edge connections before reporting the custom domain as live.
//
// Without a token we cannot safely automate `login`/`create`/`route dns`
// (interactive browser auth + DNS writes), so we return actionable setup
// instructions instead of faking a working URL.
func (m *Manager) startCustom(settings Settings) (State, error) {
	domain := strings.TrimSpace(settings.CustomDomain)
	uiDomain := strings.TrimSpace(settings.UIDomain)
	if domain == "" && uiDomain == "" {
		err := fmt.Errorf("at least one of api domain or ui domain is required for custom mode")
		m.setError(err.Error())
		return m.Snapshot(), err
	}
	if domain != "" && uiDomain != "" && strings.EqualFold(uiDomain, domain) {
		err := fmt.Errorf("api domain and ui domain must be different (got %s for both)", domain)
		m.setError(err.Error())
		return m.Snapshot(), err
	}
	// Token/config runs report PublicURL from the primary hostname (API preferred).
	primary := domain
	if primary == "" {
		primary = uiDomain
	}

	token := strings.TrimSpace(settings.TunnelToken)
	configFile := strings.TrimSpace(settings.ConfigFile)
	if token != "" {
		return m.startTokenTunnel(settings, primary, uiDomainIfDistinct(primary, uiDomain), token)
	}
	if configFile != "" {
		return m.startConfigTunnel(settings, primary, uiDomainIfDistinct(primary, uiDomain), configFile)
	}

	msg := fmt.Sprintf(
		"Custom-domain mode needs Cloudflare authorization. Click “连接 Cloudflare 并绑定” to open the official login page, "+
			"then this gateway will create the tunnel and route API %s / UI %s to http://127.0.0.1:%d automatically.",
		uiDomainOrPlaceholder(domain), uiDomainOrPlaceholder(uiDomain), m.localPort,
	)
	m.mu.Lock()
	m.status = StatusError
	m.mode = ModeCustom
	m.publicURL = ""
	m.uiPublicURL = ""
	m.message = msg
	m.mu.Unlock()
	return m.Snapshot(), fmt.Errorf("%s", msg)
}

func uiDomainOrPlaceholder(uiDomain string) string {
	if strings.TrimSpace(uiDomain) == "" {
		return "(unset)"
	}
	return uiDomain
}

func uiDomainIfDistinct(primary, uiDomain string) string {
	uiDomain = strings.TrimSpace(uiDomain)
	primary = strings.TrimSpace(primary)
	if uiDomain == "" || strings.EqualFold(uiDomain, primary) {
		return ""
	}
	return uiDomain
}

func customPublicURL(domain string) string {
	publicURL := domain
	if !strings.HasPrefix(publicURL, "http://") && !strings.HasPrefix(publicURL, "https://") {
		publicURL = "https://" + publicURL
	}
	return publicURL
}

// startConfigTunnel runs a named tunnel using a generated cloudflared config file.
func (m *Manager) startConfigTunnel(settings Settings, domain, uiDomain, configFile string) (State, error) {
	_ = settings
	baseArgs := []string{"tunnel", "--config", configFile, "--no-autoupdate", "run"}
	exitMsg := "cloudflared exited before the named tunnel registered. Re-authorize Cloudflare and try again."
	state, err := m.startNamedWithProtocol(domain, uiDomain, baseArgs, protocolAuto, exitMsg)
	if errors.Is(err, errQUICEdgeDial) {
		m.mu.Lock()
		m.status = StatusStarting
		m.message = "QUIC 连接 Cloudflare 边缘失败，正在自动改用 HTTP/2…"
		m.mu.Unlock()
		return m.startNamedWithProtocol(domain, uiDomain, baseArgs, protocolHTTP2, exitMsg)
	}
	return state, err
}

// startTokenTunnel runs a token-based named tunnel and waits for it to register
// with the Cloudflare edge. The public URL is the user's configured custom domain
// (ingress is defined dashboard-side), not something parsed from output.
func (m *Manager) startTokenTunnel(settings Settings, domain, uiDomain, token string) (State, error) {
	_ = settings
	configPath, err := isolatedCloudflaredConfig()
	if err != nil {
		m.setError(err.Error())
		return m.Snapshot(), err
	}
	baseArgs := []string{"tunnel", "--config", configPath, "--no-autoupdate", "run", "--token", token}
	exitMsg := "cloudflared exited before the named tunnel registered. Check the token and dashboard ingress."
	state, err := m.startNamedWithProtocol(domain, uiDomain, baseArgs, protocolAuto, exitMsg)
	if errors.Is(err, errQUICEdgeDial) {
		m.mu.Lock()
		m.status = StatusStarting
		m.message = "QUIC 连接 Cloudflare 边缘失败，正在自动改用 HTTP/2…"
		m.mu.Unlock()
		return m.startNamedWithProtocol(domain, uiDomain, baseArgs, protocolHTTP2, exitMsg)
	}
	return state, err
}

func (m *Manager) startNamedWithProtocol(domain, uiDomain string, baseArgs []string, protocol, exitMsg string) (State, error) {
	m.stopProcess()

	publicURL := customPublicURL(domain)
	uiPublicURL := ""
	if strings.TrimSpace(uiDomain) != "" {
		uiPublicURL = customPublicURL(uiDomain)
	}
	ctx, cancel := context.WithCancel(context.Background())
	args := withCloudflaredProtocol(baseArgs, protocol)
	cmd, out, err := m.run(ctx, args...)
	if err != nil {
		cancel()
		m.setError(err.Error())
		return m.Snapshot(), err
	}

	done := make(chan struct{})
	m.mu.Lock()
	m.cmd = cmd
	m.cancel = cancel
	m.done = done
	m.status = StatusStarting
	m.mode = ModeCustom
	m.publicURL = ""
	m.uiPublicURL = ""
	if protocol == protocolHTTP2 {
		m.message = fmt.Sprintf("Connecting named tunnel for %s (HTTP/2)...", domain)
	} else {
		m.message = fmt.Sprintf("Connecting named tunnel for %s...", domain)
	}
	m.startedAt = time.Now()
	if cmd.Process != nil {
		m.pid = cmd.Process.Pid
	}
	m.mu.Unlock()

	readyCh := make(chan struct{}, 1)
	diagCh := make(chan string, 4)
	go m.scanForReady(out, readyCh, diagCh)
	go m.waitForExit(cmd, cancel, done)

	abortOnQUIC := protocol == protocolAuto
	outcome, diag := waitNamedReady(readyCh, done, diagCh, 60*time.Second, abortOnQUIC)
	switch outcome {
	case waitOutcomeReady:
		m.mu.Lock()
		m.publicURL = publicURL
		m.uiPublicURL = uiPublicURL
		m.status = StatusRunning
		if protocol == protocolHTTP2 {
			if uiPublicURL != "" {
				m.message = fmt.Sprintf("Named tunnel is live at API %s / UI %s (HTTP/2).", publicURL, uiPublicURL)
			} else {
				m.message = fmt.Sprintf("Named tunnel is live at %s (HTTP/2).", publicURL)
			}
		} else if uiPublicURL != "" {
			m.message = fmt.Sprintf("Named tunnel is live at API %s / UI %s.", publicURL, uiPublicURL)
		} else {
			m.message = fmt.Sprintf("Named tunnel is live at %s.", publicURL)
		}
		m.mu.Unlock()
		return m.Snapshot(), nil
	case waitOutcomeQUICFail:
		m.stopProcess()
		err := fmt.Errorf("%w: %s", errQUICEdgeDial, diag)
		m.setError(err.Error())
		return m.Snapshot(), err
	case waitOutcomeExited:
		m.mu.Lock()
		if m.status != StatusError {
			m.status = StatusError
			m.message = exitMsg
		}
		m.mu.Unlock()
		return m.Snapshot(), fmt.Errorf("named tunnel failed to start")
	default:
		err := fmt.Errorf("%s", namedTunnelTimeoutMessage(diag))
		m.stopProcess()
		m.setError(err.Error())
		return m.Snapshot(), err
	}
}

type waitOutcome int

const (
	waitOutcomeReady waitOutcome = iota
	waitOutcomeExited
	waitOutcomeTimeout
	waitOutcomeQUICFail
)

func waitQuickURL(urlCh <-chan string, done <-chan struct{}, diagCh <-chan string, timeout time.Duration, abortOnQUIC bool) (waitOutcome, string) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var lastDiag string
	for {
		select {
		case publicURL := <-urlCh:
			return waitOutcomeReady, publicURL
		case <-done:
			return waitOutcomeExited, lastDiag
		case line := <-diagCh:
			if line != "" {
				lastDiag = line
			}
			if abortOnQUIC && isQUICDialFailure(line) {
				return waitOutcomeQUICFail, lastDiag
			}
		case <-timer.C:
			if last := drainDiag(diagCh); last != "" {
				lastDiag = last
			}
			if abortOnQUIC && isQUICDialFailure(lastDiag) {
				return waitOutcomeQUICFail, lastDiag
			}
			return waitOutcomeTimeout, lastDiag
		}
	}
}

func waitNamedReady(readyCh <-chan struct{}, done <-chan struct{}, diagCh <-chan string, timeout time.Duration, abortOnQUIC bool) (waitOutcome, string) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var lastDiag string
	for {
		select {
		case <-readyCh:
			return waitOutcomeReady, lastDiag
		case <-done:
			return waitOutcomeExited, lastDiag
		case line := <-diagCh:
			if line != "" {
				lastDiag = line
			}
			if abortOnQUIC && isQUICDialFailure(line) {
				return waitOutcomeQUICFail, lastDiag
			}
		case <-timer.C:
			if last := drainDiag(diagCh); last != "" {
				lastDiag = last
			}
			if abortOnQUIC && isQUICDialFailure(lastDiag) {
				return waitOutcomeQUICFail, lastDiag
			}
			return waitOutcomeTimeout, lastDiag
		}
	}
}

func (m *Manager) scanForQuickURL(out io.ReadCloser, urlCh chan<- string, diagCh chan<- string) {
	defer out.Close()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		if !sent {
			if match := quickURLPattern.FindString(line); match != "" {
				sent = true
				urlCh <- match
			}
		}
		if cloudflaredEdgeDialFailPattern.MatchString(line) {
			select {
			case diagCh <- strings.TrimSpace(line):
			default:
			}
		}
	}
}

func (m *Manager) scanForReady(out io.ReadCloser, readyCh chan<- struct{}, diagCh chan<- string) {
	defer out.Close()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		if !sent && customReadyPattern.MatchString(line) {
			sent = true
			readyCh <- struct{}{}
		}
		if cloudflaredEdgeDialFailPattern.MatchString(line) {
			select {
			case diagCh <- strings.TrimSpace(line):
			default:
			}
		}
	}
}

func drainDiag(diagCh <-chan string) string {
	var last string
	for {
		select {
		case line := <-diagCh:
			if line != "" {
				last = line
			}
		default:
			return last
		}
	}
}

func namedTunnelTimeoutMessage(diag string) string {
	base := "连接 Cloudflare 边缘节点超时（公网隧道未就绪）"
	hint := "。本机网络可能拦截了 Cloudflare 边缘连接：请切换网络/关闭系统代理或 VPN，并放行 cloudflared（UDP/QUIC 与 TCP 7844）。App 会在 QUIC 失败时自动改试 HTTP/2"
	if diag == "" {
		return base + hint
	}
	return base + "：" + diag + hint
}

func quickTunnelTimeoutMessage(diag string) string {
	base := "等待 Cloudflare 临时隧道 URL 超时"
	hint := "。请检查本机网络/代理/防火墙是否放行 cloudflared（UDP/QUIC 或 TCP 7844）"
	if diag == "" {
		return base + hint
	}
	return base + "：" + diag + hint
}

func (m *Manager) waitForExit(cmd *exec.Cmd, cancel context.CancelFunc, done chan struct{}) {
	_ = cmd.Wait()
	cancel()
	close(done)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Only reflect the exit if this is still the tracked process.
	if m.cmd != cmd {
		return
	}
	if m.status == StatusRunning || m.status == StatusStarting {
		m.status = StatusStopped
		m.message = "cloudflared process exited."
		m.publicURL = ""
		m.uiPublicURL = ""
	}
	m.cmd = nil
	m.cancel = nil
	m.done = nil
	m.pid = 0
}

// stopProcess kills the current cloudflared child without clearing wantRunning
// or stopping the heal loop. Used for in-place restarts and protocol fallbacks.
func (m *Manager) stopProcess() {
	m.mu.Lock()
	cmd := m.cmd
	cancel := m.cancel
	done := m.done
	m.status = StatusStopped
	m.publicURL = ""
	m.uiPublicURL = ""
	m.message = "Public access is stopped."
	m.pid = 0
	m.startedAt = time.Time{}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}

	m.mu.Lock()
	m.cmd = nil
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()
}

// Stop terminates the running cloudflared process, if any. The waitForExit
// goroutine is the sole caller of cmd.Wait(); Stop signals it via cancel/Kill
// and waits for it to finish reaping so state is consistent on return.
func (m *Manager) Stop() State {
	m.stopHealLoop()
	m.mu.Lock()
	m.wantRunning = false
	m.mu.Unlock()
	m.stopProcess()
	return m.Snapshot()
}

// RestartIfWanted restarts the tunnel using the last successful Start settings.
// Used by the health loop when the public hostname becomes unreachable while
// the local cloudflared process is still alive (common after proxy toggles).
func (m *Manager) RestartIfWanted() (State, error) {
	m.mu.Lock()
	want := m.wantRunning
	settings := m.lastSettings
	m.mu.Unlock()
	if !want {
		return m.Snapshot(), fmt.Errorf("tunnel restart skipped: not wanted")
	}
	// Reuse startWithSettings so we do not tear down / recreate the heal loop
	// from inside healLoop itself.
	return m.startWithSettings(settings)
}

func (m *Manager) startHealLoop() {
	m.stopHealLoop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.healCancel = cancel
	m.healDone = done
	interval := m.healInterval
	threshold := m.healFailThreshold
	m.mu.Unlock()
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if threshold <= 0 {
		threshold = 2
	}
	go m.healLoop(ctx, done, interval, threshold)
}

func (m *Manager) stopHealLoop() {
	m.mu.Lock()
	cancel := m.healCancel
	done := m.healDone
	m.healCancel = nil
	m.healDone = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) healLoop(ctx context.Context, done chan struct{}, interval time.Duration, threshold int) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			want := m.wantRunning
			status := m.status
			publicURL := m.publicURL
			uiPublicURL := m.uiPublicURL
			m.mu.Unlock()
			if !want {
				failures = 0
				continue
			}
			// Process died (or never came back) while public access is still enabled.
			if status != StatusRunning && status != StatusStarting {
				failures = 0
				m.mu.Lock()
				m.message = "cloudflared 已退出，正在自动重连…"
				m.mu.Unlock()
				_, _ = m.RestartIfWanted()
				continue
			}
			if status != StatusRunning {
				continue
			}
			probeURL := strings.TrimSpace(uiPublicURL)
			if probeURL == "" {
				probeURL = strings.TrimSpace(publicURL)
			}
			if probeURL == "" {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			err := m.probePublic(probeCtx, probeURL)
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			failures++
			if failures < threshold {
				continue
			}
			failures = 0
			m.mu.Lock()
			m.message = fmt.Sprintf("公网隧道不可达（%v），正在自动重连…", err)
			m.mu.Unlock()
			_, _ = m.RestartIfWanted()
		}
	}
}

func (m *Manager) defaultProbePublic(ctx context.Context, publicURL string) error {
	healthURL := strings.TrimRight(strings.TrimSpace(publicURL), "/") + "/__health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, // never use system / env proxy for tunnel health checks
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	// Cloudflare 530 / 502 / 1033-style origin-down responses mean the named
	// tunnel process is alive locally but no longer registered with edge.
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == 530 || resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("public health returned HTTP %d", resp.StatusCode)
	}
	// Other non-OK codes (auth redirects, etc.) still prove edge connectivity.
	if resp.StatusCode < 500 {
		return nil
	}
	return fmt.Errorf("public health returned HTTP %d", resp.StatusCode)
}

func (m *Manager) setError(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = StatusError
	m.message = message
	m.publicURL = ""
	m.uiPublicURL = ""
}
