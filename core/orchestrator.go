// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/smallllm"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

// goalKeyType is the context key for the active goal state. When present, the
// stored value is a *goal.GoalState that buildSystemPrompt renders into a
// goal-mode prompt section carrying the condition, verify clause, evidence
// mandate, and budget to the agent on every turn of the loop.
type goalKeyType struct{}

// GoalKey is the context key for the active goal. It mirrors PlanModeKey, but
// the stored value carries the whole GoalState rather than acting as a mere
// presence flag, so the prompt builder can read condition/verify/budget/turn-count.
var GoalKey = goalKeyType{}

// WithGoalState returns a context carrying the active goal state under GoalKey.
// The value carries the whole GoalState; buildSystemPrompt renders the goal-mode
// section only when a non-nil goal is present.
func WithGoalState(ctx context.Context, gs *goal.GoalState) context.Context {
	return context.WithValue(ctx, GoalKey, gs)
}

// goalStateFromCtx extracts the active goal state from the context, or nil if no
// goal is active. Used by buildSystemPrompt to decide whether to render the
// goal-mode section.
func goalStateFromCtx(ctx context.Context) *goal.GoalState {
	if gs, ok := ctx.Value(GoalKey).(*goal.GoalState); ok {
		return gs
	}
	return nil
}

// reviewModeKeyType is the context key signaling that the user's message carries
// code review feedback. It is a presence flag (like PlanModeKey), not a value
// carrier: buildSystemPrompt renders a "Code Review" section when it is set,
// instructing the agent to address the comments by editing code.
type reviewModeKeyType struct{}

// ReviewModeKey is the context key for review-feedback mode.
var ReviewModeKey = reviewModeKeyType{}

// WithReviewMode returns a context carrying ReviewModeKey so buildSystemPrompt
// renders the Code Review section for this Conductor run.
func WithReviewMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, ReviewModeKey, struct{}{})
}

// reviewModeFromCtx reports whether review-feedback mode is active for this run.
func reviewModeFromCtx(ctx context.Context) bool {
	return ctx.Value(ReviewModeKey) != nil
}

// DefaultAgentsMDMaxBytes is the default cap on AGENTS.md content injected into
// prompts. AGENTS.md is treated as untrusted, user-controlled input; an
// unbounded read would let a workspace inject arbitrarily large content into
// every system prompt.
const DefaultAgentsMDMaxBytes = 65536

// defaultResumeComplexity is the routing complexity used when a resumed task
// has no persisted routing decision. It selects the standard Conductor mode
// (checklist + optional delegation, no user sign-off plan) so the agent can
// continue making progress without requiring a declared plan.
const defaultResumeComplexity = 3

// agentsMDCacheEntry holds a cached read of AGENTS.md from a workspace.
type agentsMDCacheEntry struct {
	content string
	modTime time.Time
	err     error
}

type injectionDefenseKeyType struct{}

// InjectionDefenseKey is the context key for signaling injection defense is enabled.
// When set to a non-nil bool pointer in the context, buildSystemPrompt reads it to decide
// whether to include the injection defense prompt text.
var InjectionDefenseKey = injectionDefenseKeyType{}

// smallLLMPromptProfile carries the small-LLM SystemPrompt sub-toggle flags
// from prepareRequestContext to buildSystemPromptWith. It is stored under
// SmallLLMLiteKey (a presence flag, like PlanModeKey): when present AND Lite
// is set, buildSystemPromptWith swaps the verbose OrchestratorSystem core
// directive for the compact OrchestratorSystemLite directive, and
// conditionally appends the reasoning scaffold (ReasoningScaffold) and the
// worked-example few-shot block (FewShot). FewShot and ReasoningScaffold are
// only honored when Lite is active, since both are tailored to the lite
// directive's style. The injection-defense and verification sections are
// appended UNCHANGED in both modes (strict constraint).
type smallLLMPromptProfile struct {
	Lite              bool
	FewShot           bool
	ReasoningScaffold bool
}

// smallLLMLiteKeyType is the context key for the small-LLM SystemPrompt
// profile. Its presence signals the variant is active; the carried value is a
// smallLLMPromptProfile with the sub-toggle flags.
type smallLLMLiteKeyType struct{}

// SmallLLMLiteKey is the context key signaling the small-LLM lite prompt profile.
var SmallLLMLiteKey = smallLLMLiteKeyType{}

// WithSmallLLMLite returns a context carrying SmallLLMLiteKey with the full
// profile (lite directive + few-shot examples + reasoning scaffold). It is a
// test/fixture convenience; production wiring uses withSmallLLMPromptProfile
// to carry the actual config-derived flags.
func WithSmallLLMLite(ctx context.Context) context.Context {
	return withSmallLLMPromptProfile(ctx, smallLLMPromptProfile{
		Lite:              true,
		FewShot:           true,
		ReasoningScaffold: true,
	})
}

// withSmallLLMPromptProfile returns a context carrying the small-LLM prompt
// profile under SmallLLMLiteKey. This is the production entry point used by
// prepareRequestContext; it carries the config-derived sub-toggle flags so
// buildSystemPromptWith can gate the lite directive, few-shot examples, and
// reasoning scaffold independently.
func withSmallLLMPromptProfile(ctx context.Context, p smallLLMPromptProfile) context.Context {
	return context.WithValue(ctx, SmallLLMLiteKey, p)
}

// smallLLMLiteFromCtx reports whether the small-LLM lite prompt profile is
// active for this run (the variant is enabled and Lite is on). Used by
// buildSystemPromptWith to decide whether to swap in the compact
// OrchestratorSystemLite directive.
func smallLLMLiteFromCtx(ctx context.Context) bool {
	p, ok := ctx.Value(SmallLLMLiteKey).(smallLLMPromptProfile)
	return ok && p.Lite
}

// smallLLMPromptProfileFromCtx returns the carried small-LLM prompt profile
// and whether one is present. Used by buildSystemPromptWith to read the
// FewShot and ReasoningScaffold sub-toggle flags.
func smallLLMPromptProfileFromCtx(ctx context.Context) (smallLLMPromptProfile, bool) {
	p, ok := ctx.Value(SmallLLMLiteKey).(smallLLMPromptProfile)
	return p, ok
}

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxRedelegationDepth      int    // cap on recursive delegation when allow_redelegate is true (default: 2)
	KeepFirst                 int    // for sliding window compaction
	KeepLast                  int    // for sliding window compaction
	MaxDependencyContextChars int    // max chars for dependency context in delegation tasks (default: 8000)
	Model                     string // active model name for ModelRegistry.Resolve()

	// ReasoningEffort is the reasoning effort applied to step executors.
	// When non-empty, each executor gets this value directly (no role adaptation).
	ReasoningEffort string

	// HITLHandler is called when an executor reaches its step limit.
	// If nil, a default handler is used (deny all extensions).
	HITLHandler agent.HITLHandler

	// PreWarningPercent is the context fill % that triggers the pre-compaction
	// store_fact nudge. When fill reaches this threshold (but below predictive),
	// a warning listing vulnerable tool outputs is appended to the observation.
	PreWarningPercent int

	// InjectionDefenseEnabled gates the prompt injection defense prompt text
	// and the <untrusted-content> wrapping of tool outputs in the context window.
	InjectionDefenseEnabled bool

	// AgentsMDMaxBytes caps the AGENTS.md content size injected into prompts.
	// 0 means use the default (DefaultAgentsMDMaxBytes = 65536).
	// A negative value disables the cap entirely.
	// The cap applies to the combined content of all AGENTS.md sources.
	AgentsMDMaxBytes int

	// AgentsMDSearchPaths holds extra absolute paths (outside the workspace)
	// to search for AGENTS.md files, in priority order. Content from these
	// paths is concatenated ahead of the workspace-root AGENTS.md, so the
	// effective order is: searchPaths[0], searchPaths[1], …, workspace root.
	// Each path points directly at an AGENTS.md file. Missing files are
	// silently skipped. The combined content is capped by AgentsMDMaxBytes.
	AgentsMDSearchPaths []string

	// ConductorHistoryWindow is the maximum number of recent conversation
	// messages injected into the Conductor's context. 0 = use default (20).
	// Keeps the prompt bounded for long sessions while preserving enough
	// dialogue context for the agent to understand follow-up references.
	ConductorHistoryWindow int

	// GoalLoop holds settings for the goal-derivation / verification loop.
	// GoalLoop.Verification gates the independent verifier turn; "off"
	// disables it so the loop relies solely on the agent's own verdict.
	GoalLoop GoalLoopSettings

	// SmallLLM holds the small-LLM optimization settings. When Enabled, the
	// profile activates variant behaviors (essential-tools narrowing, prompt
	// lite swap, loop hardening, sampling) — each variant independently gated
	// by BOTH the master Enabled toggle and its own sub-toggle
	// (defense-in-depth). Inert when the master toggle is disabled.
	SmallLLM SmallLLMSettings
}

