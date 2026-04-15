package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	coreprompts "github.com/user/agent/core/prompts"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/mcp"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	sdkmemory "github.com/user/agent/sdk/memory"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/tools/builtins"
)

// OrchestratorBuilder owns the shared tool registry, MCP gateway, and cached
// LLM router. It provides Build() to create per-session Orchestrators and
// exposes methods for runtime reconfiguration (judge, router, MCP, security).
//
// OrchestratorBuilder lives in core so that all SDK imports are confined to
// the core layer. The backend.Application wraps it without importing the SDK.
type OrchestratorBuilder struct {
	mu            sync.RWMutex
	registry      *tools.ToolRegistry
	gateway       *mcp.Gateway
	llmRouter     *llm.Router
	modelRegistry *llm.ModelRegistry
	logger        *slog.Logger
}

// NewOrchestratorBuilder creates the shared infrastructure: tool registry,
// built-in tools, MCP gateway, LLM router, and tool judge.
// The cfg is used for initial setup; runtime changes are applied via the
// Rebuild* / Reconfigure* methods.
func NewOrchestratorBuilder(cfg *BuilderConfig, askUserFunc tools.AskUserFunc, logger *slog.Logger) (*OrchestratorBuilder, error) {
	b := &OrchestratorBuilder{
		logger: logger,
	}

	// 1. Tool registry + built-in tools
	b.registry = tools.NewToolRegistry()

	toolsCfg := configToBuiltinToolsConfig(cfg)
	toolsCfg.AskUserFunc = askUserFunc
	tools.RegisterBuiltinTools(b.registry, toolsCfg)

	// 2. MCP Gateway (optional — failures are non-fatal)
	mcpCfg := configToGatewayConfig(cfg)
	gw, err := mcp.StartGateway(context.Background(), mcpCfg, b.registry, cfg.ExpandEnvVars, logger)
	if err != nil {
		slog.Warn("MCP gateway startup failed", "error", err)
	}
	b.gateway = gw

	// 3. Security policies
	b.applySecurityPolicies(cfg)

	// 4. LLM Router (fail-fast validation)
	router, modelReg, err := b.buildRouter(cfg)
	if err != nil {
		slog.Warn("failed to initialize LLM router at startup", "error", err)
		// Non-fatal — router will be rebuilt per-session from current config.
	} else {
		b.llmRouter = router
		b.modelRegistry = modelReg
	}

	// 5. Tool judge
	b.rebuildJudgeInternal(cfg, b.llmRouter)

	return b, nil
}

// ToolRegistry returns the shared tool registry.
func (b *OrchestratorBuilder) ToolRegistry() *tools.ToolRegistry {
	return b.registry
}

// MCPGateway returns the MCP gateway, or nil if not started.
func (b *OrchestratorBuilder) MCPGateway() *mcp.Gateway {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.gateway
}

