package desktop

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/embedding"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	"github.com/v0lka/c0wrk/sdk/vectorindex"
)

// initLogger initializes the session logger with a temporary INFO level so any
// startup errors land on disk. Returns the active logger plus the underlying
// SessionLogger so the caller can re-init it later if config requests a
// different level. Errors are logged via slog.Default() but never block startup.
func (a *App) initLogger(logDir string) (*slog.Logger, *logger.SessionLogger) {
	sessionLogger, err := logger.Init("INFO", logDir)
	if err != nil {
		slog.Error("failed to initialize logger", "error", err)
	}
	var log *slog.Logger
	if sessionLogger != nil {
		log = sessionLogger.Logger()
	} else {
		log = slog.Default()
	}
	a.logger = log
	return log, sessionLogger
}

// maybeReinitLogger re-initializes the session logger if the configured level
// differs from the bootstrap "INFO". On failure, the original logger is kept.
// Returns the active logger and (possibly new) SessionLogger.
func (a *App) maybeReinitLogger(level string, sessionLogger *logger.SessionLogger, current *slog.Logger, logDir string) (*slog.Logger, *logger.SessionLogger) {
	if level == "" || level == "INFO" {
		return current, sessionLogger
	}
	newLogger, err := logger.Init(level, logDir)
	if err != nil {
		return current, sessionLogger
	}
	if sessionLogger != nil {
		if cerr := sessionLogger.Close(); cerr != nil {
			current.Error("failed to close session logger", "error", cerr)
		}
	}
	log := newLogger.Logger()
	a.logger = log
	return log, newLogger
}

// initConfigAndDeps loads the config file and verifies required external
// dependencies (git, rg) in parallel. The verifyExternalDependencies helper
// shows a fatal modal and quits the app on missing deps; depsOK=false signals
// the caller to abort startup gracefully.
func (a *App) initConfigAndDeps(ctx context.Context, log *slog.Logger) (*config.ResolvedConfig, bool) {
	var resolved *config.ResolvedConfig
	var depsOK bool

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		resolved = config.ResolveAndLoad(log)
	}()
	go func() {
		defer wg.Done()
		depsOK = verifyExternalDependencies(ctx)
	}()
	wg.Wait()
	return resolved, depsOK
}

// initDatabase opens the shared SQLite connection. On failure logs the error
// and returns nil — callers must tolerate a nil DB (downstream stores will be
// nil too and behavior degrades gracefully rather than panicking).
func (a *App) initDatabase(dbPath string, log *slog.Logger) *sql.DB {
	db, err := backend.OpenDatabase(dbPath, log)
	if err != nil {
		log.Error("failed to open sqlite database", "error", err)
		return nil
	}
	return db
}

// initTerminalManager constructs the PTY-backed terminal manager. Output is
// base64-encoded before emission to preserve raw bytes across JSON serialization
// (string(data) would corrupt invalid UTF-8 split across read boundaries, and
// json.Marshal replaces invalid UTF-8 with U+FFFD).
func (a *App) initTerminalManager(log *slog.Logger) *terminal.Manager {
	return terminal.NewManager(a.ctx, log, func(sessionID string, data []byte) {
		eventName := fmt.Sprintf("session:%s:terminal_output", sessionID)
		a.emit(eventName, map[string]string{"data": base64.StdEncoding.EncodeToString(data)})
	})
}

// initStores creates the project + session SQLite stores. Order matters: the
// session store has an FK reference to the projects table.
func (a *App) initStores(db *sql.DB, log *slog.Logger) (*project.SQLiteProjectStore, *session.SQLiteSessionStore) {
	if db == nil {
		return nil, nil
	}
	var projStore *project.SQLiteProjectStore
	if ps, err := project.NewSQLiteProjectStore(db); err != nil {
		log.Error("failed to init project store", "error", err)
	} else {
		projStore = ps
	}
	var sessStore *session.SQLiteSessionStore
	if s, err := session.NewSQLiteSessionStore(db); err != nil {
		log.Error("failed to init session store", "error", err)
	} else {
		sessStore = s
	}
	return projStore, sessStore
}

