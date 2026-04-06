package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	// SDK layer
	"github.com/user/agent/sdk/llm"
	sdkmemory "github.com/user/agent/sdk/memory"
	"github.com/user/agent/sdk/tools/builtins"

	// Core layer
	"github.com/user/agent/core"
	toolcore "github.com/user/agent/core/coretools"

	// Backend layer
	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/logger"
	"github.com/user/agent/backend/memory"
	"github.com/user/agent/backend/session"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/external"
	"github.com/user/agent/core/tools/mcp"

	// Workspace layer
	"github.com/user/agent/backend/workspace"
)

// compactionSummarizePrompt is the system prompt used when summarizing step blocks
// for context window compaction. The LLM is asked to produce concise summaries
// that preserve critical decision points and outcomes.
const compactionSummarizePrompt = `Summarize the following agent execution steps concisely. Preserve:
- Key decisions made
- Important tool results and their outcomes  
- Critical observations and findings
- Final state or conclusion

Be brief but complete. Output only the summary, no preamble.`

// loadShellEnvironment loads environment variables from the user's shell profile.
// This is necessary on macOS where apps launched from Finder/Dock don't inherit
// shell environment variables (like those set in .zshrc/.bash_profile).
// The function is best-effort: failures are logged but don't block startup.
func loadShellEnvironment() {
	// Only needed on macOS; Linux inherits environment normally
	if runtime.GOOS != "darwin" {
		return
	}

	// Get user's shell from SHELL env var, fallback to zsh on macOS
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}

	// Run shell with -l (login) flag to source profile files
	// We avoid -i (interactive) to prevent extra output from .zshrc/.bashrc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "printenv")
	cmd.Stderr = nil // Discard stderr to avoid noise

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			slog.Warn("timeout loading shell environment", "shell", shell)
		} else {
			slog.Warn("failed to load shell environment", "shell", shell, "error", err)
		}
		return
	}

	// Parse KEY=VALUE lines and set environment variables
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	loaded := 0
	var setErrors int
	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Find first '=' to split key and value
		eqIdx := strings.Index(line, "=")
		if eqIdx <= 0 {
			// Line without '=' or starting with '=' is invalid; skip
			continue
		}

		key := line[:eqIdx]
		value := line[eqIdx+1:]

		// Don't override already-set variables; shell profile is source of truth
		// but we want to respect explicit env vars if set by launcher
		if os.Getenv(key) != "" {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			setErrors++
			// Log but continue - some vars may not be settable
			slog.Debug("failed to set env var", "key", key, "error", err)
			continue
		}
		loaded++
	}

	if setErrors > 0 {
		slog.Warn("some shell environment variables could not be set", "failed", setErrors, "loaded", loaded)
	}

	if loaded > 0 {
		slog.Debug("loaded shell environment variables", "count", loaded, "shell", shell)
	}
}

// App struct holds the Wails application state and exposes methods to the frontend.
type App struct {
	ctx        context.Context
	manager    *session.Manager
	store      *session.SQLiteSessionStore
	config     *config.Config
	configMu   sync.RWMutex // protects config and config-related state
	configPath string

	llmRouter    *llm.LLMRouter
	toolRegistry *tools.ToolRegistry
	mcpGateway   *mcp.MCPGateway

	sessionLogger *logger.SessionLogger
	logLevel      string

	// Config loading state for UI warnings
	configMigrated     bool
	configMigrationMsg string
	configLoadErrors   []string

	pendingConfirmations sync.Map
	pendingAskUser       sync.Map

	watcher       *workspace.Watcher
	workspacesDir string // parent directory for all session workspaces
}

// currentConfig returns a shallow copy of the current config, safe to read without holding the lock.
// Slices and maps are deep-copied so the snapshot is fully independent from the live config.
func (a *App) currentConfig() *config.Config {
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	if a.config == nil {
		return nil
	}
	cfgCopy := *a.config

	// Deep-copy LLM.Models (map)
	if a.config.LLM.Models != nil {
		cfgCopy.LLM.Models = make(map[string]config.ModelOverride, len(a.config.LLM.Models))
		for k, v := range a.config.LLM.Models {
			cfgCopy.LLM.Models[k] = v
		}
	}

	// Deep-copy MCP.Servers (map containing slices and maps)
	if a.config.MCP.Servers != nil {
		cfgCopy.MCP.Servers = make(map[string]config.MCPServerConfig, len(a.config.MCP.Servers))
		for k, v := range a.config.MCP.Servers {
			srv := v
			if v.Args != nil {
				srv.Args = make([]string, len(v.Args))
				copy(srv.Args, v.Args)
			}
			if v.Env != nil {
				srv.Env = make(map[string]string, len(v.Env))
				for ek, ev := range v.Env {
					srv.Env[ek] = ev
				}
			}
			cfgCopy.MCP.Servers[k] = srv
		}
	}

	// Deep-copy Security.ToolPolicies (map containing slices)
	if a.config.Security.ToolPolicies != nil {
		cfgCopy.Security.ToolPolicies = make(map[string]config.ToolPolicyConfig, len(a.config.Security.ToolPolicies))
		for k, v := range a.config.Security.ToolPolicies {
			tp := v
			if v.Blacklist != nil {
				tp.Blacklist = make([]string, len(v.Blacklist))
				copy(tp.Blacklist, v.Blacklist)
			}
			cfgCopy.Security.ToolPolicies[k] = tp
		}
	}

	return &cfgCopy
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
}

