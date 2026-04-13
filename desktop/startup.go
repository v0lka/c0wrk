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
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	// SDK layer
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	sdktools "github.com/user/agent/sdk/tools"
	"github.com/user/agent/sdk/tools/builtins"
	websearch "github.com/user/agent/sdk/tools/builtins/web_search"

	// Core layer
	"github.com/user/agent/core"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/mcp"

	// Backend layer
	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"

	_ "modernc.org/sqlite" // register SQLite driver
)

// Note: time package is used for timeout configurations throughout startup

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
	a.ctx = ctx

	// Load shell environment variables BEFORE any other initialization.
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// This ensures ${OPENAI_API_KEY} and similar vars in config.yaml resolve correctly.
	loadShellEnvironment()

	// Collect environment info once for all sessions.
	a.envInfo = tools.CollectEnvInfo()

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

	// Determine config path: prefer ~/.c0wrk/config.yaml, fallback to ./config.yaml
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Error("failed to get user home directory", "error", err)
		homeDir = "."
	}
	agentDir := filepath.Join(homeDir, config.DefaultAgentDir)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		log.Error("failed to create agent directory", "error", err)
	}

	configPath := filepath.Join(agentDir, "config.yaml")
	result, err := config.LoadWithResult(configPath)
	if err != nil {
		// Fallback to local config.yaml if present
		fallbackPath := "config.yaml"
		if _, statErr := os.Stat(fallbackPath); statErr == nil {
			result, err = config.LoadWithResult(fallbackPath)
			if err == nil {
				configPath = fallbackPath
			}
		}
	}
	if err != nil || result == nil {
		// Use default config or log error
		log.Error("failed to load config", "error", err)
		a.config = &config.Config{}
		config.ApplyDefaults(a.config)
		log.Warn("config load failed, check your config.yaml syntax")
		errMsg := "Failed to load config"
		if err != nil {
			errMsg += ": " + err.Error()
		}
		a.configLoadErrors = []string{errMsg}
	} else {
		a.config = result.Config
		a.configMigrated = result.Migrated
		a.configMigrationMsg = result.MigrationMsg
		a.configLoadErrors = result.LoadErrors
		if result.Migrated {
			log.Info("config migrated", "message", result.MigrationMsg)
		}
		if len(result.LoadErrors) > 0 {
			for _, e := range result.LoadErrors {
				log.Warn("config warning", "error", e)
			}
		}
	}
	a.configPath = configPath

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

	// Create emit function that bridges to Wails events
	emitFunc := func(evt session.Event) {
		slog.Debug("desktop: event received", "type", evt.Type, "sessionID", evt.SessionID)
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		wailsRuntime.EventsEmit(a.ctx, eventName, evt.Data)
		slog.Debug("desktop: Wails EventsEmit called", "eventName", eventName)

		// Persist chat-visible events to SQLite
		if a.store == nil {
			return
		}

		var role, content string
		switch evt.Type {
		case "routing":
			role = "routing"
		case "tool_call":
			role = "tool_call"
		case "tool_result":
			role = "tool_result"
		case "evaluation":
			role = "eval"
		case "reflection":
			role = "reflection"
		case "plan_generated":
			role = "plan"
		case "error":
			role = "error"
		case "assistant_done":
			role = "assistant"
			// Extract content from typed struct or map (emitter uses map[string]interface{})
			switch d := evt.Data.(type) {
			case session.AssistantDoneEventData:
				content = d.Content
			case map[string]any:
				if c, ok := d["content"].(string); ok {
					content = c
				}
			}
		case "task_complete":
			// Only persist if there's output content
			switch d := evt.Data.(type) {
			case session.TaskCompleteData:
				if d.Output != "" {
					role = "assistant"
					content = d.Output
				}
			case map[string]any:
				if output, ok := d["output"].(string); ok && output != "" {
					role = "assistant"
					content = output
				}
			}
		case "thought":
			role = "thought"
			switch d := evt.Data.(type) {
			case session.ThoughtEventData:
				content = d.Content
			case map[string]any:
				if c, ok := d["content"].(string); ok {
					content = c
				}
			}
		case "step_start":
			role = "thinking"
		case "step_complete":
			role = "step_done"
		case "plan_step_start":
			slog.Debug("desktop: routing plan_step_start event", "sessionID", evt.SessionID)
			role = "plan_step_start"
		case "plan_step_complete":
			slog.Debug("desktop: routing plan_step_complete event", "sessionID", evt.SessionID)
			role = "plan_step_complete"
		case "retry":
			role = "retry"
		case "subagent_launch":
			role = "subagent_launch"
		case "subagent_complete":
			role = "subagent_complete"
		case "task_failed_resumable":
			role = "task_failed_resumable"
		case "task_resumed":
			role = "task_resumed"
		case "tool_confirm":
			role = "tool_confirm"
		case "ask_user":
			role = "ask_user"
		case "step_limit":
			role = "step_limit"
		case "task_cancelled":
			role = "task_cancelled"
		case "step_retry":
			role = "step_retry"
		case "service":
			// Only persist orchestration phase service events
			if data, ok := evt.Data.(map[string]any); ok {
				if phase, _ := data["phase"].(string); phase == "orchestration" {
					role = "status"
				}
			}
		case "session_tokens":
			return // Transient event: already emitted via Wails above, no persistence needed
		default:
			return // Don't persist transient events
		}

		if role == "" {
			return
		}

		// Serialize event data as metadata JSON
		var metadata json.RawMessage
		if evt.Data != nil {
			if b, err := json.Marshal(evt.Data); err == nil {
				metadata = b
			} else {
				metadata = json.RawMessage("{}")
			}
		} else {
			metadata = json.RawMessage("{}")
		}

		// For non-assistant roles, use metadata as content if content is empty
		if content == "" {
			content = string(metadata)
		}

		// Best-effort persistence: log and continue to avoid disrupting the user session.
		if err := a.store.SaveMessage(session.ChatMessage{
			SessionID: evt.SessionID,
			Role:      role,
			Content:   content,
			Metadata:  metadata,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			slog.Error("failed to persist event message", "type", evt.Type, "session", evt.SessionID, "error", err)
		} else {
			slog.Debug("desktop: event persisted to SQLite", "type", evt.Type, "sessionID", evt.SessionID, "role", role)
		}
	}

	// emitStartupError emits a critical startup error to the frontend
	emitStartupError := func(message string, err error) {
		log.Error(message, "error", err)
		wailsRuntime.EventsEmit(a.ctx, "startup_error", map[string]string{
			"message": message,
			"error":   err.Error(),
		})
	}

	// Build LLM Router + ModelRegistry at startup for validation (fail-fast).
	// These are also needed for the ToolJudge initialization.
	// The factory closure will rebuild them per-session from current config.
	llmRouter, modelRegistry, err := a.buildRouter(a.config)
	if err != nil {
		emitStartupError("failed to initialize LLM router", err)
		// Don't set llmRouter - it will remain nil and orchestrator creation will fail
		// with a descriptive error when the user tries to create a session
	}
	if llmRouter != nil && a.config.LLM.ActiveProvider == "" {
		emitStartupError("no active LLM provider configured - check your config.yaml", errors.New("config has no active_provider defined under llm"))
	}
	a.llmRouter = llmRouter
	_ = modelRegistry // used only for startup validation; factory rebuilds per-session

	// Initialize Tool Registry
	registry := tools.NewToolRegistry()
	a.toolRegistry = registry

	// Build tool limits from config
	fileLimits := builtins.FileLimits{
		ReadDefaultLines:  a.config.ToolLimits.ReadDefaultLines,
		ReadMaxLineLength: a.config.ToolLimits.ReadMaxLineLength,
		ReadMaxBytes:      a.config.ToolLimits.ReadMaxBytes,
		FileSearchMatches: a.config.ToolLimits.FileSearchMaxMatches,
	}
	ripgrepLimits := builtins.RipgrepLimits{
		MaxResults:    a.config.ToolLimits.RipgrepMaxResults,
		MaxLineLength: a.config.ToolLimits.RipgrepMaxLineLength,
		Timeout:       time.Duration(a.config.Timeouts.RipgrepTimeout) * time.Second,
	}
	globLimits := builtins.GlobLimits{
		MaxResults: a.config.ToolLimits.GlobMaxResults,
	}
	webFetchLimits := builtins.WebFetchLimits{
		MaxBodySize: a.config.ToolLimits.WebFetchMaxBodySize,
		Timeout:     time.Duration(a.config.Timeouts.WebFetchTimeout) * time.Second,
	}
	webSearchLimits := websearch.Limits{
		MaxResults: a.config.ToolLimits.WebSearchMaxResults,
		Timeout:    time.Duration(a.config.Timeouts.WebSearchTimeout) * time.Second,
	}
	batchLimits := builtins.BatchLimits{
		MaxConcurrency: a.config.ToolLimits.BatchMaxConcurrency,
		MaxResultSize:  a.config.ToolLimits.BatchMaxResultSize,
	}
	bashTimeouts := builtins.BashTimeouts{
		MaxTimeout: time.Duration(a.config.Timeouts.BashMaxTimeout) * time.Second,
		WaitDelay:  time.Duration(a.config.Timeouts.BashWaitDelay) * time.Second,
	}

	// Register core tools
	var bashBlacklist []string
	if bashCfg, ok := a.config.Security.ToolPolicies["bash_exec"]; ok {
		bashBlacklist = bashCfg.Blacklist
	}
	bashTool := builtins.NewBashExecToolWithTimeouts(bashBlacklist, bashTimeouts)
	registry.Register(bashTool)

	// File operation tools (individual tools replacing former monolithic tool)
	readFileTool := builtins.NewReadFileToolWithLimits(fileLimits)
	registry.Register(readFileTool)

	writeFileTool := builtins.NewWriteFileTool()
	registry.Register(writeFileTool)

	editFileTool := builtins.NewEditFileTool()
	registry.Register(editFileTool)

	listDirectoryTool := builtins.NewListDirectoryTool()
	registry.Register(listDirectoryTool)

	searchFilesTool := builtins.NewSearchFilesTool()
	registry.Register(searchFilesTool)

	searchContentTool := builtins.NewSearchContentToolWithLimits(fileLimits)
	registry.Register(searchContentTool)

	createDirectoryTool := builtins.NewCreateDirectoryTool()
	registry.Register(createDirectoryTool)

	deleteDirectoryTool := builtins.NewDeleteDirectoryTool()
	registry.Register(deleteDirectoryTool)

	deleteFileTool := builtins.NewDeleteFileTool()
	registry.Register(deleteFileTool)

	finishTool := agent.NewFinishTool()
	registry.Register(finishTool)

	// WebFetch tool
	webFetchTool := builtins.NewWebFetchToolWithLimits(webFetchLimits)
	registry.Register(webFetchTool)

	// WebSearch tool
	searchAPIKey := config.ExpandEnvVars(a.config.Search.APIKey)
	searchProvider := a.createSearchProvider(a.config.Search.Provider, searchAPIKey, webSearchLimits.Timeout)
	if searchProvider != nil {
		webSearchTool := websearch.NewWebSearchToolWithLimits(searchProvider, webSearchLimits)
		registry.Register(webSearchTool)
	}

	// Initialize MCP Gateway (optional)
	mcpEntries := make(map[string]mcp.ServerEntry, len(a.config.MCP.Servers))
	for name, cfg := range a.config.MCP.Servers {
		mcpEntries[name] = mcp.ServerEntry{
			Transport: cfg.Transport,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Env:       cfg.Env,
			URL:       cfg.URL,
			Headers:   cfg.Headers,
		}
	}
	gateway, mcpErr := mcp.StartGateway(context.Background(), mcp.GatewayConfig{Servers: mcpEntries}, registry, config.ExpandEnvVars, log)
	if mcpErr != nil {
		slog.Warn("MCP gateway startup failed", "error", mcpErr)
	}
	a.mcpGateway = gateway

	// Configure per-tool security policies from config
	policyOverrides := make(map[string]tools.ToolPolicy)
	for toolName, policyCfg := range a.config.Security.ToolPolicies {
		policyOverrides[toolName] = tools.ParseToolPolicy(policyCfg.Policy)
	}
	registry.SetPolicyOverrides(policyOverrides)

	// Set default policy for tools not explicitly configured
	if a.config.Security.DefaultPolicy != "" {
		registry.SetDefaultPolicy(tools.ParseToolPolicy(a.config.Security.DefaultPolicy))
	}

	// Glob tool (doublestar pattern matching)
	globTool := builtins.NewGlobToolWithLimits(globLimits)
	registry.Register(globTool)

	// Ripgrep tool (content search)
	ripgrepTool := builtins.NewRipgrepToolWithLimits(ripgrepLimits)
	registry.Register(ripgrepTool)

	// SharedWorkspace tools for reading step outputs
	readStepOutputTool := builtins.NewReadStepOutputTool()
	registry.Register(readStepOutputTool)

	listStepOutputsTool := builtins.NewListStepOutputsTool()
	registry.Register(listStepOutputsTool)

	// Batch tool (executes multiple tool calls in one request)
	batchTool := builtins.NewBatchTool(registry, builtins.WithBatchLimits(batchLimits))
	registry.Register(batchTool)

	// Ask User tool (interactive question panel)
	askUserTool := builtins.NewAskUserTool(func(ctx context.Context, req sdktools.AskUserRequest) (sdktools.AskUserResponse, error) {
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

		payload := session.AskUserPayload{
			RequestID: requestID,
			Questions: req.Questions,
		}

		emitFunc(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

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
	})
	registry.Register(askUserTool)

	// Validate LLM-dependent objects at startup (fail-fast).
	// The factory closure will rebuild these per-session from current config.
	if llmRouter != nil {
		_, _, _ = a.buildCoreAgents(llmRouter, registry, a.config, nil, nil, modelRegistry)
	}
	_ = a.buildOrchestratorConfig(a.config, nil)
	_ = a.buildContextFactory(llmRouter, a.config)

	// Initialize ToolJudge (also called on LLM settings update to keep judge in sync)
	a.rebuildJudge(a.config, llmRouter, log)

	// Wire tool confirmation callback (desktop-only)
	registry.SetConfirmFunc(func(ctx context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
		// If no UI context, allow once to avoid deadlock
		if a.ctx == nil {
			return tools.ConfirmAllowOnce, nil
		}

		// Extract session ID from context for session-scoped event emission
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			// No session context, allow once (shouldn't happen in normal desktop use)
			return tools.ConfirmAllowOnce, nil
		}

		requestID := uuid.New().String()
		ch := make(chan tools.ConfirmationResponse, 1)
		a.pendingConfirmations.Store(requestID, &pendingConfirmData{
			ch:          ch,
			taskContext: tools.TaskContextFrom(ctx),
			toolName:    req.ToolName,
			input:       req.Input,
			sessionID:   sessionID,
		})

		// Payload field names must match frontend ToolConfirmation.tsx expectations
		payload := session.ToolConfirmPayload{
			ConfirmID: requestID,
			Tool:      req.ToolName,
			Args:      string(req.Input),
			Reasoning: req.JudgeReasoning,
		}

		// Emit session-scoped event: session:{sessionId}:tool_confirm
		emitFunc(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			// Task was cancelled (e.g. user pressed Stop)
			a.pendingConfirmations.Delete(requestID)
			return tools.ConfirmDenyAndStop, ctx.Err()
		case <-a.ctx.Done():
			// App is shutting down, cancel the confirmation
			a.pendingConfirmations.Delete(requestID)
			return tools.ConfirmDenyAndStop, a.ctx.Err()
		}
	})

	// Create orchestrator factory — rebuilds all LLM-dependent objects per session
	// so that config changes (e.g. via UpdateLLMSettings) take effect for new sessions.
	// The tool registry is shared (expensive to rebuild, has dynamic policy updates).
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory) (*core.Orchestrator, error) {
		cfg := a.currentConfig()

		// Wrap emitter with logging so all execution events are logged to the session file.
		emitter = core.NewLoggingEmitter(emitter, logger)

		newLLMRouter, newModelRegistry, err := a.buildRouter(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to build LLM router: %w", err)
		}
		if cfg.LLM.ActiveProvider == "" {
			return nil, errors.New("no active LLM provider configured - check your config.yaml")
		}

		newRouter, newPlanner, newReflector := a.buildCoreAgents(newLLMRouter, registry, cfg, emitter, logger, newModelRegistry)
		if newRouter == nil || newPlanner == nil {
			return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, or planner is nil")
		}

		// Create the step limit function that will be called when an executor reaches its step limit
		stepLimitFunc := func(ctx context.Context, currentStep int, maxSteps int) (agent.StepLimitResponse, error) {
			// If no UI context, deny to avoid deadlock
			if a.ctx == nil {
				return agent.StepLimitDeny, nil
			}

			// Extract session ID from context for session-scoped event emission
			sessionID := session.SessionIDFromContext(ctx)
			if sessionID == "" {
				// No session context, deny (shouldn't happen in normal desktop use)
				return agent.StepLimitDeny, nil
			}

			requestID := uuid.New().String()
			ch := make(chan agent.StepLimitResponse, 1)
			a.pendingStepLimit.Store(requestID, ch)

			payload := session.StepLimitPayload{
				RequestID:   requestID,
				CurrentStep: currentStep,
				MaxSteps:    maxSteps,
			}

			// Emit session-scoped event: session:{sessionId}:step_limit
			emitFunc(session.Event{SessionID: sessionID, Type: "step_limit", Data: payload})

			select {
			case resp := <-ch:
				return resp, nil
			case <-ctx.Done():
				// Task was cancelled (e.g. user pressed Stop)
				a.pendingStepLimit.Delete(requestID)
				return agent.StepLimitDeny, ctx.Err()
			case <-a.ctx.Done():
				// App is shutting down, cancel the request
				a.pendingStepLimit.Delete(requestID)
				return agent.StepLimitDeny, a.ctx.Err()
			}
		}

		orchConfig := a.buildOrchestratorConfig(cfg, stepLimitFunc)
		contextFactory := a.buildContextFactory(newLLMRouter, cfg)

		tokenCounter := llm.NewSimpleTokenCounter()
		toolResultBudget := core.ToolResultBudget{
			HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
			MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
		}
		circuitBreaker := core.CircuitBreakerConfig{
			RepeatNudgeThreshold:     cfg.Executor.CircuitBreaker.RepeatNudgeThreshold,
			RepeatAbortThreshold:     cfg.Executor.CircuitBreaker.RepeatAbortThreshold,
			TruncationAbortThreshold: cfg.Executor.CircuitBreaker.TruncationAbortThreshold,
			ParseErrorAbortThreshold: cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold,
		}

		// Wrap the LLM caller for step-execution so those calls are also logged.
		loggedLLM := core.NewLoggingCaller(newLLMRouter, cfg.LLM.ActiveProvider, logger)

		return core.NewOrchestrator(
			newRouter,             // Router
			newPlanner,            // Planner
			loggedLLM,             // LLMCaller
			registry,              // ToolExecutor (shared)
			registry.ToolRegistry, // ToolRegistry (SDK base, shared)
			tokenCounter,          // TokenCounter
			orchConfig,
			contextFactory,
			newReflector,     // Reflector for retry-loop
			logger,           // Logger
			emitter,          // Emitter
			newModelRegistry, // ModelRegistry for resolving model metadata
			toolResultBudget,
			circuitBreaker,
			bbFactory, // BlackboardFactory (nil = default MapBlackboard)
		), nil
	}

	logDir := filepath.Join(agentDir, "logs")

	// Initialize project manager
	a.projectsDir = filepath.Join(agentDir, "Projects")
	if err := os.MkdirAll(a.projectsDir, 0o755); err != nil {
		log.Error("failed to create projects directory", "error", err)
	}
	if a.projStore != nil {
		a.projectManager = project.NewManager(a.projStore, a.projectsDir)
	}

	a.manager = session.NewManager(factory, emitFunc, logDir, a.projectsDir)

	// Pass environment info to session manager for context injection.
	if a.envInfo != nil {
		a.manager.SetEnvInfo(a.envInfo)
	}

	// File watcher is NOT initialized at startup — it's created per-project in SwitchProject.

	// Wire token persistence: each emitter will call store.UpdateSessionTokens
	// with cumulative totals after every AssistantDone.
	if a.store != nil {
		a.manager.SetTokenPersist(func(sessionID string, inputTokens, outputTokens int) {
			if err := a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens); err != nil {
				slog.Error("failed to persist session tokens", "session", sessionID, "error", err)
			}
		})

		// Wire task persistence: CreateSession will build BlackboardFactory closures
		// that create PersistentBlackboard instances backed by the session store.
		a.manager.SetTaskStore(a.store)

		// Wire max summary length from config for auto-generated step summaries.
		a.manager.SetMaxSummaryLen(a.config.Orchestration.MaxSummaryLength)
	}

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

		var resp tools.ConfirmationResponse
		switch v := decisionVal.(type) {
		case float64:
			resp = tools.ConfirmationResponse(int(v))
		case int:
			resp = tools.ConfirmationResponse(v)
		case string:
			// Allow string codes from frontend
			switch v {
			case "allow_once":
				resp = tools.ConfirmAllowOnce
			case "deny":
				resp = tools.ConfirmDeny
			case "stop":
				// Frontend sends "stop" for deny_and_stop
				resp = tools.ConfirmDenyAndStop
			case "deny_and_stop":
				resp = tools.ConfirmDenyAndStop
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
			responsePayload := session.JudgeResponsePayload{
				ConfirmID: confirmID,
			}

			judge := a.toolRegistry.GetJudge()
			if judge == nil {
				responsePayload.Error = "Judge is not available. Check LLM provider configuration."
				emitFunc(session.Event{SessionID: pendingData.sessionID, Type: "tool_judge_response", Data: responsePayload})
				return
			}

			verdict, reasoning, err := judge.Judge(a.ctx, pendingData.toolName, pendingData.input, pendingData.taskContext)
			if err != nil {
				responsePayload.Error = fmt.Sprintf("Judge evaluation failed: %v", err)
				responsePayload.Reasoning = reasoning
			} else {
				if verdict == tools.VerdictAllow {
					responsePayload.Reasoning = "SAFE: " + reasoning
				} else {
					responsePayload.Reasoning = reasoning
				}
			}

			emitFunc(session.Event{SessionID: pendingData.sessionID, Type: "tool_judge_response", Data: responsePayload})
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
		var resp sdktools.AskUserResponse

		// Parse answers array
		if answersVal, ok := payload["answers"]; ok {
			if answersArr, ok := answersVal.([]any); ok {
				for _, item := range answersArr {
					answerMap, ok := item.(map[string]any)
					if !ok {
						continue
					}
					var answer sdktools.AskUserAnswer
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
		ch, ok := chVal.(chan sdktools.AskUserResponse)
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

		var resp agent.StepLimitResponse
		switch v := responseVal.(type) {
		case string:
			resp = agent.StepLimitResponse(v)
		default:
			log.Warn("step_limit response has unsupported type", "type", fmt.Sprintf("%T", responseVal))
			return
		}

		chVal, ok := a.pendingStepLimit.Load(requestID)
		if !ok {
			log.Warn("no pending step_limit for request_id", "request_id", requestID)
			return
		}
		ch, ok := chVal.(chan agent.StepLimitResponse)
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

// Shutdown is called when the Wails app is shutting down.
func (a *App) Shutdown(ctx context.Context) {
	if a.watcher != nil {
		if err := a.watcher.Close(); err != nil {
			slog.Error("failed to close workspace watcher", "error", err)
		}
		a.watcher = nil
	}

	if a.manager != nil {
		a.manager.Shutdown()
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

	if a.mcpGateway != nil {
		if err := a.mcpGateway.Stop(); err != nil {
			slog.Error("failed to stop MCP gateway", "error", err)
		}
	}

	if a.sessionLogger != nil {
		if err := a.sessionLogger.Close(); err != nil {
			slog.Error("failed to close session logger", "error", err)
		}
	}
}

// createSearchProvider creates a search provider based on the configured provider name.
// Returns nil if the provider requires an API key but none is configured.
func (a *App) createSearchProvider(providerName, apiKey string, timeout time.Duration) websearch.SearchProvider {
	switch providerName {
	case "brave":
		if apiKey == "" {
			return nil
		}
		return websearch.NewBraveProviderWithTimeout(apiKey, timeout)
	case "exa":
		if apiKey == "" {
			return nil
		}
		return websearch.NewExaProviderWithTimeout(apiKey, timeout)
	case "duckduckgo":
		// DuckDuckGo does not require an API key
		return websearch.NewDuckDuckGoProviderWithTimeout(timeout)
	default: // "tavily" or empty
		if apiKey == "" {
			return nil
		}
		return websearch.NewTavilyProviderWithTimeout(apiKey, timeout)
	}
}
