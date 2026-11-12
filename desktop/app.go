package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/app"
	"github.com/luca/llm-protocol-gateway/internal/clidesktop"
	"github.com/luca/llm-protocol-gateway/internal/gateway"
	"github.com/luca/llm-protocol-gateway/internal/packaged"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const launchAgentLabel = "com.luca.llm-protocol-gateway"

// App is the Wails-bound desktop shell around the shared gateway runtime.
type App struct {
	ctx context.Context
	rt  *app.Runtime
	// ready closes when startup has finished (gateway listening or quitting),
	// so the bootstrap page never navigates to a port that is not open yet.
	ready chan struct{}
}

func NewApp() *App {
	return &App{rt: app.New(), ready: make(chan struct{})}
}

func (a *App) buildMenu() *menu.Menu {
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())

	fileMenu := appMenu.AddSubmenu("文件")
	fileMenu.AddText("显示窗口", keys.CmdOrCtrl("1"), func(_ *menu.CallbackData) {
		if a.ctx != nil {
			runtime.WindowShow(a.ctx)
			runtime.WindowUnminimise(a.ctx)
		}
	})
	fileMenu.AddText("打开数据目录", nil, func(_ *menu.CallbackData) {
		a.OpenDataDirectory()
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("退出", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
		if a.ctx != nil {
			runtime.Quit(a.ctx)
		}
	})

	viewMenu := appMenu.AddSubmenu("显示")
	viewMenu.AddText("白天", nil, func(_ *menu.CallbackData) {
		a.SetTheme("light")
	})
	viewMenu.AddText("夜晚", nil, func(_ *menu.CallbackData) {
		a.SetTheme("dark")
	})
	viewMenu.AddText("跟随系统", nil, func(_ *menu.CallbackData) {
		a.SetTheme("system")
	})

	gatewayMenu := appMenu.AddSubmenu("网关")
	gatewayMenu.AddText("打开管理页", nil, func(_ *menu.CallbackData) {
		a.OpenLocalUI()
	})
	gatewayMenu.AddText("开启 Web 访问", nil, func(_ *menu.CallbackData) {
		_ = a.SetWebExposed(true)
	})
	gatewayMenu.AddText("关闭 Web 访问", nil, func(_ *menu.CallbackData) {
		_ = a.SetWebExposed(false)
	})
	gatewayMenu.AddSeparator()
	gatewayMenu.AddText("打开公网访问页", nil, func(_ *menu.CallbackData) {
		a.OpenPublicAccessPage()
	})
	gatewayMenu.AddText("停止 LaunchAgent 后台服务", nil, func(_ *menu.CallbackData) {
		msg, err := a.StopLaunchAgent()
		if a.ctx == nil {
			return
		}
		if err != nil {
			_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
				Type:    runtime.ErrorDialog,
				Title:   "停止后台服务失败",
				Message: err.Error(),
			})
			return
		}
		_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.InfoDialog,
			Title:   "后台服务",
			Message: msg,
		})
	})

	appMenu.Append(menu.EditMenu())
	appMenu.Append(menu.WindowMenu())
	return appMenu
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	markReady := sync.OnceFunc(func() { close(a.ready) })
	defer markReady()
	ensurePackagedWebDistEnv()
	// Prefer the copy that travelled inside this binary: side-car web-dist dirs
	// in a downloaded .exe / AppImage are missing (or from another version).
	if dist := materializeEmbeddedUI(); dist != "" {
		_ = os.Setenv("GATEWAY_WEB_DIST", dist)
		slog.Info("using embedded web UI", "path", dist)
	} else {
		slog.Warn("no embedded web UI in this build", "web_dist", os.Getenv("GATEWAY_WEB_DIST"))
	}

	if app.PortInUse(app.DefaultPort) {
		choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:          runtime.QuestionDialog,
			Title:         "端口已被占用",
			Message:       fmt.Sprintf("端口 %d 已被占用（常见原因：LaunchAgent 后台服务 %s 正在运行）。\n\n选择「是」停止后台服务并由本 App 托管；选择「否」退出 App。", app.DefaultPort, launchAgentLabel),
			Buttons:       []string{"是", "否"},
			DefaultButton: "是",
			CancelButton:  "否",
		})
		if err != nil || choice != "是" {
			runtime.Quit(ctx)
			return
		}
		if msg, stopErr := a.StopLaunchAgent(); stopErr != nil {
			_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
				Type:    runtime.ErrorDialog,
				Title:   "无法停止后台服务",
				Message: stopErr.Error() + "\n\n请手动执行：\nlaunchctl bootout gui/$(id -u)/" + launchAgentLabel,
			})
			runtime.Quit(ctx)
			return
		} else {
			slog.Info("launch agent stopped", "message", msg)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if !app.PortInUse(app.DefaultPort) {
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
			if app.PortInUse(app.DefaultPort) {
				_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
					Type:    runtime.ErrorDialog,
					Title:   "端口仍被占用",
					Message: fmt.Sprintf("已尝试停止 LaunchAgent，但端口 %d 仍不可用。请检查是否有其他 gateway 进程。", app.DefaultPort),
				})
				runtime.Quit(ctx)
				return
			}
		}
	}

	// Desktop: prefer persisted webExposed; first install defaults to loopback-only.
	cfg := app.Config{
		Port:          app.DefaultPort,
		PreferEnvAddr: false,
	}
	if err := a.rt.Start(cfg); err != nil {
		_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "网关启动失败",
			Message: err.Error(),
		})
		runtime.Quit(ctx)
		return
	}
	if err := a.rt.WaitHealthy(8 * time.Second); err != nil {
		slog.Warn("gateway health wait", "error", err)
	}
	markReady()
	a.verifyLocalUI()
}