// startup is called when the Wails app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Load shell environment variables BEFORE any other initialization.
	// On macOS, apps launched from Finder/Dock don't inherit shell env vars.
	// This ensures ${OPENAI_API_KEY} and similar vars in config.yaml resolve correctly.
	loadShellEnvironment()

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
	log := sessionLogger.Logger()

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
		a.configLoadErrors = []string{"Failed to load config: " + err.Error()}
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

	// Note: workspace watcher will be initialized after workspacesDir is set below

	// Initialize SQLite session store
	dbPath := filepath.Join(agentDir, "sessions.db")
	store, err := session.NewSQLiteSessionStore(dbPath)
	if err != nil {
		log.Error("failed to init session store", "error", err)
		// Continue without persistence
	} else {
		a.store = store
	}

	// Create emit function that bridges to Wails events
	emitFunc := func(evt session.Event) {
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		wailsRuntime.EventsEmit(a.ctx, eventName, evt.Data)

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
			role = "plan_step_start"
		case "plan_step_complete":
			role = "plan_step_complete"
		case "retry":
			role = "retry"
		case "ac_extracted":
			role = "ac_extracted"
		case "subagent_launch":
			role = "subagent_launch"
		case "subagent_complete":
			role = "subagent_complete"
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
	llmRouter, modelRegistry, err := a.buildLLMRouter(a.config)
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

	finishTool := core.NewFinishTool()
	registry.Register(finishTool)

	// Web tools: WebFetch with optional LLM summarizer
	var summarizer builtins.LLMSummarizer
	if llmRouter != nil {
		summarizer = func(ctx context.Context, content string, prompt string) (string, error) {
			req := llm.ChatRequest{
				Messages: []llm.Message{
					{Role: "user", Content: content + "\n\n" + prompt},
				},
			}
			resp, err := llmRouter.Call(ctx, req)
			if err != nil {
				return "", err
			}
			return resp.Message.Content, nil
		}
	}
	webFetchTool := builtins.NewWebFetchTool(summarizer)
	registry.Register(webFetchTool)

	// WebSearch tool (requires Tavily API key)
	searchAPIKey := config.ExpandEnvVars(a.config.Search.APIKey)
	if searchAPIKey != "" {
		webSearchTool := toolcore.NewWebSearchTool(searchAPIKey)
		registry.Register(webSearchTool)
	}

	// Initialize MCP Gateway (optional)
	if len(a.config.MCP.Servers) > 0 {
		// Convert config MCP server configs to core/tools/mcp types
		mcpConfigs := make(map[string]mcp.MCPServerConfig, len(a.config.MCP.Servers))
		for name, cfg := range a.config.MCP.Servers {
			mcpConfigs[name] = mcp.MCPServerConfig{
				Command: cfg.Command,
				Args:    cfg.Args,
				Env:     cfg.Env,
			}
		}

		gateway := mcp.NewMCPGateway()
		if err := gateway.Start(context.Background(), mcpConfigs); err != nil {
			log.Warn("MCP gateway start errors", "error", err)
			// MCP is optional; continue
		}

		if err := gateway.RegisterTools(registry); err != nil {
			log.Warn("MCP tool registration errors", "error", err)
		}

		a.mcpGateway = gateway
	}

	// Initialize external tools directory
	toolsDir := a.config.ExternalTools.Directory
	if toolsDir == "" {
		toolsDir = filepath.Join(agentDir, "tools")
	}
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		log.Warn("failed to create tools directory", "error", err)
	}

	// Register existing external tools from tools directory
	procMem := memory.NewProceduralMemory(toolsDir)
	if err := procMem.Scan(); err != nil {
		log.Warn("failed to scan tools directory", "error", err)
	}
	for _, info := range procMem.ListTools() {
		manifest, err := external.ParseManifest(filepath.Join(info.Path, "tool.json"))
		if err != nil {
			log.Warn("failed to parse tool manifest", "tool", info.Name, "error", err)
			continue
		}
		extTool := external.NewExternalTool(manifest, info.Path)
		registry.RegisterWithSource(extTool, "external")
	}

	// Register ToolCreatorTool for dynamic tool creation
	toolCreator := toolcore.NewToolCreatorTool(toolsDir, registry.ToolRegistry)
	registry.Register(toolCreator)

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

		eventName := fmt.Sprintf("session:%s:ask_user", sessionID)
		wailsRuntime.EventsEmit(a.ctx, eventName, payload)

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
		_, _, _, _, _ = a.buildCoreAgents(llmRouter, registry, a.config, nil)
	}
	_ = a.buildOrchestratorConfig(a.config)
	_ = a.buildContextFactory(llmRouter, a.config)

	// Initialize ToolJudge if enabled in config
	if a.config.Security.Judge.Enabled != nil && *a.config.Security.Judge.Enabled && llmRouter != nil {
		var judgeProvider llm.LLMProvider
		var judgeModel string

		// Check if a specific model is configured for the judge
		if a.config.Security.Judge.Model != "" {
			judgeModel = a.config.Security.Judge.Model
			judgeProvider = llmRouter.GetDefaultProvider()
		} else {
			// Use the active provider's model
			judgeProvider = llmRouter.GetDefaultProvider()
			_, _, _, judgeModel = a.config.LLM.GetActiveProviderConfig()
		}

		if judgeProvider != nil && judgeModel != "" {
			judge := tools.NewToolJudge(judgeProvider, judgeModel)
			registry.SetJudge(judge)
			log.Info("tool judge initialized", "model", judgeModel)
		} else if judgeProvider != nil {
			log.Warn("tool judge disabled: no model configured")
		}
	}

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
		eventName := fmt.Sprintf("session:%s:tool_confirm", sessionID)
		wailsRuntime.EventsEmit(a.ctx, eventName, payload)

		select {
		case resp := <-ch:
			return resp, nil
		case <-a.ctx.Done():
			// App is shutting down, cancel the confirmation
			a.pendingConfirmations.Delete(requestID)
			return tools.ConfirmDenyAndStop, a.ctx.Err()
		}
	})

	// Create orchestrator factory — rebuilds all LLM-dependent objects per session
	// so that config changes (e.g. via UpdateLLMSettings) take effect for new sessions.
	// The tool registry is shared (expensive to rebuild, has dynamic policy updates).
	factory := func(emitter core.Emitter, logger *slog.Logger) (*core.Orchestrator, error) {
		cfg := a.currentConfig()

		newLLMRouter, newModelRegistry, err := a.buildLLMRouter(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to build LLM router: %w", err)
		}
		if cfg.LLM.ActiveProvider == "" {
			return nil, errors.New("no active LLM provider configured - check your config.yaml")
		}

		newRouter, newACExtractor, newPlanner, newEvaluator, newReflector := a.buildCoreAgents(newLLMRouter, registry, cfg, emitter)
		if newRouter == nil || newACExtractor == nil || newPlanner == nil || newEvaluator == nil {
			return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, AC extractor, planner, or evaluator is nil")
		}

		orchConfig := a.buildOrchestratorConfig(cfg)
		contextFactory := a.buildContextFactory(newLLMRouter, cfg)

		return core.NewOrchestrator(
			newRouter,                   // Router
			newACExtractor,              // ACExtractor
			newPlanner,                  // Planner
			newEvaluator,                // Evaluator
			newLLMRouter,                // LLMCaller
			registry,                    // ToolExecutor (shared)
			registry.ToolRegistry,       // ToolRegistry (SDK base, shared)
			llm.NewSimpleTokenCounter(), // TokenCounter (for backward compatibility)
			orchConfig,
			contextFactory,
			newReflector,    // Reflector for retry-loop
			logger,          // Logger
			emitter,         // Emitter
			newModelRegistry, // ModelRegistry for resolving model metadata
			core.ToolResultBudget{
				HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
				MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
			},
		), nil
	}

	logDir := filepath.Join(agentDir, "logs")
	a.workspacesDir = filepath.Join(agentDir, "workspaces")
	a.manager = session.NewManager(factory, emitFunc, logDir, a.workspacesDir)

	// Initialize file watcher on the workspaces parent directory
	watcher, err := workspace.NewWatcher(a.workspacesDir, func() {
		wailsRuntime.EventsEmit(a.ctx, "workspace:tree_changed", nil)
	})
	if err != nil {
		log.Warn("failed to start workspace file watcher", "error", err)
	} else {
		a.watcher = watcher
	}

	// Wire token persistence: each emitter will call store.UpdateSessionTokens
	// with cumulative totals after every AssistantDone.
	if a.store != nil {
		a.manager.SetTokenPersist(func(sessionID string, inputTokens, outputTokens int) {
			if err := a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens); err != nil {
				slog.Error("failed to persist session tokens", "session", sessionID, "error", err)
			}
		})
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

		ch <- resp
		a.pendingAskUser.Delete(requestID)
	})
}

