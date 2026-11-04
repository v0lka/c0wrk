// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/c0wrk/core/modelprofiles"
	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/safeio"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/strutil"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

// goalKeyType carries the active *goal.GoalState; buildSystemPrompt renders the
// goal-mode prompt section from it. See specs/domains/goal-mode.md.
type goalKeyType struct{}

// GoalKey is the context key for the active goal; unlike PlanModeKey it carries
// the whole GoalState instead of a presence flag.
var GoalKey = goalKeyType{}

// WithGoalState returns a context carrying the active goal state under GoalKey.
func WithGoalState(ctx context.Context, gs *goal.GoalState) context.Context {
	return context.WithValue(ctx, GoalKey, gs)
}

// goalStateFromCtx extracts the active goal state from the context, or nil.
func goalStateFromCtx(ctx context.Context) *goal.GoalState {
	if gs, ok := ctx.Value(GoalKey).(*goal.GoalState); ok {
		return gs
	}
	return nil
}

// reviewModeKeyType is a presence flag (like PlanModeKey): when set,
// buildSystemPrompt renders the Code Review Feedback section. See specs/domains/review.md.
type reviewModeKeyType struct{}

// ReviewModeKey is the context key for review-feedback mode.
var ReviewModeKey = reviewModeKeyType{}

// WithReviewMode returns a context carrying ReviewModeKey for this Conductor run.
func WithReviewMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, ReviewModeKey, struct{}{})
}

// reviewModeFromCtx reports whether review-feedback mode is active for this run.
func reviewModeFromCtx(ctx context.Context) bool {
	return ctx.Value(ReviewModeKey) != nil
}

// DefaultAgentsMDMaxBytes caps untrusted AGENTS.md content injected into prompts;
// see specs/decisions/020-multi-source-agents-md-threat-model.md.
const DefaultAgentsMDMaxBytes = 65536

// defaultResumeComplexity routes a resumed task with no persisted routing
// decision into standard Conductor mode (checklist + optional delegation).
const defaultResumeComplexity = 3

// agentsMDCacheEntry holds a cached read of AGENTS.md from a workspace.
type agentsMDCacheEntry struct {
	content string
	modTime time.Time
	err     error
}

type injectionDefenseKeyType struct{}

// InjectionDefenseKey is the context key carrying a non-nil *bool; when set,
// buildSystemPrompt includes the injection-defense prompt text.
var InjectionDefenseKey = injectionDefenseKeyType{}

// modelProfilesPromptProfile carries the SystemPrompt sub-toggle flags to
// buildSystemPromptWith under ModelProfilesLiteKey. See specs/domains/model-profiles.md.
type modelProfilesPromptProfile struct {
	Lite              bool
	FewShot           bool
	ReasoningScaffold bool
}

// modelProfilesLiteKeyType carries a modelProfilesPromptProfile; presence signals lite is active.
type modelProfilesLiteKeyType struct{}

// ModelProfilesLiteKey is the context key signaling the model-profile lite prompt profile.
var ModelProfilesLiteKey = modelProfilesLiteKeyType{}

// WithModelProfilesLite is a test/fixture convenience with all sub-toggles on;
// production wiring uses withModelProfilesPromptProfile.
func WithModelProfilesLite(ctx context.Context) context.Context {
	return withModelProfilesPromptProfile(ctx, modelProfilesPromptProfile{
		Lite:              true,
		FewShot:           true,
		ReasoningScaffold: true,
	})
}

// withModelProfilesPromptProfile is the production entry point: config-derived
// flags under ModelProfilesLiteKey.
func withModelProfilesPromptProfile(ctx context.Context, p modelProfilesPromptProfile) context.Context {
	return context.WithValue(ctx, ModelProfilesLiteKey, p)
}

// modelProfilesLiteFromCtx reports whether the lite directive swap is active for this run.
func modelProfilesLiteFromCtx(ctx context.Context) bool {
	p, ok := ctx.Value(ModelProfilesLiteKey).(modelProfilesPromptProfile)
	return ok && p.Lite
}

// modelProfilesPromptProfileFromCtx returns the carried profile and whether one is present.
func modelProfilesPromptProfileFromCtx(ctx context.Context) (modelProfilesPromptProfile, bool) {
	p, ok := ctx.Value(ModelProfilesLiteKey).(modelProfilesPromptProfile)
	return p, ok
}

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxRedelegationDepth      int    // cap on recursive delegation when allow_redelegate is true (default: 2)
	KeepFirst                 int    // for sliding window compaction
	KeepLast                  int    // for sliding window compaction
	MaxDependencyContextChars int    // max chars for dependency context in delegation tasks (default: 8000)
	MaxParallelSubagents      int    // cap on concurrent subagents, shared by delegate and plan waves (default: 4)
	Model                     string // active model name for ModelRegistry.Resolve()

	// ToolCallTimeout bounds a single tool call on the main and every subagent
	// executor; 0 disables. See specs/domains/tool-system/builtins.md.
	ToolCallTimeout time.Duration

	// ToolCallTimeoutExemptTools replaces the ceiling's exempt set wholesale
	// (nil keeps sp4rk's default; threaded via SetToolCallTimeoutExempt).
	ToolCallTimeoutExemptTools []string

	// Compaction carries the executor compaction settings for manual history
	// compaction (per executor via buildContextFactory). See specs/domains/memory/compaction.md.
	Compaction BuilderCompactionConfig

	// ReasoningEffort is the reasoning effort applied to step executors.
	// When non-empty, each executor gets this value directly (no role adaptation).
	ReasoningEffort string

	// HITLHandler is called when an executor reaches its step limit.
	// If nil, a default handler is used (deny all extensions).
	HITLHandler agent.HITLHandler

	// PreWarningPercent is the context fill % triggering the pre-compaction
	// store_fact nudge (below the predictive threshold).
	PreWarningPercent int

	// InjectionDefenseEnabled gates the prompt injection defense prompt text
	// and the <untrusted-content> wrapping of tool outputs in the context window.
	InjectionDefenseEnabled bool

	// AgentsMDMaxBytes caps the combined AGENTS.md content injected into prompts;
	// 0 = DefaultAgentsMDMaxBytes, negative disables the cap.
	AgentsMDMaxBytes int

	// AgentsMDSearchPaths holds extra absolute AGENTS.md paths searched ahead of
	// the workspace-root file (missing silently skipped), capped by AgentsMDMaxBytes.
	// Threat model: specs/decisions/020-multi-source-agents-md-threat-model.md.
	AgentsMDSearchPaths []string

	// ConductorHistoryWindow is the max number of recent conversation messages
	// injected into the Conductor's context (0 = default 20).
	ConductorHistoryWindow int

	// GoalLoop holds goal-loop settings; Verification "off" disables the
	// independent verifier turn. See specs/domains/goal-mode.md.
	GoalLoop GoalLoopSettings

	// ModelProfiles holds the model-profile settings; every variant is gated by the
	// master Enabled AND its own sub-toggle. See specs/domains/model-profiles.md.
	ModelProfiles ModelProfilesSettings

	// E2S holds the E2S execution-mode settings: Enabled gates availability
	// fail-closed; numerics fall back to core/e2s defaults. See specs/domains/e2s.md.
	E2S E2SSettings

	// MCPServerModes: per-server capability-mode map ("auto"/"manual"/"disabled");
	// empty gates nothing. See specs/decisions/077-explicit-mcp-server-mentions.md.
	MCPServerModes map[string]CapabilityModes
	// MCPServerModesResolver supplies the builder's current immutable policy snapshot.
	// Direct constructors fall back to MCPServerModes when it is nil.
	MCPServerModesResolver func() map[string]CapabilityModes

	// SkillModes / AgentModes carry the per-name capability-mode maps for the
	// skill and agent catalogs (same enum and shape as MCPServerModes); see
	// specs/decisions/084-per-capability-chat-code-activation-modes.md.
	SkillModes map[string]CapabilityModes
	// SkillModesResolver supplies the builder's current immutable policy
	// snapshot. Direct constructors fall back to SkillModes when it is nil.
	SkillModesResolver func() map[string]CapabilityModes
	// AgentModes mirrors SkillModes for the subagent catalog.
	AgentModes map[string]CapabilityModes
	// AgentModesResolver supplies the builder's current immutable policy
	// snapshot. Direct constructors fall back to AgentModes when it is nil.
	AgentModesResolver func() map[string]CapabilityModes
}

// E2SSettings is the runtime mirror of BuilderE2SConfig: a type alias — the
// settings are a flat behavior-only value with no core-side reshaping.
type E2SSettings = BuilderE2SConfig

// GoalLoopSettings mirrors the config-layer GoalLoopConfig; Verification is
// "independent" (default) or "off". See specs/domains/goal-mode.md.
type GoalLoopSettings struct {
	Verification string
	Suggest      GoalSuggestSettings
}

// GoalSuggestSettings is the runtime mirror of the config-layer GoalSuggestConfig:
// the post-routing goal-mode suggestion gate. See specs/decisions/086-goal-mode-suggestion.md.
type GoalSuggestSettings struct {
	// Enabled gates the whole feature; false suppresses the suggestion gate
	// entirely (goal mode stays available via the explicit toggle / /goal).
	Enabled bool
	// MinComplexity is the cheap complexity floor below which the classifier
	// is never consulted (saves an LLM call on obvious non-candidates).
	MinComplexity int
}

// ModelProfilesSettings mirrors BuilderModelProfilesConfig; the master Enabled
// toggle gates every variant (defense-in-depth). See specs/domains/model-profiles.md.
type ModelProfilesSettings struct {
	Enabled        bool
	EssentialTools ModelProfilesEssentialSettings
	SystemPrompt   ModelProfilesSystemPromptSettings
	// LoopHardening carries the circuit-breaker tightening overrides (builder
	// level; E2S maps RepeatNudgeThreshold onto its anti-spin nudge).
	LoopHardening ModelProfilesLoopHardeningSettings
}

// ModelProfilesLoopHardeningSettings projects the loop-hardening thresholds the
// E2S path consumes; zero keeps the configured/baseline threshold (mirrors applyLoopHardening).
type ModelProfilesLoopHardeningSettings struct {
	Enabled              bool
	RepeatNudgeThreshold int
}

// ModelProfilesEssentialSettings holds the always-present tool-set narrowing settings.
type ModelProfilesEssentialSettings struct {
	Enabled bool
	// AlwaysPresent is the user-pinned tool list exposed regardless of routing;
	// SelectTools additionally keeps protected orchestration tools and MCP tools.
	AlwaysPresent []string
	// CompactDescriptions swaps full builtin descriptions for one-line compact
	// variants while essential-tools is active; off keeps full form.
	CompactDescriptions bool
}

// ModelProfilesSystemPromptSettings holds the prompt-simplification variant; Lite
// is the master toggle. See specs/domains/model-profiles.md.
type ModelProfilesSystemPromptSettings struct {
	Lite bool
	// FewShot appends the worked-example ReAct block (requires Lite).
	FewShot bool
	// ReasoningScaffold appends the three-step thought template (requires Lite).
	ReasoningScaffold bool
}

// ContextManagerFactory creates a ContextManager for a new task; compactionStrategy
// selects the strategy, pruningOverrides override the global pruning configuration.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager

// BlackboardFactory creates a Blackboard for a new task.
// The taskID is a unique identifier for the orchestration request.
type BlackboardFactory func(taskID string) orchestration.Blackboard

