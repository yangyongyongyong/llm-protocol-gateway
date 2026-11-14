// Package desktopupdate owns the desktop app's self-update state machine.
//
// Detection is independent of the React console: the native process checks the
// release feed, compares semantic versions, downloads the platform archive,
// verifies its checksum (and, when a release signing key is embedded, its
// Ed25519 signature), and stages the complete app bundle. The console only
// reads status and asks for actions over the gateway's local-only endpoints.
package desktopupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var githubAPIBase = "https://api.github.com"

const (
	// DefaultFeedURL is a stable GitHub release asset URL: GitHub redirects it to
	// the latest non-prerelease release, so no API call is needed in steady state.
	DefaultFeedURL = "https://github.com/yangyongyongyong/llm-protocol-gateway/releases/latest/download/update.json"
	DefaultRepo    = "yangyongyongyong/llm-protocol-gateway"

	defaultCheckIntervalHours = 1
	autoInstallQuietFor       = 30 * time.Second
)

const (
	StateUnavailable = "unavailable"
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateUpToDate    = "up-to-date"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateInstalling  = "installing"
	StateRestarting  = "restarting"
	StateError       = "error"
)

// updatePublicKeyFile is the repository-owned public half of the release
// signing key. Public keys are safe to commit; the matching private key lives
// only in the GitHub Actions secret UPDATE_SIGNING_KEY.
//
//go:embed update_public_key.txt
var updatePublicKeyFile string

// SignaturePublicKey is the base64-encoded raw Ed25519 public key used to
// verify update.json.sig. Release builds may override it with -ldflags -X,
// but the bundled key makes every normal build signature-capable without extra
// environment setup.
var SignaturePublicKey = strings.TrimSpace(updatePublicKeyFile)

// Settings are local to the desktop app and are never written into GatewayState:
// they describe the App's own update behaviour, not the gateway's routing state.
type Settings struct {
	AutoCheck          bool `json:"autoCheck"`
	AutoDownload       bool `json:"autoDownload"`
	AutoInstall        bool `json:"autoInstall"`
	CheckIntervalHours int  `json:"checkIntervalHours"`
}

func DefaultSettings() Settings {
	return Settings{
		AutoCheck:          true,
		AutoDownload:       true,
		AutoInstall:        true,
		CheckIntervalHours: defaultCheckIntervalHours,
	}
}

func (s Settings) normalized() Settings {
	if s.CheckIntervalHours != 1 && s.CheckIntervalHours != 6 && s.CheckIntervalHours != 24 {
		s.CheckIntervalHours = defaultCheckIntervalHours
	}
	return s
}

// Asset is one downloadable file in a release manifest.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Release is the subset of update.json the app needs.
type Release struct {
	Version     string           `json:"version"`
	PublishedAt string           `json:"publishedAt,omitempty"`
	Notes       string           `json:"notes,omitempty"`
	ReleaseURL  string           `json:"releaseUrl,omitempty"`
	Assets      map[string]Asset `json:"assets"`

	signatureVerified bool
}

// Status is the complete state returned to the console. It deliberately has no
// pointer fields: a page can render it after the manager has moved on.
type Status struct {
	Available         bool     `json:"available"`
	State             string   `json:"state"`
	Current           string   `json:"current,omitempty"`
	Latest            string   `json:"latest,omitempty"`
	ReleaseURL        string   `json:"releaseUrl,omitempty"`
	CheckedAt         string   `json:"checkedAt,omitempty"`
	Progress          int64    `json:"progress,omitempty"`
	Total             int64    `json:"total,omitempty"`
	Error             string   `json:"error,omitempty"`
	Message           string   `json:"message,omitempty"`
	AppPath           string   `json:"appPath,omitempty"`
	Downloaded        bool     `json:"downloaded,omitempty"`
	CanInstall        bool     `json:"canInstall,omitempty"`
	SignatureRequired bool     `json:"signatureRequired,omitempty"`
	SignatureVerified bool     `json:"signatureVerified,omitempty"`
	Settings          Settings `json:"settings"`
}

// Options are explicit for tests and for the desktop shell.
type Options struct {
	Version         string
	FeedURL         string
	Repository      string
	BundlePath      string
	SettingsPath    string
	StageRoot       string
	HTTPClient      *http.Client
	PublicKeyBase64 string
}