// buildLLMRouter creates a new LLMRouter and ModelRegistry from the given config.
// This is extracted from startup() so it can be called per-session in the factory closure.
func (a *App) buildLLMRouter(cfg *config.Config) (*llm.LLMRouter, *llm.ModelRegistry, error) {
	// Create ModelRegistry from config overrides
	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range cfg.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: "approximate",
		}
	}
	modelRegistry := llm.NewModelRegistry(overrides)

	// Initialize LLM Router
	provType, apiKey, baseURL, model := cfg.LLM.GetActiveProviderConfig()
	initialBackoff, _ := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	maxBackoff, _ := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
	routerCfg := llm.RouterConfig{
		ActiveProvider: cfg.LLM.ActiveProvider,
		ProviderType:   provType,
		APIKey:         config.ExpandEnvVars(apiKey),
		BaseURL:        config.ExpandEnvVars(baseURL),
		Model:          model,
		MaxRetries:     cfg.LLM.Retry.MaxRetries,
		InitialBackoff: initialBackoff,
		MaxBackoff:     maxBackoff,
	}
	llmRouter, err := llm.NewLLMRouter(routerCfg, modelRegistry)
	if err != nil {
		return nil, nil, err
	}
	return llmRouter, modelRegistry, nil
}

// buildCoreAgents creates the Phase 2 components (router, acExtractor, planner, evaluator, reflector)
// from the given LLM router and config. Returns nil values if llmRouter is nil.
// When emitter is non-nil, each component receives a token-tracking wrapper so
// that service-level LLM calls are accumulated in session totals.
func (a *App) buildCoreAgents(llmRouter *llm.LLMRouter, registry *tools.ToolRegistry, cfg *config.Config, emitter core.Emitter) (*core.Router, *core.ACExtractor, *core.Planner, *core.Evaluator, *core.Reflector) {
	if llmRouter == nil {
		return nil, nil, nil, nil, nil
	}
	caller := core.NewTokenTrackingCaller(llmRouter, emitter)
	router := core.NewRouter(caller, cfg.Router.HistoryWindow)
	acExtractor := core.NewACExtractor(caller)
	planner := core.NewPlanner(caller)
	evaluator := core.NewEvaluator(registry, caller)
	reflector := core.NewReflector(caller)
	return router, acExtractor, planner, evaluator, reflector
}

