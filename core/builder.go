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

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/c0wrk/core/llmtls"
	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/oneshot"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// OrchestratorBuilder owns the shared tool registry, MCP gateway, and cached
// LLM router: Build() creates per-session Orchestrators and methods apply
// runtime reconfiguration (judge, router, MCP, security). It lives in core so
// all sp4rk imports stay confined to the core layer — the backend wraps it
// without importing sp4rk (specs/architecture/layers.md).
type OrchestratorBuilder struct {
	mu       sync.RWMutex
	mcpModes map[string]CapabilityModes // immutable snapshots published under mu
	// skillModes/agentModes: capability snapshots for the skill and agent
	// catalogs, same immutable-snapshot pattern as mcpModes, read only via the
	// cloning currentSkillModes/currentAgentModes getters.
	skillModes map[string]CapabilityModes
	agentModes map[string]CapabilityModes
	// reconfigureMu serializes ReconfigureMCP end to end: two concurrent
	// callers that both find no gateway would each dial one and orphan the
	// loser's stdio subprocesses. Holding it also keeps the gateway snapshot
	// stable (the gateway serializes its own Reconfigure calls anyway).
	// Acquired BEFORE mu, never the reverse; no config-read path takes it.
	// Startup (runMCPInit) runs once, gated by mcpDone, and does not join.
	reconfigureMu sync.Mutex
	registry      *tools.ToolRegistry
	gateway       *mcp.Gateway
	// sessionRegistries tracks per-session registry clones from Build so
	// runtime security-policy pushes reach already-open sessions. Added by
	// registerSessionRegistry (Build), removed by the orchestrator cleanup
	// hook (OnCleanup) on session delete/app shutdown. Guarded by b.mu.
	sessionRegistries map[*tools.ToolRegistry]struct{}
	// sessionModelRegistries tracks per-session model registries from
	// buildRouter so runtime metadata pushes (UpdateModelOverrides — the
	// embedded LLM context read-back being the motivating correction) reach
	// already-open sessions. Same add/remove lifecycle as sessionRegistries.
	// Guarded by b.mu.
	sessionModelRegistries map[*llm.ModelRegistry]struct{}
	// mcpWorkDir is the default working directory for MCP stdio processes,
	// applied by runMCPInit (gateway first assigned) or SetMCPWorkDir (already
	// assigned) — record-and-apply so SetMCPWorkDir never blocks on
	// network-bound MCP startup. Guarded by b.mu.
	mcpWorkDir    string
	llmRouter     *llm.Router
	modelRegistry *llm.ModelRegistry
	logger        *slog.Logger
	// serviceMetrics collects per-kind telemetry for the one-shot service
	// calls (issue #64). Created once, never mutated at builder level (only
	// its internal aggregates, under its own lock) — no b.mu guarding; shared
	// into every per-session orchestrator via OrchestratorDeps.
	serviceMetrics   *ServiceMetrics
	vectorSearchFunc builtins.VectorSearchFunc
	// vectorSearchWaitFunc is the bounded readiness waiter paired with
	// vectorSearchFunc: the RAG-hint path calls it under the same deadline as
	// the search, so readiness waiting never double-spends the knob. Set
	// alongside vectorSearchFunc by RegisterVectorSearch. Guarded by b.mu.
	vectorSearchWaitFunc builtins.VectorSearchWaitFunc
	// vectorSearchWaitTimeout bounds the RAG-hint wait in per-session
	// orchestrators; set alongside vectorSearchFunc. Guarded by b.mu.
	vectorSearchWaitTimeout time.Duration
	// vectorSearchWaitDisabled carries an EXPLICIT fail-fast
	// (vector_index.search_wait_timeout_ms: 0) — here a zero timeout alone is
	// indistinguishable from "unset" (which keeps the 3s default).
	// Guarded by b.mu.
	vectorSearchWaitDisabled bool
	baseSkillDirs            []string     // resolved skill dirs shared across sessions (highest priority first)
	baseAgentDirs            []string     // resolved Subagent Profile dirs shared across sessions (highest priority first)
	proxyClient              *http.Client // proxy-configured HTTP client (nil = direct connection)

	// embeddedLLM is the builder-level embedded-model seam: the default
	// BuilderEmbeddedLLMConfig applied to EVERY router this builder constructs
	// when the per-build cfg carries no Loader of its own. It exists because a
	// BuilderConfig is converted in more than one place — and the per-session
	// orchestrator factory cannot reach the supervisor; without this default a
	// chat request to a cold model would hit a loopback socket nothing is
	// listening on. See SetEmbeddedLLM, specs/domains/embedded-llm.md.
	// Guarded by b.mu.
	embeddedLLM BuilderEmbeddedLLMConfig

	// subscriptionAuth is the builder-level subscription-auth seam — the same
	// pattern as embeddedLLM: the per-session factory converts the live config
	// where the token manager is not in scope, and without this default the
	// router's chatgpt entry would run signed-out while the user is signed in
	// (every request failing with "sign in with ChatGPT"). See
	// SetSubscriptionTokenSource. Guarded by b.mu.
	subscriptionAuth BuilderSubscriptionAuthConfig

	// askUserFunc is the ask_user callback supplied at construction, retained
	// so a runtime silent-mode toggle can re-register the tool with the live
	// callback or a nil one (reconcileAskUser) without an app restart. Nil
	// when the caller has no ask_user channel.
	askUserFunc tools.AskUserFunc

	// reasoningEffort is the cached profile seed. Empty unless seeded by the
	// ModelProfiles sampling profile (applyModelProfilesPresets); per-request
	// overrides flow through HandleOptions.ReasoningEffort →
	// Orchestrator.SetReasoningEffort (router, planner, reflector, sp4rk P&E).
	reasoningEffort string

	// goAsync dispatches fire-and-forget background work: go fn() in
	// production; tests override it to run detached work (e.g.
	// buildLocalModelProbe) synchronously and deterministically. A nil field
	// falls back to real goroutines via asyncRunner.
	goAsync func(fn func())

	// Async initialization: LLM router + tool judge initialize in the
	// background (gated by initDone) so NewOrchestratorBuilder returns
	// immediately. MCP gateway startup is decoupled (own goroutine, gated by
	// mcpDone) so Build()/session restore never blocks on server discovery;
	// MCP tools register live, so orchestrators built before MCP is ready
	// simply don't advertise MCP tools until the next message (graceful
	// degradation).
	initDone   chan struct{}
	mcpDone    chan struct{}
	initErr    error
	gatewayErr error // non-nil if MCP gateway startup failed
	// mcpStopping marks builder shutdown. StopGateway sets it UNDER b.mu
	// first, so no gateway is published or reconfigured after the stop
	// decision: runMCPInit/publishMCPGateway stops (rather than publishes) a
	// gateway it built, and ReconfigureMCP re-checks at publication. Guarded by mu.
	mcpStopping bool
	// mcpInitCtx parents runMCPInit's context; StopGateway cancels it to abort
	// an in-flight MCP startup so the bounded join on mcpDone returns promptly
	// instead of waiting out a multi-second server spawn on the shutdown
	// thread. Set once in the constructor (nil on a hand-built test literal —
	// StopGateway skips the cancel); never mutated after, so no b.mu needed.
	mcpInitCtx    context.Context
	mcpInitCancel context.CancelFunc
}

func (b *OrchestratorBuilder) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// ServiceMetricsSnapshot returns a copy of the per-kind telemetry for the
// one-shot service calls (issue #64), keyed by ServiceKind. Nil-safe — a
// builder without a collector (hand-built test literal) yields an empty map.
func (b *OrchestratorBuilder) ServiceMetricsSnapshot() map[ServiceKind]ServiceKindMetrics {
	return b.serviceMetrics.Snapshot()
}

// asyncRunner returns the background-work dispatcher: b.goAsync (tests) or
// real goroutines — the indirection lets tests run detached work
// synchronously and deterministically.
func (b *OrchestratorBuilder) asyncRunner() func(func()) {
	if b.goAsync != nil {
		return b.goAsync
	}
	return func(fn func()) { go fn() }
}