// Build creates a new per-session Orchestrator from the current config.
// Each session gets a fresh LLM router, core agents, and context factory.
// The shared tool registry and MCP gateway are reused across sessions.
func (b *OrchestratorBuilder) Build(
	cfg *BuilderConfig,
	emitter Emitter,
	logger *slog.Logger,
	bbFactory BlackboardFactory,
	stepLimitFunc StepLimitFunc,
	dumpWriter io.Writer,
) (*Orchestrator, error) {
	// Wrap emitter with logging
	emitter = NewLoggingEmitter(emitter, logger)

	// Build per-session LLM router + model registry
	router, modelReg, err := b.buildRouter(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM router: %w", err)
	}
	if cfg.LLM.ActiveProvider == "" {
		return nil, errors.New("no active LLM provider configured - check your config.yaml")
	}

	// Build context factory
	contextFactory := b.buildContextFactory(router, cfg, dumpWriter)

	// Build core agents (router, planner, reflector) with token tracking
	tokenCounter := llm.NewSimpleTokenCounter()
	coreRouter, planner, reflector := b.buildCoreAgents(router, cfg, emitter, logger, modelReg, contextFactory, tokenCounter, dumpWriter)
	if coreRouter == nil || planner == nil {
		return nil, errors.New("orchestrator dependencies not initialized: LLM router, router, or planner is nil")
	}

	// Build orchestrator config
	orchConfig := OrchestratorConfig{
		MaxSteps:                  cfg.Executor.MaxReactSteps,
		KeepFirst:                 cfg.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:                  cfg.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries:                cfg.Executor.MaxRetries,
		MaxHistoryMessages:        cfg.Orchestration.MaxHistoryMessages,
		MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
		StepLimitFunc:             stepLimitFunc,
	}

	// Token counter, budgets, circuit breaker
	toolResultBudget := ToolResultBudget{
		HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
		MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
	}
	circuitBreaker := CircuitBreakerConfig{
		RepeatNudgeThreshold:     cfg.Executor.CircuitBreaker.RepeatNudgeThreshold,
		RepeatAbortThreshold:     cfg.Executor.CircuitBreaker.RepeatAbortThreshold,
		TruncationAbortThreshold: cfg.Executor.CircuitBreaker.TruncationAbortThreshold,
		ParseErrorAbortThreshold: cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold,
		FruitlessNudgeThreshold:      cfg.Executor.CircuitBreaker.FruitlessNudgeThreshold,
		FruitlessAbortThreshold:      cfg.Executor.CircuitBreaker.FruitlessAbortThreshold,
		FruitlessMaxResultLen:        cfg.Executor.CircuitBreaker.FruitlessMaxResultLen,
		SameToolRepeatNudgeThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatNudgeThreshold,
		SameToolRepeatAbortThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatAbortThreshold,
		SameToolResultSizeDelta:      cfg.Executor.CircuitBreaker.SameToolResultSizeDelta,
	}

	// Logged LLM caller for step execution
	loggedLLM := agent.NewLoggingLLMCaller(router, cfg.LLM.ActiveProvider, logger)
	loggedLLM = agent.NewDumpCaller(loggedLLM, dumpWriter)

	return NewOrchestrator(
		coreRouter,
		planner,
		loggedLLM,
		b.registry,              // ToolExecutor (shared)
		b.registry.ToolRegistry, // SDK ToolRegistry (shared)
		tokenCounter,
		orchConfig,
		contextFactory,
		reflector,
		logger,
		emitter,
		modelReg,
		toolResultBudget,
		circuitBreaker,
		bbFactory,
	), nil
}

// RebuildRouter creates a new LLM router from the given config and caches it.
// This is called when LLM settings change at runtime.
func (b *OrchestratorBuilder) RebuildRouter(cfg *BuilderConfig) error {
	router, modelReg, err := b.buildRouter(cfg)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.llmRouter = router
	b.modelRegistry = modelReg
	b.mu.Unlock()
	return nil
}

// RebuildJudge recreates the tool judge from the given config.
// If router is nil, the cached router is used.
func (b *OrchestratorBuilder) RebuildJudge(cfg *BuilderConfig) {
	b.mu.RLock()
	router := b.llmRouter
	b.mu.RUnlock()
	b.rebuildJudgeInternal(cfg, router)
}

// ReconfigureMCP reconfigures the MCP gateway with the given config.
// If no gateway exists, starts a new one.
func (b *OrchestratorBuilder) ReconfigureMCP(ctx context.Context, cfg *BuilderConfig) error {
	mcpCfg := configToGatewayConfig(cfg)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.gateway != nil {
		return b.gateway.Reconfigure(ctx, mcpCfg, b.registry, cfg.ExpandEnvVars, b.logger)
	}

	gw, err := mcp.StartGateway(ctx, mcpCfg, b.registry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		return err
	}
	b.gateway = gw
	return nil
}

// UpdateSecurityPolicies applies security policy overrides from config.
func (b *OrchestratorBuilder) UpdateSecurityPolicies(cfg *BuilderConfig) {
	b.applySecurityPolicies(cfg)
}

