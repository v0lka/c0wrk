package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// pendingConfirmData holds the state for a pending tool confirmation,
// including metadata needed for on-demand judge evaluation.
type pendingConfirmData struct {
	ch           chan sdktools.ConfirmationResponse
	taskContext  string
	toolName     string
	input        json.RawMessage
	sessionID    string
	reasoning    string
	toolCallID   string // tool_call_id of the triggering tool_call (for precise frontend correlation)
	disableJudge bool   // true when strict judge already ran (Smart Approve) — hide advisory Ask Agent
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

// pendingGoalSuggestionEntry mirrors pendingGoalProposalEntry for the
// post-routing goal-mode suggestion card.
type pendingGoalSuggestionEntry struct {
	ch        chan goalSuggestionResponse
	sessionID string
	payload   session.GoalSuggestionPayload
}

// wakeReloadDelay defers the post-wake reload past the OS's resume window, so
// it no longer races the mid-resume web-content restoration on macOS 26.
// See specs/decisions/018-macos-webview-recovery.md.
const wakeReloadDelay = 1500 * time.Millisecond

// deferredWakeReload re-navigates the frontend after a power-state wake;
// safe to call from the synchronous wake observer (main thread, mid-resume):
// it spawns a goroutine, waits wakeReloadDelay (cancellable via the app
// context) and re-checks the context before reloading, so a shutdown during
// the delay never reloads a torn-down webview. The caller applies a separate
// 10s debounce. Design rationale: specs/decisions/018-macos-webview-recovery.md.
func (a *App) deferredWakeReload() {
	if a.wailsCtx() == nil {
		return
	}
	ctx := a.wailsCtx()
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

// reloadFrontend re-navigates the frontend after a wake: the NATIVE
// -[WKWebView reload] first (the only primitive that revives a
// suspended/blank webview — WindowReloadApp is a no-op when the URL is
// unchanged and cannot run until the suspended process resumes), falling
// back to Wails's helper on non-darwin; a.reloadAppFn (test seam) takes
// precedence over both. Full rationale in
// specs/decisions/018-macos-webview-recovery.md.
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

// DomReady is the Wails OnDomReady hook: it fires after Wails has finished
// window setup, making it the one reveal that can never be overtaken by that
// setup (reveal ordering documented in specs/architecture/data-flow.md).
// It fires again on every frontend reload — wake recovery only, where
// raise/focus is the expected outcome.
func (a *App) DomReady(ctx context.Context) {
	a.log().Debug("frontend DOM ready")
	a.showWindow(ctx)
	a.startWebviewGpuWatchdog()
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

	// Phase bodies live as methods in startup_phases.go; the phase map,
	// budgets and the background-vs-critical rule are documented in
	// specs/architecture/data-flow.md. Exception to the budget: first-run
	// Phase 2 may block for minutes on the tool-manager install (ADR-100).

	startTime := time.Now()

	// Capture GOMEMLIMIT as the runtime saw it at PROCESS START: Phase 1's
	// shell-env load can later inject a value the runtime never applied —
	// setupMemorySoftLimit must decide on the runtime's value only (see memlimit.go).
	runtimeSeenGomemlimit := os.Getenv("GOMEMLIMIT")

	a.setContext(ctx)

	// ── Restore maximized window state ───────────────────────────────
	// The size is restored by main.go's wails.Run options; the maximized
	// flag cannot be expressed there, so it is re-applied once the context
	// exists. Best-effort.
	a.restoreMaximizedWindow()

	// Native file-drop listener: the sole delivery channel for dropped paths
	// (the webview's own drop is disabled) — see specs/contracts/event-catalog.md
	// (files:dropped); a.emit's ctx guard drops a late teardown drop harmlessly.
	wailsRuntime.OnFileDrop(ctx, func(x, y int, paths []string) {
		if len(paths) == 0 {
			return
		}
		a.emit(backend.EventFilesDropped, map[string]any{
			"paths": paths,
			"x":     x,
			"y":     y,
		})
	})

	// ── Phase 0: Resolve agent directory (needed for logger paths) ──
	// Compute early so logger.Init can write to the correct directory.
	agentDir := config.AgentDir()
	logDir := config.LogsDir(agentDir)

	// ── Phase 1: Shell Environment + Logger ──────────────────────────
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// MUST run before any config/env-var resolution.
	config.LoadShellEnvironment(nil)
	log, sessionLogger := a.initLogger(logDir)
	a.setSessionLogger(sessionLogger) // stored for cleanup on early Startup exits (W1)

	log.Info("startup phase complete", "phase", "logger", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 2: Config + Dependencies + Tools (parallel) ─────────────
	resolved, toolsBinPath, toolsInstalled, toolsOK := a.initConfigAndDeps(ctx, log)
	log.Info("startup phase complete", "phase", "config", "elapsed_ms", time.Since(startTime).Milliseconds(), "tools_installed", toolsInstalled)
	if !toolsOK {
		// Same empty-seed contract as the buildApplication failure return
		// below — a failed first-run tool download is a supported outcome.
		_ = a.frontendAPI().Lifecycle().Init(backend.FrontendAPIConfig{})
		return
	}

	// Prepend managed tools/bin/ to PATH so subsequent exec.CommandContext
	// calls (rg, markitdown) resolve to the managed binaries.
	a.toolsBinPath = toolsBinPath
	if toolsBinPath != "" {
		newPath := toolsBinPath + string(os.PathListSeparator) + os.Getenv("PATH")
		if err := os.Setenv("PATH", newPath); err != nil {
			log.Warn("failed to prepend tools/bin to PATH", "error", err)
		}
		log.Info("tools/bin prepended to PATH", "path", toolsBinPath)
	}

	cfg := resolved.Config
	configPath := resolved.ConfigPath
	configLoadErrors := resolved.LoadErrors
	// agentDir and logDir are already computed in phase 0

	// Arm the hard shutdown deadline before any teardown path can run; the
	// same mapping EffectiveShutdownDeadline hands the GPU-relaunch handover.
	a.setShutdownHardDeadline(EffectiveShutdownDeadline(cfg.Shutdown.HardDeadline))

	logLevel := cfg.LogLevel
	log, sessionLogger = a.maybeReinitLogger(logLevel, sessionLogger, log, logDir)

	// Crash forensics: report a surviving liveness marker now — after the
	// logger reinit, so the warning lands in THIS run's session log. See
	// specs/domains/crash-logging.md.
	crashlog.ReportUncleanShutdown(log, logDir)

	// ── GC memory soft limit (runtime.memory_soft_limit_mb) ──────────
	// Set after config + logger re-init (the decision must land in the right
	// log) and before background indexing, so it governs the very first
	// indexing pass. Priority rules in memlimit.go.
	setupMemorySoftLimit(cfg.Runtime.MemorySoftLimitMB, runtimeSeenGomemlimit, log)

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
		termManager = a.initTerminalManager(log, cfg.Terminal.Env)
	})
	phase3.Wait()
	log.Info("startup phase complete", "phase", "database", "elapsed_ms", time.Since(startTime).Milliseconds())
	a.setDatabase(db)

	// ── Phase 4: Stores + Project/Session Preload ────────────────────
	projStore, sessStore, reviewStore := a.initStores(db, log)
	log.Info("startup phase complete", "phase", "stores", "elapsed_ms", time.Since(startTime).Milliseconds())

	var projectMgr *project.Manager
	if projStore != nil {
		projectMgr = project.NewManager(projStore, agentDir, log)
		// No Project is deferred until after LLM config validation (Phase 5):
		// a clean first run must not create infrastructure before the app is
		// known usable.
	}
	cachedProjects := a.preloadProjectsAndSessions(projectMgr, sessStore, log)
	log.Info("startup phase complete", "phase", "preload", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 5: Callbacks + Application + FrontendAPI ───────────────
	uiEmitFunc := a.buildUIEmitFunc()
	askUserFunc := a.buildAskUserCallback(uiEmitFunc)
	planApprovalFunc := a.buildPlanApprovalCallback(uiEmitFunc)
	goalProposer := a.buildGoalProposalCallback(uiEmitFunc)
	goalSuggestionProposer := a.buildGoalSuggestionCallback(uiEmitFunc)
	confirmFunc := a.buildConfirmCallback(uiEmitFunc)
	hitlHandler := a.buildStepLimitCallback(uiEmitFunc)

	// Bound for vector-search readiness waits (vector_index.search_wait_
	// timeout_ms; unset → 3000 via config defaults, explicit 0 = fail fast),
	// governing both the semantic_search callbacks and the RAG-hint wait.
	vectorSearchWaitTimeout := time.Duration(derefInt(cfg.VectorIndex.SearchWaitTimeoutMs)) * time.Millisecond
	// Explicit fail-fast flag: a bare zero reads as "unset" (3s default) in
	// the orchestrator layer, so the user's explicit 0 must be carried
	// separately.
	vectorSearchWaitDisabled := cfg.VectorIndex.SearchWaitTimeoutMs != nil && *cfg.VectorIndex.SearchWaitTimeoutMs == 0

	// File-change notification after a file-mutating tool completes: debounced
	// incremental re-indexing (the watcher has latency and may miss
	// same-process writes). The executor context carries the session's
	// workspace root (ADR-080), so a background session refreshes its own
	// tree; a no-op without a live manager.
	fileChangeNotify := func(ctx context.Context) {
		a.frontendAPI().Lifecycle().NotifyVectorFileChange(sdktools.WorkspacePathFrom(ctx))
	}

	// Emitted alongside fileChangeNotify so frontend surfaces refresh their
	// view after agent-initiated writes.
	fileChangedWorkspaceEmitter := func() {
		a.emit("workspace:tree_changed")
	}

	application, err := a.buildApplication(backend.ApplicationConfig{
		Config:                      cfg,
		Logger:                      log,
		AgentDir:                    agentDir,
		SessionStore:                sessStore,
		TaskStore:                   sessStore,
		UIEmitFunc:                  uiEmitFunc,
		AskUserFunc:                 askUserFunc,
		PlanApprovalFunc:            planApprovalFunc,
		GoalProposer:                goalProposer,
		ConfirmFunc:                 confirmFunc,
		HITLHandler:                 hitlHandler,
		VectorSearchWaitTimeout:     vectorSearchWaitTimeout,
		VectorSearchWaitDisabled:    vectorSearchWaitDisabled,
		FileChangeNotifyFunc:        fileChangeNotify,
		FileChangedWorkspaceEmitter: fileChangedWorkspaceEmitter,
	}, log, startTime)
	if err != nil {
		// Publish an EMPTY seed before returning: seedPublished is what admits
		// the guardless RPCs — without it, a window whose Startup failed here
		// leaves every one of them spinning forever in seedAcquire.
		_ = a.frontendAPI().Lifecycle().Init(backend.FrontendAPIConfig{})
		return
	}

	// One resolution funnel per prompt kind, shared by the event and RPC paths.
	application.Manager().SetGoalProposalResolver(func(requestID, decision, condition, verify, verificationMode string) bool {
		return a.resolveGoalProposal(requestID, decision, condition, verify, verificationMode)
	})
	application.SetGoalSuggestionProposer(goalSuggestionProposer)
	application.Manager().SetGoalSuggestionResolver(func(requestID, decision string) bool {
		return a.resolveGoalSuggestion(requestID, decision)
	})

	a.buildFrontendAPI(application, backend.FrontendAPIConfig{
		App:                      application,
		Logger:                   log,
		Config:                   cfg,
		ConfigPath:               configPath,
		Store:                    sessStore,
		ProjStore:                projStore,
		ReviewStore:              reviewStore,
		SessionLogger:            sessionLogger,
		LogLevel:                 logLevel,
		ProjectManager:           projectMgr,
		AgentDir:                 agentDir,
		TerminalManager:          termManager,
		WebviewGpuStderrObserved: a.stderrObserved(),
		EmitEvent: func(eventName string, data ...any) {
			a.emit(eventName, data...)
		},
		AppCtx: func() context.Context {
			return a.wailsCtx()
		},
		QuitApp: func() {
			// Reached only from ApplyUpdate after the staged updater is
			// launched — arm the marker first so an intercepted quit can be
			// flagged as self-update context.
			a.markUpdateQuit()
			wailsRuntime.Quit(a.wailsCtx())
		},
	}, configLoadErrors, projStore, log, startTime)

	// Late-bind the per-root vector registry (ADR-081): the search closures
	// resolve it at call time (session workspace root for agent calls,
	// Git-panel focus for user search). See
	// specs/decisions/081-vector-per-root-registry.md.
	application.SetVectorRoots(a.frontendAPI().Lifecycle().VectorRoots())

	// Vector-index launcher (restartable background ONNX init): the startup
	// launch fires in Phase 6 below; UpdateIndexingSettings's enabled
	// transitions reach the same launcher through the backend hook —
	// enabled=false invalidates an in-flight init before the registry is
	// parked, enabled=true relaunches a fresh embedder + factory.
	vectorLauncher := newVectorIndexLauncher(a, agentDir, log)
	a.frontendAPI().Lifecycle().SetVectorIndexEnabledHook(vectorLauncher.applyEnabledChange)

	// ── Embedded local model: restore only (see initEmbeddedLLM) ────
	a.initEmbeddedLLM(log)

	// ── ChatGPT subscription auth: keychain restore only (see initChatGPTAuth) ──
	a.initChatGPTAuth(log)

	a.wireWailsEventListeners(log, uiEmitFunc)

	// ── Session restoration resolver ─────────────────────────────────
	// Wired inside buildFrontendAPI with a deadline-bounded store read, and
	// deliberately NOT overridden here: a resolver on projectMgr.GetProject
	// (context.Background()) would shadow it and reintroduce the hang.

	// ── Ensure No Project (only when LLM is configured) ──────────────
	// A clean first run must create no projects/sessions — the frontend
	// shows the settings dialog instead.
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

	// ── Web access sidecar (browser mirror, ADR-101; see initWebAccess) ──
	a.initWebAccess(cfg, log)

	log.Info("startup complete \u2014 backend ready", "total_elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 6: macOS power-state wake recovery ──────────────────────
	// On macOS 26 the webview's render surface is left blank after a wake
	// (suspended web-content process; Wails does not forward the termination
	// hook). Reload on wake — the frontend survives a full reload, the Go
	// backend persists across it. The wake path catches SUSPENSION, the
	// injected hook catches DEATH: specs/decisions/018-macos-webview-recovery.md.
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

	// ── Background: MCP Ready notifier ───────────────────────────────
	// Emits EventMCPReady once the MCP gateway startup goroutine finishes so
	// the settings dialog can refresh its transient "Starting…" placeholder.
	a.startMCPReadyNotifier(a.wailsCtx(), log)

	// ── Background: Vector Index ─────────────────────────────────────
	// The launcher owns the epochal guard and a per-launch ready channel;
	// with vector_index.enabled: false at boot, the launch parks the
	// registry and skips the ONNX load entirely.
	vectorLauncher.launch()

	// ── Background: Update check (see startUpdateCheckerBackground) ─
	a.startUpdateCheckerBackground(log)

	// ── Background: git auto-fetch ticker (see startAutoFetchBackground) ──
	a.startAutoFetchBackground()
}

// Shutdown is called when the Wails app is shutting down.
func (a *App) Shutdown(ctx context.Context) {
	// Runs on the main goroutine after the Wails main loop stops — every slow
	// step is a frozen app, so each logs its duration as it completes.
	shutdownStart := time.Now()
	shutdownStep := func(name string) {
		a.log().Info("shutdown step complete",
			"step", name,
			"step_ms", time.Since(shutdownStart).Milliseconds())
	}

	// One hard watchdog over the WHOLE teardown (disarmed by the deferred stop
	// on normal return): a broken cancellation path must not keep the process
	// alive past the deadline. The forced exit rides crashlog.ForceExit so a
	// forced-but-legitimate quit still cleans the liveness marker. See
	// specs/domains/crash-logging.md.
	shutdownExit := func() { crashlog.ForceExit(0) }
	if a.shutdownExitFn != nil {
		shutdownExit = func() { a.shutdownExitFn(0) }
	}
	watchdog := startShutdownWatchdog(a.shutdownDeadline(), a.log(), shutdownExit)
	defer watchdog.stop()

	// Make every quit visible in the session log — without this record the
	// log just ends mid-activity, indistinguishable from a crash.
	a.log().Info("application shutdown: starting graceful shutdown")

	// Flush any events still queued in the batcher and stop its loop before
	// teardown, so a quit never drops the last batch of a run. Events emitted
	// after this point are delivered synchronously.
	a.stopEventBatcher()
	shutdownStep("stopEventBatcher")

	// Release notification service resources (see cleanupNotifications); safe
	// when notifications were never initialized.
	a.cleanupNotifications(ctx)
	shutdownStep("cleanupNotifications")

	// Persist the window-geometry snapshot captured while the window was
	// alive: a direct read here would hit the already-destroyed window on
	// Linux and clobber the correct persisted geometry.
	a.persistWindowBoundsAtShutdown(a.log())
	shutdownStep("saveWindowBounds")

	// Release the model's RAM/VRAM early, while the rest of the teardown is
	// still working (see stopEmbeddedLLM).
	a.stopEmbeddedLLM(ctx)
	shutdownStep("stopEmbeddedLLM")

	// Close the sidecar before the judge drain/store close so connected tabs
	// see their stream end cleanly (see stopWebAccess).
	a.stopWebAccess(ctx)
	shutdownStep("stopWebAccess")

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
	// The goal-suggestion drain signals SHUTDOWN, never a synthetic decline —
	// an app quit is not a user answer (fail-open; see drainPendingGoalSuggestions
	// and specs/domains/goal-mode.md).
	a.drainPendingGoalSuggestions()
	shutdownStep("pendingActionDrains")

	// Close-path reads go through the locked accessors: Shutdown can race a
	// still-running Startup (first-run tool install) — see App's block comment.
	if fa := a.frontendAPI(); fa != nil {
		fa.Lifecycle().Cleanup()
	}
	shutdownStep("frontendAPICleanup")

	// Wait for in-flight judge goroutines before tearing down the backend (W2).
	a.judgeWG.Wait()
	shutdownStep("judgeWGWait")

	if app := a.application(); app != nil {
		app.Shutdown()
	}
	shutdownStep("applicationShutdown")

	if db := a.database(); db != nil {
		if err := db.Close(); err != nil {
			a.log().Error("failed to close database", "error", err)
		}
	}
	shutdownStep("dbClose")

	a.log().Info("application shutdown: complete", "total_ms", time.Since(shutdownStart).Milliseconds())

	// The session log closes only after the last record is written, so the
	// "complete" bracket is persisted rather than dropped into a closed file.
	if sl := a.sessionLog(); sl != nil {
		_ = sl.Close()
	}
}

// drainPendingGoalSuggestions resolves every pending goal-suggestion card
// with the SHUTDOWN signal — an error, not a user decline, so the core gate
// fails open with no session suppression (specs/domains/goal-mode.md).
// Extracted for a direct unit test; the other drains stay inline (their
// deny/cancel responses are the prompts' documented semantics).
func (a *App) drainPendingGoalSuggestions() {
	a.pendingGoalSuggestions.Range(func(key, value any) bool {
		if e, ok := value.(*pendingGoalSuggestionEntry); ok {
			select {
			case e.ch <- goalSuggestionResponse{Shutdown: true}:
			default:
			}
		}
		a.pendingGoalSuggestions.Delete(key)
		return true
	})
}

// wireWailsEventListeners subscribes to the Wails frontend HITL events; each
// handler body lives in desktop/event_handlers.go so it is unit-testable
// without a running Wails runtime.
func (a *App) wireWailsEventListeners(log *slog.Logger, uiEmitFunc func(session.Event)) {
	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventToolConfirmResponse, func(data ...any) {
		payload, ok := extractPayload("tool confirmation response", data, log)
		if !ok {
			return
		}
		a.handleToolConfirmResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventToolJudgeRequest, func(data ...any) {
		payload, ok := extractPayload("tool judge request", data, log)
		if !ok {
			return
		}
		a.handleToolJudgeRequest(payload, uiEmitFunc, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventAskUserResponse, func(data ...any) {
		payload, ok := extractPayload("ask_user response", data, log)
		if !ok {
			return
		}
		a.handleAskUserResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventStepLimitResponse, func(data ...any) {
		payload, ok := extractPayload("step_limit response", data, log)
		if !ok {
			return
		}
		a.handleStepLimitResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventPlanApprovalResponse, func(data ...any) {
		payload, ok := extractPayload("plan_approval response", data, log)
		if !ok {
			return
		}
		a.handlePlanApprovalResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventGoalProposalResponse, func(data ...any) {
		payload, ok := extractPayload("goal_proposal response", data, log)
		if !ok {
			return
		}
		a.handleGoalProposalResponse(payload, log)
	})

	wailsRuntime.EventsOn(a.wailsCtx(), backend.EventGoalSuggestionResponse, func(data ...any) {
		payload, ok := extractPayload("goal_suggestion response", data, log)
		if !ok {
			return
		}
		a.handleGoalSuggestionResponse(payload, log)
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