// buildOrchestratorConfig creates an OrchestratorConfig from the given config.
func (a *App) buildOrchestratorConfig(cfg *config.Config) core.OrchestratorConfig {
	return core.OrchestratorConfig{
		MaxSteps:   cfg.Executor.MaxReactSteps,
		KeepFirst:  cfg.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:   cfg.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries: cfg.Executor.MaxRetries,
	}
}

// buildContextFactory creates a ContextManagerFactory from the given LLM router and config.
func (a *App) buildContextFactory(llmRouter *llm.LLMRouter, cfg *config.Config) core.ContextManagerFactory {
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) core.ContextManager {
		counter := llm.NewTokenCounter(modelMeta.TokenizerType)
		tracker := llm.NewContextTokenTracker(counter)

		strategy := sdkmemory.NewCompactionStrategy(compactionStrategy, sdkmemory.CompactionConfig{
			SlidingWindow: struct{ KeepFirst, KeepLast int }{
				KeepFirst: cfg.Executor.Compaction.SlidingWindow.KeepFirst,
				KeepLast:  cfg.Executor.Compaction.SlidingWindow.KeepLast,
			},
			Summarization: struct{ BlockSize, KeepLast int }{
				BlockSize: cfg.Executor.Compaction.Summarization.BlockSize,
				KeepLast:  5,
			},
			Hierarchical: struct{ DistantRatio, MiddleRatio, RecentRatio float64 }{
				DistantRatio: 0.4,
				MiddleRatio:  0.3,
				RecentRatio:  0.3,
			},
		}, sdkmemory.CompactionDeps{
			TokenCounter: counter,
			Summarize: func(ctx context.Context, blockText string) (string, error) {
				if llmRouter == nil {
					return "", errors.New("compaction summarize: LLM router not available")
				}
				req := llm.ChatRequest{
					Messages: []llm.Message{
						{Role: "system", Content: compactionSummarizePrompt},
						{Role: "user", Content: blockText},
					},
				}
				resp, err := llmRouter.Call(ctx, req)
				if err != nil {
					return "", fmt.Errorf("compaction summarize: %w", err)
				}
				return resp.Message.Content, nil
			},
		})

		thresholds := sdkmemory.CompactionThresholds{
			PredictivePercent: cfg.Executor.Compaction.Thresholds.PredictivePercent,
			WarningPercent:    cfg.Executor.Compaction.Thresholds.WarningPercent,
			EmergencyPercent:  cfg.Executor.Compaction.Thresholds.EmergencyPercent,
		}

		cw := sdkmemory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy)
		return core.NewCoreContextManager(cw)
	}
}