// GoalLoopSettings mirrors the config-layer GoalLoopConfig for the
// orchestrator's runtime config field. Verification is "independent"
// (default) or "off".
type GoalLoopSettings struct {
	Verification string
}

// SmallLLMSettings is the runtime mirror of BuilderSmallLLMConfig, carrying
// the small-LLM variant configuration to the orchestrator. The master Enabled
// toggle gates every variant (defense-in-depth): when false, no variant
// activates regardless of its sub-toggle.
type SmallLLMSettings struct {
	Enabled        bool
	EssentialTools SmallLLMEssentialSettings
	SystemPrompt   SmallLLMSystemPromptSettings
}

// SmallLLMEssentialSettings holds the always-present tool-set narrowing settings.
type SmallLLMEssentialSettings struct {
	Enabled bool
	// AlwaysPresent is the user-pinned list of tool names always exposed when
	// this variant is active, regardless of routing. Protected orchestration
	// tools (finish, fact memory, ask_user) and all MCP tools are kept
	// additionally by SelectTools.
	AlwaysPresent []string
	// MaxTools caps the total number of tools exposed after selection.
	MaxTools int
}

// SmallLLMSystemPromptSettings holds the prompt-simplification variant
// settings. Lite is the variant master toggle (there is no separate Enabled —
// it mirrors config.SystemPromptConfig, where Lite itself gates the variant).
// FewShot and ReasoningScaffold are independent sub-toggles only honored when
// Lite is active.
type SmallLLMSystemPromptSettings struct {
	Lite bool
	// FewShot appends the worked-example ReAct block (requires Lite).
	FewShot bool
	// ReasoningScaffold appends the three-step thought template (requires Lite).
	ReasoningScaffold bool
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// pruningOverrides, when provided, override the global pruning configuration.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager

// BlackboardFactory creates a Blackboard for a new task.
// The taskID is a unique identifier for the orchestration request.
type BlackboardFactory func(taskID string) orchestration.Blackboard

// Orchestrator coordinates the agent's reasoning cycle.
// It handles c0wrk-specific concerns (routing, PersistentBlackboard,
// conversation history) and delegates the Plan&Execute loop to the sp4rk engine.
//
// Lifecycle: One Orchestrator is created per active session via Builder.Build().
// It lives for the duration of the session and is discarded when the session ends.
//
// Concurrency: Only one HandleMessage call may be active at a time per Orchestrator
// instance. The session manager enforces this contract — concurrent requests to the
// same session are rejected before reaching HandleMessage.
type Orchestrator struct {
	router              *router.Router
	llm                 agent.LLMCaller
	modelSwitcher       *llm.Router // raw LLM router for per-message model override
	toolRegistry        *sdktools.ToolRegistry
	toolExec            agent.ToolExecutor  // executor tool surface (per-session policy view)
	coreToolRegistry    *tools.ToolRegistry // core registry with policy support
	config              OrchestratorConfig
	contextFactory      ContextManagerFactory
	logger              *slog.Logger
	emitter             Emitter
	modelRegistry       *llm.ModelRegistry
	localModelProbe     LocalModelProbe // lazily probes OpenAI-compatible endpoints (LM Studio/vLLM/…) for the runtime context window
	bbFactory           BlackboardFactory
	conversationHistory []llm.Message
	taskStore           TaskPersistence       // optional, for ContinueTask blackboard restoration
	bbRestoreFunc       BlackboardRestoreFunc // optional, restores PersistableBlackboard from store
	trackingCaller      *llm.TrackingCaller   // for per-step context tracker wiring
	tokenCounter        llm.TokenCounter      // for token counting in planner history compaction
	vectorSearchFunc    builtins.VectorSearchFunc
	skillManager        *skills.SkillManager // for skill discovery and activation
	agentManager        *agents.AgentManager // for subagent discovery (drives "Available/Requested Subagents" prompt sections)

	// Conductor dependencies (stored at construction time for runConductor).
	reflector        *reflector.Reflector
	providerName     string
	stepDumpTracker  *orchestration.StepDumpTracker
	toolCache        *agent.ToolResultCache
	perToolTrunc     map[string]agent.ToolTruncationConfig
	toolResultBudget agent.ToolResultBudget
	circuitBreaker   agent.CircuitBreakerConfig

	// isNoProject is set to true when this orchestrator runs inside the
	// "No Project" pseudo-project. When true, the routing domain is
	// overridden from "code" to "general" so that code-oriented planning
	// and execution strategies are not applied.
	isNoProject bool

	// requestInFlight enforces the documented "one active request per
	// *Orchestrator" invariant. HandleMessage CompareAndSwap's it to true on
	// entry; a concurrent caller observes false->true refused and gets
	// ErrRequestInFlight back. The defer in HandleMessage stores false again.
	requestInFlight atomic.Bool

	// agentsMDCache caches AGENTS.md content per workspace path with mtime
	// check to avoid repeated disk reads on every request. Only accessed from
	// injectVectorSearchHints called from HandleMessage, which is
	// single-flight per instance.
	agentsMDCache map[string]agentsMDCacheEntry

	// goalProposer is the backend hook that submits a {condition, verify}
	// goal proposal to the user and blocks for approval. Injected by the
	// builder (via the GoalProposerSetter interface or a constructor field);
	// zero-value (nil) means goal derivation cannot run — deriveGoal returns
	// an error, so opts.Goal on HandleMessage fails fast rather than
	// silently running a non-goal Conductor pass.
	goalProposer tools.GoalProposer

	// goalTurnRunner runs ONE turn of the goal loop (route→plan→Conductor for
	// turn 1; reused-routing ReAct→Conductor for turn >1) and reports how many
	// tool calls the turn made plus its result. It is the single seam through
	// which the loop drives the Conductor, kept as a field so tests can inject
	// a mock that returns canned verdicts/tool-call counts without spinning up
	// the full routing+LLM+executor stack. The default (nil) resolves to
	// defaultGoalTurnRunner, which reuses runConductor under the hood.
	goalTurnRunner func(ctx context.Context, turn int, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, deps conductorDeps) (toolCallCount int, result *orchestration.ExecutionResult, err error)

	// goalVerifier is the independent verifier that re-checks an agent's "met"
	// goal verdict. When the goal loop reaches a "met" verdict and independent
	// verification is configured (config.GoalLoop.Verification == "independent"),
	// the verifier runs an ISOLATED Conductor pass — bounded by the same
	// complexity-derived budget as a normal executor run, restricted to a
	// read-only/test toolset — that inherits the active skills +
	// project-context prefix via the goal-verification system prompt. It
	// reports a structured outcome through
	// declare_verification.
	//
	// It runs on a FRESH blackboard (decoupled from the still-active goal
	// task's blackboard), seeded with lastTurnOutput (the met turn's work
	// product) so the verifier's own read_final_result returns the real work.
	// It branches on gs.VerificationMode: executable re-runs the verify clause;
	// re_derivation delegates a fresh read-only run of the goal's process.
	//
	// It is the single seam through which the loop drives verification, kept as
	// a field so tests can inject a mock that returns a canned outcome without
	// spinning up the full Conductor stack. The default (nil) resolves to
	// defaultGoalVerifier, which reuses RunConductor under the hood (mirroring
	// goalTurnRunner). It carries message + lastTurnOutput + bb so it can drive
	// RunConductor on an isolated blackboard seeded with the met turn's work
	// product (the verifier reads it via read_final_result / step outputs to
	// inspect the claimed artifacts).
	goalVerifier func(ctx context.Context, gs *goal.GoalState, verdict *goal.Verdict, message string, lastTurnOutput string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, deps conductorDeps) (*tools.VerificationOutcome, error)

	// activePause is the universal pause signal for the currently-running
	// conductor (any mode), if any. PauseSession loads the pointer and sets the
	// atomic; the conductor's executor polls it at every step boundary (via the
	// pause-checker wired through ConductorConfig.PauseChecker). It is swapped
	// in at request entry (HandleMessage/Resume) and cleared (nil) on exit so a
	// stale signal from a prior request cannot pause a future one.
	//
	// The pointer field itself is an atomic.Pointer so the cross-goroutine read
	// in PauseSession (a Wails-RPC goroutine) and the write in the request
	// goroutine stay race-free. The single-flight requestInFlight flag serializes
	// HandleMessage/Resume calls against each other, but does NOT cover
	// PauseSession, which runs independently — hence the atomic pointer. The
	// *atomic.Bool it points to is, of course, atomic.
	activePause atomic.Pointer[atomic.Bool]
}

// ErrRequestInFlight is returned by HandleMessage when another HandleMessage
// call on the same *Orchestrator is already running. The orchestrator is
// designed for one concurrent request per instance — the session layer is
// responsible for serializing per-session.
var ErrRequestInFlight = errors.New("orchestrator: request already in flight")

// installPauseSignal installs a fresh pause signal for the duration of the
// current request and returns a clear function to defer. Every conductor run
// launched during the request (normal path or goal loop) receives a
// pause-checker (see newPauseChecker) that reads this signal at each step
// boundary. PauseSession flips the signal from an independent goroutine; the
// executor then returns ErrPaused, which the Conductor maps to
// ExecutionStatusPaused — the request persists the task as paused and exits,
// releasing the single-flight lock so a later Resume can re-enter.
//
// Installing a fresh signal per request (rather than reusing one) guarantees a
// stale true value from a prior request can never pause a future one.
func (o *Orchestrator) installPauseSignal() func() {
	pause := &atomic.Bool{}
	o.activePause.Store(pause)
	return func() { o.activePause.Store(nil) }
}

// PauseSession signals the currently-running conductor (any mode) to pause at
// the next step boundary. It is a no-op when no request is in flight (no
// signal installed). The pause is cooperative: the executor's pause-checker
// observes the signal at the top of its next iteration, stops with ErrPaused,
// and the request path persists the task as paused + releases single-flight so
// a later Resume can re-enter.
func (o *Orchestrator) PauseSession() {
	if p := o.activePause.Load(); p != nil {
		p.Store(true)
	}
}

// newPauseChecker returns a cooperative pause-checker closure suitable for
// ConductorConfig.PauseChecker. It reads the active request's pause signal
// live: when PauseSession flips it, the next step-boundary check returns true
// and the conductor stops. With no signal installed (no request in flight) it
// always returns false — the default non-pausing behavior — so installing it
// unconditionally on every conductor run is safe.
func (o *Orchestrator) newPauseChecker() func(context.Context) bool {
	return func(context.Context) bool {
		if p := o.activePause.Load(); p != nil {
			return p.Load()
		}
		return false
	}
}

// emitSessionPaused surfaces a cooperative pause to the user. It emits a
// service message (carrying a "pause" phase so the frontend can style it) and
// relies on the existing resumable-task safety net to expose a Resume action:
// the paused task is left in_progress by persistTaskOutcome, so
// emitResumableIfUnfinished (driven by taskCompletionInfo mapping Paused to a
// non-success completion) offers the resume button. Nil-safe like all emitters.
func (o *Orchestrator) emitSessionPaused() {
	if o.emitter == nil {
		return
	}
	o.emitter.ServiceWithMeta("Task paused — use Resume to continue.", map[string]any{"phase": "pause"})
}

// LocalModelProbe is a best-effort, non-blocking hook that discovers the real
// context window for a model served from an OpenAI-compatible endpoint (LM
// Studio, vLLM, TGI, Ollama — local/LAN or self-hosted on a public host) and
// writes the result into the session's ModelRegistry.
//
// It is a no-op for models whose provider is not OpenAI-compatible, and a
// harmless no-op for a genuine cloud provider (whose /v1/models listing omits
// the context-window field, so nothing is discovered). The orchestrator
// invokes it when a model is selected — once for the session's default model
// at construction, and again on every mid-chat model switch — so token
// budgets reflect the runtime context window without paying the probe cost at
// app startup or blocking the request path.
//
// Implementations must be safe for concurrent use and must return quickly
// (the network probe, if any, runs on an internal goroutine with a detached
// context so it is not tied to the caller's request lifetime).
type LocalModelProbe func(model string)

// OrchestratorDeps holds the runtime dependencies for the Orchestrator.
// Grouping them into a single struct improves readability when the constructor
// is called (one struct literal instead of 19 positional arguments).
type OrchestratorDeps struct {
	Router           *router.Router
	LLM              agent.LLMCaller
	ToolExec         agent.ToolExecutor
	ToolRegistry     *sdktools.ToolRegistry
	TokenCounter     llm.TokenCounter
	ContextFactory   ContextManagerFactory
	Reflector        *reflector.Reflector // optional, nil-safe
	Logger           *slog.Logger         // optional, nil-safe
	Emitter          Emitter              // optional, uses noopEmitter if nil
	ModelRegistry    *llm.ModelRegistry   // optional, nil-safe
	ToolResultBudget agent.ToolResultBudget
	CircuitBreaker   agent.CircuitBreakerConfig
	BBFactory        BlackboardFactory         // optional, nil = default MapBlackboard
	TrackingCaller   *llm.TrackingCaller       // optional, for per-step context tracker wiring
	VectorSearchFunc builtins.VectorSearchFunc // optional, for auto-RAG hint generation
	SkillManager     *skills.SkillManager      // optional, for skill discovery and activation
	AgentManager     *agents.AgentManager      // optional, for subagent discovery ("Available/Requested Subagents" prompt sections); nil-safe
	CoreToolRegistry *tools.ToolRegistry       // core tool registry for skill policy overrides
	ModelSwitcher    *llm.Router               // raw LLM router for per-message model override

	// Tool result caching and per-tool truncation.
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig

	// StepDumpTracker manages per-step LLM dump files. Created by the session layer.
	// If nil, per-step dumps are disabled.
	StepDumpTracker *orchestration.StepDumpTracker

	// ProviderName is the active provider name for DEBUG-level LLM logging
	// in per-step callers (LoggingLLMCaller wrapping).
	ProviderName string

	// LocalModelProbe lazily discovers the runtime context window for an
	// OpenAI-compatible model (LM Studio/vLLM/TGI/Ollama — local/LAN or
	// self-hosted on a public host) and feeds it to ModelRegistry. Nil when no
	// OpenAI-compatible providers are configured — in that case mid-chat model
	// switches and the default model are not probed (they fall back to the
	// registry's built-in/override metadata). Optional, nil-safe.
	LocalModelProbe LocalModelProbe
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(cfg OrchestratorConfig, deps OrchestratorDeps) *Orchestrator {
	if cfg.MaxRedelegationDepth == 0 {
		cfg.MaxRedelegationDepth = 2
	}
	if cfg.KeepFirst == 0 {
		cfg.KeepFirst = 3
	}
	if cfg.KeepLast == 0 {
		cfg.KeepLast = 10
	}
	if cfg.MaxDependencyContextChars == 0 {
		cfg.MaxDependencyContextChars = 8000
	}
	if cfg.ConductorHistoryWindow == 0 {
		cfg.ConductorHistoryWindow = 20
	}
	emitter := deps.Emitter
	if emitter == nil {
		emitter = &noopEmitter{}
	}

	o := &Orchestrator{
		router:           deps.Router,
		llm:              deps.LLM,
		modelSwitcher:    deps.ModelSwitcher,
		toolRegistry:     deps.ToolRegistry,
		toolExec:         deps.ToolExec,
		config:           cfg,
		contextFactory:   deps.ContextFactory,
		logger:           deps.Logger,
		emitter:          emitter,
		modelRegistry:    deps.ModelRegistry,
		localModelProbe:  deps.LocalModelProbe,
		bbFactory:        deps.BBFactory,
		trackingCaller:   deps.TrackingCaller,
		tokenCounter:     deps.TokenCounter,
		vectorSearchFunc: deps.VectorSearchFunc,
		skillManager:     deps.SkillManager,
		agentManager:     deps.AgentManager,
		coreToolRegistry: deps.CoreToolRegistry,
		reflector:        deps.Reflector,
		providerName:     deps.ProviderName,
		stepDumpTracker:  deps.StepDumpTracker,
		toolCache:        deps.ToolCache,
		perToolTrunc:     deps.PerToolTruncation,
		toolResultBudget: deps.ToolResultBudget,
		circuitBreaker:   deps.CircuitBreaker,
	}

	return o
}

// Cleanup releases all resources held by the orchestrator (e.g., per-step dump files).
// Idempotent — safe to call multiple times.
func (o *Orchestrator) Cleanup() {
	// StepDumpTracker cleanup is owned by the session layer; the orchestrator
	// only holds a reference for per-step dump wiring. No-op here.
}

// SetGoalProposer injects the goal-proposer hook (the desktop approval flow
// that propose_goal blocks on) after construction. Implements
// GoalProposerSetter so the backend session layer can wire the proposer once
// its pending-confirmation channel + emitter are available. Without a
// proposer, goal derivation (runGoalLoop) fails fast.
func (o *Orchestrator) SetGoalProposer(proposer tools.GoalProposer) {
	o.goalProposer = proposer
}

// compile-time assertion that *Orchestrator satisfies GoalProposerSetter.
var _ GoalProposerSetter = (*Orchestrator)(nil)

// logInfo logs an INFO level message if logger is not nil.
func (o *Orchestrator) logInfo(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Info(msg, args...)
	}
}

// mergeSkillNames combines router-matched skill names with user-specified skill names,
// deduplicating by name. Router-matched names come first, user names add any extras.
func mergeSkillNames(routerMatched, userSpecified []string) []string {
	if len(routerMatched) == 0 && len(userSpecified) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(routerMatched)+len(userSpecified))
	merged := make([]string, 0, len(routerMatched)+len(userSpecified))
	for _, name := range routerMatched {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	for _, name := range userSpecified {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	return merged
}

// buildSkillAugmentedRoutingMessage constructs a routing-specific message that
// restores the skill invocation context stripped by preprocessMessageText.
// When the user types "/vibespec-check entire codebase", the preprocessor
// strips the /skill ref, leaving "entire codebase". The router would classify
// this without context, producing unreliable domain/complexity. This method
// prepends the skill invocation with its description so the router can
// classify based on the actual task semantics.
func (o *Orchestrator) buildSkillAugmentedRoutingMessage(message string, userSkills []string) string {
	parts := make([]string, 0, len(userSkills))
	for _, name := range userSkills {
		if s, ok := o.skillManager.Get(name); ok {
			parts = append(parts, fmt.Sprintf("/%s (skill: %s)", name, s.Metadata.Description))
		} else {
			parts = append(parts, "/"+name)
		}
	}

	// Reconstruct: "/vibespec-check (skill: ...) entire codebase"
	prefix := strings.Join(parts, " ")

	if message == "" {
		return prefix
	}
	return prefix + " " + message
}

// resolveTaskMessage returns the effective task message used for everything
// downstream of routing: the blackboard's original request, the Conductor's
// task, and the conversation-history recording.
//
// When the user invokes a skill via /skill-name, preprocessMessageText strips
// the reference — potentially leaving an empty message. That empty message
// would (a) produce a request with only system messages, which some providers
// reject with HTTP 400 "messages parameter is illegal", and (b) get recorded
// as an empty user turn in the conversation history, risking the same failure
// on subsequent turns. This restores the skill context so the Conductor
// receives a meaningful task — symmetrically with the router's own
// augmentation in routeAndActivateSkills (which calls
// buildSkillAugmentedRoutingMessage directly).
//
// The router itself is deliberately excluded from using this: it must receive
// the raw preprocessed message and augment internally, otherwise the skill
// reference would be double-prefixed.
func (o *Orchestrator) resolveTaskMessage(message string, userSkills []string) string {
	if len(userSkills) == 0 || o.skillManager == nil {
		return message
	}
	return o.buildSkillAugmentedRoutingMessage(message, userSkills)
}

// augmentWithAttachments appends a list of the session's attached files to the
// user message so the LLM knows which attachments are available and can request
// their content via the read_attachment tool. Only the Conductor sees this
// section — the clean (un-augmented) message is still recorded in the
// conversation history, so prior turns don't accumulate repeated attachment
// listings. On continuation turns the blackboard is restored with all session
// attachments, so every turn sees the full current set.
func (o *Orchestrator) augmentWithAttachments(message string, bb orchestration.Blackboard) string {
	attachments := bb.GetAttachments()
	if len(attachments) == 0 {
		return message
	}
	var sb strings.Builder
	sb.WriteString(message)
	if message != "" {
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Attached files\n\n")
	sb.WriteString("The following files are attached to this conversation. ")
	sb.WriteString("Call `read_attachment` with the attachment_id to read a file's content.\n\n")
	for _, a := range attachments {
		sb.WriteString("- attachment_id: ")
		sb.WriteString(a.ID)
		sb.WriteString(" | ")
		sb.WriteString(a.OriginalName)
		sb.WriteString(" (")
		sb.WriteString(a.Format)
		sb.WriteString(", ")
		sb.WriteString(strconv.FormatInt(a.SizeBytes, 10))
		sb.WriteString(" bytes)\n")
	}
	return sb.String()
}

// logDebug logs a DEBUG level message if logger is not nil.
func (o *Orchestrator) logDebug(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Debug(msg, args...)
	}
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
//
// Error semantics: when the sp4rk engine returns orchestration.ErrExecutionIncomplete with
// a non-nil ExecutionResult, Resume returns a valid *HandleResult alongside the error.
// This indicates partial success — the task made progress but did not finish (e.g., step
// limit reached). Callers should check errors.Is(err, orchestration.ErrExecutionIncomplete)
// and use the returned HandleResult for partial output, plan state, and blackboard.
// All other errors indicate complete failure; the task is marked failed and nil is returned.
// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
//
// nudge is an optional user message injected on resume (resume-with-nudge):
// when non-empty it is threaded to the Conductor's PendingUserInterjection so
// it lands as the final user message next to the pending tool result in the
// very first resumed LLM call. Empty resumes silently from the checkpoint.
//
// Error semantics: when the sp4rk engine returns orchestration.ErrExecutionIncomplete with
// a non-nil ExecutionResult, Resume returns a valid *HandleResult alongside the error.
// This indicates partial success — the task made progress but did not finish (e.g., step
// limit reached). Callers should check errors.Is(err, orchestration.ErrExecutionIncomplete)
// and use the returned HandleResult for partial output, plan state, and blackboard.
// All other errors indicate complete failure; the task is marked failed and nil is returned.
// A cooperative pause (agent.ErrPaused) is handled as a clean checkpoint: the task is
// persisted as paused (resumable), session_paused is emitted, and Resume returns the
// HandleResult with a nil error and Status=ExecutionStatusPaused.
func (o *Orchestrator) Resume(ctx context.Context, bb orchestration.Blackboard, routing *router.RoutingDecision, plansDir string, resumeSteps []agent.Step, goalState *goal.GoalState, nudge string) (result *HandleResult, err error) {
	o.logDebug("orchestrator: resume started", "resumeSteps", len(resumeSteps), "nudge", nudge != "")

	// Install the universal pause signal for this resumed request. Every
	// conductor run reads it via the pause-checker at each step boundary;
	// PauseSession flips it. Mirrors HandleMessage so a resumed task is
	// pauseable exactly like a fresh one.
	defer o.installPauseSignal()()

	// Record the resumed execution's outcome in the in-memory conversation
	// history so future routing and continuation planning see this exchange.
	// The user message that spawned the task was recorded when the task first
	// ran (or restored from the message store after a restart).
	defer func() {
		o.recordResumeOutcome(ctx, bb, result, err)
	}()

	// Generate RAG hints from vector index using the original request.
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		ctx = o.injectVectorSearchHints(ctx, origReq)
	}

	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetEmitter(o.emitter)
	}
	o.wireAttachmentNameResolver(bb)

	// Emit initial context_fill
	o.emitInitialContextFill()

	// Resolve effective domain/complexity for the resumed execution. The task
	// is NOT re-routed: a persisted routing decision is reused so the system
	// prompt and compaction strategy match the original run. When no routing
	// was persisted (nil), the Conductor defaults to the "general" domain so
	// it runs in standard mode without requiring a plan.
	domain := "general"
	complexity := defaultResumeComplexity
	if routing != nil {
		if routing.Domain != "" {
			domain = routing.Domain
		}
		if routing.Complexity > 0 {
			complexity = routing.Complexity
		}
	}
	ctx = WithDomain(ctx, domain)
	ctx = WithComplexity(ctx, complexity)

	o.logInfo("resume_task", "reflections", len(bb.GetReflections()), "domain", domain, "complexity", complexity)

	// Delegate to the Conductor. The restored blackboard carries facts and
	// step results from the prior run; the Conductor reads them via tools
	// (search_facts, read_step_output) and continues toward completion. The
	// prior trajectory (resumeSteps) is seeded into the ContextManager and the
	// Executor so the resumed run sees the full history and continues the step
	// counter from where it left off. A plan is NOT required — the Conductor
	// handles plan-less tasks via a standalone checklist.
	availableTools := o.toolRegistry.ListFiltered(o.disabledToolNames())

	// If this task was running a goal loop that was paused (or is still active),
	// re-enter the goal loop instead of the plain Conductor path. The prior
	// trajectory (resumeSteps) is seeded into the executor so the resumed goal
	// loop continues the step counter/history from the checkpoint. A nil or
	// terminal goal state falls through to the normal resume path.
	if goalState != nil && !goalState.Status.IsTerminal() {
		o.logInfo("resume_task: resuming goal loop", "status", goalState.Status, "turn", goalState.TurnCount)
		return o.resumeGoalLoop(ctx, bb.GetOriginalRequest(), bb, availableTools, plansDir, routing, goalState, resumeSteps, nudge)
	}

	// Goal-mode-only tools exist solely for goal mode and must not reach a
	// non-goal resumed Conductor run. The goal-loop resume path above receives
	// the unstripped list; strip them here for the normal resume path.
	availableTools = tools.StripGoalModeTools(availableTools)

	// Reconstruct image content blocks from the conversation history so the
	// resumed task sees the same images as the original run. The conversation
	// history (in-memory or restored from the DB via convertChatMessagesToLLM)
	// carries the user message with ContentBlocks; we find the message
	// matching the original request and extract its image blocks. The text
	// block is rebuilt from the blackboard via augmentWithAttachments so it
	// reflects the current attachment state. Without this, a resumed
	// image-bearing task would lose its images (SetTask is text-only).
	var resumeContentBlocks []llm.ContentBlock
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		if imageBlocks := imageBlocksForRequest(o.conversationHistory, origReq); len(imageBlocks) > 0 {
			conductorMessage := o.augmentWithAttachments(origReq, bb)
			resumeContentBlocks = buildContentBlocks(conductorMessage, imageBlocks)
		}
	}

	execResult, err := o.runConductor(ctx, bb.GetOriginalRequest(), bb, availableTools, plansDir, nil, resumeSteps, resumeContentBlocks, nudge)
	// Cooperative pause: a clean, recoverable checkpoint — not a failure.
	// Surface it, persist the task as resumable (persistTaskOutcome below),
	// and return the paused result with a nil error so the backend treats it
	// as a non-success completion (surfacing a Resume action) rather than an
	// error.
	if errors.Is(err, agent.ErrPaused) {
		o.emitSessionPaused()
		err = nil
	}
	var incompleteErr error
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	incompleteErr = err
	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	result = &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Plan:            execResult.Plan,
		Blackboard:      execResult.Blackboard,
		Reflections:     execResult.Reflections,
		Status:          execResult.Status,
	}

	// Persist the task outcome according to the typed execution status.
	// Partial executions stay resumable; see persistTaskOutcome.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		persistTaskOutcome(pbb, execResult)
	}

	o.logDebug("orchestrator: resume completed")
	// Propagate ErrExecutionIncomplete alongside the best-effort result, as
	// documented: callers must errors.Is-check and still use the result.
	return result, incompleteErr
}

