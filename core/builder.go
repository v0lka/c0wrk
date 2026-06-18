package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/sdk/skills"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/tools/mcp"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/planner"
	sdkmemory "github.com/v0lka/c0wrk/sdk/memory"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/prompt"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// OrchestratorBuilder owns the shared tool registry, MCP gateway, and cached
// LLM router. It provides Build() to create per-session Orchestrators and
// exposes methods for runtime reconfiguration (judge, router, MCP, security).
//
// OrchestratorBuilder lives in core so that all SDK imports are confined to
// the core layer. The backend.Application wraps it without importing the SDK.
type OrchestratorBuilder struct {
	mu               sync.RWMutex
	registry         *tools.ToolRegistry
	gateway          *mcp.Gateway
	llmRouter        *llm.Router
	modelRegistry    *llm.ModelRegistry
	logger           *slog.Logger
	vectorSearchFunc builtins.VectorSearchFunc
	baseSkillDirs    []string     // resolved skill directories shared across sessions (highest priority first)
	proxyClient      *http.Client // proxy-configured HTTP client (nil = direct connection)

	// Cached reasoning effort string. Always empty at builder level;
	// per-request overrides flow through HandleOptions.ReasoningEffort
	// → Orchestrator.SetReasoningEffort, which propagates to router,
	// planner, reflector, and the SDK P&E engine.
	reasoningEffort string

	// Async initialization: MCP gateway and LLM router are initialized in the
	// background so that NewOrchestratorBuilder returns immediately.
	initDone   chan struct{}
	initErr    error
	gatewayErr error // non-nil if MCP gateway startup failed
}

func (b *OrchestratorBuilder) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// NewOrchestratorBuilder creates the shared infrastructure: tool registry,
// built-in tools, MCP gateway, LLM router, and tool judge.
// The cfg is used for initial setup; runtime changes are applied via the
// Rebuild* / Reconfigure* methods.
//
// MCP gateway and LLM router initialization happens asynchronously so that
// this function returns immediately. Callers that need those components
// (Build, GenerateTitle, etc.) block until the background init finishes.
func NewOrchestratorBuilder(cfg *BuilderConfig, askUserFunc sdktools.AskUserFunc, logger *slog.Logger) (*OrchestratorBuilder, error) {
	// Defensive default: if the caller did not provide an env-var expander,
	// fall back to a no-op so that downstream callers (proxy/MCP/LLM config)
	// don't panic on a nil function pointer. The real expander is supplied by
	// the backend layer via configadapter.
	if cfg.ExpandEnvVars == nil {
		cfg.ExpandEnvVars = func(s string) string { return s }
	}

	b := &OrchestratorBuilder{
		logger:   logger,
		initDone: make(chan struct{}),
	}

	// 0. Build proxy client (fast — no network, just config parsing)
	if cfg.Proxy.Enabled {
		proxyClient, err := BuildProxyClient(cfg.Proxy, 30*time.Second, logger)
		if err != nil {
			logger.Warn("failed to build proxy client, proceeding without proxy", "error", err)
		} else {
			b.proxyClient = proxyClient
			// Opt-in global env mutation (W-12). SetGlobalEnv defaults to false;
			// only set when the user explicitly opts in via config.
			if cfg.Proxy.SetGlobalEnv {
				SetProxyEnvVars(cfg.Proxy)
			}
		}
	}

	// 1. Tool registry + built-in tools (fast — synchronous)
	b.registry = tools.NewToolRegistry()

	toolsCfg := configToBuiltinToolsConfig(cfg)
	toolsCfg.AskUserFunc = askUserFunc
	toolsCfg.HTTPClient = b.proxyClient
	if err := tools.RegisterBuiltinTools(b.registry, toolsCfg); err != nil {
		return nil, fmt.Errorf("registering built-in tools: %w", err)
	}

	// 1a. read_skill_resource tool is registered once with a context-aware
	// resolver that looks up skills activated on the current request (see
	// ActiveSkills in systemprompt.go). Per-session SkillManager instances are
	// created lazily in Build so each session can include its project-local
	// `.agents/skills` directory without racing with other sessions.
	b.registry.Register(skills.NewReadSkillResourceTool(activeSkillPathResolver))

	// 2. Security policies (fast — synchronous)
	b.applySecurityPolicies(cfg)

	// 3. Start slow initialization (MCP gateway + LLM router) asynchronously
	go b.runAsyncInit(cfg)

	return b, nil
}

// runAsyncInit performs the slow network-dependent initialization:
// MCP gateway startup and LLM router creation.
func (b *OrchestratorBuilder) runAsyncInit(cfg *BuilderConfig) {
	defer close(b.initDone)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// MCP Gateway (optional — failures are non-fatal)
	mcpCfg := configToGatewayConfig(cfg)
	mcpCfg.HTTPClient = b.proxyClient
	gw, err := mcp.StartGateway(ctx, mcpCfg, b.registry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		// MCP gateway failure is non-fatal: tools from MCP servers will be unavailable
		// but the orchestrator can still operate with built-in tools.
		b.log().Warn("MCP gateway startup failed", "error", err)
	}
	b.mu.Lock()
	b.gateway = gw
	b.gatewayErr = err
	b.mu.Unlock()

	// Wire schema sanitizer on successful gateway startup (S-17).
	if gw != nil {
		gw.SetSchemaSanitizer(defaultSchemaSanitizer())
	}

	// LLM Router
	llmRouter, modelReg, err := b.buildRouter(ctx, cfg)
	if err != nil {
		b.log().Warn("failed to initialize LLM router at startup", "error", err)
		b.initErr = err
	} else {
		b.mu.Lock()
		b.llmRouter = llmRouter
		b.modelRegistry = modelReg
		b.mu.Unlock()
	}

	// Tool judge
	b.rebuildJudgeInternal(cfg, b.llmRouter)
}