// GetSessionWorkspace returns the workspace directory path for a given session.
func (a *App) GetSessionWorkspace(sessionID string) (string, error) {
	if a.manager == nil {
		return "", errors.New("session manager not initialized")
	}
	path, exists := a.manager.GetSessionWorkspacePath(sessionID)
	if !exists {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	return path, nil
}

// ListDirectory returns the immediate children of a directory, sorted directories first then alphabetically.
func (a *App) ListDirectory(dirPath string) ([]FileNode, error) {
	if a.workspacesDir == "" {
		return nil, errors.New("workspaces directory not set")
	}

	// Security: resolve and validate the path is under workspaces directory
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err := filepath.Abs(a.workspacesDir)
	if err != nil {
		return nil, fmt.Errorf("invalid workspaces directory: %w", err)
	}
	if !strings.HasPrefix(absPath, absRoot) {
		return nil, errors.New("path is outside workspaces directory")
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var dirs, files []FileNode
	for _, entry := range entries {
		node := FileNode{
			Name:  entry.Name(),
			Path:  filepath.Join(absPath, entry.Name()),
			IsDir: entry.IsDir(),
		}
		if entry.IsDir() {
			dirs = append(dirs, node)
		} else {
			files = append(files, node)
		}
	}

	// Sort each group alphabetically
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	nodes := make([]FileNode, 0, len(dirs)+len(files))
	nodes = append(nodes, dirs...)
	nodes = append(nodes, files...)
	return nodes, nil
}

// WatchDirectory adds a directory to the file watcher.
func (a *App) WatchDirectory(dirPath string) error {
	if a.watcher == nil {
		return nil
	}
	return a.watcher.WatchDir(dirPath)
}

// UnwatchDirectory removes a directory from the file watcher.
func (a *App) UnwatchDirectory(dirPath string) error {
	if a.watcher == nil {
		return nil
	}
	return a.watcher.UnwatchDir(dirPath)
}

func (a *App) shutdown(ctx context.Context) {
	if a.watcher != nil {
		if err := a.watcher.Close(); err != nil {
			slog.Error("failed to close workspace watcher", "error", err)
		}
	}

	if a.manager != nil {
		a.manager.Shutdown()
	}

	if a.store != nil {
		if err := a.store.Close(); err != nil {
			slog.Error("failed to close session store", "error", err)
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

// CreateSession creates a new agent session.
func (a *App) CreateSession() (*session.SessionInfo, error) {
	if a.manager == nil {
		return nil, errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	info, err := a.manager.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	// Persist to SQLite
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.SaveSession(*info); err != nil {
			slog.Error("failed to save session to store", "error", err)
		}
	}
	return info, nil
}

// DeleteSession removes a session.
func (a *App) DeleteSession(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only delete from manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.DeleteSession(id); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
		}
	}
	// Always delete from store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.DeleteSession(id); err != nil {
			slog.Error("failed to delete session from store", "error", err)
		}
	}
	return nil
}

// ListSessions returns all sessions.
func (a *App) ListSessions() ([]session.SessionInfo, error) {
	// Load from store for persisted sessions
	if a.store != nil {
		sessions, err := a.store.ListSessions()
		if err != nil {
			return nil, err
		}
		if sessions == nil {
			return []session.SessionInfo{}, nil
		}
		return sessions, nil
	}
	if a.manager == nil {
		return []session.SessionInfo{}, nil
	}
	sessions := a.manager.ListSessions()
	if sessions == nil {
		return []session.SessionInfo{}, nil
	}
	return sessions, nil
}

// RenameSession changes session name.
func (a *App) RenameSession(id, name string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only rename in manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.RenameSession(id, name); err != nil {
			return fmt.Errorf("failed to rename session: %w", err)
		}
	}
	// Always rename in store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.RenameSession(id, name); err != nil {
			slog.Error("failed to rename session in store", "error", err)
		}
	}
	return nil
}

// ArchiveSession archives/unarchives a session.
func (a *App) ArchiveSession(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only archive in manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.ArchiveSession(id); err != nil {
			return fmt.Errorf("failed to archive session: %w", err)
		}
	}
	// Toggle archive in store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		info, err := a.store.LoadSession(id)
		if err == nil && info != nil {
			if err := a.store.ArchiveSession(id, !info.Archived); err != nil {
				slog.Error("failed to archive session in store", "error", err)
			}
		}
	}
	return nil
}

// SendMessage sends a user message to a session (async - results come via events).
func (a *App) SendMessage(id, text string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	// Update session activity timestamp
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.UpdateSessionActivity(id); err != nil {
			slog.Error("failed to update session activity", "error", err)
		}
	}
	// Save user message to store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.SaveMessage(session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			slog.Error("failed to save user message to store", "error", err)
		}
	}

	// Check if this is the first message (session has default name)
	// and spawn title generation in background
	if a.store != nil && a.llmRouter != nil {
		if info, err := a.store.LoadSession(id); err == nil && info != nil {
			// Check if name matches default pattern (first 8 chars of UUID)
			if info.Name == "Session "+id[:8] {
				go a.generateSessionTitle(id, text)
			}
		}
	}

	if err := a.manager.SendMessage(a.ctx, id, text); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// generateSessionTitle generates a title for the session based on the first user message.
