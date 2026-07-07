// Package sdk is the entry point for the sp4rk Agent SDK — a standalone Go framework
// for building AI agent systems with Plan & Execute orchestration, tool integration,
// and multi-provider LLM support.
//
// Quick start:
//
//	fw, _ := sdk.New(sdk.Config{
//	    LLM: sdk.LLMConfig{
//	        Providers: []llm.ProviderEntry{{
//	            Name: "anthropic", ProviderType: "anthropic",
//	            APIKey: os.Getenv("ANTHROPIC_API_KEY"), Models: []string{"claude-sonnet-4-5"},
//	        }},
//	    },
//	})
//	defer fw.Shutdown()
//
//	result, _ := fw.Execute(ctx, mySystemPrompt, myEvents, "Write a hello world in Go")
package sdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// Framework is the top-level entry point for building agent systems with the SDK.
// It owns shared infrastructure (LLM router, tool registry, MCP gateway, tool cache) and
// creates per-session orchestrators via NewOrchestrator().
type Framework struct {
	cfg        Config
	llmRouter  *llm.Router
	tools      *tools.ToolRegistry
	modelReg   *llm.ModelRegistry
	mcpGateway *mcp.Gateway
	toolCache  *agent.ToolResultCache
	logger     *slog.Logger
}

// Config holds all configuration for the Framework.
// Zero-value fields are replaced with sensible defaults during New().
type Config struct {
	// LLM configures the LLM providers and default model.
	LLM LLMConfig

	// MCP optionally configures Model Context Protocol servers.
	// Nil means no MCP integration.
	MCP *MCPConfig

	// Execution configures agent execution parameters.
	Execution ExecutionConfig

	// Compaction configures context window management.
	Compaction CompactionConfig

	// HITL optionally provides human-in-the-loop hooks.
	// Nil means defaults (allow all tool calls, deny step extensions).
	HITL agent.HITLHandler

	// Checkpointer optionally provides state persistence.
	Checkpointer orchestration.Checkpointer

	// OnBlackboardChanged is an optional callback invoked after every successful
	// blackboard write (plan, step_result, fact, reflection). The changeType argument
	// describes what changed. Nil means no notifications.
	OnBlackboardChanged func(changeType string)

	// Logger is an optional structured logger. Uses slog.Default() if nil.
	Logger *slog.Logger
}

// LLMConfig configures LLM providers.
type LLMConfig struct {
	// Providers lists all enabled LLM providers. At least one is required.
	Providers []llm.ProviderEntry

	// DefaultModel optionally overrides the auto-selected default model.
	// When empty, the Router auto-selects the first provider's first model.
	// When set, it must be a bare model name ("claude-sonnet-4-5") or a
	// composite identifier ("anthropic/claude-sonnet-4-5") that exists in
	// some provider's Models list.
	DefaultModel string

	// MaxRetries sets the number of retry attempts for transient errors.
	// 0 means use the default (3).
	MaxRetries int

	// InitialBackoff is the starting backoff duration for retries.
	// Empty string means use the default (1s).
	InitialBackoff string

	// MaxBackoff is the maximum backoff duration for retries.
	// Empty string means use the default (30s).
	MaxBackoff string

	// OutputTokenReserve reserves context window space for model output.
	// This affects context-window validation. 0 means use the default (4096).
	OutputTokenReserve int
}

// MCPConfig configures Model Context Protocol server integration.
type MCPConfig struct {
	// Servers maps server names to their configuration entries.
	Servers map[string]mcp.ServerEntry

	// DefaultWorkDir is the fallback working directory for stdio-based servers.
	DefaultWorkDir string
}

