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

	"github.com/google/uuid"
	"github.com/user/agent/core/skills"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	sdktools "github.com/user/agent/sdk/tools"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

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
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// pruningOverrides, when provided, override the global pruning configuration.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager

// BlackboardFactory creates a Blackboard for a new task.
// The taskID is a unique identifier for the orchestration request.
type BlackboardFactory func(taskID string) Blackboard

// Orchestrator coordinates the agent's reasoning cycle.
// It handles c0wrk-specific concerns (routing, PersistentBlackboard,
// conversation history) and delegates the Plan&Execute loop to the SDK engine.
type Orchestrator struct {
	engine              *orchestration.Orchestrator // SDK P&E engine
	planner             *Planner                    // for PlanContinuation in P&E continuations
	router              *Router
	llm                 LLMCaller
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
	vectorSearchFunc    tools.VectorSearchFunc
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
}

// OrchestratorDeps holds the runtime dependencies for the Orchestrator.
// Grouping them into a single struct improves readability when the constructor
// is called (one struct literal instead of 19 positional arguments).
type OrchestratorDeps struct {
	Router           *Router
	Planner          *Planner
	LLM              LLMCaller
	ToolExec         ToolExecutor
	ToolRegistry     *sdktools.ToolRegistry
	TokenCounter     llm.TokenCounter
	ContextFactory   ContextManagerFactory
	Reflector        *Reflector         // optional, nil-safe
	Logger           *slog.Logger       // optional, nil-safe
	Emitter          Emitter            // optional, uses noopEmitter if nil
	ModelRegistry    *llm.ModelRegistry // optional, nil-safe
	ToolResultBudget ToolResultBudget
	CircuitBreaker   CircuitBreakerConfig
	BBFactory        BlackboardFactory      // optional, nil = default MapBlackboard
	TrackingCaller   *llm.TrackingCaller    // optional, for per-step context tracker wiring
	VectorSearchFunc tools.VectorSearchFunc // optional, for auto-RAG hint generation
	SkillManager     *skills.SkillManager   // optional, for skill discovery and activation
	CoreToolRegistry *tools.ToolRegistry    // core tool registry for skill policy overrides
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
	planner   *Planner
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
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard, routing *RoutingDecision) (*HandleResult, error) {
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
		o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
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
func lastReasoningContent(bb Blackboard) string {
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
func (o *Orchestrator) buildSkillPolicyOverrides(activeSkills []*skills.Skill) map[string]tools.ToolPolicy {
	overrides := make(map[string]tools.ToolPolicy)
	for _, s := range activeSkills {
		for _, toolName := range s.Metadata.AllowedToolList() {
			// Only set if not already overridden by config
			overrides[toolName] = tools.PolicyAlwaysAllow
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
		results, err := o.vectorSearchFunc(ragCtx, tools.VectorSearchOptions{Query: query, TopK: 5})
		ragCancel()

		if err != nil {
			o.logDebug("vector search hints skipped", "error", err)
		} else if len(results) > 0 {
			hints = &VectorSearchHints{}
			for _, r := range results {
				summary := r.Content
				if len(summary) > 100 {
					summary = summary[:100]
				}
				hints.Files = append(hints.Files, VectorSearchHint{
					FilePath: r.FilePath,
					Summary:  summary,
				})
			}
			o.logDebug("vector search hints injected", "count", len(hints.Files))
		}
	}

	// Always try to read AGENTS.md from workspace root.
	if wsPath := sdktools.WorkspacePathFrom(ctx); wsPath != "" {
		agentsMDPath := filepath.Join(wsPath, "AGENTS.md")
		if content, err := os.ReadFile(agentsMDPath); err == nil {
			ctx = WithAgentsMD(ctx, &AgentsMD{Content: string(content)})
			o.logDebug("AGENTS.md found and injected", "path", agentsMDPath, "size", len(content))

			// Prepend AGENTS.md as the first hint so it always appears in
			// the "Relevant Project Files" section for executors.
			if hints == nil {
				hints = &VectorSearchHints{}
			}
			summary := string(content)
			if len(summary) > 100 {
				summary = summary[:100]
			}
			agentsHint := VectorSearchHint{
				FilePath: "AGENTS.md",
				Summary:  summary,
			}
			hints.Files = append([]VectorSearchHint{agentsHint}, hints.Files...)
		} else {
			o.logDebug("AGENTS.md not found in workspace", "path", agentsMDPath)
		}
	}

	if hints != nil && len(hints.Files) > 0 {
		ctx = WithVectorSearchHints(ctx, hints)
	}

	return ctx
}

// shouldUseSingleStep determines whether to use a single-step plan.
// Only "normal" mode produces exactly 1 step; everything else (including empty string)
// defaults to full multi-step Plan&Execute.
func (o *Orchestrator) shouldUseSingleStep(mode string) bool {
	return mode == "normal"
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
	// 0. Always set plan mode context key (planning always happens first).
	ctx = context.WithValue(ctx, PlanModeKey, true)

	// Generate RAG hints from vector index (non-blocking, 2s timeout).
	ctx = o.injectVectorSearchHints(ctx, message)

	// 1. Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	o.emitInitialContextFill(ctx)

	var bb Blackboard

	// 2. Blackboard lifecycle
	if opts.TaskID == "" {
		// First message: create clean BB
		taskID := uuid.New().String()
		if o.bbFactory != nil {
			bb = o.bbFactory(taskID)
		} else {
			bb = NewMapBlackboard()
		}
		bb.SetOriginalRequest(message)

		// Wire emitter if PersistentBlackboard
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetEmitter(o.emitter)
		}
	} else {
		// Continuation: restore existing BB
		if o.taskStore == nil || o.bbRestoreFunc == nil {
			return nil, errors.New("task persistence not configured")
		}

		o.logDebug("orchestrator: restoring blackboard", "taskID", opts.TaskID)
		pbb, err := o.bbRestoreFunc(opts.TaskID, sessionID, o.taskStore, o.logger)
		if err != nil {
			return nil, fmt.Errorf("failed to restore blackboard: %w", err)
		}
		if pbb == nil {
			return nil, fmt.Errorf("task not found: %s", opts.TaskID)
		}

		// Wire emitter
		pbb.SetEmitter(o.emitter)

		// Reactivate task
		pbb.ReactivateTask()

		bb = pbb
	}

	// 3. Get available tools
	availableTools := o.toolRegistry.List()
	mcpCount := 0
	for _, t := range availableTools {
		if t.Source != "" && t.Source != "core" {
			mcpCount++
		}
	}
	o.logDebug("orchestrator: tools loaded from registry", "total", len(availableTools), "mcp", mcpCount)

	// 4. Route the message
	o.logDebug("orchestrator: starting routing")
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})
	var skillDescriptors []skills.SkillDescriptor
	if o.skillManager != nil {
		skillDescriptors = o.skillManager.List()
	}

	// When the user explicitly invoked skill(s) via /skill-name, the message
	// has the skill reference stripped (e.g. "/vibespec-check entire codebase"
	// → "entire codebase"). The router would classify the stripped message
	// without skill context, producing unreliable domain/complexity/clarification
	// judgments. Build a routing-specific message that restores the skill context.
	routingMessage := message
	if len(opts.UserSkills) > 0 && o.skillManager != nil {
		routingMessage = o.buildSkillAugmentedRoutingMessage(message, opts.UserSkills)
	}
	routing, err := o.router.Route(ctx, routingMessage, availableTools, o.conversationHistory, skillDescriptors)
	if err != nil {
		o.logDebug("orchestrator: routing failed", "error", err)
		return nil, fmt.Errorf("routing failed: %w", err)
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// 4b. Activate matched skills (merge router-matched + user-specified, deduplicated)
	var activeSkillDescriptors []skills.SkillDescriptor
	mergedSkillNames := mergeSkillNames(routing.MatchedSkills, opts.UserSkills)
	if len(mergedSkillNames) > 0 && o.skillManager != nil {
		var activeSkills []*skills.Skill
		var activatedNames []string
		for _, name := range mergedSkillNames {
			if s, ok := o.skillManager.Get(name); ok {
				activeSkills = append(activeSkills, s)
				activatedNames = append(activatedNames, name)
				activeSkillDescriptors = append(activeSkillDescriptors, s.Descriptor())
			} else {
				o.logDebug("orchestrator: matched skill not found", "name", name)
			}
		}
		if len(activeSkills) > 0 {
			ctx = WithActiveSkills(ctx, &ActiveSkills{Skills: activeSkills})
			o.emitter.SkillsActivated(activatedNames)
			o.logInfo("skills_activated", "skills", activatedNames)

			// Apply skill-derived tool policy overrides.
			// NOTE: skill rendering narrows per step (see coreStepConfigurator),
			// but skill *policy* stays task-wide here — the tool registry is
			// shared across the task and tools are keyed by name, not by step,
			// so per-step policy is not meaningful. Deliberate asymmetry.
			skillOverrides := o.buildSkillPolicyOverrides(activeSkills)
			if len(skillOverrides) > 0 && o.coreToolRegistry != nil {
				o.coreToolRegistry.SetSkillPolicyOverrides(skillOverrides)
			}
		}
	}

	// Capture the ctx and the router-matched skill pool for the step configurator
	// and the planner SDK adapter to read during this request. Cleared on return.
	ctxCopy := ctx
	o.currentRequestCtx.Store(&ctxCopy)
	skillsCopy := activeSkillDescriptors
	o.currentRequestSkills.Store(&skillsCopy)
	defer func() {
		o.currentRequestCtx.Store(nil)
		o.currentRequestSkills.Store(nil)
	}()

	// 5. Handle clarification
	// When the user explicitly invoked skills via /skill-name, their intent is
	// clear. Even though the routing message is now augmented with skill
	// context, suppress clarification as a safety net — the router may still
	// misjudge ambiguity for terse arguments like "entire codebase".
	if routing.NeedsClarification && len(opts.UserSkills) == 0 {
		o.logDebug("orchestrator: returning clarification request")
		return &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
			Blackboard:      bb,
		}, nil
	}
	if routing.NeedsClarification && len(opts.UserSkills) > 0 {
		o.logDebug("orchestrator: suppressing clarification — user explicitly invoked skills", "skills", opts.UserSkills)
		routing.NeedsClarification = false
	}

	var output string
	var execResult *orchestration.ExecutionResult
	var attemptCount int
	var reflections []Reflection

	// 6. Branch on first message vs. continuation
	switch opts.TaskID {
	case "":
		// === First message: plan first, then decide mode ===
		ctx = WithDomain(ctx, routing.Domain)
		ctx = WithComplexity(ctx, routing.Complexity)
		if len(opts.UserSkills) > 0 {
			ctx = WithUserSkills(ctx, opts.UserSkills)
		}

		singleStep := o.shouldUseSingleStep(opts.ExecutionMode)
		o.logDebug("orchestrator: generating plan", "mode", opts.ExecutionMode, "singleStep", singleStep)
		o.emitter.ServiceWithMeta("Planning approach...", map[string]any{"phase": "orchestration"})

		plan, planErr := o.planner.Plan(ctx, message, availableTools, nil, activeSkillDescriptors, singleStep)
		if planErr != nil {
			o.logDebug("orchestrator: planning failed", "error", planErr)
			return nil, fmt.Errorf("planning failed: %w", planErr)
		}
		o.logDebug("orchestrator: plan ready", "steps", len(plan.Steps))

		// Execute in Plan&Execute mode
		o.logDebug("orchestrator: executing in full Plan&Execute mode")
		o.emitter.ServiceWithMeta("Preparing execution...", map[string]any{"phase": "orchestration", "step_count": len(plan.Steps)})

		// Store plan on blackboard for P&E execution.
		bb.SetPlan(plan)

		// Plan already set on blackboard; use Resume which picks up the existing plan.
		execResult, err = o.engine.Resume(ctx, bb)
		if err != nil {
			o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
			if pbb, ok := bb.(PersistableBlackboard); ok {
				pbb.FailTask()
			}
			return nil, err
		}
		o.logDebug("orchestrator: SDK engine completed", "attemptCount", execResult.AttemptCount)
		output = execResult.Output
		attemptCount = execResult.AttemptCount
		reflections = execResult.Reflections

	default:
		// === Continuation (TaskID != "") ===
		o.logDebug("orchestrator: executing in Plan&Execute mode (continuation)")
		ctx = WithDomain(ctx, routing.Domain)
		ctx = WithComplexity(ctx, routing.Complexity)
		if len(opts.UserSkills) > 0 {
			ctx = WithUserSkills(ctx, opts.UserSkills)
		}

		// Get existing plan
		existingPlan := bb.GetPlan()
		if existingPlan == nil {
			return nil, errors.New("no existing plan found for continuation")
		}

		// Build completedSteps from BB's step results for PlanContinuation
		singleStep := o.shouldUseSingleStep(opts.ExecutionMode)
		allResults := bb.GetAllStepResults()
		completedSteps := make([]orchestration.CompletedStep, 0, len(allResults))
		for stepID, sr := range allResults {
			completedSteps = append(completedSteps, orchestration.CompletedStep{
				StepID: stepID,
				Output: sr.FullOutput,
				Steps:  sr.Steps,
			})
		}

		o.logDebug("orchestrator: calling PlanContinuation", "singleStep", singleStep)
		continuationPlan, planErr := o.planner.PlanContinuation(ctx, bb.GetOriginalRequest(), existingPlan, completedSteps, message, availableTools, activeSkillDescriptors, singleStep)
		if planErr != nil {
			o.logDebug("orchestrator: PlanContinuation failed", "error", planErr)
			return nil, fmt.Errorf("continuation planning failed: %w", planErr)
		}

		// Merge continuation plan's steps into existing plan
		mergedPlan := &orchestration.Plan{
			Steps: append(existingPlan.Steps, continuationPlan.Steps...),
		}
		bb.SetPlan(mergedPlan)

		o.logDebug("orchestrator: merged continuation plan", "newSteps", len(continuationPlan.Steps), "totalSteps", len(mergedPlan.Steps))

		// Resume execution with the merged plan (picks up un-completed steps)
		execResult, err = o.engine.Resume(ctx, bb)
		if err != nil {
			o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
			if pbb, ok := bb.(PersistableBlackboard); ok {
				pbb.FailTask()
			}
			return nil, err
		}
		o.logDebug("orchestrator: SDK engine resume completed", "attemptCount", execResult.AttemptCount)
		output = execResult.Output
		attemptCount = execResult.AttemptCount
		reflections = execResult.Reflections
	}

	// 7. Persist routing decision on PersistentBlackboard (post-execution)
	o.logDebug("orchestrator: persisting routing decision")
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
		pbb.CompleteTask(attemptCount)
	}

	// 8. Build HandleResult
	result := &HandleResult{
		Output:          output,
		RoutingDecision: routing,
		Blackboard:      bb,
		AttemptCount:    attemptCount,
		Reflections:     reflections,
	}

	// Get plan from blackboard if available
	if plan := bb.GetPlan(); plan != nil {
		result.Plan = plan
	}

	o.logDebug("orchestrator: handle_message completed", "attemptCount", result.AttemptCount)

	// 9. Accumulate conversation history for future routing context
	o.conversationHistory = append(o.conversationHistory,
		llm.Message{Role: "user", Content: message},
		llm.Message{Role: "assistant", Content: result.Output, ReasoningContent: lastReasoningContent(bb)},
	)
	maxHistory := o.config.MaxHistoryMessages
	if maxHistory == 0 {
		maxHistory = 20
	}
	if len(o.conversationHistory) > maxHistory {
		o.conversationHistory = o.conversationHistory[len(o.conversationHistory)-maxHistory:]
	}

	return result, nil
}
