package main

import (
	"embed"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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
