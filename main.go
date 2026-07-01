package main

import (
	"embed"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Top-level panic recovery captures any unrecovered panic in the
	// main goroutine (e.g. a nil dereference in a goroutine without its
	// own recover). Logs the stack trace to the default logger and the
	// wails.log file before the process exits.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("unrecovered panic in main goroutine",
				"panic", r,
				"stack", string(debug.Stack()),
			)
			panic(r) // re-panic to preserve default crash behavior after logging
		}
	}()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	agentDir := filepath.Join(homeDir, config.DefaultAgentDir)
	logDir := config.LogsDir(agentDir)

	wlog, wlogErr := desktop.NewWailsLogger(logDir)
	if wlogErr != nil {
		slog.Warn("failed to create wails log file, Wails errors may be lost", "error", wlogErr)
	}

	app := desktop.NewApp()
	app.SetWailsLogger(wlog)

	runErr := wails.Run(&options.App{
		Title:            "c0wrk",
		Width:            1400,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        600,
		BackgroundColour: options.NewRGB(40, 44, 52),
		StartHidden:      os.Getenv("C0WRK_START_HIDDEN") != "false",
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.Startup,
		OnShutdown: app.Shutdown,
		Bind: []any{
			app,
		},
		Debug: options.Debug{
			OpenInspectorOnStartup: os.Getenv("C0WRK_DEBUG") != "",
		},
		Logger:   wlog,
		LogLevel: 2, // TRACE to capture all Wails messages
	})
	if runErr != nil {
		slog.Error("failed to start Wails application", "error", runErr)
	}
	if wlog != nil {
		_ = wlog.Close()
	}
	if runErr != nil {
		os.Exit(1)
	}
}