// Manager owns the native update state machine.
type Manager struct {
	mu sync.Mutex

	version      string
	repo         string
	feedURL      string
	bundle       string
	stageRoot    string
	settingsPath string
	client       *http.Client

	settings  Settings
	state     string
	latest    *Release
	assetKey  string
	stagePath string
	progress  int64
	total     int64
	err       string
	message   string
	checkedAt time.Time

	available         bool
	started           bool
	closed            bool
	ctx               context.Context
	publicKey         ed25519.PublicKey
	publicKeyErr      error
	signatureRequired bool
	signatureVerified bool
	githubFallback    bool

	restartHook func() error
	idleCheck   func() bool
	idleSince   time.Time
}

// New constructs the desktop updater. The manager never mutates the running
// app until InstallNow is called explicitly (or AutoInstall is enabled and the
// gateway has been idle for the quiet period).
func New(opts Options) *Manager {
	m := &Manager{
		version:      cleanVersion(opts.Version),
		repo:         strings.TrimSpace(opts.Repository),
		feedURL:      strings.TrimSpace(opts.FeedURL),
		bundle:       strings.TrimSpace(opts.BundlePath),
		stageRoot:    strings.TrimSpace(opts.StageRoot),
		settingsPath: strings.TrimSpace(opts.SettingsPath),
		client:       opts.HTTPClient,
		settings:     DefaultSettings(),
		state:        StateIdle,
	}
	if m.version == "" {
		m.version = "dev"
	}
	if m.repo == "" {
		m.repo = DefaultRepo
	}
	if m.feedURL == "" {
		m.feedURL = strings.TrimSpace(os.Getenv("LPG_UPDATE_FEED"))
		if m.feedURL == "" {
			m.feedURL = DefaultFeedURL
		}
	}
	m.githubFallback = m.feedURL == DefaultFeedURL
	if m.client == nil {
		m.client = &http.Client{Timeout: 20 * time.Second}
	}
	if m.bundle == "" {
		m.bundle = findBundle()
	}
	if m.stageRoot == "" {
		m.stageRoot = defaultStageRoot()
	}
	if m.settingsPath == "" {
		m.settingsPath = defaultSettingsPath()
	}
	if s, err := loadSettings(m.settingsPath); err == nil {
		m.settings = s.normalized()
	}
	m.available = runtime.GOOS == "darwin" && m.bundle != ""
	if !m.available {
		m.state = StateUnavailable
		m.message = "自动更新仅支持从 macOS .app 启动的桌面版本"
	}
	key := strings.TrimSpace(opts.PublicKeyBase64)
	if key == "" {
		key = strings.TrimSpace(SignaturePublicKey)
	}
	if key != "" {
		m.signatureRequired = true
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			m.publicKeyErr = errors.New("更新签名公钥格式无效")
		} else {
			m.publicKey = ed25519.PublicKey(raw)
		}
	}
	return m
}

// Start begins the background check and auto-install loop. The context should
// be cancelled when the App shuts down.
func (m *Manager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.ctx = ctx
	bundle := m.bundle
	m.mu.Unlock()
	go func() {
		// The previous process is normally gone by the time this starts; wait a
		// moment so an in-place relaunch can finish first.
		time.Sleep(2 * time.Second)
		if bundle != "" {
			cleanupOldBundles(bundle)
		}
	}()
	go m.loop(ctx)
}

// SetRestartHook registers the App-owned operation that quits and reopens the
// bundle after an install. It must return only after arranging the relaunch.
func (m *Manager) SetRestartHook(fn func() error) {
	m.mu.Lock()
	m.restartHook = fn
	m.mu.Unlock()
}

// SetIdleCheck lets AutoInstall wait until the gateway has no model calls in
// flight. A nil check means AutoInstall cannot run automatically.
func (m *Manager) SetIdleCheck(fn func() bool) {
	m.mu.Lock()
	m.idleCheck = fn
	m.mu.Unlock()
}

// Close stops future automatic work. The caller owns the context passed to
// Start; Close only prevents new checks and installs.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// AppBundle returns the bundle the updater will replace, when available.
func (m *Manager) AppBundle() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bundle
}

func (m *Manager) context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

func (m *Manager) loop(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.tick(time.Now())
			timer.Reset(time.Minute)
		}
	}
}

func (m *Manager) tick(now time.Time) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	s := m.settings
	checked := m.checkedAt
	state := m.state
	m.mu.Unlock()

	if s.AutoCheck && (checked.IsZero() || now.Sub(checked) >= time.Duration(s.CheckIntervalHours)*time.Hour) {
		m.CheckNow()
	}
	if s.AutoInstall && state == StateReady {
		m.autoInstallTick(now)
	}
}