// verifyLocalUI fails loudly when the console is not being served: a missing
// web-dist used to surface only as a bare "404 page not found" inside the window.
func (a *App) verifyLocalUI() {
	if a.rt == nil {
		return
	}
	url := a.rt.LocalURL() + "/"
	var lastErr error
	status := 0
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		status = resp.StatusCode
		buf := make([]byte, 512)
		n, _ := io.ReadFull(resp.Body, buf)
		_ = resp.Body.Close()
		lastErr = nil
		if status == http.StatusOK && strings.Contains(strings.ToLower(string(buf[:n])), "<!doctype html") {
			return
		}
	}
	webDist := os.Getenv("GATEWAY_WEB_DIST")
	slog.Error("local UI check failed", "url", url, "status", status, "error", lastErr, "web_dist", webDist)
	if a.ctx == nil {
		return
	}
	detail := fmt.Sprintf("管理页没有正常加载：%s\nHTTP %d\nweb dist: %s", url, status, webDist)
	if lastErr != nil {
		detail = fmt.Sprintf("管理页没有正常加载：%s\n%v\nweb dist: %s", url, lastErr, webDist)
	}
	_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.ErrorDialog,
		Title:   "控制台资源缺失",
		Message: detail,
	})
}

func (a *App) domReady(ctx context.Context) {
	a.ctx = ctx
	// Navigate from the embedded bootstrap page to the local gateway UI once.
	// Wait for startup first: navigating before the listener is up lands on an
	// error page, and the "already on 127.0.0.1" guard would then keep it there.
	if a.rt == nil {
		return
	}
	select {
	case <-a.ready:
	case <-time.After(12 * time.Second):
	}
	target := a.rt.LocalURL() + "/"
	runtime.WindowExecJS(ctx, fmt.Sprintf(`
		(function () {
			var target = %q;
			var href = String(location.href || '');
			var onGateway = href.indexOf('127.0.0.1:') !== -1 || href.indexOf('localhost:') !== -1;
			var tries = 0;
			try { tries = Number(sessionStorage.getItem('gw-nav-tries') || '0') || 0; } catch (e) {}
			if (!onGateway) {
				location.replace(target);
				return;
			}
			if (document.getElementById('root')) {
				try { sessionStorage.removeItem('gw-nav-tries'); } catch (e) {}
				return;
			}
			// 上一次导航可能落在网关尚未监听的错误页：就绪后重试，最多两次，
			// 避免控制台资源真的缺失时无限刷新。
			if (tries >= 2) { return; }
			try { sessionStorage.setItem('gw-nav-tries', String(tries + 1)); } catch (e) {}
			location.replace(target);
		})();
	`, target))
}

