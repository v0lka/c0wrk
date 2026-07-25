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
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agent/router"
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
	AgentsMDMaxBytes int

	// ConductorHistoryWindow is the maximum number of recent conversation
	// messages injected into the Conductor's context. 0 = use default (20).
	// Keeps the prompt bounded for long sessions while preserving enough
	// dialogue context for the agent to understand follow-up references.
	ConductorHistoryWindow int
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
	bbFactory           BlackboardFactory
	conversationHistory []llm.Message
	taskStore           TaskPersistence       // optional, for ContinueTask blackboard restoration
	bbRestoreFunc       BlackboardRestoreFunc // optional, restores PersistableBlackboard from store
	trackingCaller      *llm.TrackingCaller   // for per-step context tracker wiring
	tokenCounter        llm.TokenCounter      // for token counting in planner history compaction
	vectorSearchFunc    builtins.VectorSearchFunc
	skillManager        *skills.SkillManager // for skill discovery and activation

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

	// activeGoalPause is the pause signal for the currently-running goal
	// loop, if any. PauseGoal loads the pointer and sets the atomic; runGoalLoop
	// polls it at the top of each turn iteration. It is swapped in at goal-loop
	// entry and cleared (nil) on exit so a stale signal from a prior goal cannot
	// pause a future non-goal request.
	//
	// The pointer field itself is an atomic.Pointer so the cross-goroutine read
	// in PauseGoal (a Wails-RPC goroutine) and the write in runGoalLoop/
	// resumeGoalLoop (the HandleMessage/Resume goroutine) race-free. The
	// single-flight requestInFlight flag serializes HandleMessage calls against
	// each other, but does NOT cover PauseGoal, which runs independently — hence
	// the atomic pointer. The *atomic.Bool it points to is, of course, atomic.
	activeGoalPause atomic.Pointer[atomic.Bool]
}

// ErrRequestInFlight is returned by HandleMessage when another HandleMessage
// call on the same *Orchestrator is already running. The orchestrator is
// designed for one concurrent request per instance — the session layer is
// responsible for serializing per-session.
var ErrRequestInFlight = errors.New("orchestrator: request already in flight")

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
		bbFactory:        deps.BBFactory,
		trackingCaller:   deps.TrackingCaller,
		tokenCounter:     deps.TokenCounter,
		vectorSearchFunc: deps.VectorSearchFunc,
		skillManager:     deps.SkillManager,
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
func (o *Orchestrator) Resume(ctx context.Context, bb orchestration.Blackboard, routing *router.RoutingDecision, plansDir string, resumeSteps []agent.Step, goalState *goal.GoalState) (result *HandleResult, err error) {
	o.logDebug("orchestrator: resume started", "resumeSteps", len(resumeSteps))

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
	o.emitInitialContextFill(ctx)

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
		return o.resumeGoalLoop(ctx, bb.GetOriginalRequest(), bb, availableTools, plansDir, routing, goalState, resumeSteps)
	}

	execResult, err := o.runConductor(ctx, bb.GetOriginalRequest(), bb, availableTools, plansDir, nil, resumeSteps)
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

// buildSkillPolicyOverrides creates tool policy overrides from active skills.
// Tools listed in a skill's allowed-tools field get PolicyAlwaysAllow,
// enabling the agent to use them without manual confirmation.
func (o *Orchestrator) buildSkillPolicyOverrides(activeSkills []*skills.Skill) map[string]sdktools.ToolPolicy {
	overrides := make(map[string]sdktools.ToolPolicy)
	for _, s := range activeSkills {
		for _, toolName := range s.Metadata.AllowedToolList() {
			// Only set if not already overridden by config
			overrides[toolName] = sdktools.PolicyAlwaysAllow
		}
	}
	return overrides
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

	// Always try to read AGENTS.md from workspace root (cached per workspace with mtime check).
	if wsPath := sdktools.WorkspacePathFrom(ctx); wsPath != "" {
		agentsMDPath := filepath.Join(wsPath, "AGENTS.md")
		contentStr, err := o.readAgentsMD(agentsMDPath)
		if err != nil {
			o.logDebug("AGENTS.md not found in workspace", "path", agentsMDPath)
		} else {
			ctx = WithAgentsMD(ctx, &AgentsMD{Content: contentStr})
			o.logDebug("AGENTS.md found and injected", "path", agentsMDPath, "size", len(contentStr))

			// Prepend AGENTS.md as the first hint so it always appears in
			// the "Relevant Project Files" section for executors.
			if hints == nil {
				hints = &VectorSearchHints{}
			}
			summary := strutil.TruncateUTF8(contentStr, 100)
			agentsHint := VectorSearchHint{
				FilePath: "AGENTS.md",
				Summary:  summary,
			}
			hints.Files = append([]VectorSearchHint{agentsHint}, hints.Files...)
		}
	}

	if hints != nil && len(hints.Files) > 0 {
		ctx = WithVectorSearchHints(ctx, hints)
	}

	return ctx
}

