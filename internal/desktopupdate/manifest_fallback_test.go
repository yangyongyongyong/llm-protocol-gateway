package desktopupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// updaterRoundTripFunc 只在测试中替换网络传输，模拟附件域名的 EOF 与超时。
type updaterRoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 按测试用例预设的响应处理请求，不访问真实 GitHub。
func (f updaterRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// newManifestFallbackManager 创建独立更新器，把 GitHub API 指向假服务器，
// 不修改本机更新设置，也不下载任何真实安装包。
func newManifestFallbackManager(t *testing.T, server *httptest.Server, key ed25519.PublicKey) *Manager {
	t.Helper()
	oldAPI := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = oldAPI })
	root := t.TempDir()
	manager := New(Options{
		Version:         "1.0.26",
		Repository:      "owner/repo",
		FeedURL:         server.URL + "/feed-unavailable",
		BundlePath:      filepath.Join(root, "App.app"),
		SettingsPath:    filepath.Join(root, "settings.json"),
		StageRoot:       filepath.Join(root, "stage"),
		HTTPClient:      server.Client(),
		PublicKeyBase64: base64.StdEncoding.EncodeToString(key),
	})
	manager.githubFallback = true
	return manager
}

// writeSignedReleaseMetadata 返回固定 Release 的清单与签名附件；安装包信息
// 故意与已签名清单不同，用来验证更新器不信任 API 的未签名下载地址和哈希。
func writeSignedReleaseMetadata(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	payload := map[string]any{
		"tag_name": "v1.0.27",
		"html_url": "https://github.com/owner/repo/releases/tag/v1.0.27",
		"assets": []map[string]any{
			{"name": "update.json", "browser_download_url": "http://" + r.Host + "/v1.0.27/update.json"},
			{"name": "update.json.sig", "browser_download_url": "http://" + r.Host + "/v1.0.27/update.json.sig"},
			{"name": platformAssetName(platformKey()), "browser_download_url": "http://" + r.Host + "/unverified.zip", "digest": "sha256:bad-api-hash"},
		},
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Error(err)
	}
}

// TestGitHubFallbackReadsSignedReleaseManifest 覆盖稳定下载地址不可用但固定
// 版本附件可用的情况：必须重新验签，并仅采用已签名清单中的安装包信息。
func TestGitHubFallbackReadsSignedReleaseManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var manifestBody, signature []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/releases/latest":
			writeSignedReleaseMetadata(t, w, r)
		case "/v1.0.27/update.json":
			_, _ = w.Write(manifestBody)
		case "/v1.0.27/update.json.sig":
			_, _ = w.Write(signature)
		case "/unverified.zip", "/verified.zip":
			t.Error("检查清单不应下载安装包")
			http.Error(w, "unexpected download", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manifestBody = []byte(fmt.Sprintf(`{"version":"1.0.27","assets":{%q:{"name":"verified.zip","url":%q,"size":1,"sha256":"signed-hash"}}}`, platformKey(), server.URL+"/verified.zip"))
	signature = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifestBody)))
	manager := newManifestFallbackManager(t, server, pub)

	release, err := manager.fetchManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !release.signatureVerified || release.Version != "1.0.27" {
		t.Fatalf("回退后清单未验签或版本错误：%+v", release)
	}
	asset := release.Assets[platformKey()]
	if asset.URL != server.URL+"/verified.zip" || asset.SHA256 != "signed-hash" {
		t.Fatalf("采用了未签名 API 的安装包信息：%+v", asset)
	}
}

// TestGitHubFallbackRejectsTamperedOrMismatchedManifest 验证签名损坏与版本
// 错配均不能回退成未验证更新，签名文件读不到时也必须明确报告真实读取失败。
func TestGitHubFallbackRejectsTamperedOrMismatchedManifest(t *testing.T) {
	for _, name := range []string{"tampered", "wrong-version", "signature-unavailable"} {
		t.Run(name, func(t *testing.T) {
			pub, priv, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			manifest := []byte(fmt.Sprintf(`{"version":"1.0.27","assets":{%q:{"url":"https://example.com/verified.zip","sha256":"signed"}}}`, platformKey()))
			if name == "wrong-version" {
				manifest = []byte(strings.ReplaceAll(string(manifest), "1.0.27", "1.0.26"))
			}
			signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)))
			if name == "tampered" {
				manifest = []byte(strings.ReplaceAll(string(manifest), "verified.zip", "altered.zip"))
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/releases/latest":
					writeSignedReleaseMetadata(t, w, r)
				case "/v1.0.27/update.json":
					_, _ = w.Write(manifest)
				case "/v1.0.27/update.json.sig":
					if name == "signature-unavailable" {
						http.Error(w, "temporary failure", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write(signature)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			manager := newManifestFallbackManager(t, server, pub)
			release, err := manager.fetchManifest(context.Background())
			if release != nil || err == nil {
				t.Fatalf("不安全清单没有被拒绝：release=%+v err=%v", release, err)
			}
			want := map[string]string{
				"tampered":              "签名校验失败",
				"wrong-version":         "不一致",
				"signature-unavailable": "读取 update.json.sig 失败",
			}[name]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("缺少明确错误 %q：%v", want, err)
			}
		})
	}
}

