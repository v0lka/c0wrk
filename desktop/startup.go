package desktop

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/terminal"
	"github.com/v0lka/c0wrk/backend/vectorindex"
)

// pendingConfirmData holds the state for a pending tool confirmation,
// including metadata needed for on-demand judge evaluation.
type pendingConfirmData struct {
	ch          chan backend.ConfirmationResponse
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
	// ══════════════════════════════════════════════════════════════════

	startTime := time.Now()
	a.ctx = ctx

	// ── Phase 1: Shell Environment + Logger ──────────────────────────
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// This ensures ${OPENAI_API_KEY} and similar vars in config.yaml resolve correctly.
	config.LoadShellEnvironment(nil)

	// Initialize logger FIRST - before any other initialization
	// This ensures all startup errors are written to log files
	// Use a temporary default level; will re-init after config is loaded
	sessionLogger, err := logger.Init("INFO")
	if err != nil {
		// Can't log to file, but can still emit to frontend
		slog.Error("failed to initialize logger", "error", err)
	}
	var log *slog.Logger
	if sessionLogger != nil {
		log = sessionLogger.Logger()
	} else {
		log = slog.Default()
	}
	a.logger = log
	log.Info("startup phase complete", "phase", "logger", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 2: Config + External Dependencies (parallel) ───────────
	// verifyExternalDependencies only needs the Wails ctx (for modal on failure).
	// ResolveAndLoad needs env vars (loaded above) and logger.
	var resolved *config.ResolvedConfig
	var depsOK bool

	var phase2 sync.WaitGroup
	phase2.Add(2)
	go func() {
		defer phase2.Done()
		resolved = config.ResolveAndLoad(log)
	}()
	go func() {
		defer phase2.Done()
		depsOK = verifyExternalDependencies(ctx)
	}()
	phase2.Wait()
	log.Info("startup phase complete", "phase", "config", "elapsed_ms", time.Since(startTime).Milliseconds())

	// Fail fast if any required external CLI tool (git, rg) is missing.
	// The helper blocks on a modal and quits the app when dependencies are missing.
	if !depsOK {
		return
	}

	cfg := resolved.Config
	configPath := resolved.ConfigPath
	configLoadErrors := resolved.LoadErrors
	agentDir := resolved.AgentDir

	// Initialize logLevel from config and re-init logger if level differs
	logLevel := cfg.LogLevel
	if logLevel != "" && logLevel != "INFO" {
		if newLogger, err := logger.Init(logLevel); err == nil {
			if sessionLogger != nil {
				if err := sessionLogger.Close(); err != nil {
					slog.Error("failed to close session logger", "error", err)
				}
			}
			sessionLogger = newLogger
			log = newLogger.Logger()
			a.logger = log
		}
	}

	// ── Phase 3: Database + Terminal Manager (parallel) ───────────────
	// OpenDatabase needs config (for dbPath). Terminal manager needs only logger.
	dbPath := filepath.Join(agentDir, cfg.Memory.Database)
	var db *sql.DB
	var dbErr error
	var termManager *terminal.Manager

	var phase3 sync.WaitGroup
	phase3.Add(2)
	go func() {
		defer phase3.Done()
		db, dbErr = backend.OpenDatabase(dbPath, log)
	}()
	go func() {
		defer phase3.Done()
		// Terminal manager: emits raw PTY output as session-scoped events.
		// Output is base64-encoded to preserve raw bytes through JSON serialization
		// (string(data) would corrupt invalid UTF-8 split across read boundaries,
		// and json.Marshal replaces invalid UTF-8 with U+FFFD).
		termManager = terminal.NewManager(log, func(sessionID string, data []byte) {
			eventName := fmt.Sprintf("session:%s:terminal_output", sessionID)
			wailsRuntime.EventsEmit(a.ctx, eventName, map[string]string{"data": base64.StdEncoding.EncodeToString(data)})
		})
	}()
	phase3.Wait()
	log.Info("startup phase complete", "phase", "database", "elapsed_ms", time.Since(startTime).Milliseconds())

	if dbErr != nil {
		log.Error("failed to open sqlite database", "error", dbErr)
	}
	a.db = db

	// ── Phase 4: Stores + Project/Session Preload ────────────────────

	// Project store first (sessions FK references projects)
	var projStore *project.SQLiteProjectStore
	if db != nil {
		ps, err := project.NewSQLiteProjectStore(db)
		if err != nil {
			log.Error("failed to init project store", "error", err)
		} else {
			projStore = ps
		}
	}

	// Session store (depends on projects table)
	var store *session.SQLiteSessionStore
	if db != nil {
		s, err := session.NewSQLiteSessionStore(db)
		if err != nil {
			log.Error("failed to init session store", "error", err)
		} else {
			store = s
		}
	}

	log.Info("startup phase complete", "phase", "stores", "elapsed_ms", time.Since(startTime).Milliseconds())

	// UI emit function: bridges events to Wails frontend (persistence is handled by Application).
	uiEmitFunc := func(evt session.Event) {
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		wailsRuntime.EventsEmit(a.ctx, eventName, evt.Data)
		a.log().Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}

	// Desktop UI callbacks: closures captured here, invoked by Application at runtime.

	// AskUser callback: emits question to frontend, waits for response.
	askUserFunc := func(ctx context.Context, req backend.AskUserRequest) (backend.AskUserResponse, error) {
		if a.ctx == nil {
			return backend.AskUserResponse{}, errors.New("ask_user not available: no UI context")
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return backend.AskUserResponse{}, errors.New("ask_user not available: no session context")
		}

		requestID := uuid.New().String()
		ch := make(chan backend.AskUserResponse, 1)
		a.pendingAskUser.Store(requestID, ch)

		payload := session.AskUserPayload{
			RequestID: requestID,
			Questions: req.Questions,
		}

		uiEmitFunc(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return backend.AskUserResponse{}, ctx.Err()
		case <-a.ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return backend.AskUserResponse{}, a.ctx.Err()
		}
	}

	// Confirm callback: emits confirmation request to frontend, waits for response.
	confirmFunc := func(ctx context.Context, req backend.ConfirmationRequest) (backend.ConfirmationResponse, error) {
		if a.ctx == nil {
			return backend.ConfirmAllowOnce, nil
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return backend.ConfirmAllowOnce, nil
		}

		requestID := uuid.New().String()
		ch := make(chan backend.ConfirmationResponse, 1)
		a.pendingConfirmations.Store(requestID, &pendingConfirmData{
			ch:          ch,
			taskContext: backend.TaskContextFrom(ctx),
			toolName:    req.ToolName,
			input:       req.Input,
			sessionID:   sessionID,
		})

		payload := session.ToolConfirmPayload{
			ConfirmID: requestID,
			Tool:      req.ToolName,
			Args:      string(req.Input),
			Reasoning: req.JudgeReasoning,
		}

		uiEmitFunc(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return backend.ConfirmDenyAndStop, ctx.Err()
		case <-a.ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return backend.ConfirmDenyAndStop, a.ctx.Err()
		}
	}

	// StepLimit callback: emits step limit prompt to frontend, waits for response.
	stepLimitFunc := func(ctx context.Context, currentStep int, maxSteps int, reason string) (backend.StepLimitResponse, error) {
		if a.ctx == nil {
			return backend.StepLimitDeny, nil
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return backend.StepLimitDeny, nil
		}

		requestID := uuid.New().String()
		ch := make(chan backend.StepLimitResponse, 1)
		a.pendingStepLimit.Store(requestID, ch)

		payload := session.StepLimitPayload{
			RequestID:   requestID,
			CurrentStep: currentStep,
			MaxSteps:    maxSteps,
			Reason:      reason,
		}

		uiEmitFunc(session.Event{SessionID: sessionID, Type: "step_limit", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingStepLimit.Delete(requestID)
			return backend.StepLimitDeny, ctx.Err()
		case <-a.ctx.Done():
			a.pendingStepLimit.Delete(requestID)
			return backend.StepLimitDeny, a.ctx.Err()
		}
	}

	// Vector search deferred init — expensive ONNX loading runs in Background.
	var vectorMgrPtr atomic.Pointer[vectorindex.Manager]
	vectorReady := make(chan struct{})
	var vectorOnce sync.Once

	logDir := filepath.Join(agentDir, "logs")
	projectsDir := filepath.Join(agentDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		log.Error("failed to create projects directory", "error", err)
	}

	// Initialize project manager early — it only needs the store and a directory path.
	// This allows us to emit project/session data to the frontend before the slow
	// NewApplication() call (MCP gateway, etc.).
	var projectMgr *project.Manager
	if projStore != nil {
		projectMgr = project.NewManager(projStore, projectsDir)
	}

	// Pre-load projects and sessions and emit to frontend immediately.
	// This happens before the slow NewApplication() call so the sidebar
	// populates right away. The project resolver is wired later, so lazy
	// session restoration only works after backend:ready fires.
	var cachedProjects []project.ProjectInfo
	if projectMgr != nil {
		projects, pErr := projectMgr.ListProjects()
		if pErr == nil && len(projects) > 0 {
			cachedProjects = projects
			// NOTE: We intentionally do NOT set activeProjectID here.
			// Setting it prematurely causes SwitchProject's idempotency guard
			// to reject the frontend's first call, which skips vector index
			// initialization entirely. The frontend will call SwitchProject
			// after receiving this event, which sets activeProjectID properly.

			wailsRuntime.EventsEmit(a.ctx, backend.EventProjectsLoaded, projects)

			// Also pre-load sessions for the most recent project
			if store != nil {
				sessions, sErr := store.ListSessionsByProject(context.Background(), projects[0].ID)
				if sErr == nil {
					wailsRuntime.EventsEmit(a.ctx, backend.EventSessionsLoaded, sessions)
				} else {
					log.Warn("failed to pre-load sessions for early emit", "error", sErr)
				}
			}
		} else if pErr != nil {
			log.Warn("failed to pre-load projects for early emit", "error", pErr)
		}
	}

	log.Info("startup phase complete", "phase", "preload", "elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Phase 5: Application + FrontendAPI ───────────────────────────

	// Wire lazy vector search callbacks that gate on background init.
	vectorSearchFunc := backend.VectorSearchFunc(func(ctx context.Context, opts backend.VectorSearchOptions) ([]backend.VectorSearchResult, error) {
		select {
		case <-vectorReady:
		case <-ctx.Done():
			return nil, fmt.Errorf("vector search not ready: %w", ctx.Err())
		}
		mgr := vectorMgrPtr.Load()
		if mgr == nil {
			return nil, errors.New("vector search unavailable")
		}
		vectorSvc := mgr.Service()
		results, searchErr := vectorSvc.HybridSearch(ctx, vectorindex.SearchOptions{
			Query:       opts.Query,
			TopK:        opts.TopK,
			Mode:        vectorindex.ParseMode(opts.Mode),
			FilePattern: opts.FilePattern,
			MustMatch:   opts.MustMatch,
		})
		if searchErr != nil {
			return nil, searchErr
		}
		out := make([]backend.VectorSearchResult, len(results))
		for i, r := range results {
			out[i] = backend.VectorSearchResult{
				FilePath:    r.FilePath,
				FileName:    r.FileName,
				Content:     r.Content,
				Score:       r.Score,
				StartLine:   r.StartLine,
				EndLine:     r.EndLine,
				Language:    r.Language,
				VectorRank:  r.VectorRank,
				LexicalRank: r.LexicalRank,
			}
		}
		return out, nil
	})
	vectorSearchWaitFunc := backend.VectorSearchWaitFunc(func(ctx context.Context) error {
		select {
		case <-vectorReady:
			mgr := vectorMgrPtr.Load()
			if mgr == nil {
				return errors.New("vector search unavailable")
			}
			return mgr.Service().WaitReady(ctx)
		case <-ctx.Done():
			return fmt.Errorf("vector search not ready: %w", ctx.Err())
		}
	})

	application, err := backend.NewApplication(backend.ApplicationConfig{
		Config:               cfg,
		Logger:               log,
		AgentDir:             agentDir,
		LogDir:               logDir,
		ProjectsDir:          projectsDir,
		SessionStore:         store,
		TaskStore:            store,
		UIEmitFunc:           uiEmitFunc,
		AskUserFunc:          askUserFunc,
		ConfirmFunc:          confirmFunc,
		StepLimitFunc:        stepLimitFunc,
		VectorSearchFunc:     vectorSearchFunc,
		VectorSearchWaitFunc: vectorSearchWaitFunc,
	})
	if err != nil {
		log.Error("failed to create backend application", "error", err)
		wailsRuntime.EventsEmit(a.ctx, backend.EventStartupError, map[string]string{
			"message": "failed to create backend application",
			"error":   err.Error(),
		})
		return
	}
	a.app = application
	log.Info("startup phase complete", "phase", "application", "elapsed_ms", time.Since(startTime).Milliseconds())

	// Construct FrontendAPI — all components are ready.
	a.FrontendAPI = backend.NewFrontendAPI(backend.FrontendAPIConfig{
		App:             application,
		Logger:          log,
		Config:          cfg,
		ConfigPath:      configPath,
		Store:           store,
		ProjStore:       projStore,
		SessionLogger:   sessionLogger,
		LogLevel:        logLevel,
		ProjectManager:  projectMgr,
		ProjectsDir:     projectsDir,
		VectorManager:   nil, // set lazily once background init completes
		TerminalManager: termManager,
		EmitEvent: func(eventName string, data ...any) {
			wailsRuntime.EventsEmit(a.ctx, eventName, data...)
		},
		AppCtx: func() context.Context {
			return a.ctx
		},
	})

	log.Info("startup phase complete", "phase", "frontend_api", "elapsed_ms", time.Since(startTime).Milliseconds())

	a.SetConfigLoadState(configLoadErrors)

	manager := application.Manager()

	// Wire project resolver so the session manager can lazily restore sessions
	// from the database by looking up the project's workspace path.
	if projStore != nil {
		manager.SetProjectResolver(func(projectID string) (string, error) {
			proj, err := projStore.LoadProject(context.Background(), projectID)
			if err != nil {
				return "", fmt.Errorf("failed to load project: %w", err)
			}
			if proj == nil {
				return "", fmt.Errorf("project %s not found", projectID)
			}
			return proj.WorkspacePath, nil
		})
	}

	// Validate LLM provider configuration at startup (fail-fast).
	if cfg.LLM.ActiveProvider == "" {
		log.Error("no active LLM provider configured - check your config.yaml")
		wailsRuntime.EventsEmit(a.ctx, backend.EventStartupError, map[string]string{
			"message": "no active LLM provider configured - check your config.yaml",
			"error":   "config has no active_provider defined under llm",
		})
	}

	a.wireWailsEventListeners(log, uiEmitFunc)

	// ── EventBackendReady ────────────────────────────────────────────
	// Signal frontend that all backend subsystems are ready.
	// Reuse the pre-loaded projects slice to avoid a redundant SQLite query.
	switch {
	case len(cachedProjects) > 0:
		wailsRuntime.EventsEmit(a.ctx, backend.EventBackendReady, cachedProjects)
	case projectMgr != nil:
		projects, lErr := projectMgr.ListProjects()
		if lErr == nil && len(projects) > 0 {
			wailsRuntime.EventsEmit(a.ctx, backend.EventBackendReady, projects)
		} else {
			if lErr != nil {
				log.Warn("failed to load projects for backend:ready", "error", lErr)
			}
			wailsRuntime.EventsEmit(a.ctx, backend.EventBackendReady)
		}
	default:
		wailsRuntime.EventsEmit(a.ctx, backend.EventBackendReady)
	}
	log.Info("startup complete \u2014 backend ready", "total_elapsed_ms", time.Since(startTime).Milliseconds())

	// ── Background: Vector Index ─────────────────────────────────────
	// ONNX embedder loading is expensive (~500-2000ms). Starts after
	// EventBackendReady so it never blocks the critical path.

	go func() {
		defer vectorOnce.Do(func() { close(vectorReady) })

		modelPath := resolveModelPath("jina-v2-small.onnx", agentDir)
		tokenizerPath := resolveModelPath("jina-v2-small-tokenizer.json", agentDir)
		libraryPath := resolveONNXLibPath()

		vectorMgr, vecErr := vectorindex.NewManager(vectorindex.ManagerConfig{
			ModelPath:        modelPath,
			TokenizerPath:    tokenizerPath,
			LibraryPath:      libraryPath,
			MaxSeqLength:     512,
			HiddenDim:        512,
			PersistPath:      filepath.Join(agentDir, "vector_index"),
			IgnoreDirs:       cfg.Workspace.IgnoreDirs,
			IgnoreExtensions: cfg.Workspace.IgnoreExtensions,
			IgnoreFileNames:  cfg.Workspace.IgnoreFileNames,
			Logger:           log,
		})
		if vecErr != nil {
			log.Warn("vector search unavailable", "error", vecErr)
			return
		}
		if vectorMgr == nil {
			log.Info("vector search disabled (model files not found)")
			return
		}

		vectorMgrPtr.Store(vectorMgr)
		log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
	}()

	// Wire vector manager into FrontendAPI once background init completes.
	go func() {
		<-vectorReady
		if mgr := vectorMgrPtr.Load(); mgr != nil {
			a.SetVectorManager(mgr)
		}
	}()
}

// Shutdown is called when the Wails app is shutting down.
func (a *App) Shutdown(ctx context.Context) {
	// Drain all pending confirmation/ask-user/step-limit channels so that
	// blocked goroutines can exit cleanly instead of leaking.
	a.pendingConfirmations.Range(func(key, value any) bool {
		if pd, ok := value.(*pendingConfirmData); ok {
			select {
			case pd.ch <- backend.ConfirmDenyAndStop:
			default:
			}
		}
		a.pendingConfirmations.Delete(key)
		return true
	})
	a.pendingAskUser.Range(func(key, value any) bool {
		if ch, ok := value.(chan backend.AskUserResponse); ok {
			select {
			case ch <- backend.AskUserResponse{}:
			default:
			}
		}
		a.pendingAskUser.Delete(key)
		return true
	})
	a.pendingStepLimit.Range(func(key, value any) bool {
		if ch, ok := value.(chan backend.StepLimitResponse); ok {
			select {
			case ch <- backend.StepLimitDeny:
			default:
			}
		}
		a.pendingStepLimit.Delete(key)
		return true
	})

	if a.FrontendAPI != nil {
		a.Cleanup()
	}

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
// judge requests, ask-user responses, and step-limit responses.
func (a *App) wireWailsEventListeners(log *slog.Logger, uiEmitFunc func(session.Event)) {
	// Listen for confirmation responses from frontend
	wailsRuntime.EventsOn(a.ctx, backend.EventToolConfirmResponse, func(data ...any) {
		if len(data) == 0 {
			log.Warn("tool confirmation response missing payload")
			return
		}

		payload, ok := data[0].(map[string]any)
		if !ok {
			log.Warn("tool confirmation response has unexpected type", "data", data)
			return
		}

		requestIDVal, ok := payload["confirm_id"]
		if !ok {
			log.Warn("tool confirmation response missing confirm_id")
			return
		}
		requestID, ok := requestIDVal.(string)
		if !ok {
			log.Warn("tool confirmation confirm_id is not string")
			return
		}

		decisionVal, ok := payload["decision"]
		if !ok {
			log.Warn("tool confirmation response missing decision field")
			return
		}

		var resp backend.ConfirmationResponse
		switch v := decisionVal.(type) {
		case float64:
			resp = backend.ConfirmationResponse(int(v))
		case int:
			resp = backend.ConfirmationResponse(v)
		case string:
			switch v {
			case "allow_once":
				resp = backend.ConfirmAllowOnce
			case "deny":
				resp = backend.ConfirmDeny
			case "stop", "deny_and_stop":
				resp = backend.ConfirmDenyAndStop
			default:
				log.Warn("unknown string confirmation decision", "decision", v)
				return
			}
		default:
			log.Warn("tool confirmation decision has unsupported type", "type", fmt.Sprintf("%T", decisionVal))
			return
		}

		dataVal, ok := a.pendingConfirmations.Load(requestID)
		if !ok {
			log.Warn("no pending confirmation for confirm_id", "confirm_id", requestID)
			return
		}
		confirmData, ok := dataVal.(*pendingConfirmData)
		if !ok {
			log.Warn("pending confirmation has wrong type", "confirm_id", requestID)
			a.pendingConfirmations.Delete(requestID)
			return
		}

		select {
		case confirmData.ch <- resp:
		default:
			log.Warn("confirmation response dropped: channel full",
				"confirm_id", requestID,
				"tool", confirmData.toolName,
				"decision", resp)
		}

		a.pendingConfirmations.Delete(requestID)
	})

	// Listen for on-demand judge verdict requests from frontend
	wailsRuntime.EventsOn(a.ctx, backend.EventToolJudgeRequest, func(data ...any) {
		if len(data) == 0 {
			log.Warn("tool judge request missing payload")
			return
		}

		payload, ok := data[0].(map[string]any)
		if !ok {
			log.Warn("tool judge request has unexpected type", "data", data)
			return
		}

		confirmIDVal, ok := payload["confirm_id"]
		if !ok {
			log.Warn("tool judge request missing confirm_id")
			return
		}
		confirmID, ok := confirmIDVal.(string)
		if !ok {
			log.Warn("tool judge request confirm_id is not string")
			return
		}

		// Look up pending confirmation metadata
		dataVal, ok := a.pendingConfirmations.Load(confirmID)
		if !ok {
			log.Warn("no pending confirmation for judge request", "confirm_id", confirmID)
			return
		}
		pendingData, ok := dataVal.(*pendingConfirmData)
		if !ok {
			log.Warn("pending confirmation has wrong type for judge request", "confirm_id", confirmID)
			return
		}

		// Call judge asynchronously to avoid blocking the event listener
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("judge: goroutine panicked", "confirm_id", confirmID, "panic", r)
					uiEmitFunc(session.Event{
						SessionID: pendingData.sessionID,
						Type:      "tool_judge_response",
						Data: session.JudgeResponsePayload{
							ConfirmID: confirmID,
							Error:     fmt.Sprintf("Internal error during judge evaluation: %v", r),
						},
					})
				}
			}()

			log.Debug("judge: goroutine started", "confirm_id", confirmID, "tool", pendingData.toolName)

			judgeCtx, judgeCancel := context.WithTimeout(a.ctx, 120*time.Second)
			defer judgeCancel()

			responsePayload := session.JudgeResponsePayload{
				ConfirmID: confirmID,
			}

			_, reasoning, err := a.app.EvaluateJudge(judgeCtx, pendingData.toolName, pendingData.input, pendingData.taskContext)
			if err != nil {
				log.Warn("judge: evaluation failed", "confirm_id", confirmID, "tool", pendingData.toolName, "error", err)
				responsePayload.Error = fmt.Sprintf("Judge evaluation failed: %v", err)
				responsePayload.Reasoning = reasoning
			} else {
				log.Debug("judge: evaluation completed", "confirm_id", confirmID, "tool", pendingData.toolName, "reasoning", reasoning)
				responsePayload.Reasoning = reasoning
			}

			uiEmitFunc(session.Event{SessionID: pendingData.sessionID, Type: "tool_judge_response", Data: responsePayload})
			log.Debug("judge: response event emitted", "confirm_id", confirmID)
		}()
	})

	// Listen for ask_user responses from frontend
	wailsRuntime.EventsOn(a.ctx, backend.EventAskUserResponse, func(data ...any) {
		if len(data) == 0 {
			log.Warn("ask_user response missing payload")
			return
		}

		payload, ok := data[0].(map[string]any)
		if !ok {
			log.Warn("ask_user response has unexpected type", "data", data)
			return
		}

		requestIDVal, ok := payload["request_id"]
		if !ok {
			log.Warn("ask_user response missing request_id")
			return
		}
		requestID, ok := requestIDVal.(string)
		if !ok {
			log.Warn("ask_user request_id is not string")
			return
		}

		// Build response
		var resp backend.AskUserResponse

		// Parse answers array
		if answersVal, ok := payload["answers"]; ok {
			if answersArr, ok := answersVal.([]any); ok {
				for _, item := range answersArr {
					answerMap, ok := item.(map[string]any)
					if !ok {
						continue
					}
					var answer backend.AskUserAnswer
					if id, ok := answerMap["id"].(string); ok {
						answer.ID = id
					}
					if selectedVal, ok := answerMap["selected"]; ok {
						if selectedArr, ok := selectedVal.([]any); ok {
							for _, v := range selectedArr {
								if s, ok := v.(string); ok {
									answer.Selected = append(answer.Selected, s)
								}
							}
						}
					}
					if ct, ok := answerMap["custom_text"].(string); ok {
						answer.CustomText = ct
					}
					resp.Answers = append(resp.Answers, answer)
				}
			}
		}

		chVal, ok := a.pendingAskUser.Load(requestID)
		if !ok {
			log.Warn("no pending ask_user for request_id", "request_id", requestID)
			return
		}
		ch, ok := chVal.(chan backend.AskUserResponse)
		if !ok {
			log.Warn("pending ask_user channel has wrong type", "request_id", requestID)
			a.pendingAskUser.Delete(requestID)
			return
		}

		select {
		case ch <- resp:
		default:
			// Channel already has a value or receiver gone; drop
		}
		a.pendingAskUser.Delete(requestID)
	})

	// Listen for step_limit responses from frontend
	wailsRuntime.EventsOn(a.ctx, backend.EventStepLimitResponse, func(data ...any) {
		if len(data) == 0 {
			log.Warn("step_limit response missing payload")
			return
		}

		payload, ok := data[0].(map[string]any)
		if !ok {
			log.Warn("step_limit response has unexpected type", "data", data)
			return
		}

		requestIDVal, ok := payload["request_id"]
		if !ok {
			log.Warn("step_limit response missing request_id")
			return
		}
		requestID, ok := requestIDVal.(string)
		if !ok {
			log.Warn("step_limit request_id is not string")
			return
		}

		responseVal, ok := payload["response"]
		if !ok {
			log.Warn("step_limit response missing response field")
			return
		}

		var resp backend.StepLimitResponse
		switch v := responseVal.(type) {
		case string:
			resp = backend.StepLimitResponse(v)
		default:
			log.Warn("step_limit response has unsupported type", "type", fmt.Sprintf("%T", responseVal))
			return
		}

		chVal, ok := a.pendingStepLimit.Load(requestID)
		if !ok {
			log.Warn("no pending step_limit for request_id", "request_id", requestID)
			return
		}
		ch, ok := chVal.(chan backend.StepLimitResponse)
		if !ok {
			log.Warn("pending step_limit channel has wrong type", "request_id", requestID)
			a.pendingStepLimit.Delete(requestID)
			return
		}

		select {
		case ch <- resp:
		default:
			// Channel already has a value or receiver gone; drop
		}
		a.pendingStepLimit.Delete(requestID)
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
