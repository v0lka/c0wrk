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

	"github.com/user/agent/internal/config"
	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/logger"
	"github.com/user/agent/internal/memory"
	"github.com/user/agent/internal/session"
	"github.com/user/agent/internal/tools"
	toolcore "github.com/user/agent/internal/tools/core"
	"github.com/user/agent/internal/tools/mcp"
	"github.com/user/agent/internal/tools/skills"
)

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
			// Log but continue - some vars may not be settable
			slog.Debug("failed to set env var", "key", key, "error", err)
			continue
		}
		loaded++
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
	configPath string

	llmRouter    *llm.LLMRouter
	toolRegistry *tools.ToolRegistry
	mcpGateway   *mcp.MCPGateway

	memorySystem  *memory.MemorySystem
	localEmbedder *llm.LocalEmbedder
	constitution  *core.Constitution
	warmPool      *skills.WarmPool

	sessionLogger *logger.SessionLogger
	logLevel      string

	// Config loading state for UI warnings
	configMigrated     bool
	configMigrationMsg string
	configLoadErrors   []string

	pendingConfirmations sync.Map
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
				_ = a.sessionLogger.Close()
			}
			a.sessionLogger = newLogger
			log = newLogger.Logger()
		}
	}

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
			// Extract content from data if available
			if m, ok := evt.Data.(map[string]interface{}); ok {
				if c, ok := m["content"].(string); ok {
					content = c
				}
			}
		case "task_complete":
			// Only persist if there's output content
			if m, ok := evt.Data.(map[string]interface{}); ok {
				if output, ok := m["output"].(string); ok && output != "" {
					role = "assistant"
					content = output
				}
			}
		case "thought":
			role = "thought"
			if m, ok := evt.Data.(map[string]interface{}); ok {
				if c, ok := m["content"].(string); ok {
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
		case "escalation":
			role = "escalation"
		case "ac_extracted":
			role = "ac_extracted"
		case "subagent_launch":
			role = "subagent_launch"
		case "subagent_complete":
			role = "subagent_complete"
		default:
			return // Don't persist transient events
		}

		if role == "" {
			return
		}

		// Serialize event data as metadata JSON
		metadata := "{}"
		if evt.Data != nil {
			if b, err := json.Marshal(evt.Data); err == nil {
				metadata = string(b)
			}
		}

		// For non-assistant roles, use metadata as content if content is empty
		if content == "" {
			content = metadata
		}

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

	// Create ModelRegistry from config overrides (before router so providers can register sources)
	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range a.config.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: "approximate", // config overrides don't specify tokenizer
		}
	}
	modelRegistry := llm.NewModelRegistry(overrides)

	// Initialize LLM Router - CRITICAL: must succeed for the app to work
	// Pass registry so LM Studio providers can register their metadata sources
	llmRouter, err := llm.NewLLMRouter(a.config.LLM, modelRegistry)
	if err != nil {
		emitStartupError("failed to initialize LLM router", err)
		// Don't set llmRouter - it will remain nil and orchestrator creation will fail
		// with a descriptive error when the user tries to create a session
	}
	if llmRouter != nil && a.config.LLM.ActiveProvider == "" {
		emitStartupError("no active LLM provider configured - check your config.yaml", errors.New("config has no active_provider defined under llm"))
	}
	a.llmRouter = llmRouter

	// Initialize Tool Registry
	registry := tools.NewToolRegistry()
	a.toolRegistry = registry

	// Register core tools
	var bashBlacklist []string
	if bashCfg, ok := a.config.Security.ToolPolicies["bash_exec"]; ok {
		bashBlacklist = bashCfg.Blacklist
	}
	bashTool := toolcore.NewBashExecTool(bashBlacklist)
	registry.Register(bashTool)

	fileOpsTool := toolcore.NewFileOpsTool()
	registry.Register(fileOpsTool)

	finishTool := core.NewFinishTool()
	registry.Register(finishTool)

	// Web tools: WebFetch with optional LLM summarizer
	var summarizer toolcore.LLMSummarizer
	if llmRouter != nil {
		summarizer = func(ctx context.Context, content string, prompt string) (string, error) {
			req := llm.ChatRequest{
				Messages: []llm.Message{
					{Role: "user", Content: content + "\n\n" + prompt},
				},
			}
			resp, err := llmRouter.Call(ctx, "summarizer", req)
			if err != nil {
				return "", err
			}
			return resp.Message.Content, nil
		}
	}
	webFetchTool := toolcore.NewWebFetchTool(summarizer)
	registry.Register(webFetchTool)

	// WebSearch tool (requires Tavily API key)
	searchAPIKey := config.ExpandEnvVars(a.config.Search.APIKey)
	if searchAPIKey != "" {
		webSearchTool := toolcore.NewWebSearchTool(searchAPIKey)
		registry.Register(webSearchTool)
	}

	// Initialize MCP Gateway (optional)
	if len(a.config.MCP.Servers) > 0 {
		gateway := mcp.NewMCPGateway()
		if err := gateway.Start(context.Background(), a.config.MCP.Servers); err != nil {
			log.Warn("MCP gateway start errors", "error", err)
			// MCP is optional; continue
		}

		if err := gateway.RegisterTools(registry); err != nil {
			log.Warn("MCP tool registration errors", "error", err)
		}

		a.mcpGateway = gateway
	}

	// Initialize MemorySystem (consolidated memory subsystem)
	skillsDir := a.config.Skills.Directory
	if skillsDir == "" {
		skillsDir = filepath.Join(agentDir, "skills")
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		log.Warn("failed to create skills directory", "error", err)
	}

	// Get DB path from config or use default
	memDBPath := a.config.Memory.Database
	if memDBPath == "" {
		memDBPath = filepath.Join(agentDir, "memory.db")
	}

	// Auto-detect ONNX Runtime library if not already set
	// This must happen BEFORE initializing the local embedder
	if os.Getenv("ONNXRUNTIME_LIB_PATH") == "" {
		var libName string
		switch runtime.GOOS {
		case "darwin":
			libName = "libonnxruntime.dylib"
		case "windows":
			libName = "onnxruntime.dll"
		default:
			libName = "libonnxruntime.so"
		}

		// Check 1: Look in .cache directory relative to executable
		if exePath, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
				libPath := filepath.Join(filepath.Dir(resolved), ".cache", libName)
				if _, err := os.Stat(libPath); err == nil {
					if err := os.Setenv("ONNXRUNTIME_LIB_PATH", libPath); err == nil {
						log.Debug("auto-detected ONNX Runtime library", "path", libPath)
					}
				}
			}
		}

		// Check 2: Look in .cache directory relative to working directory
		if os.Getenv("ONNXRUNTIME_LIB_PATH") == "" {
			if wd, err := os.Getwd(); err == nil {
				libPath := filepath.Join(wd, ".cache", libName)
				if _, err := os.Stat(libPath); err == nil {
					if err := os.Setenv("ONNXRUNTIME_LIB_PATH", libPath); err == nil {
						log.Debug("auto-detected ONNX Runtime library", "path", libPath)
					}
				}
			}
		}
	}

	// Initialize local embedder for semantic memory
	var embedder memory.Embedder
	if emb, err := llm.NewLocalEmbedder(); err != nil {
		log.Warn("local embedder unavailable, semantic memory disabled", "error", err)
	} else {
		a.localEmbedder = emb
		embedder = emb
	}

	memSys, err := memory.NewMemorySystem(memory.MemorySystemConfig{
		DBPath:    memDBPath,
		SkillsDir: skillsDir,
		Embedder:  embedder,
	})
	if err != nil {
		log.Warn("failed to initialize memory system", "error", err)
	} else {
		log.Info("memory system initialized", "path", memDBPath)
	}
	a.memorySystem = memSys

	// Register existing skills as tools
	builder := skills.NewDockerBuilder()
	if memSys != nil && memSys.Procedural != nil {
		for _, info := range memSys.Procedural.ListSkills() {
			manifest, err := skills.ParseManifest(filepath.Join(info.Path, "skill.json"))
			if err != nil {
				log.Warn("failed to parse skill manifest", "skill", info.Name, "error", err)
				continue
			}
			skillTool := skills.NewSkillTool(manifest, info.Path, builder)
			registry.Register(skillTool)
		}
	}

	// Initialize WarmPool (optional, for performance)
	idleTimeout := 5 * time.Minute
	if a.config.Skills.Docker.WarmPoolIdleTimeout != "" {
		if parsed, err := time.ParseDuration(a.config.Skills.Docker.WarmPoolIdleTimeout); err == nil {
			idleTimeout = parsed
		}
	}
	poolSize := a.config.Skills.Docker.WarmPoolThreshold
	if poolSize <= 0 {
		poolSize = 10
	}
	warmPool := skills.NewWarmPool(builder, poolSize, idleTimeout)
	warmPool.Start()
	a.warmPool = warmPool

	// Configure per-tool security policies from config
	policyOverrides := make(map[string]tools.ToolPolicy)
	for toolName, policyCfg := range a.config.Security.ToolPolicies {
		switch policyCfg.Policy {
		case "always_allow":
			policyOverrides[toolName] = tools.PolicyAlwaysAllow
		case "always_deny":
			policyOverrides[toolName] = tools.PolicyAlwaysDeny
		case "user_confirm":
			policyOverrides[toolName] = tools.PolicyUserConfirm
		case "auto":
			policyOverrides[toolName] = tools.PolicyAuto
		}
	}
	registry.SetPolicyOverrides(policyOverrides)

	// Set default policy for tools not explicitly configured
	switch a.config.Security.DefaultPolicy {
	case "always_allow":
		registry.SetDefaultPolicy(tools.PolicyAlwaysAllow)
	case "always_deny":
		registry.SetDefaultPolicy(tools.PolicyAlwaysDeny)
	case "user_confirm":
		registry.SetDefaultPolicy(tools.PolicyUserConfirm)
	default:
		registry.SetDefaultPolicy(tools.PolicyAuto)
	}



	// Initialize Constitution
	var constitution *core.Constitution
	constitutionPath := a.config.Memory.Constitution.File
	if constitutionPath == "" {
		constitutionPath = filepath.Join(agentDir, "constitution.json")
	}
	constitution, err = core.NewConstitution(constitutionPath)
	if err != nil {
		log.Warn("failed to initialize constitution", "error", err)
		constitution = nil
	} else {
		constitution.IncrementSession()
		log.Info("constitution loaded", "principles", len(constitution.Principles()), "path", constitutionPath, "session", constitution.SessionCount())
	}
	a.constitution = constitution

	// Create SkillCreatorTool with DockerBuilder adapter
	var skillBuilder toolcore.SkillBuilder = &dockerBuilderAdapter{builder: builder}
	skillCreator := toolcore.NewSkillCreatorTool(skillsDir, registry, skillBuilder)
	registry.Register(skillCreator)

	// Create ContextManagerTool with memory adapters
	var semanticStore toolcore.SemanticStore
	if memSys != nil && memSys.Semantic != nil {
		semanticStore = &semanticStoreAdapter{mem: memSys.Semantic}
	}
	var episodicStoreTool toolcore.EpisodicStore
	if memSys != nil && memSys.Episodic != nil {
		episodicStoreTool = &episodicToolAdapter{em: memSys.Episodic}
	}
	var reflexionStoreTool toolcore.ReflexionStore
	if memSys != nil && memSys.Reflexion != nil {
		reflexionStoreTool = &reflexionToolAdapter{rm: memSys.Reflexion}
	}
	// CompactionSwitcher is nil for now (would need to wire into context factory)
	contextMgrTool := toolcore.NewContextManagerTool(semanticStore, nil, episodicStoreTool, reflexionStoreTool)
	registry.Register(contextMgrTool)

	// Create Phase 2 components (only if LLM router is available)
	var router *core.Router
	var acExtractor *core.ACExtractor
	var planner *core.Planner
	var evaluator *core.Evaluator
	var reflector *core.Reflector
	if llmRouter != nil {
		// Router: classifies requests and determines execution strategy
		router = core.NewRouter(llmRouter, a.config.Router.HistoryWindow)

		// ACExtractor: extracts acceptance criteria from user requests
		acExtractor = core.NewACExtractor(llmRouter)

		// Planner: generates DAG execution plans for complex tasks
		planner = core.NewPlanner(llmRouter)

		// Evaluator: checks results against acceptance criteria
		evaluator = core.NewEvaluator(registry, llmRouter)

		// Reflector: for retry-loop
		reflector = core.NewReflector(llmRouter)
	}

	// Capture constitution principles for injection into context windows
	var constitutionPrinciples []string
	if constitution != nil {
		for _, p := range constitution.Principles() {
			constitutionPrinciples = append(constitutionPrinciples, p.Principle)
		}
	}
	contextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) core.ContextManager {
		// Create model-specific token counter
		counter := llm.NewTokenCounter(modelMeta.TokenizerType)
		tracker := llm.NewContextTokenTracker(counter)

		// Create domain-aware compaction strategy using the factory
		strategy := memory.NewCompactionStrategy(compactionStrategy, memory.CompactionConfig{
			SlidingWindow: struct{ KeepFirst, KeepLast int }{
				KeepFirst: a.config.Executor.Compaction.SlidingWindow.KeepFirst,
				KeepLast:  a.config.Executor.Compaction.SlidingWindow.KeepLast,
			},
			Summarization: struct{ BlockSize, KeepLast int }{
				BlockSize: a.config.Executor.Compaction.Summarization.BlockSize,
				KeepLast:  5, // default
			},
			Hierarchical: struct{ DistantRatio, MiddleRatio, RecentRatio float64 }{
				DistantRatio: 0.4,
				MiddleRatio:  0.3,
				RecentRatio:  0.3,
			},
		}, memory.CompactionDeps{
			TokenCounter: counter,
		})

		thresholds := a.config.Executor.Compaction.Thresholds

		cw := memory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy)
		if len(constitutionPrinciples) > 0 {
			cw.SetConstitution(constitutionPrinciples)
		}
		return cw
	}

	// Orchestrator configuration
	orchConfig := core.OrchestratorConfig{
		MaxSteps:   a.config.Executor.MaxReactSteps,
		LLMRole:    "executor",
		KeepFirst:  a.config.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:   a.config.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries: a.config.Executor.MaxRetries,
	}

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
		payload := map[string]interface{}{
			"confirm_id": requestID,
			"tool":       req.ToolName,
			"args":       string(req.Input),
			"reasoning":  req.JudgeReasoning,
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

	// Create orchestrator factory
	factory := func(emitter core.Emitter, logger *slog.Logger) (*core.Orchestrator, error) {
		if llmRouter == nil || router == nil || acExtractor == nil || planner == nil || evaluator == nil {
			return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, AC extractor, planner, or evaluator is nil")
		}
		return core.NewOrchestrator(
			router,      // Router
			acExtractor, // ACExtractor
			planner,     // Planner
			evaluator,   // Evaluator
			llmRouter,   // LLMCaller
			registry,    // ToolExecutor
			registry,    // ToolRegistry
			llm.NewSimpleTokenCounter(), // TokenCounter (for backward compatibility)
			orchConfig,
			contextFactory,
			reflector,         // Reflector for retry-loop
			logger,            // Logger
			emitter,           // Emitter
			modelRegistry,     // ModelRegistry for resolving model metadata
		), nil
	}

	logDir := filepath.Join(agentDir, "logs")
	workspacesDir := filepath.Join(agentDir, "workspaces")
	a.manager = session.NewManager(factory, emitFunc, logDir, workspacesDir)

	// Listen for confirmation responses from frontend
	wailsRuntime.EventsOn(a.ctx, "tool_confirm_response", func(data ...interface{}) {
		if len(data) == 0 {
			log.Warn("tool confirmation response missing payload")
			return
		}

		payload, ok := data[0].(map[string]interface{})
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
}

// shutdown is called when the Wails app is closing.
func (a *App) shutdown(ctx context.Context) {
	if a.manager != nil {
		a.manager.Shutdown()
	}

	if a.store != nil {
		if err := a.store.Close(); err != nil {
			slog.Error("failed to close session store", "error", err)
		}
	}

	if a.warmPool != nil {
		a.warmPool.Stop()
	}

	if a.memorySystem != nil {
		if err := a.memorySystem.Close(); err != nil {
			slog.Error("failed to close memory system", "error", err)
		}
	}

	if a.localEmbedder != nil {
		if err := a.localEmbedder.Close(); err != nil {
			slog.Error("failed to close local embedder", "error", err)
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
		return a.store.ListSessions()
	}
	if a.manager == nil {
		return nil, errors.New("session manager not initialized")
	}
	return a.manager.ListSessions(), nil
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
	if a.store != nil {
		if err := a.store.UpdateSessionActivity(id); err != nil {
			slog.Error("failed to update session activity", "error", err)
		}
	}
	// Save user message to store
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

	resp, err := a.llmRouter.Call(ctx, "assistant", req)
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
	return nil, nil
}

// persistConfig saves the current in-memory config to disk.
func (a *App) persistConfig() error {
	if a.configPath == "" || a.config == nil {
		return errors.New("config path or config not set")
	}
	return config.Save(a.config, a.configPath)
}

// GetConfig returns the current configuration (sanitized, no raw API keys).
func (a *App) GetConfig() map[string]interface{} {
	if a.config == nil {
		return map[string]interface{}{"loaded": false}
	}

	// Search - mask API key
	searchKeyMasked := maskAPIKey(a.config.Search.APIKey)

	return map[string]interface{}{
		"loaded":              true,
		"log_level":           a.config.LogLevel,
		"theme":               a.config.Theme,
		"config_migrated":     a.configMigrated,
		"config_migration_msg": a.configMigrationMsg,
		"config_errors":       a.configLoadErrors,
		"llm": map[string]interface{}{
			"active_provider": a.config.LLM.ActiveProvider,
			"anthropic": map[string]interface{}{
				"api_key": maskAPIKey(a.config.LLM.Anthropic.APIKey),
				"model":   a.config.LLM.Anthropic.Model,
			},
			"gemini": map[string]interface{}{
				"api_key": maskAPIKey(a.config.LLM.Gemini.APIKey),
				"model":   a.config.LLM.Gemini.Model,
			},
			"lmstudio": map[string]interface{}{
				"base_url": a.config.LLM.LMStudio.BaseURL,
				"api_key":  maskAPIKey(a.config.LLM.LMStudio.APIKey),
				"model":    a.config.LLM.LMStudio.Model,
			},
			"openai_compatible": map[string]interface{}{
				"base_url": a.config.LLM.OpenAICompatible.BaseURL,
				"api_key":  maskAPIKey(a.config.LLM.OpenAICompatible.APIKey),
				"model":    a.config.LLM.OpenAICompatible.Model,
			},
			"chatgpt": map[string]interface{}{
				"api_key": maskAPIKey(a.config.LLM.ChatGPT.APIKey),
				"model":   a.config.LLM.ChatGPT.Model,
			},
		},
		"memory": map[string]interface{}{
			"episodic": map[string]interface{}{
				"retention_days":  a.config.Memory.Episodic.RetentionDays,
				"retrieval_limit": a.config.Memory.Episodic.RetrievalLimit,
			},
			"semantic": map[string]interface{}{},
		},
		"search": map[string]interface{}{
			"provider": a.config.Search.Provider,
			"api_key":  searchKeyMasked,
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

// ListProviderModels returns available model names for a given provider.
// For Anthropic/Gemini: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible/LM Studio: fetches from the provider's API.
func (a *App) ListProviderModels(provider string) ([]string, error) {
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

// GetMemoryStats returns current memory system statistics.
func (a *App) GetMemoryStats() map[string]interface{} {
	stats := map[string]interface{}{
		"episodic":   0,
		"semantic":   0,
		"procedural": 0,
		"reflexion":  0,
	}

	if a.memorySystem != nil {
		if a.memorySystem.Episodic != nil {
			if count, err := a.memorySystem.Episodic.Count(context.Background()); err == nil {
				stats["episodic"] = count
			}
		}
		if a.memorySystem.Semantic != nil {
			if count, err := a.memorySystem.Semantic.Count(context.Background()); err == nil {
				stats["semantic"] = count
			}
		}
		if a.memorySystem.Procedural != nil {
			stats["procedural"] = len(a.memorySystem.Procedural.ListSkills())
		}
		if a.memorySystem.Reflexion != nil {
			if count, err := a.memorySystem.Reflexion.Count(context.Background()); err == nil {
				stats["reflexion"] = count
			}
		}
	}

	return stats
}

// GetSessionMemoryStats returns memory statistics scoped to a specific session.
func (a *App) GetSessionMemoryStats(sessionID string) map[string]interface{} {
	stats := map[string]interface{}{
		"episodic": 0,
	}

	if a.memorySystem != nil && a.memorySystem.Episodic != nil && sessionID != "" {
		if count, err := a.memorySystem.Episodic.CountBySession(context.Background(), sessionID); err == nil {
			stats["episodic"] = count
		}
	}

	return stats
}

// UpdateSessionTokens persists accumulated token counts for a session.
func (a *App) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int) error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens)
}

// GetSessionTokens returns persisted token counts for a session.
func (a *App) GetSessionTokens(sessionID string) map[string]interface{} {
	result := map[string]interface{}{
		"total_input_tokens":  0,
		"total_output_tokens": 0,
	}
	if a.store == nil || sessionID == "" {
		return result
	}
	info, err := a.store.LoadSession(sessionID)
	if err != nil || info == nil {
		return result
	}
	result["total_input_tokens"] = info.TotalInputTokens
	result["total_output_tokens"] = info.TotalOutputTokens
	return result
}

// GetLogLevel returns the current log level.
func (a *App) GetLogLevel() string {
	return a.logLevel
}

// SetLogLevel sets the log level dynamically.
func (a *App) SetLogLevel(level string) error {
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
	DefaultPolicy string                       `json:"default_policy"`
	ToolPolicies  map[string]ToolPolicyResponse `json:"tool_policies"`
}

// ToolPolicyResponse holds per-tool policy for the frontend.
type ToolPolicyResponse struct {
	Policy    string   `json:"policy"`
	Blacklist []string `json:"blacklist,omitempty"`
}

// GetSecuritySettings returns current security settings for the UI.
func (a *App) GetSecuritySettings() SecuritySettingsResponse {
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
			switch policyCfg.Policy {
			case "always_allow":
				policyOverrides[toolName] = tools.PolicyAlwaysAllow
			case "always_deny":
				policyOverrides[toolName] = tools.PolicyAlwaysDeny
			case "user_confirm":
				policyOverrides[toolName] = tools.PolicyUserConfirm
			case "auto":
				policyOverrides[toolName] = tools.PolicyAuto
			}
		}
		a.toolRegistry.SetPolicyOverrides(policyOverrides)

		switch settings.DefaultPolicy {
		case "always_allow":
			a.toolRegistry.SetDefaultPolicy(tools.PolicyAlwaysAllow)
		case "always_deny":
			a.toolRegistry.SetDefaultPolicy(tools.PolicyAlwaysDeny)
		case "user_confirm":
			a.toolRegistry.SetDefaultPolicy(tools.PolicyUserConfirm)
		default:
			a.toolRegistry.SetDefaultPolicy(tools.PolicyAuto)
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

// EpisodicSettingsRequest holds episodic memory settings.
type EpisodicSettingsRequest struct {
	RetentionDays  int `json:"retention_days"`
	RetrievalLimit int `json:"retrieval_limit"`
}

// MemorySettingsRequest holds memory settings from the frontend.
type MemorySettingsRequest struct {
	Episodic EpisodicSettingsRequest `json:"episodic"`
}

// UpdateMemorySettings updates memory configuration settings.
func (a *App) UpdateMemorySettings(settings MemorySettingsRequest) error {
	if a.config == nil {
		return errors.New("config not initialized")
	}

	if settings.Episodic.RetentionDays > 0 {
		a.config.Memory.Episodic.RetentionDays = settings.Episodic.RetentionDays
	}
	if settings.Episodic.RetrievalLimit > 0 {
		a.config.Memory.Episodic.RetrievalLimit = settings.Episodic.RetrievalLimit
	}

	if err := a.persistConfig(); err != nil {
		slog.Warn("failed to persist memory settings", "error", err)
	}
	return nil
}

// SearchSettingsRequest holds search settings from the frontend.
type SearchSettingsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// UpdateSearchSettings updates search configuration.
func (a *App) UpdateSearchSettings(settings SearchSettingsRequest) error {
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

// dockerBuilderAdapter adapts skills.DockerBuilder to toolcore.SkillBuilder interface.
type dockerBuilderAdapter struct {
	builder *skills.DockerBuilder
}

func (a *dockerBuilderAdapter) Build(ctx context.Context, skillDir, name, version string) (string, error) {
	manifest := &skills.SkillManifest{
		Name:       name,
		Version:    version,
		EntryPoint: "main.py",
		Language:   "python",
	}
	return a.builder.Build(ctx, skillDir, manifest)
}

// semanticStoreAdapter adapts memory.SemanticMemory to toolcore.SemanticStore interface.
type semanticStoreAdapter struct {
	mem *memory.SemanticMemory
}

func (a *semanticStoreAdapter) Store(ctx context.Context, key, content string, metadata map[string]string) error {
	return a.mem.Store(ctx, key, content, metadata)
}

func (a *semanticStoreAdapter) Search(ctx context.Context, query string, topK int) ([]toolcore.SemanticSearchResult, error) {
	results, err := a.mem.Search(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	adapted := make([]toolcore.SemanticSearchResult, len(results))
	for i, r := range results {
		adapted[i] = toolcore.SemanticSearchResult{
			Key:     r.Key,
			Content: r.Content,
			Score:   r.Score,
		}
	}
	return adapted, nil
}

// episodicToolAdapter adapts memory.EpisodicMemory to toolcore.EpisodicStore interface.
type episodicToolAdapter struct {
	em *memory.EpisodicMemory
}

func (a *episodicToolAdapter) StoreEntry(ctx context.Context, entry toolcore.EpisodicEntry) error {
	memEntry := memory.EpisodicEntry{
		SessionID:   entry.SessionID,
		UserMessage: entry.UserMessage,
		Summary:     entry.Summary,
		Mode:        entry.Mode,
		ToolsUsed:   entry.ToolsUsed,
		Success:     entry.Success,
		Timestamp:   entry.Timestamp,
	}
	return a.em.StoreEntry(ctx, memEntry)
}

func (a *episodicToolAdapter) RetrieveEntries(ctx context.Context, sessionID string, limit int) ([]toolcore.EpisodicEntry, error) {
	entries, err := a.em.RetrieveEntries(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	adapted := make([]toolcore.EpisodicEntry, len(entries))
	for i, e := range entries {
		adapted[i] = toolcore.EpisodicEntry{
			SessionID:   e.SessionID,
			UserMessage: e.UserMessage,
			Summary:     e.Summary,
			Mode:        e.Mode,
			ToolsUsed:   e.ToolsUsed,
			Success:     e.Success,
			Timestamp:   e.Timestamp,
		}
	}
	return adapted, nil
}

// reflexionToolAdapter adapts memory.ReflexionMemory to toolcore.ReflexionStore interface.
type reflexionToolAdapter struct {
	rm *memory.ReflexionMemory
}

func (a *reflexionToolAdapter) Store(ctx context.Context, reflection toolcore.StoredReflexion) error {
	memReflection := memory.StoredReflexion{
		TaskDescription: reflection.TaskDescription,
		Summary:         reflection.Summary,
		Hypotheses:      reflection.Hypotheses,
		SuggestedAction: reflection.SuggestedAction,
		Timestamp:       reflection.Timestamp,
	}
	return a.rm.Store(ctx, memReflection)
}

func (a *reflexionToolAdapter) Search(ctx context.Context, query string, limit int) ([]toolcore.StoredReflexion, error) {
	results, err := a.rm.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	adapted := make([]toolcore.StoredReflexion, len(results))
	for i, r := range results {
		adapted[i] = toolcore.StoredReflexion{
			TaskDescription: r.TaskDescription,
			Summary:         r.Summary,
			Hypotheses:      r.Hypotheses,
			SuggestedAction: r.SuggestedAction,
			Timestamp:       r.Timestamp,
		}
	}
	return adapted, nil
}