// lastReasoningContent extracts the last non-empty reasoning content from the
// blackboard's step results. This is needed so that assistant messages echoed
// back to reasoning models (e.g. DeepSeek) include the reasoning_content field
// those models require.
func lastReasoningContent(bb orchestration.Blackboard) string {
	var last string
	for _, sr := range bb.GetAllStepResults() {
		for _, step := range sr.Steps {
			if step.ReasoningContent != "" {
				last = step.ReasoningContent
			}
		}
	}
	return last
}

// injectVectorSearchHints queries the vector index for relevant files and
// attaches the results as VectorSearchHints on the returned context.
// It also reads AGENTS.md from the workspace root and injects its full
// content via WithAgentsMD, as well as prepending it as the first hint.
// If the vector search function is nil or the search fails, hints are
// still injected if AGENTS.md exists. Hints are a nice-to-have, not critical.
func (o *Orchestrator) injectVectorSearchHints(ctx context.Context, query string) context.Context {
	var hints *VectorSearchHints

	// Vector search (optional, non-blocking). Skipped entirely for No
	// Project (CHAT mode), where the vector index is disabled: no
	// collection is built, so querying would only yield stale results from
	// a previously-active CODE project. AGENTS.md injection below is
	// unaffected (it is workspace-local, not vector-index-dependent).
	if !o.isNoProject && o.vectorSearchFunc != nil {
		ragCtx, ragCancel := context.WithTimeout(ctx, 2*time.Second)
		results, err := o.vectorSearchFunc(ragCtx, builtins.VectorSearchOptions{Query: query, TopK: 5})
		ragCancel()

		if err != nil {
			o.logDebug("vector search hints skipped", "error", err)
		} else if len(results) > 0 {
			hints = &VectorSearchHints{}
			for _, r := range results {
				summary := strutil.TruncateUTF8(r.Content, 100)
				hints.Files = append(hints.Files, VectorSearchHint{
					FilePath: r.FilePath,
					Summary:  summary,
				})
			}
			o.logDebug("vector search hints injected", "count", len(hints.Files))
		}
	}

	// Read AGENTS.md from all configured sources and concatenate in priority
	// order: search paths (global → c0wrk-specific) first, then the
	// workspace-root file (project-specific). Each source is cached per path
	// with an mtime check; missing files are silently skipped.
	agentsMDPaths := o.collectAgentsMDPaths(ctx)
	var agentsParts []string
	for _, p := range agentsMDPaths {
		contentStr, err := o.readAgentsMD(p)
		if err != nil {
			o.logDebug("AGENTS.md not found", "path", p)
			continue
		}
		o.logDebug("AGENTS.md found and injected", "path", p, "size", len(contentStr))
		agentsParts = append(agentsParts, contentStr)
	}

	if len(agentsParts) > 0 {
		combined := strings.Join(agentsParts, "\n\n")
		// Re-apply the cap to the combined content: individual files were
		// capped already, but concatenation may push the total over the limit.
		combined = o.capAgentsMD(combined)
		ctx = WithAgentsMD(ctx, &AgentsMD{Content: combined})

		// Prepend AGENTS.md as the first hint so it always appears in
		// the "Relevant Project Files" section for executors.
		if hints == nil {
			hints = &VectorSearchHints{}
		}
		summary := strutil.TruncateUTF8(combined, 100)
		agentsHint := VectorSearchHint{
			FilePath: "AGENTS.md",
			Summary:  summary,
		}
		hints.Files = append([]VectorSearchHint{agentsHint}, hints.Files...)
	}

	if hints != nil && len(hints.Files) > 0 {
		ctx = WithVectorSearchHints(ctx, hints)
	}

	// Build the research-awareness snapshot (RESEARCH mode only) alongside the
	// AGENTS.md injection above. Best-effort: a parse failure is logged and
	// leaves the context without a research block — it never breaks the task.
	ctx = o.injectResearchContext(ctx)

	return ctx
}