// UpdateSearchTool replaces or removes the web_search tool in the registry.
func (b *OrchestratorBuilder) UpdateSearchTool(cfg *BuilderConfig) {
	apiKey := cfg.ExpandEnvVars(cfg.Search.APIKey)
	limits := tools.WebSearchLimits{
		MaxResults: cfg.ToolLimits.WebSearchMaxResults,
		Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
	tools.UpdateSearchTool(b.registry, cfg.Search.Provider, apiKey, limits)
}

// SetBashRtkPath updates the rtk binary path on the registered bash_exec tool.
// This is called after rtk is installed at runtime.
func (b *OrchestratorBuilder) SetBashRtkPath(path string) {
	tool, ok := b.registry.Get("bash_exec")
	if !ok {
		return
	}
	if bashTool, ok := tool.(*builtins.BashExecTool); ok {
		bashTool.SetRtkPath(path)
		slog.Info("updated bash_exec rtk path", "path", path)
	}
}

// GenerateTitle generates a concise title for a conversation using the cached LLM router.
func (b *OrchestratorBuilder) GenerateTitle(ctx context.Context, userMessage string) (string, error) {
	b.mu.RLock()
	router := b.llmRouter
	b.mu.RUnlock()

	if router == nil {
		return "", errors.New("LLM router not available")
	}

	temp := 0.3
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Generate a concise title (3-7 words) for a conversation that starts with the following user message. Output ONLY the title text, no quotes, no punctuation at the end."},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   30,
		Temperature: &temp,
	}
	resp, err := router.Call(ctx, req)
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
		apiKey := cfg.ExpandEnvVars(cfg.LLM.ChatGPTAPIKey)
		if apiKey == "" {
			return nil, errors.New("ChatGPT API key not configured")
		}
		return listOpenAIModels(ctx, "", apiKey)
	case "openai_compatible":
		baseURL := cfg.ExpandEnvVars(cfg.LLM.OpenAICompatBaseURL)
		apiKey := cfg.ExpandEnvVars(cfg.LLM.OpenAICompatAPIKey)
		if baseURL == "" {
			return nil, errors.New("OpenAI Compatible base URL not configured")
		}
		return listOpenAIModels(ctx, baseURL, apiKey)
	case "lmstudio":
		baseURL := cfg.ExpandEnvVars(cfg.LLM.LMStudioBaseURL)
		if baseURL == "" {
			baseURL = "http://localhost:1234"
		}
		apiKey := cfg.ExpandEnvVars(cfg.LLM.LMStudioAPIKey)
		return listLMStudioModels(ctx, baseURL, apiKey)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// StopGateway stops the MCP gateway. Called during app shutdown.
func (b *OrchestratorBuilder) StopGateway() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gateway != nil {
		return b.gateway.Stop()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildRouter creates a fresh LLM Router + ModelRegistry from config.
func (b *OrchestratorBuilder) buildRouter(cfg *BuilderConfig) (*llm.Router, *llm.ModelRegistry, error) {
	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range cfg.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: "approximate",
		}
	}
	modelRegistry := llm.NewModelRegistry(overrides)

	initialBackoff, err := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	if err != nil && cfg.LLM.Retry.InitialBackoff != "" {
		slog.Warn("invalid initial_backoff, using default", "value", cfg.LLM.Retry.InitialBackoff, "error", err)
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
	if err != nil && cfg.LLM.Retry.MaxBackoff != "" {
		slog.Warn("invalid max_backoff, using default", "value", cfg.LLM.Retry.MaxBackoff, "error", err)
	}
	routerCfg := llm.RouterConfig{
		ActiveProvider:      cfg.LLM.ActiveProvider,
		ProviderType:        cfg.LLM.ProviderType,
		APIKey:              cfg.ExpandEnvVars(cfg.LLM.APIKey),
		BaseURL:             cfg.ExpandEnvVars(cfg.LLM.BaseURL),
		Model:               cfg.LLM.Model,
		MaxRetries:          cfg.LLM.Retry.MaxRetries,
		InitialBackoff:      initialBackoff,
		MaxBackoff:          maxBackoff,
		SafetyMarginPercent: cfg.Executor.Compaction.SafetyMarginPercent,
		OutputTokenReserve:  cfg.Executor.OutputTokenReserve,
	}
	router, err := llm.NewRouter(context.Background(), routerCfg, modelRegistry)
	if err != nil {
		return nil, nil, err
	}
	return router, modelRegistry, nil
}

// buildCoreAgents creates the core Router, Planner, Reflector with token tracking.
func (b *OrchestratorBuilder) buildCoreAgents(
	router *llm.Router,
	cfg *BuilderConfig,
	emitter Emitter,
	logger *slog.Logger,
	modelRegistry *llm.ModelRegistry,
	contextFactory ContextManagerFactory,
	tokenCounter llm.TokenCounter,
	dumpWriter io.Writer,
) (*Router, *Planner, *Reflector) {
	if router == nil {
		return nil, nil, nil
	}
	caller := orchestration.NewTokenTrackingCaller(router, emitter)
	caller = agent.NewLoggingLLMCaller(caller, cfg.LLM.ActiveProvider, logger)
	caller = agent.NewDumpCaller(caller, dumpWriter)
	coreRouter := NewRouter(caller, cfg.Router.HistoryWindow)
	planner := NewPlanner(caller)
	reflector := NewReflector(caller)

	if modelRegistry != nil {
		coreRouter.SetModelRegistry(modelRegistry)
		planner.SetModelRegistry(modelRegistry)
		reflector.SetModelRegistry(modelRegistry)
	}

	// Wire planner exploration dependencies
	planner.SetToolRegistry(b.registry)
	planner.SetTokenCounter(tokenCounter)
	planner.SetContextFactory(contextFactory)
	planner.SetEmitter(emitter)
	planner.SetMaxExploreSteps(cfg.Orchestration.MaxPlannerExploreSteps)

	return coreRouter, planner, reflector
}

// buildContextFactory creates a ContextManagerFactory from LLM router and config.
func (b *OrchestratorBuilder) buildContextFactory(router *llm.Router, cfg *BuilderConfig, dumpWriter io.Writer) ContextManagerFactory {
	var summarizeCaller agent.LLMCaller = router
	summarizeCaller = agent.NewDumpCaller(summarizeCaller, dumpWriter)

	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		counter, err := llm.NewTokenCounter(modelMeta.TokenizerType)
		if err != nil {
			slog.Warn("token counter fallback", "tokenizer", modelMeta.TokenizerType, "error", err)
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
				if router == nil {
					return "", errors.New("compaction summarize: LLM router not available")
				}
				req := llm.ChatRequest{
					Messages: []llm.Message{
						{Role: "system", Content: coreprompts.CompactionSummarize},
						{Role: "user", Content: blockText},
					},
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
			KeepLastN:      cfg.Executor.ToolOutputPruning.KeepLastN,
			ProtectedTools: cfg.Executor.ToolOutputPruning.ProtectedTools,
		}

		cw := sdkmemory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy, cfg.Executor.Compaction.SafetyMarginPercent, pruning)
		return NewCoreContextManager(cw)
	}
}