func (m *Manager) autoInstallTick(now time.Time) {
	m.mu.Lock()
	if m.state != StateReady || !m.settings.AutoInstall || m.stagePath == "" || m.restartHook == nil || m.idleCheck == nil {
		m.idleSince = time.Time{}
		m.mu.Unlock()
		return
	}
	idle := m.idleCheck()
	if !idle {
		m.idleSince = time.Time{}
		m.mu.Unlock()
		return
	}
	if m.idleSince.IsZero() {
		m.idleSince = now
	}
	due := now.Sub(m.idleSince) >= autoInstallQuietFor
	m.mu.Unlock()
	if due {
		m.InstallNow()
	}
}

// Settings returns a copy of the persisted local preferences.
func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

// SetSettings validates, persists, and applies desktop update preferences.
func (m *Manager) SetSettings(s Settings) (Status, error) {
	s = s.normalized()
	if err := saveSettings(m.settingsPath, s); err != nil {
		return m.Status(), err
	}
	m.mu.Lock()
	m.settings = s
	state := m.state
	m.mu.Unlock()
	if s.AutoDownload && state == StateAvailable {
		m.DownloadNow()
	}
	return m.Status(), nil
}

// Status is safe to call from HTTP handlers while a download is in progress.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Status{
		Available:         m.available,
		State:             m.state,
		Current:           m.version,
		CheckedAt:         formatTime(m.checkedAt),
		Progress:          m.progress,
		Total:             m.total,
		Error:             m.err,
		Message:           m.message,
		AppPath:           m.bundle,
		Downloaded:        m.stagePath != "",
		CanInstall:        m.available && m.stagePath != "" && m.restartHook != nil && m.state == StateReady,
		SignatureRequired: m.signatureRequired,
		SignatureVerified: m.signatureVerified,
		Settings:          m.settings,
	}
	if m.latest != nil {
		out.Latest = m.latest.Version
		out.ReleaseURL = m.latest.ReleaseURL
	}
	return out
}

// CheckNow starts an asynchronous feed check. It returns false when another
// operation is already running or this build cannot update itself.
func (m *Manager) CheckNow() bool {
	m.mu.Lock()
	if !m.available || m.closed || m.state == StateChecking || m.state == StateDownloading || m.state == StateInstalling || m.state == StateRestarting || m.state == StateReady {
		m.mu.Unlock()
		return false
	}
	m.state = StateChecking
	m.err = ""
	m.message = ""
	m.checkedAt = time.Now()
	ctx := m.contextLocked()
	m.mu.Unlock()
	go m.check(ctx)
	return true
}

// DownloadNow starts the asset download selected by CheckNow.
func (m *Manager) DownloadNow() bool {
	m.mu.Lock()
	if !m.available || m.closed || m.latest == nil || m.state == StateDownloading || m.state == StateInstalling || m.state == StateRestarting {
		m.mu.Unlock()
		return false
	}
	assetKey := platformKey()
	asset, ok := m.latest.Assets[assetKey]
	if !ok || strings.TrimSpace(asset.URL) == "" || strings.TrimSpace(asset.SHA256) == "" {
		m.state = StateError
		m.err = "当前版本没有提供可校验的桌面安装包"
		m.mu.Unlock()
		return false
	}
	m.state = StateDownloading
	m.assetKey = assetKey
	m.progress, m.total = 0, asset.Size
	m.err, m.message = "", ""
	ctx := m.contextLocked()
	version := m.latest.Version
	m.mu.Unlock()
	go m.download(ctx, version, asset)
	return true
}

// InstallNow swaps the staged bundle and asks the App to quit into it. The
// restart hook is supplied by the desktop shell because only it knows Wails.
func (m *Manager) InstallNow() bool {
	m.mu.Lock()
	if m.closed || m.state != StateReady || m.stagePath == "" || m.restartHook == nil || m.bundle == "" {
		m.mu.Unlock()
		return false
	}
	m.state = StateInstalling
	m.err = ""
	stagePath, bundle, restart := m.stagePath, m.bundle, m.restartHook
	m.mu.Unlock()

	if err := installBundleFn(stagePath, bundle); err != nil {
		m.mu.Lock()
		m.state = StateReady
		m.err = "安装更新失败：" + err.Error()
		m.mu.Unlock()
		return false
	}
	m.mu.Lock()
	m.state = StateRestarting
	m.stagePath = ""
	m.mu.Unlock()
	go func() {
		time.Sleep(350 * time.Millisecond)
		if err := restart(); err != nil {
			m.mu.Lock()
			m.state = StateError
			m.err = "更新已安装，但重新打开 App 失败：" + err.Error()
			m.mu.Unlock()
		}
	}()
	return true
}