// injectResearchContext builds the research-awareness snapshot for RESEARCH
// mode and attaches it to the context, so buildSystemPrompt and the router can
// render a concise context block. It is a no-op outside RESEARCH mode.
//
// The snapshot is derived from the parsed research catalog (research-root path,
// the current active R-NNN from the index, and a phase hint from the
// hypothesis-graph metrics). All methodology rules stay in the research-*
// skill bodies — this is pure awareness, never policy. Best-effort: a missing
// or unreadable root, or a parse error, is logged and leaves the context
// unchanged (no research block) so the task is never broken.
func (o *Orchestrator) injectResearchContext(ctx context.Context) context.Context {
	if !tools.IsResearch(ctx) {
		return ctx
	}
	rootPath := tools.ResearchRootPathFrom(ctx)
	if rootPath == "" {
		o.logDebug("research context skipped: no research-root path in context")
		return ctx
	}

	root, err := research.ParseResearchRoot(rootPath)
	if err != nil {
		o.logDebug("research context skipped: root not readable", "path", rootPath, "error", err)
		return ctx
	}

	rc := buildResearchContextSnapshot(rootPath, root)
	ctx = WithResearchContext(ctx, rc)
	o.logDebug("research context injected", "root", rootPath, "active", rc.ActiveID, "phase", rc.PhaseHint)
	return ctx
}

