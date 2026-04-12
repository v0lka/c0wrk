// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/prompt"
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
	taskStore           TaskPersistence // optional, for ContinueTask blackboard restoration
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
			if pbb, ok := bb.(*PersistentBlackboard); ok {
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
	}
}

// roleSuffixes defines role-specific system prompt suffixes for large models.
// These are appended to the base system prompt when no explicit SystemPrompt is set.
var roleSuffixes = map[string]string{
	"researcher": "## Role: Researcher\nYour primary function is information gathering and analysis. Synthesize findings clearly and pass all results through the finish tool. Do NOT create or modify project files.",
	"coder":      "## Role: Coder\nYour primary function is code implementation. Write clean, well-structured code. Verify your changes compile and work before finishing.",
	"tester":     "## Role: Tester\nYour primary function is verification and testing. Run tests, check builds, and report results clearly. Do NOT modify source code — only test infrastructure if necessary.",
}

// smallRoleSuffixes defines more explicit, directive role suffixes for small models.
// Small models benefit from clearer, more structured instructions.
var smallRoleSuffixes = map[string]string{
	"researcher": "## Role: Researcher\nYou gather information. Follow these rules:\n1. Use search and read tools to find information.\n2. Summarize findings clearly.\n3. Pass ALL results through the finish tool.\n4. Do NOT create or modify project files.\n5. Do NOT write code.",
	"coder":      "## Role: Coder\nYou write code. Follow these rules:\n1. Read existing code before making changes.\n2. Write clean, working code.\n3. Verify your changes compile before finishing.\n4. Use the finish tool when done.",
	"tester":     "## Role: Tester\nYou run tests and verify code. Follow these rules:\n1. Run the specified tests or checks.\n2. Report results clearly: PASS or FAIL.\n3. Do NOT modify source code.\n4. Use the finish tool with your findings.",
}

// coreStepConfigurator resolves AgentProfile from PlanStep.Profile.
// If modelRegistry is provided, role suffixes are selected based on model tier.
// Applies tool filtering based on AgentProfile.AllowedTools or role-based ToolProfiles.
func coreStepConfigurator(cfg OrchestratorConfig, modelRegistry *llm.ModelRegistry, logger *slog.Logger) orchestration.StepConfigurator {
	return func(step orchestration.PlanStep, defaults orchestration.StepDefaults) orchestration.StepConfig {
		profile := resolveAgentProfile(step, cfg.MaxSteps)

		// Determine allowed tools: explicit profile setting > role-based profile > all tools
		var allowed []tools.ToolDescriptor
		if len(profile.AllowedTools) > 0 {
			// Profile has explicit AllowedTools - use them
			allowed = FilterToolsByProfile(defaults.AllTools, profile.AllowedTools)
		} else if toolProfile, ok := ToolProfiles[profile.Role]; ok {
			// Apply role-based tool profile (e.g., "router", "planner", "reflector")
			allowed = FilterToolsByProfile(defaults.AllTools, toolProfile)
			if logger != nil {
				logger.Debug("orchestrator: applied role-based tool profile", "role", profile.Role, "tools", len(allowed))
			}
		}

		// Only inject role suffix when there's no explicit SystemPrompt override.
		// If someone explicitly set SystemPrompt, they're taking full control.
		var suffix string
		if profile.SystemPrompt == "" {
			// Resolve tier and pick appropriate suffix map
			tier := resolveTierFromRegistry(modelRegistry)
			if logger != nil {
				logger.Debug("orchestrator: model tier resolved", "tier", tier)
			}
			suffixMap := roleSuffixes
			if tier == prompt.TierSmall {
				suffixMap = smallRoleSuffixes
			}
			suffix = suffixMap[profile.Role]
		}

		return orchestration.StepConfig{
			MaxSteps:           profile.MaxSteps,
			AllowedTools:       allowed,
			SystemPrompt:       profile.SystemPrompt,
			SystemPromptSuffix: suffix,
			CompactionStrategy: applyCompactionStrategy(profile.Domain, 3),
		}
	}
}

// resolveTierFromRegistry resolves the model tier from the registry, defaulting to large.
func resolveTierFromRegistry(registry *llm.ModelRegistry) prompt.ModelTier {
	if registry == nil {
		return prompt.TierLarge
	}
	meta, _ := registry.Resolve("")
	tier := prompt.ModelTier(meta.Tier)
	if tier == "" {
		return prompt.TierLarge
	}
	return tier
}