// rebuildJudgeInternal recreates the ToolJudge and sets it on the registry.
func (b *OrchestratorBuilder) rebuildJudgeInternal(cfg *BuilderConfig, router *llm.Router) {
	if b.registry == nil {
		return
	}

	defaultModel := cfg.LLM.Model

	var judgeProvider llm.Provider
	if router != nil {
		judgeProvider = router.GetDefaultProvider()
	} else {
		// Try building a fresh router
		newRouter, _, err := b.buildRouter(cfg)
		if err == nil && newRouter != nil {
			judgeProvider = newRouter.GetDefaultProvider()
		} else if b.logger != nil {
			b.logger.Warn("rebuildJudge: failed to build LLM router for judge", "error", err)
		}
	}

	judge := tools.NewToolJudgeFromConfig(tools.JudgeConfig{
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
	policyOverrides := make(map[string]tools.ToolPolicy)
	for toolName, policyCfg := range cfg.Security.ToolPolicies {
		policyOverrides[toolName] = tools.ParseToolPolicy(policyCfg.Policy)
	}
	b.registry.SetPolicyOverrides(policyOverrides)

	if cfg.Security.DefaultPolicy != "" {
		b.registry.SetDefaultPolicy(tools.ParseToolPolicy(cfg.Security.DefaultPolicy))
	}
}

// ---------------------------------------------------------------------------
// Config conversion helpers
// ---------------------------------------------------------------------------

// configToBuiltinToolsConfig converts BuilderConfig to BuiltinToolsConfig.
func configToBuiltinToolsConfig(cfg *BuilderConfig) tools.BuiltinToolsConfig {
	var bashBlacklist []string
	if bashCfg, ok := cfg.Security.ToolPolicies["bash_exec"]; ok {
		bashBlacklist = bashCfg.Blacklist
	}

	return tools.BuiltinToolsConfig{
		FileLimits: tools.FileLimits{
			ReadDefaultLines:  cfg.ToolLimits.ReadDefaultLines,
			ReadMaxLineLength: cfg.ToolLimits.ReadMaxLineLength,
			ReadMaxBytes:      cfg.ToolLimits.ReadMaxBytes,
			FileSearchMatches: cfg.ToolLimits.FileSearchMaxMatches,
		},
		RipgrepLimits: tools.RipgrepLimits{
			MaxResults:    cfg.ToolLimits.RipgrepMaxResults,
			MaxLineLength: cfg.ToolLimits.RipgrepMaxLineLength,
			Timeout:       time.Duration(cfg.Timeouts.RipgrepTimeout) * time.Second,
		},
		GlobLimits: tools.GlobLimits{
			MaxResults: cfg.ToolLimits.GlobMaxResults,
		},
		WebFetchLimits: tools.WebFetchLimits{
			MaxBodySize: cfg.ToolLimits.WebFetchMaxBodySize,
			Timeout:     time.Duration(cfg.Timeouts.WebFetchTimeout) * time.Second,
		},
		WebSearchLimits: tools.WebSearchLimits{
			MaxResults: cfg.ToolLimits.WebSearchMaxResults,
			Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
		},
		BatchLimits: tools.BatchLimits{
			MaxConcurrency: cfg.ToolLimits.BatchMaxConcurrency,
			MaxResultSize:  cfg.ToolLimits.BatchMaxResultSize,
		},
		BashTimeouts: tools.BashTimeouts{
			MaxTimeout: time.Duration(cfg.Timeouts.BashMaxTimeout) * time.Second,
			WaitDelay:  time.Duration(cfg.Timeouts.BashWaitDelay) * time.Second,
		},
		BashBlacklist:  bashBlacklist,
		RtkPath:        detectRtkPath(),
		SearchProvider: cfg.Search.Provider,
		SearchAPIKey:   cfg.ExpandEnvVars(cfg.Search.APIKey),
		SearchTimeout:  time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
}

// detectRtkPath returns the path to the rtk binary if it's available.
func detectRtkPath() string {
	if path, err := exec.LookPath("rtk"); err == nil {
		return path
	}
	return ""
}

// configToGatewayConfig converts BuilderConfig to MCP GatewayConfig.
func configToGatewayConfig(cfg *BuilderConfig) mcp.GatewayConfig {
	entries := make(map[string]mcp.ServerEntry, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		entries[name] = mcp.ServerEntry{
			Transport: srv.Transport,
			Command:   srv.Command,
			Args:      srv.Args,
			Env:       srv.Env,
			URL:       srv.URL,
			Headers:   srv.Headers,
		}
	}
	return mcp.GatewayConfig{Servers: entries}
}

// ---------------------------------------------------------------------------
// Standalone model listing helpers (no receiver state needed)
// ---------------------------------------------------------------------------

// listOpenAIModels fetches model names from an OpenAI-compatible API.
func listOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	var client *openai.Client
	if baseURL == "" {
		client = openai.NewClient(apiKey)
	} else {
		cfg := openai.DefaultConfig(apiKey)
		cfg.BaseURL = baseURL
		client = openai.NewClientWithConfig(cfg)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	modelList, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	names := make([]string, 0, len(modelList.Models))
	for _, m := range modelList.Models {
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