// This is a best-effort operation that runs asynchronously.
func (a *App) generateSessionTitle(sessionID, userMessage string) {
	// Create a background context (not tied to the request context)
	ctx := context.Background()

	// Try LLM-based title generation
	title := a.generateTitleViaLLM(ctx, userMessage)

	// Fallback: use first few words of the user message
	if title == "" {
		title = fallbackTitle(userMessage)
	}

	if title == "" {
		return
	}

	// Truncate if too long
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	// Rename session (persists to DB and updates in-memory)
	if err := a.RenameSession(sessionID, title); err != nil {
		slog.Warn("failed to rename session with generated title", "session", sessionID, "error", err)
	} else {
		slog.Info("session auto-named", "session", sessionID, "title", title)
	}
}

// generateTitleViaLLM calls the LLM to generate a concise session title.
// TODO: token usage from this call is not tracked in session totals because
// the session emitter is not available here. Wire emitter when possible.
func (a *App) generateTitleViaLLM(ctx context.Context, userMessage string) string {
	temp := 0.3
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Generate a concise title (3-7 words) for a conversation that starts with the following user message. Output ONLY the title text, no quotes, no punctuation at the end."},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   30,
		Temperature: &temp,
	}

	resp, err := a.llmRouter.Call(ctx, req)
	if err != nil {
		slog.Warn("failed to generate session title via LLM, using fallback", "error", err)
		return ""
	}
	if resp == nil {
		slog.Warn("LLM returned nil response for session title, using fallback")
		return ""
	}

	title := strings.TrimSpace(resp.Message.Content)
	title = strings.Trim(title, "\"'")
	return title
}

// fallbackTitle creates a simple title from the first few words of a message.
func fallbackTitle(message string) string {
	words := strings.Fields(message)
	if len(words) == 0 {
		return ""
	}
	maxWords := 5
	if len(words) < maxWords {
		maxWords = len(words)
	}
	title := strings.Join(words[:maxWords], " ")
	if len(words) > maxWords {
		title += "..."
	}
	return title
}

// CancelTask cancels the running task in a session.
func (a *App) CancelTask(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	return a.manager.CancelTask(id)
}

// GetSessionHistory returns chat history for a session.
func (a *App) GetSessionHistory(id string) ([]session.ChatMessage, error) {
	if a.store != nil {
		return a.store.LoadMessages(id)
	}
	return []session.ChatMessage{}, nil
}

// persistConfig saves the current in-memory config to disk.
func (a *App) persistConfig() error {
	if a.configPath == "" || a.config == nil {
		return errors.New("config path or config not set")
	}
	return config.Save(a.config, a.configPath)
}

// FileNode represents a file or directory entry in the workspace tree.
type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded             bool              `json:"loaded"`
	LogLevel           string            `json:"log_level"`
	Theme              string            `json:"theme"`
	ConfigMigrated     bool              `json:"config_migrated"`
	ConfigMigrationMsg string            `json:"config_migration_msg"`
	ConfigErrors       []string          `json:"config_errors"`
	LLM                ConfigLLMResponse `json:"llm"`
	Memory             ConfigMemResponse `json:"memory"`
	Search             ConfigSearchResp  `json:"search"`
}

// ConfigLLMResponse holds sanitised LLM provider info.
type ConfigLLMResponse struct {
	ActiveProvider   string                 `json:"active_provider"`
	Anthropic        ConfigProviderKeyModel `json:"anthropic"`
	Gemini           ConfigProviderKeyModel `json:"gemini"`
	LMStudio         ConfigProviderFull     `json:"lmstudio"`
	OpenAICompatible ConfigProviderFull     `json:"openai_compatible"`
	ChatGPT          ConfigProviderKeyModel `json:"chatgpt"`
}

// ConfigProviderKeyModel is a provider with api_key + model.
type ConfigProviderKeyModel struct {
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

// ConfigProviderFull is a provider with base_url + api_key + model.
type ConfigProviderFull struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// ConfigMemResponse holds memory section of config response.
type ConfigMemResponse struct {
	Database string `json:"database"`
}

// ConfigSearchResp holds search config values.
type ConfigSearchResp struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// SessionTokensResponse holds token usage for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int `json:"total_input_tokens"`
	TotalOutputTokens int `json:"total_output_tokens"`
}

// GetConfig returns the current configuration (sanitized, no raw API keys).
func (a *App) GetConfig() ConfigResponse {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return ConfigResponse{Loaded: false}
	}

	return ConfigResponse{
		Loaded:             true,
		LogLevel:           a.config.LogLevel,
		Theme:              a.config.Theme,
		ConfigMigrated:     a.configMigrated,
		ConfigMigrationMsg: a.configMigrationMsg,
		ConfigErrors:       nonNilStringSlice(a.configLoadErrors),
		LLM: ConfigLLMResponse{
			ActiveProvider: a.config.LLM.ActiveProvider,
			Anthropic: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.Anthropic.APIKey),
				Model:  a.config.LLM.Anthropic.Model,
			},
			Gemini: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.Gemini.APIKey),
				Model:  a.config.LLM.Gemini.Model,
			},
			LMStudio: ConfigProviderFull{
				BaseURL: a.config.LLM.LMStudio.BaseURL,
				APIKey:  maskAPIKey(a.config.LLM.LMStudio.APIKey),
				Model:   a.config.LLM.LMStudio.Model,
			},
			OpenAICompatible: ConfigProviderFull{
				BaseURL: a.config.LLM.OpenAICompatible.BaseURL,
				APIKey:  maskAPIKey(a.config.LLM.OpenAICompatible.APIKey),
				Model:   a.config.LLM.OpenAICompatible.Model,
			},
			ChatGPT: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.ChatGPT.APIKey),
				Model:  a.config.LLM.ChatGPT.Model,
			},
		},
		Memory: ConfigMemResponse{
			Database: a.config.Memory.Database,
		},
		Search: ConfigSearchResp{
			Provider: a.config.Search.Provider,
			APIKey:   maskAPIKey(a.config.Search.APIKey),
		},
	}
}

