package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v2"
	wailslogger "github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/c0wrk/core/cli"
	"github.com/v0lka/c0wrk/core/updater"
	"github.com/v0lka/c0wrk/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

// activeCapture holds the process-wide crash capture installed by mainImpl.
// Package-level because main() must reach it after mainImpl returns to log
// the exit code; nil-tolerant methods make the zero state safe.
var activeCapture *crashlog.Capture

func main() {
	code := mainImpl()
	activeCapture.LogExit(code)
	os.Exit(code)
}

// gpuRelaunchHandoverBudget resolves the relaunched child's handover budget
// from the CONFIGURED shutdown deadline — the same config.yaml the quitting
// instance read — so the child waits exactly as long as the predecessor is
// legitimately allowed to tear down (plus margin). Best-effort and
// read-only: a missing or unreadable file falls back to the built-in
// default.
func gpuRelaunchHandoverBudget(agentDir string) time.Duration {
	budget := desktop.GpuRelaunchHandoverBudget(desktop.EffectiveShutdownDeadline(0))
	cfg, err := config.Load(config.ConfigPath(agentDir))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("GPU-fallback relaunch: config unreadable; handover budget sized from the default shutdown deadline", "error", err)
		}
		return budget
	}
	return desktop.GpuRelaunchHandoverBudget(desktop.EffectiveShutdownDeadline(cfg.Shutdown.HardDeadline))
}