// waitReady blocks until async initialization completes or the context is cancelled.
func (b *OrchestratorBuilder) waitReady(ctx context.Context) error {
	select {
	case <-b.initDone:
		return b.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady blocks until async initialization completes or the context is cancelled.
// Exported for use by the backend package.
//
// IMPORTANT: A nil return only guarantees the LLM router initialized successfully.
// The MCP gateway may have failed independently — check MCPGatewayError() if MCP
// tools are required. Gateway failures are intentionally non-fatal so the
// orchestrator can still operate with built-in tools when MCP servers are unavailable.
func (b *OrchestratorBuilder) WaitReady(ctx context.Context) error {
	return b.waitReady(ctx)
}

// ToolRegistry returns the shared tool registry. The registry is set once during
// NewOrchestratorBuilder and never reassigned, so no lock is needed.
func (b *OrchestratorBuilder) ToolRegistry() *tools.ToolRegistry {
	return b.registry
}

// MCPGateway returns the MCP gateway, or nil if not started.
// Waits up to 30 seconds for async initialization to complete.
func (b *OrchestratorBuilder) MCPGateway() *mcp.Gateway {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.waitReady(waitCtx)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.gateway
}

// MCPGatewayError returns the error from MCP gateway startup, if any.
// Returns "" if the gateway started successfully or hasn't been initialized yet.
func (b *OrchestratorBuilder) MCPGatewayError() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.gatewayErr != nil {
		return b.gatewayErr.Error()
	}
	return ""
}

// ModelRegistry returns the shared model registry.
func (b *OrchestratorBuilder) ModelRegistry() *llm.ModelRegistry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.modelRegistry
}