// buildResearchContextSnapshot derives the concise ResearchContext snapshot
// from a parsed research root. It is a pure function (no I/O) so it can be
// unit-tested in isolation. The active R-NNN is the latest index entry
// (chronological append order), falling back to the highest-numbered project
// directory when there is no index yet. The phase hint comes from the active
// project's hypothesis-graph metrics.
func buildResearchContextSnapshot(rootPath string, root *research.ResearchRoot) *ResearchContext {
	rc := &ResearchContext{
		RootPath:     rootPath,
		ProjectCount: len(root.Index),
	}
	if rc.ProjectCount == 0 {
		// No index yet — count discovered project dirs instead.
		rc.ProjectCount = len(root.Projects)
	}

	active := research.PickActiveProject(root)
	if active == nil {
		rc.PhaseHint = "ready to initialize the first research project (research-init)"
		return rc
	}

	rc.ActiveID = active.ID
	rc.ActiveTitle = active.Brief.Title
	rc.TotalHypotheses = active.Metrics.Total
	rc.ActiveFront = len(active.Metrics.ActiveFront)
	rc.PhaseHint = researchPhaseHint(active)
	return rc
}

// researchPhaseHint derives a short, human-readable phase label from a
// research project's hypothesis-graph metrics. It is a pure mapping over the
// metrics struct — no I/O — mirroring the lifecycle vocabulary in the
// research-* skills.
func researchPhaseHint(p *research.ResearchProject) string {
	if p == nil {
		return "unknown"
	}
	m := p.Metrics
	if m.Total == 0 {
		return "setup: brief defined, no hypotheses formulated yet (research-hypothesis)"
	}
	if len(m.ActiveFront) > 0 {
		noun := "hypothesis"
		if len(m.ActiveFront) > 1 {
			noun = "hypotheses"
		}
		return "experimenting: " + strconv.Itoa(len(m.ActiveFront)) + " active " + noun +
			" on the front (open/in-progress)"
	}
	// All hypotheses are terminal.
	confirmed := m.ByStatus[research.StatusConfirmed]
	refuted := m.ByStatus[research.StatusRefuted]
	if confirmed > 0 {
		return "concluding: all hypotheses decided (" + strconv.Itoa(confirmed) +
			" confirmed, " + strconv.Itoa(refuted) + " refuted) — ready for synthesis"
	}
	return "falsified: all " + strconv.Itoa(m.Total) + " hypotheses refuted"
}

