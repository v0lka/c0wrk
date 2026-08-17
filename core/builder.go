package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// OrchestratorBuilder owns the shared tool registry, MCP gateway, and cached
// LLM router. It provides Build() to create per-session Orchestrators and
// exposes methods for runtime reconfiguration (judge, router, MCP, security).
//
// OrchestratorBuilder lives in core so that all sp4rk imports are confined to
// the core layer. The backend.Application wraps it without importing sp4rk.
type OrchestratorBuilder struct {
	mu       sync.RWMutex
	registry *tools.ToolRegistry
	gateway  *mcp.Gateway
	// sessionRegistries tracks the per-session registry clones created by
	// Build so runtime security-policy pushes (applySecurityPolicies) reach
	// already-open sessions, not only sessions built after the change.
	// Entries are added by registerSessionRegistry (Build) and removed by the
	// orchestrator's cleanup hook (OrchestratorDeps.OnCleanup), which the
	// session manager invokes on session delete and app shutdown. Guarded by
	// b.mu.
	sessionRegistries map[*tools.ToolRegistry]struct{}
	// mcpWorkDir is the default working directory requested for MCP stdio
	// server processes. It is applied to the gateway by runMCPInit (when the
	// gateway is first assigned) or by SetMCPWorkDir (when the gateway is
	// already assigned), using a record-and-apply pattern so SetMCPWorkDir
	// never blocks on network-bound MCP startup. Guarded by b.mu.
	mcpWorkDir       string
	llmRouter        *llm.Router
	modelRegistry    *llm.ModelRegistry
	logger           *slog.Logger
	vectorSearchFunc builtins.VectorSearchFunc
	baseSkillDirs    []string     // resolved skill directories shared across sessions (highest priority first)
	baseAgentDirs    []string     // resolved Subagent Profile directories shared across sessions (highest priority first)
	proxyClient      *http.Client // proxy-configured HTTP client (nil = direct connection)

	// Cached reasoning effort string. Empty unless seeded by the SmallLLM
	// sampling profile (applySmallLLMPresets); per-request overrides flow
	// through HandleOptions.ReasoningEffort → Orchestrator.SetReasoningEffort,
	// which propagates to router, planner, reflector, and the sp4rk P&E engine.
	reasoningEffort string

	// goAsync dispatches a fire-and-forget unit of background work. In
	// production it runs go fn(); tests override it (e.g. to run fn
	// synchronously) so code paths that launch detached goroutines — such as
	// buildLocalModelProbe — can be verified deterministically without timing
	// or polling. A nil field (direct struct construction) safely falls back
	// to real goroutines via asyncRunner.
	goAsync func(fn func())

	// Async initialization: LLM router and tool judge are initialized in the
	// background (gated by initDone) so that NewOrchestratorBuilder returns
	// immediately. MCP gateway startup is decoupled from initDone: it runs in
	// its own goroutine (gated by mcpDone) so that Build()/session restore is
	// not blocked on MCP server discovery, which can take seconds for remote
	// servers. MCP tools register into the shared sp4rk registry live, so
	// orchestrators built before MCP is ready simply don't advertise MCP tools
	// until the next message (graceful degradation).
	initDone   chan struct{}
	mcpDone    chan struct{}
	initErr    error
	gatewayErr error // non-nil if MCP gateway startup failed
}

func (b *OrchestratorBuilder) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// asyncRunner returns the background-work dispatcher. When b.goAsync is set
// (by tests) it is used directly; otherwise real goroutines are spawned. This
// indirection lets tests run detached work synchronously and deterministically.
func (b *OrchestratorBuilder) asyncRunner() func(func()) {
	if b.goAsync != nil {
		return b.goAsync
	}
	return func(fn func()) { go fn() }
}

// NewOrchestratorBuilder creates the shared infrastructure: tool registry,
// built-in tools, MCP gateway, LLM router, and tool judge.
// The cfg is used for initial setup; runtime changes are applied via the
// Rebuild* / Reconfigure* methods.
//
// MCP gateway and LLM router initialization happens asynchronously so that
// this function returns immediately. Callers that need those components
// (Build, GenerateTitle, etc.) block until the background init finishes.
func NewOrchestratorBuilder(cfg *BuilderConfig, askUserFunc tools.AskUserFunc, planApprovalFunc tools.ApprovalFunc, logger *slog.Logger) (*OrchestratorBuilder, error) {
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
		mcpDone:  make(chan struct{}),
	}

	// 0. Build proxy client (fast — no network, just config parsing)
	if cfg.Proxy.Enabled {
		proxyClient, err := proxy.BuildClient(cfg.Proxy, 30*time.Second, logger)
		if err != nil {
			logger.Warn("failed to build proxy client, proceeding without proxy", "error", err)
		} else {
			b.proxyClient = proxyClient
			// Opt-in global env mutation (W-12). SetGlobalEnv defaults to false;
			// only set when the user explicitly opts in via config.
			if cfg.Proxy.SetGlobalEnv {
				proxy.SetEnvVars(cfg.Proxy)
			}
		}
	}

	// 1. Tool registry + built-in tools (fast — synchronous)
	b.registry = tools.NewToolRegistry()

	toolsCfg := configToBuiltinToolsConfig(cfg)
	toolsCfg.AskUserFunc = askUserFunc
	toolsCfg.PlanApprovalFunc = planApprovalFunc
	toolsCfg.HTTPClient = b.proxyClient
	toolsCfg.Logger = logger
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

	// 2a. SmallLLM profile (fast — synchronous): caches the profile and seeds
	// the builder-level reasoning-effort default when the sampling variant is
	// active. Per-request overrides still win via ApplyRequestOverrides.
	b.applySmallLLMPresets(cfg)

	// 3. Start slow initialization asynchronously.
	// MCP gateway runs in its own goroutine (mcpDone), decoupled from initDone,
	// so Build()/session restore is not blocked on MCP server discovery.
	go b.runMCPInit(cfg)
	// LLM router + tool judge still gate initDone so Build() waits for them.
	go b.runAsyncInit(cfg)

	return b, nil
}