// ExecutionConfig configures agent execution.
type ExecutionConfig struct {
	// MaxSteps is the maximum number of ReAct loop iterations per step.
	// 0 means use the default (50).
	MaxSteps int

	// MaxRetries is the maximum number of retry attempts per plan step.
	// 0 means use the default (2).
	MaxRetries int

	// ToolResultBudget configures tool result truncation.
	// Zero value means use the default.
	ToolResultBudget agent.ToolResultBudget

	// CircuitBreaker configures circuit breaker thresholds.
	// Zero value means use the default.
	CircuitBreaker agent.CircuitBreakerConfig

	// SafetyMarginPercent reserves a percentage of the context window as safety margin.
	// 0 means use the default (5).
	SafetyMarginPercent int

	// PreWarningPercent triggers the pre-compaction store_fact nudge at this context fill %.
	// When fill reaches this threshold (but below the compaction trigger), a warning listing
	// vulnerable tool outputs is appended to the observation. 0 means disabled.
	PreWarningPercent int

	// ToolCacheTTLSeconds controls the TTL for cached tool results. The cache enables
	// tool_result_read fragmentation reads for truncated outputs.
	// 0 means use the default (300s); negative means disabled.
	ToolCacheTTLSeconds int

	// MaxDependencyContextChars limits the context size for step dependency summaries.
	// When provided to the LLM as context for a dependent step. 0 means use the default (8000).
	MaxDependencyContextChars int
}

// CompactionConfig configures context window compaction.
type CompactionConfig struct {
	// Strategy is the compaction algorithm: "sliding", "summary", or "hierarchical".
	// Empty means "sliding".
	Strategy string

	// PredictivePercent triggers predictive compaction at this context fill %.
	// 0 means use the default (85).
	PredictivePercent int

	// WarningPercent triggers warning-level compaction at this context fill %.
	// 0 means use the default (92).
	WarningPercent int

	// EmergencyPercent triggers emergency compaction at this context fill %.
	// 0 means use the default (98).
	EmergencyPercent int
}

// New creates a new Framework from the given configuration.
// It builds the LLM router, tool registry, and optionally starts MCP servers.
// Call Shutdown() when done to release resources.
func New(cfg Config) (*Framework, error) {
	if len(cfg.LLM.Providers) == 0 {
		return nil, errors.New("at least one LLM provider is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Apply defaults for zero-value fields.
	if cfg.Execution.MaxSteps == 0 {
		cfg.Execution.MaxSteps = 50
	}
	if cfg.Execution.MaxRetries == 0 {
		cfg.Execution.MaxRetries = 2
	}
	if cfg.Execution.SafetyMarginPercent == 0 {
		cfg.Execution.SafetyMarginPercent = 5
	}
	if cfg.Execution.MaxDependencyContextChars == 0 {
		cfg.Execution.MaxDependencyContextChars = 8000
	}
	if cfg.Execution.ToolResultBudget == (agent.ToolResultBudget{}) {
		cfg.Execution.ToolResultBudget = agent.DefaultToolResultBudget()
	}
	if cfg.Execution.CircuitBreaker == (agent.CircuitBreakerConfig{}) {
		cfg.Execution.CircuitBreaker = agent.DefaultCircuitBreakerConfig()
	}
	if cfg.Compaction.PredictivePercent == 0 {
		cfg.Compaction.PredictivePercent = 85
	}
	if cfg.Compaction.WarningPercent == 0 {
		cfg.Compaction.WarningPercent = 92
	}
	if cfg.Compaction.EmergencyPercent == 0 {
		cfg.Compaction.EmergencyPercent = 98
	}
	if cfg.Compaction.Strategy == "" {
		cfg.Compaction.Strategy = "sliding"
	}

	// Parse retry durations
	initialBackoff, err := time.ParseDuration(cfg.LLM.InitialBackoff)
	if err != nil && cfg.LLM.InitialBackoff != "" {
		logger.Warn("invalid InitialBackoff, using default", "value", cfg.LLM.InitialBackoff, "error", err)
	}
	if initialBackoff == 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.MaxBackoff)
	if err != nil && cfg.LLM.MaxBackoff != "" {
		logger.Warn("invalid MaxBackoff, using default", "value", cfg.LLM.MaxBackoff, "error", err)
	}
	if maxBackoff == 0 {
		maxBackoff = 30 * time.Second
	}
	maxRetries := cfg.LLM.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}
	outputReserve := cfg.LLM.OutputTokenReserve
	if outputReserve == 0 {
		outputReserve = llm.DefaultRouterConfig().OutputTokenReserve
	}

	// Build LLM router
	routerCfg := llm.RouterConfig{
		Providers:           cfg.LLM.Providers,
		MaxRetries:          maxRetries,
		InitialBackoff:      initialBackoff,
		MaxBackoff:          maxBackoff,
		SafetyMarginPercent: cfg.Execution.SafetyMarginPercent,
		OutputTokenReserve:  outputReserve,
	}
	modelReg := llm.NewModelRegistry(nil)
	llmRouter, err := llm.NewRouter(context.Background(), routerCfg, modelReg)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM router: %w", err)
	}
	if cfg.LLM.DefaultModel != "" {
		if err := llmRouter.SetModel(context.Background(), cfg.LLM.DefaultModel); err != nil {
			return nil, fmt.Errorf("default model %q: %w", cfg.LLM.DefaultModel, err)
		}
	}

	// Create tool result cache (enables tool_result_read fragmentation for truncated outputs).
	var toolCache *agent.ToolResultCache
	if cfg.Execution.ToolCacheTTLSeconds >= 0 {
		ttl := time.Duration(cfg.Execution.ToolCacheTTLSeconds) * time.Second
		if ttl == 0 {
			ttl = 5 * time.Minute // default
		}
		toolCache = agent.NewToolResultCache(ttl)
	}

	fw := &Framework{
		cfg:       cfg,
		llmRouter: llmRouter,
		tools:     tools.NewToolRegistry(),
		modelReg:  modelReg,
		toolCache: toolCache,
		logger:    logger,
	}

	// Start MCP gateway if configured
	if cfg.MCP != nil && len(cfg.MCP.Servers) > 0 {
		pm := tools.DefaultParamManager()
		fw.tools.SetParamManager(pm)
		mcpCfg := mcp.GatewayConfig{
			Servers:         cfg.MCP.Servers,
			DefaultWorkDir:  cfg.MCP.DefaultWorkDir,
			SchemaSanitizer: pm.SanitizeSchema,
		}
		gw, gwErr := mcp.StartGateway(context.Background(), mcpCfg, fw.tools, func(s string) string { return s }, logger)
		if gwErr != nil {
			logger.Warn("MCP gateway startup failed", "error", gwErr)
		}
		fw.mcpGateway = gw
	}

	return fw, nil
}

