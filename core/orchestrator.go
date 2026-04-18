// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxSteps                  int
	KeepFirst                 int // for sliding window compaction
	KeepLast                  int // for sliding window compaction
	MaxRetries                int // max retry attempts after failed evaluation (default: 3)
	MaxHistoryMessages        int // max conversation history messages to retain (default: 20)
	MaxDependencyContextChars int // max chars for dependency context in step tasks (default: 8000)

	// StepLimitFunc is called when an executor reaches its step limit.
	// If nil, the executor will stop with a budget exhausted error.
	StepLimitFunc agent.StepLimitFunc
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager

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
	toolRegistry        *tools.ToolRegistry
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
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(
	router *Router,
	planner *Planner,
	llmCaller LLMCaller,
	toolExec ToolExecutor,
	toolReg *tools.ToolRegistry,
	counter llm.TokenCounter,
	cfg OrchestratorConfig,
	contextFactory ContextManagerFactory,
	reflector *Reflector, // optional, nil-safe
	logger *slog.Logger, // optional, nil-safe
	emitter Emitter, // optional, uses noopEmitter if nil
	modelRegistry *llm.ModelRegistry, // optional, nil-safe
	toolResultBudget ToolResultBudget,
	circuitBreaker CircuitBreakerConfig,
	bbFactory BlackboardFactory, // optional, nil = default MapBlackboard
	trackingCaller *llm.TrackingCaller, // optional, for per-step context tracker wiring
) *Orchestrator {
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
	if emitter == nil {
		emitter = &noopEmitter{}
	}

	// Build SDK orchestration config
	sdkCfg := orchestration.Config{
		Planner:       planner,
		LLM:           llmCaller,
		Tools:         toolExec,
		ToolRegistry:  toolReg,
		TokenCounter:  counter,
		ModelRegistry: modelRegistry,
		ContextFactory: func(sys string, meta llm.ModelMetadata, compact string) agent.ContextManager {
			return contextFactory(sys, meta, compact)
		},
		ContextSetup: func(cm agent.ContextManager, taskDesc string) {
			if ccm, ok := cm.(interface{ SetTask(string) }); ok {
				ccm.SetTask(taskDesc)
			}
		},
		CallerForStep: func(cm agent.ContextManager) agent.LLMCaller {
			if trackingCaller == nil {
				return llmCaller
			}
			if ctm, ok := cm.(interface {
				ContextTracker() *llm.ContextTokenTracker
			}); ok {
				return trackingCaller.WithContextTracker(ctm.ContextTracker())
			}
			return trackingCaller
		},
		Events:                    &emitterEventsAdapter{emitter},
		SystemPrompt:              buildSystemPrompt,
		MaxRetries:                cfg.MaxRetries,
		MaxSteps:                  cfg.MaxSteps,
		MaxDependencyContextChars: cfg.MaxDependencyContextChars,
		ToolResultBudget:          toolResultBudget,
		CircuitBreaker:            circuitBreaker,
		StepConfigurator:          coreStepConfigurator(cfg, modelRegistry, logger),
		StepLimitFunc:             cfg.StepLimitFunc,
	}

	// Configure state factory to use core's blackboard factory.
	// If the factory produces a PersistentBlackboard, wire the emitter so
	// persistence warnings are surfaced to the user instead of being silently logged.
	if bbFactory != nil {
		capturedEmitter := emitter // capture for closure
		sdkCfg.StateFactory = func(taskID string) orchestration.Blackboard {
			bb := bbFactory(taskID)
			if pbb, ok := bb.(PersistableBlackboard); ok {
				pbb.SetEmitter(capturedEmitter)
			}
			return bb
		}
	}

	// Wire optional strategies
	if reflector != nil {
		sdkCfg.Reflection = reflector
	}

	engine := orchestration.New(sdkCfg)

	return &Orchestrator{
		engine:         engine,
		planner:        planner,
		router:         router,
		llm:            llmCaller,
		toolRegistry:   toolReg,
		config:         cfg,
		contextFactory: contextFactory,
		logger:         logger,
		emitter:        emitter,
		modelRegistry:  modelRegistry,
		bbFactory:      bbFactory,
		trackingCaller: trackingCaller,
	}
}

