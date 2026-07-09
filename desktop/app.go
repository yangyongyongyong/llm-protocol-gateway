package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/app"
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
}

func NewApp() *App {
	return &App{rt: app.New()}
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
	ensurePackagedWebDistEnv()

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
}

func (a *App) domReady(ctx context.Context) {
	a.ctx = ctx
	// Navigate from the embedded bootstrap page to the local gateway UI once.
	// Do NOT replace when already on 127.0.0.1 — OnDomReady fires again after
	// every navigation and would otherwise reload-loop (page flicker).
	if a.rt == nil {
		return
	}
	target := a.rt.LocalURL() + "/"
	runtime.WindowExecJS(ctx, fmt.Sprintf(`
		(function () {
			var target = %q;
			var href = String(location.href || '');
			if (href.indexOf('127.0.0.1:') !== -1 || href.indexOf('localhost:') !== -1) {
				return;
			}
			location.replace(target);
		})();
	`, target))
}

func (a *App) shutdown(_ context.Context) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