// NewConductor creates a new per-session Conductor wired with the Framework's
// shared infrastructure (LLM router, tool registry, MCP tools).
//
// systemPrompt is a factory that creates the Conductor's system prompt.
// events receives lifecycle events; use nil for a no-op emitter.
//
// The Conductor is a single ReAct loop that owns a task end-to-end. The
// caller is responsible for injecting any Conductor-specific tools (delegate,
// declare_plan, reflect) into the context before calling Run, if desired.
// The SDK Conductor primitive itself does not provide those tools; they are
// an application-layer concern.
func (fw *Framework) NewConductor(systemPrompt orchestration.SystemPromptFactory, events agent.Events) (*orchestration.Conductor, error) {
	if fw.llmRouter == nil {
		return nil, errors.New("framework not initialized: LLM router is nil")
	}

	tokenCounter := llm.NewSimpleTokenCounter()
	loggedLLM := agent.NewLoggingLLMCaller(agent.LLMCaller(fw.llmRouter), fw.llmRouter.ActiveProviderName(), fw.logger)

	// Session-level usage tracker + tracking caller for per-step context correction.
	usageTracker := llm.NewUsageTracker()
	trackingCaller := llm.NewTrackingCaller(loggedLLM, usageTracker)

	conductorCfg := orchestration.ConductorConfig{
		LLM:               trackingCaller,
		Tools:             fw.tools,
		ToolRegistry:      fw.tools,
		TokenCounter:      tokenCounter,
		Model:             llm.BareModel(fw.llmRouter.ActiveModel()),
		ModelRegistry:     fw.modelReg,
		ContextFactory:    fw.buildContextWindow,
		SystemPrompt:      systemPrompt,
		MaxSteps:          fw.cfg.Execution.MaxSteps,
		ToolResultBudget:  fw.cfg.Execution.ToolResultBudget,
		CircuitBreaker:    fw.cfg.Execution.CircuitBreaker,
		HITLHandler:       fw.cfg.HITL,
		PreWarningPercent: fw.cfg.Execution.PreWarningPercent,
		ToolCache:         fw.toolCache,
	}

	return orchestration.NewConductor(conductorCfg), nil
}