// Build creates a new per-session Orchestrator from the current config.
// Each session gets a fresh LLM router, core agents, and context factory.
// The shared tool registry and MCP gateway are reused across sessions.
//
// workspacePath, when non-empty, prepends `<workspacePath>/.agents/skills` to
// the skill discovery dirs for this session (highest priority) so that
// project-local skills override user-wide ones.
func (b *OrchestratorBuilder) Build(
	cfg *BuilderConfig,
	emitter Emitter,
	logger *slog.Logger,
	workspacePath string,
	bbFactory BlackboardFactory,
	stepLimitFunc agent.StepLimitFunc,
	dumpWriter io.Writer,
) (*Orchestrator, error) {
	// Wait for async initialization to complete before building an orchestrator.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := b.waitReady(waitCtx); err != nil {
		return nil, fmt.Errorf("orchestrator builder not ready: %w", err)
	}

	// Wrap emitter with logging
	emitter = NewLoggingEmitter(emitter, logger)

	// Build per-session LLM router + model registry
	llmRouter, modelReg, err := b.buildRouter(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM router: %w", err)
	}
	// Verify at least one provider has models enabled.
	hasModels := false
	for _, pc := range cfg.LLM.ProviderConfigs {
		if len(pc.Models) > 0 {
			hasModels = true
			break
		}
	}
	if !hasModels {
		return nil, errors.New("no active LLM provider configured - check your config.yaml")
	}

	// Create session-level UsageTracker and TrackingCaller
	usageTracker := llm.NewUsageTracker()
	trackingCaller := llm.NewTrackingCaller(llmRouter, usageTracker)

	// Register emitter as observer for session token events and persistence
	if te, ok := emitter.(interface {
		EmitSessionTokens(totalIn, totalOut int, model, family string)
	}); ok {
		usageTracker.AddObserver(func(_ llm.TokenUsage, totalIn, totalOut int, model, family string) {
			te.EmitSessionTokens(totalIn, totalOut, model, family)
		})
	}

	// Build context factory
	contextFactory := b.buildContextFactory(trackingCaller, cfg, modelReg, dumpWriter)

	// Build core agents (router, planner, reflector) with tracking caller
	tokenCounter := llm.NewSimpleTokenCounter()
	coreRouter, corePlanner, coreReflector := b.buildCoreAgents(trackingCaller, cfg, emitter, logger, modelReg, contextFactory, tokenCounter, dumpWriter)
	if coreRouter == nil || corePlanner == nil {
		return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, or corePlanner is nil")
	}

	// Resolve reasoning effort for step executors
	reasoningEffort := b.reasoningEffort

	// Build orchestrator config
	orchConfig := OrchestratorConfig{
		MaxSteps:                  cfg.Executor.MaxReactSteps,
		KeepFirst:                 cfg.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:                  cfg.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries:                cfg.Executor.MaxRetries,
		MaxHistoryMessages:        cfg.Orchestration.MaxHistoryMessages,
		MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
		Model:                     cfg.LLM.DefaultModel,
		ReasoningEffort:           reasoningEffort,
		StepLimitFunc:             stepLimitFunc,
		PreWarningPercent:         cfg.Executor.Compaction.Thresholds.PreWarningPercent,
		InjectionDefenseEnabled:   cfg.Security.InjectionDefenseEnabled,
		AgentsMDMaxBytes:          cfg.Security.AgentsMDMaxBytes,
	}

	// Create tool result cache (per-session lifetime).
	cacheTTL := time.Duration(cfg.Executor.ToolResultBudget.CacheTTLSeconds) * time.Second
	toolCache := agent.NewToolResultCache(cacheTTL)

	// Convert per-tool truncation config from builder to agent types.
	perToolTruncation := make(map[string]agent.ToolTruncationConfig, len(cfg.ToolLimits.PerToolTruncation))
	for name, tc := range cfg.ToolLimits.PerToolTruncation {
		perToolTruncation[name] = agent.ToolTruncationConfig{
			MaxLines: tc.MaxLines,
			MaxBytes: tc.MaxBytes,
		}
	}

	// Token counter, budgets, circuit breaker
	toolResultBudget := agent.ToolResultBudget{
		HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
		MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
	}
	circuitBreaker := agent.CircuitBreakerConfig{
		RepeatNudgeThreshold:         cfg.Executor.CircuitBreaker.RepeatNudgeThreshold,
		RepeatAbortThreshold:         cfg.Executor.CircuitBreaker.RepeatAbortThreshold,
		TruncationAbortThreshold:     cfg.Executor.CircuitBreaker.TruncationAbortThreshold,
		ParseErrorAbortThreshold:     cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold,
		FruitlessNudgeThreshold:      cfg.Executor.CircuitBreaker.FruitlessNudgeThreshold,
		FruitlessAbortThreshold:      cfg.Executor.CircuitBreaker.FruitlessAbortThreshold,
		FruitlessMaxResultLen:        cfg.Executor.CircuitBreaker.FruitlessMaxResultLen,
		SameToolRepeatNudgeThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatNudgeThreshold,
		SameToolRepeatAbortThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatAbortThreshold,
		SameToolResultSizeDelta:      cfg.Executor.CircuitBreaker.SameToolResultSizeDelta,
	}

	// Logged LLM caller for step execution (wraps trackingCaller)
	loggedLLM := agent.NewLoggingLLMCaller(trackingCaller, cfg.LLM.DefaultProviderName(), logger)
	loggedLLM = agent.NewDumpCaller(loggedLLM, dumpWriter, logger)

	// Per-session SkillManager: project-local `.agents/skills` is always prepended
	// (highest priority) to the shared base dirs. This must be built per-session
	// because the workspace path differs between concurrent sessions.
	sessionSkillMgr := b.buildSessionSkillManager(workspacePath, logger)

	// Per-session ToolRegistry clone: skill-derived policy overrides set during
	// HandleMessage must NOT leak to other concurrent sessions. The clone shares
	// the underlying SDK ToolRegistry (tools themselves are stateless), but each
	// session has its own policyOverrides/skillPolicyOverrides view.
	sessionRegistry := b.registry.Clone()

	return NewOrchestrator(orchConfig, OrchestratorDeps{
		Router:            coreRouter,
		Planner:           corePlanner,
		LLM:               loggedLLM,
		ModelSwitcher:     llmRouter,
		ToolExec:          sessionRegistry,         // ToolExecutor (per-session policy view)
		ToolRegistry:      b.registry.ToolRegistry, // SDK ToolRegistry (shared)
		TokenCounter:      tokenCounter,
		ContextFactory:    contextFactory,
		Reflector:         coreReflector,
		Logger:            logger,
		Emitter:           emitter,
		ModelRegistry:     modelReg,
		ToolResultBudget:  toolResultBudget,
		CircuitBreaker:    circuitBreaker,
		BBFactory:         bbFactory,
		TrackingCaller:    trackingCaller,
		VectorSearchFunc:  b.vectorSearchFunc,
		SkillManager:      sessionSkillMgr,
		CoreToolRegistry:  sessionRegistry, // for skill policy overrides (per-session)
		ToolCache:         toolCache,
		PerToolTruncation: perToolTruncation,
	}), nil
}

// RebuildRouter creates a new LLM router from the given config and caches it.
// This is called when LLM settings change at runtime.
func (b *OrchestratorBuilder) RebuildRouter(cfg *BuilderConfig) error {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.waitReady(waitCtx); err != nil {
		return err
	}
	llmRouter, modelReg, err := b.buildRouter(context.Background(), cfg)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.llmRouter = llmRouter
	b.modelRegistry = modelReg
	b.mu.Unlock()
	return nil
}

// RebuildJudge recreates the tool judge from the given config.
// If router is nil, the cached router is used.
func (b *OrchestratorBuilder) RebuildJudge(cfg *BuilderConfig) {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.waitReady(waitCtx); err != nil {
		b.log().Warn("rebuildJudge: builder not ready", "error", err)
		return
	}
	b.mu.RLock()
	llmRouter := b.llmRouter
	b.mu.RUnlock()
	b.rebuildJudgeInternal(cfg, llmRouter)
}