func (a *App) shutdown(_ context.Context) {
	// The runtime's own budget, not a shorter one: every step of Stop (drain,
	// landing the deferred writes, closing the DB) needs room to finish, and Stop
	// guarantees it returns inside this deadline either way.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), app.DefaultShutdownBudget)
	defer cancel()
	if err := a.rt.Stop(shutdownCtx); err != nil {
		slog.Error("gateway shutdown failed", "error", err)
	}
}

// LocalURL returns the loopback management URL.
func (a *App) LocalURL() string {
	if a.rt == nil {
		return fmt.Sprintf("http://127.0.0.1:%d", app.DefaultPort)
	}
	return a.rt.LocalURL()
}

// WebExposed reports whether LAN/tunnel binding is enabled.
func (a *App) WebExposed() bool {
	if a.rt == nil {
		return false
	}
	return a.rt.WebExposed()
}

// SetWebExposed toggles LAN/tunnel exposure (rebinds the HTTP listener).
func (a *App) SetWebExposed(enabled bool) string {
	if a.rt == nil {
		return "runtime not ready"
	}
	if err := a.rt.SetWebExposed(enabled); err != nil {
		if a.ctx != nil {
			_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
				Type:    runtime.ErrorDialog,
				Title:   "更新 Web 访问失败",
				Message: err.Error(),
			})
		}
		return err.Error()
	}
	if a.ctx != nil {
		msg := "已关闭 Web 访问：仅本机可访问"
		if enabled {
			msg = "已开启 Web 访问（局域网 / 穿透）。管理页无登录，勿对不可信网络长期暴露。"
		}
		runtime.EventsEmit(a.ctx, "web-exposed-changed", enabled)
		_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.InfoDialog,
			Title:   "Web 访问",
			Message: msg,
		})
	}
	return "ok"
}

// SetTheme switches the management UI between light / dark / system.
func (a *App) SetTheme(mode string) {
	switch mode {
	case "light", "dark", "system":
	default:
		mode = "system"
	}
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowExecJS(a.ctx, fmt.Sprintf(`
		(function () {
			var mode = %q;
			try { localStorage.setItem('llm-gateway-theme', mode); } catch (e) {}
			if (typeof window.__setGatewayTheme === 'function') {
				window.__setGatewayTheme(mode);
				return;
			}
			var dark = mode === 'dark' || (mode === 'system' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
			document.documentElement.dataset.theme = dark ? 'dark' : 'light';
			document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
		})();
	`, mode))
}

// OpenLocalUI focuses the window and navigates to the local management UI.
func (a *App) OpenLocalUI() {
	if a.ctx == nil || a.rt == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	a.navigateGateway("/")
}

// OpenPublicAccessPage opens the public-access settings route in the WebView.
func (a *App) OpenPublicAccessPage() {
	if a.ctx == nil || a.rt == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	a.navigateGateway("/public-access")
}

func (a *App) navigateGateway(path string) {
	if a.ctx == nil || a.rt == nil {
		return
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	target := a.rt.LocalURL() + path
	// Only navigate when the path differs; avoid full reload flicker on same page.
	runtime.WindowExecJS(a.ctx, fmt.Sprintf(`
		(function () {
			var target = %q;
			var path = %q;
			var href = String(location.href || '');
			var onGateway = href.indexOf('127.0.0.1:') !== -1 || href.indexOf('localhost:') !== -1;
			if (onGateway && location.pathname === path) {
				return;
			}
			if (onGateway && typeof history !== 'undefined' && history.pushState) {
				history.pushState({}, '', path);
				window.dispatchEvent(new PopStateEvent('popstate'));
				return;
			}
			location.replace(target);
		})();
	`, target, path))
}

// OpenDataDirectory reveals the user-level data folder in Finder (outside .app).
func (a *App) OpenDataDirectory() {
	paths := gateway.ResolveDataPaths()
	dir := strings.TrimSpace(paths.DataDir)
	if dir == "" {
		if a.ctx != nil {
			_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
				Type:    runtime.ErrorDialog,
				Title:   "数据目录",
				Message: "无法解析数据目录路径。",
			})
		}
		return
	}
	if err := openPathInFinder(dir); err != nil && a.ctx != nil {
		_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "打开数据目录失败",
			Message: err.Error() + "\n\n路径：" + dir,
		})
	}
}