// resolveAgentProfile returns the effective AgentProfile for a plan step.
func resolveAgentProfile(step orchestration.PlanStep, defaultMaxSteps int) AgentProfile {
	if step.Profile != nil {
		if profile, ok := step.Profile.(*AgentProfile); ok {
			p := *profile
			if p.MaxSteps == 0 {
				p.MaxSteps = defaultMaxSteps
			}
			return p
		}
	}
	return AgentProfile{Role: "executor", MaxSteps: defaultMaxSteps}
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
	return o.HandleMessage(ctx, userMessage, "", HandleOptions{PlanFirst: true})
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard, routing *RoutingDecision) (*HandleResult, error) {
	o.logDebug("orchestrator: resume started")
	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(*PersistentBlackboard); ok {
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
		if pbb, ok := bb.(*PersistentBlackboard); ok {
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
	if pbb, ok := bb.(*PersistentBlackboard); ok {
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

// buildSystemPrompt creates the system prompt for executors.
func buildSystemPrompt(ctx context.Context, userMessage string, modelMeta llm.ModelMetadata) string {
	// Build workspace context string
	var workspaceCtxStr string
	if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
		workspaceCtxStr = "## Workspace\nYour session workspace is: " + wsPath + "\nAll artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location."
		if tempDir := tools.TempDirFrom(ctx); tempDir != "" {
			workspaceCtxStr += "\nYour session temp directory is: " + tempDir + "\nUse this directory for ANY intermediate files — drafts, partial results, scratch data, inter-step artifacts. These files are NOT part of the final deliverable and will be cleaned up when the session ends."
		}
	}

	// Determine model tier (default to large if not specified)
	tier := prompt.ModelTier(modelMeta.Tier)
	if tier == "" {
		tier = prompt.TierLarge
	}

	// Build base prompt using the prompt builder
	result := prompt.New(tier).
		Core(prompts.OrchestratorSystem).
		ForLarge(prompts.OrchestratorLarge).
		ForSmall(prompts.OrchestratorSmall).
		Replace("WORKSPACE-CONTEXT", workspaceCtxStr).
		Build()

	// Append mode-specific context.
	if ctx.Value(PlanModeKey) != nil {
		result += "\n\n" + prompts.OrchestratorPlanContext
	} else {
		// ReAct mode: reinforce finish tool requirement since there's no plan context
		// to naturally motivate its use.
		result += "\n\n## Completion\nYou are operating in single-step mode. When you have completed your work, you MUST call the `finish` tool with your final answer. Do not simply respond with text — the system only recognizes task completion through an explicit `finish` tool call."
	}

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// SetTaskStore sets the TaskPersistence store for blackboard restoration.
// This is used by HandleMessage continuations to restore a completed task's blackboard.
func (o *Orchestrator) SetTaskStore(store TaskPersistence) {
	o.taskStore = store
}

// terminalSteps returns the IDs of steps that have no dependents in the plan.
// These are the leaf nodes of the DAG - steps that no other step depends on.
func terminalSteps(plan *Plan) []string {
	if plan == nil || len(plan.Steps) == 0 {
		return nil
	}

	// Build set of all steps that are dependencies of other steps
	dependedOn := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, depID := range step.DependsOn {
			dependedOn[depID] = true
		}
	}

	// Terminal steps are those not depended on by any other step
	var terminals []string
	for _, step := range plan.Steps {
		if !dependedOn[step.ID] {
			terminals = append(terminals, step.ID)
		}
	}
	return terminals
}

// domainToAgentProfile maps a routing domain to an AgentProfile with appropriate role.
func domainToAgentProfile(domain string, maxSteps int) AgentProfile {
	var role string
	switch domain {
	case "code":
		role = "coder"
	case "research":
		role = "researcher"
	default:
		role = "executor"
	}
	return AgentProfile{
		Role:     role,
		MaxSteps: maxSteps,
		Domain:   domain,
	}
}

// Run is a backwards-compatible method that calls Handle.
// Kept for compatibility with Phase 1 code.
func (o *Orchestrator) Run(ctx context.Context, userMessage string) (*HandleResult, error) {
	return o.Handle(ctx, userMessage)
}

// HandleMessage is the unified entry point for processing user messages.
// It supports all 4 flows: {ReAct, Plan&Execute} x {first message, continuation}.
// The opts parameter controls the behavior:
//   - PlanFirst=true: Use Plan&Execute mode (full planning, multi-step)
//   - PlanFirst=false: Use ReAct mode (single step)
//   - TaskID="": First message (create new blackboard)
//   - TaskID!="": Continuation (restore existing blackboard)
func (o *Orchestrator) HandleMessage(ctx context.Context, message, sessionID string, opts HandleOptions) (*HandleResult, error) {
	// 0. Set plan mode context key if PlanFirst is enabled.
	if opts.PlanFirst {
		ctx = context.WithValue(ctx, PlanModeKey, true)
	}

	// 1. Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "planFirst", opts.PlanFirst, "taskID", opts.TaskID)
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
		if pbb, ok := bb.(*PersistentBlackboard); ok {
			pbb.SetEmitter(o.emitter)
		}
	} else {
		// Continuation: restore existing BB
		if o.taskStore == nil {
			return nil, errors.New("task persistence not configured")
		}

		o.logDebug("orchestrator: restoring blackboard", "taskID", opts.TaskID)
		pbb, err := RestoreBlackboard(opts.TaskID, sessionID, o.taskStore, o.logger)
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

	// 6. Branch on PlanFirst
	switch {
	case !opts.PlanFirst:
		// === ReAct mode (single step) ===
		o.logDebug("orchestrator: executing in ReAct mode (single step)")

		// Build synthetic 1-step plan
		plan := bb.GetPlan()
		stepN := 0
		if plan != nil {
			for _, step := range plan.Steps {
				if strings.HasPrefix(step.ID, "continuation_") {
					stepN++
				}
			}
		}
		stepN++ // increment for the new step

		var stepID string
		var dependsOn []string
		if opts.TaskID == "" {
			// First message
			stepID = "step_1"
		} else {
			// Continuation
			stepID = fmt.Sprintf("continuation_%d", stepN)
			if plan != nil {
				dependsOn = terminalSteps(plan)
			}
		}

		// Build AgentProfile from routing domain
		profile := domainToAgentProfile(routing.Domain, o.config.MaxSteps)

		step := orchestration.PlanStep{
			ID:          stepID,
			Description: message,
			DependsOn:   dependsOn,
			Profile:     &profile,
		}

		// Execute ad-hoc step
		o.logDebug("orchestrator: executing ReAct step", "stepID", stepID)
		stepResult, err := o.engine.ExecuteAdHocStep(ctx, bb, step, bb.GetOriginalRequest(), true /* streaming */)
		if err != nil {
			o.logDebug("orchestrator: ReAct step failed", "error", err)
			return nil, fmt.Errorf("step execution failed: %w", err)
		}

		o.logDebug("orchestrator: ReAct step completed", "stepID", stepID, "hasError", stepResult.Error != nil)

		// Build output from step result
		if stepResult.Error != nil {
			output = "Step failed: " + stepResult.Error.Error()
		} else {
			output = stepResult.FullOutput
		}
		attemptCount = 1
		reflections = bb.GetReflections()

	case opts.TaskID == "":
		// === Plan&Execute, first message ===
		o.logDebug("orchestrator: executing in Plan&Execute mode (first message)")
		execResult, err = o.engine.ExecuteWithBlackboard(ctx, message, bb)
		if err != nil {
			o.logDebug("orchestrator: SDK engine returned error", "error", err)
			return nil, err
		}
		o.logDebug("orchestrator: SDK engine completed", "attemptCount", execResult.AttemptCount)
		output = execResult.Output
		attemptCount = execResult.AttemptCount
		reflections = execResult.Reflections

	default:
		// === Plan&Execute, continuation ===
		o.logDebug("orchestrator: executing in Plan&Execute mode (continuation)")

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
		continuationPlan, err := o.planner.PlanContinuation(ctx, bb.GetOriginalRequest(), existingPlan, completedSteps, message, availableTools)
		if err != nil {
			o.logDebug("orchestrator: PlanContinuation failed", "error", err)
			return nil, fmt.Errorf("continuation planning failed: %w", err)
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
			if pbb, ok := bb.(*PersistentBlackboard); ok {
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
	if pbb, ok := bb.(*PersistentBlackboard); ok {
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

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *agent.Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}
