package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// pendingConfirmData holds the state for a pending tool confirmation,
// including metadata needed for on-demand judge evaluation.
type pendingConfirmData struct {
	ch          chan sdktools.ConfirmationResponse
	taskContext string
	toolName    string
	input       json.RawMessage
	sessionID   string
	reasoning   string
	toolCallID  string // tool_call_id of the triggering tool_call (for precise frontend correlation)
}

// pendingStepLimitEntry wraps the step-limit response channel with the
// session ID, so GetPendingActions can filter by session.
type pendingStepLimitEntry struct {
	ch        chan agent.StepLimitResponse
	sessionID string
	payload   session.StepLimitPayload
}

// pendingAskUserEntry wraps the ask-user response channel with the
// session ID and payload, so GetPendingActions can filter by session.
type pendingAskUserEntry struct {
	ch        chan coretools.AskUserResponse
	sessionID string
	payload   session.AskUserPayload
}

// pendingPlanApprovalEntry wraps the plan-approval response channel with
// the session ID and payload, so GetPendingActions can filter by session.
type pendingPlanApprovalEntry struct {
	ch        chan planApprovalResponse
	sessionID string
	payload   session.PlanApprovalPayload
}

// pendingGoalProposalEntry wraps the goal-proposal response channel with the
// session ID and payload, so GetPendingActions can filter by session and the
// goal-proposal callback can resolve via the event or RPC paths.
type pendingGoalProposalEntry struct {
	ch        chan goalProposalResponse
	sessionID string
	payload   session.GoalProposalPayload
}

// wakeReloadDelay is how long deferredWakeReload waits between detecting a
// power-state wake and re-navigating the frontend. The reload cannot run
// inline inside the NSWorkspaceDidWake observer block: on macOS 26 the
// notification fires mid-resume while the OS is still bringing the web-content
// process back, and calling WindowReloadApp synchronously at that point races
// the OS's own process restoration and silently kills the app. Deferring past
// the resume window (~1.5s) moves the reload off the notification callback so
// it no longer collides with the OS, while still refreshing a
// suspended-but-alive render surface. See ADR-018.
const wakeReloadDelay = 1500 * time.Millisecond

// deferredWakeReload re-navigates the frontend to the start URL after a
// power-state wake, with a delay and context-liveness guards so it is safe to
// call from the synchronous NSWorkspace wake observer (which runs on the main
// thread during resume). It:
//   - returns immediately to the caller (it spawns a goroutine);
//   - waits wakeReloadDelay, but cancels early if the app context is done
//     (shutdown), via select;
//   - re-checks a.ctx.Err() once more after the wait, so a shutdown that
//     began during the delay never triggers a reload of a torn-down context.
//
// The delay is essential: calling the reload inline inside the wake observer
// races the OS's mid-resume web-content process and silently kills the app.
// The 10-second debounce (lastWake) is applied by the caller and is
// independent of this delay.
//
// reloadFrontend issues a NATIVE -[WKWebView reload]. If the web-content
// process actually died (rather than just suspending), the runtime-injected
// -webViewWebContentProcessDidTerminate: hook (powerstate_darwin.go) has
// already reloaded via -[WKWebView reload]; the native reload here is then a
// harmless second reload of the same view.
func (a *App) deferredWakeReload() {
	if a.ctx == nil {
		return
	}
	ctx := a.ctx
	go func() {
		timer := time.NewTimer(wakeReloadDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return // app is shutting down — cancel the deferred reload
		case <-timer.C:
		}
		// Re-check after the wait: teardown may have begun during the delay.
		if err := ctx.Err(); err != nil {
			return
		}
		log := a.log()
		log.Info("power-state wake detected; reloading frontend to restore UI")
		a.reloadFrontend(ctx)
	}()
}