// preloadProjectsAndSessions emits projects + sessions from the most recent
// project to the frontend before the slow NewApplication() call so the
// sidebar populates immediately. Returns the cached project list so
// emitBackendReady can reuse it without re-querying the database.
//
// NOTE: We intentionally do NOT set activeProjectID here — that would cause
// SwitchProject's idempotency guard to reject the frontend's first call,
// skipping vector index initialization. The frontend calls SwitchProject
// after EventBackendReady which sets activeProjectID properly.
func (a *App) preloadProjectsAndSessions(projectMgr *project.Manager, sessStore *session.SQLiteSessionStore, log *slog.Logger) []project.ProjectInfo {
	if projectMgr == nil {
		return nil
	}
	projects, err := projectMgr.ListProjects()
	if err != nil {
		log.Warn("failed to pre-load projects for early emit", "error", err)
		return nil
	}
	if len(projects) == 0 {
		return nil
	}

	a.emit(backend.EventProjectsLoaded, projects)

	if sessStore != nil {
		sessions, sErr := sessStore.ListSessionsByProject(context.Background(), projects[0].ID)
		if sErr == nil {
			a.emit(backend.EventSessionsLoaded, sessions)
		} else {
			log.Warn("failed to pre-load sessions for early emit", "error", sErr)
		}
	}
	return projects
}

// buildUIEmitFunc returns the session-event emitter used by the orchestrator
// and tool callbacks. It logs at debug level and forwards the typed
// session.Event to the Wails frontend via a.emit so tests can substitute the
// underlying transport.
func (a *App) buildUIEmitFunc() func(session.Event) {
	return func(evt session.Event) {
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		a.emit(eventName, evt.Data)
		a.log().Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}
}