// maskAPIKey returns a masked representation of an API key for display.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		return key
	}
	return "***configured***"
}

// nonNilStringSlice returns an empty slice if the input is nil,
// ensuring JSON serialization produces [] instead of null.
func nonNilStringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListProviderModels returns available model names for a given provider.
// For Anthropic/Gemini: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible/LM Studio: fetches from the provider's API.
func (a *App) ListProviderModels(provider string) ([]string, error) {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return nil, errors.New("config not initialized")
	}

	switch provider {
	case "anthropic":
		return llm.BuiltInModelNames("anthropic-api"), nil
	case "gemini":
		return llm.BuiltInModelNamesByPrefix("gemini-"), nil
	case "chatgpt":
		apiKey := config.ExpandEnvVars(a.config.LLM.ChatGPT.APIKey)
		if apiKey == "" {
			return nil, errors.New("ChatGPT API key not configured")
		}
		return a.listOpenAIModels("", apiKey)
	case "openai_compatible":
		cfg := a.config.LLM.OpenAICompatible
		baseURL := config.ExpandEnvVars(cfg.BaseURL)
		apiKey := config.ExpandEnvVars(cfg.APIKey)
		if baseURL == "" {
			return nil, errors.New("OpenAI Compatible base URL not configured")
		}
		return a.listOpenAIModels(baseURL, apiKey)
	case "lmstudio":
		cfg := a.config.LLM.LMStudio
		baseURL := config.ExpandEnvVars(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "http://localhost:1234"
		}
		apiKey := config.ExpandEnvVars(cfg.APIKey)
		return a.listLMStudioModels(baseURL, apiKey)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// listOpenAIModels fetches available models from an OpenAI-compatible API.
func (a *App) listOpenAIModels(baseURL, apiKey string) ([]string, error) {
	var client *openai.Client
	if baseURL == "" {
		client = openai.NewClient(apiKey)
	} else {
		cfg := openai.DefaultConfig(apiKey)
		cfg.BaseURL = baseURL
		client = openai.NewClientWithConfig(cfg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	modelList, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	names := []string{}
	for _, m := range modelList.Models {
		names = append(names, m.ID)
	}
	sort.Strings(names)
	return names, nil
}

// listLMStudioModels fetches available models from LM Studio API.
func (a *App) listLMStudioModels(baseURL, apiKey string) ([]string, error) {
	provider, err := llm.NewLMStudioProvider(llm.LMStudioProviderConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create LM Studio provider: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	names := []string{}
	for _, m := range models {
		names = append(names, m.ID)
	}
	sort.Strings(names)
	return names, nil
}

// UpdateSessionTokens persists accumulated token counts for a session.
func (a *App) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int) error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens)
}

// GetSessionTokens returns persisted token counts for a session.
func (a *App) GetSessionTokens(sessionID string) SessionTokensResponse {
	var result SessionTokensResponse
	if a.store == nil || sessionID == "" {
		return result
	}
	info, err := a.store.LoadSession(sessionID)
	if err != nil || info == nil {
		return result
	}
	result.TotalInputTokens = info.TotalInputTokens
	result.TotalOutputTokens = info.TotalOutputTokens
	return result
}

// GetLogLevel returns the current log level.
func (a *App) GetLogLevel() string {
	return a.logLevel
}

// SetLogLevel sets the log level dynamically.
func (a *App) SetLogLevel(level string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	// Validate the level
	level = strings.ToUpper(level)
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		a.logLevel = level
		if a.manager != nil {
			a.manager.SetLogLevel(level)
		}
		a.config.LogLevel = level
		if err := a.persistConfig(); err != nil {
			slog.Warn("failed to persist log level change", "error", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
}

// SetTheme sets the UI theme and persists to config.
func (a *App) SetTheme(theme string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	switch theme {
	case "light", "dark", "system":
		a.config.Theme = theme
		return a.persistConfig()
	default:
		return fmt.Errorf("invalid theme: %s (must be light, dark, or system)", theme)
	}
}

// SecuritySettingsResponse holds security settings for the frontend.
type SecuritySettingsResponse struct {
	DefaultPolicy string                        `json:"default_policy"`
	ToolPolicies  map[string]ToolPolicyResponse `json:"tool_policies"`
}

// ToolPolicyResponse holds per-tool policy for the frontend.
type ToolPolicyResponse struct {
	Policy    string   `json:"policy"`
	Blacklist []string `json:"blacklist,omitempty"`
}

// GetSecuritySettings returns current security settings for the UI.
func (a *App) GetSecuritySettings() SecuritySettingsResponse {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return SecuritySettingsResponse{DefaultPolicy: "auto"}
	}
	resp := SecuritySettingsResponse{
		DefaultPolicy: a.config.Security.DefaultPolicy,
		ToolPolicies:  make(map[string]ToolPolicyResponse),
	}
	for name, cfg := range a.config.Security.ToolPolicies {
		resp.ToolPolicies[name] = ToolPolicyResponse{
			Policy:    cfg.Policy,
			Blacklist: cfg.Blacklist,
		}
	}
	return resp
}

// UpdateSecuritySettings updates security settings at runtime.
func (a *App) UpdateSecuritySettings(settings SecuritySettingsResponse) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	// Update config
	a.config.Security.DefaultPolicy = settings.DefaultPolicy
	if a.config.Security.ToolPolicies == nil {
		a.config.Security.ToolPolicies = make(map[string]config.ToolPolicyConfig)
	}
	for name, policyCfg := range settings.ToolPolicies {
		a.config.Security.ToolPolicies[name] = config.ToolPolicyConfig{
			Policy:    policyCfg.Policy,
			Blacklist: policyCfg.Blacklist,
		}
	}

	// Update registry policy overrides
	if a.toolRegistry != nil {
		policyOverrides := make(map[string]tools.ToolPolicy)
		for toolName, policyCfg := range settings.ToolPolicies {
			policyOverrides[toolName] = tools.ParseToolPolicy(policyCfg.Policy)
		}
		a.toolRegistry.SetPolicyOverrides(policyOverrides)

		if settings.DefaultPolicy != "" {
			a.toolRegistry.SetDefaultPolicy(tools.ParseToolPolicy(settings.DefaultPolicy))
		}
	}

	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist security settings", "error", err)
	}

	return nil
}