// Orchestrator coordinates the agent's reasoning cycle: c0wrk-specific concerns
// (routing, PersistentBlackboard, history) around the sp4rk Plan&Execute engine.
type Orchestrator struct {
	router           *router.Router
	llm              agent.LLMCaller
	modelSwitcher    *llm.Router // raw LLM router for per-message model override
	visionResolver   markitdown.VisionResolver
	toolRegistry     *sdktools.ToolRegistry
	toolExec         agent.ToolExecutor  // executor tool surface (per-session policy view)
	coreToolRegistry *tools.ToolRegistry // core registry with policy support
	config           OrchestratorConfig
	contextFactory   ContextManagerFactory
	logger           *slog.Logger
	// serviceMetrics is the builder-owned per-kind collector for auxiliary one-shot
	// service calls, shared via OrchestratorDeps (nil-safe).
	serviceMetrics  *ServiceMetrics
	emitter         Emitter
	modelRegistry   *llm.ModelRegistry
	localModelProbe LocalModelProbe // lazily probes OpenAI-compatible endpoints (LM Studio/vLLM/…) for the runtime context window
	bbFactory       BlackboardFactory
	// modelMu guards the per-request LLM identity fields (config.Model,
	// config.ReasoningEffort) — access only via the current*/setCurrent* accessors;
	// the rest of config is immutable after Build.
	modelMu sync.RWMutex

	// e2sSettingsOverride, when set, supersedes config.E2S (immutable after Build)
	// with settings refreshed via SetE2SSettings; readers go through e2sSettings().
	e2sSettingsOverride atomic.Pointer[E2SSettings]

	// mcpMentions holds ephemeral intent only for explicitly nonpersistent callers.
	// Durable tasks load through taskStore on every entry, never from this cache.
	mcpMentions sync.Map

	// modelProfilesSettingsOverride mirrors e2sSettingsOverride for model-profile
	// settings (SetModelProfilesSettings / modelProfilesSettings()).
	modelProfilesSettingsOverride atomic.Pointer[ModelProfilesSettings]

	// historyMu guards conversationHistory (request goroutines + Wails-RPC readers);
	// access only via historySnapshot/setConversationHistory/appendHistory.
	historyMu sync.RWMutex
	// conversationHistory holds prior user/assistant exchanges from the session.
	conversationHistory []llm.Message
	// forecastMu guards compactionForecast (calibration/restore writers, status-poll
	// readers); access via compactionForecastSnapshot / SetCompactionForecast.
	forecastMu sync.Mutex
	// compactionForecast holds EWMA-calibrated compression-ratio forecasts for LLM
	// manual compaction (seeded from config, refined after each compaction).
	compactionForecast sdkmemory.CompactionForecast
	taskStore          TaskPersistence       // optional, for ContinueTask blackboard restoration
	bbRestoreFunc      BlackboardRestoreFunc // optional, restores PersistableBlackboard from store
	trackingCaller     *llm.TrackingCaller   // for per-step context tracker wiring
	tokenCounter       llm.TokenCounter      // for token counting in planner history compaction
	vectorSearchFunc   builtins.VectorSearchFunc
	// vectorSearchWaitFunc — the readiness waiter paired with vectorSearchFunc;
	// full semantics on OrchestratorDeps.VectorSearchWaitFunc.
	vectorSearchWaitFunc builtins.VectorSearchWaitFunc
	// Semantics on OrchestratorDeps.VectorSearchWaitTimeout.
	vectorSearchWaitTimeout time.Duration
	// Semantics on OrchestratorDeps.VectorSearchWaitDisabled.
	vectorSearchWaitDisabled bool
	skillManager             *skills.SkillManager // for skill discovery and activation
	agentManager             *agents.AgentManager // for subagent discovery (drives "Available/Requested Subagents" prompt sections)

	// Conductor dependencies (stored at construction time for runConductor).
	reflector        *reflector.Reflector
	providerName     string
	stepDumpTracker  *orchestration.StepDumpTracker
	toolCache        *agent.ToolResultCache
	perToolTrunc     map[string]agent.ToolTruncationConfig
	toolResultBudget agent.ToolResultBudget
	circuitBreaker   agent.CircuitBreakerConfig

	// onCleanup is invoked exactly once from Cleanup (session delete or app
	// shutdown) to release builder-owned resources; nil when no hook is set.
	onCleanup func()

	// verifyOnEdit is the edit-verification runner (delta on
	// OrchestratorDeps.VerifyOnEdit: specs/domains/verify-on-edit.md); nil default.
	verifyOnEdit agent.EditVerifyRunner
	// verifyOnEditMaxOutputChars caps the verification output injected into
	// context; 0 falls back to agent.DefaultVerifyOnEditCap.
	verifyOnEditMaxOutputChars int

	// isNoProject: inside the No Project pseudo-project index-dependent tools are
	// disabled and auto-RAG/verify-on-edit suppressed; code CHAT still routes "code".
	isNoProject bool

	// requestInFlight enforces the one-active-request invariant (HandleMessage CAS,
	// concurrent callers get ErrRequestInFlight; defer clears).
	requestInFlight atomic.Bool

	// agentsMDCache caches AGENTS.md content per workspace path with mtime check;
	// accessed only from injectVectorSearchHints (single-flight per instance).
	agentsMDCache map[string]agentsMDCacheEntry

	// goalProposer is the backend hook submitting a {condition, verify} proposal and
	// blocking for approval; nil → deriveGoal fails fast (goal request errors).
	goalProposer tools.GoalProposer

	// goalTurnRunner runs ONE goal-loop turn (turn 1: route→plan→Conductor; after:
	// reused-routing ReAct→Conductor) — single test seam; nil → defaultGoalTurnRunner.
	goalTurnRunner func(ctx context.Context, turn int, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, deps conductorDeps) (toolCallCount int, result *orchestration.ExecutionResult, err error)

	// goalVerifier is the independent verifier re-checking a "met" verdict (isolated
	// pass, fresh blackboard); nil → defaultGoalVerifier. See specs/domains/goal-mode.md.
	goalVerifier func(ctx context.Context, gs *goal.GoalState, verdict *goal.Verdict, message string, lastTurnOutput string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, deps conductorDeps) (*tools.VerificationOutcome, error)

	// goalSuggester is the goal-mode suitability classifier seam
	// (SuggestGoalMode); nil → production implementation. See core/goal_suggest.go.
	goalSuggester GoalSuggester

	// goalSuggestionProposer is the desktop-side pending-card bridge for the
	// post-routing suggestion (see GoalSuggestionProposer); nil disables the gate.
	goalSuggestionProposer GoalSuggestionProposer

	// goalSuggestionDeclined is the session-scoped suppression flag: after one
	// decline the gate stays silent for the session's lifetime.
	goalSuggestionDeclined atomic.Bool

	// activePause is the universal pause signal for the running conductor: swapped
	// in at request entry, cleared on exit (no stale signal pauses a later request).
	activePause atomic.Pointer[atomic.Bool]

	// liveMu guards liveMessages — user messages sent mid-request; the executor
	// drains per step boundary, late arrivals via the epilogue.
	liveMu sync.Mutex
	// liveMessages holds queued live user messages in FIFO order.
	liveMessages []string

	// resumeCompactionMu guards resumeCompactionStrategy — the one-shot "compact
	// on resume" request; producer (RequestResumeCompaction) and consumer (Resume)
	// run concurrently, so arm/consume is mutex-guarded.
	resumeCompactionMu sync.Mutex
	// resumeCompactionStrategy holds the strategy name ("sliding_window" |
	// "summarization" | "hierarchical") armed for the NEXT Resume; empty keeps the
	// normal routing-derived, threshold-driven behavior.
	resumeCompactionStrategy string

	// resumeRerouteMu guards resumeRerouteRequested — the one-shot "re-route on
	// resume" request; producer (RequestResumeReroute) and consumer (Resume) may
	// run on different goroutines, so arm/consume is mutex-guarded.
	resumeRerouteMu sync.Mutex
	// resumeRerouteRequested is true when the next Resume must re-run routing
	// (original run never reached it); false keeps reuse-without-routing — a routed
	// task is never re-classified.
	resumeRerouteRequested bool
}

// ErrRequestInFlight is returned by HandleMessage when another call on the same
// *Orchestrator is already running; the session layer serializes per-session.
var ErrRequestInFlight = errors.New("orchestrator: request already in flight")

// installPauseSignal installs the request's pause signal and returns the clear
// function to defer (protocol on activePause: specs/domains/goal-mode.md).
func (o *Orchestrator) installPauseSignal() func() {
	pause := &atomic.Bool{}
	o.activePause.Store(pause)
	return func() { o.activePause.Store(nil) }
}

// PauseSession flips the active pause signal; no-op with no request in flight.
// Cooperative checkpoint semantics: specs/domains/session-lifecycle.md ("Task Resumption").
func (o *Orchestrator) PauseSession() {
	if p := o.activePause.Load(); p != nil {
		p.Store(true)
	}
}

// newPauseChecker returns the pause-checker closure for ConductorConfig: reads the
// active pause signal live, always false when none installed.
func (o *Orchestrator) newPauseChecker() func(context.Context) bool {
	return func(context.Context) bool {
		if p := o.activePause.Load(); p != nil {
			return p.Load()
		}
		return false
	}
}

// QueueLiveUserMessage appends a user message to the live queue for delivery at
// the next step boundary; no-op for empty text (protocol on liveMessages).
func (o *Orchestrator) QueueLiveUserMessage(text string) {
	if text == "" {
		return
	}
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	o.liveMessages = append(o.liveMessages, text)
}

// DrainLiveUserMessages removes and returns the oldest queued live message, or ""
// when empty (per-boundary poll; see also TakeLiveUserMessages).
func (o *Orchestrator) DrainLiveUserMessages() string {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	if len(o.liveMessages) == 0 {
		return ""
	}
	msg := o.liveMessages[0]
	o.liveMessages = o.liveMessages[1:]
	return msg
}

// RequestResumeCompaction arms the one-shot "compact on resume" request (the NEXT
// Resume consumes it; empty is a no-op, re-arming overwrites). See resumeCompactionStrategy.
func (o *Orchestrator) RequestResumeCompaction(strategy string) {
	if strategy == "" {
		return
	}
	o.resumeCompactionMu.Lock()
	defer o.resumeCompactionMu.Unlock()
	o.resumeCompactionStrategy = strategy
}

// ClearResumeCompaction disarms the request (via clearResumeRequests when the
// armed task dies — a stale arm must not compact an unrelated later task).
func (o *Orchestrator) ClearResumeCompaction() {
	o.resumeCompactionMu.Lock()
	defer o.resumeCompactionMu.Unlock()
	o.resumeCompactionStrategy = ""
}

// consumeResumeCompaction atomically reads and clears the armed strategy; "" keeps
// Resume's normal threshold-driven behavior. See resumeCompactionStrategy.
func (o *Orchestrator) consumeResumeCompaction() string {
	o.resumeCompactionMu.Lock()
	defer o.resumeCompactionMu.Unlock()
	strategy := o.resumeCompactionStrategy
	o.resumeCompactionStrategy = ""
	return strategy
}

// RequestResumeReroute arms the one-shot "re-route on resume" request for the NEXT
// Resume (only meaningful when routing never ran — see resumeRerouteRequested).
func (o *Orchestrator) RequestResumeReroute() {
	o.resumeRerouteMu.Lock()
	defer o.resumeRerouteMu.Unlock()
	o.resumeRerouteRequested = true
}

// ClearResumeReroute drops an armed request (via clearResumeRequests when the armed
// task dies — a stale arm must not re-classify a later task); defense-in-depth
// behind Resume's `forceReroute && routing == nil` guard. See
// specs/domains/orchestration/conductor.md, "Never-routed exception".
func (o *Orchestrator) ClearResumeReroute() {
	o.resumeRerouteMu.Lock()
	defer o.resumeRerouteMu.Unlock()
	o.resumeRerouteRequested = false
}

// consumeResumeReroute atomically reads and clears the armed request; false keeps
// Resume's reuse-without-routing behavior. See resumeRerouteRequested.
func (o *Orchestrator) consumeResumeReroute() bool {
	o.resumeRerouteMu.Lock()
	defer o.resumeRerouteMu.Unlock()
	requested := o.resumeRerouteRequested
	o.resumeRerouteRequested = false
	return requested
}

// TakeLiveUserMessages removes and returns ALL queued live messages (FIFO), or nil
// when empty; the epilogue turns them into the follow-up task's text.
func (o *Orchestrator) TakeLiveUserMessages() []string {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	if len(o.liveMessages) == 0 {
		return nil
	}
	msgs := o.liveMessages
	o.liveMessages = nil
	return msgs
}

