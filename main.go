package main

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/c0wrk/core/updater"
	"github.com/v0lka/c0wrk/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

// activeCapture holds the process-wide crash capture installed by mainImpl.
// Package-level (not returned) because main() must reach it after mainImpl
// returns to log the exit code. Nil-tolerant methods make the zero state
// (capture disabled or failed) safe.
var activeCapture *crashlog.Capture

func main() {
	code := mainImpl()
	activeCapture.LogExit(code)
	os.Exit(code)
}

func mainImpl() int {
	// Self-update re-exec path: when launched with --self-update, this process
	// is the staging updater. It must NOT start the Wails lifecycle. Instead it
	// waits for the parent PID to exit, swaps the install tree, relaunches the
	// new app, and exits.
	if opts, isSelfUpdate, err := updater.ParseSelfUpdateFlags(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "c0wrk self-update: %v\n", err)
		return 2
	} else if isSelfUpdate {
		if applyErr := updater.ApplySelfUpdate(opts, slog.Default()); applyErr != nil {
			fmt.Fprintf(os.Stderr, "c0wrk self-update failed: %v\n", applyErr)
			return 1
		}
		return 0
	}

	// Normal startup: reap any orphaned updater artifacts left by a previous
	// update (notably Windows, where a running updater .exe cannot self-delete).
	updater.CleanupStaleUpdaters(slog.Default())

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

	agentDir := config.AgentDir()
	logDir := config.LogsDir(agentDir)

	// Arm crash capture before anything else can fail: fd 1/2 are redirected
	// into <logDir>/stderr.log so Go runtime panic dumps, native-library
	// errors and termination signals are persisted even when launched from
	// Finder. C0WRK_DISABLE_CRASH_CAPTURE=1 opts out (useful under `wails
	// dev` to keep live console output). The exact value is compared so a
	// stray "0"/"false" cannot silently disable capture.
	if os.Getenv("C0WRK_DISABLE_CRASH_CAPTURE") != "1" {
		capture, err := crashlog.Install(logDir)
		if err != nil {
			slog.Warn("crash capture unavailable; panics may leave no trace", "error", err)
		} else {
			activeCapture = capture
		}
	}

	wlog, wlogErr := desktop.NewWailsLogger(logDir)
	if wlogErr != nil {
		slog.Warn("failed to create wails log file, Wails errors may be lost", "error", wlogErr)
	}

	app := desktop.NewApp()
	app.SetWailsLogger(wlog)

	// Restore the persisted window size (written on resize/shutdown) so the
	// app reopens at the size the user left it. On first run (no state file)
	// this returns the built-in 1400x900 default. The maximized flag is
	// re-applied in Startup once the Wails context exists.
	windowBounds := desktop.LoadWindowBounds(agentDir)

	runErr := wails.Run(&options.App{
		Title:            "c0wrk",
		Width:            windowBounds.Width,
		Height:           windowBounds.Height,
		MinWidth:         1024,
		MinHeight:        600,
		BackgroundColour: options.NewRGB(40, 44, 52),
		StartHidden:      os.Getenv("C0WRK_START_HIDDEN") != "false",
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Enable native file-drop so dragging files onto the window emits their
		// absolute paths to the frontend (files:dropped, wired in Startup via
		// wailsRuntime.OnFileDrop). DisableWebViewDrop prevents the webview
		// from navigating to/opening the dropped file — paths are delivered
		// only through the Go event, never interpreted as navigation targets.
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		OnStartup:  app.Startup,
		OnShutdown: app.Shutdown,
		// Close guard: every quit path (window close button, Cmd+Q / the OS
		// quit menu, runtime.Quit — including the updater's quit) funnels
		// through this hook on all platforms. While sessions have live work
		// the hook intercepts the quit, emits app:exit_requested, and the
		// frontend shows a confirmation modal; ConfirmExit re-issues the quit
		// with a bypass flag so it goes through. See desktop/exit_guard.go.
		OnBeforeClose: app.ShouldPreventClose,
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
	// Clean-exit point for the liveness marker: reached on every normal
	// return (graceful quit, wails.Run error). A panic escaping mainImpl
	// skips this — deliberately, so the next start reports the crash.
	activeCapture.RemoveMarker()
	if runErr != nil {
		return 1
	}
	return 0
}