// LLMSettingsRequest holds LLM settings from the frontend.
type LLMSettingsRequest struct {
	ActiveProvider string `json:"active_provider"`
	APIKey         string `json:"api_key"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
}

// UpdateLLMSettings updates LLM active provider and model settings.
func (a *App) UpdateLLMSettings(settings LLMSettingsRequest) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	// Update active provider
	if settings.ActiveProvider != "" {
		// Validate the provider is one of the known providers
		validProviders := map[string]bool{
			"anthropic":         true,
			"gemini":            true,
			"lmstudio":          true,
			"openai_compatible": true,
			"chatgpt":           true,
		}
		if !validProviders[settings.ActiveProvider] {
			return fmt.Errorf("active_provider %q is not a valid provider", settings.ActiveProvider)
		}
		a.config.LLM.ActiveProvider = settings.ActiveProvider
	}

	// Update model on the active provider
	if a.config.LLM.ActiveProvider != "" {
		switch a.config.LLM.ActiveProvider {
		case "anthropic":
			if settings.Model != "" {
				a.config.LLM.Anthropic.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != "***configured***" {
				a.config.LLM.Anthropic.APIKey = settings.APIKey
			}
		case "gemini":
			if settings.Model != "" {
				a.config.LLM.Gemini.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != "***configured***" {
				a.config.LLM.Gemini.APIKey = settings.APIKey
			}
		case "lmstudio":
			if settings.Model != "" {
				a.config.LLM.LMStudio.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != "***configured***" {
				a.config.LLM.LMStudio.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				a.config.LLM.LMStudio.BaseURL = settings.BaseURL
			}
		case "openai_compatible":
			if settings.Model != "" {
				a.config.LLM.OpenAICompatible.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != "***configured***" {
				a.config.LLM.OpenAICompatible.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				a.config.LLM.OpenAICompatible.BaseURL = settings.BaseURL
			}
		case "chatgpt":
			if settings.Model != "" {
				a.config.LLM.ChatGPT.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != "***configured***" {
				a.config.LLM.ChatGPT.APIKey = settings.APIKey
			}
		}
	}

	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist LLM settings", "error", err)
	}

	// Clear any config load errors since settings are now valid
	a.configLoadErrors = nil

	return nil
}

// SearchSettingsRequest holds search settings from the frontend.
type SearchSettingsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// UpdateSearchSettings updates search configuration.
func (a *App) UpdateSearchSettings(settings SearchSettingsRequest) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	a.config.Search.Provider = settings.Provider
	// Only update API key if it's not the masked placeholder
	if settings.APIKey != "" && settings.APIKey != "***configured***" {
		a.config.Search.APIKey = settings.APIKey
	}

	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist search settings", "error", err)
	}
	return nil
}