// buildContextWindow creates a sdkmemory.ContextWindow for a step executor using
// the Framework's compaction, safety margin, and pruning configuration.
// Extracted for testability.
func (fw *Framework) buildContextWindow(sysPrompt string, meta llm.ModelMetadata, compactStrategy string, pruningOverrides ...orchestration.PruningOverride) agent.ContextManager {
	counter, err := llm.NewTokenCounter(meta.TokenizerType)
	if err != nil {
		counter = llm.NewSimpleTokenCounter()
	}
	tracker := llm.NewContextTokenTracker(counter)

	thresholds := sdkmemory.DefaultCompactionThresholds()
	if fw.cfg.Compaction.PredictivePercent > 0 {
		thresholds.PredictivePercent = fw.cfg.Compaction.PredictivePercent
	}
	if fw.cfg.Compaction.WarningPercent > 0 {
		thresholds.WarningPercent = fw.cfg.Compaction.WarningPercent
	}
	if fw.cfg.Compaction.EmergencyPercent > 0 {
		thresholds.EmergencyPercent = fw.cfg.Compaction.EmergencyPercent
	}

	strategy := sdkmemory.NewCompactionStrategy(compactStrategy, sdkmemory.CompactionConfig{
		SlidingWindow: struct{ KeepFirst, KeepLast int }{KeepFirst: 3, KeepLast: 10},
	}, sdkmemory.CompactionDeps{
		TokenCounter:       counter,
		MaxSummarizeTokens: 4000,
		Summarize:          nil, // summarization requires LLM caller; nil = sliding only
	})

	pruning := sdkmemory.DefaultToolOutputPruning()
	if len(pruningOverrides) > 0 {
		if pruningOverrides[0].KeepLastN > 0 {
			pruning.KeepLastN = pruningOverrides[0].KeepLastN
		}
		if pruningOverrides[0].ProtectedTools != nil {
			pruning.ProtectedTools = pruningOverrides[0].ProtectedTools
		}
	}

	return sdkmemory.NewContextWindow(sysPrompt, meta, tracker, thresholds, strategy, fw.cfg.Execution.SafetyMarginPercent, false, pruning)
}

// Execute is a convenience method that creates a Conductor and executes a
// single user message. Returns the execution result. For repeated use, call
// NewConductor() once and reuse it.
func (fw *Framework) Execute(ctx context.Context, systemPrompt orchestration.SystemPromptFactory, events agent.Events, userMessage string) (*orchestration.ExecutionResult, error) {
	conductor, err := fw.NewConductor(systemPrompt, events)
	if err != nil {
		return nil, err
	}
	defer conductor.Cleanup()

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(userMessage)
	availableTools := fw.tools.List()
	return conductor.Run(ctx, userMessage, bb, availableTools, events, "")
}

// Shutdown releases all resources held by the Framework (MCP connections, etc.).
// Safe to call multiple times.
func (fw *Framework) Shutdown() error {
	if fw.mcpGateway != nil {
		gw := fw.mcpGateway
		fw.mcpGateway = nil // ensure idempotency on repeated calls
		return gw.Stop()
	}
	return nil
}

// RestoreBlackboard loads a previously persisted blackboard state from the configured
// Checkpointer. Returns nil, nil if no checkpoint exists for the given ID.
// The returned CheckpointedBlackboard must be shut down by the caller after use.
func (fw *Framework) RestoreBlackboard(ctx context.Context, id string) (*orchestration.CheckpointedBlackboard, error) {
	if fw.cfg.Checkpointer == nil {
		return nil, errors.New("no Checkpointer configured")
	}
	return orchestration.RestoreBlackboard(ctx, id, fw.cfg.Checkpointer, fw.logger, 0)
}

// ToolRegistry returns the shared tool registry for direct tool registration.
func (fw *Framework) ToolRegistry() *tools.ToolRegistry {
	return fw.tools
}

// LLMRouter returns the shared LLM router for model switching at runtime.
func (fw *Framework) LLMRouter() *llm.Router {
	return fw.llmRouter
}