// DiscardLiveUserMessages drops all queued live messages (user stop): an
// undelivered message must not leak into a future request.
func (o *Orchestrator) DiscardLiveUserMessages() {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	o.liveMessages = nil
}

// emitSessionPaused surfaces a cooperative pause (service message, "pause" phase);
// the resumable-task safety net offers the Resume action. Nil-safe.
func (o *Orchestrator) emitSessionPaused() {
	if o.emitter == nil {
		return
	}
	o.emitter.ServiceWithMeta("Task paused — use Resume to continue.", map[string]any{"phase": "pause"})
}

// LocalModelProbe is a best-effort, non-blocking hook that discovers the real
// context window for a model served from an OpenAI-compatible endpoint and writes
// it into the session's ModelRegistry; invoked on model selection (construction +
// every mid-chat switch). See specs/domains/llm-providers.md, "Lazy local-model probe".
type LocalModelProbe func(model string)

// OrchestratorDeps holds the runtime dependencies for the Orchestrator.
type OrchestratorDeps struct {
	Router         *router.Router
	LLM            agent.LLMCaller
	ToolExec       agent.ToolExecutor
	ToolRegistry   *sdktools.ToolRegistry
	TokenCounter   llm.TokenCounter
	ContextFactory ContextManagerFactory
	Reflector      *reflector.Reflector // optional, nil-safe
	Logger         *slog.Logger         // optional, nil-safe
	Emitter        Emitter              // optional, uses noopEmitter if nil
	// ServiceMetrics is the builder-owned per-kind collector for auxiliary one-shot
	// service calls (issue #64); manual-compaction summarization records into it.
	ServiceMetrics   *ServiceMetrics
	ModelRegistry    *llm.ModelRegistry // optional, nil-safe
	ToolResultBudget agent.ToolResultBudget
	CircuitBreaker   agent.CircuitBreakerConfig
	BBFactory        BlackboardFactory         // optional, nil = default MapBlackboard
	TrackingCaller   *llm.TrackingCaller       // optional, for per-step context tracker wiring
	VectorSearchFunc builtins.VectorSearchFunc // optional, for auto-RAG hint generation
	// VectorSearchWaitFunc is the bounded readiness waiter paired with
	// VectorSearchFunc: one deadline covers both the wait and the search (the search
	// closure never waits itself — HybridSearchNoWait on the desktop wiring).
	VectorSearchWaitFunc builtins.VectorSearchWaitFunc // optional
	// VectorSearchWaitTimeout bounds how long injectVectorSearchHints waits before
	// skipping RAG hints (vector_index.search_wait_timeout_ms); zero (unset) defaults
	// to defaultVectorSearchWaitTimeout, explicit fail-fast 0 rides VectorSearchWaitDisabled.
	VectorSearchWaitTimeout time.Duration
	// VectorSearchWaitDisabled marks an explicit vector_index.search_wait_timeout_ms: 0
	// (fail fast — never wait for index readiness). The desktop layer resolves "unset"
	// to 3000ms first, so a zero reaching it is unambiguously the user's choice.
	VectorSearchWaitDisabled bool
	SkillManager             *skills.SkillManager // optional, for skill discovery and activation
	AgentManager             *agents.AgentManager // optional, for subagent discovery ("Available/Requested Subagents" prompt sections); nil-safe
	CoreToolRegistry         *tools.ToolRegistry  // per-session registry for No-Project tool disabling
	ModelSwitcher            *llm.Router          // raw LLM router for per-message model override

	// VisionResolver inspects the CURRENT active model and returns markitdown
	// connection parameters when it is vision-capable; injected so document
	// conversions caption images with the model active at conversion time. Nil-safe.
	VisionResolver markitdown.VisionResolver

	// Tool result caching and per-tool truncation.
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig

	// StepDumpTracker manages per-step LLM dump files. Created by the session layer.
	// If nil, per-step dumps are disabled.
	StepDumpTracker *orchestration.StepDumpTracker

	// ProviderName is the active provider name for DEBUG-level LLM logging
	// in per-step callers (LoggingLLMCaller wrapping).
	ProviderName string

	// LocalModelProbe — see the LocalModelProbe type. Nil when no OpenAI-compatible
	// providers are configured: switches and the default model go unprobed. Nil-safe.
	LocalModelProbe LocalModelProbe

	// OnCleanup is invoked exactly once from Cleanup (session delete or shutdown);
	// the builder releases the registry's live-tracking entry through it. Nil-safe.
	OnCleanup func()

	// VerifyOnEdit arms the mechanical edit-verification hook (executor.verify_on_edit
	// — specs/domains/verify-on-edit.md): the user-configured command runs after each
	// successful write_file/edit_file as a system observation. Nil disables.
	VerifyOnEdit agent.EditVerifyRunner

	// VerifyOnEditMaxOutputChars caps the verification output injected into
	// context; 0 falls back to agent.DefaultVerifyOnEditCap.
	VerifyOnEditMaxOutputChars int
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(cfg OrchestratorConfig, deps OrchestratorDeps) *Orchestrator {
	if cfg.MaxRedelegationDepth == 0 {
		cfg.MaxRedelegationDepth = 2
	}
	if cfg.MaxParallelSubagents == 0 {
		cfg.MaxParallelSubagents = 4
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
		router:                     deps.Router,
		llm:                        deps.LLM,
		modelSwitcher:              deps.ModelSwitcher,
		visionResolver:             deps.VisionResolver,
		toolRegistry:               deps.ToolRegistry,
		toolExec:                   deps.ToolExec,
		config:                     cfg,
		contextFactory:             deps.ContextFactory,
		logger:                     deps.Logger,
		serviceMetrics:             deps.ServiceMetrics,
		emitter:                    emitter,
		modelRegistry:              deps.ModelRegistry,
		localModelProbe:            deps.LocalModelProbe,
		bbFactory:                  deps.BBFactory,
		trackingCaller:             deps.TrackingCaller,
		tokenCounter:               deps.TokenCounter,
		vectorSearchFunc:           deps.VectorSearchFunc,
		vectorSearchWaitFunc:       deps.VectorSearchWaitFunc,
		vectorSearchWaitTimeout:    deps.VectorSearchWaitTimeout,
		vectorSearchWaitDisabled:   deps.VectorSearchWaitDisabled,
		skillManager:               deps.SkillManager,
		agentManager:               deps.AgentManager,
		coreToolRegistry:           deps.CoreToolRegistry,
		reflector:                  deps.Reflector,
		providerName:               deps.ProviderName,
		stepDumpTracker:            deps.StepDumpTracker,
		toolCache:                  deps.ToolCache,
		perToolTrunc:               deps.PerToolTruncation,
		toolResultBudget:           deps.ToolResultBudget,
		circuitBreaker:             deps.CircuitBreaker,
		onCleanup:                  deps.OnCleanup,
		verifyOnEdit:               deps.VerifyOnEdit,
		verifyOnEditMaxOutputChars: deps.VerifyOnEditMaxOutputChars,
		compactionForecast:         resolveCompactionForecast(cfg.Compaction.Forecast),
	}

	return o
}

// Cleanup releases all resources held by the orchestrator (e.g., per-step dump files).
func (o *Orchestrator) Cleanup() {
	// StepDumpTracker cleanup is owned by the session layer (orchestrator only holds
	// a reference); clear-before-invoke makes repeated calls no-ops.
	if o.onCleanup != nil {
		fn := o.onCleanup
		o.onCleanup = nil
		fn()
	}
}

// ResolveVisionOptions returns markitdown vision parameters for the model CURRENTLY
// active on the router, or nil when captioning must not use it (re-resolved per call).
func (o *Orchestrator) ResolveVisionOptions() *markitdown.VisionOptions {
	if o.visionResolver == nil {
		return nil
	}
	return o.visionResolver()
}

// SetGoalProposer injects the goal-proposer hook (the desktop approval flow
// propose_goal blocks on); without it, goal derivation fails fast.
func (o *Orchestrator) SetGoalProposer(proposer tools.GoalProposer) {
	o.goalProposer = proposer
}

// compile-time assertion that *Orchestrator satisfies GoalProposerSetter.
var _ GoalProposerSetter = (*Orchestrator)(nil)

// SetGoalSuggestionProposer injects the suggestion proposer (desktop pending-card
// flow), mirroring SetGoalProposer; nil disables the suggestion gate entirely.
func (o *Orchestrator) SetGoalSuggestionProposer(proposer GoalSuggestionProposer) {
	o.goalSuggestionProposer = proposer
}

// logInfo logs an INFO level message if logger is not nil.
func (o *Orchestrator) logInfo(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Info(msg, args...)
	}
}

