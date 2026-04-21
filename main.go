package main

import (
	"embed"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/user/agent/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := desktop.NewApp()

	err := wails.Run(&options.App{
		Title:            "c0wrk",
		Width:            1400,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        600,
		BackgroundColour: options.NewRGB(40, 44, 52),
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
	})
	if err != nil {
		slog.Error("failed to start Wails application", "error", err)
		os.Exit(1)
	}
}