// logInfo logs an INFO level message if logger is not nil.
func (o *Orchestrator) logInfo(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Info(msg, args...)
	}
}

// logDebug logs a DEBUG level message if logger is not nil.
func (o *Orchestrator) logDebug(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Debug(msg, args...)
	}
}

// Handle executes the agent reasoning cycle for the given user message.
// This is a backwards-compatible wrapper around HandleMessage that uses
// Plan&Execute mode for first messages.
func (o *Orchestrator) Handle(ctx context.Context, userMessage string) (*HandleResult, error) {
	return o.HandleMessage(ctx, userMessage, "", HandleOptions{})
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard, routing *RoutingDecision) (*HandleResult, error) {
	o.logDebug("orchestrator: resume started")

	// Inject codebase-memory MCP availability into context for system prompt assembly.
	if o.toolRegistry != nil && o.toolRegistry.HasSourceContaining("codebase-memory") {
		ctx = context.WithValue(ctx, codebaseMemoryKey, true)
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
	o.emitInitialContextFill()

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

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) emitInitialContextFill() {
	var effectiveMax int
	if o.modelRegistry != nil {
		meta, _ := o.modelRegistry.Resolve("")
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

// Run is a backwards-compatible method that calls Handle.
// Kept for compatibility with Phase 1 code.
func (o *Orchestrator) Run(ctx context.Context, userMessage string) (*HandleResult, error) {
	return o.Handle(ctx, userMessage)
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

	// Inject codebase-memory MCP availability into context for system prompt assembly.
	if o.toolRegistry != nil && o.toolRegistry.HasSourceContaining("codebase-memory") {
		ctx = context.WithValue(ctx, codebaseMemoryKey, true)
	}

	// 1. Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	o.emitInitialContextFill()

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
	routing, err := o.router.Route(ctx, message, availableTools, o.conversationHistory)
	if err != nil {
		o.logDebug("orchestrator: routing failed", "error", err)
		return nil, fmt.Errorf("routing failed: %w", err)
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// 5. Handle clarification
	if routing.NeedsClarification {
		o.logDebug("orchestrator: returning clarification request")
		return &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
			Blackboard:      bb,
		}, nil
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

		// Generate plan
		o.logDebug("orchestrator: generating plan")
		o.emitter.ServiceWithMeta("Planning approach...", map[string]any{"phase": "orchestration"})
		plan, planErr := o.planner.Plan(ctx, message, availableTools, nil)
		if planErr != nil {
			o.logDebug("orchestrator: planning failed", "error", planErr)
			return nil, fmt.Errorf("planning failed: %w", planErr)
		}
		o.logDebug("orchestrator: plan generated", "steps", len(plan.Steps))

		// Execute in Plan&Execute mode
		o.logDebug("orchestrator: executing in full Plan&Execute mode")

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

		// Build completedSteps from BB's step results for PlanContinuation
		allResults := bb.GetAllStepResults()
		completedSteps := make([]orchestration.CompletedStep, 0, len(allResults))
		for stepID, sr := range allResults {
			completedSteps = append(completedSteps, orchestration.CompletedStep{
				StepID: stepID,
				Output: sr.FullOutput,
				Steps:  sr.Steps,
			})
		}

		// Get existing plan
		existingPlan := bb.GetPlan()
		if existingPlan == nil {
			return nil, errors.New("no existing plan found for continuation")
		}

		// Call planner's PlanContinuation
		o.logDebug("orchestrator: calling PlanContinuation")
		continuationPlan, planErr := o.planner.PlanContinuation(ctx, bb.GetOriginalRequest(), existingPlan, completedSteps, message, availableTools)
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
		llm.Message{Role: "assistant", Content: result.Output},
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