// buildAskUserCallback returns the closure that turns ask_user tool invocations
// into Wails events and waits for the frontend response. Errors out cleanly
// when no UI context or session is available so the tool reports the
// unavailability instead of blocking forever.
func (a *App) buildAskUserCallback(uiEmit func(session.Event)) sdktools.AskUserFunc {
	return func(ctx context.Context, req sdktools.AskUserRequest) (sdktools.AskUserResponse, error) {
		if a.ctx == nil {
			return sdktools.AskUserResponse{}, errors.New("ask_user not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return sdktools.AskUserResponse{}, errors.New("ask_user not available: no session context")
		}

		requestID := uuid.New().String()
		ch := make(chan sdktools.AskUserResponse, 1)
		a.pendingAskUser.Store(requestID, ch)

		payload := session.AskUserPayload{RequestID: requestID, Questions: req.Questions}
		uiEmit(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return sdktools.AskUserResponse{}, ctx.Err()
		case <-a.ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return sdktools.AskUserResponse{}, a.ctx.Err()
		}
	}
}

// buildConfirmCallback returns the tool-confirmation closure. C-4 contract:
// when no UI context is available we return ConfirmDenyAndStop and log a
// warning rather than auto-approving — silently allowing in this path would
// let any tool execute without user oversight.
func (a *App) buildConfirmCallback(uiEmit func(session.Event)) tools.ConfirmFunc {
	return func(ctx context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
		if a.ctx == nil {
			slog.Warn("confirmation callback denied: app context unavailable",
				"tool", req.ToolName, "reason", "ctx_nil")
			return tools.ConfirmDenyAndStop, nil
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			slog.Warn("confirmation callback denied: no session ID in context",
				"tool", req.ToolName, "reason", "session_id_missing")
			return tools.ConfirmDenyAndStop, nil
		}

		requestID := uuid.New().String()
		ch := make(chan tools.ConfirmationResponse, 1)
		a.pendingConfirmations.Store(requestID, &pendingConfirmData{
			ch:          ch,
			taskContext: sdktools.TaskContextFrom(ctx),
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
		uiEmit(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return tools.ConfirmDenyAndStop, ctx.Err()
		case <-a.ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return tools.ConfirmDenyAndStop, a.ctx.Err()
		}
	}
}

// buildStepLimitCallback returns the step-limit prompt closure.
func (a *App) buildStepLimitCallback(uiEmit func(session.Event)) agent.StepLimitFunc {
	return func(ctx context.Context, currentStep int, maxSteps int, reason string) (agent.StepLimitResponse, error) {
		if a.ctx == nil {
			return agent.StepLimitDeny, nil
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return agent.StepLimitDeny, nil
		}

		requestID := uuid.New().String()
		ch := make(chan agent.StepLimitResponse, 1)
		a.pendingStepLimit.Store(requestID, ch)

		payload := session.StepLimitPayload{
			RequestID:   requestID,
			CurrentStep: currentStep,
			MaxSteps:    maxSteps,
			Reason:      reason,
		}
		uiEmit(session.Event{SessionID: sessionID, Type: "step_limit", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingStepLimit.Delete(requestID)
			return agent.StepLimitDeny, ctx.Err()
		case <-a.ctx.Done():
			a.pendingStepLimit.Delete(requestID)
			return agent.StepLimitDeny, a.ctx.Err()
		}
	}
}

// buildVectorCallbacks returns the lazy vector-search callbacks that gate on
// background ONNX initialization. They block until vectorReady is closed (or
// ctx cancels) before delegating to the manager service.
func (a *App) buildVectorCallbacks(vectorMgrPtr *atomic.Pointer[vectorindex.Manager], vectorReady <-chan struct{}) (builtins.VectorSearchFunc, builtins.VectorSearchWaitFunc) { //nolint:gocritic // unnamedResult is acceptable for tuple returns with distinct types
	searchFunc := builtins.VectorSearchFunc(func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		select {
		case <-vectorReady:
		case <-ctx.Done():
			return nil, fmt.Errorf("vector search not ready: %w", ctx.Err())
		}
		mgr := vectorMgrPtr.Load()
		if mgr == nil {
			return nil, errors.New("vector search unavailable")
		}
		results, err := mgr.Service().HybridSearch(ctx, vectorindex.SearchOptions{
			Query:       opts.Query,
			TopK:        opts.TopK,
			Mode:        vectorindex.ParseMode(opts.Mode),
			FilePattern: opts.FilePattern,
			MustMatch:   opts.MustMatch,
		})
		if err != nil {
			return nil, err
		}
		out := make([]builtins.VectorSearchResult, len(results))
		for i, r := range results {
			out[i] = builtins.VectorSearchResult{
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

	waitFunc := builtins.VectorSearchWaitFunc(func(ctx context.Context) error {
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

	return searchFunc, waitFunc
}

// buildApplication constructs the backend.Application and stores it on the App.
// Returns the application (also stored as a.app) on success, or an error.
func (a *App) buildApplication(cfg backend.ApplicationConfig, log *slog.Logger, startTime time.Time) (*backend.Application, error) {
	application, err := backend.NewApplication(cfg)
	if err != nil {
		log.Error("failed to create backend application", "error", err)
		a.emit(backend.EventStartupError, map[string]string{
			"message": "failed to create backend application",
			"error":   err.Error(),
		})
		return nil, err
	}
	a.app = application
	log.Info("startup phase complete", "phase", "application", "elapsed_ms", time.Since(startTime).Milliseconds())
	return application, nil
}

// buildFrontendAPI constructs the FrontendAPI, wires the project resolver, and
// validates LLM provider config. Stores the result on a.FrontendAPI.
func (a *App) buildFrontendAPI(
	application *backend.Application,
	cfg backend.FrontendAPIConfig,
	configLoadErrors []string,
	projStore *project.SQLiteProjectStore,
	log *slog.Logger,
	startTime time.Time,
) {
	a.FrontendAPI = backend.NewFrontendAPI(cfg)
	log.Info("startup phase complete", "phase", "frontend_api", "elapsed_ms", time.Since(startTime).Milliseconds())

	a.SetConfigLoadState(configLoadErrors)

	// Wire project resolver for lazy session restoration.
	if projStore != nil {
		application.Manager().SetProjectResolver(func(projectID string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			proj, err := projStore.LoadProject(ctx, projectID)
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
	if cfg.Config != nil && cfg.Config.LLM.DefaultModel == "" {
		log.Error("no default model configured - check your config.yaml")
		a.emit(backend.EventStartupError, map[string]string{
			"message":    "no default model configured - check your config.yaml",
			"error":      "config has no default_model defined under llm",
			"error_code": "missing_default_model",
		})
	}
}

// emitBackendReady fires the EventBackendReady event with cached projects
// when available, falling back to a fresh ListProjects call. The signal tells
// the frontend that all synchronous backend subsystems are wired up.
func (a *App) emitBackendReady(cachedProjects []project.ProjectInfo, projectMgr *project.Manager, log *slog.Logger) {
	switch {
	case len(cachedProjects) > 0:
		a.emit(backend.EventBackendReady, cachedProjects)
	case projectMgr != nil:
		projects, err := projectMgr.ListProjects()
		if err != nil {
			log.Warn("failed to load projects for backend:ready", "error", err)
			a.emit(backend.EventBackendReady)
			return
		}
		if len(projects) > 0 {
			a.emit(backend.EventBackendReady, projects)
		} else {
			a.emit(backend.EventBackendReady)
		}
	default:
		a.emit(backend.EventBackendReady)
	}
}

// startVectorIndexBackground launches the ONNX-backed vector index in a
// goroutine after EventBackendReady so it never blocks the critical path.
// The vectorReady channel is closed exactly once on completion (success or
// known-unavailable) so callers waiting on it always unblock.
func (a *App) startVectorIndexBackground(
	agentDir string,
	cfg *config.Config,
	vectorMgrPtr *atomic.Pointer[vectorindex.Manager],
	vectorReady chan struct{},
	vectorOnce *sync.Once,
	startTime time.Time,
	log *slog.Logger,
) {
	go func() {
		defer vectorOnce.Do(func() { close(vectorReady) })

		modelPath := resolveModelPath("jina-v2-small.onnx", agentDir)
		tokenizerPath := resolveModelPath("jina-v2-small-tokenizer.json", agentDir)
		libraryPath := resolveONNXLibPath()

		// Create embedder directly via sdk/embedding.
		if modelPath == "" || tokenizerPath == "" || libraryPath == "" {
			log.Info("vector search disabled (model files not found)")
			a.emit("vector_index:status", map[string]any{"available": false, "reason": "model files not found"})
			return
		}
		emb, embErr := embedding.NewEmbedder(embedding.EmbedderConfig{
			ModelPath:     modelPath,
			TokenizerPath: tokenizerPath,
			LibraryPath:   libraryPath,
			MaxSeqLength:  512,
			HiddenDim:    512,
			Logger:       log,
		})
		if embErr != nil {
			log.Warn("vector search unavailable", "error", embErr)
			a.emit("vector_index:status", map[string]any{"available": false, "reason": embErr.Error()})
			return
		}

		vectorMgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc:    emb.EmbeddingFunc(),
			CloseFn:          emb.Close,
			IgnoreDirs:       cfg.Workspace.IgnoreDirs,
			IgnoreExtensions: cfg.Workspace.IgnoreExtensions,
			IgnoreFileNames:  cfg.Workspace.IgnoreFileNames,
			Logger:           log,
		})
		if err != nil {
			log.Warn("vector search unavailable", "error", err)
			a.emit("vector_index:status", map[string]any{"available": false, "reason": err.Error()})
			return
		}
		if vectorMgr == nil {
			log.Info("vector search disabled (model files not found)")
			a.emit("vector_index:status", map[string]any{"available": false, "reason": "model files not found"})
			return
		}

		vectorMgrPtr.Store(vectorMgr)
		// Store on FrontendAPI immediately so Cleanup can shut it down — doing
		// this here instead of in a separate goroutine eliminates the race where
		// Shutdown runs Cleanup before SetVectorManager completes (W3).
		a.SetVectorManager(vectorMgr)
		log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
	}()
}
