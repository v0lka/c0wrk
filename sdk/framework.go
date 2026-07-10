// Package sp4rk is the entry point for the sp4rk Agent SDK — a standalone Go framework
// for building AI agent systems with Plan & Execute orchestration, tool integration,
// and multi-provider LLM support.
//
// Quick start:
//
//	fw, _ := sp4rk.New(sp4rk.Config{
//	    LLM: sp4rk.LLMConfig{
//	        Providers: []llm.ProviderEntry{{
//	            Name: "anthropic", ProviderType: "anthropic",
//	            APIKey: os.Getenv("ANTHROPIC_API_KEY"), Models: []string{"claude-sonnet-4-5"},
//	        }},
//	    },
//	})
//	defer fw.Shutdown()
//
//	result, _ := fw.Execute(ctx, mySystemPrompt, myEvents, "Write a hello world in Go")
//
// # Fluent API
//
// The same framework is reachable through a concise method-chain (fluent) API. The
// fluent entry points carry an "F" postfix to distinguish them from the classic
// methods on the shared [Framework] type; once inside a builder, methods keep
// their natural names so the chain reads fluently:
//
//	fw, _ := sp4rk.NewF().
//	    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
//	    FileTools().
//	    AutoApprove().
//	    Build()
//	defer fw.Shutdown()
//
//	// fw.RunF — single ReAct loop (fluent counterpart of Execute).
//	result, _ := fw.RunF(ctx).
//	    System("You are a helpful assistant.").
//	    Ask("Write a hello world in Go")
//
//	// fw.TaskF — Plan → Execute → Reflect orchestration.
//	result, _ = fw.TaskF(ctx, task).
//	    System("You are a task execution agent.").
//	    Plan().Reflect().Execute()
//
// [NewF] returns a [FrameworkBuilder]; [Framework.RunF] and [Framework.TaskF]
// start the single-loop and orchestration builders. The classic and fluent APIs
// are fully interoperable — both produce and operate on the same [*Framework].
// For the layer map, before/after comparisons, and when to reach for classic
// escapes, see the Fluent API guide at docs/fluent-api.md.
package sp4rk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// Framework is the top-level entry point for building agent systems with sp4rk.
// It owns shared infrastructure (LLM router, tool registry, MCP gateway, tool cache) and
// creates per-session conductors via NewConductor().
type Framework struct {
	cfg          Config
	llmRouter    *llm.Router
	tools        *tools.ToolRegistry
	modelReg     *llm.ModelRegistry
	mcpGateway   *mcp.Gateway
	toolCache    *agent.ToolResultCache
	logger       *slog.Logger
	shutdownOnce sync.Once
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

	// ConfirmFunc is the confirmation callback the tool registry consults
	// before executing tools whose effective policy is PolicyUserConfirm
	// (e.g. write_file, bash_exec). The registry is FAIL-CLOSED: when no
	// ConfirmFunc is configured, such tools are denied instead of executing
	// silently. To run without interactive confirmation, either provide a
	// ConfirmFunc that auto-approves, or relax individual tools via
	// ToolRegistry().SetPolicyOverride(name, tools.PolicyAlwaysAllow).
	//
	// Note: this is a separate, lower-level gate than HITL.OnToolCall.
	// HITL intercepts tool calls at the executor level for every tool;
	// ConfirmFunc is consulted by the registry only for confirm-policy tools.
	ConfirmFunc tools.ConfirmFunc

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
	// 0 means use the default (3); negative means explicitly 0 (no retries).
	MaxRetries int

	// InitialBackoff is the starting backoff duration for retries.
	// Empty string means use the default (1s); a negative duration (e.g.
	// "-1s") means explicitly 0 (no initial backoff).
	InitialBackoff string

	// MaxBackoff is the maximum backoff duration for retries.
	// Empty string means use the default (30s); a negative duration means
	// explicitly 0 (no backoff cap).
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
	// 0 means use the default (50); negative means explicitly 0 (no loop
	// iterations — effectively disabled).
	MaxSteps int

	// MaxRetries is the maximum number of retry attempts per plan step.
	// 0 means use the default (2); negative means explicitly 0 (no retries).
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
	// Strategy is the compaction algorithm: "sliding_window", "summarization",
	// or "hierarchical". Empty means "sliding_window". Unrecognised values fall
	// back to sliding window (see memory.NewCompactionStrategy).
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

// resolveIntSentinel implements the documented zero-value convention for
// numeric config fields:
//
//	value == 0 → use def (zero value means "unset")
//	value <  0 → explicitly 0 / disabled
//	value >  0 → use as-is
func resolveIntSentinel(value, def int) int {
	switch {
	case value == 0:
		return def
	case value < 0:
		return 0
	default:
		return value
	}
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

	// Apply defaults for zero-value fields. Negative values are documented
	// sentinels meaning "explicitly 0/disabled" (see field doc comments).
	cfg.Execution.MaxSteps = resolveIntSentinel(cfg.Execution.MaxSteps, 50)
	cfg.Execution.MaxRetries = resolveIntSentinel(cfg.Execution.MaxRetries, 2)
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
		cfg.Compaction.Strategy = "sliding_window"
	}

	// Parse retry durations. Sentinel values (0 → default, negative →
	// explicitly 0/disabled) are resolved by llm.NewRouter — pass through.
	initialBackoff, err := time.ParseDuration(cfg.LLM.InitialBackoff)
	if err != nil && cfg.LLM.InitialBackoff != "" {
		logger.Warn("invalid InitialBackoff, using default", "value", cfg.LLM.InitialBackoff, "error", err)
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.MaxBackoff)
	if err != nil && cfg.LLM.MaxBackoff != "" {
		logger.Warn("invalid MaxBackoff, using default", "value", cfg.LLM.MaxBackoff, "error", err)
	}
	maxRetries := cfg.LLM.MaxRetries
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
	fw.tools.SetLogger(logger)
	if cfg.ConfirmFunc != nil {
		fw.tools.SetConfirmFunc(cfg.ConfirmFunc)
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
// Lifecycle events are supplied per-run via Conductor.Run (nil = no-op emitter).
//
// The Conductor is a single ReAct loop that owns a task end-to-end. The
// caller is responsible for injecting any Conductor-specific tools (delegate,
// declare_plan, reflect) into the context before calling Run, if desired.
// The sp4rk Conductor primitive itself does not provide those tools; they are
// an application-layer concern.
func (fw *Framework) NewConductor(systemPrompt orchestration.SystemPromptFactory) (*orchestration.Conductor, error) {
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

	return sdkmemory.NewContextWindow(sdkmemory.ContextWindowConfig{
		SystemPrompt:        sysPrompt,
		ModelMeta:           meta,
		Tracker:             tracker,
		Thresholds:          thresholds,
		Strategy:            strategy,
		SafetyMarginPercent: fw.cfg.Execution.SafetyMarginPercent,
		Pruning:             pruning,
	})
}

// Execute is a convenience method that creates a Conductor and executes a
// single user message. Returns the execution result. For repeated use, call
// NewConductor() once and reuse it.
//
// When Config.Checkpointer is set, the blackboard is a CheckpointedBlackboard
// (persisted through the checkpointer, keyed by a unique per-execution ID) and
// Config.OnBlackboardChanged (if set) receives change notifications. Without a
// Checkpointer the blackboard is in-memory only and OnBlackboardChanged is not
// invoked.
func (fw *Framework) Execute(ctx context.Context, systemPrompt orchestration.SystemPromptFactory, events agent.Events, userMessage string) (*orchestration.ExecutionResult, error) {
	conductor, err := fw.NewConductor(systemPrompt)
	if err != nil {
		return nil, err
	}
	defer conductor.Cleanup()

	var bb orchestration.Blackboard
	if fw.cfg.Checkpointer != nil {
		id := fmt.Sprintf("execute-%d", time.Now().UnixNano())
		cb := orchestration.NewCheckpointedBlackboard(id, fw.cfg.Checkpointer, fw.logger, 0)
		if fw.cfg.OnBlackboardChanged != nil {
			cb.SetOnChanged(fw.cfg.OnBlackboardChanged)
		}
		defer cb.Shutdown()
		bb = cb
	} else {
		bb = orchestration.NewMapBlackboard()
	}
	bb.SetOriginalRequest(userMessage)
	availableTools := fw.tools.List()
	return conductor.Run(ctx, userMessage, bb, availableTools, events, "")
}

// Shutdown releases all resources held by the Framework (MCP connections, etc.).
// Safe to call multiple times, including concurrently: sync.Once guarantees
// the underlying shutdown runs exactly once.
func (fw *Framework) Shutdown() error {
	var err error
	fw.shutdownOnce.Do(func() {
		if fw.mcpGateway != nil {
			gw := fw.mcpGateway
			fw.mcpGateway = nil
			err = gw.Stop()
		}
	})
	return err
}

// RestoreBlackboard loads a previously persisted blackboard state from the configured
// Checkpointer. Returns nil, nil if no checkpoint exists for the given ID.
// The returned CheckpointedBlackboard must be shut down by the caller after use.
func (fw *Framework) RestoreBlackboard(ctx context.Context, id string) (*orchestration.CheckpointedBlackboard, error) {
	if fw.cfg.Checkpointer == nil {
		return nil, errors.New("no Checkpointer configured")
	}
	pb, err := orchestration.RestoreBlackboard(ctx, id, fw.cfg.Checkpointer, fw.logger, 0)
	if err != nil || pb == nil {
		return pb, err
	}
	if fw.cfg.OnBlackboardChanged != nil {
		pb.SetOnChanged(fw.cfg.OnBlackboardChanged)
	}
	return pb, nil
}

// ToolRegistry returns the shared tool registry for direct tool registration.
func (fw *Framework) ToolRegistry() *tools.ToolRegistry {
	return fw.tools
}

// LLMRouter returns the shared LLM router for model switching at runtime.
func (fw *Framework) LLMRouter() *llm.Router {
	return fw.llmRouter
}

// ContextFactory returns a ContextManagerFactory wired with the Framework's
// compaction, safety margin, and pruning configuration. Use it to give the
// Planner (or any other component that needs a ContextManager) the same
// context-window behaviour as the Framework-built Conductors.
//
// The returned factory accepts a system prompt, model metadata, compaction
// strategy name, and optional pruning overrides — matching the
// orchestration.ContextManagerFactory signature.
func (fw *Framework) ContextFactory() orchestration.ContextManagerFactory {
	return fw.buildContextWindow
}