// ReconfigureMCP, StopGateway, SetMCPWorkDir, and configToGatewayConfig live
// in builder_mcp.go to keep MCP gateway lifecycle code adjacent to its
// configuration helpers (W-16 file split).

// UpdateSecurityPolicies applies security policy overrides from config.
func (b *OrchestratorBuilder) UpdateSecurityPolicies(cfg *BuilderConfig) {
	b.applySecurityPolicies(cfg)
}

// UpdateSearchTool replaces or removes the web_search tool in the registry.
func (b *OrchestratorBuilder) UpdateSearchTool(cfg *BuilderConfig) {
	apiKey := cfg.ExpandEnvVars(cfg.Search.APIKey)
	limits := builtins.WebSearchLimits{
		MaxResults: cfg.ToolLimits.WebSearchMaxResults,
		Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
	b.mu.RLock()
	pc := b.proxyClient
	b.mu.RUnlock()
	tools.UpdateSearchToolWithClient(b.registry, cfg.Search.Provider, apiKey, limits, pc)
}

// RebuildProxy rebuilds the proxy HTTP client from the given config and propagates
// the new transport to all subsystems: web tools, MCP gateway, LLM router, and judge.
func (b *OrchestratorBuilder) RebuildProxy(ctx context.Context, cfg *BuilderConfig) error {
	if err := b.waitReady(ctx); err != nil {
		return err
	}

	if !cfg.Proxy.Enabled {
		b.mu.Lock()
		b.proxyClient = nil
		b.mu.Unlock()
		// Always clear global env on disable so a previously-set HTTP_PROXY does
		// not linger after the user turns the proxy off, regardless of
		// SetGlobalEnv (the user has explicitly switched proxy off).
		ClearProxyEnvVars()
	} else {
		proxyClient, err := BuildProxyClient(cfg.Proxy, 30*time.Second, b.logger)
		if err != nil {
			return fmt.Errorf("building proxy client: %w", err)
		}
		b.mu.Lock()
		b.proxyClient = proxyClient
		b.mu.Unlock()
		// Opt-in global env mutation (W-12). SetGlobalEnv defaults to false;
		// only set when the user explicitly opts in via config. Never clear
		// when proxy is enabled — the user may have set proxy env vars externally.
		if cfg.Proxy.SetGlobalEnv {
			SetProxyEnvVars(cfg.Proxy)
		}
	}

	// Propagate to web tools
	b.UpdateWebTools(cfg)

	// Propagate to MCP gateway (reconnects HTTP servers with new transport)
	if err := b.ReconfigureMCP(ctx, cfg); err != nil {
		b.log().Warn("failed to reconfigure MCP with new proxy", "error", err)
	}

	// Rebuild LLM router (new sessions will use the new proxy client)
	if err := b.RebuildRouter(cfg); err != nil {
		b.log().Warn("failed to rebuild router with new proxy", "error", err)
	}

	// Rebuild judge
	b.RebuildJudge(cfg)

	return nil
}

// UpdateWebTools re-registers web_fetch and web_search tools with the current proxy client.
func (b *OrchestratorBuilder) UpdateWebTools(cfg *BuilderConfig) {
	fetchLimits := builtins.WebFetchLimits{
		Timeout: time.Duration(cfg.Timeouts.WebFetchTimeout) * time.Second,
	}
	b.mu.RLock()
	pc := b.proxyClient
	b.mu.RUnlock()
	tools.UpdateWebFetchTool(b.registry, fetchLimits, pc)

	searchLimits := builtins.WebSearchLimits{
		MaxResults: cfg.ToolLimits.WebSearchMaxResults,
		Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
	apiKey := cfg.ExpandEnvVars(cfg.Search.APIKey)
	tools.UpdateSearchToolWithClient(b.registry, cfg.Search.Provider, apiKey, searchLimits, pc)
}

// GenerateTitle generates a concise title for a conversation using the cached LLM router.
func (b *OrchestratorBuilder) GenerateTitle(ctx context.Context, userMessage string, activeSkills []string) (string, error) {
	if err := b.waitReady(ctx); err != nil {
		return "", err
	}

	b.mu.RLock()
	llmRouter := b.llmRouter
	reasoningEffort := b.reasoningEffort
	b.mu.RUnlock()

	if llmRouter == nil {
		return "", errors.New("llm router not available")
	}

	systemPrompt := "Generate a concise title (3-7 words) describing the primary goal for a conversation that starts with the following user message. Output ONLY the title text, no quotes, no punctuation at the end."
	if len(activeSkills) > 0 {
		systemPrompt += "\n\nThe user has explicitly activated the following skills: " + strings.Join(activeSkills, ", ") + ". Consider these when determining the topic."
	}

	temp := 1.0
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:       30,
		Temperature:     &temp,
		ReasoningEffort: reasoningEffort,
	}
	resp, err := llmRouter.Call(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}

// ListProviderModels returns available model names for a given provider.
func (b *OrchestratorBuilder) ListProviderModels(ctx context.Context, provider string, cfg *BuilderConfig) ([]string, error) {
	switch provider {
	case "anthropic":
		return llm.BuiltInModelNames("anthropic-api"), nil
	case "gemini":
		return llm.BuiltInModelNamesByPrefix("gemini-"), nil
	case "chatgpt":
		pc, ok := cfg.LLM.ProviderConfigs["chatgpt"]
		if !ok {
			return nil, errors.New("ChatGPT provider not configured")
		}
		apiKey := cfg.ExpandEnvVars(pc.APIKey)
		if apiKey == "" {
			return nil, errors.New("ChatGPT API key not configured")
		}
		models, err := listOpenAIModels(ctx, "", apiKey)
		if err != nil {
			return nil, err
		}
		return filterKnownFamilyModels(models), nil
	case "openai_compatible":
		pc, ok := cfg.LLM.ProviderConfigs["openai_compatible"]
		if !ok {
			return nil, errors.New("openAI compatible provider not configured")
		}
		baseURL := cfg.ExpandEnvVars(pc.BaseURL)
		apiKey := cfg.ExpandEnvVars(pc.APIKey)
		if baseURL == "" {
			return nil, errors.New("openAI compatible base URL not configured")
		}
		return listOpenAIModels(ctx, baseURL, apiKey)
	case "lmstudio":
		pc, ok := cfg.LLM.ProviderConfigs["lmstudio"]
		baseURL := "http://localhost:1234"
		apiKey := ""
		if ok {
			if u := cfg.ExpandEnvVars(pc.BaseURL); u != "" {
				baseURL = u
			}
			apiKey = cfg.ExpandEnvVars(pc.APIKey)
		}
		return listLMStudioModels(ctx, baseURL, apiKey)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// filterKnownFamilyModels returns only models that belong to a recognized family.
func filterKnownFamilyModels(models []string) []string {
	result := make([]string, 0, len(models))
	for _, m := range models {
		if llm.DetectFamily(m) != llm.FamilyDefault {
			result = append(result, m)
		}
	}
	return result
}

// RegisterVectorSearch adds the semantic_search tool to the shared registry.
// This must be called after NewOrchestratorBuilder when the vector index backend
// is available. The searchFunc and waitFunc are provided by the desktop layer.
func (b *OrchestratorBuilder) RegisterVectorSearch(searchFunc builtins.VectorSearchFunc, waitFunc builtins.VectorSearchWaitFunc) {
	if searchFunc == nil {
		return
	}
	b.mu.Lock()
	b.vectorSearchFunc = searchFunc
	b.mu.Unlock()
	b.registry.Register(builtins.NewVectorSearchTool(searchFunc, waitFunc))
	if b.logger != nil {
		b.logger.Info("registered semantic_search tool")
	}
}

// SetSkillDirs sets the base (shared) skill discovery directories, applied to
// every session. Paths must already be absolute and expanded; the backend
// layer owns path resolution (home/~, env vars, relative-to-agent-dir).
//
// The project-local `<workspacePath>/.agents/skills` directory is always
// prepended automatically in Build() — do NOT include it here.
func (b *OrchestratorBuilder) SetSkillDirs(dirs []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baseSkillDirs = append([]string(nil), dirs...)
}

// GetBaseSkillDirs returns a copy of the base (shared) skill discovery directories.
func (b *OrchestratorBuilder) GetBaseSkillDirs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.baseSkillDirs...)
}

// GetSkillDescriptors returns lightweight skill descriptors from the shared
// base dirs and an optional project-local skill directory. This is used by
// the frontend ListSkills API to avoid creating a full SkillManager per call.
func (b *OrchestratorBuilder) GetSkillDescriptors(projectSkillDir string) []skills.SkillDescriptor {
	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseSkillDirs...)
	b.mu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if projectSkillDir != "" {
		dirs = append(dirs, projectSkillDir)
	}
	dirs = append(dirs, baseDirs...)

	if len(dirs) == 0 {
		return nil
	}

	// Cached: skill list changes only on config reload or project switch.
	// We rebuild on-demand; the FrontendAPI layer caches between calls.
	sm := skills.NewSkillManager(dirs, b.log())
	if err := sm.Scan(); err != nil {
		b.log().Warn("GetSkillDescriptors scan failed", "error", err, "dirs", dirs)
	}
	return sm.List()
}

// buildSessionSkillManager constructs a per-session SkillManager that always
// scans the current project's `.agents/skills` (when workspacePath is set) in
// addition to the shared base dirs. Scan errors are logged and do not abort
// session start-up.
func (b *OrchestratorBuilder) buildSessionSkillManager(workspacePath string, logger *slog.Logger) *skills.SkillManager {
	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseSkillDirs...)
	b.mu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if workspacePath != "" {
		dirs = append(dirs, filepath.Join(workspacePath, ".agents", "skills"))
	}
	dirs = append(dirs, baseDirs...)

	if len(dirs) == 0 {
		return nil
	}

	sm := skills.NewSkillManager(dirs, logger)
	if err := sm.Scan(); err != nil {
		if logger != nil {
			logger.Warn("session skill scan failed", "error", err, "dirs", dirs)
		} else {
			b.log().Warn("session skill scan failed", "error", err, "dirs", dirs)
		}
	}
	return sm
}