// logWarn logs a WARN level message if logger is not nil.
func (o *Orchestrator) logWarn(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Warn(msg, args...)
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

// buildSkillAugmentedRoutingMessage prepends "/name (skill: description)" per skill
// so the router classifies actual task semantics, not the stripped bare remainder.
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

// resolveTaskMessage returns the effective task message for everything downstream
// of routing: a /skill invocation stripped by preprocessMessageText can leave an
// empty message (providers reject it, HTTP 400), so skill context is restored; the
// router keeps the raw message (double-prefix).
func (o *Orchestrator) resolveTaskMessage(message string, userSkills []string) string {
	if len(userSkills) == 0 || o.skillManager == nil {
		return message
	}
	return o.buildSkillAugmentedRoutingMessage(message, userSkills)
}

// augmentWithAttachments appends the session's attached-file list to the user
// message so the LLM can read them via read_attachment. Only the Conductor sees the
// section — history keeps the clean message (continuations restore from the blackboard).
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

// Resume continues a previously interrupted task from its checkpoint; the
// blackboard must be pre-loaded with its persisted state (via RestoreBlackboard).
// Nudge landing and the error contract (ErrExecutionIncomplete partial success,
// ErrPaused clean checkpoint): specs/domains/session-lifecycle.md ("Task Resumption").
func (o *Orchestrator) Resume(ctx context.Context, bb orchestration.Blackboard, routing *router.RoutingDecision, plansDir string, resumeSteps []agent.Step, goalState *goal.GoalState, nudge string) (result *HandleResult, err error) {
	o.logDebug("orchestrator: resume started", "resumeSteps", len(resumeSteps), "nudge", nudge != "")

	// Consume the re-route arm before any early return (see consumeResumeReroute).
	forceReroute := o.consumeResumeReroute()

	// Model Profiles goal guard (ErrGoalBlockedByModelProfiles,
	// specs/domains/model-profiles.md): a paused non-terminal goal must not re-enter
	// the goal loop under essential-tools narrowing — checked before ANY side effect.
	if goalState != nil && !goalState.Status.IsTerminal() && o.modelProfilesEssentialToolsEnabled() {
		return nil, ErrGoalBlockedByModelProfiles
	}

	ctx, err = o.prepareTaskMCP(ctx, bb, nil)
	if err != nil {
		return nil, err
	}
	// Capability snapshots pin the authorization view for a resumed task (no fresh
	// mentions exist on resume): restored skills may have changed mode while paused.
	ctx = o.attachCapabilitySnapshots(ctx)

	// The user-picked compaction strategy for THIS resume (RequestResumeCompaction),
	// threaded into BOTH resume paths; empty = not requested.
	forceCompactionStrategy := o.consumeResumeCompaction()

	// Universal pause signal for this resumed request (as in HandleMessage).
	defer o.installPauseSignal()()

	// Outcome lands in the in-memory history (recordResumeOutcome); the user
	// message was recorded at first run or restored from the message store.
	defer func() {
		o.recordResumeOutcome(ctx, bb, result, err)
	}()

	// Generate RAG hints from vector index using the original request.
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		ctx = o.injectVectorSearchHints(ctx, origReq)
	}

	// Vision-assisted document conversion on the resume path (mirrors
	// prepareRequestContext; per-conversion model resolution). Nil-safe.
	ctx = markitdown.WithVisionResolver(ctx, o.visionResolver)

	// Model Profiles prompt profile on the resume path (mirrors prepareRequestContext);
	// without it the resumed run — including a resumed goal's verifier — misses Lite.
	ctx = o.applyModelProfilesPromptProfile(ctx)

	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetEmitter(o.emitter)
	}
	o.wireAttachmentNameResolver(bb)

	// Emit initial context_fill
	o.emitInitialContextFill()

	// Routing is reused, never re-run; nil falls back to "general"/standard mode
	// (specs/domains/session-lifecycle.md, "Task Resumption").
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

	// AUTO-RESUME WAVE (system-driven, no LLM — see resumePausedWork): settles
	// everything paused before the first LLM call; its factual summary rides along.
	wave, err := o.resumePausedWork(ctx, bb, nudge)
	waveFallbackNote := ""
	if err != nil {
		// The wave is best-effort scaffolding: a wiring failure must not doom the task;
		// fall through — subagent failures surface in their results, not as a wave error.
		o.logWarn("resume_task: auto-resume wave skipped", "error", err)
		// The plan-continuation context must not degrade silently: without the wave the
		// "continue the approved plan" framing is gone — note it factually to the conductor.
		if planHasUnreachedSteps(bb) {
			waveFallbackNote = "\n\n[Auto-resume note: the system resume wave could not run and the approved plan still has unreached steps; continue it via execute_plan.]"
		}
	}
	if wave.pausedAgain {
		o.logInfo("resume_task: auto-resume wave paused again — checkpointing without an LLM call")
		o.emitSessionPaused()
		if pbb, ok := bb.(PersistableBlackboard); ok {
			persistTaskOutcome(pbb, &orchestration.ExecutionResult{Status: orchestration.ExecutionStatusPaused})
		}
		return &HandleResult{
			Output:          "",
			RoutingDecision: routing,
			Blackboard:      bb,
			Status:          orchestration.ExecutionStatusPaused,
		}, nil
	}

	// The restored blackboard carries prior facts/step results (search_facts/
	// read_step_output); a plan is not required. Seeding: specs/domains/session-lifecycle.md.
	availableTools := o.toolRegistry.ListFiltered(o.disabledToolNames())

	availableTools = stripGatedMCPTools(ctx, availableTools)

	// A paused/still-active goal re-enters the goal loop; resumeSteps seeds the
	// executor (specs/domains/session-lifecycle.md, "Goal resume").
	if goalState != nil && !goalState.Status.IsTerminal() {
		o.logInfo("resume_task: resuming goal loop", "status", goalState.Status, "turn", goalState.TurnCount)
		goalMessage := bb.GetOriginalRequest()
		if wave.summary != "" {
			goalMessage += wave.summary
		}
		if waveFallbackNote != "" {
			goalMessage += waveFallbackNote
		}
		return o.resumeGoalLoop(ctx, goalMessage, bb, availableTools, plansDir, routing, goalState, resumeSteps, nudge, forceCompactionStrategy)
	}

	// E2S resume: a paused/interrupted E2S task re-enters the E2S loop with its
	// persisted Σ (specs/domains/e2s.md); terminal/absent states fall through.
	if e2sState := o.loadE2SResumeState(bb); e2sState != nil {
		return o.resumeE2SLoop(ctx, bb, availableTools, routing, e2sState, nudge)
	}

	// Strip goal-mode-only tools for the non-goal resume path (see the
	// HandleMessage strip).
	availableTools = tools.StripGoalModeTools(availableTools)

	// Armed re-route (RequestResumeReroute): routing never ran, so the defaults above
	// would run under the wrong domain (rerouteResumedTask; Conductor path only).
	if forceReroute && routing == nil {
		routedCtx, decision, rerouteErr := o.rerouteResumedTask(ctx, bb, availableTools)
		if rerouteErr != nil {
			return nil, rerouteErr
		}
		ctx = routedCtx
		routing = decision
		// Persist immediately (mirroring finalizeResult): otherwise the next resume
		// falls back to "general".
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetRouting(decision)
		}
	}

	// Continuable resume: paused/interrupted with unreached plan steps — seed the run
	// so the plan workflow stays active (execute_plan without re-declare, delegate
	// disabled, declare_plan soft-hints). Fully-done/plan-less tasks resume without it.
	resumedWithPlan := planHasUnreachedSteps(bb)
	o.logInfo("resume_task", "resumedWithPlan", resumedWithPlan)

	// Task message: original request + the wave's factual summary (see
	// resumePausedWork); stored history keeps the clean original (assistant side only).
	taskMessage := bb.GetOriginalRequest()
	// Resume-with-nudge rides on the task message (specs/domains/
	// session-lifecycle.md, "Session Pause / Resume / Nudge").
	if nudge != "" {
		taskMessage += "\n\n## User follow-up on resume\n\n" + nudge
	}
	if wave.summary != "" {
		taskMessage += wave.summary
	}
	if waveFallbackNote != "" {
		taskMessage += waveFallbackNote
	}

	// Reconstruct the original run's image blocks from the conversation history; the
	// text block is rebuilt from the directive-carrying task message so it reflects
	// current attachments (SetTask is text-only). The "## Attached files" augmentation
	// is UNCONDITIONAL: the only place the model learns an attachment_id.
	conductorMessage := o.augmentWithAttachments(taskMessage, bb)
	var resumeContentBlocks []llm.ContentBlock
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		if imageBlocks := imageBlocksForRequest(o.historySnapshot(), origReq); len(imageBlocks) > 0 {
			resumeContentBlocks = buildContentBlocks(conductorMessage, imageBlocks)
		}
	}

	// Same history window HandleMessage injects; dropFailedExchangeTail removes
	// the trailing failed exchange of the original request (see its doc).
	conversationHistory := truncateHistory(dropFailedExchangeTail(o.historySnapshot(), bb.GetOriginalRequest()), o.config.ConductorHistoryWindow)

	execResult, err := o.runConductor(ctx, conductorMessage, bb, availableTools, plansDir, conversationHistory, resumeSteps, resumeContentBlocks, "", forceCompactionStrategy, resumedWithPlan)
	// Cooperative pause: a clean checkpoint — surface, persist resumable, return
	// nil error (specs/domains/session-lifecycle.md, "Task Resumption").
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

	if pbb, ok := bb.(PersistableBlackboard); ok {
		persistTaskOutcome(pbb, execResult)
	}

	o.logDebug("orchestrator: resume completed")
	return result, incompleteErr
}

// rerouteResumedTask re-runs routing for a resumed task whose original run failed
// before routing completed (RequestResumeReroute): classify, activate skills, apply
// domain/complexity, return the refreshed context. Request TEXT only — an explicit
// /-mention ref is NOT re-applied (conductor.md, "Never-routed exception").
func (o *Orchestrator) rerouteResumedTask(ctx context.Context, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor) (context.Context, *router.RoutingDecision, error) {
	o.logInfo("resume_task: no persisted routing decision — re-running the routing stage")
	routedCtx, decision, _, _, err := o.routeAndActivateSkills(ctx, bb.GetOriginalRequest(), HandleOptions{}, bb, availableTools)
	if err != nil {
		return ctx, nil, err
	}
	routedCtx = WithDomain(routedCtx, decision.Domain)
	routedCtx = WithComplexity(routedCtx, decision.Complexity)
	return routedCtx, decision, nil
}

// resumeWaveOutcome reports what the auto-resume wave settled: summary is a factual,
// bounded digest of settled subagent outcomes (empty when nothing settled);
// pausedAgain = the pause signal re-tripped mid-wave, checkpointing in-flight units.
type resumeWaveOutcome struct {
	summary     string
	pausedAgain bool
}

// waveSummaryOutputCap bounds each subagent's output excerpt inside the wave
// summary so a huge delegation result cannot flood the resumed task message.
const waveSummaryOutputCap = 300

// resumePausedWork is the system-driven half of the pause invariant: before the
// resumed task's first LLM call everything paused settles formally — no model
// decision involved. Outcome contract: specs/domains/session-lifecycle.md
// (unified recovery ledger) + specs/decisions/048-unified-recovery-ledger.md.
func (o *Orchestrator) resumePausedWork(ctx context.Context, bb orchestration.Blackboard, nudge string) (resumeWaveOutcome, error) {
	// Context parity with the Conductor path: the wave relaunches subagents DIRECTLY
	// through the launcher, bypassing orchestration.Conductor.Run — the sole place the
	// executor context gains the BB-backed stores; without them the fact tools fail.
	ctx = agent.WithStepOutputStore(ctx, orchestration.NewStepOutputStore(bb))
	ctx = agent.WithFactStore(ctx, orchestration.NewFactStore(bb))
	ctx = agent.WithAttachmentStore(ctx, orchestration.NewAttachmentStore(bb))
	ctx = agent.WithFinalResultStore(ctx, orchestration.NewFinalResultStore(bb))

	// One funnel, one enumeration: every in-flight unit (all kinds/depths/namespaces,
	// including the goal verifier's) lifts from the durable unit ledger and settles below.
	unitsToSettle := o.resumeUnitsForBlackboard(bb)

	relaunchable := 0
	for _, u := range unitsToSettle {
		if u.relaunchable() {
			relaunchable++
		}
	}
	continuablePlan := planHasUnreachedSteps(bb)
	if !continuablePlan && relaunchable == 0 {
		return resumeWaveOutcome{}, nil
	}

	o.logInfo("resume_task: auto-resume wave starting", "continuable_plan", continuablePlan, "relaunchable_units", relaunchable)

	deps := o.buildConductorDeps(nil, nil)
	planState := newPlanRunState(true)
	inlineLifecycle := newInlineStepLifecycle(deps.emitter, bb)
	// Issue #99: restored-successful steps settle BEFORE the wave, silently —
	// specs/domains/session-lifecycle.md ("Resume wave: silent replay of completed steps").
	inlineLifecycle.seedCompletedFromBlackboard()
	inlineLifecycle.planState = planState
	deps.lifecycle = inlineLifecycle
	launcher := &conductorLauncher{deps: deps, bb: bb, planState: planState}

	var sb strings.Builder
	outcome := resumeWaveOutcome{}

	// Branch 1 — continue the plan through the DAG engine.
	if continuablePlan {
		// preWaveSuccess keeps the summary from re-listing work finished in an
		// earlier run (specs/domains/session-lifecycle.md, "Resume wave").
		preWaveSuccess := make(map[string]bool)
		if plan := bb.GetPlan(); plan != nil {
			for _, step := range plan.Steps {
				if sr, ok := bb.GetStepResult(step.ID); ok && sr.Error == nil {
					preWaveSuccess[step.ID] = true
				}
			}
		}
		results, err := launcher.Execute(ctx, nil)
		if err != nil {
			if ctx.Err() != nil {
				return outcome, ctx.Err()
			}
			return outcome, fmt.Errorf("auto-resume plan continuation failed: %w", err)
		}
		for _, r := range results {
			if r.Status == "paused" {
				outcome.pausedAgain = true
			}
			if preWaveSuccess[r.StepID] {
				continue
			}
			writeWaveSummaryLine(&sb, r.StepID, r.Status, r.Output, r.Error)
		}
	}

	// Branch 2 — settle the enumerated units through the single ledger funnel
	// (deepest scope first, so children settle before their parents).
	if relaunchable > 0 && !outcome.pausedAgain {
		pausedAgain, err := o.resumeUnits(ctx, bb, launcher, unitsToSettle, nudge, &sb)
		if err != nil {
			return outcome, err
		}
		outcome.pausedAgain = pausedAgain
	}

	if sb.Len() > 0 {
		outcome.summary = "\n\n## Auto-resumed subagents\n\nThe subagents below were paused or interrupted before this run and have been settled by the system; their full outputs are on the blackboard (read_step_output):\n\n" + sb.String()
	}
	return outcome, nil
}

