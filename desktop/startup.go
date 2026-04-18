package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/user/agent/backend"
	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	beMcp "github.com/user/agent/backend/mcp"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"

	_ "modernc.org/sqlite" // register SQLite driver
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
	a.ctx = ctx

	// Load shell environment variables BEFORE any other initialization.
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// This ensures ${OPENAI_API_KEY} and similar vars in config.yaml resolve correctly.
	config.LoadShellEnvironment()

	// Initialize logger FIRST - before any other initialization
	// This ensures all startup errors are written to log files
	// Use a temporary default level; will re-init after config is loaded
	sessionLogger, err := logger.Init("INFO")
	if err != nil {
		// Can't log to file, but can still emit to frontend
		slog.Error("failed to initialize logger", "error", err)
	} else {
		a.sessionLogger = sessionLogger
	}
	var log *slog.Logger
	if sessionLogger != nil {
		log = sessionLogger.Logger()
	} else {
		log = slog.Default()
	}

	// Determine config path and load configuration via backend.
	resolved := config.ResolveAndLoad(log)
	a.config = resolved.Config
	a.configPath = resolved.ConfigPath
	a.configMigrated = resolved.Migrated
	a.configMigrationMsg = resolved.MigrationMsg
	a.configLoadErrors = resolved.LoadErrors
	agentDir := resolved.AgentDir

	// Initialize logLevel from config and re-init logger if level differs
	a.logLevel = a.config.LogLevel
	if a.logLevel != "" && a.logLevel != "INFO" {
		if newLogger, err := logger.Init(a.logLevel); err == nil {
			if a.sessionLogger != nil {
				if err := a.sessionLogger.Close(); err != nil {
					slog.Error("failed to close session logger", "error", err)
				}
			}
			a.sessionLogger = newLogger
			log = newLogger.Logger()
		}
	}

	// Note: workspace watcher is initialized per-project in SwitchProject

	// Initialize shared SQLite database
	dbPath := filepath.Join(agentDir, a.config.Memory.Database)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Error("failed to open sqlite database", "error", err)
	} else {
		// Apply pragmas once on the shared connection
		if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
			log.Error("failed to enable WAL mode", "error", err)
		}
		if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
			log.Error("failed to enable foreign keys", "error", err)
		}
		a.db = db

		// Project store first (sessions FK references projects)
		projStore, err := project.NewSQLiteProjectStore(db)
		if err != nil {
			log.Error("failed to init project store", "error", err)
		} else {
			a.projStore = projStore
		}

		// Session store (depends on projects table)
		store, err := session.NewSQLiteSessionStore(db)
		if err != nil {
			log.Error("failed to init session store", "error", err)
		} else {
			a.store = store
		}
	}

	// UI emit function: bridges events to Wails frontend (persistence is handled by Application).
	uiEmitFunc := func(evt session.Event) {
		slog.Debug("desktop: event received", "type", evt.Type, "sessionID", evt.SessionID)
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		wailsRuntime.EventsEmit(a.ctx, eventName, evt.Data)
		slog.Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}

	// --- Desktop UI callbacks ---

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
	stepLimitFunc := func(ctx context.Context, currentStep int, maxSteps int) (backend.StepLimitResponse, error) {
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

	// --- Create backend Application (owns builder, manager, persister) ---

	logDir := filepath.Join(agentDir, "logs")
	a.projectsDir = filepath.Join(agentDir, "Projects")
	if err := os.MkdirAll(a.projectsDir, 0o755); err != nil {
		log.Error("failed to create projects directory", "error", err)
	}

	application, err := backend.NewApplication(backend.ApplicationConfig{
		Config:        a.config,
		Logger:        log,
		AgentDir:      agentDir,
		LogDir:        logDir,
		ProjectsDir:   a.projectsDir,
		SessionStore:  a.store,
		TaskStore:     a.store,
		UIEmitFunc:    uiEmitFunc,
		AskUserFunc:   askUserFunc,
		ConfirmFunc:   confirmFunc,
		StepLimitFunc: stepLimitFunc,
	})
	if err != nil {
		log.Error("failed to create backend application", "error", err)
		wailsRuntime.EventsEmit(a.ctx, "startup_error", map[string]string{
			"message": "failed to create backend application",
			"error":   err.Error(),
		})
		return
	}
	a.app = application
	a.manager = application.Manager()

	// Filter out codebase-memory-mcp management tools that should not be exposed to the LLM.
	a.app.Builder().ToolRegistry().SetToolFilter(func(toolName, source string) bool {
		if source == "codebase-memory" {
			return toolName != "list_projects" && toolName != "delete_project"
		}
		return true
	})
	// Clean up already-registered tools (gateway registered them before filter was set).
	a.app.Builder().ToolRegistry().Unregister("list_projects")
	a.app.Builder().ToolRegistry().Unregister("delete_project")

	// Auto-inject project scoping for codebase-memory-mcp tools.
	a.app.Builder().ToolRegistry().SetParamInjector(func(toolName, source string, input json.RawMessage) json.RawMessage {
		if source != "codebase-memory" {
			return input
		}
		a.activeProjectMu.RLock()
		projectName := a.codebaseProjectName
		a.activeProjectMu.RUnlock()
		if projectName == "" {
			return input
		}
		var params map[string]any
		if err := json.Unmarshal(input, &params); err != nil {
			return input
		}
		if _, ok := params["project"]; ok {
			return input // don't override explicit project param
		}
		params["project"] = projectName
		modified, err := json.Marshal(params)
		if err != nil {
			return input
		}
		return modified
	})

	// Validate LLM provider configuration at startup (fail-fast).
	if a.config.LLM.ActiveProvider == "" {
		log.Error("no active LLM provider configured - check your config.yaml")
		wailsRuntime.EventsEmit(a.ctx, "startup_error", map[string]string{
			"message": "no active LLM provider configured - check your config.yaml",
			"error":   "config has no active_provider defined under llm",
		})
	}

	// Initialize project manager
	if a.projStore != nil {
		a.projectManager = project.NewManager(a.projStore, a.projectsDir)
	}

	// --- Wire Wails event listeners ---

	// Listen for confirmation responses from frontend
	wailsRuntime.EventsOn(a.ctx, "tool_confirm_response", func(data ...any) {
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
		}

		a.pendingConfirmations.Delete(requestID)
	})

	// Listen for on-demand judge verdict requests from frontend
	wailsRuntime.EventsOn(a.ctx, "tool_judge_request", func(data ...any) {
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

			responsePayload := session.JudgeResponsePayload{
				ConfirmID: confirmID,
			}

			_, reasoning, err := a.app.EvaluateJudge(a.ctx, pendingData.toolName, pendingData.input, pendingData.taskContext)
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
	wailsRuntime.EventsOn(a.ctx, "ask_user_response", func(data ...any) {
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
	wailsRuntime.EventsOn(a.ctx, "step_limit_response", func(data ...any) {
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

	// Check codebase-memory-mcp availability and emit status
	cmStatus := a.CheckCodebaseMemoryMCP()
	wailsRuntime.EventsEmit(a.ctx, "codememory:status", cmStatus)

	// Ensure auto_index is enabled if codebase-memory-mcp is installed
	if cmStatus.Installed {
		restoreFn, err := beMcp.EnsureAutoIndex(a.ctx)
		if err != nil {
			slog.Warn("failed to ensure codebase-memory-mcp auto_index", "error", err)
		}
		a.restoreAutoIndex = restoreFn
	}

	// Set pre-execute hook to block codebase-memory MCP tools during indexing
	a.app.Builder().ToolRegistry().SetPreExecuteHook(func(ctx context.Context, toolName, source string) error {
		if source != "codebase-memory" {
			return nil
		}
		a.indexingMu.Lock()
		ch := a.indexingDone
		a.indexingMu.Unlock()
		if ch == nil {
			return nil // not indexing
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID != "" {
			wailsRuntime.EventsEmit(a.ctx, "session:event", session.Event{
				SessionID: sessionID,
				Type:      "service",
				Data: map[string]any{
					"content": "Waiting for codebase indexing to complete...",
				},
			})
		}

		slog.Info("blocking MCP tool call until indexing completes", "tool", toolName)
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// Check rtk availability and emit status
	rtkStatus := a.CheckRtk()
	wailsRuntime.EventsEmit(a.ctx, "rtk:status", rtkStatus)

	// Signal frontend that all backend subsystems are ready.
	// Pre-load projects so the frontend doesn't need a separate round-trip.
	if a.projectManager != nil {
		projects, err := a.projectManager.ListProjects()
		if err == nil && len(projects) > 0 {
			wailsRuntime.EventsEmit(a.ctx, "backend:ready", projects)
		} else {
			if err != nil {
				log.Warn("failed to pre-load projects for backend:ready", "error", err)
			}
			wailsRuntime.EventsEmit(a.ctx, "backend:ready")
		}
	} else {
		wailsRuntime.EventsEmit(a.ctx, "backend:ready")
	}
}

// Shutdown is called when the Wails app is shutting down.
func (a *App) Shutdown(ctx context.Context) {
	// Restore original auto_index value before shutting down
	if a.restoreAutoIndex != nil {
		a.restoreAutoIndex()
	}

	if a.watcher != nil {
		if err := a.watcher.Close(); err != nil {
			slog.Error("failed to close workspace watcher", "error", err)
		}
		a.watcher = nil
	}

	if a.app != nil {
		a.app.Shutdown()
	}

	if a.store != nil {
		if err := a.store.Close(); err != nil {
			slog.Error("failed to close session store", "error", err)
		}
	}

	if a.projStore != nil {
		if err := a.projStore.Close(); err != nil {
			slog.Error("failed to close project store", "error", err)
		}
	}

	if a.db != nil {
		if err := a.db.Close(); err != nil {
			slog.Error("failed to close database", "error", err)
		}
	}

	if a.sessionLogger != nil {
		if err := a.sessionLogger.Close(); err != nil {
			slog.Error("failed to close session logger", "error", err)
		}
	}
}
