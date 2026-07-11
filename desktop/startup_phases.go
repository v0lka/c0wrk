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
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/embedding"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// initLogger initializes the session logger with a temporary INFO level so any
// startup errors land on disk. Returns the active logger plus the underlying
// SessionLogger so the caller can re-init it later if config requests a
// different level. Errors are logged via slog.Default() but never block startup.
//
// Once the session logger is ready, the Wails log adapter is updated so that
// Wails-internal messages (including fatal RPC errors) also appear in the
// session log.
func (a *App) initLogger(logDir string) (*slog.Logger, *logger.SessionLogger) {
	sessionLogger, err := logger.Init("INFO", logDir)
	if err != nil {
		slog.Error("failed to initialize logger", "error", err)
	}
	var log *slog.Logger
	if sessionLogger != nil {
		log = sessionLogger.Logger()
		if a.wailsLogger != nil {
			a.wailsLogger.SetDelegate(log)
		}
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
	if a.wailsLogger != nil {
		a.wailsLogger.SetDelegate(log)
	}
	return log, newLogger
}

// initConfigAndDeps loads the config file and ensures managed tools
// (rg, rtk, uv, markitdown) are downloaded/installed — all in parallel.
// On first run, tool downloads may take 3–10 minutes; subsequent runs check
// the .versions file and skip.
//
// Returns the resolved config, the tools/bin/ directory path (empty on
// failure), whether any tools were installed, and toolsOK=false if the tool
// install fails.
// The caller must prepend toolsBinPath to PATH before subsequent phases.
func (a *App) initConfigAndDeps(ctx context.Context, log *slog.Logger) (resolved *config.ResolvedConfig, toolsBinPath string, toolsInstalled, toolsOK bool) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in config resolution", "panic", r)
			}
		}()
		resolved = config.ResolveAndLoad(log)
	}()
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in tool initialization", "panic", r)
			}
		}()
		toolsBinPath, toolsInstalled = a.initTools(ctx, log)
	}()
	wg.Wait()

	if toolsBinPath == "" {
		return resolved, "", false, false
	}
	return resolved, toolsBinPath, toolsInstalled, true
}

// initTools ensures managed tools (rg, rtk, uv, markitdown) are downloaded and
// installed in <agentDir>/tools/. On first run this blocks startup for several
// minutes. On subsequent runs it returns immediately (version check). If a tool
// cannot be installed, a fatal modal is shown and the function returns an empty
// string so the caller can abort startup. On success, returns the tools/bin/
// directory path and whether any tools were actually installed.
func (a *App) initTools(ctx context.Context, log *slog.Logger) (toolsBinPath string, toolsInstalled bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	agentDir := filepath.Join(homeDir, config.DefaultAgentDir)

	toolsDir := config.ToolsDir(agentDir)
	binDir := config.ToolsBinDir(agentDir)
	pythonDir := config.ToolsPythonDir(agentDir)

	mgr := toolmanager.NewManager(toolsDir, binDir, pythonDir, log, toolmanager.ManagerConfig{
		ProgressCallback: func(toolName, stage string, bytesDone, bytesTotal int64) {
			a.emit("tool_manager:progress", map[string]any{
				"tool":        toolName,
				"stage":       stage,
				"bytes_done":  bytesDone,
				"bytes_total": bytesTotal,
			})
		},
	})

	// Show the window unconditionally at the start of tool initialization so
	// the frontend has time to mount and subscribe to events before
	// backend:ready fires in Phase 5. On first run this reveals the
	// tool-install splash; on subsequent runs it prevents the race where
	// backend:ready is emitted before the frontend subscribes, which would
	// leave the app stuck on the spinner forever.
	// WindowShow is idempotent — calling it again in emitBackendReady is a
	// no-op when the window is already visible.
	wailsRuntime.WindowShow(ctx)

	// Early detection: check which tools need installing BEFORE doing any work.
	needed, needsErr := mgr.NeedsInstall()
	if needsErr != nil {
		log.Warn("failed to check tool install status, showing window as fallback", "error", needsErr)
		// ManagedTools() or ReadVersions() failed — we can't determine which tools
		// need installing, but EnsureCriticalTools will run regardless.
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": []map[string]string{},
		})
	} else if len(needed) > 0 {
		toolNames := make([]map[string]string, len(needed))
		for i, t := range needed {
			toolNames[i] = map[string]string{"name": t.Name, "version": t.Version}
		}
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": toolNames,
		})
	}

	if err := mgr.EnsureCriticalTools(ctx); err != nil {
		log.Error("failed to install critical tools", "error", err)
		_, _ = wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
			Type:          wailsRuntime.ErrorDialog,
			Title:         "Tool Installation Failed",
			Message:       "c0wrk was unable to download required tools:\n\n" + err.Error() + "\n\nCheck your internet connection and disk space, then restart c0wrk.",
			Buttons:       []string{"Exit"},
			DefaultButton: "Exit",
		})
		wailsRuntime.Quit(ctx)
		return "", false
	}

	switch {
	case needsErr == nil && len(needed) > 0:
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": len(needed),
			"skipped_count":   0,
		})
	default:
		// No tools needed or install failed — tell the frontend so it can
		// transition from splash to waiting_ready before backend:ready arrives.
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
	}

	return mgr.PrependToPATH(), len(needed) > 0
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
		// session_renamed is a session-list metadata change (it mirrors the
		// global project:renamed event). Re-emit it globally so the sidebar
		// updates the title even when the renamed session is NOT the active
		// one — e.g. when background auto-titling completes after the user has
		// already switched to another session. Without this, the session-scoped
		// event has no listener and the title stays stale until a project
		// switch or app reload.
		if evt.Type == "session_renamed" {
			if rd, ok := evt.Data.(session.SessionRenamedData); ok {
				a.emit(backend.EventSessionRenamed, map[string]string{"id": rd.ID, "name": rd.NewName})
			}
		}
		a.log().Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}
}