// resumeUnit is one execution unit the resume funnel settles, normalized from the
// durable unit ledger or (pre-ledger tasks) the legacy delegation-spec store — one
// shape so every kind settles uniformly. See specs/domains/orchestration/delegation.md
// ("Durable Unit Ledger and Resume Relaunch").
type resumeUnit struct {
	id   string
	task tools.DelegationTask
	// parentID scopes a unit under its delegating unit ("" at top level; a
	// goal-verifier delegate is parented under the verifier unit).
	parentID string
	// scope is the wave-registry group key ("" for mainline; isolated-context
	// units are filtered before normalization — see resumeUnitsForBlackboard).
	scope   string
	depth   int
	status  units.UnitStatus
	steps   []agent.Step
	output  string
	execErr error
}

// relaunchable reports whether the funnel must (re)launch the unit — every
// non-terminal unit (paused from its checkpoint, the rest fresh; see the
// classification table in specs/domains/orchestration/delegation.md).
func (u resumeUnit) relaunchable() bool {
	return !u.terminal()
}

// terminal reports a final outcome (completed/failed): replayed into the wave
// registries for dependency resolution, never re-run. "Finished" is defined by
// units.UnitStatus.Terminal (interrupted is deliberately not terminal).
func (u resumeUnit) terminal() bool {
	return u.status.Terminal()
}

// paused reports whether the unit carries a resumable checkpoint.
func (u resumeUnit) paused() bool {
	return u.status == units.UnitStatusPaused
}

// resumeUnitsForBlackboard enumerates every unit the resume funnel must settle:
// the durable ledger's mainline units merged with legacy specs (ledger wins by
// id); isolated-context units are excluded (the owning loop re-derives them); an
// unreadable ledger is not fatal — resume proceeds with legacy specs + plan arm.
func (o *Orchestrator) resumeUnitsForBlackboard(bb orchestration.Blackboard) []resumeUnit {
	ledger := resumeLedgerFromBlackboard(bb)
	unitsToSettle, err := resumeUnitsFromLedger(ledger, bb)
	if err != nil {
		o.logWarn("resume_task: unit ledger unreadable — falling back to legacy delegation specs", "error", err)
		unitsToSettle = nil
	}
	seen := make(map[string]bool, len(unitsToSettle))
	for _, u := range unitsToSettle {
		seen[u.id] = true
	}
	return append(unitsToSettle, legacyResumeUnits(delegationSpecsFromBlackboard(bb), bb, seen)...)
}

// resumeLedgerFromBlackboard derives the mainline unit ledger (nil without a durable
// unit store); blackboard-cached so the funnel reads what the prior run wrote.
func resumeLedgerFromBlackboard(bb orchestration.Blackboard) units.Ledger {
	if pbb, ok := bb.(PersistableBlackboard); ok {
		return NewBlackboardLedger(pbb)
	}
	return nil
}

// resumeUnitsFromLedger enumerates the ledger's MAINLINE units into resumeUnits.
// Skip list: specs/domains/orchestration/delegation.md ("One funnel on Resume").
func resumeUnitsFromLedger(ledger units.Ledger, bb orchestration.Blackboard) ([]resumeUnit, error) {
	if ledger == nil {
		return nil, nil
	}
	recs, err := ledger.List()
	if err != nil {
		return nil, fmt.Errorf("resume: list units: %w", err)
	}
	out := make([]resumeUnit, 0, len(recs))
	for _, rec := range recs {
		if !relaunchableUnitKind(rec.Kind) {
			continue
		}
		if rec.Namespace != "" {
			continue
		}
		// No rebuild spec — cannot be relaunched (status still surfaces via
		// work_units).
		if len(rec.Spec) == 0 {
			continue
		}
		var spec tools.DelegationSpec
		if err := json.Unmarshal(rec.Spec, &spec); err != nil {
			continue
		}
		u := resumeUnit{
			id:       rec.ID,
			task:     spec.Task,
			parentID: rec.ParentID,
			scope:    rec.Namespace,
			depth:    rec.Depth,
			status:   rec.Status,
		}
		if u.task.ID == "" {
			u.task.ID = rec.ID
		}
		if len(rec.Steps) > 0 {
			var steps []agent.Step
			if err := json.Unmarshal(rec.Steps, &steps); err == nil {
				u.steps = steps
			}
		}
		// Reconcile with the blackboard outcome (written before the ledger — a
		// crash between the writes leaves them disagreeing):
		// specs/domains/orchestration/delegation.md ("One funnel on Resume").
		outcomeKnown := false
		if bb != nil {
			if sr, ok := bb.GetStepResult(rec.ID); ok {
				u.output, u.execErr = sr.FullOutput, sr.Error
				outcomeKnown = true
				switch {
				case isPaused(sr.Error):
					u.status = units.UnitStatusPaused
				case sr.Error != nil:
					u.status = units.UnitStatusFailed
				default:
					u.status = units.UnitStatusCompleted
				}
			}
		}
		// A durable checkpoint under a still-Resumable status is a paused checkpoint
		// (Checkpoint/Settle are separate writes) — resume, don't re-run; terminal stays.
		if len(u.steps) > 0 && u.status.Resumable() {
			u.status = units.UnitStatusPaused
		}
		// A failed unit may have no blackboard step result to lift (ledger-only settles)
		// — synthesize an error so the replay reports the dependency failed, not completed.
		if !outcomeKnown && u.status == units.UnitStatusFailed {
			u.execErr = fmt.Errorf("unit %q failed in a previous run", rec.ID)
		}
		out = append(out, u)
	}
	return out, nil
}

// relaunchableUnitKind reports whether a ledger unit kind is relaunchable work;
// containers (task root, goal-verification pass), plan steps and tools excluded.
func relaunchableUnitKind(kind units.UnitKind) bool {
	switch kind {
	case units.UnitKindSubagent, "":
		// "" is the documented default for a delegation (unitKindForDelegation),
		// so a unit without an explicit kind is still a delegate.
		return true
	default:
		return false
	}
}

// legacyResumeUnits lifts legacy delegation specs into resume units so a pre-ledger
// task still resumes; ids covered by the ledger are skipped (ledger is
// authoritative). No step result = no checkpoint: crash-interrupted, fresh relaunch.
func legacyResumeUnits(specs []tools.DelegationSpec, bb orchestration.Blackboard, seen map[string]bool) []resumeUnit {
	var out []resumeUnit
	for _, spec := range specs {
		if spec.Task.ID == "" || seen[spec.Task.ID] {
			continue
		}
		seen[spec.Task.ID] = true
		u := resumeUnit{
			id:       spec.Task.ID,
			task:     spec.Task,
			parentID: spec.ParentID,
			depth:    spec.Depth,
			status:   units.UnitStatusInterrupted,
		}
		if sr, ok := bb.GetStepResult(spec.Task.ID); ok {
			switch {
			case isPaused(sr.Error):
				u.status = units.UnitStatusPaused
			case sr.Error != nil:
				u.status = units.UnitStatusFailed
			default:
				u.status = units.UnitStatusCompleted
			}
			u.output, u.execErr, u.steps = sr.FullOutput, sr.Error, sr.Steps
		}
		out = append(out, u)
	}
	return out
}

// resumeUnits settles every enumerated unit, grouped by (scope, depth) deepest-first
// (child before parent); each group relaunches into its own registry at the unit's
// original depth, so a pause/resume cycle never grants extra redelegation levels —
// specs/domains/orchestration/delegation.md ("One funnel on Resume").
func (o *Orchestrator) resumeUnits(
	ctx context.Context,
	bb orchestration.Blackboard,
	launcher *conductorLauncher,
	unitsToSettle []resumeUnit,
	nudge string,
	sb *strings.Builder,
) (bool, error) {
	type groupKey struct {
		scope string
		depth int
	}
	groups := make(map[groupKey][]resumeUnit)
	var settled []resumeUnit
	for _, u := range unitsToSettle {
		if u.terminal() {
			settled = append(settled, u)
			continue
		}
		k := groupKey{scope: u.scope, depth: u.depth}
		groups[k] = append(groups[k], u)
	}
	if len(groups) == 0 {
		return false, nil
	}

	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	// Deeper registries settle first; scope breaks depth ties deterministically.
	slices.SortFunc(keys, func(a, b groupKey) int {
		if a.depth != b.depth {
			return cmp.Compare(b.depth, a.depth)
		}
		return cmp.Compare(a.scope, b.scope)
	})

	pausedAgain := false
	for _, k := range keys {
		registry := tools.NewDelegationRegistryWithDepth(k.depth)
		// Replay only THIS group's scope's terminal units — a foreign replay
		// would collide on the id (specs/domains/orchestration/delegation.md,
		// "One funnel on Resume").
		for _, s := range settled {
			if s.scope != k.scope {
				continue
			}
			mode := s.task.Mode
			if mode == "" {
				mode = "blocking"
			}
			if err := registry.Register(s.id, s.task.Summary, s.task.DependsOn, mode); err == nil {
				registry.Complete(s.id, s.output, s.execErr, s.steps)
			}
		}

		groupUnits := groups[k]
		tasks := make([]tools.DelegationTask, 0, len(groupUnits))
		for _, u := range groupUnits {
			t := u.task
			// The wave relaunches blocking (delegation.md, "One funnel on
			// Resume").
			t.Mode = "blocking"
			// Surface settled children in the parent's task text: a
			// re-delegating parent resumes only after its children settled.
			if childSummary := childDelegationOutcomes(bb, unitsToSettle, u.id); childSummary != "" {
				t.Task += childSummary
			}
			// Nudge lands before wave work (specs/domains/session-lifecycle.md,
			// "Session Pause / Resume / Nudge").
			if nudge != "" {
				t.Task += "\n\n## User follow-up provided on resume\n\n" + nudge
			}
			// Seed the paused checkpoint before launch (buildSubAgentTask seeds from the
			// blackboard's paused step result); not-started units relaunch fresh
			// (delegation.md, "One funnel on Resume").
			if u.paused() {
				if sr, ok := bb.GetStepResult(u.id); !ok || !isPaused(sr.Error) {
					bb.SetStepResult(u.id, "", agent.ErrPaused, u.steps)
				}
			}
			tasks = append(tasks, t)
			// Register the relaunched task so its lifecycle transitions from
			// Launch are not silent no-ops (classification is exclusive per id).
			if err := registry.RegisterTask(t); err != nil {
				// Dependency resolution would otherwise evaluate another
				// entry's summary/DependsOn for this id — surface the conflict.
				o.logWarn("resume_task: failed to register relaunched unit in its wave registry",
					"unit", t.ID, "scope", k.scope, "depth", k.depth, "error", err)
			}
		}
		results := launcher.Launch(ctx, tasks, registry)
		for _, r := range results {
			if r.Status == tools.DelegationStatusPaused {
				pausedAgain = true
			}
			writeWaveSummaryLine(sb, r.ID, string(r.Status), r.Output, r.Error)
		}
		if pausedAgain {
			break
		}
	}
	return pausedAgain, nil
}

// childDelegationOutcomes builds a bounded factual digest of the settled
// sub-delegations of the given parent unit, for the parent's resumed task text.
func childDelegationOutcomes(bb orchestration.Blackboard, unitsToSettle []resumeUnit, parentID string) string {
	var sb strings.Builder
	for _, u := range unitsToSettle {
		if u.parentID != parentID {
			continue
		}
		sr, ok := bb.GetStepResult(u.id)
		if !ok {
			continue
		}
		status := "completed"
		if sr.Error != nil {
			status = "failed"
		}
		var line strings.Builder
		fmt.Fprintf(&line, "- %s: %s", u.id, status)
		if detail := waveDetail(sr.FullOutput, sr.Error); detail != "" {
			line.WriteString(" — " + detail)
		}
		sb.WriteString(line.String() + "\n")
	}
	if sb.Len() == 0 {
		return ""
	}
	return "\n\n## Auto-resumed sub-delegations\n\nThese sub-delegations you launched were paused or interrupted and have been settled by the system; their full outputs are on the blackboard (read_step_output):\n\n" + sb.String()
}