// collectAgentsMDPaths returns the ordered list of AGENTS.md file paths to
// consult: the configured search paths (global → c0wrk-specific, already
// absolute and pointing at an AGENTS.md file) followed by the workspace-root
// AGENTS.md. The workspace-root entry is omitted when there is no workspace
// path in the context (e.g., No Project / CHAT mode), so only the global and
// c0wrk files are read in that case.
func (o *Orchestrator) collectAgentsMDPaths(ctx context.Context) []string {
	paths := make([]string, 0, len(o.config.AgentsMDSearchPaths)+1)
	paths = append(paths, o.config.AgentsMDSearchPaths...)
	if wsPath := sdktools.WorkspacePathFrom(ctx); wsPath != "" {
		paths = append(paths, filepath.Join(wsPath, "AGENTS.md"))
	}
	return paths
}

// readAgentsMD reads AGENTS.md from disk with a per-path cache that
// invalidates on mtime change. This avoids repeated disk reads on every
// HandleMessage call while still picking up edits. Write-through: every cache
// miss re-reads the file and updates modTime. The raw (uncapped) content is
// returned; the size cap is applied to the combined content of all sources in
// capAgentsMD.
func (o *Orchestrator) readAgentsMD(path string) (string, error) {
	if o.agentsMDCache == nil {
		o.agentsMDCache = make(map[string]agentsMDCacheEntry)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		// File doesn't exist or is inaccessible — evict stale positive cache.
		delete(o.agentsMDCache, path)
		return "", statErr
	}

	if cached, ok := o.agentsMDCache[path]; ok && cached.modTime.Equal(info.ModTime()) {
		if cached.err != nil {
			return "", cached.err
		}
		return cached.content, nil
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		o.agentsMDCache[path] = agentsMDCacheEntry{err: readErr, modTime: info.ModTime()}
		return "", readErr
	}

	contentStr := string(content)
	o.agentsMDCache[path] = agentsMDCacheEntry{content: contentStr, modTime: info.ModTime()}
	return contentStr, nil
}

// capAgentsMD applies the AgentsMDMaxBytes cap to the combined AGENTS.md
// content. A cap of 0 means use the default (DefaultAgentsMDMaxBytes); a
// negative cap disables truncation entirely. Truncation snaps to a UTF-8 rune
// boundary and then back to the last newline so multibyte content is not split
// mid-rune.
func (o *Orchestrator) capAgentsMD(content string) string {
	maxBytes := o.config.AgentsMDMaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultAgentsMDMaxBytes
	}
	originalSize := len(content)
	if maxBytes <= 0 || originalSize <= maxBytes {
		return content
	}
	trimmed := strutil.TruncateUTF8AtLineBoundary(content, maxBytes)
	trimmed += fmt.Sprintf("\n\n[…AGENTS.md truncated at %d bytes; original was %d bytes]", maxBytes, originalSize)
	o.logDebug("AGENTS.md truncated", "originalSize", originalSize, "cap", maxBytes)
	return trimmed
}

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
// It also injects the model's advertised context window into the emitter so that
// the user-facing fill display (status bar, compaction messages) is computed
// relative to the real window, not the internal "effective max" the executor
// uses for compaction.
//
// Resolution is deliberately ResolveLocal (network-free): this runs on every
// HandleMessage on the UI-critical path, where a full Resolve could fire a
// HuggingFace lookup (10s timeout) for an unknown self-hosted model id and
// stall the initial fill. The local tiers cover the meaningful sources —
// config override, observed runtime (lazy server probe), built-in catalog,
// lazy cache — and a late-arriving probe result refreshes the display via
// SetDisplayContextWindowForModel once it lands.
func (o *Orchestrator) emitInitialContextFill() {
	var contextWindow int
	if o.modelRegistry != nil {
		model := o.config.Model
		meta, _ := o.modelRegistry.ResolveLocal(model)
		if meta.ContextWindow > 0 {
			contextWindow = meta.ContextWindow
		}
	}
	if contextWindow > 0 {
		if setter, ok := o.emitter.(DisplayContextWindowSetter); ok {
			setter.SetDisplayContextWindow(contextWindow)
		}
	}
	o.emitter.ContextFill(0, 0, contextWindow, "ok", "")
}

// SetNoProjectMode configures this orchestrator for No Project mode:
// disables code-oriented tools, adds extended shell command blacklist,
// and marks the orchestrator to override code domain to general.
func (o *Orchestrator) SetNoProjectMode() {
	o.isNoProject = true
	if o.coreToolRegistry == nil {
		if o.logger != nil {
			o.logger.Warn("SetNoProjectMode: coreToolRegistry is nil, skipping tool configuration")
		}
		return
	}
	o.coreToolRegistry.SetDisabledTools(NoProjectDisabledTools)
	if err := o.coreToolRegistry.SetExtraShellBlacklist(NoProjectShellBlacklist); err != nil {
		if o.logger != nil {
			o.logger.Warn("failed to set extra shell blacklist for No Project", "error", err)
		}
	}
}

// Emitter returns the orchestrator's event emitter for use by external callers
// that need to emit plan step events (e.g., after feedback-driven replanning).
// Returns a noop emitter when the orchestrator has no emitter configured,
// preventing nil-pointer panics in callers.
func (o *Orchestrator) Emitter() Emitter {
	if o.emitter == nil {
		return &noopEmitter{}
	}
	return o.emitter
}

// LookupSkillDescriptors converts skill names to SkillDescriptors using the
// orchestrator's skill manager. Unknown names are silently skipped. Returns nil
// if the skill manager is not available.
func (o *Orchestrator) LookupSkillDescriptors(names []string) []skills.SkillDescriptor {
	if o.skillManager == nil || len(names) == 0 {
		return nil
	}
	var result []skills.SkillDescriptor
	for _, name := range names {
		if s, ok := o.skillManager.Get(name); ok {
			result = append(result, s.Descriptor())
		}
	}
	return result
}

// RescanSkills re-scans the skill discovery directories and refreshes the
// per-session skill catalog in place. It is used when skills are seeded into
// the project's .agents/skills directory mid-session (e.g. enabling RESEARCH
// mode, which seeds the research-* methodology skills) so the running session
// can discover them without a restart. Safe to call concurrently with skill
// lookups — the SkillManager holds its own lock and Scan replaces the catalog
// atomically. A nil skill manager is a no-op (returns nil).
func (o *Orchestrator) RescanSkills() error {
	if o.skillManager == nil {
		return nil
	}
	if err := o.skillManager.Scan(); err != nil {
		if o.logger != nil {
			o.logger.Warn("RescanSkills: skill re-scan failed", "error", err)
		}
		return err
	}
	if o.logger != nil {
		o.logger.Debug("RescanSkills: skill catalog refreshed",
			"count", len(o.skillManager.List()))
	}
	return nil
}

// SetReasoningEffort propagates the per-request reasoning effort to all components
// that make LLM calls: router, reflector, and the direct LLM caller.
func (o *Orchestrator) SetReasoningEffort(effort string) {
	o.config.ReasoningEffort = effort
	if o.router != nil {
		o.router.SetReasoningEffort(effort)
	}
	if o.reflector != nil {
		o.reflector.SetReasoningEffort(effort)
	}
	if setter, ok := o.llm.(interface{ SetReasoningEffort(string) }); ok {
		setter.SetReasoningEffort(effort)
	}
}

// ApplyRequestOverrides applies per-request model and reasoning-effort
// overrides to all LLM-calling components (router, reflector, the direct LLM
// caller, and config.Model for metadata resolution). It is the shared step 0
// of HandleMessage (fresh task and continuation) AND the session manager's
// resume-an-interrupted-task path, which bypasses HandleMessage — without
// calling this, a model/reasoning switch on a resume message would be
// silently dropped. When both overrides are empty it is a no-op.
func (o *Orchestrator) ApplyRequestOverrides(ctx context.Context, modelOverride, reasoningEffort string) {
	o.SetReasoningEffort(reasoningEffort)
	if modelOverride != "" && o.modelSwitcher != nil {
		if err := o.modelSwitcher.SetModel(ctx, modelOverride); err != nil {
			if o.logger != nil {
				o.logger.Warn("failed to apply model override", "model", modelOverride, "error", err)
			}
		} else {
			o.config.Model = llm.BareModel(modelOverride)
			// Lazily probe the newly-selected model's real context window when
			// it is served from an OpenAI-compatible endpoint. The probe is
			// fire-and-forget (returns immediately); the discovered window
			// lands in the model registry and is picked up by subsequent
			// context-budget math.
			if o.localModelProbe != nil {
				o.localModelProbe(llm.BareModel(modelOverride))
			}
			// Synchronize the emitter's cached model so the initial
			// context_fill — emitted before the first LLM call reports usage —
			// carries the newly-selected model instead of the previous task's
			// model. Without this the status bar shows a stale model in a
			// continuation until the first token-usage report arrives.
			family := ""
			if o.modelRegistry != nil {
				if meta, _ := o.modelRegistry.Resolve(ctx, o.config.Model); meta.Family != "" {
					family = meta.Family
				}
			}
			if setter, ok := o.emitter.(LastModelSetter); ok {
				setter.SetLastModel(o.config.Model, family)
			}
		}
	}
}

