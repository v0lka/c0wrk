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

	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	"github.com/v0lka/c0wrk/sdk/strutil"
	"github.com/v0lka/c0wrk/sdk/skills"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/planner"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

// DefaultAgentsMDMaxBytes is the default cap on AGENTS.md content injected into
// prompts. AGENTS.md is treated as untrusted, user-controlled input; an
// unbounded read would let a workspace inject arbitrarily large content into
// every system prompt.
const DefaultAgentsMDMaxBytes = 65536

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
	MaxSteps                  int
	KeepFirst                 int    // for sliding window compaction
	KeepLast                  int    // for sliding window compaction
	MaxRetries                int    // max retry attempts (default: 2, yielding 3 total executions)
	MaxHistoryMessages        int    // max conversation history messages to retain (default: 20)
	MaxDependencyContextChars int    // max chars for dependency context in step tasks (default: 8000)
	Model                     string // active model name for ModelRegistry.Resolve()

	// ReasoningEffort is the base reasoning effort for step executors.
	// When set, each executor receives AgentReasoningMode(stepRole, effort),
	// where stepRole comes from the step's AgentProfile.Role.
	ReasoningEffort llm.ReasoningEffort

	// RoleOverrides allows overriding the reasoning effort for specific agent roles.
	// Keys are role names, values are ReasoningEffort levels.
	RoleOverrides map[string]string

	// StepLimitFunc is called when an executor reaches its step limit.
	// If nil, the executor will stop with a budget exhausted error.
	StepLimitFunc agent.StepLimitFunc

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
// conversation history) and delegates the Plan&Execute loop to the SDK engine.
//
// Lifecycle: One Orchestrator is created per active session via Builder.Build().
// It lives for the duration of the session and is discarded when the session ends.
//
// Concurrency: Only one HandleMessage call may be active at a time per Orchestrator
// instance. The session manager enforces this contract — concurrent requests to the
// same session are rejected before reaching HandleMessage.
type Orchestrator struct {
	engine              *orchestration.Orchestrator // SDK P&E engine
	planner             *planner.Planner           // for PlanContinuation in P&E continuations
	router              *router.Router
	llm                 agent.LLMCaller
	toolRegistry        *sdktools.ToolRegistry
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
	vectorSearchFunc    builtins.VectorSearchFunc
	skillManager        *skills.SkillManager // for skill discovery and activation

	// currentRequestCtx captures the ctx produced at the top of HandleMessage
	// (after WithActiveSkills). coreStepConfigurator's step-local skill narrowing
	// and plannerSDKAdapter's Replan callback both read from this pointer to
	// access the router-matched skill pool. One active request per *Orchestrator
	// is the same concurrency assumption as conversationHistory above.
	currentRequestCtx atomic.Pointer[context.Context]

	// currentRequestSkills holds the router-matched skill descriptors for the
	// active request. Read by plannerSDKAdapter.Replan to thread skills into
	// the planner call back from the SDK engine.
	currentRequestSkills atomic.Pointer[[]skills.SkillDescriptor]

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
	Planner          *planner.Planner
	LLM              agent.LLMCaller
	ToolExec         agent.ToolExecutor
	ToolRegistry     *sdktools.ToolRegistry
	TokenCounter     llm.TokenCounter
	ContextFactory   ContextManagerFactory
	Reflector        *reflector.Reflector // optional, nil-safe
	Logger           *slog.Logger       // optional, nil-safe
	Emitter          Emitter            // optional, uses noopEmitter if nil
	ModelRegistry    *llm.ModelRegistry // optional, nil-safe
	ToolResultBudget agent.ToolResultBudget
	CircuitBreaker   agent.CircuitBreakerConfig
	BBFactory        BlackboardFactory      // optional, nil = default MapBlackboard
	TrackingCaller   *llm.TrackingCaller    // optional, for per-step context tracker wiring
	VectorSearchFunc builtins.VectorSearchFunc // optional, for auto-RAG hint generation
	SkillManager     *skills.SkillManager   // optional, for skill discovery and activation
	CoreToolRegistry *tools.ToolRegistry    // core tool registry for skill policy overrides

	// Tool result caching and per-tool truncation.
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(cfg OrchestratorConfig, deps OrchestratorDeps) *Orchestrator {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 30
	}
	if cfg.KeepFirst == 0 {
		cfg.KeepFirst = 3
	}
	if cfg.KeepLast == 0 {
		cfg.KeepLast = 10
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2 // default: 3 total attempts
	}
	emitter := deps.Emitter
	if emitter == nil {
		emitter = &noopEmitter{}
	}

	// Pre-construct the Orchestrator so the step configurator and planner adapter
	// can close over its currentRequestCtx / currentRequestSkills pointers.
	o := &Orchestrator{
		planner:          deps.Planner,
		router:           deps.Router,
		llm:              deps.LLM,
		toolRegistry:     deps.ToolRegistry,
		config:           cfg,
		contextFactory:   deps.ContextFactory,
		logger:           deps.Logger,
		emitter:          emitter,
		modelRegistry:    deps.ModelRegistry,
		bbFactory:        deps.BBFactory,
		trackingCaller:   deps.TrackingCaller,
		vectorSearchFunc: deps.VectorSearchFunc,
		skillManager:     deps.SkillManager,
		coreToolRegistry: deps.CoreToolRegistry,
	}

	// plannerAdapter adapts core.*Planner (which now takes an additional
	// availableSkills parameter) to sdk/orchestration.Planner. For Replan it
	// reads the router-matched skill pool captured at the top of HandleMessage.
	plannerAdapter := &plannerSDKAdapter{planner: deps.Planner, skillsFor: func() []skills.SkillDescriptor {
		if ptr := o.currentRequestSkills.Load(); ptr != nil {
			return *ptr
		}
		return nil
	}}

	// taskCtxProvider returns the ctx captured at the top of HandleMessage so
	// the step configurator can derive a step-local WithActiveSkills ctx.
	taskCtxProvider := func() context.Context {
		if ptr := o.currentRequestCtx.Load(); ptr != nil {
			return *ptr
		}
		return context.Background()
	}

	// Build SDK orchestration config
	sdkCfg := orchestration.Config{
		Planner:       plannerAdapter,
		LLM:           deps.LLM,
		Tools:         deps.ToolExec,
		ToolRegistry:  deps.ToolRegistry,
		TokenCounter:  deps.TokenCounter,
		Model:         cfg.Model,
		ModelRegistry: deps.ModelRegistry,
		ContextFactory: func(sys string, meta llm.ModelMetadata, compact string, pruningOverrides ...orchestration.PruningOverride) agent.ContextManager {
			return deps.ContextFactory(sys, meta, compact, pruningOverrides...)
		},
		ContextSetup: func(cm agent.ContextManager, taskDesc string) {
			if ccm, ok := cm.(interface{ SetTask(string) }); ok {
				ccm.SetTask(taskDesc)
			}
		},
		CallerForStep: func(cm agent.ContextManager) agent.LLMCaller {
			if deps.TrackingCaller == nil {
				return deps.LLM
			}
			if ctm, ok := cm.(interface {
				ContextTracker() *llm.ContextTokenTracker
			}); ok {
				return deps.TrackingCaller.WithContextTracker(ctm.ContextTracker())
			}
			return deps.TrackingCaller
		},
		Events:                    &emitterEventsAdapter{Emitter: emitter, logger: deps.Logger},
		SystemPrompt:              buildSystemPrompt,
		MaxRetries:                cfg.MaxRetries,
		MaxSteps:                  cfg.MaxSteps,
		MaxDependencyContextChars: cfg.MaxDependencyContextChars,
		ToolResultBudget:          deps.ToolResultBudget,
		CircuitBreaker:            deps.CircuitBreaker,
		PreWarningPercent:         cfg.PreWarningPercent,
		ToolCache:                 deps.ToolCache,
		PerToolTruncation:         deps.PerToolTruncation,
		ReasoningEffort:           cfg.ReasoningEffort,
		RoleOverrides:             cfg.RoleOverrides,
		StepConfigurator:          coreStepConfigurator(cfg, deps.ModelRegistry, deps.Logger, buildSystemPrompt, taskCtxProvider, deps.SkillManager),
		StepLimitFunc:             cfg.StepLimitFunc,
	}

	// Configure state factory to use core's blackboard factory.
	// If the factory produces a PersistentBlackboard, wire the emitter so
	// persistence warnings are surfaced to the user instead of being silently logged.
	if deps.BBFactory != nil {
		capturedEmitter := emitter // capture for closure
		sdkCfg.StateFactory = func(taskID string) orchestration.Blackboard {
			bb := deps.BBFactory(taskID)
			if pbb, ok := bb.(PersistableBlackboard); ok {
				pbb.SetEmitter(capturedEmitter)
			}
			return bb
		}
	}

	// Wire optional strategies
	if deps.Reflector != nil {
		sdkCfg.Reflection = deps.Reflector
	}

	o.engine = orchestration.New(sdkCfg)
	return o
}