func mainImpl() int {
	// The command line is parsed exactly once, here, before anything else in
	// the process runs. The ordering is load-bearing — see
	// specs/domains/cli.md (startup ordering): diagnostics must reach the
	// real stdout/stderr, a diagnostic exit must not touch app state, and
	// the self-update re-exec path must not start the Wails lifecycle.
	req, parseErr := cli.Parse(os.Args[1:])
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "c0wrk: %v\n", parseErr)
		cli.PrintHelp(os.Stderr)
		return 2
	}
	switch req.Action {
	case cli.ActionHelp:
		cli.PrintHelp(os.Stdout)
		return 0
	case cli.ActionVersion:
		cli.PrintVersion(os.Stdout)
		return 0
	case cli.ActionSelfUpdate:
		if applyErr := updater.ApplySelfUpdate(*req.SelfUpdate, slog.Default()); applyErr != nil {
			fmt.Fprintf(os.Stderr, "c0wrk self-update failed: %v\n", applyErr)
			return 1
		}
		return 0
	}

	// Normal startup: reap orphaned updater artifacts left by a previous
	// update (notably Windows, where a running updater .exe cannot
	// self-delete). MUST happen only after the single-instance gate below
	// and only in the owning instance: the sweep RemoveAll's every
	// c0wrk-update-* / c0wrk-extract-* staging in os.TempDir() older than
	// the 24 h bound (updater.cleanupStaleMaxAge), so fresh staging of an
	// in-flight self-update is never touched, and a transient second launch
	// can never race a mid-flight relaunch's use of its staging (review
	// finding #37).

	// Top-level panic recovery captures any unrecovered panic in the main
	// goroutine (e.g. a nil dereference in a goroutine without its own
	// recover), logging the stack before the process exits.
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

	// GPU-fallback relaunch ordering (issue #34): the relaunched child waits
	// for the quitting instance's advisory lock before starting up —
	// specs/domains/webview-gpu-policy.md (the relaunch handover): the
	// budget is sized from the configured shutdown deadline, and expiry is
	// TERMINAL (starting anyway would relay this launch to the
	// still-quitting instance and lose the replacement window).
	if os.Getenv(desktop.GpuRelaunchWaitEnvVar) == "1" {
		_ = os.Unsetenv(desktop.GpuRelaunchWaitEnvVar)
		if err := desktop.WaitForGpuRelaunchHandover(config.SingleInstanceLockPath(agentDir), gpuRelaunchHandoverBudget(agentDir), slog.Default()); err != nil {
			slog.Error("GPU-fallback relaunch: not starting after the failed handover "+
				"(the fallback is persisted; the next manual start applies it)", "error", err)
			return 1
		}
	}

	// Single-instance gate (issue #98): take the app-level lock BEFORE any
	// shared state is touched — specs/decisions/075-single-instance-lock.md
	// (D3): a second process stays read-only toward ~/.c0wrk and skips crash
	// capture entirely (marker churn would poison the running instance's
	// forensics); setup failures fail open to "first instance"; enforcement
	// itself is the Wails option below.
	instanceLock, firstInstance, lockErr := desktop.AcquireSingleInstanceLock(config.SingleInstanceLockPath(agentDir))
	if lockErr != nil {
		slog.Warn("single-instance lock file unavailable; crash-capture forensics ungated for this run",
			"error", lockErr)
	}
	// Held for the process lifetime: closing it would release the lock, so
	// the defer both pins it and releases it on the one clean-return path.
	// The second instance never returns from wails.Run (it exits inside the
	// relay), where the OS drops the lock anyway.
	defer func() { _ = instanceLock.Close() }()

	// Reap only now that the app-level lock is held, and only in the first
	// instance — the exact gate the crash capture below uses (see above).
	if firstInstance {
		updater.CleanupStaleUpdaters(slog.Default())
	}

	// Arm crash capture before anything else can fail
	// (specs/domains/crash-logging.md): fd 1/2 are redirected into
	// <logDir>/stderr.log; C0WRK_DISABLE_CRASH_CAPTURE=1 opts out; a second
	// instance skips installation entirely (ADR-075).
	//
	// The outcome also decides the webview GPU resolution — the DMABUF
	// watchdog reads ONLY the captured stderr, so an unobserved run pins
	// fresh-auto to never (specs/domains/webview-gpu-policy.md, review
	// finding #155.2).
	var stderrObserved bool
	if os.Getenv("C0WRK_DISABLE_CRASH_CAPTURE") != "1" && firstInstance {
		capture, err := crashlog.Install(logDir)
		if err != nil {
			slog.Warn("crash capture unavailable; panics may leave no trace and the webview GPU watchdog cannot observe this run", "error", err)
		} else {
			activeCapture = capture
			stderrObserved = true
		}
	}

	wlog, wlogErr := desktop.NewWailsLogger(logDir)
	if wlogErr != nil {
		slog.Warn("failed to create wails log file, Wails errors may be lost", "error", wlogErr)
	}
	// Wails substitutes its default logger only when options.Logger is a nil
	// INTERFACE. wlog is a concrete *wailsLogAdapter, so a plain
	// `Logger: wlog` would store a typed nil inside a non-nil interface,
	// defeat the guard, and panic on the first Wails log call. Convert the
	// failure to a genuine nil interface so Wails' default logger takes over.
	var wailsLogger wailslogger.Logger
	if wlog != nil {
		wailsLogger = wlog
	}

	app := desktop.NewApp()
	app.SetWailsLogger(wlog)
	// Record the stderr-observation outcome BEFORE the startup phases read it:
	// the FrontendAPI init derives the Settings RPC's running-policy snapshot
	// from the same fact the wails.Run options below consume.
	app.SetWebviewGpuStderrObserved(stderrObserved)
	// Next-start post-mortem: a previous fresh-auto run that GDK killed on a
	// fatal Wayland protocol error never reached the watchdog's DomReady
	// scan; persist the fallback now, before the policy below is resolved.
	desktop.ApplyWebviewGpuCrashFallback(agentDir, logDir, stderrObserved, slog.Default())
	// NVIDIA Wayland explicit sync breaks GTK3's mixed GL/shm surface under
	// the accelerated policy (fatal protocol error before DomReady); must be
	// set before wails.Run initializes GTK/EGL.
	desktop.ApplyNvidiaExplicitSyncWorkaround(slog.Default())
	// Arm the Linux DMABUF-failure watchdog (issue #34) after crash capture —
	// the stderr offset snapshot needs the redirected fds — and before
	// wails.Run, whose webview must not render first. Nil off Linux and
	// whenever detection does not apply.
	app.SetWebviewGpuWatchdog(desktop.NewWebviewGpuWatchdog(agentDir, logDir, stderrObserved, slog.Default()))

	// Hand the web-access sidecar (ADR-101) the same SPA tree the Wails
	// asset server serves, resolved once here where the embed lives.
	// Best-effort: a missing tree only degrades the sidecar to API+WS; it
	// must never block the desktop app.
	if dist, subErr := fs.Sub(assets, "frontend/dist"); subErr == nil {
		app.SetWebAccessStaticFS(dist)
	} else {
		slog.Warn("web access: embedded frontend tree unavailable; sidecar will serve API/WS only", "error", subErr)
	}

	// Restore the persisted window size (written on resize/shutdown) so the
	// app reopens at the size the user left it; first run returns the
	// built-in 1400x900 default. The maximized flag is re-applied in Startup
	// once the Wails context exists.
	windowBounds := desktop.LoadWindowBounds(agentDir)

	runErr := wails.Run(&options.App{
		Title:            "c0wrk",
		Width:            windowBounds.Width,
		Height:           windowBounds.Height,
		MinWidth:         1024,
		MinHeight:        600,
		BackgroundColour: options.NewRGB(40, 44, 52),
		// The window is deliberately NOT created hidden: Wails queues the
		// StartHidden hide on the platform UI loop *after* the webview starts
		// loading, while OnStartup already runs concurrently — a fast backend
		// start's WindowShow queues ahead of the hide, the hide executes
		// last, and the window stays withdrawn for the life of the process.
		// Starting visible removes the race.
		AssetServer: &assetserver.Options{
			Assets: assets,
			// Strict CSP on every asset response (production builds only —
			// the dev variant in desktop/csp_dev.go is a no-op so it never
			// breaks the Vite/HMR pipeline). Defense in depth for injected
			// theme CSS: even a future sanitizer bypass cannot fetch anything.
			Middleware: desktop.CSPMiddleware(),
		},
		// Native file-drop emits dropped files' absolute paths to the frontend
		// (files:dropped, wired in Startup). DisableWebViewDrop prevents the
		// webview from navigating to/opening the dropped file — paths ride
		// only the Go event, never a navigation target.
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		// Linux-only webview GPU policy; resolution order and the auto
		// demotion rules: specs/domains/webview-gpu-policy.md. Env override
		// → persisted Settings choice (webview_state.json) → auto
		// (on-demand; never-fallback when the watchdog persisted one or this
		// run's stderr is unobservable). Nil on macOS/Windows.
		Linux: desktop.WebviewGpuPolicyOptions(agentDir, stderrObserved, slog.Default()),
		// Single instance (issue #98, ADR-075): a LaunchServices-triggered
		// second open must relay to THIS instance and exit, never boot a
		// full second app over the same SQLite DB, embedded-LLM supervisor
		// and watchers. The relay focuses the existing window; the
		// notification's session routing is not recoverable on this path.
		SingleInstanceLock: desktop.SingleInstanceOptions(app),
		OnStartup:          app.Startup,
		OnDomReady:         app.DomReady,
		OnShutdown:         app.Shutdown,
		// Close guard: every quit path funnels through this hook; with live
		// session work it intercepts the quit (app:exit_requested → frontend
		// confirmation modal; ConfirmExit re-issues with a bypass flag).
		// See desktop/exit_guard.go.
		OnBeforeClose: app.ShouldPreventClose,
		Bind: []any{
			app,
		},
		Debug: options.Debug{
			OpenInspectorOnStartup: os.Getenv("C0WRK_DEBUG") != "",
		},
		Logger:   wailsLogger,
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