// NewOrchestratorBuilder creates the shared infrastructure (tool registry,
// built-in tools, MCP gateway, LLM router, tool judge) from cfg; runtime
// changes go through the Rebuild*/Reconfigure* methods. MCP gateway and LLM
// router initialize asynchronously — callers needing them (Build,
// GenerateTitle, …) block until the background init finishes.
func NewOrchestratorBuilder(cfg *BuilderConfig, askUserFunc tools.AskUserFunc, planApprovalFunc tools.ApprovalFunc, logger *slog.Logger) (*OrchestratorBuilder, error) {
	// Defensive default: no-op expander so downstream callers (proxy/MCP/LLM
	// config) never see a nil function pointer; the backend supplies the real
	// one via configadapter.
	if cfg.ExpandEnvVars == nil {
		cfg.ExpandEnvVars = func(s string) string { return s }
	}

	b := &OrchestratorBuilder{
		logger:         logger,
		initDone:       make(chan struct{}),
		mcpDone:        make(chan struct{}),
		mcpModes:       mcpServerModesFromConfig(cfg),
		skillModes:     capabilityModesFromConfig(cfg.Capabilities.Skills),
		agentModes:     capabilityModesFromConfig(cfg.Capabilities.Agents),
		serviceMetrics: newServiceMetrics(),
	}
	// Parent the MCP startup goroutine's context so StopGateway can abort an
	// in-flight startup and join it promptly at shutdown (see StopGateway).
	b.mcpInitCtx, b.mcpInitCancel = context.WithCancel(context.Background())

	// 0. Build proxy client (fast — no network, just config parsing)
	if cfg.Proxy.Enabled {
		proxyClient, err := proxy.BuildClient(cfg.Proxy, time.Duration(cfg.Timeouts.WebFetchProxyTimeout)*time.Second, logger)
		if err != nil {
			logger.Warn("failed to build proxy client, proceeding without proxy", "error", err)
		} else {
			b.proxyClient = proxyClient
			// Global env mutation (W-12): SetGlobalEnv defaults to true when
			// the proxy is enabled; credentials are stripped before export
			// (see proxy.SetEnvVars).
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
	b.askUserFunc = askUserFunc
	if err := tools.RegisterBuiltinTools(b.registry, toolsCfg); err != nil {
		return nil, fmt.Errorf("registering built-in tools: %w", err)
	}

	// 1a. read_skill_resource is registered once with a context-aware
	// resolver over the skills activated on the current request (see
	// ActiveSkills in systemprompt.go); per-session SkillManagers are created
	// lazily in Build so each session can add its project-local
	// .agents/skills dir without racing other sessions.
	b.registry.Register(skills.NewReadSkillResourceTool(activeSkillPathResolver))

	// 2. Security policies (fast — synchronous)
	b.applySecurityPolicies(cfg)

	// 2a. ModelProfiles profile: caches it and seeds the builder-level
	// reasoning-effort default when the sampling variant is active;
	// per-request overrides still win via ApplyRequestOverrides.
	b.applyModelProfilesPresets(cfg)

	// 3. Slow initialization, asynchronously: MCP gateway in its own
	// goroutine (mcpDone) so Build()/restore is never blocked on server
	// discovery; LLM router + judge still gate initDone, which Build waits on.
	go b.runMCPInit(cfg)
	go b.runAsyncInit(cfg)

	return b, nil
}

// runMCPInit starts the MCP gateway in a dedicated goroutine, decoupled from
// initDone; MCP tools register into the shared sp4rk registry, so
// orchestrators built before completion don't advertise them until the next
// message (graceful degradation). mcpDone closes on exit — including failure
// or panic — so waiters never block forever. Once the gateway is assigned it
// applies any work directory recorded during the startup window
// (record-and-apply with SetMCPWorkDir).
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

	// Derive the startup context from the builder's cancellable parent so
	// StopGateway can abort an in-flight startup promptly (the timeout still
	// caps a wedged connect).
	base := b.mcpInitCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()

	// MCP Gateway (optional — failures are non-fatal)
	mcpCfg := configToGatewayConfig(cfg)
	// Under the lock: MCP startup is decoupled from initDone, so a
	// RebuildProxy past waitReady may write b.proxyClient concurrently.
	b.mu.RLock()
	mcpCfg.HTTPClient = b.proxyClient
	b.mu.RUnlock()
	gw, err := mcp.StartGateway(ctx, mcpCfg, b.registry.ToolRegistry, cfg.ExpandEnvVars, b.logger)
	if err != nil {
		// MCP gateway failure is non-fatal: tools from MCP servers will be unavailable
		// but the orchestrator can still operate with built-in tools.
		b.log().Warn("MCP gateway startup failed", "error", err)
	}
	b.publishMCPGateway(gw, err)
}

// publishMCPGateway assigns the freshly built gateway (or its startup error)
// and applies any work directory recorded during the startup window
// (record-and-apply). It reports whether the gateway was published: false
// means the builder is shutting down and the caller must treat the gateway as
// rejected — it is stopped here, off the app-shutdown path (freshly spawned
// stdio children exit when their stdin closes, so this cannot stall app exit
// materially).
func (b *OrchestratorBuilder) publishMCPGateway(gw *mcp.Gateway, err error) bool {
	b.mu.Lock()
	if b.mcpStopping {
		b.mu.Unlock()
		if gw != nil {
			if stopErr := gw.Stop(); stopErr != nil {
				b.log().Warn("MCP gateway built during shutdown was stopped before publication", "error", stopErr)
			}
		}
		return false
	}
	b.gateway = gw
	b.gatewayErr = err
	// Apply a work dir recorded by SetMCPWorkDir before the gateway existed —
	// closing the record/apply race.
	if gw != nil && b.mcpWorkDir != "" {
		gw.SetDefaultWorkDir(b.mcpWorkDir)
	}
	b.mu.Unlock()
	return true
}

// runAsyncInit performs the slow network-dependent init gated by initDone:
// LLM router creation and the tool judge. (MCP gateway startup is runMCPInit's,
// gated by mcpDone.)
func (b *OrchestratorBuilder) runAsyncInit(cfg *BuilderConfig) {
	defer close(b.initDone)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// LLM Router
	llmRouter, modelReg, err := b.buildRouter(ctx, cfg, nil)
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

// waitReady blocks until async init completes or the context is cancelled.
// Unlike WaitReady it does NOT return initErr — callers proceeding with their
// own logic (RebuildRouter, RebuildJudge, Build) use this; Build constructs a
// fresh per-session router anyway and RebuildRouter clears initErr on success.
func (b *OrchestratorBuilder) waitReady(ctx context.Context) error {
	select {
	case <-b.initDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitMCPReady blocks until the MCP startup goroutine completes (or the
// context is cancelled). Separate from waitReady because MCP startup is
// deliberately decoupled from Build()/restore; methods that must observe the
// gateway's final state (MCPGateway, StopGateway, ReconfigureMCP) call it so
// the "still starting" race is closed before they read/act on b.gateway.
// SetMCPWorkDir does NOT — it uses record-and-apply so it never blocks.
func (b *OrchestratorBuilder) waitMCPReady(ctx context.Context) error {
	select {
	case <-b.mcpDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitReady blocks until async init completes or the context is cancelled and
// returns the init error if the initial LLM router setup failed. Exported for
// the backend package. A nil return only guarantees the LLM router: the MCP
// gateway may have failed independently — check MCPGatewayError() when MCP
// tools are required (gateway failures are intentionally non-fatal).
func (b *OrchestratorBuilder) WaitReady(ctx context.Context) error {
	select {
	case <-b.initDone:
		// Under the lock: RebuildRouter clears initErr under b.mu on success,
		// and close(initDone) ordering covers only runAsyncInit's own write.
		b.mu.RLock()
		err := b.initErr
		b.mu.RUnlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ToolRegistry returns the shared tool registry. The registry is set once during
// NewOrchestratorBuilder and never reassigned, so no lock is needed.
func (b *OrchestratorBuilder) ToolRegistry() *tools.ToolRegistry {
	return b.registry
}

// JudgeAvailable reports whether a strict OWASP ASI tool judge is configured
// on the shared registry (false when the registry or judge is nil — e.g. no
// model available to power the judge); the frontend uses it to disable the
// Smart Approve toggle.
func (b *OrchestratorBuilder) JudgeAvailable() bool {
	if b.registry == nil {
		return false
	}
	return b.registry.GetJudge() != nil
}

// MCPGateway returns the MCP gateway (or nil), waiting up to 30 s for the
// startup goroutine (decoupled from initDone/WaitReady, so it may still be in
// flight when initDone closes). This is the BLOCKING variant for non-UI
// consumers that must observe the gateway's final state (Shutdown,
// ReconfigureMCP, StopGateway) and MUST NOT run on the UI path — a UI call in
// the first seconds would stall behind server discovery; GetMCPStatus uses
// the non-blocking MCPStartupDone() + MCPGatewayNoWait() pair instead.
func (b *OrchestratorBuilder) MCPGateway() *mcp.Gateway {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.waitMCPReady(waitCtx)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.gateway
}

// MCPStartupDone reports whether the MCP startup goroutine has finished
// (non-blocking) — the UI status path's "still starting" discriminator, so
// the settings dialog never hangs on the multi-second discovery; pair with
// MCPGatewayNoWait.
func (b *OrchestratorBuilder) MCPStartupDone() bool {
	select {
	case <-b.mcpDone:
		return true
	default:
		return false
	}
}

// WaitMCPStartup blocks until the MCP startup goroutine finishes or the
// context is cancelled — the blocking counterpart of MCPStartupDone, for a
// one-shot notifier (the desktop layer emits EventMCPReady so the settings
// dialog can drop its transient "Starting…" placeholder). Returns nil as soon
// as startup is over, success or failure; observe the outcome via
// MCPGatewayError()/MCPGatewayNoWait().
func (b *OrchestratorBuilder) WaitMCPStartup(ctx context.Context) error {
	select {
	case <-b.mcpDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MCPGatewayNoWait returns the gateway WITHOUT blocking on mcpDone: nil while
// startup is in flight or on failure/no config — the non-blocking counterpart
// of MCPGateway for the UI path; pair with MCPStartupDone to distinguish
// "still starting" from "started and nil".
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

// Build creates a per-session Orchestrator: a fresh LLM router, core agents
// and context factory per session, while the tool registry and MCP gateway
// are shared. A non-empty workspacePath prepends
// `<workspacePath>/.agents/skills` (highest priority) to this session's skill
// discovery dirs so project-local skills override user-wide ones.
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
	// waitReady blocks only for the LLM router + judge (initDone) — never for
	// the MCP gateway (mcpDone): MCP tools register live, so an orchestrator
	// built before MCP is ready simply doesn't advertise them until the next
	// message (graceful degradation), e.g. a session created right after app
	// launch.
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

	// Per-session LLM router + model registry; the session-scoped
	// adaptive-budget table (ADR-071 D10) is created HERE — beside the
	// UsageTracker that feeds it below — and handed to the router build so
	// every provider entry's budget transport shares it.
	routerStart := time.Now()
	var budgetTable *llmbudget.BudgetTable
	if cfg.Timeouts.AdaptiveBudgetEnabled {
		budgetTable = llmbudget.NewBudgetTable()
	}
	llmRouter, modelReg, err := b.buildRouter(context.Background(), cfg, budgetTable)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM router: %w", err)
	}
	// Track the model registry for runtime metadata pushes (see
	// registerSessionModelRegistry). Build can still fail after this point and
	// the OnCleanup hook fires only when an orchestrator is actually returned,
	// so release the registry on every path that does not hand it to one —
	// otherwise the long-lived builder's live set leaks the entry and it keeps
	// receiving every UpdateModelOverrides push forever.
	modelRegOwned := false
	defer func() {
		if !modelRegOwned {
			b.unregisterSessionModelRegistry(modelReg)
		}
	}()
	b.registerSessionModelRegistry(modelReg)
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

	usageTracker := llm.NewUsageTracker()
	// Adaptive request budget (ADR-071 D1): every successful call through the
	// shared TrackingCaller (conductor steps, subagents, E2S turns) reports one
	// sample keyed by the model actually served. budgetIngestCaller sits
	// between tracker and router so the learned DURATION is the single winning
	// provider attempt, not the router call (which may wrap a retry + backoff)
	// — see budgetIngestCaller. It is the only writer of the table; both ride
	// the session lifetime, so no separate teardown.
	var routerCaller llm.Caller = llmRouter
	if budgetTable != nil {
		routerCaller = &budgetIngestCaller{inner: llmRouter, table: budgetTable}
	}
	trackingCaller := llm.NewTrackingCaller(routerCaller, usageTracker)

	// Register emitter as observer for session token events. Preferred seam:
	// SessionTokenThroughputEmitter carries the median per-call output-token
	// rate from a per-session sliding window fed by the UsageTracker's timed
	// observer (every successful call reports one sample); older emitters keep
	// the plain-totals path.
	switch te := emitter.(type) {
	case SessionTokenThroughputEmitter:
		throughput := newSessionThroughputWindow()
		usageTracker.AddTimedObserver(func(usage llm.TokenUsage, duration time.Duration, totalIn, totalOut int, model, family string) {
			median, samples := throughput.record(usage.OutputTokens, duration)
			te.EmitSessionTokensWithThroughput(totalIn, totalOut, model, family, median, samples)
		})
	case SessionTokenEmitter:
		usageTracker.AddObserver(func(_ llm.TokenUsage, totalIn, totalOut int, model, family string) {
			te.EmitSessionTokens(totalIn, totalOut, model, family)
		})
	}

	// Build context factory
	contextFactory := b.buildContextFactory(trackingCaller, cfg, modelReg, dumpWriter, llmRouter)

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

	// Model Profiles context-management override: tightens compaction,
	// tool-output pruning, and the output token reserve when both the master
	// toggle and the context variant are enabled (no-op otherwise).
	exec := applyContextManagement(cfg.Executor, cfg.ModelProfiles)

	// Build orchestrator config
	orchConfig := OrchestratorConfig{
		KeepFirst: exec.Compaction.SlidingWindow.KeepFirst,
		KeepLast:  exec.Compaction.SlidingWindow.KeepLast,
		// Full compaction settings (Model Profiles overrides applied) for
		// manual conversation-history compaction.
		Compaction:                exec.Compaction,
		MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
		MaxRedelegationDepth:      cfg.Orchestration.MaxRedelegationDepth,
		MaxParallelSubagents:      cfg.Orchestration.MaxParallelSubagents,
		// ToolCallTimeout bounds a SINGLE tool call in the ReAct loop (0 =
		// disabled), threaded to the Conductor and every subagent executor via
		// conductorDeps.toolCallTimeout.
		ToolCallTimeout: time.Duration(cfg.Timeouts.ToolCallTimeout) * time.Second,
		// The ceiling's exempt tool-name set (nil = sp4rk's built-in default;
		// an explicit list replaces it wholesale). Rides the same path as
		// ToolCallTimeout (ConductorConfig + SetToolCallTimeoutExempt).
		ToolCallTimeoutExemptTools: cfg.Timeouts.ToolCallTimeoutExemptTools,
		// Model is for METADATA resolution (ModelRegistry.Resolve keys on the
		// bare name), not routing — strip the provider prefix.
		Model:                   llm.BareModel(llmRouter.ActiveModel()),
		ReasoningEffort:         reasoningEffort,
		HITLHandler:             hitlHandler,
		PreWarningPercent:       exec.Compaction.Thresholds.PreWarningPercent,
		InjectionDefenseEnabled: cfg.Security.InjectionDefenseEnabled,
		AgentsMDMaxBytes:        cfg.Security.AgentsMDMaxBytes,
		AgentsMDSearchPaths:     cfg.Security.AgentsMDSearchPaths,
		GoalLoop: GoalLoopSettings{
			Verification: cfg.GoalLoop.Verification,
			// Suggest arrives from the config layer with Enabled already
			// resolved from the pointer-bool (nil = on) — see ToBuilderConfig.
			Suggest: cfg.GoalLoop.Suggest,
		},
		E2S: E2SSettings{
			Enabled:                 cfg.E2S.Enabled,
			MaxSteps:                cfg.E2S.MaxSteps,
			StateByteLimit:          cfg.E2S.StateByteLimit,
			PatchRetries:            cfg.E2S.PatchRetries,
			MaxObservationChars:     cfg.E2S.MaxObservationChars,
			ObservationBudgetTokens: cfg.E2S.ObservationBudgetTokens,
			ObservationFillFraction: cfg.E2S.ObservationFillFraction,
			RepeatNudgeThreshold:    cfg.E2S.RepeatNudgeThreshold,
			RepeatAbortThreshold:    cfg.E2S.RepeatAbortThreshold,
			Tools: BuilderE2SToolsConfig{
				Preset: cfg.E2S.Tools.Preset,
				Allow:  cfg.E2S.Tools.Allow,
				Deny:   cfg.E2S.Tools.Deny,
			},
		},
		ModelProfiles: ModelProfilesSettingsFromBuilderConfig(cfg.ModelProfiles),
		// Per-server MCP modes drive task-level tool gating (manual without a
		// mention / disabled hidden and rejected at dispatch); empty map gates
		// nothing. See specs/domains/capabilities.md.
		MCPServerModes:         mcpServerModesFromConfig(cfg),
		MCPServerModesResolver: b.currentMCPServerModes,
		// Skill/agent modes carry the same three-state semantics over their
		// catalogs (auto = discovery + mentions, manual = /-mention only,
		// disabled = unavailable); empty maps gate nothing.
		SkillModes:         capabilityModesFromConfig(cfg.Capabilities.Skills),
		SkillModesResolver: b.currentSkillModes,
		AgentModes:         capabilityModesFromConfig(cfg.Capabilities.Agents),
		AgentModesResolver: b.currentAgentModes,
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
	// ModelProfiles loop hardening: when enabled, override the breaker
	// thresholds with the tighter profile values (absent thresholds keep
	// their baseline) so a looping small model is caught earlier.
	circuitBreaker = applyLoopHardening(circuitBreaker, cfg.ModelProfiles)

	// Logged LLM caller for step execution (wraps trackingCaller)
	loggedLLM := agent.NewLoggingLLMCaller(trackingCaller, cfg.LLM.DefaultProviderName(), logger)
	loggedLLM = agent.NewDumpCaller(loggedLLM, dumpWriter, logger)

	// Per-session SkillManager: project-local .agents/skills is always
	// prepended (highest priority) to the shared base dirs — built
	// per-session because the workspace path differs between sessions.
	sessionSkillMgr := b.buildSessionSkillManager(workspacePath, logger)

	// Per-session AgentManager: mirrors the skill manager for Subagent
	// Profiles (project-local .agents/agents prepended; nil-safe when no
	// agent dirs are configured).
	sessionAgentMgr := b.buildSessionAgentManager(workspacePath, logger)

	// Per-session ToolRegistry clone: runtime policy mutations on a session
	// registry must NOT leak to concurrent sessions. The clone shares the
	// underlying sp4rk ToolRegistry (tools are stateless) but has its own
	// groupPolicies view; registerSessionRegistry also records it as live so
	// global security pushes reach this session without a restart (released
	// by the OnCleanup hook below).
	sessionRegistry := b.registerSessionRegistry()

	// Mirror the per-tool-call ceiling onto the session registry so a
	// user-confirmation wait can be bounded just under it and yield a clean
	// denial (the run continues) instead of the executor's watchdog aborting
	// on a slow human answer. The ceiling itself still bounds execution. See
	// tools.ToolRegistry.SetToolCallTimeout.
	sessionRegistry.SetToolCallTimeout(time.Duration(cfg.Timeouts.ToolCallTimeout) * time.Second)

	// Bind this session's judge to the session's OWN router NOW, so even the
	// first escalation is judged on the provider/model this session runs on —
	// not the builder's global active model, which another session's picker
	// may switch mid-Build. The judge rides the router as a plain caller, so
	// the session's own model switches are followed with NO re-binding.
	b.bindSessionJudge(cfg, llmRouter, sessionRegistry, usageTracker)

	// HITLHandler.OnToolCall is invoked by the executor before every tool
	// call; PolicyUserConfirm tools fall through to auto-execute when
	// ConfirmFunc is nil (CLI-mode behavior) — intended, since the HITL
	// handler already intercepted at the executor level. No ConfirmFunc
	// bridge is needed.

	totalElapsed := time.Since(buildStart)
	if totalElapsed > 100*time.Millisecond {
		b.log().Warn("orchestrator Build slow", "elapsed_ms", totalElapsed.Milliseconds(),
			"wait_ready_ms", waitElapsed.Milliseconds())
	}

	// Lazy local-model probe for this session, closing over the per-session
	// model registry so the discovered context window lands exactly where
	// Resolve reads it. Harmless no-op for cloud-only setups (their /v1/models
	// listing omits the context-window field). onWindow pushes a late-arriving
	// probe result into the emitter's display window so the status bar
	// corrects MID-task: the initial context_fill carries the pre-probe
	// window and would otherwise stay wrong for the whole first task.
	localProbe := b.buildLocalModelProbe(cfg, modelReg, func(model string, window int) {
		if setter, ok := emitter.(DisplayContextWindowForModelSetter); ok {
			setter.SetDisplayContextWindowForModel(model, window)
		}
	})
	// Probe the session's default model once at construction so the first
	// request benefits from the real context window.
	if defaultModel := llm.BareModel(llmRouter.ActiveModel()); defaultModel != "" {
		localProbe(defaultModel)
	}

	// Mechanical edit verification (executor.verify_on_edit): armed only when
	// the user enabled it AND a workspace is active. The runner comes
	// exclusively from user config (never model output) and executes via
	// ExecuteUnattended, so group-deny and the command blocklist still apply
	// (see core/verify_on_edit.go).
	var verifyOnEditRunner agent.EditVerifyRunner
	if cfg.Executor.VerifyOnEdit.Enabled {
		switch {
		case strings.TrimSpace(cfg.Executor.VerifyOnEdit.Command) == "":
			// Documented as required when enabled; an empty one would
			// silently disable verification, so surface it.
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

	// Ownership of the registry transfers to the orchestrator (via OnCleanup
	// above) only once construction returns, so even a panic inside
	// NewOrchestrator releases the registration below.
	orch := NewOrchestrator(orchConfig, OrchestratorDeps{
		Router:               coreRouter,
		LLM:                  loggedLLM,
		ModelSwitcher:        llmRouter,
		VisionResolver:       newMarkitdownVisionResolver(llmRouter, modelReg, cfg),
		ToolExec:             sessionRegistry,         // ToolExecutor (per-session policy view)
		ToolRegistry:         b.registry.ToolRegistry, // sp4rk ToolRegistry (shared)
		TokenCounter:         tokenCounter,
		ContextFactory:       contextFactory,
		Reflector:            coreReflector,
		Logger:               logger,
		ServiceMetrics:       b.serviceMetrics,
		Emitter:              emitter,
		ModelRegistry:        modelReg,
		ToolResultBudget:     toolResultBudget,
		CircuitBreaker:       circuitBreaker,
		BBFactory:            bbFactory,
		TrackingCaller:       trackingCaller,
		VectorSearchFunc:     b.vectorSearchFunc,
		VectorSearchWaitFunc: b.vectorSearchWaitFunc,
		// Raw configured value: 0 (fail-fast) is meaningful only to the
		// desktop search closure, which enforces it before this bound; the
		// orchestrator's own zero resolves to the 3s default.
		VectorSearchWaitTimeout:  b.vectorSearchWaitTimeout,
		VectorSearchWaitDisabled: b.vectorSearchWaitDisabled,
		SkillManager:             sessionSkillMgr,
		AgentManager:             sessionAgentMgr,
		CoreToolRegistry:         sessionRegistry, // per-session registry (No-Project tool disabling)
		ToolCache:                toolCache,
		PerToolTruncation:        perToolTruncation,
		StepDumpTracker:          stepDumpTracker,
		ProviderName:             cfg.LLM.DefaultProviderName(),
		LocalModelProbe:          localProbe,
		// Mechanical edit verification runner (nil = disabled); inert in
		// No-Project (CHAT) mode and goal-loop turns (see buildConductorDeps,
		// RunConductor, defaultGoalTurnRunner).
		VerifyOnEdit:               verifyOnEditRunner,
		VerifyOnEditMaxOutputChars: cfg.Executor.VerifyOnEdit.MaxOutputChars,
		// Release the session registry's live-tracking entry on cleanup, so
		// security pushes stop reaching dead clones and the builder does not
		// accumulate registries forever.
		OnCleanup: func() {
			b.unregisterSessionRegistry(sessionRegistry)
			b.unregisterSessionModelRegistry(modelReg)
		},
	})
	modelRegOwned = true
	return orch, nil
}

// RebuildRouter creates and caches a new LLM router from the given config
// (LLM settings changed at runtime); on success it clears initErr so session
// creation can proceed even if the startup initialization had failed.
func (b *OrchestratorBuilder) RebuildRouter(cfg *BuilderConfig) error {
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.waitReady(waitCtx); err != nil {
		return err
	}
	llmRouter, modelReg, err := b.buildRouter(context.Background(), cfg, nil)
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

// UpdateShellBlocklist re-registers the shell-execution tool with the
// execute-group command blocklist and the shell_exec launch-shape override.
// The blocklist compiles into the tool at construction and shapes its
// description, so runtime edits (security settings UI) need re-registration
// to take effect without an app restart; a compile failure leaves the
// previous tool in place and is returned.
func (b *OrchestratorBuilder) UpdateShellBlocklist(cfg *BuilderConfig) error {
	// Fail closed: an absent execute group or a never-materialized (nil)
	// blocklist must not re-register the shell tool with an empty list.
	// ToBuilderConfig materializes an explicit empty list (the list is
	// user-authored and empty by default), so production always arrives
	// non-nil; this guard rejects incomplete programmatic configs. An
	// explicitly emptied (non-nil) list is a deliberate clear and is honored.
	execGroup, ok := cfg.Security.Groups[string(sdktools.GroupExecute)]
	if !ok {
		return errors.New("security config is missing the execute group; refusing to compile an empty shell blocklist")
	}
	if execGroup.Blocklist == nil {
		return errors.New("security config execute group has no blocklist; refusing to compile an empty shell blocklist")
	}
	return tools.UpdateShellTool(b.registry, execGroup.Blocklist, builtins.BashTimeouts{
		MaxTimeout: time.Duration(cfg.Timeouts.BashMaxTimeout) * time.Second,
		WaitDelay:  time.Duration(cfg.Timeouts.BashWaitDelay) * time.Second,
	}, cfg.ShellExec.BashExec, cfg.ShellExec.PoshExec)
}

// UpdateSearchTool replaces or removes the web_search tool in the registry.
func (b *OrchestratorBuilder) UpdateSearchTool(cfg *BuilderConfig) error {
	apiKey := cfg.ExpandEnvVars(cfg.Search.APIKey)
	baseURL := cfg.ExpandEnvVars(cfg.Search.BaseURL)
	limits := builtins.WebSearchLimits{
		MaxResults: cfg.ToolLimits.WebSearchMaxResults,
		Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
	}
	b.mu.RLock()
	pc := b.proxyClient
	b.mu.RUnlock()
	return tools.UpdateSearchToolWithClient(b.registry, cfg.Search.Provider, apiKey, baseURL, limits, pc)
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
		// Always clear global env on disable, regardless of SetGlobalEnv —
		// the user explicitly switched the proxy off; a stale HTTP_PROXY
		// must not linger.
		proxy.ClearEnvVars()
	} else {
		proxyClient, err := proxy.BuildClient(cfg.Proxy, time.Duration(cfg.Timeouts.WebFetchProxyTimeout)*time.Second, b.logger)
		if err != nil {
			return fmt.Errorf("building proxy client: %w", err)
		}
		b.mu.Lock()
		b.proxyClient = proxyClient
		b.mu.Unlock()
		// Global env mutation (W-12). Never clear when the proxy is enabled —
		// the user may have set proxy env vars externally.
		if cfg.Proxy.SetGlobalEnv {
			proxy.SetEnvVars(cfg.Proxy)
		}
	}

	// Propagate to web tools
	if err := b.UpdateWebTools(cfg); err != nil {
		return fmt.Errorf("propagating web tools: %w", err)
	}

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
func (b *OrchestratorBuilder) UpdateWebTools(cfg *BuilderConfig) error {
	fetchLimits := builtins.WebFetchLimits{
		Timeout: time.Duration(cfg.Timeouts.WebFetchTimeout) * time.Second,
		Retries: cfg.Timeouts.WebFetchRetries,
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
	baseURL := cfg.ExpandEnvVars(cfg.Search.BaseURL)
	return tools.UpdateSearchToolWithClient(b.registry, cfg.Search.Provider, apiKey, baseURL, searchLimits, pc)
}

// GenerateTitle generates a concise conversation title via the cached LLM
// router, riding the sp4rk oneshot service policy (temperature 0.3, reasoning
// tier resolved per active model) which overrides any Model Profiles seed.
// The parse never fails — an empty title is valid (the backend replaces it
// with fallback text), so no nudge loop engages; transport errors pass
// through as-is.
func (b *OrchestratorBuilder) GenerateTitle(ctx context.Context, userMessage string, activeSkills []string) (string, error) {
	if err := b.waitReady(ctx); err != nil {
		return "", err
	}

	b.mu.RLock()
	llmRouter := b.llmRouter
	b.mu.RUnlock()

	if llmRouter == nil {
		return "", errors.New("llm router not available")
	}

	var caller oneshot.Caller = llmRouter
	if dw := agent.DumpWriterFromContext(ctx); dw != nil {
		caller = agent.NewLoggingLLMCaller(caller, llmRouter.ActiveProviderName(), b.logger)
		caller = agent.NewDumpCaller(caller, dw, b.logger)
	}
	return generateTitleWithCaller(ctx, caller, b.serviceMetrics, bareActiveModel(llmRouter), b.log(), userMessage, activeSkills)
}

// generateTitleWithCaller issues the title one-shot. It is a separate function
// so that tests can inject a mock caller and verify the request shape
// deterministically.
func generateTitleWithCaller(ctx context.Context, caller oneshot.Caller, metrics *ServiceMetrics, model string, logger *slog.Logger, userMessage string, activeSkills []string) (string, error) {
	systemPrompt := "Generate a concise title (3-7 words) describing the primary goal for a conversation that starts with the following user message. Output ONLY the title text, no quotes, no punctuation at the end."
	if len(activeSkills) > 0 {
		systemPrompt += "\n\nThe user has explicitly activated the following skills: " + strings.Join(activeSkills, ", ") + ". Consider these when determining the topic."
	}

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:       30,
		Temperature:     &oneshotTempTitle, // oneshot service policy — explicit value wins over any profile
		ReasoningEffort: serviceReasoningEffort(model, oneshotTierTitle),
		// Auxiliary text composition call — summarization class.
		CallPurpose: llm.CallPurposeSummarization,
	}
	return serviceCall(ctx, metrics, logger, ServiceKindTitle, model, caller, req, titleContent, oneshot.Options[string]{})
}

// titleContent passes the response content through unchanged: any content is
// a valid title (the backend falls back to first-words text), so the nudge
// loop has nothing to repair. The nil branch is unreachable defensive code —
// oneshot.Do itself treats (nil, nil) as a parse failure (three attempts and
// a final refusal); transport errors are returned by Do.
func titleContent(resp *llm.ChatResponse) (string, error) {
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}

// conventionalCommitRe validates the Conventional Commits shape:
// <type>[scope]: <description> over feat/fix/docs/style/refactor/perf/test/
// build/ci/chore/revert. (?s) makes . match newlines so multi-line messages
// (with body) pass — only the first line's type prefix matters here.
var conventionalCommitRe = regexp.MustCompile(
	`(?s)^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]*\))?: .+$`,
)

// isValidConventionalCommit checks the regex plus a lowercase first character
// of the description line (proper nouns mid-line like "GitHub" are allowed).
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

// extractCommitMessage extracts the best commit-message text from an LLM
// response via the oneshot toolkit (Content → ReasoningContent → Reasoning,
// fence and preamble stripped) — covering the failure mode where small models
// (especially Qwen) put the message into a reasoning field instead of
// Content. Returns "" when nothing usable is found.
func extractCommitMessage(resp *llm.ChatResponse) string {
	return oneshot.Text(resp)
}

// optimizedPromptMarkerStart / optimizedPromptMarkerEnd are the exact
// unambiguous boundaries the rewrite prompt instructs the LLM to emit around
// the optimized prompt; extractOptimizedPrompt searches them first and only
// falls back to the legacy heuristic when they are absent.
const (
	optimizedPromptMarkerStart = "### OPTIMIZED_PROMPT_START"
	optimizedPromptMarkerEnd   = "### OPTIMIZED_PROMPT_END"
)

// extractOptimizedPrompt extracts the optimized prompt in two phases:
// oneshot.Marked searches all candidate fields for both markers (first
// candidate carrying both wins, even with empty text between); when no
// markers exist at all, the oneshot heuristic strips fences and reasoning
// prefixes per candidate — so marker-using reasoning models extract
// unambiguously while older models still work.
func extractOptimizedPrompt(resp *llm.ChatResponse) string {
	// Phase 1 — markers found: return what is between them (may be empty);
	// do NOT fall through to the heuristic.
	if extracted, ok := oneshot.Marked(resp, optimizedPromptMarkerStart, optimizedPromptMarkerEnd); ok {
		return extracted
	}

	// Phase 2 — heuristic fallback (models that don't use markers).
	for _, candidate := range oneshot.CandidateTexts(resp) {
		stripped := strings.TrimSpace(oneshot.StripReasoningPrefix(oneshot.StripFence(candidate)))
		if stripped != "" {
			return stripped
		}
	}

	return ""
}

// errCommitUnparseable marks commit-message parse failures so the final
// oneshot refusal (wrapping the last parse error) can be recognized and
// re-surfaced with operator-facing advice. Empty and invalid-format outputs
// both ride the client's nudge loop.
var errCommitUnparseable = errors.New("commit message unparseable")

// commitMessageRetryHint restates the Conventional Commits contract inside the
// oneshot nudge; the failed output itself rides the assistant echo message.
const commitMessageRetryHint = "The previous output did not follow the Conventional Commits format.\n" +
	"Your output MUST start with a valid type prefix: feat, fix, docs, " +
	"style, refactor, perf, test, build, ci, chore, or revert.\n" +
	"Example: feat(auth): add token validation\n\n" +
	"DO NOT prefix with phrases like 'this commit', 'Here is the commit message:', " +
	"'Based on my analysis:', or similar."

// buildCommitMessageRequest constructs the commit-message request under the
// oneshot service policy: temperature pinned 0.3 (explicit values win over
// any profile; the Router strips sampling for models that cannot take the
// parameter), reasoning tier resolved per active model by the caller,
// summarization purpose for observability.
func buildCommitMessageRequest(diff, reasoningEffort string) llm.ChatRequest {
	// Reasoning models count reasoning tokens against the output budget;
	// 2048 covers reasoning plus a short Conventional Commits message while
	// capping runaway output (non-reasoning models stop naturally earlier).
	const commitMsgMaxTokens = 2048

	return llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coreprompts.CommitMessage},
			{Role: "user", Content: "## Staged Diff\n\n" + diff},
		},
		MaxTokens:       commitMsgMaxTokens,
		Temperature:     &oneshotTempCommit, // oneshot service policy — explicit value wins over any profile
		ReasoningEffort: reasoningEffort,
		// Auxiliary text composition call — summarization class.
		CallPurpose: llm.CallPurposeSummarization,
	}
}

// GenerateCommitMessage produces a Conventional Commits message from the
// staged diff via the cached LLM router. The caller owns the request timeout
// (ctx); the parse-retry loop is the oneshot client's (two nudges → final
// refusal); transport failures are never retried here — the Router owns
// provider retry.
func (b *OrchestratorBuilder) GenerateCommitMessage(ctx context.Context, diff string) (string, error) {
	if err := b.waitReady(ctx); err != nil {
		b.log().Warn("commit message generation aborted: builder not ready",
			"err", err, "diff_bytes", len(diff))
		return "", err
	}

	b.mu.RLock()
	llmRouter := b.llmRouter
	b.mu.RUnlock()

	if llmRouter == nil {
		b.log().Error("commit message generation failed: llm router not available",
			"diff_bytes", len(diff))
		return "", errors.New("llm router not available")
	}

	var caller oneshot.Caller = llmRouter
	if dw := agent.DumpWriterFromContext(ctx); dw != nil {
		caller = agent.NewLoggingLLMCaller(caller, llmRouter.ActiveProviderName(), b.logger)
		caller = agent.NewDumpCaller(caller, dw, b.logger)
	}
	providerName := llmRouter.ActiveProviderName()
	b.log().Debug("generating commit message",
		"provider", providerName, "diff_bytes", len(diff))

	return b.generateCommitMessageWithCaller(ctx, caller, providerName, bareActiveModel(llmRouter), diff)
}

// generateCommitMessageWithCaller runs the commit-message one-shot. It is a
// separate method so that tests can inject a mock caller and verify the
// client-owned retry behavior deterministically.
func (b *OrchestratorBuilder) generateCommitMessageWithCaller(
	ctx context.Context,
	caller oneshot.Caller,
	providerName string,
	model string,
	diff string,
) (string, error) {
	req := buildCommitMessageRequest(diff, serviceReasoningEffort(model, oneshotTierCommit))

	message, err := serviceCall(ctx, b.serviceMetrics, b.log(), ServiceKindCommitMessage, model, caller, req, b.commitMessageParse(providerName, diff), oneshot.Options[string]{
		RetryHint: commitMessageRetryHint,
	})
	if err != nil {
		if errors.Is(err, errCommitUnparseable) {
			b.log().Warn("commit message generation failed validation after all retries",
				"diff_bytes", len(diff), "provider", providerName)
			return "", fmt.Errorf("the model produced an invalid commit message after "+
				"multiple attempts; try a different model or reduce the staged diff size: %w", err)
		}
		// Transport error — never retried here (the Router owns provider
		// retry). Classified so operators can distinguish an oversized diff
		// (context window) from a slow provider (deadline) or a provider-side
		// error — the single place the LLM-side cause is logged; the backend
		// RPC layer logs only its own preconditions and passes this through.
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
	return message, nil
}

// commitMessageParse extracts and validates the commit message (multi-
// candidate oneshot extraction; empty/invalid outputs are retryable parse
// failures shown to the model via the assistant echo; transport errors never
// reach here).
func (b *OrchestratorBuilder) commitMessageParse(providerName, diff string) oneshot.Parse[string] {
	return func(resp *llm.ChatResponse) (string, error) {
		message := extractCommitMessage(resp)
		if message == "" {
			// The LLM call succeeded but produced no usable text — the
			// failure mode that previously surfaced as a silent UI no-op:
			// with a reasoning model, a too-small output budget is consumed
			// by reasoning tokens (status=incomplete,
			// reason=max_output_tokens). Surface it explicitly.
			hasReasoning := resp != nil && (resp.Message.ReasoningContent != "" || resp.Reasoning != "")
			stopReason := ""
			if resp != nil {
				stopReason = resp.StopReason
			}
			b.log().Warn("commit message generation produced no usable output",
				"diff_bytes", len(diff), "provider", providerName,
				"stop_reason", stopReason, "has_reasoning", hasReasoning)
			if hasReasoning {
				return "", fmt.Errorf("%w: the model produced no commit message text "+
					"(its output budget was likely consumed by reasoning); "+
					"try a non-reasoning model, a smaller staged diff, or a larger model", errCommitUnparseable)
			}
			return "", fmt.Errorf("%w: the model produced an empty commit message; "+
				"try again or use a different model", errCommitUnparseable)
		}

		// Validate Conventional Commits format.
		if !isValidConventionalCommit(message) {
			b.log().Debug("commit message validation failed, will retry",
				"diff_bytes", len(diff), "provider", providerName)
			return "", fmt.Errorf("%w: output does not follow the Conventional Commits format "+
				"(a valid type prefix plus a lowercase description is required)", errCommitUnparseable)
		}
		return message, nil
	}
}

// ListProviderModels returns deduplicated model names for a provider: some
// endpoints (LM Studio, OpenAI-compatible gateways) legitimately report the
// same ID twice, and the settings UI renders the list verbatim.
func (b *OrchestratorBuilder) ListProviderModels(ctx context.Context, provider string, cfg *BuilderConfig) ([]string, error) {
	names, err := b.fetchProviderModels(ctx, provider, cfg)
	if err != nil {
		return nil, err
	}
	return dedupeModelNames(names), nil
}

// fetchProviderModels resolves the raw model-name list (built-in registry
// entries or an endpoint fetch); ListProviderModels dedupes before display.
func (b *OrchestratorBuilder) fetchProviderModels(ctx context.Context, provider string, cfg *BuilderConfig) ([]string, error) {
	// Snapshot the proxy client under the read lock once per listing so every
	// endpoint-fetching branch honors the same proxy the chat path uses (the
	// "chatgpt" and "openai" branches previously ignored it) and the read is
	// not a data race against RebuildProxy. No lock across network calls.
	b.mu.RLock()
	proxyClient := b.proxyClient
	b.mu.RUnlock()
	// The bypass matcher mirrors the live proxy transport's Proxy func; both
	// rebuild from the same cfg, so a stale-matcher race is bounded to a
	// concurrent settings save and self-corrects on the next rebuild — same
	// tolerance as every other cfg snapshot in this file.
	bypass := proxy.NewBypassMatcher(cfg.Proxy.BypassList)
	log := b.log()

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
		// Fixed provider: no pin key exists (api.openai.com has a public
		// certificate) — this only threads the dial policy.
		models, err := listOpenAIModels(ctx, "", apiKey, llmtls.DirectDialClient(proxyClient, dialPolicy(proxyClient, bypass, "api.openai.com"), "", log))
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
			// Per-provider TLS override under the proxy-wins rule
			// (ADR-054): while the proxy dials, the pin is ignored; a
			// bypassed host (or no proxy) dials directly, pinned when
			// configured — the listing reaches self-signed endpoints
			// exactly like the chat path.
			return listOpenAIModels(ctx, baseURL, apiKey, llmtls.DirectDialClient(proxyClient, dialPolicy(proxyClient, bypass, baseURL), pc.TLSFingerprint, log))
		case "anthropic":
			baseURL := cfg.ExpandEnvVars(pc.BaseURL)
			// Fixed "anthropic" provider (no BaseURL): built-in Claude
			// list. An anthropic_compatible entry queries the endpoint's
			// /v1/models and falls back to the built-in list on any error
			// so the UI degrades gracefully.
			if baseURL == "" {
				return llm.BuiltInModelNames("anthropic-api"), nil
			}
			apiKey := cfg.ExpandEnvVars(pc.APIKey)
			names, err := listAnthropicModels(ctx, baseURL, apiKey, llmtls.DirectDialClient(proxyClient, dialPolicy(proxyClient, bypass, baseURL), pc.TLSFingerprint, log))
			if err != nil {
				log.Warn("anthropic-compatible model listing failed; falling back to built-in list",
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

// dedupeModelNames removes duplicate entries while preserving first-occurrence
// order. No sortedness is assumed: built-in registry lists and raw endpoint
// responses (sorted or not) both flow through here.
func dedupeModelNames(names []string) []string {
	if len(names) < 2 {
		return names
	}
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, n := range names {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		result = append(result, n)
	}
	return result
}

// SetEmbeddedLLM installs (or, with a zero value, withdraws) the builder-level
// embedded-model seam applied to every router this builder constructs — see
// specs/domains/embedded-llm.md. The backend calls it from the embedded
// lifecycle (startup restore, after install, on removal), mirroring the
// persisted state exactly like the per-build injection in
// FrontendAPI.applyEmbeddedLoader; this is the net for the router builds that
// skip that injection. A per-session orchestrator is built from a
// BuilderConfig converted deep inside the session factory, which has no path
// to the supervisor — a seam remembered per conversion site would eventually
// be forgotten at one, and the failure is silent (a chat request to a cold
// model dispatched to a loopback socket nothing is listening on). It does NOT
// rebuild anything: follow with RebuildRouter to reach the cached router;
// per-session routers pick it up at their next build.
func (b *OrchestratorBuilder) SetEmbeddedLLM(cfg BuilderEmbeddedLLMConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.embeddedLLM = cfg
}

// embeddedSeam resolves the effective embedded seam for one router build: an
// explicit per-build Loader wins, else the builder-level default. buildRouter
// is the single place every router (cached or per-session) is constructed, so
// resolving here covers both.
func (b *OrchestratorBuilder) embeddedSeam(cfg *BuilderConfig) BuilderEmbeddedLLMConfig {
	if cfg.EmbeddedLLM.Loader != nil && cfg.EmbeddedLLM.ProviderName != "" {
		return cfg.EmbeddedLLM
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.embeddedLLM
}

// SetSubscriptionTokenSource installs (or, with a zero value, withdraws) the
// builder-level subscription-auth seam — the identical mechanism to
// SetEmbeddedLLM: the per-session factory has no path to the token manager,
// and a seam remembered per conversion site would eventually be forgotten at
// one, leaving a signed-IN user's requests failing with the signed-out error.
// The backend calls it after a successful sign-in (live token manager) and on
// sign-out (zero value). It does NOT rebuild anything: follow with
// RebuildRouter; per-session routers pick it up at their next build.
func (b *OrchestratorBuilder) SetSubscriptionTokenSource(cfg BuilderSubscriptionAuthConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscriptionAuth = cfg
}

// subscriptionAuthSeam resolves the effective subscription-auth seam: an
// explicit per-build TokenSource wins, else the builder-level default — the
// same single-resolution-point rule as embeddedSeam.
func (b *OrchestratorBuilder) subscriptionAuthSeam(cfg *BuilderConfig) BuilderSubscriptionAuthConfig {
	if cfg.SubscriptionAuth.TokenSource != nil && cfg.SubscriptionAuth.ProviderName != "" {
		return cfg.SubscriptionAuth
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subscriptionAuth
}

// RegisterVectorSearch adds the semantic_search tool to the shared registry
// (after NewOrchestratorBuilder, once the vector backend is available). The
// desktop layer supplies searchFunc and waitFunc; waitFunc is stored
// alongside for the RAG-hint path, which calls it under the same deadline as
// the search so readiness waiting and query execution share one budget (the
// search closure itself never waits). waitTimeout bounds the RAG-hint wait in
// per-session orchestrators; waitDisabled marks an EXPLICIT fail-fast
// (configured 0) — without it a zero waitTimeout would keep the 3s default,
// silently widening the user's choice.
func (b *OrchestratorBuilder) RegisterVectorSearch(searchFunc builtins.VectorSearchFunc, waitFunc builtins.VectorSearchWaitFunc, waitTimeout time.Duration, waitDisabled bool) {
	if searchFunc == nil {
		return
	}
	b.mu.Lock()
	b.vectorSearchFunc = searchFunc
	b.vectorSearchWaitFunc = waitFunc
	b.vectorSearchWaitTimeout = waitTimeout
	b.vectorSearchWaitDisabled = waitDisabled
	b.mu.Unlock()
	b.registry.Register(builtins.NewVectorSearchTool(searchFunc, waitFunc))
	if b.logger != nil {
		b.logger.Info("registered semantic_search tool")
	}
}

// SetSkillDirs sets the base (shared) skill discovery directories, applied to
// every session; paths must already be absolute and expanded (the backend
// layer owns resolution). The project-local `<workspacePath>/.agents/skills`
// directory is always prepended automatically in Build() — do NOT include it.
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
// base dirs plus an optional project-local skill directory — the frontend
// ListSkills API's path, avoiding a full SkillManager per call.
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
// applied to every session; paths must already be absolute and expanded (the
// backend layer owns resolution). The project-local
// `<workspacePath>/.agents/agents` directory is always prepended
// automatically in GetAgentDescriptors — do NOT include it here.
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
// the shared base dirs plus an optional project-local agent directory — the
// frontend ListAgents API's path; mirrors GetSkillDescriptors.
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

// ResolveSkillResourcePath resolves a read_skill_resource (skill, path) call
// to the absolute file path the tool actually read. The skill is looked up in
// the same discovery set a session's SkillManager scans (project-local first,
// then shared base dirs, first name occurrence wins) and the resource path
// goes through skills.SafeResolvePath — the SAME traversal guard the tool
// itself applies. Used by the frontend RPC so a tool-call card can open the
// resource in the viewer. Errors (no dirs configured, unknown skill, escaping
// path) fall back to the frontend rendering the path as plain text.
func (b *OrchestratorBuilder) ResolveSkillResourcePath(projectSkillDir, skillName, relPath string) (string, error) {
	if skillName == "" {
		return "", errors.New("skill name is required")
	}
	if relPath == "" {
		return "", errors.New("resource path is required")
	}

	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseSkillDirs...)
	b.mu.RUnlock()

	dirs := make([]string, 0, len(baseDirs)+1)
	if projectSkillDir != "" {
		dirs = append(dirs, projectSkillDir)
	}
	dirs = append(dirs, baseDirs...)
	if len(dirs) == 0 {
		return "", errors.New("no skill directories configured")
	}

	sm := skills.NewSkillManager(dirs, b.log())
	if err := sm.Scan(); err != nil {
		b.log().Warn("ResolveSkillResourcePath scan failed", "error", err, "dirs", dirs)
	}

	skillDir, ok := sm.SkillPath(skillName)
	if !ok {
		return "", fmt.Errorf("skill %q not found", skillName)
	}

	// context.TODO(): no session case-sensitivity flag exists on this path —
	// IsWithinRoot falls back to the case-sensitive check, the same behavior
	// SafeResolvePath's documented nil-ctx fallback gives.
	return skills.SafeResolvePath(context.TODO(), skillDir, relPath)
}

// buildSessionSkillManager constructs a per-session SkillManager that always
// scans the current project's .agents/skills (when workspacePath is set) in
// addition to the shared base dirs; scan errors log and never abort startup.
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

// buildSessionAgentManager constructs a per-session AgentManager — mirrors
// buildSessionSkillManager for Subagent Profiles (.agents/agents prepended;
// scan errors log, never abort startup).
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

// activeSkillPathResolver resolves a skill name to its directory via the
// ActiveSkills set in the request context: only skills the router activated
// on the current request are addressable, matching the read_skill_resource
// tool's documented contract.
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

// Internal helpers.

// modelOverridesFromConfig derives the tier-1 model-metadata override map a
// model registry is seeded with; buildRouter (construction) and
// UpdateModelOverrides (runtime pushes into live session registries) both
// derive through this one helper so the two writers cannot drift. Entries are
// seeded PARTIAL (only user-set fields; unset = inherit, filled by the
// registry's enrichPartialOverride from the tiers below) — merging
// ResolveBuiltInModel values here would pin the catalog window (262144) or
// fallback (128000) into tier 1, permanently shadowing the lazy server probe
// and the model's real non-standard window. Two post-processing passes run on
// the raw map: the Google-protocol remap for locally-served Gemma/Gemini
// checkpoints (remapLocalGoogleProtocols) and the per-provider
// output_token_reserve seeding (applyProviderOutputReserves).
func modelOverridesFromConfig(cfg *BuilderConfig) map[string]llm.ModelMetadata {
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

	remapLocalGoogleProtocols(overrides, cfg.LLM.ProviderConfigs, cfg.ExpandEnvVars)
	applyProviderOutputReserves(overrides, cfg.LLM.ProviderConfigs)
	return overrides
}

// buildRouter creates a fresh LLM Router + ModelRegistry from config;
// budgetTable is the session-scoped adaptive-budget sample table (ADR-071) —
// nil (builder-level routers) installs no budget transport on any entry.
func (b *OrchestratorBuilder) buildRouter(ctx context.Context, cfg *BuilderConfig, budgetTable *llmbudget.BudgetTable) (*llm.Router, *llm.ModelRegistry, error) {
	// Snapshot proxyClient under lock to avoid data races with RebuildProxy.
	b.mu.RLock()
	proxyClient := b.proxyClient
	b.mu.RUnlock()

	// Only config.yaml-derived user overrides are seeded into the registry at
	// construction (Resolution tier 1) — see modelOverridesFromConfig. LM
	// Studio / local-server context-window discovery is performed lazily
	// per-session (Build + buildLocalModelProbe) and written via
	// SetRuntimeMetadata (tier 1.5, ABOVE the built-in catalog) so the
	// server's observed runtime window beats the catalog spec, while an
	// explicit config.yaml value still wins.
	overrides := modelOverridesFromConfig(cfg)

	modelRegistry := llm.NewModelRegistry(overrides)
	if proxyClient != nil {
		modelRegistry.SetHTTPClient(proxyClient)
	}

	// Dedicated HTTP client for LLM inference: reasoning models can take
	// minutes, so this timeout is far longer than the proxy/tools client's —
	// without it the SDK falls back to http.DefaultClient (NO timeout), and a
	// stalled upstream hangs the session indefinitely.
	llmClient := buildLLMHTTPClient(proxyClient, cfg.Timeouts.LLMRequestTimeout)
	initialBackoff, err := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	if err != nil && cfg.LLM.Retry.InitialBackoff != "" {
		b.log().Warn("invalid initial_backoff, using default", "value", cfg.LLM.Retry.InitialBackoff, "error", err)
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
	if err != nil && cfg.LLM.Retry.MaxBackoff != "" {
		b.log().Warn("invalid max_backoff, using default", "value", cfg.LLM.Retry.MaxBackoff, "error", err)
	}

	// Build provider entries in a deterministic order (matching backend/config
	// allProviderEntries) so the first provider is predictable.
	//
	// The embedded seam is resolved ONCE per build, not per entry: it falls
	// back to the builder-level default when this BuilderConfig carries no
	// Loader — the per-session orchestrator factory's case (SetEmbeddedLLM).
	embedded := b.embeddedSeam(cfg)
	// The subscription-auth seam resolves the same way (per-build wins, else
	// the builder-level default) so the per-session router is covered too.
	subscription := b.subscriptionAuthSeam(cfg)
	// Adaptive request budget (ADR-071): when the kill-switch is on and a
	// session table was supplied, every entry's client gets the budget
	// RoundTripper (on top of the pin, beneath the ensure-loaded gate).
	budget := budgetWiringFromConfig(cfg, budgetTable)
	providers := make([]llm.ProviderEntry, 0, len(cfg.LLM.ProviderConfigs))
	// Non-nil exactly when the proxy is effective (proxy.BuildClient rule);
	// the bypass matcher re-arms the pin for hosts the operator excluded
	// (ADR-054). One matcher per router build — read-only after construction.
	bypass := proxy.NewBypassMatcher(cfg.Proxy.BypassList)
	providerOrder := []string{"anthropic", "chatgpt"}
	for _, name := range providerOrder {
		pc, ok := cfg.LLM.ProviderConfigs[name]
		if !ok || len(pc.Models) == 0 {
			continue
		}
		providers = append(providers, providerEntryFromConfig(name, pc, llmClient, proxyClient, bypass, embedded, budget, subscription, cfg.ExpandEnvVars, b.log(), b))
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
		providers = append(providers, providerEntryFromConfig(name, pc, llmClient, proxyClient, bypass, embedded, budget, subscription, cfg.ExpandEnvVars, b.log(), b))
	}

	// Model Profiles context-management override: keeps the router's token budget
	// (safety margin, output reserve) in sync with the executor tightening
	// applied in Build (no-op unless both toggles are enabled).
	exec := applyContextManagement(cfg.Executor, cfg.ModelProfiles)

	routerCfg := llm.RouterConfig{
		Providers:           providers,
		MaxRetries:          cfg.LLM.Retry.MaxRetries,
		InitialBackoff:      initialBackoff,
		MaxBackoff:          maxBackoff,
		SafetyMarginPercent: exec.Compaction.SafetyMarginPercent,
		OutputTokenReserve:  exec.OutputTokenReserve,
		HTTPClient:          llmClient,
		Logger:              b.logger,
		SamplingFunc:        resolveSamplingFunc(cfg.ModelProfiles),
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

// applyProviderOutputReserves seeds ModelMetadata.OutputLimit for every model
// served by a provider that sets output_token_reserve — OutputLimit is both
// the context-window reserve and the executor MaxTokens ceiling, so one
// provider-level knob raises the generation budget for all its models (the
// right granularity for self-hosted gateways whose real limits differ from
// the built-in catalog). Priority: an explicit per-model llm.models
// output_limit always wins and is never clobbered; unseeded models keep
// inheriting the global executor.output_token_reserve fallback. Unset (0) and
// negative values are ignored; when two providers list the SAME bare model,
// the lexicographically first provider wins (sorted iteration, no map-order
// dependence). Idempotent.
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
// chat_completions for Google-named checkpoints (Gemma/Gemini) served by an
// OpenAI-compatible server: per the LM Studio endpoint matrix these servers
// expose /v1/chat/completions, /v1/responses and /v1/messages but NOT
// :generateContent, which they answer with a misleading 200 OK + empty body.
// Only the Google protocol is remapped (Responses/Anthropic are served fine);
// keyed by bare model name — the value that becomes req.Model on the wire.
//
// The injected override is PROTOCOL-ONLY: Protocol pinned, built-in
// Capabilities backfilled (multimodal flag preserved), and ContextWindow /
// OutputLimit / TokenizerType left zero so the registry inherits them from
// lower tiers at Resolve — essential because the lazy probe writes the REAL
// runtime window via SetRuntimeMetadata, and a wholesale override carrying a
// catalog/fallback window would permanently shadow it. A pre-seeded PARTIAL
// user entry is merged into (protocol pinned, Capabilities backfilled only
// when unset, every user field preserved); an explicit user protocol override
// is respected and never clobbered. Idempotent.
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
			// Respect an explicit user protocol override — never clobber.
			existing, hasEntry := overrides[model]
			if hasEntry && existing.Protocol != "" {
				continue
			}
			base, _ := llm.ResolveBuiltInModel(model)
			if base.Protocol != llm.ProtocolGoogle {
				continue
			}
			if hasEntry {
				// Partial user entry (Protocol still empty): merge — pin the
				// protocol, backfill Capabilities only when unset, so the
				// user's other tier-1 fields survive.
				existing.Protocol = llm.ProtocolChatCompletions
				if existing.Capabilities == nil {
					existing.Capabilities = base.Capabilities
				}
				overrides[model] = existing
				continue
			}
			// Protocol-only override: unset scalars inherit from lower tiers
			// so the lazy probe result takes effect (shadowing rationale on
			// the function doc).
			overrides[model] = llm.ModelMetadata{
				Protocol:     llm.ProtocolChatCompletions,
				Capabilities: base.Capabilities,
			}
		}
	}
}

// buildLocalModelProbe returns a LocalModelProbe that locates the model's
// OpenAI-compatible provider and fires an asynchronous context-window probe
// into the per-session model registry via SetRuntimeMetadata. Any
// OpenAI-compatible provider is probed — local/LAN (LM Studio), self-hosted
// on a public host (vLLM/TGI/Ollama), even cloud (harmless: real cloud
// /v1/models listings omit the per-model context-window field, so nothing is
// discovered). Non-OpenAI providers and unknown models are silent no-ops.
// The probe tries the LM Studio native endpoint first (runtime/loaded
// window), then the standard /v1/models listing (max_model_len) — see
// probeSelfHostedContextWindow. It runs detached (b.asyncRunner, fresh
// context.Background(), bounded 3s inside each probe) so it never blocks
// HandleMessage/session creation; results land in Resolution tier 1.5 (above
// the catalog, below a config override). onWindow fires on the same goroutine
// so the caller can refresh display state mid-task; tests override
// asyncRunner for deterministic assertions.
func (b *OrchestratorBuilder) buildLocalModelProbe(cfg *BuilderConfig, registry *llm.ModelRegistry, onWindow func(model string, window int)) LocalModelProbe {
	// Snapshot the proxy client once so the closure never touches b.mu.
	b.mu.RLock()
	proxyClient := b.proxyClient
	b.mu.RUnlock()
	log := b.log()
	expand := cfg.ExpandEnvVars
	goRun := b.asyncRunner()
	// Same rationale as fetchProviderModels: rebuilt from the same config
	// snapshot the probe reads its providers from.
	bypass := proxy.NewBypassMatcher(cfg.Proxy.BypassList)

	return func(model string) {
		if model == "" || registry == nil {
			return
		}
		baseURL, apiKey, tlsFingerprint, ok := lookupOpenAIProviderBaseURL(cfg, model, expand)
		if !ok {
			return
		}
		// Per-provider TLS override under the proxy-wins rule (ADR-054):
		// proxy dials → the plain proxy client; bypassed host or no proxy →
		// direct, pinned when configured. Resolved here, on the caller's
		// goroutine, so the detached probe receives a ready client.
		probeClient := llmtls.DirectDialClient(proxyClient, dialPolicy(proxyClient, bypass, baseURL), tlsFingerprint, log)
		goRun(func() {
			window, err := probeSelfHostedContextWindow(context.Background(), baseURL, apiKey, model, probeClient)
			if err != nil {
				log.Warn("lazy model probe failed", "model", model, "base_url", baseURL, "error", err)
			}
			if window <= 0 {
				return
			}
			// SetRuntimeMetadata writes Resolution tier 1.5 — above the
			// catalog and lazy cache, below a config.yaml override — so the
			// user's explicit choice is never clobbered. A tier-3 write is
			// NOT enough: self-hosted servers routinely serve well-known
			// checkpoints far below the catalog maximum (LM Studio loads at a
			// user-chosen length; the catalog says 262144), and the catalog
			// tier would permanently shadow the observed runtime window.
			//
			// OutputLimit mirrors the built-in tier-5 fallback (32768) so
			// self-hosted models are not regressed (no listing exposes a
			// per-model output cap), clamped to at most window/4 — a larger
			// OutputLimit drives EffectiveMax negative and disables
			// compaction ("reject"), so a small-context model would grow
			// unbounded until the API rejects it. TokenizerType stays empty:
			// a synthetic "approximate" here would sit ABOVE the catalog,
			// shadowing the exact tokenizer a well-known checkpoint carries.
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

// lookupOpenAIProviderBaseURL finds the OpenAI-compatible provider serving
// `model` and returns its expanded base_url, api key, and per-provider TLS
// pin (ADR-054); false only when none serves the model. Host locality is
// deliberately NOT filtered — a self-hosted server on a public host is probed
// exactly like a local one, and the probe is a harmless no-op for cloud.
//
// Subscription-auth entries (ChatGPT oauth) are EXCLUDED: their pinned vendor
// BaseURL would otherwise qualify them for the self-hosted probe, which
// stamps the entry's retained static API key onto a request to the vendor's
// keyless probe paths — leaking a key the operator believed unused (ADR-074
// D2/D6) — and probe endpoints that do not answer the subscription catalog
// anyway (that comes from FetchChatGPTModels, never here).
func lookupOpenAIProviderBaseURL(cfg *BuilderConfig, model string, expand func(string) string) (baseURL, apiKey, tlsFingerprint string, ok bool) {
	for _, pc := range cfg.LLM.ProviderConfigs {
		if pc.ProviderType != "openai" {
			continue
		}
		if pc.SubscriptionAuth {
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
		return raw, expand(pc.APIKey), pc.TLSFingerprint, true
	}
	return "", "", "", false
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

// llmBudgetWiring carries the adaptive per-model LLM request budget (ADR-071)
// into providerEntryFromConfig; the zero value is the disabled posture (every
// entry client byte-for-byte pre-feature).
type llmBudgetWiring struct {
	enabled bool
	// table is the session-scoped sample store shared by every entry
	// transport of one session, fed by that session's UsageTracker timed
	// observer; non-nil exactly when enabled.
	table *llmbudget.BudgetTable
	// global is timeouts.llmRequestTimeout: under the kill-switch a positive
	// value is a fixed, never-escalated override (ADR-071 D5); 0 = "no
	// opinion", trained budgets govern.
	global time.Duration
	// overrides are the raw llm.models entries, consulted per wire-extracted
	// model name for request_timeout and the output_limit reserve.
	overrides map[string]BuilderModelOverride
}

// budgetWiringFromConfig derives the wiring for one router build; a nil
// budgetTable (builder-level router) always disables the transport — those
// routers serve service-shaped calls that own their ctx deadlines, and a
// table without a tracker would only ever arm warmup budgets.
func budgetWiringFromConfig(cfg *BuilderConfig, budgetTable *llmbudget.BudgetTable) llmBudgetWiring {
	if !cfg.Timeouts.AdaptiveBudgetEnabled || budgetTable == nil {
		return llmBudgetWiring{}
	}
	var global time.Duration
	if cfg.Timeouts.LLMRequestTimeout > 0 {
		global = time.Duration(cfg.Timeouts.LLMRequestTimeout) * time.Second
	}
	return llmBudgetWiring{
		enabled:   true,
		table:     budgetTable,
		global:    global,
		overrides: cfg.LLM.Models,
	}
}

// overridesFor resolves the per-model operator opinions for one wire-extracted
// model name (ADR-071 D5/D7): llm.models.<name>.request_timeout as the fixed
// deadline and llm.models.<name>.output_limit as the out-reserve ceiling.
// Unknown model, empty map — zero values, i.e. "no opinion".
func (w llmBudgetWiring) overridesFor(model string) llmbudget.ModelOverrides {
	ov, ok := w.overrides[model]
	if !ok {
		return llmbudget.ModelOverrides{}
	}
	mo := llmbudget.ModelOverrides{}
	if ov.RequestTimeout > 0 {
		mo.RequestTimeout = time.Duration(ov.RequestTimeout) * time.Second
	}
	if ov.OutputLimit > 0 {
		mo.OutputLimit = ov.OutputLimit
	}
	return mo
}

// budgetIngestCaller feeds the session-scoped adaptive-budget table (ADR-071
// D1) with one sample per successful call, keyed by the model actually
// served. It sits BETWEEN the TrackingCaller and the router because it must
// control the sample's DURATION: the router retries a budget-expired request
// (D9) with backoff, so a duration around the whole router call would fold
// the failed attempt + backoff + success into one sample and inflate the
// fitted r_in/r_out — worst exactly in the escalation scenario the feature
// exists for. The budget transport instead reports the winning attempt's own
// duration via the context recorder this caller installs; on the
// pass-through path (caller-owned deadline, no recorder) it falls back to its
// own wall-clock measurement. The tracker observes the same calls for its
// totals; this caller only owns the budget-table write.
type budgetIngestCaller struct {
	inner llm.Caller
	table *llmbudget.BudgetTable
}

// Call delegates to the inner caller and, on success, ingests one timed
// sample (Ingest ignores an empty model or non-positive duration); failures
// record nothing, matching the successful-calls-only feed contract.
func (c *budgetIngestCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	rec := llmbudget.NewAttemptRecorder()
	start := time.Now()
	resp, err := c.inner.Call(llmbudget.WithAttemptRecorder(ctx, rec), req)
	if err != nil {
		return nil, err
	}
	d, ok := rec.Duration()
	if !ok {
		d = time.Since(start)
	}
	c.table.Ingest(resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens, d)
	return resp, nil
}

// budgetWire selects the model-extraction strategy for one provider entry
// (ADR-071 D9): openai/anthropic wires carry the model as a top-level JSON
// field; only an explicitly google-protocol model speaks the gemini URL
// shape. Local google checkpoints are remapped to chat_completions before
// this point (remapLocalGoogleProtocols), so the URL wire is chosen ONLY when
// EVERY enabled model carries an explicit "google" protocol override — a
// mixed provider keeps the JSON wire and its google-wire requests degrade to
// the provider-level budget key (still armed, never unbounded).
func budgetWire(pc BuilderProviderConfig, overrides map[string]BuilderModelOverride) llmbudget.WireFormat {
	if len(pc.Models) == 0 {
		return llmbudget.WireJSONModel
	}
	for _, model := range pc.Models {
		if ov, ok := overrides[model]; !ok || ov.Protocol != string(llm.ProtocolGoogle) {
			return llmbudget.WireJSONModel
		}
	}
	return llmbudget.WireURLModel
}

// attachBudgetClient wraps one provider entry's dial client with the adaptive
// budget RoundTripper (ADR-071 D9) — AFTER llmtls.RouterEntryClient (the
// wrapper goes on TOP of the pin: llmtls needs a concrete *http.Transport
// beneath it) and BEFORE the embedded ensure-loaded decoration (UNDER that
// gate, so the armed deadline never covers the cold-load wait — D13).
//
// Kill-switch off → client returned untouched, byte-for-byte pre-ADR-071.
// Enabled → the entry gets an EXPLICIT client, never the load-bearing nil:
// the pin path clones the pinned client (never mutated), zeroes its Timeout
// and wraps its transport (pin beneath, budget above); the nil path (proxy
// dials or no pin) clones the shared router client preserving its proxy
// transport — the whole point of the load-bearing nil — replacing its fixed
// Timeout with the budget transport's ALWAYS-ARM (the stalled-upstream
// invariant); a plain nil would fall back to RouterConfig.HTTPClient, which
// carries no budget transport. Both paths set Client.Timeout = 0: with the
// transport on the wire a client-level timeout would double-cap, and on the
// embedded entry it is what keeps EnsureLoadedClient from arming a fixed
// post-readiness 600 s under a trained adaptive budget up to the class
// ceiling (ADR-071 D8).
func (w llmBudgetWiring) attachBudgetClient(
	client, sharedClient *http.Client,
	name, baseURL, timeoutClass string,
	wire llmbudget.WireFormat,
	logger *slog.Logger,
) *http.Client {
	if !w.enabled {
		return client
	}
	base := client
	if base == nil {
		base = sharedClient
	}
	if base == nil {
		// Defensive: no client to derive from (no pin, nil shared). Leave the
		// entry on the router-level fallback — the pre-adaptive posture.
		return client
	}
	class := llmbudget.Classify(name, baseURL, timeoutClass)
	clone := *base
	clone.Timeout = 0
	clone.Transport = llmbudget.NewTransport(base.Transport, llmbudget.TransportOptions{
		Table:                w.table,
		Class:                class,
		Wire:                 wire,
		ProviderName:         name,
		Overrides:            w.overridesFor,
		GlobalRequestTimeout: w.global,
		AdaptiveEnabled:      true,
		Logger:               logger,
	})
	return &clone
}

// providerEntryFromConfig builds one llm.ProviderEntry from a provider's
// BuilderConfig slice.
//
// Client resolution order is fixed. (1) The per-provider TLS pin (ADR-054 —
// the pin is the switch) attaches a pinned client ONLY when the entry carries
// a non-empty TLSFingerprint AND the proxy does NOT dial this endpoint; while
// the proxy dials the entry deliberately leaves HTTPClient nil — nil makes
// the SDK fall back to RouterConfig.HTTPClient, which already has the proxy
// transport AND the long LLM timeout (attaching the raw proxy client here
// would shadow it and cap every inference at the shorter web-fetch proxy
// timeout). See llmtls.RouterEntryClient; logger (may be nil) flows to
// llmtls warnings. (2) The adaptive budget wraps on top of the pin, beneath
// the ensure-loaded gate, setting Client.Timeout = 0 when enabled (see
// attachBudgetClient); kill-switch off = inert, byte-for-byte pre-ADR-071.
// (3) The embedded ensure-loaded transport (embedded) decorates only what the
// pin rule decided — embedding first would hand llmtls a wrapper transport
// that cannot hold a tls.Config; both clone sharedClient, so
// timeouts.llmRequestTimeout survives either way.
//
// The embedded guard is also the ONLY writer of ReasoningWire and
// OmitReasoningHistory: the embedded llama-server reads Qwen reasoning
// controls ONLY from chat_template_kwargs (top-level spelling silently
// ignored — a property of the supervised server, never of a user-authored
// base URL, which is why it rides the same guard), and its chat template
// rejects the reasoning_content history echo the DeepSeek V4 contract adds to
// every replayed assistant message — so that entry alone opts out.
//
// Subscription auth (SubscriptionAuth, set by ToBuilderConfig for
// llm.chatgpt.auth.mode: "oauth") resolves through the subscription seam and
// becomes TokenSource + RequireStreaming; marker off = neither field ever set
// (the exact pre-feature entry, whatever the seam carries).
func providerEntryFromConfig(
	name string,
	pc BuilderProviderConfig,
	sharedClient *http.Client,
	proxyClient *http.Client,
	bypass proxy.BypassMatcher,
	embedded BuilderEmbeddedLLMConfig,
	budget llmBudgetWiring,
	subscription BuilderSubscriptionAuthConfig,
	expand func(string) string,
	logger *slog.Logger,
	builder *OrchestratorBuilder,
) llm.ProviderEntry {
	resolvedBase := expand(pc.BaseURL)
	policy := dialPolicy(proxyClient, bypass, resolvedBase)
	client := llmtls.RouterEntryClient(policy, sharedClient, pc.TLSFingerprint, logger)
	// Adaptive request budget (ADR-071): wrap on top of the pin, beneath the
	// ensure-loaded gate. The wire is derived from the same explicit protocol
	// overrides the registry sees; the class from the operator override and
	// the resolved loopback shape of base_url.
	client = budget.attachBudgetClient(client, sharedClient, name, resolvedBase,
		pc.TimeoutClass, budgetWire(pc, budget.overrides), logger)
	reasoningWire := llm.ReasoningWireVendorDefault
	omitReasoningHistory := false
	if embedded.guards(name) {
		// A cold embedded model is not listening, so this entry's client must
		// start it before the request goes out and restart the idle budget
		// after the response. The clone inherits the long LLM timeout —
		// handing over the web-fetch proxy budget is the exact mistake llmtls
		// warns about; under the budget wiring Timeout is already 0, so the
		// gate adds no fixed post-readiness budget and a trained adaptive
		// budget up to the class ceiling is honored (ADR-071 D8/D13).
		client = embeddedllm.EnsureLoadedClient(client, sharedClient, embedded.Loader, embedded.LoadWaitTimeout, logger)
		// The embedded server (pinned PrismML-Eng/llama.cpp fork) reads
		// enable_thinking ONLY from chat_template_kwargs; a top-level field
		// is silently ignored, so "Off" would not turn thinking off.
		reasoningWire = llm.ReasoningWireChatTemplateKwargs
		// The same template rejects the reasoning_content history echo the
		// DeepSeek V4 contract adds to every replayed assistant message — a
		// template that doesn't know the variable fails the whole request.
		// The embedded entry opts out; DeepSeek-style endpoints keep the echo
		// via the zero value.
		omitReasoningHistory = true
	}
	tokenSource, requireStreaming := subscriptionAuthEntry(name, pc, subscription, builder)
	return llm.ProviderEntry{
		Name:         name,
		ProviderType: pc.ProviderType,
		APIKey:       expand(pc.APIKey),
		BaseURL:      resolvedBase,
		Models:       pc.Models,
		HTTPClient:   client,
		// Zero value for non-embedded providers: the vendor-default top-level
		// spelling stays right for LM Studio/vLLM/Ollama on the same loopback.
		ReasoningWire: reasoningWire,
		// Only the embedded entry opts out of the reasoning_content echo;
		// every other provider keeps the DeepSeek V4 contract via the zero.
		OmitReasoningHistory: omitReasoningHistory,
		// Subscription auth (nil unless SubscriptionAuth is set): the token
		// source's per-request credentials override the static APIKey on the
		// wire, so a key lingering in config.yaml can never act as a silent
		// fallback. RequireStreaming marks the subscription endpoint
		// (streaming calls only).
		TokenSource:      tokenSource,
		RequireStreaming: requireStreaming,
	}
}

// subscriptionAuthEntry resolves one entry's auth shape from the marker and
// the seam. Marker off (api_key mode): (nil, false) unconditionally — the
// historical entry, byte-for-byte, whatever the seam carries (the AC that
// api_key configs never change behavior). Marker on + seam serving: the live
// token source (the static APIKey stays on the entry but the middleware
// overrides it per request). Marker on + no serving seam (signed out / not
// wired): a DYNAMIC stand-in consulting the builder's CURRENT seam per
// request (dynamicSubscriptionSource) instead of freezing the signed-out
// posture of build time — models stay visible and requests fail fast with
// the actionable sign-in error, while a later sign-in makes the SAME cached
// session router serve subscription credentials without a rebuild.
func subscriptionAuthEntry(name string, pc BuilderProviderConfig, seam BuilderSubscriptionAuthConfig, b *OrchestratorBuilder) (llm.TokenSource, bool) {
	if !pc.SubscriptionAuth {
		return nil, false
	}
	if seam.serves(name) {
		return seam.TokenSource, true
	}
	return &dynamicSubscriptionSource{builder: b, providerName: name}, true
}

// errSignInRequired is the signed-out subscription-auth failure. It is the
// ACTIONABLE error the task demands: it names the one action that fixes it
// (sign in with ChatGPT) rather than a bare 401 the model or user would have
// to diagnose. It contains no token material and is safe to surface anywhere.
var errSignInRequired = errors.New(
	"the chatgpt provider is set to ChatGPT subscription auth (llm.chatgpt.auth.mode: \"oauth\") " +
		"but no account is signed in — sign in with ChatGPT to send requests through it",
)

// dynamicSubscriptionSource is the request-time-resolved TokenSource handed
// to entries built while the seam served nothing: each Token call re-reads
// the builder's effective seam, delegating to the live source once it serves
// this provider and failing with errSignInRequired otherwise — the stand-in
// behavior, never frozen at build time.
type dynamicSubscriptionSource struct {
	builder      *OrchestratorBuilder
	providerName string
}

// Token implements llm.TokenSource.
func (d *dynamicSubscriptionSource) Token(ctx context.Context) (llm.BearerToken, error) {
	if d.builder != nil {
		seam := d.builder.builderSubscriptionSeam()
		if seam.serves(d.providerName) {
			return seam.TokenSource.Token(ctx)
		}
	}
	return llm.BearerToken{}, errSignInRequired
}

// builderSubscriptionSeam snapshots the builder-level default seam for
// dynamicSubscriptionSource's per-Token resolution (a direct accessor: no
// per-request BuilderConfig allocation, no zero-config sentinel trick).
func (b *OrchestratorBuilder) builderSubscriptionSeam() BuilderSubscriptionAuthConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subscriptionAuth
}

// dialPolicy derives the llmtls.DialPolicy for targetURL: the proxy is active
// when a proxy client exists (the BuildTransport rule) and the target is
// bypassed per proxy.bypass_list. A nil proxyClient short-circuits — no
// proxy, every dial direct, the bypass list irrelevant.
func dialPolicy(proxyClient *http.Client, bypass proxy.BypassMatcher, targetURL string) llmtls.DialPolicy {
	if proxyClient == nil {
		return llmtls.ZeroDialPolicy
	}
	host := hostOf(targetURL)
	return llmtls.DialPolicy{
		ProxyActive:    true,
		TargetBypassed: bypass.Matches(host),
	}
}

// hostOf extracts the hostname (no port); an unparseable URL yields "" which
// matches nothing in the bypass list, so an unusual base URL simply keeps the
// proxy routing.
func hostOf(rawURL string) string {
	return llmtls.TargetHost(rawURL)
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

	return coreRouter, coreReflector, nil
}

// buildContextFactory creates a ContextManagerFactory using the tracking
// caller for compaction summarization (so those tokens count in session
// totals).
func (b *OrchestratorBuilder) buildContextFactory(caller *llm.TrackingCaller, cfg *BuilderConfig, modelRegistry *llm.ModelRegistry, dumpWriter io.Writer, llmRouter *llm.Router) ContextManagerFactory {
	var summarizeCaller agent.LLMCaller = caller
	summarizeCaller = agent.NewDumpCaller(summarizeCaller, dumpWriter, b.logger)

	// Model Profiles context override: tightens the compaction strategy and
	// pruning baselines when both toggles are enabled; per-step pruning
	// overrides (below) still take precedence over the tightened baseline.
	exec := applyContextManagement(cfg.Executor, cfg.ModelProfiles)

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
					// Oneshot service policy: reasoning tier minimal, resolved
					// per call from the session router's ACTIVE model (model
					// switches ride along) — the Model Profiles seed does not
					// reach service calls; temperature pinned to the
					// family-safe floor (deterministic summaries).
					ReasoningEffort: serviceReasoningEffort(bareActiveModel(llmRouter), oneshotTierCompaction),
					CallPurpose:     llm.CallPurposeCompaction,
				}
				summary, err := serviceCall(ctx, b.serviceMetrics, b.log(), ServiceKindCompactionSummary, bareActiveModel(llmRouter), summarizeCaller, req, compactionSummarizeContent, oneshot.Options[string]{})
				if err != nil {
					return "", fmt.Errorf("compaction summarize: %w", err)
				}
				return summary, nil
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

// newJudgeForRouter builds a ToolJudge riding the given router: one-shot
// calls go through the router as a plain llm.Caller with NO model pinned, so
// every call resolves to the ACTIVE provider/model — a session model switch
// is picked up by the next judge call with no re-binding. usageTracker, when
// non-nil, routes judge calls through a TrackingCaller so judge tokens land
// in the session's accounting (nil for the shared-registry fallback judge —
// no session tracker exists at builder level). Returns nil when the router is
// nil — callers treat nil as "keep the previous judge" (fail-safe).
func (b *OrchestratorBuilder) newJudgeForRouter(cfg *BuilderConfig, llmRouter *llm.Router, usageTracker *llm.UsageTracker) *sdktools.ToolJudge {
	if llmRouter == nil {
		return nil
	}

	debugEnabled := b.logger != nil && b.logger.Enabled(context.Background(), slog.LevelDebug)

	return sdktools.NewToolJudgeFromConfig(sdktools.JudgeConfig{
		Caller:       newJudgeDumpCaller(llmRouter, b.logger, debugEnabled),
		UsageTracker: usageTracker,
		MaxCacheSize: cfg.Orchestration.MaxJudgeCacheSize,
	}, b.logger)
}

// rebuildJudgeInternal recreates the SHARED registry's judge — only a
// clone-time fallback for new sessions (Build binds every session clone to a
// judge riding the session's own router, bindSessionJudge). Triggered by a
// default-model change in settings or another session's picker; it never
// re-binds a live session's judge, and the fallback judge is not
// usage-tracked (no session tracker at builder level).
func (b *OrchestratorBuilder) rebuildJudgeInternal(cfg *BuilderConfig, llmRouter *llm.Router) {
	if b.registry == nil {
		return
	}

	if llmRouter == nil {
		// Try building a fresh router
		newRouter, _, err := b.buildRouter(context.Background(), cfg, nil)
		if err == nil && newRouter != nil {
			llmRouter = newRouter
		} else if err != nil && b.logger != nil {
			b.logger.Warn("rebuildJudge: failed to build LLM router for judge", "error", err)
		}
	}

	judge := b.newJudgeForRouter(cfg, llmRouter, nil)

	if judge != nil {
		b.registry.SetJudge(judge)
		if b.logger != nil {
			b.logger.Info("tool judge rebuilt successfully")
		}
	} else if b.logger != nil {
		b.logger.Warn("tool judge rebuild failed: judge will not be available for on-demand evaluation")
	}
}

// bindSessionJudge binds the SESSION registry's judge to the session's OWN
// router, replacing the clone-inherited shared fallback. The judge calls
// through the router with no pinned model, so it rides the ACTIVE
// provider/model by construction: the session's own model switches are
// followed with no re-binding, while a global default-model change rebuilds
// only the shared fallback. usageTracker routes judge tokens into session
// accounting; a nil judge keeps the previous binding (a session never loses a
// judge it had).
func (b *OrchestratorBuilder) bindSessionJudge(cfg *BuilderConfig, llmRouter *llm.Router, sessionRegistry *tools.ToolRegistry, usageTracker *llm.UsageTracker) {
	judge := b.newJudgeForRouter(cfg, llmRouter, usageTracker)
	if judge == nil {
		return
	}
	sessionRegistry.SetJudge(judge)
}

// judgeDumpCaller wraps the session router as the judge's llm.Caller with
// context-aware dump support: with a DumpWriter in the context and DEBUG
// enabled, the call is wrapped with logging + dump layers (provider name
// resolved live). Forwards ActiveModel() so ToolJudge's activeModelSource
// probe keeps resolving the tier-off reasoning spelling and per-model
// advisory cache keys through the wrapper.
type judgeDumpCaller struct {
	router       *llm.Router
	logger       *slog.Logger
	debugEnabled bool
}

// newJudgeDumpCaller wraps the router for context-aware dump support; with
// debug disabled it degenerates to a pass-through (no layers constructed).
func newJudgeDumpCaller(llmRouter *llm.Router, logger *slog.Logger, debugEnabled bool) *judgeDumpCaller {
	return &judgeDumpCaller{router: llmRouter, logger: logger, debugEnabled: debugEnabled}
}

func (c *judgeDumpCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	var caller agent.LLMCaller = c.router
	if c.debugEnabled {
		if dw := agent.DumpWriterFromContext(ctx); dw != nil {
			caller = agent.NewLoggingLLMCaller(caller, c.router.ActiveProviderName(), c.logger)
			caller = agent.NewDumpCaller(caller, dw, c.logger)
		}
	}
	return caller.Call(ctx, req)
}

// ActiveModel forwards the router's active model (composite id).
func (c *judgeDumpCaller) ActiveModel() string { return c.router.ActiveModel() }

// registerSessionRegistry clones the shared registry for a new session and
// records it live; clone + insert are atomic under b.mu so a concurrent
// applySecurityPolicies push cannot slip between: either the clone inherits
// the new group policies or it is already tracked and receives the
// group-policy half of the push. (The autonomy posture is pinned per task —
// RefreshAutonomyPosture at launch — not pushed.) Released by
// unregisterSessionRegistry via the orchestrator cleanup hook.
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

// unregisterSessionRegistry removes a session registry from the live set —
// the cleanup hook wired into every orchestrator Build creates, so tracked
// clones do not outlive their sessions.
func (b *OrchestratorBuilder) unregisterSessionRegistry(r *tools.ToolRegistry) {
	b.mu.Lock()
	delete(b.sessionRegistries, r)
	b.mu.Unlock()
}

// registerSessionModelRegistry records a fresh per-session model registry in
// the live set so UpdateModelOverrides pushes reach it, under b.mu: a push
// either sees the registry or ran entirely before it existed (later sessions
// carry the metadata from construction). The one gap is INSIDE buildRouter —
// the registry is constructed there, outside b.mu, a few statements before
// registration; a push landing there skips the session. The tool registry's
// equivalent closes it by cloning under b.mu, which the model registry cannot
// mirror cheaply — accepted: such a session re-syncs on the NEXT push, and
// only the motivating read-back push exists today.
func (b *OrchestratorBuilder) registerSessionModelRegistry(reg *llm.ModelRegistry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessionModelRegistries == nil {
		b.sessionModelRegistries = make(map[*llm.ModelRegistry]struct{})
	}
	b.sessionModelRegistries[reg] = struct{}{}
}

// unregisterSessionModelRegistry removes a session model registry from the
// live set, sharing the cleanup hook with unregisterSessionRegistry.
func (b *OrchestratorBuilder) unregisterSessionModelRegistry(reg *llm.ModelRegistry) {
	b.mu.Lock()
	delete(b.sessionModelRegistries, reg)
	b.mu.Unlock()
}

// UpdateModelOverrides pushes config-derived tier-1 model metadata into every
// live per-session model registry, so a runtime correction (the motivating
// one: the embedded LLM's /props context read-back landing in llm.models)
// reaches already-open sessions instead of only future ones — the
// model-registry counterpart of the security push. Overrides derive from the
// CURRENT cfg and apply as an upsert (models cfg no longer mentions keep
// their stored entries). The builder-cached router/registry pair is
// deliberately untouched; RebuildRouter replaces it and the backend pairs
// the two calls.
func (b *OrchestratorBuilder) UpdateModelOverrides(cfg *BuilderConfig) {
	overrides := modelOverridesFromConfig(cfg)
	if len(overrides) == 0 {
		return
	}
	// Lock ordering is b.mu → registry mu, the same order the security push
	// and registerSessionModelRegistry use; the registries never call back
	// into the builder.
	b.mu.Lock()
	defer b.mu.Unlock()
	for reg := range b.sessionModelRegistries {
		reg.ApplyOverrides(overrides)
	}
}

// applySecurityPolicies applies group-based security policies to the registry:
// every non-system tool resolves its policy from its capability GROUP (never
// its name — ADR-024); the reserved system group and unknown names are
// skipped defensively.
//
// Split delivery contract (per-task autonomy pinning): the shared registry
// receives the FULL state — group policies, auto-approval, autonomy mode,
// silent-mode sub-policies — while live per-session clones receive ONLY the
// group policies and auto-approval. Group policy is fail-closed posture every
// session shares: a runtime deny must reach already-open sessions too, or it
// would silently fail-open on sessions created before the save (that save's
// execute blocklist reaches them via tool re-registration in the shared sp4rk
// registry the clones embed). The autonomy posture is pinned per task: clones
// inherit at creation and re-sync from the shared registry at task launch
// (fresh send + every resume) via RefreshAutonomyPosture, so a task that
// started interactive can never silently turn unattended mid-run. A
// TIGHTENING save must not fail open either, so each live clone also gets
// ApplyAutonomyPostureIfTightening — a task can never silently BECOME
// unattended, but a revocation reaches it immediately. applySecurityPolicies
// also reconciles ask_user registration on the shared registry
// (registration-scoped, so it follows Settings immediately — a documented
// boundary of the pinning contract). Holds b.mu across the whole update so a
// racing Build cannot miss the new state (registerSessionRegistry).
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
	autonomyMode := cfg.Security.AutonomyMode
	silentMode := tools.SilentModeState{
		ToolConfirm: cfg.Security.SilentMode.ToolConfirm,
		UserConfirm: cfg.Security.SilentMode.UserConfirm,
		StepLimit:   cfg.Security.SilentMode.StepLimit,
		AskUser:     cfg.Security.SilentMode.AskUser,
	}

	// Lock ordering is b.mu → registry mu: registerSessionRegistry clones
	// under b.mu (same order) and registries never call back into the
	// builder, so no reverse order exists.
	b.mu.Lock()
	b.registry.ApplySecurityState(groupPolicies, autoApprove, autonomyMode, silentMode)
	for r := range b.sessionRegistries {
		// Group policies + auto-approval only; the autonomy posture is pinned
		// per task (method doc) — a clone re-syncs an ESCALATION at task
		// launch, never mid-run. The one mid-run exception is DE-ESCALATION:
		// a revoked unattended posture must stop auto-approving immediately,
		// so push it when tighter than the clone's current posture.
		r.ApplyGroupPolicies(groupPolicies, autoApprove)
		r.ApplyAutonomyPostureIfTightening(autonomyMode, silentMode)
	}
	// ask_user lives in the shared sp4rk registry the clones embed, so
	// re-registering reaches live sessions immediately — a runtime
	// silent-mode toggle must not require an app restart.
	b.reconcileAskUser(cfg)
	b.mu.Unlock()
}

// reconcileAskUser (re)registers ask_user on the shared registry with the
// callback the silent-mode ask_user sub-policy selects — the runtime
// counterpart of the build-time registration: silent mode + "disable"
// registers a NIL callback (a call resolves the explicit "not available"
// result, never blocking the agent) instead of a missing-tool error; turning
// silent mode off restores the live callback. Stateless, so re-registration
// is idempotent; a nil askUserFunc (CLI/test builder) is a no-op. The shared
// registry's tools are visible to every session clone.
//
// Callers hold b.mu (lock order b.mu → registry mu).
func (b *OrchestratorBuilder) reconcileAskUser(cfg *BuilderConfig) {
	if b.askUserFunc == nil {
		return
	}
	askUser := b.askUserFunc
	if cfg.Security.AskUserDisabled() {
		askUser = nil
	}
	b.registry.Register(tools.NewAskUserTool(askUser))
}

// parseGroupPolicy maps the security.groups.<group>.policy short enum
// ("allow" | "user_confirm" | "deny", backend/config GroupPolicy* constants)
// onto sdktools policy values; it lives here because core never imports
// backend/config. Anything unrecognized — including empty — fails safe to
// user confirmation.
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

// applyModelProfilesPresets seeds the builder-level reasoning-effort default
// when the ModelProfiles sampling variant is active; the other variant
// effects (loop hardening, sampling) apply lazily in Build/buildRouter via
// the pure helpers applyLoopHardening/resolveSamplingFunc reading the passed
// *BuilderConfig. Master toggle off = no-op (un-profiled baseline).
func (b *OrchestratorBuilder) applyModelProfilesPresets(cfg *BuilderConfig) {
	// Sampling variant with a reasoning effort seeds the builder-level
	// default; per-request overrides (HandleOptions.ReasoningEffort →
	// ApplyRequestOverrides → SetReasoningEffort) still win at request time.
	if cfg.ModelProfiles.Enabled && cfg.ModelProfiles.Sampling.Enabled && cfg.ModelProfiles.Sampling.ReasoningEffort != "" {
		b.reasoningEffort = cfg.ModelProfiles.Sampling.ReasoningEffort
	}
}

// applyLoopHardening overrides circuit-breaker thresholds with the tighter
// ModelProfiles loop-hardening values when the variant (and master toggle)
// are enabled; only thresholds present in the profile are overridden, the
// rest keep their baseline. Variant disabled → breaker unchanged.
func applyLoopHardening(cb agent.CircuitBreakerConfig, s BuilderModelProfilesConfig) agent.CircuitBreakerConfig {
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
// compaction (keep-last, block size, predictive trigger), pruning depth, and
// the output token reserve — when the ModelProfiles context variant (and
// master toggle) are enabled. Each knob overrides independently (zero = keep
// the executor baseline); variant disabled → byte-for-byte unchanged.
func applyContextManagement(exec BuilderExecutorConfig, s BuilderModelProfilesConfig) BuilderExecutorConfig {
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

// resolveSamplingFunc returns the router's SamplingFunc. The per-family
// vendor preset (prompt.DefaultSampling) is always the base; when the
// sampling variant (and master toggle) are enabled, only explicitly set
// (non-zero) fields override it — a fully-unset profile reproduces the
// vendor preset exactly, so enabling the variant never degrades a family to
// hardcoded constants (a constant temperature once broke vendor-tuned
// 27-30B presets). prompt.SamplingConfig converts field-by-field into
// llm.SamplingDefaults because llm cannot import prompt (import cycle
// through prompt's in-package tests). MaxTokens is deliberately not
// forwarded: the router-level preset is sampling-only.
func resolveSamplingFunc(s BuilderModelProfilesConfig) llm.SamplingFunc {
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
		// clobbering it (see BuilderModelProfilesSampling field docs).
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
		if s.Sampling.PresencePenalty > 0 {
			d.PresencePenalty = &s.Sampling.PresencePenalty
		}
		return d
	}
}

// Config conversion helpers.

// configToBuiltinToolsConfig maps BuilderConfig into the tool-registration
// config.
func configToBuiltinToolsConfig(cfg *BuilderConfig) tools.BuiltinToolsConfig {
	// The command blocklist comes from security.groups.execute.blocklist and
	// compiles into the shell-exec tool (a Judge match is a hard escalation
	// naming the pattern). Presence-based, empty by default: explicit-empty
	// and absent both compile to "no patterns" (the loader back-fills the
	// entry, so nil here only affects programmatic configs).
	var shellBlocklist []string
	if execGroup, ok := cfg.Security.Groups[string(sdktools.GroupExecute)]; ok {
		shellBlocklist = execGroup.Blocklist
	}

	return tools.BuiltinToolsConfig{
		FileLimits: builtins.FileLimits{
			ReadDefaultLines: cfg.ToolLimits.ReadDefaultLines,
		},
		RipgrepLimits: builtins.RipgrepLimits{
			Timeout: time.Duration(cfg.Timeouts.RipgrepTimeout) * time.Second,
		},
		GlobLimits: builtins.GlobLimits{
			MaxEntries: cfg.ToolLimits.GlobMaxEntries,
			MaxResults: cfg.ToolLimits.GlobMaxResults,
			Timeout:    time.Duration(cfg.Timeouts.GlobTimeout) * time.Second,
		},
		GlobLimitsExplicit: cfg.ToolLimits.GlobLimitsExplicit,
		WebFetchLimits: builtins.WebFetchLimits{
			Timeout: time.Duration(cfg.Timeouts.WebFetchTimeout) * time.Second,
			Retries: cfg.Timeouts.WebFetchRetries,
		},
		WebSearchLimits: builtins.WebSearchLimits{
			MaxResults: cfg.ToolLimits.WebSearchMaxResults,
			Timeout:    time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,
		},
		BashTimeouts: builtins.BashTimeouts{
			MaxTimeout: time.Duration(cfg.Timeouts.BashMaxTimeout) * time.Second,
			WaitDelay:  time.Duration(cfg.Timeouts.BashWaitDelay) * time.Second,
		},
		ShellBlocklist:      shellBlocklist,
		BashShellInvocation: cfg.ShellExec.BashExec,
		PoshShellInvocation: cfg.ShellExec.PoshExec,
		SearchProvider:      cfg.Search.Provider,
		SearchAPIKey:        cfg.ExpandEnvVars(cfg.Search.APIKey),
		SearchBaseURL:       cfg.ExpandEnvVars(cfg.Search.BaseURL),
		SearchTimeout:       time.Duration(cfg.Timeouts.WebSearchTimeout) * time.Second,

		SilentMode: tools.SilentModeState{
			ToolConfirm: cfg.Security.SilentMode.ToolConfirm,
			UserConfirm: cfg.Security.SilentMode.UserConfirm,
			StepLimit:   cfg.Security.SilentMode.StepLimit,
			AskUser:     cfg.Security.SilentMode.AskUser,
		},
		AutonomyMode: cfg.Security.AutonomyMode,

		MarkitdownPythonPath: cfg.MarkitdownPythonPath,
	}
}

// Standalone model listing helpers (no receiver state needed).

// maxModelsListBodyLen caps the buffered size of a models-listing response
// (GET /v1/models) — a small JSON document; anything larger is a misbehaving
// or hostile endpoint (same rationale as the bounded reads in
// lmstudio_probe.go and embeddedllm/server.go). The 10-second context bounds
// duration, not bytes, so a loopback/LAN endpoint could otherwise push
// unbounded data into the heap.
const maxModelsListBodyLen = 1 << 20

// listOpenAIModels fetches model names from an OpenAI-compatible API;
// httpClient may be nil (SDK default transport) or carry the proxy /
// per-provider TLS pin so the listing reaches self-signed endpoints exactly
// like the chat path (ADR-054). The transport is wrapped in a body-capping
// layer: the SDK buffers the whole response with a bare io.ReadAll, so the
// cap must sit beneath it.
func listOpenAIModels(ctx context.Context, baseURL, apiKey string, httpClient *http.Client) ([]string, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	// Copy the caller's client and cap its response bodies; a nil client gets
	// the default transport, capped the same way.
	bounded := http.Client{}
	if httpClient != nil {
		bounded = *httpClient
	}
	if bounded.Transport == nil {
		bounded.Transport = http.DefaultTransport
	}
	bounded.Transport = &boundedBodyTransport{inner: bounded.Transport, limit: maxModelsListBodyLen}
	opts = append(opts, option.WithHTTPClient(&bounded))

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

// boundedBodyTransport wraps an http.RoundTripper and caps every response
// body at limit bytes: the openai-go SDK reads bodies with a bare io.ReadAll
// and offers no maximum-body option, so an oversized/endless response from a
// UI-configurable /v1/models endpoint would otherwise grow the heap
// unbounded (same class as the capped io.ReadAll in listAnthropicModels).
type boundedBodyTransport struct {
	inner http.RoundTripper
	limit int64
}

func (t *boundedBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		resp.Body = &limitErrorBody{rc: resp.Body, limit: t.limit}
	}
	return resp, nil
}

// limitErrorBody wraps a response body and fails the read once more than
// limit bytes are consumed, so an oversized response surfaces as an
// actionable error instead of an unbounded allocation inside the caller's
// io.ReadAll.
type limitErrorBody struct {
	rc       io.ReadCloser
	limit    int64
	consumed int64
}

func (b *limitErrorBody) Read(p []byte) (int, error) {
	if b.consumed > b.limit {
		return 0, fmt.Errorf("response body exceeds %d bytes", b.limit)
	}
	n, err := b.rc.Read(p)
	b.consumed += int64(n)
	if b.consumed > b.limit && (err == nil || errors.Is(err, io.EOF)) {
		return n, fmt.Errorf("response body exceeds %d bytes", b.limit)
	}
	return n, err
}

func (b *limitErrorBody) Close() error { return b.rc.Close() }

// listAnthropicModels fetches model names from an Anthropic-compatible API
// via a raw GET to {baseURL}/v1/models (the go-anthropic SDK exposes no
// ListModels); the path is normalized, an optional proxy/pinned httpClient is
// honored, and x-api-key/anthropic-version headers go out for a non-empty
// key.
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

	// Cap+1 read: a body larger than the cap means a misbehaving or hostile
	// endpoint — refuse it with an actionable error instead of buffering an
	// unbounded response (the truncated tail is drained and discarded).
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsListBodyLen+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read models response: %w", err)
	}
	if len(body) > maxModelsListBodyLen {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("models response exceeds %d bytes; refusing to buffer it", maxModelsListBodyLen)
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