// StopLaunchAgent boots out the headless LaunchAgent so the App can own the port.
func (a *App) StopLaunchAgent() (string, error) {
	return stopLaunchAgent(launchAgentLabel)
}

// ClientSetupInput 是前端传进来的连接信息。前端已经知道网关地址与密钥，
// 这里只负责写入，不再解析一遍。
type ClientSetupInput struct {
	// BaseURL 是 Claude Code CLI 用的地址（含 /anthropic 前缀）。
	BaseURL string `json:"baseUrl"`
	// GatewayURL 是 Claude Desktop 的 3p 模式用的地址（不含 /anthropic）。
	GatewayURL string `json:"gatewayUrl"`
	Token      string `json:"token"`
	// Model 固定为 default 或 default[1m]，留空按 default[1m] 处理。
	Model string `json:"model"`
}

// ClientSetupResult 是写入结果，直接回给界面展示。
type ClientSetupResult struct {
	OK     bool              `json:"ok"`
	Error  string            `json:"error,omitempty"`
	Result clidesktop.Result `json:"result"`
}

// SetupClaudeClients 把本机的 Claude Code CLI 与 Claude Desktop 都指向本网关。
//
// CLI 与 Desktop 是两套完全不同的配置：CLI 读 ~/.claude/settings.json 的
// env，Desktop 的第三方网关模式读自己的 Claude-3p 目录。两边都写，用户不必
// 关心自己在哪个客户端里用。写入前都会备份，且只改各自负责的键。
func (a *App) SetupClaudeClients(input ClientSetupInput) ClientSetupResult {
	res, err := clidesktop.Apply(clidesktop.Endpoint{
		BaseURL:    strings.TrimSpace(input.BaseURL),
		GatewayURL: strings.TrimSpace(input.GatewayURL),
		Token:      strings.TrimSpace(input.Token),
		Model:      strings.TrimSpace(input.Model),
	})
	if err != nil {
		return ClientSetupResult{OK: false, Error: err.Error(), Result: res}
	}
	return ClientSetupResult{OK: true, Result: res}
}

// ensurePackagedWebDistEnv points the gateway at Contents/Resources when running
// inside a macOS .app (cwd is typically "/").
func ensurePackagedWebDistEnv() {
	res := packaged.ResourcesDir()
	if res == "" {
		// Fallback when ResourcesDir heuristics miss (rare).
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidate := filepath.Join(filepath.Dir(exe), "..", "Resources")
		if abs, err := filepath.Abs(candidate); err == nil {
			candidate = abs
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			res = candidate
		}
	}
	if res == "" {
		return
	}
	_ = os.Setenv("GATEWAY_APP_RESOURCES", res)
	if dist := packaged.WebDistDir(); dist != "" && strings.TrimSpace(os.Getenv("GATEWAY_WEB_DIST")) == "" {
		_ = os.Setenv("GATEWAY_WEB_DIST", dist)
	}
	if cf, err := packaged.Cloudflared(); err == nil {
		_ = os.Setenv("GATEWAY_CLOUDFLARED", cf)
		slog.Info("using packaged cloudflared", "path", cf)
	}
	if bun, err := packaged.Bun(); err == nil {
		_ = os.Setenv("GATEWAY_BUN", bun)
		slog.Info("using packaged bun", "path", bun)
	}
	if bridge := packaged.CursorBridgeDir(); bridge != "" {
		_ = os.Setenv("GATEWAY_CURSOR_BRIDGE_DIR", bridge)
		slog.Info("using packaged cursor-bridge", "path", bridge)
	}
	if dist := os.Getenv("GATEWAY_WEB_DIST"); dist != "" {
		slog.Info("using packaged web dist", "path", dist)
	}
}