// plannerSDKAdapter adapts the core *Planner (whose public methods now take
// an additional availableSkills parameter) to the stable sdk/orchestration.Planner
// interface. skillsFor returns the router-matched skill pool for the current
// request (read from Orchestrator state captured in HandleMessage).
type plannerSDKAdapter struct {
	planner   *planner.Planner
	skillsFor func() []skills.SkillDescriptor
}

func (a *plannerSDKAdapter) Plan(ctx context.Context, task string, availableTools []sdktools.ToolDescriptor, reflections []orchestration.Reflection) (*orchestration.Plan, error) {
	return a.planner.Plan(ctx, task, availableTools, reflections, a.skillsFor(), false)
}

func (a *plannerSDKAdapter) Replan(ctx context.Context, plan *orchestration.Plan, completed []orchestration.CompletedStep, failedStep orchestration.CompletedStep, reflection *orchestration.Reflection, reflections []orchestration.Reflection) (*orchestration.Plan, error) {
	return a.planner.Replan(ctx, plan, completed, failedStep, reflection, reflections, a.skillsFor())
}

func (a *plannerSDKAdapter) PlanContinuation(ctx context.Context, originalRequest string, existingPlan *orchestration.Plan, completedSteps []orchestration.CompletedStep, newMessage string, availableTools []sdktools.ToolDescriptor) (*orchestration.Plan, error) {
	return a.planner.PlanContinuation(ctx, originalRequest, existingPlan, completedSteps, newMessage, availableTools, a.skillsFor(), false)
}

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