// buildAskUserCallback returns the closure that turns ask_user tool invocations
// into Wails events and waits for the frontend response. Errors out cleanly
// when no UI context or session is available so the tool reports the
// unavailability instead of blocking forever.
func (a *App) buildAskUserCallback(uiEmit func(session.Event)) coretools.AskUserFunc {
	return func(ctx context.Context, req coretools.AskUserRequest) (coretools.AskUserResponse, error) {
		if a.ctx == nil {
			return coretools.AskUserResponse{}, errors.New("ask_user not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return coretools.AskUserResponse{}, errors.New("ask_user not available: no session context")
		}

		requestID := uuid.New().String()
		ch := make(chan coretools.AskUserResponse, 1)
		payload := session.AskUserPayload{RequestID: requestID, Questions: req.Questions}
		a.pendingAskUser.Store(requestID, &pendingAskUserEntry{
			ch:        ch,
			sessionID: sessionID,
			payload:   payload,
		})
		uiEmit(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return coretools.AskUserResponse{}, ctx.Err()
		case <-a.ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return coretools.AskUserResponse{}, a.ctx.Err()
		}
	}
}

// planApprovalResponse carries the user's decision back to the blocked
// declare_plan tool call.
type planApprovalResponse struct {
	Decision string
	Feedback string
}

// buildPlanApprovalCallback returns the closure that turns declare_plan's
// await_approval mode into a Wails event and waits for the frontend response.
func (a *App) buildPlanApprovalCallback(uiEmit func(session.Event)) coretools.ApprovalFunc {
	return func(ctx context.Context, planPath, planMarkdown string) (string, string, error) {
		if a.ctx == nil {
			return "", "", errors.New("plan approval not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return "", "", errors.New("plan approval not available: no session context")
		}

		if planMarkdown == "" && planPath != "" {
			content, err := os.ReadFile(planPath)
			if err != nil {
				return "", "", fmt.Errorf("plan approval: failed to read plan file: %w", err)
			}
			planMarkdown = string(content)
		}

		requestID := uuid.New().String()
		ch := make(chan planApprovalResponse, 1)
		payload := session.PlanApprovalPayload{
			RequestID:   requestID,
			PlanPath:    planPath,
			PlanContent: planMarkdown,
		}
		a.pendingPlanApprovals.Store(requestID, &pendingPlanApprovalEntry{
			ch:        ch,
			sessionID: sessionID,
			payload:   payload,
		})
		// Emit through the Application's combined UI + persistence path so
		// the plan_review_ready event survives app restarts. Fall back to
		// the raw UI emitter when the Application is not yet initialized.
		evt := session.Event{SessionID: sessionID, Type: "plan_review_ready", Data: payload}
		if a.app != nil {
			a.app.EmitSessionEvent(evt)
		} else {
			uiEmit(evt)
		}

		select {
		case resp := <-ch:
			return resp.Decision, resp.Feedback, nil
		case <-ctx.Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", ctx.Err()
		case <-a.ctx.Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", a.ctx.Err()
		}
	}
}

// buildConfirmCallback returns the tool-confirmation closure. C-4 contract:
// when no UI context is available we return ConfirmDenyAndStop and log a
// warning rather than auto-approving — silently allowing in this path would
// let any tool execute without user oversight.
func (a *App) buildConfirmCallback(uiEmit func(session.Event)) sdktools.ConfirmFunc {
	return func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		if a.ctx == nil {
			slog.Warn("confirmation callback denied: app context unavailable",
				"tool", req.ToolName, "reason", "ctx_nil")
			return sdktools.ConfirmDenyAndStop, nil
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			slog.Warn("confirmation callback denied: no session ID in context",
				"tool", req.ToolName, "reason", "session_id_missing")
			return sdktools.ConfirmDenyAndStop, nil
		}

		requestID := uuid.New().String()
		ch := make(chan sdktools.ConfirmationResponse, 1)
		a.pendingConfirmations.Store(requestID, &pendingConfirmData{
			ch:          ch,
			taskContext: sdktools.TaskContextFrom(ctx),
			toolName:    req.ToolName,
			input:       req.Input,
			sessionID:   sessionID,
			reasoning:   req.JudgeReasoning,
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
			return sdktools.ConfirmDenyAndStop, ctx.Err()
		case <-a.ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return sdktools.ConfirmDenyAndStop, a.ctx.Err()
		}
	}
}

// buildStepLimitCallback returns a HITLHandler that handles step-limit prompts.
// Tool confirmation is handled separately via buildConfirmCallback → ToolRegistry.ConfirmFunc.
func (a *App) buildStepLimitCallback(uiEmit func(session.Event)) agent.HITLHandler {
	return &stepLimitHITLAdapter{
		ctx:              a.ctx,
		pendingStepLimit: &a.pendingStepLimit,
		uiEmit:           uiEmit,
	}
}

// stepLimitHITLAdapter wraps the step-limit UI prompt logic as an agent.HITLHandler.
// Tool confirmation is handled separately by the ToolRegistry's ConfirmFunc (policy-driven).
type stepLimitHITLAdapter struct {
	ctx              context.Context
	pendingStepLimit *sync.Map
	uiEmit           func(session.Event)
}

// OnToolCall allows all tool calls unchanged. Tool confirmation is handled
// by the ToolRegistry's ConfirmFunc (policy-driven, only for PolicyUserConfirm tools).
func (s *stepLimitHITLAdapter) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (*agent.HITLToolDecision, error) {
	return &agent.HITLToolDecision{Allow: true}, nil
}

func (s *stepLimitHITLAdapter) OnStepLimit(ctx context.Context, currentStep, maxSteps int, reason string) (agent.StepLimitResponse, error) {
	if s.ctx == nil {
		return agent.StepLimitDeny, nil
	}
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return agent.StepLimitDeny, nil
	}

	requestID := uuid.New().String()
	ch := make(chan agent.StepLimitResponse, 1)
	payload := session.StepLimitPayload{
		RequestID:   requestID,
		CurrentStep: currentStep,
		MaxSteps:    maxSteps,
		Reason:      reason,
	}
	s.pendingStepLimit.Store(requestID, &pendingStepLimitEntry{
		ch:        ch,
		sessionID: sessionID,
		payload:   payload,
	})
	s.uiEmit(session.Event{SessionID: sessionID, Type: "step_limit", Data: payload})

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		s.pendingStepLimit.Delete(requestID)
		return agent.StepLimitDeny, ctx.Err()
	case <-s.ctx.Done():
		s.pendingStepLimit.Delete(requestID)
		return agent.StepLimitDeny, s.ctx.Err()
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

	a.Lifecycle().SetConfigLoadState(configLoadErrors)

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
// WindowShow is called unconditionally — when the window was already shown
// for tool installation this is a no-op; when no tools were needed this is
// the first time the window becomes visible.
//
// filterNoProject strips the No Project pseudo-project from the emitted list
// regardless of whether projects come from cache or a fresh query.
// This is used when LLM is unconfigured to prevent the frontend from
// auto-loading No Project before the settings dialog is shown.
func (a *App) emitBackendReady(cachedProjects []project.ProjectInfo, projectMgr *project.Manager, filterNoProject bool, log *slog.Logger) {
	// When the window is still hidden (no tools needed installing), show it now.
	// WindowShow is idempotent: if already visible (tools were installed), it's a no-op.
	// Guard against nil ctx in tests (no Wails lifecycle).
	if a.ctx != nil {
		wailsRuntime.WindowShow(a.ctx)
	}

	// Collect projects from cache or fresh query, applying No Project filter if
	// requested.
	var projects []project.ProjectInfo
	switch {
	case len(cachedProjects) > 0:
		projects = cachedProjects
	case projectMgr != nil:
		var err error
		projects, err = projectMgr.ListProjects()
		if err != nil {
			log.Warn("failed to load projects for backend:ready", "error", err)
			a.emit(backend.EventBackendReady)
			return
		}
	}

	if filterNoProject {
		filtered := make([]project.ProjectInfo, 0, len(projects))
		for _, p := range projects {
			if p.ID != project.NoProjectID {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	if len(projects) > 0 {
		a.emit(backend.EventBackendReady, projects)
	} else {
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

		// Create embedder directly via github.com/v0lka/sp4rk/embedding.
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
			HiddenDim:     512,
			Logger:        log,
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
			HybridConfig: vectorindex.HybridConfig{
				RRFK:              cfg.VectorIndex.HybridRRFK,
				FanoutMultiplier:  cfg.VectorIndex.HybridFanoutMultiplier,
				FanoutMin:         cfg.VectorIndex.HybridFanoutMin,
				VectorScoreFloor:  derefFloat(cfg.VectorIndex.HybridVectorScoreFloor),
				VectorScoreRatio:  derefFloat(cfg.VectorIndex.HybridVectorScoreRatio),
				LexicalScoreRatio: derefFloat(cfg.VectorIndex.HybridLexicalScoreRatio),
			},
			Logger: log,
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
		a.Lifecycle().SetVectorManager(vectorMgr)
		log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
	}()
}

// derefFloat returns *p when p is non-nil, else 0. Used to convert the
// pointer-float64 hybrid thresholds from config into the value-based
// vectorindex.HybridConfig.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
