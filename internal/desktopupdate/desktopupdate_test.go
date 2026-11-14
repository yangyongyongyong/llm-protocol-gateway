package desktopupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func disableBundledSignatureForTest(t *testing.T) {
	t.Helper()
	old := SignaturePublicKey
	SignaturePublicKey = ""
	t.Cleanup(func() { SignaturePublicKey = old })
}

func TestBundledUpdatePublicKeyIsValid(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(updatePublicKeyFile))
	if err != nil {
		t.Fatalf("bundled public key is not base64: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("bundled public key length = %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	m := New(Options{Version: "1.0.0", BundlePath: filepath.Join(t.TempDir(), "App.app")})
	if !m.signatureRequired || m.publicKeyErr != nil {
		t.Fatalf("normal build must require the bundled signature: required=%v err=%v", m.signatureRequired, m.publicKeyErr)
	}
}

func TestDefaultSettingsEnableHourlyHandsFreeUpdates(t *testing.T) {
	got := DefaultSettings()
	want := Settings{AutoCheck: true, AutoDownload: true, AutoInstall: true, CheckIntervalHours: 1}
	if got != want {
		t.Fatalf("DefaultSettings() = %+v, want %+v", got, want)
	}
}

func TestNewerSemanticVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.10", "1.0.9", true},
		{"v1.0.2", "1.0.1", true},
		{"1.0.0", "1.0.0-rc.1", true},
		{"1.0.0-rc.2", "1.0.0-rc.10", false},
		{"1.0.0-dev", "1.0.0", false},
		{"dev", "1.0.0", false},
		{"1.0.0+sha", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheckAndDownloadUseTheSelectedAsset(t *testing.T) {
	disableBundledSignatureForTest(t)
	assetBody := []byte("fake desktop archive")
	sum := sha256.Sum256(assetBody)
	var requestedAsset atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/update.json":
			key := platformKey()
			fmt.Fprintf(w, `{"version":"1.2.0","assets":{%q:{"name":"app.zip","url":%q,"size":%d,"sha256":%q}}}`,
				key, "http://"+r.Host+"/asset", len(assetBody), hex.EncodeToString(sum[:]))
		case "/asset":
			requestedAsset.Add(1)
			_, _ = w.Write(assetBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	oldStage, oldInstall := stageDownloadFn, installBundleFn
	stageDownloadFn = func(_ context.Context, _ string, dir, version string) (string, error) {
		if version != "1.2.0" {
			t.Errorf("stage version = %q", version)
		}
		return filepath.Join(dir, "payload"), nil
	}
	installBundleFn = func(string, string) error { return nil }
	t.Cleanup(func() { stageDownloadFn, installBundleFn = oldStage, oldInstall })

	m := New(Options{
		Version:      "1.1.0",
		FeedURL:      srv.URL + "/update.json",
		BundlePath:   filepath.Join(root, "LLM Protocol Gateway.app"),
		SettingsPath: filepath.Join(root, "settings.json"),
		StageRoot:    filepath.Join(root, "stage"),
		HTTPClient:   srv.Client(),
	})
	m.available = true
	if _, err := m.SetSettings(Settings{AutoCheck: false, AutoDownload: false, CheckIntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	if !m.CheckNow() {
		t.Fatal("CheckNow returned false")
	}
	waitForState(t, m, StateAvailable)
	if !m.DownloadNow() {
		t.Fatal("DownloadNow returned false")
	}
	waitForState(t, m, StateReady)
	if requestedAsset.Load() != 1 {
		t.Fatalf("asset requests = %d, want 1", requestedAsset.Load())
	}
	st := m.Status()
	if st.Latest != "1.2.0" || !st.Downloaded || st.Current != "1.1.0" {
		t.Fatalf("status = %+v", st)
	}
}

func TestInstallUsesTheRestartHookOnlyAfterStaging(t *testing.T) {
	root := t.TempDir()
	oldInstall := installBundleFn
	installBundleFn = func(string, string) error { return nil }
	t.Cleanup(func() { installBundleFn = oldInstall })

	m := New(Options{
		Version:      "1.0.0",
		BundlePath:   filepath.Join(root, "LLM Protocol Gateway.app"),
		SettingsPath: filepath.Join(root, "settings.json"),
		StageRoot:    filepath.Join(root, "stage"),
	})
	m.available = true
	m.state = StateReady
	m.stagePath = filepath.Join(root, "payload")
	var restarted atomic.Int32
	m.SetRestartHook(func() error {
		restarted.Add(1)
		return nil
	})
	if !m.InstallNow() {
		t.Fatal("InstallNow returned false")
	}
	deadline := time.Now().Add(2 * time.Second)
	for restarted.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if restarted.Load() != 1 {
		t.Fatalf("restart hook calls = %d", restarted.Load())
	}
}

func TestManifestSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":"1.0.1","assets":{}}`)
	sig := ed25519.Sign(priv, body)
	if err := verifySignature(body, []byte(base64.StdEncoding.EncodeToString(sig)), pub); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), body...)
	bad[0] ^= 0xff
	if err := verifySignature(bad, []byte(base64.StdEncoding.EncodeToString(sig)), pub); err == nil {
		t.Fatal("tampered manifest passed signature verification")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "desktop-update.json")
	want := Settings{AutoCheck: false, AutoDownload: true, AutoInstall: true, CheckIntervalHours: 24}
	if err := saveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func waitForState(t *testing.T, m *Manager, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.Status().State; got == want {
			return
		} else if got == StateError {
			t.Fatalf("manager entered error while waiting for %s: %s", want, m.Status().Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state did not become %s: %+v", want, m.Status())
}

func TestGitHubReleaseFallback(t *testing.T) {
	disableBundledSignatureForTest(t)
	assetBody := []byte("fallback archive")
	sum := sha256.Sum256(assetBody)
	key := platformKey()
	name := platformAssetName(key)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v2.0.0","html_url":"https://github.com/owner/repo/releases/tag/v2.0.0","body":"notes","assets":[{"name":%q,"size":%d,"browser_download_url":%q,"digest":%q}]}`,
				name, len(assetBody), "http://"+r.Host+"/asset", "sha256:"+hex.EncodeToString(sum[:]))
		case "/asset":
			_, _ = w.Write(assetBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldAPI := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldAPI })

	m := New(Options{
		Version:      "1.0.0",
		Repository:   "owner/repo",
		FeedURL:      srv.URL + "/missing-update.json",
		BundlePath:   filepath.Join(t.TempDir(), "App.app"),
		SettingsPath: filepath.Join(t.TempDir(), "settings.json"),
		StageRoot:    filepath.Join(t.TempDir(), "stage"),
		HTTPClient:   srv.Client(),
	})
	m.available = true
	m.githubFallback = true
	if _, err := m.SetSettings(Settings{AutoCheck: false, AutoDownload: false, CheckIntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	m.CheckNow()
	waitForState(t, m, StateAvailable)
	if got := m.Status().Latest; got != "2.0.0" {
		t.Fatalf("fallback latest = %q", got)
	}
}

func TestUnsignedNewerGitHubFallbackIsRejectedWhenSignatureIsRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v2.0.0","html_url":"https://github.com/owner/repo/releases/tag/v2.0.0","assets":[{"name":%q,"size":1,"browser_download_url":%q,"digest":"sha256:00"}]}`,
				platformAssetName(platformKey()), "http://"+r.Host+"/asset")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldAPI := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldAPI })

	m := New(Options{
		Version:      "1.0.0",
		Repository:   "owner/repo",
		FeedURL:      srv.URL + "/missing-update.json",
		BundlePath:   filepath.Join(t.TempDir(), "App.app"),
		SettingsPath: filepath.Join(t.TempDir(), "settings.json"),
		StageRoot:    filepath.Join(t.TempDir(), "stage"),
		HTTPClient:   srv.Client(),
	})
	m.githubFallback = true
	_, err := m.fetchGitHubLatest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "update.json/update.json.sig") {
		t.Fatalf("unsigned newer fallback must be rejected, got %v", err)
	}
}