func (m *Manager) contextLocked() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

func (m *Manager) check(ctx context.Context) {
	rel, err := m.fetchManifest(ctx)
	if err != nil {
		m.mu.Lock()
		m.state = StateError
		m.err = err.Error()
		m.mu.Unlock()
		return
	}
	if m.publicKeyErr != nil {
		m.mu.Lock()
		m.state = StateError
		m.err = m.publicKeyErr.Error()
		m.mu.Unlock()
		return
	}
	rel.Version = cleanVersion(rel.Version)
	if rel.Assets == nil {
		rel.Assets = map[string]Asset{}
	}
	m.mu.Lock()
	m.latest = rel
	m.signatureVerified = relSignatureFlag(rel)
	newer := Newer(rel.Version, m.version)
	if !newer {
		m.state = StateUpToDate
		m.err = ""
		m.message = ""
		m.mu.Unlock()
		return
	}
	m.state = StateAvailable
	m.err = ""
	m.message = ""
	autoDownload := m.settings.AutoDownload
	m.mu.Unlock()
	if autoDownload {
		m.DownloadNow()
	}
}

// ReleaseSignatureVerified is used by tests and by the manifest reader without
// exposing an unexported field on the public JSON type.
func relSignatureFlag(rel *Release) bool {
	return rel != nil && rel.signatureVerified
}

var (
	stageDownloadFn = stageDownload
	installBundleFn = installBundle
)

func (m *Manager) download(ctx context.Context, version string, asset Asset) {
	dir := filepath.Join(m.stageRoot, safePathPart(version))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.failDownload(err)
		return
	}
	zipPath := filepath.Join(dir, "download.zip")
	downloadClient := *m.client
	downloadClient.Timeout = 0
	downloadCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	if err := downloadFile(downloadCtx, &downloadClient, asset, zipPath, func(done, total int64) {
		m.mu.Lock()
		m.progress, m.total = done, total
		m.mu.Unlock()
	}); err != nil {
		m.failDownload(err)
		return
	}
	staged, err := stageDownloadFn(ctx, zipPath, dir, version)
	if err != nil {
		m.failDownload(err)
		return
	}
	m.mu.Lock()
	m.stagePath = staged
	m.state = StateReady
	m.progress, m.total = 0, 0
	m.err = ""
	m.message = ""
	m.mu.Unlock()
}

func (m *Manager) failDownload(err error) {
	m.mu.Lock()
	m.state = StateError
	m.err = "下载更新失败：" + err.Error()
	m.progress, m.total = 0, 0
	m.mu.Unlock()
}