// logDebug logs a DEBUG level message if logger is not nil.
func (o *Orchestrator) logDebug(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Debug(msg, args...)
	}
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
//
// Error semantics: when the SDK engine returns orchestration.ErrExecutionIncomplete with
// a non-nil ExecutionResult, Resume returns a valid *HandleResult alongside the error.
// This indicates partial success — the task made progress but did not finish (e.g., step
// limit reached). Callers should check errors.Is(err, orchestration.ErrExecutionIncomplete)
// and use the returned HandleResult for partial output, plan state, and blackboard.
// All other errors indicate complete failure; the task is marked failed and nil is returned.
func (o *Orchestrator) Resume(ctx context.Context, bb orchestration.Blackboard, routing *router.RoutingDecision) (*HandleResult, error) {
	o.logDebug("orchestrator: resume started")

	// Generate RAG hints from vector index using the original request.
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		ctx = o.injectVectorSearchHints(ctx, origReq)
	}

	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetEmitter(o.emitter)
	}
	plan := bb.GetPlan()
	if plan == nil {
		o.logDebug("orchestrator: resume failed, no plan in blackboard")
		return nil, errors.New("no plan found in restored blackboard")
	}

	// Emit initial context_fill
	o.emitInitialContextFill(ctx)

	// Emit routing decision so the frontend can display the resumed context.
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))

	o.logInfo("resume_task", "plan_steps", len(plan.Steps), "reflections", len(bb.GetReflections()))

	// Delegate to SDK engine
	o.logDebug("orchestrator: invoking SDK engine for resume", "planSteps", len(plan.Steps))
	execResult, err := o.engine.Resume(ctx, bb)
	if err != nil {
		if errors.Is(err, orchestration.ErrExecutionIncomplete) {
			if execResult == nil {
				return nil, fmt.Errorf("orchestrator: ErrExecutionIncomplete with nil result: %w", err)
			}
			o.logDebug("orchestrator: SDK engine resume reported incomplete execution", "error", err)
		} else {
			o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
			if pbb, ok := bb.(PersistableBlackboard); ok {
				pbb.FailTask()
			}
			return nil, err
		}
	}
	o.logDebug("orchestrator: SDK engine resume completed", "attemptCount", execResult.AttemptCount)

	result := &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Plan:            execResult.Plan,
		Blackboard:      execResult.Blackboard,
		AttemptCount:    execResult.AttemptCount,
		Reflections:     execResult.Reflections,
	}

	// Persist task completion if using persistent blackboard.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.CompleteTask(execResult.AttemptCount)
	}

	o.logDebug("orchestrator: resume completed", "attemptCount", result.AttemptCount)
	return result, nil
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

	// Vector search (optional, non-blocking).
	if o.vectorSearchFunc != nil {
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

// shouldUseSingleStep determines whether to use a single-step plan.
// Only ExecutionModeNormal mode produces exactly 1 step; everything else
// (including empty string) defaults to full multi-step Plan&Execute.
func (o *Orchestrator) shouldUseSingleStep(mode string) bool {
	return mode == ExecutionModeNormal
}

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) emitInitialContextFill(ctx context.Context) {
	var effectiveMax int
	if o.modelRegistry != nil {
		model := o.config.Model
		meta, _ := o.modelRegistry.Resolve(ctx, model)
		if meta.ContextWindow > 0 {
			safetyMargin := meta.ContextWindow * 5 / 100
			effectiveMax = meta.ContextWindow - meta.OutputLimit - safetyMargin
		}
	}
	o.emitter.ContextFill(0, 0, effectiveMax, "ok", "")
}