// runMCPInit starts the MCP gateway in a dedicated goroutine, decoupled from
// initDone. It registers MCP tools into the shared sp4rk registry, so
// orchestrators built before completion simply won't advertise MCP tools until
// the next message (graceful degradation). mcpDone is closed on exit,
// including when startup fails or panics, so waiters never block forever.
//
// Once the gateway is assigned, it applies any work directory recorded by
// SetMCPWorkDir during the startup window (record-and-apply), so a
// SetMCPWorkDir call that arrived before the gateway existed is not lost.
func (b *OrchestratorBuilder) runMCPInit(cfg *BuilderConfig) {
	defer close(b.mcpDone)
	defer func() {
		if r := recover(); r != nil {
			b.log().Error("MCP gateway panicked", "panic", r)
			b.mu.Lock()
			b.gatewayErr = fmt.Errorf("mcp gateway panic: %v", r)
			b.mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// MCP Gateway (optional — failures are non-fatal)
	mcpCfg := configToGatewayConfig(cfg)
	mcpCfg.HTTPClient = b.proxyClient
	gw, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		// MCP gateway failure is non-fatal: tools from MCP servers will be unavailable
		// but the orchestrator can still operate with built-in tools.
		b.log().Warn("MCP gateway startup failed", "error", err)
	}
	b.mu.Lock()
	b.gateway = gw
	b.gatewayErr = err
	// Apply a work dir recorded by SetMCPWorkDir before the gateway was
	// assigned (the startup window). This closes the race where SetMCPWorkDir
	// was called first: the field is now persisted and applied here.
	if gw != nil && b.mcpWorkDir != "" {
		gw.SetDefaultWorkDir(b.mcpWorkDir)
	}
	b.mu.Unlock()
}

// runAsyncInit performs the slow network-dependent initialization gated by
// initDone: LLM router creation and the tool judge. MCP gateway startup is
// handled separately by runMCPInit (decoupled, gated by mcpDone).
func (b *OrchestratorBuilder) runAsyncInit(cfg *BuilderConfig) {
	defer close(b.initDone)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
// Unlike WaitReady, this does NOT return initErr — it only waits for the init
// goroutine to finish. Callers that need the cached router (RebuildRouter,
// RebuildJudge, Build) should proceed with their own logic; Build constructs a
// fresh per-session router anyway, and RebuildRouter clears initErr on success.
func (b *OrchestratorBuilder) waitReady(ctx context.Context) error {
	select {
	case <-b.initDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitMCPReady blocks until the MCP gateway startup goroutine completes (or the
// context is cancelled). This is separate from waitReady/initDone because MCP
// startup is intentionally decoupled from Build()/restore: the gateway can take
// seconds to discover remote servers, and we don't want to block session
// restore on it. Methods that must observe the gateway's final state
// (MCPGateway, StopGateway, ReconfigureMCP) call this so the
// "MCP is still starting" race window is closed before they read/act on b.gateway.
// SetMCPWorkDir does NOT call this: it uses record-and-apply instead so it never
// blocks on MCP startup (see SetMCPWorkDir/runMCPInit docs).
func (b *OrchestratorBuilder) waitMCPReady(ctx context.Context) error {
	select {
	case <-b.mcpDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady blocks until async initialization completes or the context is cancelled.
// Returns the init error if the initial LLM router setup failed.
// Exported for use by the backend package.
//
// IMPORTANT: A nil return only guarantees the LLM router initialized successfully.
// The MCP gateway may have failed independently — check MCPGatewayError() if MCP
// tools are required. Gateway failures are intentionally non-fatal so the
// orchestrator can still operate with built-in tools when MCP servers are unavailable.
func (b *OrchestratorBuilder) WaitReady(ctx context.Context) error {
	select {
	case <-b.initDone:
		return b.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ToolRegistry returns the shared tool registry. The registry is set once during
// NewOrchestratorBuilder and never reassigned, so no lock is needed.
func (b *OrchestratorBuilder) ToolRegistry() *tools.ToolRegistry {
	return b.registry
}

// JudgeAvailable reports whether a strict OWASP ASI tool judge is configured on
// the shared registry. Returns false when the registry or judge is nil (e.g. no
// LLM model is available to power the judge). Used by the frontend to disable
// the Smart Approve toggle when the judge is not operational.
func (b *OrchestratorBuilder) JudgeAvailable() bool {
	if b.registry == nil {
		return false
	}
	return b.registry.GetJudge() != nil
}

// MCPGateway returns the MCP gateway, or nil if not started.
// Waits up to 30 seconds for the MCP startup goroutine to complete (note: MCP
// startup is decoupled from initDone/WaitReady, so it may still be in flight
// when initDone is closed). Returns nil if the gateway failed to start or was
// not configured.
//
// This is the BLOCKING variant, intended for non-UI consumers that must observe
// the gateway's final state (e.g. Shutdown, ReconfigureMCP, StopGateway). It
// MUST NOT be used on the UI path: a UI call during the first seconds of startup
// would stall behind MCP server discovery. UI callers — notably GetMCPStatus —
// use the non-blocking MCPStartupDone() + MCPGatewayNoWait() pair instead.
func (b *OrchestratorBuilder) MCPGateway() *mcp.Gateway {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.waitMCPReady(waitCtx)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.gateway
}

// MCPStartupDone reports whether the MCP gateway startup goroutine has finished
// (runMCPInit closed mcpDone). It is non-blocking — a select with a default
// fallback — and is intended for the UI status path (GetMCPStatus) so the
// settings dialog does not hang on the (potentially multi-second) MCP server
// discovery that happens during the first seconds of startup. Use it together
// with MCPGatewayNoWait to distinguish "still starting" from "started".
func (b *OrchestratorBuilder) MCPStartupDone() bool {
	select {
	case <-b.mcpDone:
		return true
	default:
		return false
	}
}

// WaitMCPStartup blocks until the MCP gateway startup goroutine finishes
// (runMCPInit closed mcpDone) or the context is cancelled. It is the BLOCKING
// counterpart to the non-blocking MCPStartupDone, intended for a one-shot
// notifier (e.g. the desktop layer emits EventMCPReady once startup completes
// so the settings dialog can refresh its transient "Starting…" placeholder).
// It returns nil as soon as startup is over — success or failure — so callers
// must then use MCPGatewayError()/MCPGatewayNoWait() to observe the outcome.
func (b *OrchestratorBuilder) WaitMCPStartup(ctx context.Context) error {
	select {
	case <-b.mcpDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MCPGatewayNoWait returns the MCP gateway WITHOUT blocking on mcpDone. It
// returns nil when startup is still in flight (mcpDone not yet closed) or when
// the gateway failed to start / was not configured. This is the non-blocking
// counterpart to MCPGateway, intended for callers on the UI path that must not
// stall (e.g. GetMCPStatus). Pair it with MCPStartupDone to distinguish the
// "still starting" state from "started and nil".
func (b *OrchestratorBuilder) MCPGatewayNoWait() *mcp.Gateway {
	select {
	case <-b.mcpDone:
	default:
		return nil
	}
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
	hitlHandler agent.HITLHandler,
	dumpWriter io.Writer,
	stepDumpTracker *orchestration.StepDumpTracker,
) (*Orchestrator, error) {
	// Wait for async initialization to complete before building an orchestrator.
	// Timing: waitReady blocks only for the LLM router + tool judge (runAsyncInit,
	// gated by initDone) — it does NOT wait for the MCP gateway, which runs in a
	// separate goroutine (runMCPInit, gated by mcpDone). MCP tools register into
	// the shared sp4rk registry live, so an orchestrator built before MCP is
	// ready simply won't advertise MCP tools until the next message (graceful
	// degradation), e.g. when the user creates a session shortly after app
	// launch, before the background router/judge init has closed initDone.
	buildStart := time.Now()
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitReadyStart := time.Now()
	if err := b.waitReady(waitCtx); err != nil {
		return nil, fmt.Errorf("orchestrator builder not ready: %w", err)
	}
	waitElapsed := time.Since(waitReadyStart)
	if waitElapsed > 50*time.Millisecond {
		b.log().Warn("build waited for async init", "elapsed_ms", waitElapsed.Milliseconds())
	}

	// Wrap emitter with logging
	emitter = NewLoggingEmitter(emitter, logger)

	// Build per-session LLM router + model registry
	routerStart := time.Now()
	llmRouter, modelReg, err := b.buildRouter(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM router: %w", err)
	}
	if d := time.Since(routerStart); d > 50*time.Millisecond {
		b.log().Warn("build_router slow", "elapsed_ms", d.Milliseconds())
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
	coreRouter, coreReflector, err := b.buildCoreAgents(trackingCaller, cfg, emitter, logger, dumpWriter)
	if err != nil {
		return nil, fmt.Errorf("building core agents: %w", err)
	}
	if coreRouter == nil {
		return nil, errors.New("orchestrator dependencies not initialized: LLM router or router is nil")
	}

	// Resolve reasoning effort for step executors
	reasoningEffort := b.reasoningEffort

	// Small-LLM context-management override: tightens compaction, tool-output
	// pruning, and the output token reserve when both the master toggle and the
	// context variant are enabled (no-op otherwise).
	exec := applyContextManagement(cfg.Executor, cfg.SmallLLM)

	// Build orchestrator config
	orchConfig := OrchestratorConfig{
		KeepFirst:                 exec.Compaction.SlidingWindow.KeepFirst,
		KeepLast:                  exec.Compaction.SlidingWindow.KeepLast,
		MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
		MaxRedelegationDepth:      cfg.Orchestration.MaxRedelegationDepth,
		// OrchestratorConfig.Model is used for model METADATA resolution
		// (ModelRegistry.Resolve keys on the bare model name), not for routing —
		// so strip any provider prefix from the router's composite active model.
		Model:                   llm.BareModel(llmRouter.ActiveModel()),
		ReasoningEffort:         reasoningEffort,
		HITLHandler:             hitlHandler,
		PreWarningPercent:       exec.Compaction.Thresholds.PreWarningPercent,
		InjectionDefenseEnabled: cfg.Security.InjectionDefenseEnabled,
		AgentsMDMaxBytes:        cfg.Security.AgentsMDMaxBytes,
		AgentsMDSearchPaths:     cfg.Security.AgentsMDSearchPaths,
		GoalLoop: GoalLoopSettings{
			Verification: cfg.GoalLoop.Verification,
		},
		SmallLLM: SmallLLMSettings{
			Enabled: cfg.SmallLLM.Enabled,
			EssentialTools: SmallLLMEssentialSettings{
				Enabled:       cfg.SmallLLM.EssentialTools.Enabled,
				AlwaysPresent: cfg.SmallLLM.EssentialTools.AlwaysPresent,
				MaxTools:      cfg.SmallLLM.EssentialTools.MaxTools,

				CompactDescriptions: cfg.SmallLLM.EssentialTools.CompactDescriptions,
			},
			SystemPrompt: SmallLLMSystemPromptSettings{
				Lite:              cfg.SmallLLM.SystemPrompt.Lite,
				FewShot:           cfg.SmallLLM.SystemPrompt.FewShot,
				ReasoningScaffold: cfg.SmallLLM.SystemPrompt.ReasoningScaffold,
			},
		},
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
	// SmallLLM loop hardening: when enabled, override the breaker thresholds
	// with the tighter SmallLLM values so a looping small model is caught
	// earlier. Thresholds absent from the profile keep their baseline.
	circuitBreaker = applyLoopHardening(circuitBreaker, cfg.SmallLLM)

	// Logged LLM caller for step execution (wraps trackingCaller)
	loggedLLM := agent.NewLoggingLLMCaller(trackingCaller, cfg.LLM.DefaultProviderName(), logger)
	loggedLLM = agent.NewDumpCaller(loggedLLM, dumpWriter, logger)

	// Per-session SkillManager: project-local `.agents/skills` is always prepended
	// (highest priority) to the shared base dirs. This must be built per-session
	// because the workspace path differs between concurrent sessions.
	sessionSkillMgr := b.buildSessionSkillManager(workspacePath, logger)

	// Per-session AgentManager: project-local `.agents/agents` is always prepended
	// (highest priority) to the shared base dirs. Built per-session because the
	// workspace path differs between concurrent sessions. Mirrors the skill
	// manager; nil-safe when no agent dirs are configured.
	sessionAgentMgr := b.buildSessionAgentManager(workspacePath, logger)

	// Per-session ToolRegistry clone: runtime policy mutations on a session
	// registry must NOT leak to other concurrent sessions. The clone shares
	// the underlying sp4rk ToolRegistry (tools themselves are stateless), but
	// each session has its own groupPolicies view. registerSessionRegistry
	// also records the clone as live so global security-policy pushes reach
	// this session without a restart; its entry is released by the cleanup
	// hook wired into OrchestratorDeps below.
	sessionRegistry := b.registerSessionRegistry()

	// HITLHandler.OnToolCall is invoked by the executor before every tool call
	// (see executor_run.go processSingleToolCall). PolicyUserConfirm tools fall
	// through to auto-execute when ConfirmFunc is nil (CLI-mode behavior), which
	// is the intended path — the HITL handler already intercepted at the executor
	// level. No explicit ConfirmFunc bridge is needed.

	totalElapsed := time.Since(buildStart)
	if totalElapsed > 100*time.Millisecond {
		b.log().Warn("orchestrator Build slow", "elapsed_ms", totalElapsed.Milliseconds(),
			"wait_ready_ms", waitElapsed.Milliseconds())
	}

	// Construct the lazy local-model probe for this session. It closes over the
	// per-session model registry so the discovered context window lands exactly
	// where Resolve will read it. The probe is a harmless no-op for cloud-only
	// setups (their /v1/models listing omits the context-window field).
	//
	// The onWindow callback pushes a late-arriving probe result into the
	// emitter's display window so the status bar corrects MID-task: the initial
	// context_fill (emitted at HandleMessage start, before the async probe
	// completes) carries the pre-probe window (catalog spec / fallback), and
	// without this refresh it would stay wrong for the entire first task.
	localProbe := b.buildLocalModelProbe(cfg, modelReg, func(model string, window int) {
		if setter, ok := emitter.(DisplayContextWindowForModelSetter); ok {
			setter.SetDisplayContextWindowForModel(model, window)
		}
	})
	// Probe the session's default model once at construction so the first
	// request benefits from the real context window if it is served by an
	// OpenAI-compatible endpoint.
	if defaultModel := llm.BareModel(llmRouter.ActiveModel()); defaultModel != "" {
		localProbe(defaultModel)
	}

	// Mechanical edit verification (executor.verify_on_edit): arm the hook
	// only when the user enabled it AND a workspace is active. The runner is
	// built exclusively from user config — the command never originates from
	// model output. Executed via ExecuteUnattended so group-deny and the
	// command blacklist still apply (see core/verify_on_edit.go).
	var verifyOnEditRunner agent.EditVerifyRunner
	if cfg.Executor.VerifyOnEdit.Enabled {
		switch {
		case strings.TrimSpace(cfg.Executor.VerifyOnEdit.Command) == "":
			// The command is documented as required when the feature is on;
			// an empty one would silently disable verification, so surface it.
			logger.Warn("verify-on-edit is enabled but executor.verify_on_edit.command is empty — verification is inactive until a command is configured")
		case workspacePath == "":
			logger.Debug("verify-on-edit enabled but no workspace is active — verification stays inactive for this orchestrator")
		}
		if workspacePath != "" {
			verifyOnEditRunner = buildEditVerifyRunner(
				sessionRegistry,
				workspacePath,
				cfg.Executor.VerifyOnEdit.Command,
				cfg.Executor.VerifyOnEdit.Timeout,
				time.Duration(cfg.Timeouts.BashMaxTimeout)*time.Second,
				logger,
			)
			if verifyOnEditRunner != nil {
				logger.Info("verify-on-edit enabled",
					"command", cfg.Executor.VerifyOnEdit.Command,
					"timeout", cfg.Executor.VerifyOnEdit.Timeout,
					"max_output_chars", cfg.Executor.VerifyOnEdit.MaxOutputChars)
			}
		}
	}

	return NewOrchestrator(orchConfig, OrchestratorDeps{
		Router:            coreRouter,
		LLM:               loggedLLM,
		ModelSwitcher:     llmRouter,
		ToolExec:          sessionRegistry,         // ToolExecutor (per-session policy view)
		ToolRegistry:      b.registry.ToolRegistry, // sp4rk ToolRegistry (shared)
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
		AgentManager:      sessionAgentMgr,
		CoreToolRegistry:  sessionRegistry, // per-session registry (No-Project tool disabling, extra shell blacklist)
		ToolCache:         toolCache,
		PerToolTruncation: perToolTruncation,
		StepDumpTracker:   stepDumpTracker,
		ProviderName:      cfg.LLM.DefaultProviderName(),
		LocalModelProbe:   localProbe,
		// Mechanical edit verification runner (nil = disabled). Inert in No
		// Project (CHAT) mode and goal-loop turns (see buildConductorDeps,
		// RunConductor, defaultGoalTurnRunner).
		VerifyOnEdit:               verifyOnEditRunner,
		VerifyOnEditMaxOutputChars: cfg.Executor.VerifyOnEdit.MaxOutputChars,
		// Release the session registry's live-tracking entry when the session
		// orchestrator is cleaned up, so security pushes stop reaching dead
		// clones and the builder does not accumulate registries forever.
		OnCleanup: func() { b.unregisterSessionRegistry(sessionRegistry) },
	}), nil
}

// RebuildRouter creates a new LLM router from the given config and caches it.
// This is called when LLM settings change at runtime.
// On success, clears initErr so that subsequent Build calls (session creation)
// can proceed even if the initial startup initialization failed.
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
	b.initErr = nil
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

// UpdateSecurityPolicies applies security policy overrides from config to
// the shared registry and every live session registry, so the change is in
// force for already-open sessions as well as future ones.
func (b *OrchestratorBuilder) UpdateSecurityPolicies(cfg *BuilderConfig) {
	b.applySecurityPolicies(cfg)
}

// UpdateShellBlacklist re-registers the shell-execution tool with the
// execute-group command blacklist from cfg. The blacklist is compiled into
// the tool instance at construction time, so runtime blacklist edits
// (security settings UI) require re-registration to take effect without an
// app restart. A compile failure leaves the previously registered tool in
// place and is returned to the caller.
func (b *OrchestratorBuilder) UpdateShellBlacklist(cfg *BuilderConfig) error {
	// Invariant: BuilderConfig.Security.Groups carries a complete execute
	// entry — ToBuilderConfig back-fills the shipped defaults for a missing
	// or nil blacklist, mirroring the config loader. A missing key here
	// would compile an EMPTY blacklist (fail-open), so hand-built configs
	// must populate it the same way.
	var blacklist []string
	if execGroup, ok := cfg.Security.Groups[string(sdktools.GroupExecute)]; ok {
		blacklist = execGroup.Blacklist
	}
	return tools.UpdateShellTool(b.registry, blacklist, builtins.BashTimeouts{
		MaxTimeout: time.Duration(cfg.Timeouts.BashMaxTimeout) * time.Second,
		WaitDelay:  time.Duration(cfg.Timeouts.BashWaitDelay) * time.Second,
	})
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
		proxy.ClearEnvVars()
	} else {
		proxyClient, err := proxy.BuildClient(cfg.Proxy, 30*time.Second, b.logger)
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
			proxy.SetEnvVars(cfg.Proxy)
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
		Temperature:     &temp, // explicit value wins over any profile
		ReasoningEffort: reasoningEffort,
		// Auxiliary text composition call — summarization class.
		CallPurpose: llm.CallPurposeSummarization,
	}
	caller := agent.LLMCaller(llmRouter)
	if dw := agent.DumpWriterFromContext(ctx); dw != nil {
		caller = agent.NewLoggingLLMCaller(caller, llmRouter.ActiveProviderName(), b.logger)
		caller = agent.NewDumpCaller(caller, dw, b.logger)
	}
	resp, err := caller.Call(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}

// commitMarkdownFenceRe matches a commit message that the model has wrapped
// in a single markdown code block, e.g.:
//
//	```
//	feat(auth): add token refresh
//	```
//
// or with an optional language tag such as "text"/"markdown":
//
//	```text
//	feat(auth): add token refresh
//	```
//
// The opening fence must be at the very start and the closing fence at the
// very end (after TrimSpace), so legitimate backticks inside a body are left
// untouched. The captured group holds the inner content.
//
// The (.+?) pattern (non-greedy) is used instead of (.*) so that multi-line
// messages with a body (blank line + paragraphs) are captured correctly even
// when the closing fence appears at the very end.
var commitMarkdownFenceRe = regexp.MustCompile("(?s)^```[a-zA-Z0-9+-]*[ \t]*\n(.+?)\n```[ \t]*$")

// commitMessageReasoningPrefixRe matches common reasoning/thinking prefixes
// that some LLMs (especially small models like Qwen) may accidentally include
// at the start of the content field.  The regex is case-insensitive and
// captures everything after the prefix so we can strip it.
var commitMessageReasoningPrefixRe = regexp.MustCompile(
	`(?i)^` +
		`(?:` +
		`based on my analysis(?:,| of|:) ` +
		`|here(?:['′]s|s) (?:the )?commit message(?:,|:) ` +
		`|the commit message(?:,| is|:) ` +
		`|sure,? ` +
		`|ok,? ` +
		`|ok sure,? ` +
		`|here(?:['′]s|s) an? ` +
		`|below(?:,| is|:) ` +
		`|according to my analysis ` +
		`|from the diff ` +
		`|from the provided diff ` +
		`|from the staged diff ` +
		`|this commit ` +
		`)`,
)

// conventionalCommitRe validates that a string follows the Conventional Commits
// format: <type>[optional scope]: <description>.
// Types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert.
// The (?s) flag makes . match newlines so multi-line messages (with body) are
// accepted — we only care that the first line starts with a valid type prefix.
var conventionalCommitRe = regexp.MustCompile(
	`(?s)^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]*\))?: .+$`,
)

// isValidConventionalCommit checks whether msg starts with a valid Conventional
// Commits type prefix followed by a colon and a non-empty description in
// lowercase. The lowercase requirement applies only to the first character of
// the description line (proper nouns like "GitHub" in the middle are allowed).
func isValidConventionalCommit(msg string) bool {
	if !conventionalCommitRe.MatchString(msg) {
		return false
	}
	// Extract the description portion (everything after "<type>(scope): ").
	// Only the first character of the description must be lowercase.
	idx := strings.IndexByte(msg, ':')
	if idx < 0 {
		return false
	}
	descLine := msg[idx+1:]
	if nl := strings.IndexByte(descLine, '\n'); nl >= 0 {
		descLine = descLine[:nl]
	}
	descLine = strings.TrimSpace(descLine)
	if descLine == "" {
		return false
	}
	return descLine[0] >= 'a' && descLine[0] <= 'z'
}

// stripMarkdownCodeFence removes a single surrounding markdown code block from
// s if the entire (trimmed) string is wrapped in one. It is a defensive
// safety net for commit-message generation: the prompt already forbids
// fencing, but some models still emit it. Input without fencing is returned
// unchanged apart from leading/trailing whitespace trimming.
func stripMarkdownCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if m := commitMarkdownFenceRe.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1])
	}
	return trimmed
}

// extractCommitMessage extracts the best available commit message text from an
// LLM response. It tries fields in order of preference:
//
//  1. resp.Message.Content (after stripping markdown fences and reasoning prefixes)
//  2. resp.Message.ReasoningContent (for DeepSeek-style providers)
//  3. resp.Reasoning (for OpenAI Responses API)
//
// This handles the failure mode where small models (especially Qwen) put the
// actual commit message into a reasoning field instead of Content.
func extractCommitMessage(resp *llm.ChatResponse) string {
	if resp == nil {
		return ""
	}

	// Collect candidates in priority order.
	candidates := []string{
		resp.Message.Content,
		resp.Message.ReasoningContent,
		resp.Reasoning,
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		// Strip markdown fencing first.
		candidate = stripMarkdownCodeFence(candidate)
		if candidate == "" {
			continue
		}
		// Strip common reasoning/thinking prefixes that some models
		// (especially small ones) may accidentally include.
		candidate = commitMessageReasoningPrefixRe.ReplaceAllString(candidate, "")
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

// optimizedPromptReasoningPrefixRe matches common reasoning/thinking prefixes
// that some LLMs (especially small models) may accidentally include at the
// start of the content field when generating an optimized prompt.  The regex
// is case-insensitive and captures everything after the prefix so we can strip
// it.
var optimizedPromptReasoningPrefixRe = regexp.MustCompile(
	`(?i)^` +
		`(?:` +
		`here(?:['′]s|s) (?:the )?(?:optimized )?prompt(?:,|:) ` +
		`|the optimized prompt(?:,| is|:) ` +
		`|based on my analysis(?:,| of|:) ` +
		`|sure,? ` +
		`|ok,? ` +
		`|ok sure,? ` +
		`|here(?:['′]s|s) an? ` +
		`|below(?:,| is|:) ` +
		`|according to my analysis ` +
		`|this is the optimized prompt ` +
		`|this prompt ` +
		`)`,
)

// --- Prompt optimization extraction markers ---

// optimizedPromptMarkerStart / optimizedPromptMarkerEnd are the exact strings
// the rewrite prompt instructs the LLM to use as unambiguous boundaries around
// the optimized prompt.  extractOptimizedPrompt searches for these markers
// first; only when they are absent does it fall back to the old heuristic.
const (
	optimizedPromptMarkerStart = "### OPTIMIZED_PROMPT_START"
	optimizedPromptMarkerEnd   = "### OPTIMIZED_PROMPT_END"
)

// extractOptimizedPrompt extracts the optimized prompt text from an LLM
// response.  It uses a two-phase strategy:
//
//  1. Marker-based extraction (preferred): searches all candidate fields
//     (Content, ReasoningContent, Reasoning) for the
//     OPTIMIZED_PROMPT_START / OPTIMIZED_PROMPT_END markers.  If both
//     markers are present in any field, the text between them is returned.
//  2. Heuristic fallback (legacy): when no markers are found at all, falls
//     back to stripping markdown fences and common reasoning prefixes.
//
// This ensures that reasoning-model outputs containing the markers are
// extracted unambiguously, while older models that don't use markers still
// work via the heuristic fallback.
func extractOptimizedPrompt(resp *llm.ChatResponse) string {
	if resp == nil {
		return ""
	}

	// Collect candidates in priority order.
	candidates := []string{
		resp.Message.Content,
		resp.Message.ReasoningContent,
		resp.Reasoning,
	}

	// Phase 1 — marker-based extraction.
	// Search all candidates for the marker pair. Return the first match.
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if extracted, ok := extractBetweenMarkers(candidate); ok {
			// Markers were found — return whatever is between them
			// (may be empty).  Do NOT fall through to heuristic.
			return extracted
		}
	}

	// Phase 2 — heuristic fallback (legacy, for models that don't use markers).
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		candidate = stripMarkdownCodeFence(candidate)
		if candidate == "" {
			continue
		}
		candidate = optimizedPromptReasoningPrefixRe.ReplaceAllString(candidate, "")
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

// extractBetweenMarkers searches s for optimizedPromptMarkerStart and
// optimizedPromptMarkerEnd and returns the text between them.  It returns the
// text and true when both markers are present (even if the content between
// them is empty or whitespace-only); otherwise it returns ("", false).
func extractBetweenMarkers(s string) (string, bool) {
	startIdx := strings.Index(s, optimizedPromptMarkerStart)
	if startIdx < 0 {
		return "", false
	}
	endIdx := strings.Index(s[startIdx:], optimizedPromptMarkerEnd)
	if endIdx < 0 {
		return "", false
	}
	// Content starts after the start marker.
	contentStart := startIdx + len(optimizedPromptMarkerStart)
	// Content ends at the start of the end marker.
	contentEnd := startIdx + endIdx
	extracted := strings.TrimSpace(s[contentStart:contentEnd])
	return extracted, true
}

// buildCommitMessageRequest constructs a commit-message request without forcing
// sampling parameters. The request declares a summarization purpose, so the
// router applies the deterministic profile (temperature 0 or the family-safe
// floor) instead of the vendor preset — only when the active model advertises
// temperature support; reasoning models such as GPT-5/o-series and
// kimi-k2-thinking therefore receive no unsupported temperature field.
func buildCommitMessageRequest(diff, extraUserText, reasoningEffort string) llm.ChatRequest {
	// Reasoning models count reasoning tokens against the output-token budget.
	// 2048 comfortably covers reasoning plus a short Conventional Commits
	// message while still capping runaway output. Non-reasoning models stop
	// naturally well before this.
	const commitMsgMaxTokens = 2048

	userContent := "## Staged Diff\n\n" + diff
	if extraUserText != "" {
		userContent += "\n\n" + extraUserText
	}

	return llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.CommitMessage},
			{Role: "user", Content: userContent},
		},
		MaxTokens:       commitMsgMaxTokens,
		ReasoningEffort: reasoningEffort,
		// Auxiliary text composition call — deterministic profile.
		CallPurpose: llm.CallPurposeSummarization,
	}
}

// GenerateCommitMessage produces a Conventional Commits-formatted commit
// message from the given staged diff using the cached LLM router. The diff
// is typically the output of `git diff --staged`. The caller is responsible
// for enforcing any request timeout via the supplied context.
func (b *OrchestratorBuilder) GenerateCommitMessage(ctx context.Context, diff string) (string, error) {
	if err := b.waitReady(ctx); err != nil {
		b.log().Warn("commit message generation aborted: builder not ready",
			"err", err, "diff_bytes", len(diff))
		return "", err
	}

	b.mu.RLock()
	llmRouter := b.llmRouter
	reasoningEffort := b.reasoningEffort
	b.mu.RUnlock()

	if llmRouter == nil {
		b.log().Error("commit message generation failed: llm router not available",
			"diff_bytes", len(diff))
		return "", errors.New("llm router not available")
	}

	caller := agent.LLMCaller(llmRouter)
	if dw := agent.DumpWriterFromContext(ctx); dw != nil {
		caller = agent.NewLoggingLLMCaller(caller, llmRouter.ActiveProviderName(), b.logger)
		caller = agent.NewDumpCaller(caller, dw, b.logger)
	}
	providerName := llmRouter.ActiveProviderName()
	b.log().Debug("generating commit message",
		"provider", providerName, "diff_bytes", len(diff))

	buildRequest := func(extraUserText string) llm.ChatRequest {
		return buildCommitMessageRequest(diff, extraUserText, reasoningEffort)
	}

	return b.generateCommitMessageWithCaller(ctx, caller, providerName, diff, buildRequest)
}

// generateCommitMessageWithCaller runs the retry loop for commit message
// generation. It is a separate method so that tests can inject a mock
// LLMCaller and verify retry behavior deterministically.
func (b *OrchestratorBuilder) generateCommitMessageWithCaller(
	ctx context.Context,
	caller agent.LLMCaller,
	providerName string,
	diff string,
	buildRequest func(extraUserText string) llm.ChatRequest,
) (string, error) {
	// Attempt generation with up to 2 retries if the first result is not a
	// valid Conventional Commits message.
	const maxRetries = 2
	var lastInvalidMsg string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var extraUserText string
		if attempt > 0 && lastInvalidMsg != "" {
			extraUserText = "PREVIOUS ATTEMPT FAILED VALIDATION\n\n" +
				"The previous output did not follow the Conventional Commits format.\n" +
				"Here is what was produced (DO NOT repeat this format):\n" +
				"```\n" + lastInvalidMsg + "\n```\n\n" +
				"Your output MUST start with a valid type prefix: feat, fix, docs, " +
				"style, refactor, perf, test, build, ci, chore, or revert.\n" +
				"Example: feat(auth): add token validation\n\n" +
				"DO NOT prefix with phrases like 'this commit', 'Here is the commit message:', " +
				"'Based on my analysis:', or similar."
		}

		req := buildRequest(extraUserText)
		resp, err := caller.Call(ctx, req)
		if err != nil {
			// Classify the failure so operators can distinguish a too-large
			// staged diff (context window) from a slow or unresponsive
			// provider (deadline) or a provider-side error. This is the
			// single place the LLM-side cause is logged; the backend RPC
			// layer only logs its own preconditions and passes this through.
			switch {
			case errors.Is(err, llm.ErrContextWindowExceeded):
				b.log().Error("commit message generation failed: staged diff exceeds model context window",
					"err", err, "diff_bytes", len(diff), "provider", providerName)
			case errors.Is(err, context.DeadlineExceeded):
				b.log().Error("commit message generation failed: LLM call timed out",
					"err", err, "diff_bytes", len(diff), "provider", providerName)
			default:
				b.log().Error("commit message generation failed: LLM call error",
					"err", err, "diff_bytes", len(diff), "provider", providerName)
			}
			return "", err
		}
		if resp == nil {
			b.log().Warn("commit message generation returned empty response",
				"diff_bytes", len(diff), "provider", providerName)
			return "", nil
		}

		// Extract the best available commit message from the response.
		// This handles the failure mode where small models (especially Qwen)
		// put the actual commit message into a reasoning field instead of
		// Content, and also strips common reasoning prefixes.
		message := extractCommitMessage(resp)
		if message == "" {
			// The LLM call succeeded (no error) but produced no usable text.
			// This is the failure mode that previously surfaced as a silent
			// no-op in the UI: with a reasoning model, a too-small output
			// budget is consumed by reasoning tokens and the model emits no
			// text content (status=incomplete, reason=max_output_tokens).
			// Surface it explicitly instead of returning an empty string so
			// the UI reports an error and operators see a log entry.
			hasReasoning := resp.Message.ReasoningContent != "" || resp.Reasoning != ""
			b.log().Warn("commit message generation produced no usable output",
				"diff_bytes", len(diff), "provider", providerName,
				"stop_reason", resp.StopReason, "has_reasoning", hasReasoning)
			if hasReasoning {
				return "", errors.New("the model produced no commit message text " +
					"(its output budget was likely consumed by reasoning); " +
					"try a non-reasoning model, a smaller staged diff, or a larger model")
			}
			return "", errors.New("the model produced an empty commit message; " +
				"try again or use a different model")
		}

		// Validate Conventional Commits format.
		if isValidConventionalCommit(message) {
			// Success — valid format.
			return message, nil
		}

		// Invalid format — store for retry feedback.
		lastInvalidMsg = message
		b.log().Debug("commit message validation failed, will retry",
			"diff_bytes", len(diff), "provider", providerName,
			"attempt", attempt+1, "raw", message)
	}

	// All attempts exhausted — return an error with the last invalid output.
	b.log().Warn("commit message generation failed validation after all retries",
		"diff_bytes", len(diff), "provider", providerName,
		"last_output", lastInvalidMsg)
	return "", errors.New("the model produced an invalid commit message after " +
		"multiple attempts; try a different model or reduce the staged diff size")
}

// ListProviderModels returns available model names for a given provider.
func (b *OrchestratorBuilder) ListProviderModels(ctx context.Context, provider string, cfg *BuilderConfig) ([]string, error) {
	switch provider {
	case "anthropic":
		return llm.BuiltInModelNames("anthropic-api"), nil
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
	default:
		// Type-based dispatch: look up the provider by name, then dispatch on ProviderType.
		pc, ok := cfg.LLM.ProviderConfigs[provider]
		if !ok {
			return nil, fmt.Errorf("unknown provider: %s", provider)
		}
		switch pc.ProviderType {
		case "openai":
			baseURL := cfg.ExpandEnvVars(pc.BaseURL)
			apiKey := cfg.ExpandEnvVars(pc.APIKey)
			if baseURL == "" {
				return nil, fmt.Errorf("openAI-compatible base URL not configured for provider %q", provider)
			}
			return listOpenAIModels(ctx, baseURL, apiKey)
		case "anthropic":
			baseURL := cfg.ExpandEnvVars(pc.BaseURL)
			// Fixed "anthropic" provider (no BaseURL): return the built-in
			// Claude model list. An anthropic_compatible entry (non-empty
			// BaseURL) queries the custom endpoint's /v1/models and falls
			// back to the built-in list on any error so the UI degrades
			// gracefully when the endpoint is unreachable or non-standard.
			if baseURL == "" {
				return llm.BuiltInModelNames("anthropic-api"), nil
			}
			apiKey := cfg.ExpandEnvVars(pc.APIKey)
			names, err := listAnthropicModels(ctx, baseURL, apiKey, b.proxyClient)
			if err != nil {
				b.log().Warn("anthropic-compatible model listing failed; falling back to built-in list",
					"provider", provider, "base_url", baseURL, "error", err)
				return llm.BuiltInModelNames("anthropic-api"), nil
			}
			return names, nil
		default:
			return nil, fmt.Errorf("unsupported provider type %q for provider %q", pc.ProviderType, provider)
		}
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

// SetAgentDirs sets the base (shared) Subagent Profile discovery directories,
// applied to every session. Paths must already be absolute and expanded; the
// backend layer owns path resolution (home/~, env vars, relative-to-agent-dir).
//
// The project-local `<workspacePath>/.agents/agents` directory is always
// prepended automatically in GetAgentDescriptors — do NOT include it here.
func (b *OrchestratorBuilder) SetAgentDirs(dirs []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baseAgentDirs = append([]string(nil), dirs...)
}

// GetBaseAgentDirs returns a copy of the base (shared) Subagent Profile
// discovery directories.
func (b *OrchestratorBuilder) GetBaseAgentDirs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.baseAgentDirs...)
}

// GetAgentDescriptors returns lightweight Subagent Profile descriptors from
// the shared base dirs and an optional project-local agent directory. This is
// used by the frontend ListAgents API to avoid creating a full AgentManager
// per call. Mirrors GetSkillDescriptors.
func (b *OrchestratorBuilder) GetAgentDescriptors(projectAgentDir string) []agents.AgentDescriptor {
	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseAgentDirs...)
	b.mu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if projectAgentDir != "" {
		dirs = append(dirs, projectAgentDir)
	}
	dirs = append(dirs, baseDirs...)

	if len(dirs) == 0 {
		return nil
	}

	am := agents.NewAgentManager(dirs, b.log())
	if err := am.Scan(); err != nil {
		b.log().Warn("GetAgentDescriptors scan failed", "error", err, "dirs", dirs)
	}
	return am.List()
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
		dirs = append(dirs, filepath.Join(workspacePath, SkillsRelativePath))
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

// buildSessionAgentManager constructs a per-session AgentManager that always
// scans the current project's `.agents/agents` (when workspacePath is set) in
// addition to the shared base dirs. Scan errors are logged and do not abort
// session start-up. Mirrors buildSessionSkillManager for Subagent Profiles.
func (b *OrchestratorBuilder) buildSessionAgentManager(workspacePath string, logger *slog.Logger) *agents.AgentManager {
	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseAgentDirs...)
	b.mu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if workspacePath != "" {
		dirs = append(dirs, filepath.Join(workspacePath, AgentsRelativePath))
	}
	dirs = append(dirs, baseDirs...)

	if len(dirs) == 0 {
		return nil
	}

	am := agents.NewAgentManager(dirs, logger)
	if err := am.Scan(); err != nil {
		if logger != nil {
			logger.Warn("session agent scan failed", "error", err, "dirs", dirs)
		} else {
			b.log().Warn("session agent scan failed", "error", err, "dirs", dirs)
		}
	}
	return am
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

	// Only config.yaml-derived user overrides are seeded into the registry at
	// construction (Resolution tier 1). LM Studio / local-server context-window
	// discovery is performed lazily per-session (see Build + buildLocalModelProbe)
	// and written into the registry via SetRuntimeMetadata (tier 1.5, ABOVE the
	// built-in catalog) so the server's observed runtime window beats the
	// catalog spec, while an explicit config.yaml value still wins.
	//
	// Entries are seeded PARTIAL: only the fields the user actually set are
	// carried (unset scalars stay zero/empty = inherit), and the registry's
	// enrichPartialOverride fills the rest at Resolve time from the tiers
	// below (observed runtime -> built-in catalog -> cache -> fallback).
	// Merging ResolveBuiltInModel values HERE — as an earlier version did —
	// pins the catalog window (262144) or the fallback (128000) into tier 1,
	// permanently shadowing both the lazy server probe and the model's real
	// non-standard window: a user override pinning only the output limit still
	// carried a wrong context window at tier 1.
	//
	// Capabilities is overridden atomically via its pointer: nil = inherit
	// (the registry fills the effective set from the tiers below), non-nil =
	// authoritative — including an all-false set, which is exactly why the
	// field is a pointer (a value struct could not distinguish "user disabled
	// everything" from "user set nothing"). There is no per-flag partial
	// override, matching the dialog's "submit the full capability set" UX.
	// TokenizerType/Family/Protocol are string sentinels — empty = inherit
	// (DetectProtocol/resolveFamily derive the effective value at Resolve
	// time), non-empty = authoritative override.
	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range cfg.LLM.Models {
		entry := llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: override.TokenizerType,
			Family:        override.Family,
			Protocol:      llm.APIProtocol(override.Protocol),
			Capabilities:  override.Capabilities,
		}
		overrides[name] = entry
	}

	// Auto-remap the Google protocol for Gemma/Gemini checkpoints served by a
	// local OpenAI-compatible server (LM Studio/vLLM/Ollama). These servers
	// expose /v1/chat/completions (and the /v1/responses, /v1/messages
	// delegates) but NOT Google's :generateContent endpoint — which they
	// answer with a misleading 200 OK + empty body (see the bug log). Remapping
	// only the Google protocol → chat_completions keeps the request on an
	// endpoint the server actually serves, while GPT-5 (Responses) and Claude
	// (Anthropic) keep working unchanged. An explicit protocol override seeded
	// above from cfg.LLM.Models always wins and is never clobbered here.
	remapLocalGoogleProtocols(overrides, cfg.LLM.ProviderConfigs, cfg.ExpandEnvVars)

	// Per-provider output-token reserve: seed ModelMetadata.OutputLimit for
	// every model of a provider that sets output_token_reserve. The registry
	// uses OutputLimit both as the context-window reserve and as the executor
	// MaxTokens ceiling, so a provider-level budget raises the generation
	// ceiling for all of its models at once. Priority: per-model llm.models
	// output_limit > per-provider output_token_reserve > global
	// executor.output_token_reserve (the RouterConfig fallback).
	applyProviderOutputReserves(overrides, cfg.LLM.ProviderConfigs)

	modelRegistry := llm.NewModelRegistry(overrides)
	if proxyClient != nil {
		modelRegistry.SetHTTPClient(proxyClient)
	}

	// Construct a dedicated HTTP client for LLM inference calls. Reasoning
	// models can take several minutes to respond, so the LLM timeout must be
	// much longer than the proxy/tools client (30s). Without this, the SDK
	// falls back to http.DefaultClient which has NO timeout — a stalled
	// upstream hangs the session indefinitely.
	llmClient := buildLLMHTTPClient(proxyClient, cfg.Timeouts.LLMRequestTimeout)
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
	providerOrder := []string{"anthropic", "chatgpt"}
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

	// Small-LLM context-management override: keeps the router's token budget
	// (safety margin, output reserve) in sync with the executor tightening
	// applied in Build (no-op unless both toggles are enabled).
	exec := applyContextManagement(cfg.Executor, cfg.SmallLLM)

	routerCfg := llm.RouterConfig{
		Providers:           providers,
		MaxRetries:          cfg.LLM.Retry.MaxRetries,
		InitialBackoff:      initialBackoff,
		MaxBackoff:          maxBackoff,
		SafetyMarginPercent: exec.Compaction.SafetyMarginPercent,
		OutputTokenReserve:  exec.OutputTokenReserve,
		HTTPClient:          llmClient,
		Logger:              b.logger,
		SamplingFunc:        resolveSamplingFunc(cfg.SmallLLM),
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

// applyProviderOutputReserves seeds ModelMetadata.OutputLimit into the model
// overrides for every model served by a provider that sets a per-provider
// output_token_reserve. The router treats a model's OutputLimit both as the
// reserve subtracted from the context window during overflow validation and as
// the executor's MaxTokens ceiling, so one provider-level knob raises the
// generation budget for all of that provider's models at once — the right
// granularity for self-hosted gateways (LM Studio, vLLM) whose real limits
// differ from the built-in catalog.
//
// Priority: an explicit per-model llm.models output_limit always wins and is
// never clobbered; models without any per-provider seeding keep inheriting the
// global executor.output_token_reserve that llm.NewRouter applies as the
// last-resort fallback. Unset (0) and negative provider values are ignored.
// When two providers list the SAME bare model name with different reserves,
// the lexicographically first provider name wins — iteration is over sorted
// provider names, so the outcome does not depend on Go's randomized map
// iteration order. The function is idempotent.
func applyProviderOutputReserves(
	overrides map[string]llm.ModelMetadata,
	providerConfigs map[string]BuilderProviderConfig,
) {
	names := make([]string, 0, len(providerConfigs))
	for name := range providerConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pc := providerConfigs[name]
		if pc.OutputTokenReserve <= 0 {
			continue
		}
		for _, model := range pc.Models {
			if model == "" {
				continue
			}
			entry, ok := overrides[model]
			if ok && entry.OutputLimit > 0 {
				continue // explicit per-model override (or an earlier provider) wins
			}
			entry.OutputLimit = pc.OutputTokenReserve
			overrides[model] = entry
		}
	}
}

// remapLocalGoogleProtocols rewrites the built-in Google protocol to
// ProtocolChatCompletions for Google-named checkpoints (Gemma / Gemini) that
// are served by an OpenAI-compatible server (LM Studio, vLLM, Ollama, a
// self-hosted gateway on a public host).
//
// Per the LM Studio endpoint matrix, OpenAI-compatible self-hosted servers
// expose /v1/chat/completions, /v1/responses and /v1/messages — but NOT
// Google's /models/{model}:generateContent endpoint, which they answer with a
// 200 OK + empty body. So a Google-named model must be steered onto
// chat_completions instead. Only the Google protocol is remapped: Responses
// (GPT-5/Codex) and Anthropic (Claude) are served fine by these servers, so
// they are left untouched. The override is keyed by the bare model name — the
// same value that becomes req.Model on the wire.
//
// The injected override is PROTOCOL-ONLY: it pins Protocol=ChatCompletions and
// carries the built-in Capabilities (so multimodal flag is preserved), but
// leaves ContextWindow / OutputLimit / TokenizerType at their zero values. The
// ModelRegistry then inherits those unset scalars from its lower non-network
// tiers at Resolve time (observed runtime -> built-in catalog -> cache ->
// fallback). This is essential because the lazy model probe
// (buildLocalModelProbe) writes the model's REAL runtime context window via
// SetRuntimeMetadata; a wholesale override that also carried the
// catalog/fallback window (128000 for a catalog miss) would permanently shadow
// that probe result, leaving the context-fill accounting and compaction
// thresholds pinned to an inflated window.
//
// An explicit protocol override seeded from cfg.LLM.Models (already present in
// overrides with a non-empty protocol) is respected: the user's choice always
// wins and is never clobbered. The remap is idempotent, so map-iteration order
// across providers does not affect the result.
func remapLocalGoogleProtocols(
	overrides map[string]llm.ModelMetadata,
	providerConfigs map[string]BuilderProviderConfig,
	expandEnv func(string) string,
) {
	for _, pc := range providerConfigs {
		if pc.ProviderType != "openai" {
			continue
		}
		for _, model := range pc.Models {
			// Respect an explicit user override (cfg.LLM.Models) that already
			// set a protocol — never clobber the user's choice.
			if existing, ok := overrides[model]; ok && existing.Protocol != "" {
				continue
			}
			base, _ := llm.ResolveBuiltInModel(model)
			if base.Protocol == llm.ProtocolGoogle {
				// Protocol-only override: the registry inherits the unset
				// scalar fields (context window, output limit, tokenizer) from
				// its lower tiers so the lazy probe result takes effect. See
				// the function doc comment for the shadowing rationale.
				overrides[model] = llm.ModelMetadata{
					Protocol:     llm.ProtocolChatCompletions,
					Capabilities: base.Capabilities,
				}
			}
		}
	}
}

// buildLocalModelProbe returns a LocalModelProbe that, for the given model,
// locates its OpenAI-compatible provider and fires an asynchronous
// context-window probe whose result is written into the per-session model
// registry via SetRuntimeMetadata.
//
// Any OpenAI-compatible provider is probed — local/LAN (LM Studio), a
// self-hosted server on a public host (vLLM/TGI/Ollama behind a domain or
// Tailscale), and even a genuine cloud provider. The probe is harmless for the
// cloud case: the standard /v1/models listing of a real cloud API omits the
// per-model context-window field, so no window is discovered and the registry
// keeps its built-in spec. Non-OpenAI providers and models not found in any
// provider config are silent no-ops (the closure returns without spawning a
// goroutine).
//
// The probe tries the LM Studio native endpoint first (runtime/loaded window)
// and falls back to the standard OpenAI /v1/models listing (max_model_len) —
// see probeSelfHostedContextWindow.
//
// The network probe runs on a detached goroutine (dispatched via b.asyncRunner)
// with a fresh context.Background() (bounded to 3s inside each probe) so it is
// not tied to the caller's request lifetime and never blocks HandleMessage /
// session creation. Results land in the registry's observed-runtime tier
// (Resolution 1.5): above the built-in catalog so the server's enforced
// runtime window supersedes the checkpoint spec, below a config.yaml override
// (tier 1) so the user's explicit choice always wins. onWindow, when non-nil,
// is invoked on the same goroutine with the discovered window, letting the
// caller refresh user-facing display state (the status bar's context max)
// mid-task. Tests override asyncRunner to run the probe synchronously, making
// assertions deterministic without polling.
func (b *OrchestratorBuilder) buildLocalModelProbe(cfg *BuilderConfig, registry *llm.ModelRegistry, onWindow func(model string, window int)) LocalModelProbe {
	// Snapshot proxyClient under read lock once at probe construction so the
	// closure does not touch b.mu on every invocation.
	b.mu.RLock()
	proxyClient := b.proxyClient
	b.mu.RUnlock()
	log := b.log()
	expand := cfg.ExpandEnvVars
	goRun := b.asyncRunner()

	return func(model string) {
		if model == "" || registry == nil {
			return
		}
		baseURL, apiKey, ok := lookupOpenAIProviderBaseURL(cfg, model, expand)
		if !ok {
			return
		}
		goRun(func() {
			window, err := probeSelfHostedContextWindow(context.Background(), baseURL, apiKey, model, proxyClient)
			if err != nil {
				log.Warn("lazy model probe failed", "model", model, "base_url", baseURL, "error", err)
			}
			if window <= 0 {
				return
			}
			// SetRuntimeMetadata writes to Resolution tier 1.5 — ABOVE the
			// built-in catalog and the lazy cache, BELOW a config.yaml
			// override (tier 1) — so the user's explicit choice is never
			// clobbered. A tier-3 write (SetCachedMetadata) is NOT enough:
			// self-hosted servers routinely serve well-known checkpoints at a
			// runtime context length far below the catalog maximum (LM Studio
			// loads models at a user-chosen length; the catalog says 262144),
			// and the catalog tier would permanently shadow the observed
			// runtime window, pinning compaction budgets and the status-bar
			// max to a window the server will never honor.
			//
			// OutputLimit mirrors the model registry's built-in fallback
			// (tier 5: 32768) so self-hosted models are not regressed — neither
			// LM Studio nor vLLM expose a per-model output cap in their model
			// listings — but is clamped to at most a quarter of the discovered
			// window. An OutputLimit larger than the context window drives
			// EffectiveMax negative and disables compaction (CheckFill reports
			// "reject"), so without the clamp a small-context model
			// (7B/13B commonly run at 8K/16K/32K) would grow unbounded until
			// the API rejects it.
			//
			// TokenizerType is deliberately left empty: the probe observes
			// only the window, and a synthetic "approximate" here would sit in
			// tier 1.5 — ABOVE the built-in catalog — shadowing the exact
			// tokenizer a well-known checkpoint carries there.
			registry.SetRuntimeMetadata(model, llm.ModelMetadata{
				ContextWindow: window,
				OutputLimit:   min(32768, window/4),
			})
			log.Debug("lazy model probe populated context window",
				"model", model, "context_window", window)
			if onWindow != nil {
				onWindow(model, window)
			}
		})
	}
}

// lookupOpenAIProviderBaseURL searches the provider configs for the
// OpenAI-compatible one that serves `model` and returns its expanded base_url +
// api key. The second return is false only when no OpenAI-compatible provider
// serves the model. Host locality is deliberately NOT filtered: a self-hosted
// server on a public host (vLLM/TGI/Ollama behind a domain or Tailscale) is
// probed exactly like a local one — the probe is a harmless no-op for a
// genuine cloud provider whose /v1/models listing omits the context-window
// field.
func lookupOpenAIProviderBaseURL(cfg *BuilderConfig, model string, expand func(string) string) (baseURL, apiKey string, ok bool) {
	for _, pc := range cfg.LLM.ProviderConfigs {
		if pc.ProviderType != "openai" {
			continue
		}
		enabled := false
		for _, m := range pc.Models {
			if m == model {
				enabled = true
				break
			}
		}
		if !enabled {
			continue
		}
		raw := expand(pc.BaseURL)
		if raw == "" {
			continue
		}
		return raw, expand(pc.APIKey), true
	}
	return "", "", false
}

// buildLLMHTTPClient creates an *http.Client dedicated to LLM inference
// calls. It always sets a finite Timeout so a stalled upstream cannot hang
// the session indefinitely. When proxyClient is non-nil, its transport
// (proxy, TLS, dialer settings) is reused with the LLM-specific timeout.
func buildLLMHTTPClient(proxyClient *http.Client, timeoutSec int) *http.Client {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	client := &http.Client{Timeout: timeout}
	if proxyClient != nil && proxyClient.Transport != nil {
		client.Transport = proxyClient.Transport
	}
	return client
}

// buildCoreAgents creates the core Router and Reflector.
func (b *OrchestratorBuilder) buildCoreAgents(
	caller agent.LLMCaller,
	cfg *BuilderConfig,
	emitter Emitter,
	logger *slog.Logger,
	dumpWriter io.Writer,
) (*router.Router, *reflector.Reflector, error) {
	if caller == nil {
		return nil, nil, nil
	}
	providerName := cfg.LLM.DefaultProviderName()
	loggedCaller := agent.NewLoggingLLMCaller(caller, providerName, logger)
	loggedCaller = agent.NewDumpCaller(loggedCaller, dumpWriter, logger)
	coreRouter := newCoreRouter(loggedCaller, cfg.Router.HistoryWindow)
	coreReflector := newCoreReflector(loggedCaller)

	coreRouter.SetReasoningEffort(b.reasoningEffort)
	coreReflector.SetReasoningEffort(b.reasoningEffort)

	// Enable semantic tool selection in the router when the SmallLLM master
	// toggle and the EssentialTools variant are both active. When enabled, the
	// router prompt includes a tool-selection instruction and the matched_tools
	// field in its JSON output schema; the conductor then narrows its advertised
	// tool set accordingly.
	coreRouter.SetToolMatching(cfg.SmallLLM.Enabled && cfg.SmallLLM.EssentialTools.Enabled)

	return coreRouter, coreReflector, nil
}

// buildContextFactory creates a ContextManagerFactory using the tracking caller for
// compaction summarization (ensuring those tokens are counted in session totals).
func (b *OrchestratorBuilder) buildContextFactory(caller *llm.TrackingCaller, cfg *BuilderConfig, modelRegistry *llm.ModelRegistry, dumpWriter io.Writer) ContextManagerFactory {
	var summarizeCaller agent.LLMCaller = caller
	summarizeCaller = agent.NewDumpCaller(summarizeCaller, dumpWriter, b.logger)
	compactionEffort := b.reasoningEffort

	// Small-LLM context-management override: tightens the compaction strategy
	// and tool-output pruning baselines when both the master toggle and the
	// context variant are enabled. Per-step pruning overrides (below) still
	// take precedence over the tightened baseline.
	exec := applyContextManagement(cfg.Executor, cfg.SmallLLM)

	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager {
		counter, err := llm.NewTokenCounter(modelMeta.TokenizerType)
		if err != nil {
			b.log().Warn("token counter fallback", "tokenizer", modelMeta.TokenizerType, "error", err)
			counter = llm.NewSimpleTokenCounter()
		}
		tracker := llm.NewContextTokenTracker(counter)

		strategy := sdkmemory.NewCompactionStrategy(compactionStrategy, sdkmemory.CompactionConfig{
			SlidingWindow: struct{ KeepFirst, KeepLast int }{
				KeepFirst: exec.Compaction.SlidingWindow.KeepFirst,
				KeepLast:  exec.Compaction.SlidingWindow.KeepLast,
			},
			Summarization: struct {
				BlockSize           int
				KeepLast            int
				ObservationTruncate int
			}{
				BlockSize:           exec.Compaction.Summarization.BlockSize,
				KeepLast:            exec.Compaction.Summarization.KeepLast,
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
					// Compaction summaries are deterministic calls: no vendor
					// preset, temperature pinned to the family-safe floor.
					CallPurpose: llm.CallPurposeCompaction,
				}
				resp, err := summarizeCaller.Call(ctx, req)
				if err != nil {
					return "", fmt.Errorf("compaction summarize: %w", err)
				}
				return resp.Message.Content, nil
			},
		})

		thresholds := sdkmemory.CompactionThresholds{
			PredictivePercent: exec.Compaction.Thresholds.PredictivePercent,
			WarningPercent:    cfg.Executor.Compaction.Thresholds.WarningPercent,
			EmergencyPercent:  cfg.Executor.Compaction.Thresholds.EmergencyPercent,
		}

		pruning := sdkmemory.ToolOutputPruning{
			KeepLastN:        exec.ToolOutputPruning.KeepLastN,
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

		cw := sdkmemory.NewContextWindow(sdkmemory.ContextWindowConfig{
			SystemPrompt:            systemPrompt,
			ModelMeta:               modelMeta,
			Tracker:                 tracker,
			Thresholds:              thresholds,
			Strategy:                strategy,
			SafetyMarginPercent:     cfg.Executor.Compaction.SafetyMarginPercent,
			InjectionDefenseEnabled: cfg.Security.InjectionDefenseEnabled,
			Pruning:                 pruning,
		})
		cw.SetHistoryMutation(sdkmemory.HistoryMutation{
			ToolResultEvictionStep: cfg.Executor.HistoryMutation.ToolResultEvictionStep,
			EvictStepStatus:        cfg.Executor.HistoryMutation.EvictStepStatus,
			DedupRepeatedReads:     cfg.Executor.HistoryMutation.DedupRepeatedReads,
			Logger:                 b.logger,
		})
		return NewCoreContextManager(cw)
	}
}

// rebuildJudgeInternal recreates the ToolJudge and sets it on the registry.
//
// The judge calls the active provider DIRECTLY (bypassing the Router), so the
// model it sends must be the bare model name — the provider prefix is stripped
// from any composite identifier (DefaultModel may be "provider/model").
func (b *OrchestratorBuilder) rebuildJudgeInternal(cfg *BuilderConfig, llmRouter *llm.Router) {
	if b.registry == nil {
		return
	}

	// The judge sends the model name straight to the provider API, so it must be
	// the bare model name (no "provider/" prefix).
	defaultModel := llm.BareModel(cfg.LLM.DefaultModel)
	if llmRouter != nil {
		defaultModel = llm.BareModel(llmRouter.ActiveModel())
	}
	judgeModel := llm.BareModel(cfg.Security.JudgeModel)

	var judgeProvider llm.Provider
	if llmRouter != nil {
		judgeProvider = llmRouter.DefaultProvider()
	} else {
		// Try building a fresh router
		newRouter, _, err := b.buildRouter(context.Background(), cfg)
		if err == nil && newRouter != nil {
			judgeProvider = newRouter.DefaultProvider()
		} else if err != nil && b.logger != nil {
			b.logger.Warn("rebuildJudge: failed to build LLM router for judge", "error", err)
		}
	}

	debugEnabled := b.logger != nil && b.logger.Enabled(context.Background(), slog.LevelDebug)
	judgeProvider = newJudgeDumpProvider(judgeProvider, b.logger, cfg.LLM.DefaultProviderName(), debugEnabled)

	judge := sdktools.NewToolJudgeFromConfig(sdktools.JudgeConfig{
		Model:        judgeModel,
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

// judgeDumpProvider wraps an llm.Provider to add context-aware LLM dump support.
// When a DumpWriter exists in the context, it wraps the LLM call with
// agent.NewDumpCaller + agent.NewLoggingLLMCaller for DEBUG-level observability.
type judgeDumpProvider struct {
	inner        llm.Provider
	logger       *slog.Logger
	providerName string
}

func (p *judgeDumpProvider) ChatCompletion(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	caller := agent.LLMCaller(&providerAsLLMCaller{p: p.inner})
	if dw := agent.DumpWriterFromContext(ctx); dw != nil {
		caller = agent.NewLoggingLLMCaller(caller, p.providerName, p.logger)
		caller = agent.NewDumpCaller(caller, dw, p.logger)
	}
	return caller.Call(ctx, req)
}

func (p *judgeDumpProvider) Name() string { return p.inner.Name() }

// providerAsLLMCaller adapts llm.Provider to agent.LLMCaller so it can be
// wrapped with agent.NewDumpCaller and agent.NewLoggingLLMCaller.
type providerAsLLMCaller struct{ p llm.Provider }

func (a *providerAsLLMCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.p.ChatCompletion(ctx, req)
}

// newJudgeDumpProvider wraps a provider for context-aware dump support.
// Returns inner unchanged if logger is nil, providerName is empty, or debug is disabled.
func newJudgeDumpProvider(inner llm.Provider, logger *slog.Logger, providerName string, debugEnabled bool) llm.Provider {
	if inner == nil || logger == nil || providerName == "" || !debugEnabled {
		return inner
	}
	return &judgeDumpProvider{inner: inner, logger: logger, providerName: providerName}
}

// registerSessionRegistry clones the shared registry for a new session and
// records the clone as live. The clone and the insert happen atomically under
// b.mu so a concurrent applySecurityPolicies push cannot slip between them:
// either the clone is created after the parent registry was updated (it
// inherits the new security state) or it is already tracked here and receives
// the push. The entry is released by unregisterSessionRegistry via the
// orchestrator's cleanup hook.
func (b *OrchestratorBuilder) registerSessionRegistry() *tools.ToolRegistry {
	b.mu.Lock()
	defer b.mu.Unlock()
	clone := b.registry.Clone()
	if b.sessionRegistries == nil {
		b.sessionRegistries = make(map[*tools.ToolRegistry]struct{})
	}
	b.sessionRegistries[clone] = struct{}{}
	return clone
}

// unregisterSessionRegistry removes a session registry from the live set. It
// is the cleanup hook wired into every orchestrator built by Build so tracked
// clones do not outlive their sessions.
func (b *OrchestratorBuilder) unregisterSessionRegistry(r *tools.ToolRegistry) {
	b.mu.Lock()
	delete(b.sessionRegistries, r)
	b.mu.Unlock()
}

// applySecurityPolicies applies group-based security policies to the tool
// registry: every non-system tool resolves its policy from its capability
// group (the sdktools.Group* values), never from its name. The reserved
// system group is not configurable and any entry for it is skipped
// defensively; unknown group names are likewise skipped.
//
// The state is pushed to the shared registry AND every live per-session
// registry clone: each session executes on its own clone (see Build), so a
// runtime edit from the security settings UI must reach already-open sessions
// too — otherwise a deny set in the UI would silently fail-open on every
// session created before the save (the same save's execute blacklist does
// reach them, because it re-registers the tool in the shared sp4rk registry
// the clones embed). The push holds b.mu across the whole update so a Build
// racing it cannot miss the new state (see registerSessionRegistry).
func (b *OrchestratorBuilder) applySecurityPolicies(cfg *BuilderConfig) {
	groupPolicies := make(map[sdktools.ToolGroup]sdktools.ToolPolicy, len(cfg.Security.Groups))
	for name, group := range cfg.Security.Groups {
		g := sdktools.ToolGroup(name)
		if g == sdktools.GroupSystem || !sdktools.IsValidToolGroup(g) {
			if b.logger != nil {
				b.logger.Warn("ignoring unconfigurable or unknown security group", "group", name)
			}
			continue
		}
		groupPolicies[g] = parseGroupPolicy(group.Policy)
	}

	autoApprove := cfg.Security.AutoApproveWorkspaceWrites
	smartApprove := cfg.Security.SmartApprove

	// Lock ordering is b.mu → registry mu: registerSessionRegistry clones
	// under b.mu (same order), and the registries never call back into the
	// builder, so no reverse order exists.
	b.mu.Lock()
	b.registry.ApplySecurityState(groupPolicies, autoApprove, smartApprove)
	for r := range b.sessionRegistries {
		r.ApplySecurityState(groupPolicies, autoApprove, smartApprove)
	}
	b.mu.Unlock()
}

// parseGroupPolicy maps the short config enum used by
// security.groups.<group>.policy ("allow" | "user_confirm" | "deny", see the
// backend/config GroupPolicy* constants) onto sdktools policy values. The
// mapping lives here because core never imports backend/config. Anything
// unrecognized — including an empty value — fails safe to user confirmation.
func parseGroupPolicy(policy string) sdktools.ToolPolicy {
	switch policy {
	case "allow":
		return sdktools.PolicyAlwaysAllow
	case "deny":
		return sdktools.PolicyAlwaysDeny
	default: // "user_confirm", "", unknown → fail safe
		return sdktools.PolicyUserConfirm
	}
}

// applySmallLLMPresets seeds the builder-level reasoning-effort default when
// the SmallLLM sampling variant is active. The remaining per-variant effects
// (loop hardening, sampling temperature) are applied lazily in Build() and
// buildRouter() via the pure helpers applyLoopHardening and
// resolveSamplingFunc, which read the profile straight from the passed-in
// *BuilderConfig. When the master toggle is off this is a no-op, so behavior
// is identical to the un-profiled baseline.
func (b *OrchestratorBuilder) applySmallLLMPresets(cfg *BuilderConfig) {
	// When the sampling variant is active and supplies a reasoning effort, use
	// it as the builder-level default. Per-request overrides
	// (HandleOptions.ReasoningEffort → ApplyRequestOverrides →
	// SetReasoningEffort) still take precedence at request time.
	if cfg.SmallLLM.Enabled && cfg.SmallLLM.Sampling.Enabled && cfg.SmallLLM.Sampling.ReasoningEffort != "" {
		b.reasoningEffort = cfg.SmallLLM.Sampling.ReasoningEffort
	}
}

// applyLoopHardening overrides circuit-breaker thresholds with the tighter
// SmallLLM loop-hardening values when the variant is enabled (and the master
// toggle is on). Only the thresholds present in the profile are overridden;
// all others (RepeatAbortThreshold, TruncationAbortThreshold, etc.) keep their
// baseline. When the variant is disabled the breaker is returned unchanged.
func applyLoopHardening(cb agent.CircuitBreakerConfig, s BuilderSmallLLMConfig) agent.CircuitBreakerConfig {
	if !s.Enabled || !s.LoopHardening.Enabled {
		return cb
	}
	lh := s.LoopHardening
	cb.RepeatNudgeThreshold = lh.RepeatNudgeThreshold
	cb.ParseErrorAbortThreshold = lh.ParseErrorAbortThreshold
	cb.FruitlessNudgeThreshold = lh.FruitlessNudgeThreshold
	cb.FruitlessAbortThreshold = lh.FruitlessAbortThreshold
	cb.SameToolRepeatNudgeThreshold = lh.SameToolRepeatNudgeThreshold
	return cb
}

// applyContextManagement tightens the executor's context-management knobs —
// compaction (sliding-window keep-last, summarization block size, predictive
// trigger), tool-output pruning depth, and the output token reserve — when the
// SmallLLM context variant is enabled (and the master toggle is on). Each knob
// is overridden independently: a zero value in the profile means "keep the
// executor baseline" for that knob. When the variant is disabled the executor
// config is returned byte-for-byte unchanged.
func applyContextManagement(exec BuilderExecutorConfig, s BuilderSmallLLMConfig) BuilderExecutorConfig {
	if !s.Enabled || !s.Context.Enabled {
		return exec
	}
	c := s.Context
	if c.Compaction.KeepLast > 0 {
		exec.Compaction.SlidingWindow.KeepLast = c.Compaction.KeepLast
	}
	if c.Compaction.BlockSize > 0 {
		exec.Compaction.Summarization.BlockSize = c.Compaction.BlockSize
	}
	if c.Compaction.TriggerPercent > 0 {
		exec.Compaction.Thresholds.PredictivePercent = c.Compaction.TriggerPercent
	}
	if c.ToolOutputKeepLastN > 0 {
		exec.ToolOutputPruning.KeepLastN = c.ToolOutputKeepLastN
	}
	if c.OutputTokenReserve > 0 {
		exec.OutputTokenReserve = c.OutputTokenReserve
	}
	return exec
}

// resolveSamplingFunc returns the SamplingFunc for the LLM router. The
// per-family vendor matrix preset (prompt.DefaultSampling) is always the
// base. When the sampling variant is enabled (and the SmallLLM master toggle
// is on), only the fields the user set explicitly (non-zero) override the
// preset; every unset field inherits the vendor value. A fully-unset profile
// therefore reproduces the vendor preset exactly — enabling the variant alone
// never degrades a family to hardcoded constants such as a constant
// temperature, which previously broke vendor-tuned 27-30B model presets.
//
// prompt.SamplingConfig is converted field-by-field into llm.SamplingDefaults
// because the llm package cannot import prompt (it would create an import
// cycle through prompt's in-package tests). MaxTokens is deliberately not
// forwarded: the router-level preset is sampling-only.
func resolveSamplingFunc(s BuilderSmallLLMConfig) llm.SamplingFunc {
	override := s.Enabled && s.Sampling.Enabled
	return func(family string) llm.SamplingDefaults {
		c := prompt.DefaultSampling(family)
		d := llm.SamplingDefaults{
			Temperature:       c.Temperature,
			TopP:              c.TopP,
			TopK:              c.TopK,
			RepetitionPenalty: c.RepetitionPenalty,
			PresencePenalty:   c.PresencePenalty,
		}
		if !override {
			return d
		}
		// Zero means "not set" — inherit the vendor preset instead of
		// clobbering it (see BuilderSmallLLMSampling field docs).
		if s.Sampling.Temperature > 0 {
			d.Temperature = &s.Sampling.Temperature
		}
		if s.Sampling.TopP > 0 {
			d.TopP = &s.Sampling.TopP
		}
		if s.Sampling.TopK > 0 {
			d.TopK = &s.Sampling.TopK
		}
		if s.Sampling.RepetitionPenalty > 0 {
			d.RepetitionPenalty = &s.Sampling.RepetitionPenalty
		}
		return d
	}
}

// ---------------------------------------------------------------------------
// Config conversion helpers
// ---------------------------------------------------------------------------

// configToBuiltinToolsConfig converts BuilderConfig to BuiltinToolsConfig.
func configToBuiltinToolsConfig(cfg *BuilderConfig) tools.BuiltinToolsConfig {
	// The command blacklist is sourced from the execute group
	// (security.groups.execute.blacklist) and compiled into the shell-exec
	// tool, whose Judge reports a match as a hard escalation naming the
	// pattern. Presence-based: an explicitly empty blacklist ([] in YAML)
	// clears the patterns, while an absent group leaves them unset (the
	// config loader back-fills defaults, so this only affects programmatically
	// built configs).
	var shellBlacklist []string
	if execGroup, ok := cfg.Security.Groups[string(sdktools.GroupExecute)]; ok {
		shellBlacklist = execGroup.Blacklist
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
		ShellBlacklist: shellBlacklist,
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

// listAnthropicModels fetches model names from an Anthropic-compatible API by
// performing a raw HTTP GET to {baseURL}/v1/models (the go-anthropic SDK does
// not expose a ListModels method). baseURL may or may not end with "/v1"; the
// path is normalized. An optional proxy-configured httpClient is honored. The
// "x-api-key" and "anthropic-version" headers are sent when apiKey is non-empty.
func listAnthropicModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) ([]string, error) {
	// Normalize the URL: ensure it ends with "/v1/models".
	endpoint := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/models"
	} else {
		endpoint += "/v1/models"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build models request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "application/json")

	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic-compatible models endpoint returned %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read models response: %w", err)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	names := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	sort.Strings(names)
	return names, nil
}