func downloadFile(ctx context.Context, client *http.Client, asset Asset, path string, progress func(done, total int64)) error {
	if strings.TrimSpace(asset.SHA256) == "" {
		return errors.New("更新清单没有这个安装包的 SHA-256")
	}
	tmp := path + ".part"
	_ = os.Remove(tmp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "llm-protocol-gateway-updater/1")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	total := asset.Size
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	progress(0, total)
	_, copyErr := io.Copy(f, &progressReader{r: resp.Body, total: total, done: 0, report: progress})
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	sum, err := fileSHA256(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !strings.EqualFold(sum, strings.TrimPrefix(asset.SHA256, "sha256:")) {
		_ = os.Remove(tmp)
		return fmt.Errorf("安装包校验失败：期望 %s，实际 %s", asset.SHA256, sum)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type progressReader struct {
	r      io.Reader
	total  int64
	done   int64
	report func(done, total int64)
	last   time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.done += int64(n)
	if r.report != nil && (r.last.IsZero() || time.Since(r.last) >= 250*time.Millisecond || err == io.EOF) {
		r.report(r.done, r.total)
		r.last = time.Now()
	}
	return n, err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Manager) fetchManifest(ctx context.Context) (*Release, error) {
	if m.publicKeyErr != nil {
		return nil, m.publicKeyErr
	}
	body, err := getBytes(ctx, m.client, m.feedURL)
	if err == nil {
		var rel Release
		if err := json.Unmarshal(body, &rel); err != nil {
			return nil, fmt.Errorf("解析更新清单失败：%w", err)
		}
		if m.signatureRequired {
			sig, sigErr := getBytes(ctx, m.client, m.feedURL+".sig")
			if sigErr != nil {
				return nil, fmt.Errorf("读取更新签名失败：%w", sigErr)
			}
			if err := verifySignature(body, sig, m.publicKey); err != nil {
				return nil, err
			}
			rel.signatureVerified = true
		}
		if strings.TrimSpace(rel.Version) == "" || len(rel.Assets) == 0 {
			return nil, errors.New("更新清单缺少版本号或安装包")
		}
		return &rel, nil
	}
	if !m.githubFallback {
		return nil, err
	}
	return m.fetchGitHubLatest(ctx)
}

func (m *Manager) fetchGitHubLatest(ctx context.Context) (*Release, error) {
	api := githubAPIBase + "/repos/" + m.repo + "/releases/latest"
	body, err := getBytes(ctx, m.client, api)
	if err != nil {
		return nil, fmt.Errorf("读取 GitHub Release 失败：%w", err)
	}
	var raw struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Assets  []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	rel := &Release{Version: cleanVersion(raw.TagName), ReleaseURL: raw.HTMLURL, Notes: raw.Body, Assets: map[string]Asset{}}
	if strings.TrimSpace(rel.Version) == "" {
		return nil, errors.New("GitHub Release 没有版本号")
	}
	byName := map[string]struct {
		size int64
		url  string
		sha  string
	}{}
	for _, a := range raw.Assets {
		byName[a.Name] = struct {
			size int64
			url  string
			sha  string
		}{a.Size, a.URL, strings.TrimPrefix(a.Digest, "sha256:")}
	}
	var sumBody []byte
	for _, key := range []string{"darwin-arm64", "darwin-amd64", "windows-amd64", "linux-amd64"} {
		name := platformAssetName(key)
		a, ok := byName[name]
		if !ok || a.url == "" {
			continue
		}
		sha := a.sha
		if sha == "" {
			if sumBody == nil {
				if sums, ok := byName["SHA256SUMS"]; ok {
					sumBody, _ = getBytes(ctx, m.client, sums.url)
				}
			}
			sha = sumFor(sumBody, name)
		}
		if sha == "" {
			continue
		}
		rel.Assets[key] = Asset{Name: name, URL: a.url, Size: a.size, SHA256: sha}
	}
	if len(rel.Assets) == 0 {
		return nil, errors.New("GitHub Release 中没有可用于当前平台的 App 安装包")
	}
	if m.signatureRequired && Newer(rel.Version, m.version) {
		return nil, errors.New("GitHub Release 缺少经过签名的 update.json/update.json.sig，拒绝未验证的新版本")
	}
	return rel, nil
}

func getBytes(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("更新地址无效：%q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/octet-stream, text/plain")
	req.Header.Set("User-Agent", "llm-protocol-gateway-updater/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	const max = 8 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func sumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return fields[0]
		}
	}
	return ""
}

func verifySignature(body, sig []byte, key ed25519.PublicKey) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("更新签名不是 base64：%w", err)
	}
	if len(raw) != ed25519.SignatureSize || !ed25519.Verify(key, body, raw) {
		return errors.New("更新清单签名校验失败")
	}
	return nil
}

func platformKey() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "darwin-arm64"
		}
		return "darwin-amd64"
	case "windows":
		return "windows-amd64"
	case "linux":
		return "linux-amd64"
	default:
		return runtime.GOOS + "-" + runtime.GOARCH
	}
}

func platformAssetName(key string) string {
	switch key {
	case "darwin-arm64":
		return "llm-protocol-gateway-desktop-darwin-arm64.zip"
	case "darwin-amd64":
		return "llm-protocol-gateway-desktop-darwin-amd64.zip"
	case "windows-amd64":
		return "llm-protocol-gateway-desktop-windows-amd64.exe"
	case "linux-amd64":
		return "llm-protocol-gateway-desktop-linux-amd64.AppImage"
	default:
		return ""
	}
}

func cleanVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func safePathPart(v string) string {
	v = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '+':
			return r
		default:
			return '_'
		}
	}, v)
	if v == "" || v == "." || v == ".." {
		return "unknown"
	}
	return v
}

func defaultStageRoot() string {
	dir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return filepath.Join(os.TempDir(), "llm-protocol-gateway", "updates")
	}
	return filepath.Join(dir, "llm-protocol-gateway", "updates")
}

func defaultSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return filepath.Join(os.TempDir(), "llm-protocol-gateway-update.json")
	}
	return filepath.Join(dir, "llm-protocol-gateway", "desktop-update.json")
}

func loadSettings(path string) (Settings, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), err
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return DefaultSettings(), err
	}
	return s.normalized(), nil
}

func saveSettings(path string, s Settings) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, ".desktop-update-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
