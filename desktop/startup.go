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
	dbPath := filepath.Join(agentDir, "sessions.db")
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
	// These are also needed for the ToolJudge and WebFetch summarizer initialization.
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

	// Register core tools
	var bashBlacklist []string
	if bashCfg, ok := a.config.Security.ToolPolicies["bash_exec"]; ok {
		bashBlacklist = bashCfg.Blacklist
	}
	bashTool := builtins.NewBashExecTool(bashBlacklist)
	registry.Register(bashTool)

	fileOpsTool := builtins.NewFileOpsTool()
	registry.Register(fileOpsTool)

	finishTool := agent.NewFinishTool()
	registry.Register(finishTool)

	// Web tools: WebFetch with LLM summarizer.
	// The summarizer reads a.llmRouter dynamically so that LLM config
	// changes via the UI take effect without an application restart.
	summarizer := builtins.LLMSummarizer(func(ctx context.Context, content string, prompt string) (string, error) {
		a.configMu.RLock()
		router := a.llmRouter
		a.configMu.RUnlock()
		if router == nil {
			return "", errors.New("LLM router not configured")
		}
		req := llm.ChatRequest{
			Messages: []llm.Message{
				{Role: "user", Content: content + "\n\n" + prompt},
			},
		}
		resp, err := router.Call(ctx, req)
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	})
	webFetchTool := builtins.NewWebFetchTool(summarizer)
	registry.Register(webFetchTool)

	// WebSearch tool
	searchAPIKey := config.ExpandEnvVars(a.config.Search.APIKey)
	searchProvider := a.createSearchProvider(a.config.Search.Provider, searchAPIKey)
	if searchProvider != nil {
		webSearchTool := websearch.NewWebSearchTool(searchProvider)
		registry.Register(webSearchTool)
	}

	// Initialize MCP Gateway (optional)
	mcpEntries := make(map[string]mcp.ServerEntry, len(a.config.MCP.Servers))
	for name, cfg := range a.config.MCP.Servers {
		mcpEntries[name] = mcp.ServerEntry{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
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
	globTool := builtins.NewGlobTool()
	registry.Register(globTool)

	// Ripgrep tool (content search)
	ripgrepTool := builtins.NewRipgrepTool()
	registry.Register(ripgrepTool)

	// SharedWorkspace tools for reading step outputs
	readStepOutputTool := builtins.NewReadStepOutputTool()
	registry.Register(readStepOutputTool)

	listStepOutputsTool := builtins.NewListStepOutputsTool()
	registry.Register(listStepOutputsTool)

	// Batch tool (executes multiple tool calls in one request)
	batchTool := builtins.NewBatchTool(registry)
	registry.Register(batchTool)

	// Ask User tool (interactive question panel)
	askUserTool := builtins.NewAskUserTool(func(ctx context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		if a.ctx == nil {
			return tools.AskUserResponse{}, errors.New("ask_user not available: no UI context")
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return tools.AskUserResponse{}, errors.New("ask_user not available: no session context")
		}

		requestID := uuid.New().String()
		ch := make(chan tools.AskUserResponse, 1)
		a.pendingAskUser.Store(requestID, ch)

		payload := session.AskUserPayload{
			RequestID:   requestID,
			Question:    req.Question,
			Options:     req.Options,
			MultiSelect: req.MultiSelect,
			Recommended: req.Recommended,
		}

		emitFunc(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return tools.AskUserResponse{}, ctx.Err()
		case <-a.ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return tools.AskUserResponse{}, a.ctx.Err()
		}
	})
	registry.Register(askUserTool)

	// Validate LLM-dependent objects at startup (fail-fast).
	// The factory closure will rebuild these per-session from current config.
	if llmRouter != nil {
		_, _, _ = a.buildCoreAgents(llmRouter, registry, a.config, nil, nil)
	}
	_ = a.buildOrchestratorConfig(a.config)
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
		a.pendingConfirmations.Store(requestID, ch)

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

		newRouter, newPlanner, newReflector := a.buildCoreAgents(newLLMRouter, registry, cfg, emitter, logger)
		if newRouter == nil || newPlanner == nil {
			return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, or planner is nil")
		}

		orchConfig := a.buildOrchestratorConfig(cfg)
		contextFactory := a.buildContextFactory(newLLMRouter, cfg)

		tokenCounter := llm.NewSimpleTokenCounter()
		toolResultBudget := core.ToolResultBudget{
			HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
			MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
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

	a.manager = session.NewManager(factory, emitFunc, logDir)

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

		chVal, ok := a.pendingConfirmations.Load(requestID)
		if !ok {
			log.Warn("no pending confirmation for confirm_id", "confirm_id", requestID)
			return
		}
		ch, ok := chVal.(chan tools.ConfirmationResponse)
		if !ok {
			log.Warn("pending confirmation has wrong type", "confirm_id", requestID)
			a.pendingConfirmations.Delete(requestID)
			return
		}

		select {
		case ch <- resp:
		default:
			// Channel already has a value or receiver gone; drop
		}

		a.pendingConfirmations.Delete(requestID)
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
		var resp tools.AskUserResponse

		// Parse selected options
		if selectedVal, ok := payload["selected"]; ok {
			if selectedArr, ok := selectedVal.([]any); ok {
				for _, v := range selectedArr {
					if s, ok := v.(string); ok {
						resp.Selected = append(resp.Selected, s)
					}
				}
			}
		}

		// Parse custom text
		if customVal, ok := payload["custom_text"]; ok {
			if s, ok := customVal.(string); ok {
				resp.CustomText = s
			}
		}

		chVal, ok := a.pendingAskUser.Load(requestID)
		if !ok {
			log.Warn("no pending ask_user for request_id", "request_id", requestID)
			return
		}
		ch, ok := chVal.(chan tools.AskUserResponse)
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
func (a *App) createSearchProvider(providerName, apiKey string) websearch.SearchProvider {
	switch providerName {
	case "brave":
		if apiKey == "" {
			return nil
		}
		return websearch.NewBraveProvider(apiKey)
	case "exa":
		if apiKey == "" {
			return nil
		}
		return websearch.NewExaProvider(apiKey)
	case "duckduckgo":
		// DuckDuckGo does not require an API key
		return websearch.NewDuckDuckGoProvider()
	default: // "tavily" or empty
		if apiKey == "" {
			return nil
		}
		return websearch.NewTavilyProvider(apiKey)
	}
}