// readAgentsMD reads AGENTS.md from disk with a per-workspace-path cache that
// invalidates on mtime change. This avoids repeated disk reads on every
// HandleMessage call while still picking up edits. Write-through: every cache
// miss re-reads the file and updates modTime.
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

	// Apply truncation cap (same logic as before).
	originalSize := len(content)
	contentStr := string(content)
	maxBytes := o.config.AgentsMDMaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultAgentsMDMaxBytes
	}
	if maxBytes > 0 && originalSize > maxBytes {
		trimmed := contentStr[:maxBytes]
		if idx := strings.LastIndex(trimmed, "\n"); idx > 0 {
			trimmed = trimmed[:idx]
		}
		contentStr = trimmed + fmt.Sprintf("\n\n[…AGENTS.md truncated at %d bytes; original was %d bytes]", maxBytes, originalSize)
		o.logDebug("AGENTS.md truncated", "path", path, "originalSize", originalSize, "cap", maxBytes)
	}

	o.agentsMDCache[path] = agentsMDCacheEntry{content: contentStr, modTime: info.ModTime()}
	return contentStr, nil
}

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
// It also injects the model's advertised context window into the emitter so that
// the user-facing fill display (status bar, compaction messages) is computed
// relative to the real window, not the internal "effective max" the executor
// uses for compaction.
func (o *Orchestrator) emitInitialContextFill(ctx context.Context) {
	var contextWindow int
	if o.modelRegistry != nil {
		model := o.config.Model
		meta, _ := o.modelRegistry.Resolve(ctx, model)
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
// disables code-oriented tools, adds extended bash command blacklist,
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
	if err := o.coreToolRegistry.SetExtraBashBlacklist(NoProjectBashBlacklist); err != nil {
		if o.logger != nil {
			o.logger.Warn("failed to set extra bash blacklist for No Project", "error", err)
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

// recordConversationOutcome appends the current exchange to the in-memory
// conversation history. It is invoked (via defer) for EVERY terminal outcome
// of HandleMessage — success, partial success, failure, cancellation —
// failure, and cancellation — so the router and continuation planner always
// see the full dialogue, not just successful exchanges.
func (o *Orchestrator) recordConversationOutcome(ctx context.Context, message string, result *HandleResult, err error) {
	userMsg := llm.Message{Role: "user", Content: message}

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

	// Resolve the effective task message. When the user invokes a skill via
	// /skill-name, preprocessMessageText strips the reference — potentially
	// leaving an empty message. resolveTaskMessage rebuilds it so the
	// Conductor receives a meaningful task and the conversation history never
	// records an empty user message (which some providers reject with HTTP
	// 400 "messages parameter is illegal"). The raw `message` is still passed
	// to the router below, which applies its own augmentation — using the
	// augmented message there would double-prefix the skill reference.
	taskMessage := o.resolveTaskMessage(message, opts.UserSkills)

	// Record the exchange in the in-memory conversation history for EVERY
	// terminal outcome (success, failure, cancellation) of HandleMessage
	// cancellation) so future routing and continuation planning always see
	// the full dialogue. Registered after the single-flight guard so a
	// rejected concurrent request is not recorded.
	defer func() {
		o.recordConversationOutcome(ctx, taskMessage, result, err)
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
	conductorMessage := o.augmentWithAttachments(taskMessage, bb)

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

	// Truncate conversation history to the configured window so long
	// sessions don't overflow the Conductor's context. The most recent
	// messages are kept — they carry the dialogue context the agent needs
	// to understand follow-up references (e.g. "implement variant a").
	conductorHistory := truncateHistory(o.conversationHistory, o.config.ConductorHistoryWindow)

	plansDir := opts.SessionPlansDir
	execResult, err := o.runConductor(ctx, conductorMessage, bb, availableTools, plansDir, conductorHistory, nil)
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