// SetTaskStore sets the TaskPersistence store for blackboard restoration.
// This is used by HandleMessage continuations to restore a completed task's blackboard.
func (o *Orchestrator) SetTaskStore(store TaskPersistence) {
	o.taskStore = store
}

// SetConversationHistory sets the full conversation history for the session.
// Call this during session restore to pre-populate the history from persistent storage,
// ensuring the planner sees all previous messages even after a backend restart.
func (o *Orchestrator) SetConversationHistory(history []llm.Message) {
	o.conversationHistory = history
}

// ConversationHistory returns the current conversation history (for testing).
func (o *Orchestrator) ConversationHistory() []llm.Message {
	return o.conversationHistory
}

// HistoryNoteCancelled is the assistant-side conversation-history note
// recorded when a task is cancelled before completion. The backend uses the
// same note when reconstructing history from the message store so live and
// restored histories stay identical.
const HistoryNoteCancelled = "[Task was cancelled before completion]"

// historyNoteFailedPrefix is the prefix of failure notes produced by
// HistoryNoteFailed; used to recognize a previously recorded failed attempt.
const historyNoteFailedPrefix = "[Task failed before completion: "

// HistoryNoteFailed formats the assistant-side conversation-history note
// recorded when a task fails before completion. Shared with the backend's
// history reconstruction (see HistoryNoteCancelled).
func HistoryNoteFailed(errText string) string {
	return historyNoteFailedPrefix + errText + "]"
}

// buildContentBlocks assembles structured content blocks (text + images) for
// the Conductor's task user message. The first block is the text task;
// subsequent blocks are the images. Returns nil when there are no images so
// the legacy text-only SetTask path is preserved (backward compatible).
func buildContentBlocks(text string, images []llm.ContentBlock) []llm.ContentBlock {
	if len(images) == 0 {
		return nil
	}
	blocks := make([]llm.ContentBlock, 0, 1+len(images))
	blocks = append(blocks, llm.ContentBlock{Type: "text", Text: text})
	blocks = append(blocks, images...)
	return blocks
}

// imageBlocksForRequest searches the conversation history for a user message
// matching the given request text and returns its image content blocks
// (Type == "image"). This is used on resume to reconstruct the image content
// blocks that were part of the original task's user message — the conversation
// history (in-memory or restored from the DB via convertChatMessagesToLLM)
// carries the blocks, so they can be re-injected into the resumed Conductor
// run without persisting them separately. Returns nil when no matching message
// is found or it carries no image blocks.
func imageBlocksForRequest(history []llm.Message, request string) []llm.ContentBlock {
	for _, msg := range history {
		if msg.Role == "user" && msg.Content == request && len(msg.ContentBlocks) > 0 {
			var images []llm.ContentBlock
			for _, blk := range msg.ContentBlocks {
				if blk.Type == "image" {
					images = append(images, blk)
				}
			}
			return images
		}
	}
	return nil
}

// recordConversationOutcome appends the current exchange to the in-memory
// conversation history. It is invoked (via defer) for EVERY terminal outcome
// of HandleMessage — success, partial success, failure, cancellation —
// failure, and cancellation — so the router and continuation planner always
// see the full dialogue, not just successful exchanges.
//
// contentBlocks, when non-nil, carries structured content (text + images) for
// the user message. The history user message is emitted with ContentBlocks
// set so providers that support multimodal input see the images on
// continuation turns. nil preserves the legacy text-only history entry.
func (o *Orchestrator) recordConversationOutcome(ctx context.Context, message string, contentBlocks []llm.ContentBlock, result *HandleResult, err error) {
	userMsg := llm.Message{Role: "user", Content: message, ContentBlocks: contentBlocks}

	// Continuation fallback: the session manager retries a failed
	// continuation as a fresh workflow with the same message. If the last
	// recorded exchange is a failed attempt of this exact message, replace
	// it with the retry's outcome instead of duplicating the user message.
	// This also matches how the persisted store reads after a restart
	// (consecutive assistant entries collapse to the most recent one).
	if n := len(o.conversationHistory); n >= 2 {
		prev, last := o.conversationHistory[n-2], o.conversationHistory[n-1]
		if prev.Role == "user" && prev.Content == message &&
			last.Role == "assistant" && strings.HasPrefix(last.Content, historyNoteFailedPrefix) {
			o.conversationHistory = o.conversationHistory[:n-2]
		}
	}

	switch {
	case err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete):
		if result == nil {
			o.conversationHistory = append(o.conversationHistory, userMsg)
			return
		}
		var reasoning string
		if result.Blackboard != nil {
			reasoning = lastReasoningContent(result.Blackboard)
		}
		// Failure-mode guard: if the assistant output contains tool-call
		// syntax printed as text (```bash_exec etc. instead of a tool_use
		// block), the "finish" was a stuck-model artifact, not a real
		// answer. Record a failure note instead of the hallucinated text
		// so future routing/planning sees an honest failure, not garbage.
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: reasoning})
	case ctx.Err() != nil:
		// Cancellation (or deadline): the manager persists a task_cancelled
		// event; mirror it in the in-memory history.
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	default:
		// Hard failure: the manager persists an error event; mirror it so
		// the rejected request stays visible to future routing/planning.
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// recordResumeOutcome appends the outcome of a resumed execution (interrupted
// task) to the in-memory conversation history. The
// user message that spawned the task was recorded when the task first ran (or
// restored from the message store after a restart), so only the assistant
// side is appended here.
func (o *Orchestrator) recordResumeOutcome(ctx context.Context, bb orchestration.Blackboard, result *HandleResult, err error) {
	switch {
	case (err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete)) && result != nil:
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: lastReasoningContent(bb)})
	case ctx.Err() != nil:
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	case err != nil:
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// SetBlackboardRestoreFunc sets the function used to restore a PersistableBlackboard from persistence.
func (o *Orchestrator) SetBlackboardRestoreFunc(fn BlackboardRestoreFunc) {
	o.bbRestoreFunc = fn
}

// truncateHistory returns the last window messages of history. If window <= 0
// or len(history) <= window, history is returned as-is. The most recent
// messages are preserved so the agent sees the dialogue context leading up to
// the current message.
func truncateHistory(history []llm.Message, window int) []llm.Message {
	if window <= 0 || len(history) <= window {
		return history
	}
	truncated := make([]llm.Message, window)
	copy(truncated, history[len(history)-window:])
	return truncated
}

