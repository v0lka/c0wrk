package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
)

// pendingConfirmData holds the state for a pending tool confirmation,
// including metadata needed for on-demand judge evaluation.
type pendingConfirmData struct {
	ch          chan tools.ConfirmationResponse
	taskContext string
	toolName    string
	input       json.RawMessage
	sessionID   string
}

// Startup is called when the Wails app starts.
func (a *App) Startup(ctx context.Context) {
	// ══════════════════════════════════════════════════════════════════
	// STARTUP MANIFEST — Critical Path Time Budget: <500ms total
	//
	// CRITICAL PATH (blocks UI):
	//   Phase 1: shell_env + logger      — budget: 50ms
	//   Phase 2: config + deps_check     — budget: 100ms (parallel)
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

	// ── Phase 1: Shell Environment + Logger ──────────────────────────
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// MUST run before any config/env-var resolution.
	config.LoadShellEnvironment(nil)
	log, sessionLogger := a.initLogger()
	a.sessionLogger = sessionLogger // stored for cleanup on early Startup exits (W1)
	log.Info("startup phase complete", "phase", "logger", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 2: Config + External Dependencies (parallel) ───────────
	resolved, depsOK := a.initConfigAndDeps(ctx, log)
	log.Info("startup phase complete", "phase", "config", "elapsed_ms", time.Since(startTime).Milliseconds())
	if !depsOK {
		return
	}

	cfg := resolved.Config
	configPath := resolved.ConfigPath
	configLoadErrors := resolved.LoadErrors
	agentDir := resolved.AgentDir

	logLevel := cfg.LogLevel
	log, sessionLogger = a.maybeReinitLogger(logLevel, sessionLogger, log)

	// ── Phase 3: Database + Terminal Manager (parallel) ───────────────
	dbPath := filepath.Join(agentDir, cfg.Memory.Database)
	var db *sql.DB
	var termManager *terminal.Manager
	var phase3 sync.WaitGroup
	phase3.Add(2)
	go func() {
		defer phase3.Done()
		db = a.initDatabase(dbPath, log)
	}()
	go func() {
		defer phase3.Done()
		termManager = a.initTerminalManager(log)
	}()
	phase3.Wait()
	log.Info("startup phase complete", "phase", "database", "elapsed_ms", time.Since(startTime).Milliseconds())
	a.db = db

	// ── Phase 4: Stores + Project/Session Preload ────────────────────
	projStore, sessStore := a.initStores(db, log)
	log.Info("startup phase complete", "phase", "stores", "elapsed_ms", time.Since(startTime).Milliseconds())

	logDir := filepath.Join(agentDir, "logs")
	projectsDir := filepath.Join(agentDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		log.Error("failed to create projects directory", "error", err)
	}

	var projectMgr *project.Manager
	if projStore != nil {
		projectMgr = project.NewManager(projStore, projectsDir)
	}
	cachedProjects := a.preloadProjectsAndSessions(projectMgr, sessStore, log)
	log.Info("startup phase complete", "phase", "preload", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 5: Callbacks + Application + FrontendAPI ───────────────
	uiEmitFunc := a.buildUIEmitFunc()
	askUserFunc := a.buildAskUserCallback(uiEmitFunc)
	confirmFunc := a.buildConfirmCallback(uiEmitFunc)
	stepLimitFunc := a.buildStepLimitCallback(uiEmitFunc)

	var vectorMgrPtr atomic.Pointer[vectorindex.Manager]
	vectorReady := make(chan struct{})
	var vectorOnce sync.Once
	vectorSearchFunc, vectorSearchWaitFunc := a.buildVectorCallbacks(&vectorMgrPtr, vectorReady)

	application, err := a.buildApplication(backend.ApplicationConfig{
		Config:               cfg,
		Logger:               log,
		AgentDir:             agentDir,
		LogDir:               logDir,
		ProjectsDir:          projectsDir,
		SessionStore:         sessStore,
		TaskStore:            sessStore,
		UIEmitFunc:           uiEmitFunc,
		AskUserFunc:          askUserFunc,
		ConfirmFunc:          confirmFunc,
		StepLimitFunc:        stepLimitFunc,
		VectorSearchFunc:     vectorSearchFunc,
		VectorSearchWaitFunc: vectorSearchWaitFunc,
	}, log, startTime)
	if err != nil {
		return
	}

	a.buildFrontendAPI(application, backend.FrontendAPIConfig{
		App:             application,
		Logger:          log,
		Config:          cfg,
		ConfigPath:      configPath,
		Store:           sessStore,
		ProjStore:       projStore,
		SessionLogger:   sessionLogger,
		LogLevel:        logLevel,
		ProjectManager:  projectMgr,
		ProjectsDir:     projectsDir,
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

	// ── EventBackendReady ────────────────────────────────────────────
	a.emitBackendReady(cachedProjects, projectMgr, log)
	log.Info("startup complete \u2014 backend ready", "total_elapsed_ms", time.Since(startTime).Milliseconds())

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
			case pd.ch <- tools.ConfirmDenyAndStop:
			default:
			}
		}
		a.pendingConfirmations.Delete(key)
		return true
	})
	a.pendingAskUser.Range(func(key, value any) bool {
		if ch, ok := value.(chan tools.AskUserResponse); ok {
			select {
			case ch <- tools.AskUserResponse{}:
			default:
			}
		}
		a.pendingAskUser.Delete(key)
		return true
	})
	a.pendingStepLimit.Range(func(key, value any) bool {
		if ch, ok := value.(chan agent.StepLimitResponse); ok {
			select {
			case ch <- agent.StepLimitDeny:
			default:
			}
		}
		a.pendingStepLimit.Delete(key)
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
			a.SetVectorManager(mgr)
		}
	}

	if a.FrontendAPI != nil {
		a.Cleanup()
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
	userPath := filepath.Join(agentDir, "models", filename)
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