// activeSkillPathResolver resolves a skill name to its directory by consulting
// the ActiveSkills set in the request context. This is the resolver used by the
// read_skill_resource tool: only skills that were activated by the router on
// the current request are addressable, matching the tool's documented contract.
func activeSkillPathResolver(ctx context.Context, skillName string) (string, bool) {
	as := ActiveSkillsFromContext(ctx)
	if as == nil {
		return "", false
	}
	for _, s := range as.Skills {
		if s != nil && s.Metadata.Name == skillName {
			return s.DirPath, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildRouter creates a fresh LLM Router + ModelRegistry from config.
func (b *OrchestratorBuilder) buildRouter(ctx context.Context, cfg *BuilderConfig) (*llm.Router, *llm.ModelRegistry, error) {
	// Snapshot proxyClient under lock to avoid data races with RebuildProxy.
	b.mu.RLock()
	proxyClient := b.proxyClient
	b.mu.RUnlock()

	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range cfg.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: "approximate",
		}
	}
	modelRegistry := llm.NewModelRegistry(overrides)
	if proxyClient != nil {
		modelRegistry.SetHTTPClient(proxyClient)
	}

	initialBackoff, err := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	if err != nil && cfg.LLM.Retry.InitialBackoff != "" {
		b.log().Warn("invalid initial_backoff, using default", "value", cfg.LLM.Retry.InitialBackoff, "error", err)
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
	if err != nil && cfg.LLM.Retry.MaxBackoff != "" {
		b.log().Warn("invalid max_backoff, using default", "value", cfg.LLM.Retry.MaxBackoff, "error", err)
	}

	// Build provider entries from all enabled providers.
	// Iterate in a deterministic order (matching backend/config allProviderEntries)
	// to ensure the first provider in the list is predictable.
	providers := make([]llm.ProviderEntry, 0, len(cfg.LLM.ProviderConfigs))
	providerOrder := []string{"anthropic", "gemini", "lmstudio", "openai_compatible", "chatgpt"}
	for _, name := range providerOrder {
		pc, ok := cfg.LLM.ProviderConfigs[name]
		if !ok || len(pc.Models) == 0 {
			continue
		}
		providers = append(providers, llm.ProviderEntry{
			Name:         name,
			ProviderType: pc.ProviderType,
			APIKey:       cfg.ExpandEnvVars(pc.APIKey),
			BaseURL:      cfg.ExpandEnvVars(pc.BaseURL),
			Models:       pc.Models,
		})
	}
	// Also include any providers not in the standard order (e.g. future additions).
	// Collect unknown names and iterate in sorted order for determinism.
	var unknown []string
	for name, pc := range cfg.LLM.ProviderConfigs {
		if len(pc.Models) == 0 || slices.Contains(providerOrder, name) {
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		pc := cfg.LLM.ProviderConfigs[name]
		providers = append(providers, llm.ProviderEntry{
			Name:         name,
			ProviderType: pc.ProviderType,
			APIKey:       cfg.ExpandEnvVars(pc.APIKey),
			BaseURL:      cfg.ExpandEnvVars(pc.BaseURL),
			Models:       pc.Models,
		})
	}

	routerCfg := llm.RouterConfig{
		Providers:           providers,
		MaxRetries:          cfg.LLM.Retry.MaxRetries,
		InitialBackoff:      initialBackoff,
		MaxBackoff:          maxBackoff,
		SafetyMarginPercent: cfg.Executor.Compaction.SafetyMarginPercent,
		OutputTokenReserve:  cfg.Executor.OutputTokenReserve,
		HTTPClient:          proxyClient,
		SamplingFunc: func(family string) *float64 {
			return prompt.DefaultSampling(family).Temperature
		},
	}
	llmRouter, err := llm.NewRouter(ctx, routerCfg, modelRegistry)
	if err != nil {
		return nil, nil, err
	}

	// Initialize the router to the default model.
	if cfg.LLM.DefaultModel != "" {
		if err := llmRouter.SetModel(ctx, cfg.LLM.DefaultModel); err != nil {
			return nil, nil, fmt.Errorf("default model %q: %w", cfg.LLM.DefaultModel, err)
		}
	}

	return llmRouter, modelRegistry, nil
}


// buildCoreAgents creates the core Router, Planner, Reflector.
func (b *OrchestratorBuilder) buildCoreAgents(
	caller agent.LLMCaller,
	cfg *BuilderConfig,
	emitter Emitter,
	logger *slog.Logger,
	modelRegistry *llm.ModelRegistry,
	contextFactory ContextManagerFactory,
	tokenCounter llm.TokenCounter,
	dumpWriter io.Writer,
) (*router.Router, *planner.Planner, *reflector.Reflector) {
	if caller == nil {
		return nil, nil, nil
	}
	loggedCaller := agent.NewLoggingLLMCaller(caller, cfg.LLM.DefaultProviderName(), logger)
	loggedCaller = agent.NewDumpCaller(loggedCaller, dumpWriter, logger)
	coreRouter := newCoreRouter(loggedCaller, cfg.Router.HistoryWindow)
	corePlanner := newCorePlanner(loggedCaller, b.registry)
	coreReflector := newCoreReflector(loggedCaller)

	if modelRegistry != nil {
		coreRouter.SetModelRegistry(modelRegistry)
		corePlanner.Cfg.ModelRegistry = modelRegistry
	}

	// Wire reasoning effort for all agents
	coreRouter.SetReasoningEffort(b.reasoningEffort)
	coreReflector.SetReasoningEffort(b.reasoningEffort)
	corePlanner.Cfg.ReasoningEffort = b.reasoningEffort

	// Wire planner exploration dependencies
	corePlanner.Cfg.Logger = logger
	corePlanner.Cfg.Model = cfg.LLM.DefaultModel
	corePlanner.Cfg.TokenCounter = tokenCounter
	corePlanner.Cfg.ContextFactory = plannerContextFactoryAdapter(contextFactory)
	corePlanner.Cfg.Emitter = emitter
	corePlanner.Cfg.MaxExploreSteps = cfg.Orchestration.MaxPlannerExploreSteps

	// Wire CallerForStep so exploration's ContextTokenTracker gets corrected by API responses
	if tc, ok := caller.(*llm.TrackingCaller); ok {
		corePlanner.Cfg.CallerForStep = func(cm agent.ContextManager) agent.LLMCaller {
			if ctm, ok := cm.(interface {
				ContextTracker() *llm.ContextTokenTracker
			}); ok {
				return tc.WithContextTracker(ctm.ContextTracker())
			}
			return tc
		}
	}

	return coreRouter, corePlanner, coreReflector
}

// plannerContextFactoryAdapter adapts a core ContextManagerFactory to the planner's
// ContextManagerFactory signature by dropping the variadic PruningOverride parameter
// (the planner never uses pruning overrides during exploration).
func plannerContextFactoryAdapter(cf ContextManagerFactory) planner.ContextManagerFactory {
	if cf == nil {
		return nil
	}
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) agent.ContextManager {
		return cf(systemPrompt, modelMeta, compactionStrategy)
	}
}

// buildContextFactory creates a ContextManagerFactory using the tracking caller for
// compaction summarization (ensuring those tokens are counted in session totals).
func (b *OrchestratorBuilder) buildContextFactory(caller *llm.TrackingCaller, cfg *BuilderConfig, modelRegistry *llm.ModelRegistry, dumpWriter io.Writer) ContextManagerFactory {
	var summarizeCaller agent.LLMCaller = caller
	summarizeCaller = agent.NewDumpCaller(summarizeCaller, dumpWriter, b.logger)
	compactionEffort := b.reasoningEffort

	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager {
		counter, err := llm.NewTokenCounter(modelMeta.TokenizerType)
		if err != nil {
			b.log().Warn("token counter fallback", "tokenizer", modelMeta.TokenizerType, "error", err)
			counter = llm.NewSimpleTokenCounter()
		}
		tracker := llm.NewContextTokenTracker(counter)

		strategy := sdkmemory.NewCompactionStrategy(compactionStrategy, sdkmemory.CompactionConfig{
			SlidingWindow: struct{ KeepFirst, KeepLast int }{
				KeepFirst: cfg.Executor.Compaction.SlidingWindow.KeepFirst,
				KeepLast:  cfg.Executor.Compaction.SlidingWindow.KeepLast,
			},
			Summarization: struct {
				BlockSize           int
				KeepLast            int
				ObservationTruncate int
			}{
				BlockSize:           cfg.Executor.Compaction.Summarization.BlockSize,
				KeepLast:            cfg.Executor.Compaction.Summarization.KeepLast,
				ObservationTruncate: cfg.Executor.Compaction.ObservationTruncate,
			},
			Hierarchical: struct{ DistantRatio, MiddleRatio, RecentRatio float64 }{
				DistantRatio: cfg.Executor.Compaction.Hierarchical.DistantRatio,
				MiddleRatio:  cfg.Executor.Compaction.Hierarchical.MiddleRatio,
				RecentRatio:  cfg.Executor.Compaction.Hierarchical.RecentRatio,
			},
		}, sdkmemory.CompactionDeps{
			TokenCounter:       counter,
			MaxSummarizeTokens: cfg.Executor.Compaction.MaxSummarizeTokens,
			Summarize: func(ctx context.Context, blockText string) (string, error) {
				if summarizeCaller == nil {
					return "", errors.New("compaction summarize: LLM caller not available")
				}
				req := llm.ChatRequest{
					Messages: []llm.Message{
						{Role: "system", Content: coreprompts.CompactionSummarize},
						{Role: "user", Content: blockText},
					},
					ReasoningEffort: compactionEffort,
				}
				resp, err := summarizeCaller.Call(ctx, req)
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

		pruning := sdkmemory.ToolOutputPruning{
			KeepLastN:        cfg.Executor.ToolOutputPruning.KeepLastN,
			ProtectedTools:   cfg.Executor.ToolOutputPruning.ProtectedTools,
			ThresholdPercent: cfg.Executor.ToolOutputPruning.ThresholdPercent,
			Logger:           b.logger,
		}
		// Apply per-step pruning overrides from StepConfig (via planner role assignment).
		if len(pruningOverrides) > 0 {
			po := pruningOverrides[0]
			if po.KeepLastN > 0 {
				pruning.KeepLastN = po.KeepLastN
			}
			if po.ProtectedTools != nil {
				pruning.ProtectedTools = po.ProtectedTools
			}
		}

		cw := sdkmemory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy, cfg.Executor.Compaction.SafetyMarginPercent, cfg.Security.InjectionDefenseEnabled, pruning)
		return NewCoreContextManager(cw)
	}
}

// rebuildJudgeInternal recreates the ToolJudge and sets it on the registry.
func (b *OrchestratorBuilder) rebuildJudgeInternal(cfg *BuilderConfig, llmRouter *llm.Router) {
	if b.registry == nil {
		return
	}

	defaultModel := cfg.LLM.DefaultModel

	var judgeProvider llm.Provider
	if llmRouter != nil {
		judgeProvider = llmRouter.GetDefaultProvider()
	} else {
		// Try building a fresh router
		newRouter, _, err := b.buildRouter(context.Background(), cfg)
		if err == nil && newRouter != nil {
			judgeProvider = newRouter.GetDefaultProvider()
		} else if err != nil && b.logger != nil {
			b.logger.Warn("rebuildJudge: failed to build LLM router for judge", "error", err)
		}
	}

	judge := sdktools.NewToolJudgeFromConfig(sdktools.JudgeConfig{
		Model:        cfg.Security.JudgeModel,
		DefaultModel: defaultModel,
		Provider:     judgeProvider,
		MaxCacheSize: cfg.Orchestration.MaxJudgeCacheSize,
	}, b.logger)

	if judge != nil {
		b.registry.SetJudge(judge)
		if b.logger != nil {
			b.logger.Info("tool judge rebuilt successfully")
		}
	} else if b.logger != nil {
		b.logger.Warn("tool judge rebuild failed: judge will not be available for on-demand evaluation")
	}
}

// applySecurityPolicies applies per-tool policy overrides and default policy.
func (b *OrchestratorBuilder) applySecurityPolicies(cfg *BuilderConfig) {
	policyOverrides := make(map[string]sdktools.ToolPolicy)
	for toolName, policyCfg := range cfg.Security.ToolPolicies {
		policyOverrides[toolName] = sdktools.ParseToolPolicy(policyCfg.Policy)
	}
	b.registry.SetPolicyOverrides(policyOverrides)

	if cfg.Security.DefaultPolicy != "" {
		b.registry.SetDefaultPolicy(sdktools.ParseToolPolicy(cfg.Security.DefaultPolicy))
	}

	b.registry.SetAutoApproveWorkspaceWrites(cfg.Security.AutoApproveWorkspaceWrites)
}

// ---------------------------------------------------------------------------
// Config conversion helpers
// ---------------------------------------------------------------------------

// configToBuiltinToolsConfig converts BuilderConfig to BuiltinToolsConfig.
func configToBuiltinToolsConfig(cfg *BuilderConfig) tools.BuiltinToolsConfig {
	var bashBlacklist []string
	if bashCfg, ok := cfg.Security.ToolPolicies[ToolBashExec]; ok {
		bashBlacklist = bashCfg.Blacklist
	}

	return tools.BuiltinToolsConfig{
		FileLimits: builtins.FileLimits{
			ReadDefaultLines: cfg.ToolLimits.ReadDefaultLines,
		},
		RipgrepLimits: builtins.RipgrepLimits{
			Timeout: time.Duration(cfg.Timeouts.RipgrepTimeout) * time.Second,
		},
		WebFetchLimits: builtins.WebFetchLimits{
			Timeout: time.Duration(cfg.Timeouts.WebFetchTimeout) * time.Second,
		},
		WebSearchLimits: builtins.WebSearchLimits{
			MaxResults: cfg.ToolLimits.WebSearchMaxResults,
			Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
		},
		BashTimeouts: builtins.BashTimeouts{
			MaxTimeout: time.Duration(cfg.Timeouts.BashMaxTimeout) * time.Second,
			WaitDelay:  time.Duration(cfg.Timeouts.BashWaitDelay) * time.Second,
		},
		BashBlacklist:  bashBlacklist,
		SearchProvider: cfg.Search.Provider,
		SearchAPIKey:   cfg.ExpandEnvVars(cfg.Search.APIKey),
		SearchTimeout:  time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Standalone model listing helpers (no receiver state needed)
// ---------------------------------------------------------------------------

// listOpenAIModels fetches model names from an OpenAI-compatible API.
func listOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := oai.NewClient(opts...)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	modelList, err := client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	names := make([]string, 0, len(modelList.Data))
	for _, m := range modelList.Data {
		names = append(names, m.ID)
	}
	sort.Strings(names)
	return names, nil
}

// listLMStudioModels fetches model names from an LM Studio server.
func listLMStudioModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	provider, err := llm.NewLMStudioProvider(llm.LMStudioProviderConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create LM Studio provider: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	models, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.ID)
	}
	sort.Strings(names)
	return names, nil
}