// reloadFrontend re-navigates the frontend after a power-state wake. It prefers
// a NATIVE -[WKWebView reload] (reloadWebviewNative) over Wails's
// WindowReloadApp: Wails's helper is evaluateJavaScript("window.location.href
// = startURL") (Wails v2.12.0 darwin frontend.go:214, WailsContext.m:451),
// which cannot restore a post-wake blank webview — it is a no-op when the URL
// is unchanged and cannot run until the SUSPENDED web-content process fully
// resumes. Because this machine's sleep suspends rather than kills the process,
// -webViewWebContentProcessDidTerminate: does NOT fire on wake, so a native
// reload is the only reliable recovery primitive. On non-darwin builds
// reloadWebviewNative returns false and WindowReloadApp is used (the bug is
// macOS/WKWebView-specific). Tests inject a fake via a.reloadAppFn, which
// takes precedence over both paths. Mirrors the a.wailsEmit override pattern.
func (a *App) reloadFrontend(ctx context.Context) {
	if a.reloadAppFn != nil {
		a.reloadAppFn(ctx)
		return
	}
	if reloadWebviewNative() {
		return
	}
	wailsRuntime.WindowReloadApp(ctx)
}

// Startup is called when the Wails app starts.
func (a *App) Startup(ctx context.Context) {
	// Catch any unrecovered panic during startup so a stack trace lands in
	// the session log (if available) or the wails.log fallback before exit.
	defer func() {
		if r := recover(); r != nil {
			a.log().Error("unrecovered panic during startup",
				"panic", r,
				"stack", string(debug.Stack()),
			)
			panic(r) // re-panic so Wails can still tear down gracefully
		}
	}()

	// ══════════════════════════════════════════════════════════════════
	// STARTUP MANIFEST — Critical Path Time Budget: <500ms (subsequent runs)
	//
	// NOTE: On first run, Phase 2 may take 3–10 minutes while the
	// tool-manager downloads and installs required external tools
	// (rg, uv, markitdown). Subsequent runs skip via .versions check.
	//
	// CRITICAL PATH (blocks UI):
	//   Phase 1: shell_env + logger      — budget: 50ms
	//   Phase 2: config + tools          — budget: 100ms (parallel)
	//   Phase 3: database + terminal     — budget: 100ms (parallel)
	//   Phase 4: stores + preload        — budget: 100ms
	//   Phase 5: application + api       — budget: 150ms
	//   → EventBackendReady emitted here ←
	//
	// BACKGROUND (non-blocking, starts after EventBackendReady):
	//   - ONNX embedder + vector index manager
	//   - MCP gateway (already async inside builder)
	//   - LLM router (already async inside builder)
	//
	// RULES:
	//   1. New subsystems MUST go in BACKGROUND unless they absolutely
	//      require completion before EventBackendReady.
	//   2. Any change that adds >50ms to a critical phase MUST be
	//      justified in a code review comment.
	//   3. Run with LOG_LEVEL=INFO to see per-phase timing.
	//
	// Each phase body lives as a method on *App in startup_phases.go (W-24).
	// ══════════════════════════════════════════════════════════════════

	startTime := time.Now()
	a.ctx = ctx

	// ── Phase 0: Resolve agent directory (needed for logger paths) ──
	// Compute early so logger.Init can write to the correct directory.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	agentDir := filepath.Join(homeDir, config.DefaultAgentDir)
	logDir := config.LogsDir(agentDir)

	// ── Phase 1: Shell Environment + Logger ──────────────────────────
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// MUST run before any config/env-var resolution.
	config.LoadShellEnvironment(nil)
	log, sessionLogger := a.initLogger(logDir)
	a.sessionLogger = sessionLogger // stored for cleanup on early Startup exits (W1)
	log.Info("startup phase complete", "phase", "logger", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 2: Config + Dependencies + Tools (parallel) ─────────────
	// On first run, tool downloads may take 3–10 minutes. Subsequent runs
	// check the .versions file and return in <100ms.
	resolved, toolsBinPath, toolsInstalled, toolsOK := a.initConfigAndDeps(ctx, log)
	log.Info("startup phase complete", "phase", "config", "elapsed_ms", time.Since(startTime).Milliseconds(), "tools_installed", toolsInstalled)
	if !toolsOK {
		return
	}

	// Prepend managed tools/bin/ to PATH so subsequent exec.CommandContext
	// calls (rg, markitdown) resolve to the managed binaries.
	a.toolsBinPath = toolsBinPath
	if toolsBinPath != "" {
		os.Setenv("PATH", toolsBinPath+string(os.PathListSeparator)+os.Getenv("PATH")) //nolint:errcheck // Setenv error is non-actionable at startup
		log.Info("tools/bin prepended to PATH", "path", toolsBinPath)
	}

	cfg := resolved.Config
	configPath := resolved.ConfigPath
	configLoadErrors := resolved.LoadErrors
	// agentDir and logDir are already computed in phase 0

	logLevel := cfg.LogLevel
	log, sessionLogger = a.maybeReinitLogger(logLevel, sessionLogger, log, logDir)

	// ── Phase 3: Database + Terminal Manager (parallel) ───────────────
	dbPath := config.DatabasePath(agentDir)
	var db *sql.DB
	var termManager *terminal.Manager
	var phase3 sync.WaitGroup
	phase3.Add(2)
	safeGo(log, "database", func() {
		defer phase3.Done()
		db = a.initDatabase(dbPath, log)
	})
	safeGo(log, "terminal", func() {
		defer phase3.Done()
		termManager = a.initTerminalManager(log)
	})
	phase3.Wait()
	log.Info("startup phase complete", "phase", "database", "elapsed_ms", time.Since(startTime).Milliseconds())
	a.db = db

	// ── Phase 4: Stores + Project/Session Preload ────────────────────
	projStore, sessStore, reviewStore := a.initStores(db, log)
	log.Info("startup phase complete", "phase", "stores", "elapsed_ms", time.Since(startTime).Milliseconds())

	var projectMgr *project.Manager
	if projStore != nil {
		projectMgr = project.NewManager(projStore, agentDir)
		// No Project is deferred until after LLM config validation (Phase 5).
		// On a clean first run, we must not create infrastructure before
		// verifying that the app is usable.
	}
	cachedProjects := a.preloadProjectsAndSessions(projectMgr, sessStore, log)
	log.Info("startup phase complete", "phase", "preload", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 5: Callbacks + Application + FrontendAPI ───────────────
	uiEmitFunc := a.buildUIEmitFunc()
	askUserFunc := a.buildAskUserCallback(uiEmitFunc)
	planApprovalFunc := a.buildPlanApprovalCallback(uiEmitFunc)
	goalProposer := a.buildGoalProposalCallback(uiEmitFunc)
	confirmFunc := a.buildConfirmCallback(uiEmitFunc)
	hitlHandler := a.buildStepLimitCallback(uiEmitFunc)

	var vectorMgrPtr atomic.Pointer[vectorindex.Manager]
	vectorReady := make(chan struct{})
	var vectorOnce sync.Once
	vectorSearchFunc, vectorSearchWaitFunc := a.buildVectorCallbacks(&vectorMgrPtr, vectorReady)

	// File-change notification: called by the PostExecuteHook after a
	// file-mutating tool completes. Triggers debounced incremental
	// re-indexing so subsequent searches reflect the change without waiting
	// for the filesystem watcher (which has latency on macOS and may miss
	// same-process writes). Safe to call before the vector manager is ready
	// — the atomic pointer returns nil and the call is a no-op.
	fileChangeNotify := func() {
		if mgr := vectorMgrPtr.Load(); mgr != nil {
			mgr.NotifyFileChange()
		}
	}

	application, err := a.buildApplication(backend.ApplicationConfig{
		Config:               cfg,
		Logger:               log,
		AgentDir:             agentDir,
		SessionStore:         sessStore,
		TaskStore:            sessStore,
		UIEmitFunc:           uiEmitFunc,
		AskUserFunc:          askUserFunc,
		PlanApprovalFunc:     planApprovalFunc,
		GoalProposer:         goalProposer,
		ConfirmFunc:          confirmFunc,
		HITLHandler:          hitlHandler,
		VectorSearchFunc:     vectorSearchFunc,
		VectorSearchWaitFunc: vectorSearchWaitFunc,
		FileChangeNotifyFunc: fileChangeNotify,
	}, log, startTime)
	if err != nil {
		return
	}

	// Register the goal-proposal resolver so the RPC-based path
	// (FrontendAPI.ConfirmGoal/CancelGoal) and the event-based path both
	// funnel through a single resolution on the desktop pending map.
	application.Manager().SetGoalProposalResolver(func(requestID, decision, condition, verify, verificationMode, clarification string) bool {
		return a.resolveGoalProposal(requestID, decision, condition, verify, verificationMode, clarification)
	})

	a.buildFrontendAPI(application, backend.FrontendAPIConfig{
		App:             application,
		Logger:          log,
		Config:          cfg,
		ConfigPath:      configPath,
		Store:           sessStore,
		ProjStore:       projStore,
		ReviewStore:     reviewStore,
		SessionLogger:   sessionLogger,
		LogLevel:        logLevel,
		ProjectManager:  projectMgr,
		AgentDir:        agentDir,
		VectorManager:   nil, // set lazily once background init completes
		TerminalManager: termManager,
		EmitEvent: func(eventName string, data ...any) {
			a.emit(eventName, data...)
		},
		AppCtx: func() context.Context {
			return a.ctx
		},
	}, configLoadErrors, projStore, log, startTime)

	a.wireWailsEventListeners(log, uiEmitFunc)

	// ── Session restoration resolver ─────────────────────────────────
	// Enable lazy session restoration from the database by wiring a
	// project resolver that maps project IDs to workspace paths. Without
	// this, sessions created in previous launches cannot be restored on
	// demand, and GetSessionWorkspace falls back to the project-level
	// directory which is incorrect for No Project (per-session isolation).
	if projectMgr != nil {
		application.Manager().SetProjectResolver(func(projectID string) (string, error) {
			p, err := projectMgr.GetProject(projectID)
			if err != nil {
				return "", fmt.Errorf("resolving project %s: %w", projectID, err)
			}
			if p == nil {
				return "", fmt.Errorf("project not found: %s", projectID)
			}
			return p.WorkspacePath, nil
		})
	}

	// ── Ensure No Project (only when LLM is configured) ──────────────
	// On a clean first run with no config, we must not create projects
	// or sessions — the frontend will show the settings dialog instead.
	if cfg.LLM.DefaultModel != "" && projectMgr != nil {
		created, err := projectMgr.EnsureNoProject()
		if err != nil {
			log.Warn("failed to ensure No Project", "error", err)
		} else if created {
			// Refresh cached list so emitBackendReady includes No Project.
			if projects, pErr := projectMgr.ListProjects(); pErr == nil {
				cachedProjects = projects
			} else {
				log.Warn("failed to refresh project list after EnsureNoProject", "error", pErr)
			}
		}
	}

	// ── EventBackendReady ────────────────────────────────────────────
	// filterNoProject=true delegates No Project stripping to emitBackendReady
	// when LLM is unconfigured.
	a.emitBackendReady(cachedProjects, projectMgr, cfg.LLM.DefaultModel == "", log)
	log.Info("startup complete \u2014 backend ready", "total_elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 6: macOS power-state wake recovery ──────────────────────
	// On macOS 26 (Tahoe) the WKWebView's rendering surface is left blank
	// after the system or the displays wake from sleep: the web content
	// process is suspended/killed by the OS while the Go backend keeps
	// running. Wails v2 does not forward
	// webViewWebContentProcessDidTerminate, so without this the window would
	// stay blank until a manual restart. Reload the frontend on wake — the
	// frontend is designed to survive a full reload (sessionRuntime state
	// reconciliation + the on-mount listProjects() safety-net RPC), and the
	// Go backend (DB, stores, sessions) persists across it. Rationale: see specs/decisions/018-macos-webview-recovery.md.
	//
	// The reload is deferred (deferredWakeReload) rather than invoked inline:
	// the NSWorkspaceDidWake observer block runs synchronously on the main
	// thread mid-resume, while the OS is still restoring the web-content
	// process. Calling the reload inline at that point races the OS and
	// silently kills the app. deferredWakeReload spawns a goroutine that waits
	// wakeReloadDelay, is cancellable via a.ctx.Done() (shutdown), and
	// re-checks a.ctx.Err() before reloading.
	//
	// Both paths use a NATIVE -[WKWebView reload] (reloadFrontend on the wake
	// path; the IMP in powerstate_darwin.go on the death path). Wails's
	// WindowReloadApp is an evaluateJavaScript("window.location.href =
	// startURL") call that cannot restore a post-wake blank webview (no-op
	// when URL unchanged; cannot run until the suspended process resumes), so
	// it is used only as a non-darwin fallback. The wake path catches process
	// SUSPENSION (alive but blank render surface); the runtime-injected
	// -webViewWebContentProcessDidTerminate: hook catches process DEATH. If
	// the process died, the hook has already reloaded; the wake-path native
	// reload is then a harmless second reload of the same view.
	var lastWake atomic.Int64
	registerPowerWakeObserver(log, func() {
		now := time.Now().UnixNano()
		// Debounce: a single wake can post both NSWorkspaceDidWake and
		// NSWorkspaceScreensDidWake. Skip repeats within 10s.
		if now-lastWake.Load() < int64(10*time.Second) {
			return
		}
		lastWake.Store(now)
		// Deferred, context-cancellable reload — never block the observer.
		a.deferredWakeReload()
	})

	// Store vector manager pointer for Shutdown fallback check (W3).
	a.vectorMgrPtr = &vectorMgrPtr

	// ── Background: Vector Index ─────────────────────────────────────
	a.startVectorIndexBackground(agentDir, cfg, &vectorMgrPtr, vectorReady, &vectorOnce, startTime, log)
}

// Shutdown is called when the Wails app is shutting down.
func (a *App) Shutdown(ctx context.Context) {
	// Drain all pending confirmation/ask-user/step-limit channels so that
	// blocked goroutines can exit cleanly instead of leaking.
	a.pendingConfirmations.Range(func(key, value any) bool {
		if pd, ok := value.(*pendingConfirmData); ok {
			select {
			case pd.ch <- sdktools.ConfirmDenyAndStop:
			default:
			}
		}
		a.pendingConfirmations.Delete(key)
		return true
	})
	a.pendingAskUser.Range(func(key, value any) bool {
		if e, ok := value.(*pendingAskUserEntry); ok {
			select {
			case e.ch <- coretools.AskUserResponse{}:
			default:
			}
		}
		a.pendingAskUser.Delete(key)
		return true
	})
	a.pendingStepLimit.Range(func(key, value any) bool {
		if e, ok := value.(*pendingStepLimitEntry); ok {
			select {
			case e.ch <- agent.StepLimitDeny:
			default:
			}
		}
		a.pendingStepLimit.Delete(key)
		return true
	})
	a.pendingPlanApprovals.Range(func(key, value any) bool {
		if e, ok := value.(*pendingPlanApprovalEntry); ok {
			select {
			case e.ch <- planApprovalResponse{Decision: "abandon"}:
			default:
			}
		}
		a.pendingPlanApprovals.Delete(key)
		return true
	})
	a.pendingGoalProposals.Range(func(key, value any) bool {
		if e, ok := value.(*pendingGoalProposalEntry); ok {
			select {
			case e.ch <- goalProposalResponse{Decision: "cancel"}:
			default:
			}
		}
		a.pendingGoalProposals.Delete(key)
		return true
	})

	if a.sessionLogger != nil {
		_ = a.sessionLogger.Close()
	}

	// Ensure vector manager set by background init is visible to Cleanup (W3).
	// The background init goroutine calls vectorMgrPtr.Store then SetVectorManager
	// in sequence; this check catches the narrow window where Store has run but
	// SetVectorManager has not, so Cleanup won't miss it.
	if a.vectorMgrPtr != nil {
		if mgr := a.vectorMgrPtr.Load(); mgr != nil {
			a.Lifecycle().SetVectorManager(mgr)
		}
	}

	if a.FrontendAPI != nil {
		a.Lifecycle().Cleanup()
	}

	// Wait for in-flight judge goroutines before tearing down the backend (W2).
	a.judgeWG.Wait()

	if a.app != nil {
		a.app.Shutdown()
	}

	if a.db != nil {
		if err := a.db.Close(); err != nil {
			a.log().Error("failed to close database", "error", err)
		}
	}
}

// wireWailsEventListeners subscribes to Wails frontend events for tool confirmations,
// judge requests, ask-user responses, and step-limit responses. Each handler
// body is extracted into desktop/event_handlers.go (W-23) so it can be
// unit-tested without a running Wails runtime.
func (a *App) wireWailsEventListeners(log *slog.Logger, uiEmitFunc func(session.Event)) {
	wailsRuntime.EventsOn(a.ctx, backend.EventToolConfirmResponse, func(data ...any) {
		payload, ok := extractPayload("tool confirmation response", data, log)
		if !ok {
			return
		}
		a.handleToolConfirmResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.ctx, backend.EventToolJudgeRequest, func(data ...any) {
		payload, ok := extractPayload("tool judge request", data, log)
		if !ok {
			return
		}
		a.handleToolJudgeRequest(payload, uiEmitFunc, log)
	})

	wailsRuntime.EventsOn(a.ctx, backend.EventAskUserResponse, func(data ...any) {
		payload, ok := extractPayload("ask_user response", data, log)
		if !ok {
			return
		}
		a.handleAskUserResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.ctx, backend.EventStepLimitResponse, func(data ...any) {
		payload, ok := extractPayload("step_limit response", data, log)
		if !ok {
			return
		}
		a.handleStepLimitResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.ctx, backend.EventPlanApprovalResponse, func(data ...any) {
		payload, ok := extractPayload("plan_approval response", data, log)
		if !ok {
			return
		}
		a.handlePlanApprovalResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.ctx, backend.EventGoalProposalResponse, func(data ...any) {
		payload, ok := extractPayload("goal_proposal response", data, log)
		if !ok {
			return
		}
		a.handleGoalProposalResponse(payload, log)
	})
}

// resolveModelPath checks the app bundle Resources/models/ first, then <agentDir>/models/.
// Returns empty string if the file is not found in either location.
func resolveModelPath(filename, agentDir string) string {
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		// macOS app bundle: <app>/Contents/MacOS/../Resources/models/<filename>
		bundlePath := filepath.Join(exeDir, "..", "Resources", "models", filename)
		if _, statErr := os.Stat(bundlePath); statErr == nil {
			return bundlePath
		}

		// Flat layout (Linux/Windows): models/ next to the binary
		flatPath := filepath.Join(exeDir, "models", filename)
		if _, statErr := os.Stat(flatPath); statErr == nil {
			return flatPath
		}
	}

	// Fallback: <agentDir>/models/<filename>
	userPath := filepath.Join(config.ModelsDir(agentDir), filename)
	if _, statErr := os.Stat(userPath); statErr == nil {
		return userPath
	}

	return ""
}

// resolveONNXLibPath finds the ONNX Runtime shared library next to the executable.
func resolveONNXLibPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exePath)

	var libName string
	switch runtime.GOOS {
	case "darwin":
		libName = "libonnxruntime.dylib"
	case "linux":
		libName = "libonnxruntime.so"
	case "windows":
		libName = "onnxruntime.dll"
	default:
		return ""
	}

	libPath := filepath.Join(exeDir, libName)
	if _, statErr := os.Stat(libPath); statErr == nil {
		return libPath
	}
	return ""
}