// HandleMessage is the unified entry point for processing user messages.
// It supports two flows: first message and continuation.
// For first messages, a plan is always generated first, then executed via Plan&Execute.
// For continuations (TaskID != ""), the P&E continuation path is used.
//   - TaskID="": First message (create new blackboard)
//   - TaskID!="": Continuation (restore existing blackboard)
func (o *Orchestrator) HandleMessage(ctx context.Context, message, sessionID string, opts HandleOptions) (result *HandleResult, err error) {
	// W-8: Enforce single-flight — only one HandleMessage per *Orchestrator.
	if !o.requestInFlight.CompareAndSwap(false, true) {
		return nil, ErrRequestInFlight
	}
	defer o.requestInFlight.Store(false)

	// Install the universal pause signal for this request. Every conductor run
	// (normal path or goal loop) reads it via the pause-checker at each step
	// boundary; PauseSession flips it. Cleared on exit so a stale signal can
	// never pause a future request.
	defer o.installPauseSignal()()

	// Resolve the effective task message. When the user invokes a skill via
	// /skill-name, preprocessMessageText strips the reference — potentially
	// leaving an empty message. resolveTaskMessage rebuilds it so the
	// Conductor receives a meaningful task and the conversation history never
	// records an empty user message (which some providers reject with HTTP
	// 400 "messages parameter is illegal"). The raw `message` is still passed
	// to the router below, which applies its own augmentation — using the
	// augmented message there would double-prefix the skill reference.
	taskMessage := o.resolveTaskMessage(message, opts.UserSkills)

	// contentBlocks carries structured content (text + images) for the task
	// user message. Built after augmentWithAttachments when the request
	// includes staged image attachments (opts.PendingImages). Declared here
	// (before the defer) so the recordConversationOutcome closure captures it
	// by reference and records the blocks in the conversation history. nil
	// preserves the legacy text-only path.
	var contentBlocks []llm.ContentBlock

	// Record the exchange in the in-memory conversation history for EVERY
	// terminal outcome (success, failure, cancellation) of HandleMessage
	// cancellation) so future routing and continuation planning always see
	// the full dialogue. Registered after the single-flight guard so a
	// rejected concurrent request is not recorded.
	defer func() {
		o.recordConversationOutcome(ctx, taskMessage, contentBlocks, result, err)
	}()

	// 0. Apply per-request overrides to all LLM-calling components.
	o.ApplyRequestOverrides(ctx, opts.ModelOverride, opts.ReasoningEffort)

	// 1. Prepare context (plan-mode key, injection-defense, vector hints, initial context_fill).
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	ctx = o.prepareRequestContext(ctx, taskMessage)

	// Signal review-feedback mode so the system prompt instructs the agent to
	// treat the user's message as actionable review comments (see review domain).
	if opts.ReviewMode {
		ctx = WithReviewMode(ctx)
	}

	// 2. Setup blackboard (fresh or restored).
	bb, err := o.setupBlackboard(taskMessage, sessionID, opts.TaskID, opts.PendingAttachments)
	if err != nil {
		return nil, err
	}

	// Augment the Conductor's task message with the session's attached files.
	// The router and conversation history keep the clean `taskMessage`; only the
	// Conductor receives this section so the LLM can call read_attachment.
	// Images are NOT included here — they travel as structured ContentBlocks
	// (below), not as text in the blackboard/augmentWithAttachments section.
	conductorMessage := o.augmentWithAttachments(taskMessage, bb)

	// Build structured content blocks when the request carries staged image
	// attachments. The first block is the (attachment-augmented) text task;
	// subsequent blocks are the images from opts.PendingImages. These flow
	// through runConductor → ConductorConfig.ContentBlocks → SetTaskWithBlocks
	// so the ContextManager emits a Message carrying ContentBlocks (providers
	// give blocks precedence over the plain Content string). Images never
	// enter the blackboard or augmentWithAttachments — they are pure content
	// blocks, not read_attachment targets.
	contentBlocks = buildContentBlocks(conductorMessage, opts.PendingImages)

	// 2. Get available tools (exclude disabled tools in No Project mode).
	availableTools := o.toolRegistry.ListFiltered(o.disabledToolNames())
	mcpCount := 0
	for _, t := range availableTools {
		if t.SourceCategory == sdktools.SourceCategoryMCP {
			mcpCount++
		}
	}
	o.logDebug("orchestrator: tools loaded from registry", "total", len(availableTools), "mcp", mcpCount)

	// GOAL MODE: a goal request enters the multi-turn goal loop instead of the
	// single-pass route→Conductor flow. The loop derives a crisp {condition,
	// verify} goal (with user sign-off), then iterates the Conductor
	// turn-by-turn until the agent declares the goal met, the budget is
	// exhausted, the agent goes idle (anti-spin), or the goal is paused.
	// Goal mode is entered on BOTH a fresh task (TaskID == "") and on a
	// continuation (TaskID != ""): on a continuation the prior task's
	// blackboard is restored and the agent runs the goal loop on the inherited
	// facts/history, deriving a fresh goal from the new message.
	if opts.Goal {
		return o.runGoalLoop(ctx, message, opts, bb, availableTools, opts.SessionPlansDir)
	}

	// Goal-mode-only tools (propose_goal, declare_goal_status, declare_verification)
	// exist solely for goal mode and must not reach a non-goal Conductor run. Strip
	// them here so neither the router nor the Conductor's available-tool view ever
	// offers them when goal mode is off. The goal path above receives the unstripped
	// list: the independent verifier's verifierToolFilter/verifierReDerivationToolFilter
	// build their read-only toolset (which must include declare_verification) from it.
	availableTools = tools.StripGoalModeTools(availableTools)

	// 3. Route and activate skills.
	// Continuation fast-path: routeOrContinue skips the router when a restored
	// task has an existing plan + routing (the router is blind to the plan and
	// would misclassify continuation messages).
	// NOTE: the raw preprocessed `message` is passed here (not taskMessage)
	// because the router augments the skill context itself — passing the
	// already-augmented taskMessage would double-prefix the skill reference.
	ctx, routing, _, _, err := o.routeOrContinue(ctx, message, opts, bb, availableTools)
	if err != nil {
		return nil, err
	}

	// 4. Enrich context with domain/complexity/user-skills for execution.
	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// 5. Execute via the Conductor (single ReAct loop owning the task).
	// Persist the routing decision on the blackboard so the Conductor and
	// finalization can retrieve it.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	// Small-LLM essential-tools filter: when enabled, narrow the conductor's
	// tool set ONCE here (before the ReAct loop starts) to reduce per-prompt
	// schema overhead. This is the NON-GOAL path only: goal mode returns
	// early above (runGoalLoop), before this point, so the goal-mode tool set
	// — including the verifier-required goal-mode-only tools
	// (declare_verification etc.) that SelectTools would otherwise drop — is
	// never narrowed. Runs exactly once per task, never inside the step loop.
	// When the profile is OFF (default) this is a no-op passthrough.
	availableTools = o.applySmallLLMToolFilter(availableTools, routing)

	// Truncate conversation history to the configured window so long
	// sessions don't overflow the Conductor's context. The most recent
	// messages are kept — they carry the dialogue context the agent needs
	// to understand follow-up references (e.g. "implement variant a").
	conductorHistory := truncateHistory(o.conversationHistory, o.config.ConductorHistoryWindow)

	plansDir := opts.SessionPlansDir
	execResult, err := o.runConductor(ctx, conductorMessage, bb, availableTools, plansDir, conductorHistory, nil, contentBlocks, "")
	// Cooperative pause: a clean, recoverable checkpoint — not a failure.
	// Surface it, persist the task as resumable (persistTaskOutcome via
	// finalizeResult), and return the paused result with a nil error so the
	// backend surfaces a Resume action rather than an error.
	if errors.Is(err, agent.ErrPaused) {
		o.emitSessionPaused()
		err = nil
	}
	// C-5: propagate ErrExecutionIncomplete alongside best-effort result.
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		// Mark the task as failed so it is not left lingering in_progress
		// (which would make it a silent-resume candidate). Mirrors the Resume
		// path and the routing-error path below.
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	incompleteErr := err

	// Guard against nil execResult.
	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	// 6. Finalize: persist routing and build result. The conversation
	// history is updated by the recordConversationOutcome defer above.
	result = o.finalizeResult(bb, routing, execResult)

	if incompleteErr != nil {
		return result, incompleteErr
	}
	return result, nil
}

// disabledToolNames returns the set of tool names disabled for the current
// mode (e.g. CHAT / No-Project mode disables glob/ripgrep/semantic_search),
// or nil if no tools are disabled / the core registry is unavailable. Used to
// build the Conductor's available-tool view and the per-mode subagent toolset.
func (o *Orchestrator) disabledToolNames() map[string]bool {
	if o.coreToolRegistry == nil {
		return nil
	}
	return o.coreToolRegistry.DisabledTools()
}

// applySmallLLMToolFilter narrows the conductor's available-tool set when the
// small-LLM profile is active. It delegates to smallllm.SelectTools, which
// unions the router-matched tool names (routing.MatchedTools), the user's
// always-present list, the protected orchestration tools (finish + memory +
// ask_user), and every MCP-sourced tool, then (optionally) enforces the
// MaxTools budget. It runs exactly once per task, before the non-goal ReAct
// loop starts (HandleMessage applies it after the goal-mode early return, so
// goal mode is intentionally never narrowed).
//
// When the profile is OFF (the default), it returns the tools untouched — zero
// behavior change. When filtering is active (master ON + essential ON + a
// routing decision is present), it emits a ToolsAssigned event so the UI can
// surface the curated tool set as a card.
func (o *Orchestrator) applySmallLLMToolFilter(in []sdktools.ToolDescriptor, routing *router.RoutingDecision) []sdktools.ToolDescriptor {
	sc := o.config.SmallLLM
	// Master toggle AND the essential-tools variant must both be enabled.
	// When either is off, return the input untouched (zero behavior change).
	if !sc.Enabled || !sc.EssentialTools.Enabled {
		return in
	}

	var matched []string
	if routing != nil {
		matched = routing.MatchedTools
	}
	filtered := smallllm.SelectTools(in, matched, sc.EssentialTools.AlwaysPresent, sc.EssentialTools.MaxTools)

	// Surface the curated tool set as a UI card when filtering is active
	// (master + essential on AND a routing decision is present). Mirrors
	// SkillsActivated.
	if routing != nil && o.emitter != nil {
		names := make([]string, len(filtered))
		for i, d := range filtered {
			names[i] = d.Name
		}
		o.emitter.ToolsAssigned(names)
		o.logInfo("tools_assigned", "count", len(names))
	}
	return filtered
}