// TestGitHubFallbackPreservesAttachmentNetworkFailure 覆盖真实问题：GitHub
// API 明确列了两个附件，但附件请求 EOF。最终错误不能再称“缺少签名文件”。
func TestGitHubFallbackPreservesAttachmentNetworkFailure(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/releases/latest" {
			writeSignedReleaseMetadata(t, w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	manager := newManifestFallbackManager(t, server, pub)
	transport := manager.client.Transport
	manager.client.Transport = updaterRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/feed-unavailable" || r.URL.Path == "/v1.0.27/update.json" {
			return nil, io.EOF
		}
		return transport.RoundTrip(r)
	})
	release, err := manager.fetchManifest(context.Background())
	if release != nil || err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("网络错误链丢失：release=%+v err=%v", release, err)
	}
	if !strings.Contains(err.Error(), "已发布更新清单") || !strings.Contains(err.Error(), "读取 update.json 失败") {
		t.Fatalf("网络问题缺少准确提示：%v", err)
	}
	if strings.Contains(err.Error(), "缺少经过签名") {
		t.Fatalf("仍将网络错误误报为附件缺失：%v", err)
	}
}

// TestGetBytesDoesNotExposeSignedRedirectQuery 验证 EOF 提示不回显 CDN
// 跳转地址的临时 sig/token 参数，底层错误仍可用 errors.Is 判断。
func TestGetBytesDoesNotExposeSignedRedirectQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://release-assets.githubusercontent.com/file?sig=do-not-display", http.StatusFound)
	}))
	defer server.Close()
	transport := server.Client().Transport
	client := &http.Client{Transport: updaterRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "release-assets.githubusercontent.com" {
			return nil, io.EOF
		}
		return transport.RoundTrip(r)
	})}
	_, err := getBytes(context.Background(), client, server.URL)
	if err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF: %v", err)
	}
	if strings.Contains(err.Error(), "do-not-display") || strings.Contains(err.Error(), "?sig=") {
		t.Fatalf("错误信息泄露签名查询参数：%v", err)
	}
	if !strings.Contains(err.Error(), "release-assets.githubusercontent.com") {
		t.Fatalf("错误信息缺少失败的主机：%v", err)
	}
}

// TestUnverifiedGitHubStatusCannotStartDownload 防止“同版只读 API 回退”留存
// 的未验签快照被手动下载接口拿去安装，即使其 SHA-256 已经有值也必须拒绝。
func TestUnverifiedGitHubStatusCannotStartDownload(t *testing.T) {
	manager := New(Options{Version: "1.0.26", BundlePath: filepath.Join(t.TempDir(), "App.app")})
	manager.available = true
	manager.latest = &Release{
		Version: "1.0.27",
		Assets:  map[string]Asset{platformKey(): {URL: "https://example.com/asset", SHA256: "hash"}},
	}
	manager.state = StateUpToDate
	if manager.DownloadNow() {
		t.Fatal("未验证签名的快照不应启动下载")
	}
	if status := manager.Status(); status.State != StateError || !strings.Contains(status.Error, "尚未通过签名验证") {
		t.Fatalf("拒绝下载的状态不明确：%+v", status)
	}
}

// TestCustomFeedFailureDoesNotUseGitHub 验证自定义更新源仍只请求自己的
// 清单，不会因网络失败转向默认 GitHub 仓库，更不改变用户的可信更新来源。
func TestCustomFeedFailureDoesNotUseGitHub(t *testing.T) {
	manager := New(Options{
		FeedURL:      "https://custom.example/update.json",
		BundlePath:   filepath.Join(t.TempDir(), "App.app"),
		HTTPClient:   &http.Client{Transport: updaterRoundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, io.EOF })},
		SettingsPath: filepath.Join(t.TempDir(), "settings.json"),
	})
	if manager.githubFallback {
		t.Fatal("自定义 feed 不应启用 GitHub 回退")
	}
	_, err := manager.fetchManifest(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("自定义源错误丢失：%v", err)
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		t.Fatalf("用户可见错误不应携带完整请求 URL：%v", err)
	}
}

// TestGitHubFallbackNamesMissingSignedAsset 验证真正缺少清单或签名时准确指出
// 哪个附件缺失，同时拒绝返回未经验证的新版本。
func TestGitHubFallbackNamesMissingSignedAsset(t *testing.T) {
	for _, missing := range []string{"update.json", "update.json.sig"} {
		t.Run(missing, func(t *testing.T) {
			pub, _, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/releases/latest" {
					http.NotFound(w, r)
					return
				}
				assets := []map[string]any{}
				for _, name := range []string{"update.json", "update.json.sig"} {
					if name != missing {
						assets = append(assets, map[string]any{"name": name, "browser_download_url": "http://" + r.Host + "/" + name})
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.0.27", "assets": assets})
			}))
			defer server.Close()
			manager := newManifestFallbackManager(t, server, pub)
			release, err := manager.fetchManifest(context.Background())
			if release != nil || err == nil || !strings.Contains(err.Error(), "缺少 "+missing+"）") {
				t.Fatalf("缺失附件的错误不准确：release=%+v err=%v", release, err)
			}
		})
	}
}
