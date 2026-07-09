package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/app"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rt := app.New()
	// Headless mode: prefer GATEWAY_ADDR when set; otherwise expose on all
	// interfaces by default so existing LaunchAgent / LAN setups keep working.
	cfg := app.Config{
		Port:          app.DefaultPort,
		PreferEnvAddr: true,
		// PreferEnvAddr also selects the headless first-run default (webExposed=true)
		// when SQLite has no webExposed setting yet.
	}
	if err := rt.Start(cfg); err != nil {
		slog.Error("gateway start failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Stop(shutdownCtx); err != nil {
		slog.Error("gateway shutdown failed", "error", err)
	}
}
