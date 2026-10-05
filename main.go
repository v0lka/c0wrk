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

	// Single-instance gate (issue #98, ADR-075): take the app-level lock
	// BEFORE any shared state is touched. A second process (on macOS,
	// LaunchServices activating the app from a delivered notification banner
	// whose bundle registration no longer matches the running process — a
	// rebuilt .app or an updater-swapped install tree) must stay read-only
	// toward ~/.c0wrk from here on: most importantly it must not install the
	// crash capture below, whose liveness-marker stash would overwrite the
	// running instance's forensics and leave an orphaned marker that the next
	// start misreports as an unclean shutdown. The lock is advisory only —
	// actual enforcement (relay to the first instance + early exit) is the
	// Wails SingleInstanceLock option wired into wails.Run below. Setup
	// failures fail open to "first instance": a wedged lock file must never
	// brick startup, and the Wails lock still enforces single instance.
	instanceLock, firstInstance, lockErr := desktop.AcquireSingleInstanceLock(config.SingleInstanceLockPath(agentDir))
	if lockErr != nil {
		slog.Warn("single-instance lock file unavailable; crash-capture forensics ungated for this run",
			"error", lockErr)
	}
	// Held for the process lifetime: closing it would release the lock (and
	// an unreachable os.File is closed by its finalizer under GC), so the
	// defer both pins it and releases it on the one clean-return path. The
	// second instance never returns from wails.Run (it exits inside the
	// Wails single-instance relay), where the OS drops the lock anyway.
	defer func() { _ = instanceLock.Close() }()

	// Arm crash capture before anything else can fail: fd 1/2 are redirected
	// into <logDir>/stderr.log so Go runtime panic dumps, native-library
	// errors and termination signals are persisted even when launched from
	// Finder. C0WRK_DISABLE_CRASH_CAPTURE=1 opts out (useful under `wails
	// dev` to keep live console output). The exact value is compared so a
	// stray "0"/"false" cannot silently disable capture. A second instance
	// skips installation entirely: it lives only long enough to relay its
	// launch data and exit, and its marker writes would poison the first
	// instance's crash forensics (see the gate above and ADR-075).
	if os.Getenv("C0WRK_DISABLE_CRASH_CAPTURE") != "1" && firstInstance {
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
		// The window is deliberately NOT created hidden. Wails applies
		// StartHidden by queueing a hide on the platform UI loop *after* the
		// webview starts loading, while OnStartup already runs concurrently on
		// its own goroutine — so a fast backend start gets its WindowShow calls
		// queued ahead of that hide, the hide executes last, and the window
		// stays withdrawn for the life of the process. Starting visible removes
		// the race: the window is mapped synchronously by Wails before its UI
		// loop begins. Nothing is lost — desktop/startup_phases.go revealed the
		// window unconditionally within a millisecond of startup anyway.
		AssetServer: &assetserver.Options{
			Assets: assets,
			// Strict CSP on every asset response (production builds only —
			// the dev variant in desktop/csp_dev.go is a no-op so it never
			// breaks the Vite/HMR pipeline). Defense in depth for injected
			// theme CSS: even a future sanitizer bypass cannot fetch anything.
			Middleware: desktop.CSPMiddleware(),
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
		// Linux-only webview hardware-acceleration policy, overridable via
		// C0WRK_WEBVIEW_GPU_POLICY (always | on-demand | never; default
		// never — the workaround Wails itself applies for wails#2977).
		// Returns nil on macOS/Windows, leaving those platforms untouched.
		Linux: desktop.WebviewGpuPolicyOptions(slog.Default()),
		// Single instance (issue #98, ADR-075): a LaunchServices-triggered
		// second open (macOS notification-banner click against a stale or
		// rebuilt bundle registration, a Finder re-open while the app runs)
		// must relay to THIS instance and exit, never boot a full second app
		// over the same SQLite DB, embedded-LLM supervisor and watchers —
		// the in-flight tasks of the first instance used to die with it.
		// The relay focuses the existing window; the notification's session
		// routing is not recoverable on this path (only the process macOS
		// attributes the click to receives it).
		SingleInstanceLock: desktop.SingleInstanceOptions(app),
		OnStartup:          app.Startup,
		OnDomReady:         app.DomReady,
		OnShutdown:         app.Shutdown,
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
