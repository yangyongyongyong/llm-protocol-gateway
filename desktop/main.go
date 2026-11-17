package main

import (
	"embed"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter(), &slog.HandlerOptions{Level: slog.LevelInfo})))

	application := NewApp()

	err := wails.Run(&options.App{
		Title:             "LLM Protocol Gateway",
		Width:             1280,
		Height:            860,
		MinWidth:          960,
		MinHeight:         640,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 1},
		OnStartup:        application.startup,
		OnShutdown:       application.shutdown,
		OnDomReady:       application.domReady,
		Bind: []interface{}{
			application,
		},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarDefault(),
			Appearance: mac.DefaultAppearance,
			About: &mac.AboutInfo{
				Title:   "LLM Protocol Gateway",
				Message: "本地 LLM 协议网关 · 管理页与 API",
			},
		},
		Menu: application.buildMenu(),
	})
	if err != nil {
		slog.Error("wails run failed", "error", err)
		os.Exit(1)
	}
}

// logWriter mirrors slog into a file: GUI builds have no console, and a blank
// window otherwise leaves nothing to diagnose.
func logWriter() io.Writer {
	cache, err := os.UserCacheDir()
	if err != nil {
		return os.Stdout
	}
	dir := filepath.Join(cache, "llm-protocol-gateway", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return os.Stdout
	}
	path := filepath.Join(dir, "desktop.log")
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 2<<20 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return os.Stdout
	}
	// 文件必须排在第一位：GUI 构建（-H windowsgui）里 os.Stdout 是无效句柄，
	// io.MultiWriter 遇到第一个 writer 写失败就返回，后面的文件永远收不到字节，
	// desktop.log 会一直是 0 字节，线上问题因此没有任何日志可查。
	return io.MultiWriter(f, os.Stdout)
}