// writeWaveSummaryLine appends one bounded outcome line to the wave summary.
func writeWaveSummaryLine(sb *strings.Builder, id, status, output string, execErr error) {
	var line strings.Builder
	fmt.Fprintf(&line, "- %s: %s", id, status)
	if detail := waveDetail(output, execErr); detail != "" {
		line.WriteString(" — " + detail)
	}
	sb.WriteString(line.String() + "\n")
}

// waveDetail flattens and caps an outcome detail: the error text when the
// outcome failed, otherwise the output excerpt.
func waveDetail(output string, execErr error) string {
	detail := output
	if execErr != nil {
		detail = execErr.Error()
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > waveSummaryOutputCap {
		// Back the cut up to a rune boundary — a sliced rune would surface as U+FFFD
		// replacement characters after JSON marshaling.
		end := waveSummaryOutputCap
		for end > 0 && !utf8.RuneStart(detail[end]) {
			end--
		}
		detail = detail[:end] + "…"
	}
	return detail
}

// delegationSpecsFromBlackboard reads the task's persisted delegation specs via the
// optional DelegationSpecReader capability (absent → no resumable delegates).
func delegationSpecsFromBlackboard(bb orchestration.Blackboard) []tools.DelegationSpec {
	reader, ok := bb.(DelegationSpecReader)
	if !ok {
		return nil
	}
	return reader.DelegationSpecs()
}

// planHasUnreachedSteps reports whether the blackboard carries a declared plan with
// a step lacking a successful StepResult — a plan worth continuing; it seeds the
// resumed run's planRunState (execute_plan without a re-declare). Completed/absent
// returns false — restarting a completed task must not re-enter the plan workflow.
func planHasUnreachedSteps(bb orchestration.Blackboard) bool {
	if bb == nil {
		return false
	}
	plan := bb.GetPlan()
	if plan == nil || len(plan.Steps) == 0 {
		return false
	}
	for _, step := range plan.Steps {
		sr, ok := bb.GetStepResult(step.ID)
		if !ok || sr.Error != nil {
			return true
		}
	}
	return false
}

// lastReasoningContent extracts the last non-empty reasoning content from the
// blackboard's step results (reasoning models require the field, e.g. DeepSeek).
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

// defaultVectorSearchWaitTimeout mirrors vectorindex.DefaultSearchWaitTimeout
// (search_wait_timeout_ms default); duplicated — core cannot import core/vectorindex.
const defaultVectorSearchWaitTimeout = 3 * time.Second

// effectiveVectorSearchWaitTimeout resolves the RAG-hint wait bound per the
// VectorSearchWait* deps field semantics documented above.
func (o *Orchestrator) effectiveVectorSearchWaitTimeout() time.Duration {
	if o.vectorSearchWaitDisabled {
		return 0
	}
	if o.vectorSearchWaitTimeout > 0 {
		return o.vectorSearchWaitTimeout
	}
	return defaultVectorSearchWaitTimeout
}

// injectVectorSearchHints attaches relevant files (vector search) and the combined
// AGENTS.md content (WithAgentsMD, prepended first). Best-effort: a search failure
// or nil function only drops the vector hints — AGENTS.md injection is unaffected.
func (o *Orchestrator) injectVectorSearchHints(ctx context.Context, query string) context.Context {
	var hints *VectorSearchHints

	// Vector search is skipped for No Project (CHAT): no collection exists, so queries
	// would yield stale results from a prior CODE project; AGENTS.md stays workspace-local.
	if !o.isNoProject && o.vectorSearchFunc != nil {
		// One deadline covers both stages (see OrchestratorDeps.VectorSearchWaitFunc).
		ragCtx, ragCancel := context.WithTimeout(ctx, o.effectiveVectorSearchWaitTimeout())
		var waitErr error
		if o.vectorSearchWaitFunc != nil {
			waitErr = o.vectorSearchWaitFunc(ragCtx)
		}
		var results []builtins.VectorSearchResult
		var err error
		if waitErr == nil {
			results, err = o.vectorSearchFunc(ragCtx, builtins.VectorSearchOptions{Query: query, TopK: 5})
		}
		ragCancel()

		switch {
		case waitErr != nil:
			o.logDebug("vector search hints skipped (index not ready)", "error", waitErr)
		case err != nil:
			o.logDebug("vector search hints skipped", "error", err)
		case len(results) > 0:
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

	// AGENTS.md sources and priority order: specs/domains/orchestration/README.md;
	// each source is mtime-cached per path, missing files silently skipped.
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

	// Build the research-awareness snapshot (RESEARCH mode only), mirroring the
	// best-effort AGENTS.md injection: failures log and never break the task.
	ctx = o.injectResearchContext(ctx)

	return ctx
}

// injectResearchContext builds the research-awareness snapshot for RESEARCH mode
// (no-op otherwise) for buildSystemPrompt and the router; methodology stays in the
// research-* skills — pure awareness, never policy. Best-effort: failures log.
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

// buildResearchContextSnapshot derives the ResearchContext snapshot from a parsed
// research root (pure, no I/O): the active R-NNN is the latest index entry (fallback:
// highest-numbered project dir); the phase hint comes from the graph metrics.
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

// researchPhaseHint maps a research project's hypothesis-graph metrics to a
// short phase label (pure; vocabulary mirrors the research-* skills).
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

// collectAgentsMDPaths returns the ordered AGENTS.md paths: the configured search
// paths (already absolute) then the workspace-root file (none without a workspace).
func (o *Orchestrator) collectAgentsMDPaths(ctx context.Context) []string {
	paths := make([]string, 0, len(o.config.AgentsMDSearchPaths)+1)
	paths = append(paths, o.config.AgentsMDSearchPaths...)
	if wsPath := sdktools.WorkspacePathFrom(ctx); wsPath != "" {
		paths = append(paths, filepath.Join(wsPath, "AGENTS.md"))
	}
	return paths
}

// readAgentsMD reads AGENTS.md through a per-path mtime-invalidated cache and a
// per-file byte cap equal to capAgentsMD's combined cap — a huge workspace-controlled
// file is never fully materialized. The capped result (same note, snapping, true
// size) is identical to reading uncapped; only the allocation stays bounded.
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

	// Open through safeio (refuses FIFOs/devices); read through io.LimitReader with
	// cap+1 bytes — the extra byte DETECTS oversize instead of silently clipping
	// (losing capAgentsMD's note and snapping). A negative cap disables capping.
	f, readErr := safeio.Open(path)
	if readErr != nil {
		o.agentsMDCache[path] = agentsMDCacheEntry{err: readErr, modTime: info.ModTime()}
		return "", readErr
	}
	var content []byte
	truncated := false
	if maxBytes := o.agentsMDMaxBytes(); maxBytes > 0 {
		content, readErr = io.ReadAll(io.LimitReader(f, maxBytes+1))
		truncated = int64(len(content)) > maxBytes
	} else {
		content, readErr = io.ReadAll(f)
	}
	if closeErr := f.Close(); readErr == nil {
		readErr = closeErr
	}
	if readErr != nil {
		o.agentsMDCache[path] = agentsMDCacheEntry{err: readErr, modTime: info.ModTime()}
		return "", readErr
	}
	if truncated {
		// Byte-precise snap with the note's size RESERVED from the cap (rationale on
		// capAgentsMD): a full-cap clip would re-truncate — second marker, false size.
		maxBytes := o.agentsMDMaxBytes()
		note := agentsMDTruncationNote(maxBytes, info.Size())
		clipped := snapAgentsMDWindow(content, maxBytes-int64(len(note)))
		o.logDebug("AGENTS.md truncated", "path", path, "originalSize", info.Size(), "cap", maxBytes)
		content = []byte(string(clipped) + note)
	}

	contentStr := string(content)
	o.agentsMDCache[path] = agentsMDCacheEntry{content: contentStr, modTime: info.ModTime()}
	return contentStr, nil
}

// agentsMDMaxBytes resolves the effective AGENTS.md byte cap shared by readAgentsMD
// (per-file bound) and capAgentsMD (combined truncation): 0 = the default
// (DefaultAgentsMDMaxBytes), negative disables capping.
func (o *Orchestrator) agentsMDMaxBytes() int64 {
	maxBytes := o.config.AgentsMDMaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultAgentsMDMaxBytes
	}
	return int64(maxBytes)
}

// agentsMDTruncationNote renders the marker appended to clipped AGENTS.md content
// (shared by readAgentsMD and capAgentsMD so both emit byte-identical markers).
func agentsMDTruncationNote(maxBytes, originalSize int64) string {
	return fmt.Sprintf("\n\n[…AGENTS.md truncated at %d bytes; original was %d bytes]", maxBytes, originalSize)
}

// snapAgentsMDWindow returns the longest byte-precise prefix of content that fits
// budget bytes AND ends at a line boundary: the last '\n' in the window, or — with
// no newline — a UTF-8 rune boundary. Byte-precise on purpose: a rune-counting trim
// (strutil.TruncateUTF8AtLineBoundary) would overflow the byte budget by up to 4x.
func snapAgentsMDWindow(content []byte, budget int64) []byte {
	if budget <= 0 {
		return nil
	}
	if int64(len(content)) <= budget {
		return content
	}
	window := content[:budget]
	if idx := bytes.LastIndexByte(window, '\n'); idx >= 0 {
		return window[:idx+1]
	}
	for len(window) > 0 && window[len(window)-1]&0xC0 == 0x80 {
		window = window[:len(window)-1]
	}
	if len(window) > 0 && window[len(window)-1]&0xC0 == 0xC0 {
		window = window[:len(window)-1]
	}
	return window
}

// capAgentsMD applies the AgentsMDMaxBytes cap to the combined AGENTS.md content
// (0 = default, negative = no cap). Truncation snaps to the last newline (fallback:
// UTF-8 rune boundary); the note's size is RESERVED from the cap, else a second-stage
// clip would duplicate markers and misreport size; a pathological cap returns the note.
func (o *Orchestrator) capAgentsMD(content string) string {
	originalSize := int64(len(content))
	maxBytes := o.agentsMDMaxBytes()
	if maxBytes <= 0 || originalSize <= maxBytes {
		return content
	}
	note := agentsMDTruncationNote(maxBytes, originalSize)
	clipped := snapAgentsMDWindow([]byte(content), maxBytes-int64(len(note)))
	o.logDebug("AGENTS.md truncated", "originalSize", originalSize, "cap", maxBytes)
	return string(clipped) + note
}

// resetDynamicStepScope clears the root emitter's dynamic plan-step scope (if it
// supports one). Task-boundary insurance: a pause→abandon, panic or crash mid-run
// leaves the scope at a previous task's step, which later empty-stepID ContextFills
// would inherit; the Conductor re-scopes inline steps on start, so resumes are safe.
func (o *Orchestrator) resetDynamicStepScope() {
	if o.emitter == nil {
		return
	}
	if scoper, ok := o.emitter.(CurrentStepScopable); ok {
		scoper.SetCurrentStepID("")
	}
}

// emitInitialContextFill emits a 0% context_fill baseline and injects the model's
// advertised context window so the user-facing fill is computed against the real
// window. Resolution is deliberately ResolveLocal (network-free): a full Resolve on
// this UI-critical path could stall on a HuggingFace lookup; a probe refreshes later.
func (o *Orchestrator) emitInitialContextFill() {
	o.resetDynamicStepScope()
	var contextWindow int
	if o.modelRegistry != nil {
		model := o.currentModel()
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

// SetNoProjectMode configures No Project (CHAT) mode: disables index-dependent
// tools. Tool scope, honest routing, shell policy: specs/domains/orchestration/router.md.
func (o *Orchestrator) SetNoProjectMode() {
	o.isNoProject = true
	if o.coreToolRegistry == nil {
		if o.logger != nil {
			o.logger.Warn("SetNoProjectMode: coreToolRegistry is nil, skipping tool configuration")
		}
		return
	}
	o.coreToolRegistry.SetDisabledTools(NoProjectDisabledTools)
}

// Emitter returns the orchestrator's event emitter for external plan-step emissions
// (e.g. after feedback-driven replanning); a noop emitter when none is configured,
// so callers cannot nil-panic.
func (o *Orchestrator) Emitter() Emitter {
	if o.emitter == nil {
		return &noopEmitter{}
	}
	return o.emitter
}

// ToolRegistry returns the per-session registry clone whose tool judge rides
// the session's own router (builder.bindSessionJudge), so manual "Judge"
// evaluations follow the same provider/model as automatic escalations.
// nil when not wired; nil-check.
func (o *Orchestrator) ToolRegistry() *tools.ToolRegistry {
	return o.coreToolRegistry
}

// LookupSkillDescriptors converts skill names to SkillDescriptors using the
// orchestrator's skill manager; unknown names are silently skipped. Returns nil
// when the skill manager is not available.
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

// currentModel returns the session's active model identity, synchronized for
// the cross-goroutine readers described on modelMu.
func (o *Orchestrator) currentModel() string {
	o.modelMu.RLock()
	defer o.modelMu.RUnlock()
	return o.config.Model
}

// CurrentModel returns the session's active model identity (the bare model
// name), synchronized for cross-goroutine readers.
func (o *Orchestrator) CurrentModel() string {
	return o.currentModel()
}

// setCurrentModel records the session's active model identity (see modelMu).
func (o *Orchestrator) setCurrentModel(model string) {
	o.modelMu.Lock()
	defer o.modelMu.Unlock()
	o.config.Model = model
}

// currentReasoningEffort returns the per-request reasoning effort,
// synchronized for the cross-goroutine readers described on modelMu.
func (o *Orchestrator) currentReasoningEffort() string {
	o.modelMu.RLock()
	defer o.modelMu.RUnlock()
	return o.config.ReasoningEffort
}

// setCurrentReasoningEffort records the per-request reasoning effort (see
// modelMu).
func (o *Orchestrator) setCurrentReasoningEffort(effort string) {
	o.modelMu.Lock()
	defer o.modelMu.Unlock()
	o.config.ReasoningEffort = effort
}

// SetReasoningEffort propagates the per-request reasoning effort to all components
// that make LLM calls: router, reflector, and the direct LLM caller.
func (o *Orchestrator) SetReasoningEffort(effort string) {
	o.setCurrentReasoningEffort(effort)
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

// SetE2SSettings stores refreshed E2S settings (e2sSettingsOverride); safe while a
// task runs.
func (o *Orchestrator) SetE2SSettings(settings E2SSettings) {
	o.e2sSettingsOverride.Store(&settings)
}

// e2sSettings returns the effective E2S settings: the runtime override when a
// toggle has refreshed them, otherwise the build-time config snapshot.
func (o *Orchestrator) e2sSettings() E2SSettings {
	if p := o.e2sSettingsOverride.Load(); p != nil {
		return *p
	}
	return o.config.E2S
}

// SetModelProfilesSettings replaces the effective model-profile settings at
// runtime (see modelProfilesSettingsOverride); mirrors SetE2SSettings.
func (o *Orchestrator) SetModelProfilesSettings(settings ModelProfilesSettings) {
	o.modelProfilesSettingsOverride.Store(&settings)
}

// modelProfilesSettings returns the effective model-profile settings: the runtime
// override when refreshed, otherwise the build-time config snapshot.
func (o *Orchestrator) modelProfilesSettings() ModelProfilesSettings {
	if p := o.modelProfilesSettingsOverride.Load(); p != nil {
		return *p
	}
	return o.config.ModelProfiles
}

// ApplyRequestOverrides applies per-request model and reasoning-effort overrides
// to every LLM-calling component (router, reflector, the direct caller,
// config.Model). Shared step 0 of HandleMessage AND the resume path — a resume
// that skips it silently drops the switch.
func (o *Orchestrator) ApplyRequestOverrides(ctx context.Context, modelOverride, reasoningEffort string) {
	o.SetReasoningEffort(reasoningEffort)
	if modelOverride != "" && o.modelSwitcher != nil {
		if err := o.modelSwitcher.SetModel(ctx, modelOverride); err != nil {
			if o.logger != nil {
				o.logger.Warn("failed to apply model override", "model", modelOverride, "error", err)
			}
		} else {
			bare := llm.BareModel(modelOverride)
			o.setCurrentModel(bare)
			// Fire-and-forget probe of the new model's real context window;
			// the result lands in the model registry for context-budget math.
			if o.localModelProbe != nil {
				o.localModelProbe(bare)
			}
			// Sync the emitter's cached model so the initial context_fill
			// (emitted before the first usage report) shows the new model.
			family := ""
			if o.modelRegistry != nil {
				if meta, _ := o.modelRegistry.Resolve(ctx, bare); meta.Family != "" {
					family = meta.Family
				}
			}
			if setter, ok := o.emitter.(LastModelSetter); ok {
				setter.SetLastModel(bare, family)
			}
		}
	}
}

// SetTaskStore sets the TaskPersistence store for blackboard restoration.
func (o *Orchestrator) SetTaskStore(store TaskPersistence) {
	o.taskStore = store
}

// SetConversationHistory sets the full conversation history; session restore
// pre-populates it from persistence so planning sees prior messages.
func (o *Orchestrator) SetConversationHistory(history []llm.Message) {
	o.setConversationHistory(history)
}

// ConversationHistory returns a snapshot copy (historySnapshot); exported for
// tests and the session layer's compaction-marker snapshot.
func (o *Orchestrator) ConversationHistory() []llm.Message {
	return o.historySnapshot()
}

// historySnapshot returns a deep copy of the conversation history taken under
// historyMu, so cross-goroutine readers never race a concurrent append or swap —
// not even on the backing array (appendHistory's retry-collapse rewrites slots a
// stale header snapshot would still cover).
func (o *Orchestrator) historySnapshot() []llm.Message {
	o.historyMu.RLock()
	defer o.historyMu.RUnlock()
	if o.conversationHistory == nil {
		return nil
	}
	snapshot := make([]llm.Message, len(o.conversationHistory))
	copy(snapshot, o.conversationHistory)
	return snapshot
}

// setConversationHistory replaces the conversation history under historyMu.
func (o *Orchestrator) setConversationHistory(history []llm.Message) {
	o.historyMu.Lock()
	defer o.historyMu.Unlock()
	o.conversationHistory = history
}

// appendHistory appends msgs to the conversation history in one atomic step under
// historyMu. With retryMessage set, a trailing failed exchange of that exact message
// is collapsed first — the continuation-retry dedup (see recordConversationOutcome);
// collapse and append share one critical section (no half-collapsed reader state).
func (o *Orchestrator) appendHistory(retryMessage string, msgs ...llm.Message) {
	o.historyMu.Lock()
	defer o.historyMu.Unlock()
	if retryMessage != "" {
		if n := len(o.conversationHistory); n >= 2 {
			prev, last := o.conversationHistory[n-2], o.conversationHistory[n-1]
			if prev.Role == "user" && prev.Content == retryMessage &&
				last.Role == "assistant" && strings.HasPrefix(last.Content, historyNoteFailedPrefix) {
				o.conversationHistory = o.conversationHistory[:n-2]
			}
		}
	}
	o.conversationHistory = append(o.conversationHistory, msgs...)
}

// dropFailedExchangeTail removes a trailing failed exchange for the given request —
// the injection-time mirror of appendHistory's retry collapse: a resume repeats the
// original request, and without this the model sees it twice with a failure marker
// between. Cancelled notes stay; unmatched tails pass through.
func dropFailedExchangeTail(history []llm.Message, request string) []llm.Message {
	if request == "" {
		return history
	}
	n := len(history)
	if n < 2 {
		return history
	}
	prev, last := history[n-2], history[n-1]
	if prev.Role == "user" && prev.Content == request &&
		last.Role == "assistant" && strings.HasPrefix(last.Content, historyNoteFailedPrefix) {
		return history[:n-2]
	}
	return history
}

// HistoryNoteCancelled is the assistant-side history note for a task cancelled
// before completion (shared with the backend's history reconstruction).
const HistoryNoteCancelled = "[Task was cancelled before completion]"

// historyNoteFailedPrefix is the prefix of failure notes produced by
// HistoryNoteFailed; used to recognize a previously recorded failed attempt.
const historyNoteFailedPrefix = "[Task failed before completion: "

// HistoryNoteFailed formats the assistant-side history note for a failed task,
// shared with the backend's history reconstruction (see HistoryNoteCancelled).
func HistoryNoteFailed(errText string) string {
	return historyNoteFailedPrefix + errText + "]"
}

// buildContentBlocks assembles the task message's content blocks: text first, then
// images; nil without images (legacy text-only SetTask path).
func buildContentBlocks(text string, images []llm.ContentBlock) []llm.ContentBlock {
	if len(images) == 0 {
		return nil
	}
	blocks := make([]llm.ContentBlock, 0, 1+len(images))
	blocks = append(blocks, llm.ContentBlock{Type: "text", Text: text})
	blocks = append(blocks, images...)
	return blocks
}

// imageBlocksForRequest returns the image content blocks of the history user message
// matching request, so a resume re-injects the original task's images from history
// without separate persistence. nil when no match or no image blocks.
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

// recordConversationOutcome appends the current exchange to the in-memory history
// via defer for every terminal HandleMessage outcome, so routing and continuation
// planning see the full dialogue (contentBlocks carries multimodal task content).
func (o *Orchestrator) recordConversationOutcome(ctx context.Context, message string, contentBlocks []llm.ContentBlock, result *HandleResult, err error) {
	userMsg := llm.Message{Role: "user", Content: message, ContentBlocks: contentBlocks}

	// Continuation fallback: the session manager retries a failed continuation as a
	// fresh workflow with the same message; a prior failed exchange of this exact
	// message is replaced, not duplicated (persisted-store collapse parity).

	switch {
	case err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete):
		if result == nil {
			o.appendHistory(message, userMsg)
			return
		}
		var reasoning string
		if result.Blackboard != nil {
			reasoning = lastReasoningContent(result.Blackboard)
		}
		// Failure-mode guard: tool-call syntax printed as text is a stuck-model artifact,
		// not an answer — record a failure note so routing sees an honest failure.
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.appendHistory(message, userMsg,
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: reasoning})
	case ctx.Err() != nil:
		// Cancellation (or deadline): the manager persists a task_cancelled
		// event; mirror it in the in-memory history.
		o.appendHistory(message, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	default:
		// Hard failure: the manager persists an error event; mirror it so
		// the rejected request stays visible to future routing/planning.
		o.appendHistory(message, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// recordResumeOutcome appends a resumed execution's outcome to the in-memory
// history. Only the assistant side is appended (the user message was recorded on
// first run or restored); on failure appendResumeFailure re-anchors the exchange.
func (o *Orchestrator) recordResumeOutcome(ctx context.Context, bb orchestration.Blackboard, result *HandleResult, err error) {
	switch {
	case (err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete)) && result != nil:
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.appendHistory("",
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: lastReasoningContent(bb)})
	case ctx.Err() != nil:
		o.appendHistory("",
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	case err != nil:
		o.appendResumeFailure(bb.GetOriginalRequest(),
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// appendResumeFailure records a failed resume as exactly one well-formed
// [user:originalRequest, assistant:failed] exchange, re-anchored at the original
// request in one locked step: a plain appendHistory would stack assistant-only notes
// across repeated failed resumes and break dropFailedExchangeTail's exact match.
// Replaceable tail: failure notes and empty pause records; real output and
// HistoryNoteCancelled stay; a lone restored user message is kept, never duplicated.
func (o *Orchestrator) appendResumeFailure(origReq string, failed llm.Message) {
	o.historyMu.Lock()
	defer o.historyMu.Unlock()
	i := len(o.conversationHistory)
	for i > 0 {
		msg := o.conversationHistory[i-1]
		if msg.Role != "assistant" {
			break
		}
		if strings.HasPrefix(msg.Content, historyNoteFailedPrefix) || msg.Content == "" {
			i--
			continue
		}
		break
	}
	anchored := i > 0 && o.conversationHistory[i-1].Role == "user" && o.conversationHistory[i-1].Content == origReq
	if anchored {
		o.conversationHistory = append(o.conversationHistory[:i], failed)
		return
	}
	o.conversationHistory = append(o.conversationHistory,
		llm.Message{Role: "user", Content: origReq}, failed)
}

// SetBlackboardRestoreFunc sets the function used to restore a PersistableBlackboard from persistence.
func (o *Orchestrator) SetBlackboardRestoreFunc(fn BlackboardRestoreFunc) {
	o.bbRestoreFunc = fn
}

// truncateHistory returns the last window messages of history; window <= 0 or
// a shorter history is returned as-is.
func truncateHistory(history []llm.Message, window int) []llm.Message {
	if window <= 0 || len(history) <= window {
		return history
	}
	truncated := make([]llm.Message, window)
	copy(truncated, history[len(history)-window:])
	return truncated
}

// HandleMessage is the unified entry point for user messages: a first message
// (TaskID == "") or a continuation, dispatched to the plain, goal, or E2S mode. See
// specs/domains/orchestration/README.md and specs/domains/goal-mode.md.
func (o *Orchestrator) HandleMessage(ctx context.Context, message, sessionID string, opts HandleOptions) (result *HandleResult, err error) {
	// W-8: Enforce single-flight — only one HandleMessage per *Orchestrator.
	if !o.requestInFlight.CompareAndSwap(false, true) {
		return nil, ErrRequestInFlight
	}
	defer o.requestInFlight.Store(false)

	// Install the universal pause signal for this request; cleared on exit so
	// a stale signal can never pause a future request (see installPauseSignal).
	defer o.installPauseSignal()()

	taskMessage := o.resolveTaskMessage(message, opts.UserSkills)

	// contentBlocks carries the structured task content (text + images); declared
	// before the defer so recordConversationOutcome captures it by reference.
	var contentBlocks []llm.ContentBlock

	// Registered after the single-flight guard: a rejected request is not recorded.
	defer func() {
		o.recordConversationOutcome(ctx, taskMessage, contentBlocks, result, err)
	}()

	o.ApplyRequestOverrides(ctx, opts.ModelOverride, opts.ReasoningEffort)

	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	ctx = o.prepareRequestContext(ctx, taskMessage)

	// Signal review-feedback mode so the system prompt instructs the agent to
	// treat the user's message as actionable review comments (see review domain).
	if opts.ReviewMode {
		ctx = WithReviewMode(ctx)
	}

	bb, err := o.setupBlackboard(taskMessage, sessionID, opts.TaskID, opts.PendingAttachments)
	if err != nil {
		return nil, err
	}
	ctx, err = o.prepareTaskMCP(ctx, bb, opts.UserMCPServers)
	if err != nil {
		return nil, err
	}
	opts.UserMCPServers = UserMCPServersFromContext(ctx)

	// Capability snapshots resolve chat_mode/code_mode ONCE per task entry; mentions
	// of capabilities disabled here are dropped. See specs/domains/capabilities.md.
	ctx = o.attachCapabilitySnapshots(ctx)
	opts.UserSkills = mentionedSkillsForTask(ctx, opts.UserSkills)
	opts.UserAgents = mentionedAgentsForTask(ctx, opts.UserAgents)

	// Only the Conductor sees attachments; images travel as ContentBlocks below.
	conductorMessage := o.augmentWithAttachments(taskMessage, bb)

	// Structured content blocks: attachment-augmented text task first, then the staged
	// images; providers give blocks precedence. Images never enter the blackboard.
	contentBlocks = buildContentBlocks(conductorMessage, opts.PendingImages)

	// Get available tools (exclude disabled tools in No Project mode).
	availableTools := o.toolRegistry.ListFiltered(o.disabledToolNames())
	mcpCount := 0
	for _, t := range availableTools {
		if t.SourceCategory == sdktools.SourceCategoryMCP {
			mcpCount++
		}
	}
	o.logDebug("orchestrator: tools loaded from registry", "total", len(availableTools), "mcp", mcpCount)

	availableTools = stripGatedMCPTools(ctx, availableTools)

	// E2S MODE: an explicit-execution-state request enters runE2SLoop instead of the
	// route→Conductor flow; E2S and Goal are mutually exclusive. This branch must
	// precede the goal branch (else it swallows the E2S request). See specs/domains/e2s.md.
	if opts.E2S {
		if opts.Goal {
			return nil, ErrE2SGoalConflict
		}
		if !o.e2sSettings().Enabled {
			return nil, ErrE2SModeDisabled
		}
		// Commit point (mirrors the goal branch): reactivate only now, after
		// blackboard restore succeeded.
		o.reactivateContinuationTask(bb, opts.TaskID)
		// taskMessage, not the raw message: /skill-only sends strip to empty
		// and the E2S objective seeds Σ from it.
		return o.runE2SLoop(ctx, taskMessage, opts, bb, availableTools)
	}

	// GOAL MODE: a goal request enters the multi-turn goal loop instead of the
	// single-pass route→Conductor flow, on BOTH a fresh task and a continuation (a
	// fresh goal derives on the restored blackboard). See specs/domains/goal-mode.md.
	if opts.Goal {
		// Goal mode is hard-blocked under Model Profiles essential-tools narrowing (it
		// would strip the goal-loop tooling) — refused BEFORE the reactivation side
		// effect. See specs/domains/model-profiles.md (goal-mode gate).
		if o.modelProfilesEssentialToolsEnabled() {
			return nil, ErrGoalBlockedByModelProfiles
		}
		// Commit point: reactivate only now — an earlier failure leaves the anchor's
		// terminal status intact for the manager's fresh-workflow fallback.
		o.reactivateContinuationTask(bb, opts.TaskID)
		// taskMessage, not the raw message: /goal /skill-only sends strip to
		// empty and the goal derivation seeds from it.
		return o.runGoalLoop(ctx, taskMessage, opts, bb, availableTools, opts.SessionPlansDir)
	}

	// Goal-mode-only tools must not reach a non-goal Conductor run: strip them from
	// the router/Conductor view; the unstripped list serves the verifier toolsets and
	// an accepted goal suggestion (runGoalLoopRouted reuses this routing).
	goalAvailableTools := availableTools
	availableTools = tools.StripGoalModeTools(availableTools)

	// 3. Route and activate skills. routeOrContinue skips the router on a
	// restored task (blind to the existing plan — it would misclassify the
	// continuation); the raw message goes in (see resolveTaskMessage).
	ctx, routing, _, _, err := o.routeOrContinue(ctx, message, opts, bb, availableTools)
	if err != nil {
		// Routing failed before the commit point below: the anchor task keeps
		// its terminal status, so the fresh-workflow fallback cannot orphan it.
		return nil, err
	}

	// GOAL-MODE SUGGESTION GATE: offer goal activation on a fresh plain task whose
	// routing just ran, BEFORE any execution; an accepted offer re-enters the goal
	// loop with THIS routing. See specs/decisions/086-goal-mode-suggestion.md.
	if o.goalSuggestionApplicable(opts) && o.goalSuggestRoutingEligible(routing) {
		switch o.runGoalSuggestionGate(ctx, sessionID, message, routing) {
		case gateAccepted:
			opts.Goal = true
			result, deriveFailed, err := o.runGoalLoopRouted(ctx, taskMessage, opts, bb, goalAvailableTools, opts.SessionPlansDir, routing, true)
			if !deriveFailed {
				return result, err
			}
			// Derivation failed after acceptance — fall through to the plain
			// Conductor path so the accepted task still gets done.
			o.logInfo("goal suggest: derivation failed after acceptance — continuing as a plain task")
		case gateDeclined:
			// Plain path continues; suppression already recorded.
		case gateNoSuggestion:
			// Plain path continues.
		}
	}

	// Commit point: routing succeeded, execution starts — flip the anchor task
	// back to in_progress (no-op for fresh tasks, already in_progress).
	o.reactivateContinuationTask(bb, opts.TaskID)

	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// Persist the routing decision so the Conductor and finalization retrieve it.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	// Model Profiles essential-tools narrowing: once per task, before the ReAct loop
	// (goal returned earlier; E2S filters in its own branch). The delegate tool
	// survives narrowing on explicit subagent requests. See specs/domains/model-profiles.md.
	availableTools = o.applyModelProfilesToolFilter(availableTools, modelProfilesAgentGuaranteedTools(ctx)...)

	// Window the history to ConductorHistoryWindow (recent messages carry the
	// follow-up context); dropFailedExchangeTail drops a trailing failed exchange first.
	conductorHistory := truncateHistory(dropFailedExchangeTail(o.historySnapshot(), message), o.config.ConductorHistoryWindow)

	plansDir := opts.SessionPlansDir
	execResult, err := o.runConductor(ctx, conductorMessage, bb, availableTools, plansDir, conductorHistory, nil, contentBlocks, "", "", false)
	// Cooperative pause is a checkpoint, not a failure: return the paused result with
	// a nil error so the backend surfaces a Resume action.
	if errors.Is(err, agent.ErrPaused) {
		o.emitSessionPaused()
		err = nil
	}
	// C-5: propagate ErrExecutionIncomplete alongside best-effort result.
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		// Mark the task failed so it cannot become a silent-resume candidate;
		// mirrors the Resume and routing-error paths.
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	incompleteErr := err

	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	// Conversation history is updated by the recordConversationOutcome defer.
	result = o.finalizeResult(bb, routing, execResult)

	if incompleteErr != nil {
		return result, incompleteErr
	}
	return result, nil
}

// disabledToolNames returns the tool names disabled for the current mode (CHAT/
// No-Project disables filesystem tools) or nil; Conductor view + subagent toolset.
func (o *Orchestrator) disabledToolNames() map[string]bool {
	if o.coreToolRegistry == nil {
		return nil
	}
	return o.coreToolRegistry.DisabledTools()
}

// delegateToolName is the conductor-only delegation channel.
const delegateToolName = "delegate"

// modelProfilesAgentGuaranteedTools returns the turn-scoped guaranteed tool names
// for THIS request: delegate when the user explicitly requested subagents (the
// narrowing must keep them callable); nil otherwise. See specs/domains/model-profiles.md.
func modelProfilesAgentGuaranteedTools(ctx context.Context) []string {
	if len(UserAgentsFromContext(ctx)) > 0 {
		return []string{delegateToolName}
	}
	return nil
}

// applyModelProfilesToolFilter narrows the conductor's available-tool set while a
// profile with essential-tools narrowing is active; OFF (default) is a no-op
// passthrough. Runs once per task on the Conductor path and once in the E2S branch;
// goal mode is never narrowed (HandleMessage returns first). Rules: specs/domains/model-profiles.md.
func (o *Orchestrator) applyModelProfilesToolFilter(in []sdktools.ToolDescriptor, extraGuaranteed ...string) []sdktools.ToolDescriptor {
	sc := o.modelProfilesSettings()
	if !o.modelProfilesEssentialToolsEnabled() {
		return in
	}

	filtered := modelprofiles.SelectTools(in, sc.EssentialTools.AlwaysPresent, extraGuaranteed...)
	return modelprofiles.MaybeCompactDescriptions(filtered, sc.EssentialTools.CompactDescriptions)
}

// modelProfilesEssentialToolsEnabled reports whether essential-tools narrowing is
// active (master toggle AND variant on), reading effective settings so a runtime
// toggle needs no restart.
func (o *Orchestrator) modelProfilesEssentialToolsEnabled() bool {
	sc := o.modelProfilesSettings()
	return sc.Enabled && sc.EssentialTools.Enabled
}

// ModelProfilesNarrowingEnabled exposes modelProfilesEssentialToolsEnabled so the
// session manager's resume pre-check reads the same invariant (Manager.ResumeTask).
func (o *Orchestrator) ModelProfilesNarrowingEnabled() bool {
	return o.modelProfilesEssentialToolsEnabled()
}