// SetTaskStore sets the TaskPersistence store for blackboard restoration.
// This is used by HandleMessage continuations to restore a completed task's blackboard.
func (o *Orchestrator) SetTaskStore(store TaskPersistence) {
	o.taskStore = store
}

// SetBlackboardRestoreFunc sets the function used to restore a PersistableBlackboard from persistence.
func (o *Orchestrator) SetBlackboardRestoreFunc(fn BlackboardRestoreFunc) {
	o.bbRestoreFunc = fn
}

// HandleMessage is the unified entry point for processing user messages.
// It supports two flows: first message and continuation.
// For first messages, a plan is always generated first, then executed via Plan&Execute.
// For continuations (TaskID != ""), the P&E continuation path is used.
//   - TaskID="": First message (create new blackboard)
//   - TaskID!="": Continuation (restore existing blackboard)
func (o *Orchestrator) HandleMessage(ctx context.Context, message, sessionID string, opts HandleOptions) (*HandleResult, error) {
	// W-8: Enforce single-flight — only one HandleMessage per *Orchestrator.
	if !o.requestInFlight.CompareAndSwap(false, true) {
		return nil, ErrRequestInFlight
	}
	defer o.requestInFlight.Store(false)

	// 0. Prepare context (plan-mode key, injection-defense, vector hints, initial context_fill).
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	ctx = o.prepareRequestContext(ctx, message)

	// 1. Setup blackboard (fresh or restored).
	bb, err := o.setupBlackboard(message, sessionID, opts.TaskID)
	if err != nil {
		return nil, err
	}

	// 2. Get available tools.
	availableTools := o.toolRegistry.List()
	mcpCount := 0
	for _, t := range availableTools {
		if t.SourceCategory == sdktools.SourceCategoryMCP {
			mcpCount++
		}
	}
	o.logDebug("orchestrator: tools loaded from registry", "total", len(availableTools), "mcp", mcpCount)

	// 3. Route and activate skills; may short-circuit with clarification.
	ctx, routing, activeSkills, clarification, err := o.routeAndActivateSkills(ctx, message, opts, bb, availableTools)
	if err != nil {
		return nil, err
	}
	if clarification != nil {
		return clarification, nil
	}

	// Capture ctx and skills for step configurator / planner adapter. Cleared on return.
	ctxCopy := ctx
	o.currentRequestCtx.Store(&ctxCopy)
	skillsCopy := activeSkills
	o.currentRequestSkills.Store(&skillsCopy)
	defer func() {
		o.currentRequestCtx.Store(nil)
		o.currentRequestSkills.Store(nil)
	}()

	// 4. Enrich context with domain/complexity/user-skills for execution.
	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// 5. Execute (first message or continuation).
	var execResult *orchestration.ExecutionResult
	switch opts.TaskID {
	case "":
		execResult, err = o.executeFirstMessage(ctx, message, bb, availableTools, activeSkills, opts)
	default:
		execResult, err = o.executeContinuation(ctx, message, bb, availableTools, activeSkills, opts)
	}
	// C-5: propagate ErrExecutionIncomplete alongside best-effort result.
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		return nil, err
	}
	incompleteErr := err

	// Guard against nil execResult: executeFirstMessage / executeContinuation may
	// return (nil, ErrExecutionIncomplete) when the SDK engine returns a nil result.
	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	// 6. Finalize: persist routing, build result, update history.
	result := o.finalizeResult(bb, routing, execResult, message)

	if incompleteErr != nil {
		return result, incompleteErr
	}
	return result, nil
}
