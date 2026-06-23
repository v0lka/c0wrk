package orchestration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/prompt"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// Orchestrator is the generic Plan&Execute engine. It composes strategy
// interfaces (Planner, Evaluator, Reflector, etc.) to run a DAG-based
// execution loop with optional retry/replan.
type Orchestrator struct {
	cfg        Config
	events     Events // non-nil (NoopEvents as default)
	maxRetries int    // resolved from Config (default 2)
	maxSteps   int    // resolved from Config (default 30)
}

// ErrExecutionIncomplete is returned by Execute/Resume when the plan was not
// fully executed (some steps remained un-attempted after per-step retries
// exhausted their budgets). The accompanying *ExecutionResult still contains
// best-effort output and the partially-completed blackboard. Callers should
// detect this with errors.Is so they can distinguish partial execution from
// total success while still consuming the result.
var ErrExecutionIncomplete = errors.New("plan execution incomplete")

// New creates a new Orchestrator from the given Config.
func New(cfg Config) *Orchestrator {
	events := cfg.Events
	if events == nil {
		events = &NoopEvents{}
	}
	maxRetries := cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2
	}
	maxSteps := cfg.MaxSteps
	if maxSteps == 0 {
		maxSteps = 30
	}
	return &Orchestrator{
		cfg:        cfg,
		events:     events,
		maxRetries: maxRetries,
		maxSteps:   maxSteps,
	}
}

// Cleanup releases held resources (per-step dump files). Idempotent.
func (o *Orchestrator) Cleanup() {
	if o.cfg.StepDumpTracker != nil {
		if err := o.cfg.StepDumpTracker.CloseAll(); err != nil {
			o.log().Warn("orchestrator cleanup: error closing step dump files", "error", err)
		}
	}
}

func (o *Orchestrator) log() *slog.Logger {
	if o.cfg.Logger != nil {
		return o.cfg.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Execute runs the full Plan&Execute loop for a user request.
func (o *Orchestrator) Execute(ctx context.Context, userMessage string) (*ExecutionResult, error) {
	// 1. Create blackboard
	var bb Blackboard
	if o.cfg.StateFactory != nil {
		bb = o.cfg.StateFactory(uuid.New().String())
	} else {
		bb = NewMapBlackboard()
	}
	bb.SetOriginalRequest(userMessage)

	// Get available tools
	availableTools := o.availableTools()

	o.events.OnServiceMeta("starting plan-execute loop", map[string]any{"tools": len(availableTools)})
	return o.runPlanExecute(ctx, userMessage, availableTools, nil, bb, nil, nil)
}

// ExecuteWithBlackboard runs the full Plan&Execute loop with a pre-existing blackboard.
// This is used for continuations where the blackboard has been restored from persistence.
// The blackboard must already have its OriginalRequest set.
func (o *Orchestrator) ExecuteWithBlackboard(ctx context.Context, userMessage string, bb Blackboard) (*ExecutionResult, error) {
	// Get available tools
	availableTools := o.availableTools()

	o.events.OnServiceMeta("starting plan-execute loop with existing blackboard", map[string]any{"tools": len(availableTools)})
	return o.runPlanExecute(ctx, userMessage, availableTools, nil, bb, nil, nil)
}

// Resume continues execution from a previously persisted blackboard state.
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard) (*ExecutionResult, error) {
	plan := bb.GetPlan()
	if plan == nil {
		return nil, errors.New("blackboard has no plan to resume")
	}
	userMessage := bb.GetOriginalRequest()
	reflections := bb.GetReflections()
	availableTools := o.availableTools()

	// Build preCompleted from blackboard step results
	preCompleted := make(map[string]CompletedStep)
	allResults := bb.GetAllStepResults()
	for stepID, sr := range allResults {
		if sr.Error == nil {
			preCompleted[stepID] = CompletedStep{
				StepID: stepID,
				Output: sr.FullOutput,
				Steps:  sr.Steps,
			}
		}
	}
	if len(preCompleted) == 0 {
		preCompleted = nil
	}

	// Re-emit plan with correct statuses
	planStepEvents := make([]PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		status := "pending"
		if preCompleted != nil {
			if _, ok := preCompleted[s.ID]; ok {
				status = "completed"
			}
		}
		planStepEvents[i] = PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: status, DependsOn: s.DependsOn}
	}
	o.log().Debug("orchestrator: OnPlanGenerated", "stepCount", len(plan.Steps), "steps", planStepEvents)
	o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)

	return o.runPlanExecute(ctx, userMessage, availableTools, reflections, bb, plan, preCompleted)
}

// runPlanExecute is the core plan-execute-reflect loop shared by Execute and Resume.
func (o *Orchestrator) runPlanExecute(
	ctx context.Context,
	userMessage string,
	availableTools []tools.ToolDescriptor,
	sessionReflections []Reflection,
	bb Blackboard,
	initialPlan *Plan,
	initialPreCompleted map[string]CompletedStep,
) (*ExecutionResult, error) {
	var currentPlan *Plan
	var preCompleted map[string]CompletedStep

	if initialPlan != nil {
		o.events.OnServiceMeta("resuming with existing plan", map[string]any{"steps": len(initialPlan.Steps), "preCompleted": len(initialPreCompleted)})
		currentPlan = initialPlan
		preCompleted = initialPreCompleted
	} else {
		// Generate a new plan
		o.events.OnServiceMeta("creating new plan", nil)
		o.events.OnServiceMeta("Creating execution plan...", map[string]any{"phase": "orchestration"})
		plan, err := o.cfg.Planner.Plan(ctx, userMessage, availableTools, sessionReflections)
		if err != nil {
			return nil, fmt.Errorf("planning failed: %w", err)
		}
		o.log().Debug("orchestrator: Planner.Plan returned", "steps", len(plan.Steps), "firstStepSummary", func() string {
			if len(plan.Steps) > 0 {
				return plan.Steps[0].Summary
			}
			return ""
		}())

		planStepEvents := make([]PlanStepEvent, len(plan.Steps))
		for i, s := range plan.Steps {
			planStepEvents[i] = PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
		}
		o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)
		bb.SetPlan(plan)
		currentPlan = plan
	}

	var lastOutput string
	var stepRetryContext string

	// Retry loop
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		// Check for context cancellation (e.g., user pressed Stop) before retrying.
		if ctx.Err() != nil {
			return &ExecutionResult{
				Output:       lastOutput,
				Plan:         currentPlan,
				Blackboard:   bb,
				AttemptCount: attempt,
				Reflections:  sessionReflections,
			}, ctx.Err()
		}
		if attempt > 0 {
			o.events.OnRetry(attempt, o.maxRetries+1)
		}

		// Execute the current plan
		o.events.OnServiceMeta("executing plan", map[string]any{"steps": len(currentPlan.Steps), "preCompleted": len(preCompleted)})
		finalOutput, completedSteps, updatedReflections, execErr := o.executePlanWithSteps(ctx, currentPlan, availableTools, preCompleted, userMessage, stepRetryContext, bb, sessionReflections)
		o.events.OnServiceMeta("plan execution finished", map[string]any{"completedSteps": len(completedSteps), "hasError": execErr != nil})
		sessionReflections = updatedReflections
		lastOutput = finalOutput
		prevCompletedSteps := completedSteps

		// Handle execution errors
		if execErr != nil {
			// Propagate context cancellation immediately — do not treat partial results as final.
			if ctx.Err() != nil {
				return &ExecutionResult{
					Output:       lastOutput,
					Plan:         currentPlan,
					Blackboard:   bb,
					AttemptCount: attempt + 1,
					Reflections:  sessionReflections,
				}, ctx.Err()
			}
			// Check if all steps were executed (some may have failed but all were attempted)
			allExecuted := len(completedSteps) == len(currentPlan.Steps)
			if !allExecuted {
				// Steps failed even after per-step retries — don't retry at the outer level
				// (per-step retries already exhausted the budget for failed steps).
				// Return the best-effort result with a sentinel error so callers can
				// distinguish partial execution from full success without parsing
				// the output string.
				bb.SetFinalResult(finalOutput)
				return &ExecutionResult{
					Output:       finalOutput + "\n\n[Execution incomplete: " + execErr.Error() + "]",
					Plan:         currentPlan,
					Blackboard:   bb,
					AttemptCount: attempt + 1,
					Reflections:  sessionReflections,
				}, fmt.Errorf("%w: %w", ErrExecutionIncomplete, execErr)
			}
			// All steps executed but some had errors — check if we should retry or reflect
		}

		// Set final result in blackboard
		bb.SetFinalResult(finalOutput)

		result := &ExecutionResult{
			Output:       finalOutput,
			Plan:         currentPlan,
			Blackboard:   bb,
			AttemptCount: attempt + 1,
			Reflections:  sessionReflections,
		}

		// Check for step execution errors
		var hasStepErrors bool
		for _, cs := range completedSteps {
			if cs.Error != nil {
				hasStepErrors = true
				break
			}
		}

		// Success - no step errors
		if !hasStepErrors {
			o.events.OnServiceMeta("plan execution completed successfully", nil)
			return result, nil
		}

		// Failure with retries remaining
		o.events.OnServiceMeta("steps failed", map[string]any{"attempt": attempt + 1, "retriesRemaining": o.maxRetries - attempt})
		if attempt < o.maxRetries {
			if o.cfg.Reflection != nil {
				executionSteps := BuildPlanExecutionSteps(completedSteps, currentPlan)
				o.events.OnServiceMeta("Some steps failed, reflecting...", map[string]any{"phase": "orchestration"})
				reflection, reflectErr := o.cfg.Reflection.Reflect(ctx, executionSteps, currentPlan, sessionReflections)
				if reflectErr != nil {
					continue // retry without reflection guidance
				}
				if reflection.SuggestedAction == "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					bb.AddReflection(*reflection)
					result.Reflections = sessionReflections
					result.Output = finalOutput + "\n\n[Execution: some steps failed. Reflector suggests abort.]"
					return result, nil
				}

				// Collect failed step IDs for logging
				var outerFailedIDs []string
				for _, cs := range completedSteps {
					if cs.Error != nil {
						outerFailedIDs = append(outerFailedIDs, cs.StepID)
					}
				}
				o.log().Info("reflection completed",
					"failed_steps", outerFailedIDs,
					"summary", reflection.Summary,
					"suggested_action", reflection.SuggestedAction,
					"root_cause", reflection.RootCause,
					"action_plan", reflection.ActionPlan,
					"failure_analysis", reflection.FailureAnalysis,
					"hypotheses", reflection.Hypotheses,
					"reasoning", reflection.Reasoning,
				)
				o.events.OnReflected(reflection, attempt+1, o.maxRetries)
				sessionReflections = append(sessionReflections, *reflection)
				bb.AddReflection(*reflection)

				if reflection.SuggestedAction == "replan" {
					o.log().Info("replan triggered by reflection",
						"suggested_action", reflection.SuggestedAction,
						"root_cause", reflection.RootCause,
						"action_plan", reflection.ActionPlan,
					)
					o.events.OnServiceMeta("replanning after reflection", nil)
					var failedStep CompletedStep
					if len(completedSteps) > 0 {
						failedStep = completedSteps[len(completedSteps)-1]
					}
					newPlan, replanErr := o.cfg.Planner.Replan(ctx, currentPlan, completedSteps, failedStep, reflection, sessionReflections)
					if replanErr != nil {
						o.events.OnReplanFailed(replanErr)
						continue
					}
					o.events.OnServiceMeta("replan succeeded", map[string]any{"newSteps": len(newPlan.Steps), "carryForward": len(BuildCarryForward(prevCompletedSteps, newPlan))})
					currentPlan = newPlan
					bb.SetPlan(currentPlan)
					preCompleted = BuildCarryForward(prevCompletedSteps, newPlan)
					stepRetryContext = ""
					o.emitPlanWithStatuses(currentPlan, preCompleted)
				} else {
					// Step-level retry - retry all failed steps
					preCompleted = make(map[string]CompletedStep)
					for _, cs := range prevCompletedSteps {
						if cs.Error == nil {
							preCompleted[cs.StepID] = cs
						}
					}
				}
			}
		}
	}

	bb.SetFinalResult(lastOutput)
	result := &ExecutionResult{
		Output:       lastOutput,
		Plan:         currentPlan,
		Blackboard:   bb,
		AttemptCount: o.maxRetries + 1,
		Reflections:  sessionReflections,
	}
	return result, nil
}

// executePlanWithSteps runs a DAG plan to completion and returns completed steps.
// It also handles per-step retries internally, returning updated reflections.
func (o *Orchestrator) executePlanWithSteps(
	ctx context.Context,
	plan *Plan,
	availableTools []tools.ToolDescriptor,
	preCompleted map[string]CompletedStep,
	userMessage, retryContext string,
	bb Blackboard,
	sessionReflections []Reflection,
) (string, []CompletedStep, []Reflection, error) {
	var completedSteps map[string]CompletedStep
	var completedList []CompletedStep
	if preCompleted != nil {
		completedSteps = make(map[string]CompletedStep, len(preCompleted))
		for k, v := range preCompleted {
			completedSteps[k] = v
			completedList = append(completedList, v)
		}
	} else {
		completedSteps = make(map[string]CompletedStep)
	}

	for ctx.Err() == nil {
		readySteps := FindReadySteps(plan, completedSteps)
		o.events.OnServiceMeta("finding ready steps", map[string]any{"ready": len(readySteps), "completed": len(completedSteps), "total": len(plan.Steps)})
		if len(readySteps) == 0 {
			o.events.OnServiceMeta("all steps complete", nil)
			break
		}

		readyIDs := make([]string, len(readySteps))
		for i, s := range readySteps {
			readyIDs[i] = s.ID
		}
		o.events.OnServiceMeta("ready steps identified", map[string]any{"stepIDs": readyIDs})

		tasks := make([]agent.SubAgentTask, 0, len(readySteps))
		stepStartTimes := make(map[string]time.Time)

		for _, step := range readySteps {
			stepIndex := o.findStepIndex(plan, step.ID)
			o.events.OnServiceMeta(fmt.Sprintf("Executing step %d/%d...", stepIndex+1, len(plan.Steps)), map[string]any{"phase": "orchestration"})
			o.log().Debug("orchestrator: OnStepStarted", "stepID", step.ID, "summary", step.Summary, "description", step.Description)
			o.events.OnStepStarted(step.ID, step.Description, step.Summary)
			stepStartTimes[step.ID] = time.Now()
		}

		for _, step := range readySteps {
			stepIndex := o.findStepIndex(plan, step.ID)

			// Resolve step config
			stepCfg := o.resolveStepConfig(step, availableTools)
			stepTools := stepCfg.AllowedTools
			if len(stepTools) == 0 {
				stepTools = availableTools
			}
			o.log().Debug("orchestrator: step tools resolved",
				"step", step.ID,
				"tool_count", len(stepTools),
				"using_full_set", len(stepCfg.AllowedTools) == 0,
			)
			maxSteps := stepCfg.MaxSteps
			if maxSteps == 0 {
				maxSteps = o.maxSteps
			}

			taskDef := o.buildStepTask(step, stepIndex, *plan, completedSteps, stepTools, bb, userMessage, retryContext, maxSteps)

			// Resolve model metadata
			modelMeta := o.resolveModelMeta(ctx)

			// Build system prompt
			var systemPrompt string
			switch {
			case stepCfg.SystemPrompt != "":
				systemPrompt = stepCfg.SystemPrompt
			case o.cfg.SystemPrompt != nil:
				systemPrompt = o.cfg.SystemPrompt(ctx, step.Description, modelMeta)
			default:
				systemPrompt = defaultSystemPrompt(ctx, step.Description)
			}
			if stepCfg.SystemPromptSuffix != "" {
				systemPrompt += "\n\n" + stepCfg.SystemPromptSuffix
			}

			// Create context manager
			var cm agent.ContextManager
			if o.cfg.ContextFactory != nil {
				cm = o.cfg.ContextFactory(systemPrompt, modelMeta, stepCfg.CompactionStrategy, PruningOverride{
					KeepLastN:      stepCfg.KeepLastN,
					ProtectedTools: stepCfg.ProtectedTools,
				})
			} else {
				return "", completedList, sessionReflections, errors.New("ContextFactory is required but not configured")
			}

			// Allow consumer to inject task-specific context
			if o.cfg.ContextSetup != nil {
				o.cfg.ContextSetup(cm, taskDef.task)
			}

			scopedEvents := o.scopeEvents(step.ID)
			stepCaller := o.callerForStep(cm, step.ID)
			executor := agent.NewExecutor(stepCaller, o.cfg.Tools, o.cfg.TokenCounter, maxSteps, scopedEvents, false, o.cfg.ToolResultBudget, o.cfg.CircuitBreaker)
			executor.SetPlanContext(step.ID, stepIndex+1, len(plan.Steps))
			o.configureExecutor(executor, stepCfg)

			todoUpdateFunc := func(stepID string, items []agent.TodoItem) {
				scopedEvents.OnStepTodoUpdate(stepID, items)
			}

			tasks = append(tasks, agent.SubAgentTask{
				StepID:         step.ID,
				Executor:       executor,
				CM:             cm,
				TaskTools:      taskDef.tools,
				TaskDesc:       taskDef.task,
				Emitter:        scopedEvents,
				TodoUpdateFunc: todoUpdateFunc,
			})
		}

		// Inject StepOutputStore into context so tools can read step outputs
		ctx = agent.WithStepOutputStore(ctx, NewStepOutputStore(bb))

		// Inject FactStore into context so tools can access fact memory
		ctx = agent.WithFactStore(ctx, NewFactStore(bb))

		o.events.OnServiceMeta("dispatching parallel execution", map[string]any{"taskCount": len(tasks)})
		results := agent.RunSubAgentsParallel(ctx, tasks)
		var failedSteps []string
		for _, r := range results {
			duration := time.Since(stepStartTimes[r.StepID])
			if r.Error != nil {
				o.events.OnStepCompleted(r.StepID, false, duration, r.Error.Error())
			} else {
				o.events.OnStepCompleted(r.StepID, true, duration, "")
			}

			cs := CompletedStep{
				StepID: r.StepID,
				Output: r.Output,
				Error:  r.Error,
				Steps:  r.Steps,
			}
			completedSteps[r.StepID] = cs
			completedList = append(completedList, cs)
			bb.SetStepResult(r.StepID, r.Output, r.Error, r.Steps)

			if r.Error != nil {
				o.log().Info("plan step failed",
					"step_id", r.StepID,
					"error", r.Error.Error(),
					"output_length", len(r.Output),
				)
				failedSteps = append(failedSteps, r.StepID)
			}
		}

		// Per-step retry loop for failed steps
		if len(failedSteps) > 0 {
			sessionReflections = o.retryFailedSteps(ctx, failedSteps, plan, availableTools, completedSteps, &completedList, bb, userMessage, sessionReflections)
		}

		// Check if any steps are still failed after per-step retries
		var stillFailed bool
		for _, stepID := range failedSteps {
			if cs, ok := completedSteps[stepID]; ok && cs.Error != nil {
				stillFailed = true
				break
			}
		}
		if stillFailed {
			o.events.OnServiceMeta("steps still failed after retries, escalating", nil)
			break // escalate to outer loop
		}
	}

	// Aggregate errors
	var stepErrors []error
	for _, cs := range completedList {
		if cs.Error != nil {
			stepErrors = append(stepErrors, fmt.Errorf("step %s failed: %w", cs.StepID, cs.Error))
		}
	}
	var aggErr error
	if len(stepErrors) > 0 {
		aggErr = errors.Join(stepErrors...)
	}

	// Build exclusion set from pre-completed step IDs so that only newly
	// completed steps contribute to the aggregated output.
	var preCompletedIDs map[string]bool
	if len(preCompleted) > 0 {
		preCompletedIDs = make(map[string]bool, len(preCompleted))
		for id := range preCompleted {
			preCompletedIDs[id] = true
		}
	}

	return AggregateOutput(completedSteps, plan, preCompletedIDs), completedList, sessionReflections, aggErr
}

// retryFailedSteps retries each failed step up to maxRetries times, performing
// reflection before each retry attempt when a reflector is configured.
// Returns the updated session reflections.
func (o *Orchestrator) retryFailedSteps(
	ctx context.Context,
	failedSteps []string,
	plan *Plan,
	availableTools []tools.ToolDescriptor,
	completedSteps map[string]CompletedStep,
	completedList *[]CompletedStep,
	bb Blackboard,
	userMessage string,
	sessionReflections []Reflection,
) []Reflection {
	o.events.OnServiceMeta("per-step retries starting", map[string]any{"failedSteps": failedSteps})
	for _, failedStepID := range failedSteps {
		// Find the failed step in the plan
		var failedPlanStep PlanStep
		for _, s := range plan.Steps {
			if s.ID == failedStepID {
				failedPlanStep = s
				break
			}
		}

		var stepRetryContext string
	stepRetryLoop:
		for retryAttempt := 1; retryAttempt <= o.maxRetries; retryAttempt++ {
			// Exit per-step retry if context was cancelled.
			if ctx.Err() != nil {
				break stepRetryLoop
			}
			scopedEvents := o.scopeEvents(failedStepID)
			scopedEvents = scopeRetryAttempt(scopedEvents, retryAttempt)
			scopedEvents.OnStepRetry(failedStepID, retryAttempt, o.maxRetries+1)

			// Reflect on failure if reflector is configured
			if o.cfg.Reflection != nil {
				o.events.OnServiceMeta(fmt.Sprintf("Step %s failed, reflecting...", failedStepID), map[string]any{"phase": "orchestration"})
				executionSteps := BuildPlanExecutionSteps(*completedList, plan)
				reflection, reflectErr := o.cfg.Reflection.Reflect(ctx, executionSteps, plan, sessionReflections)
				if reflectErr == nil {
					if reflection.SuggestedAction == "abort" {
						sessionReflections = append(sessionReflections, *reflection)
						bb.AddReflection(*reflection)
						o.events.OnReflected(reflection, retryAttempt, o.maxRetries)
						break stepRetryLoop
					}

					o.log().Info("reflection completed",
						"step_id", failedStepID,
						"summary", reflection.Summary,
						"suggested_action", reflection.SuggestedAction,
						"root_cause", reflection.RootCause,
						"action_plan", reflection.ActionPlan,
						"failure_analysis", reflection.FailureAnalysis,
						"hypotheses", reflection.Hypotheses,
						"reasoning", reflection.Reasoning,
					)
					o.events.OnReflected(reflection, retryAttempt, o.maxRetries)
					sessionReflections = append(sessionReflections, *reflection)
					bb.AddReflection(*reflection)

					// Build step retry context from reflection
					if reflection.ActionPlan != "" {
						stepRetryContext = fmt.Sprintf("Previous attempt failed. Reflection guidance:\n%s\n", reflection.ActionPlan)
					} else if reflection.RootCause != "" {
						stepRetryContext = fmt.Sprintf("Previous attempt failed. Root cause: %s\n", reflection.RootCause)
					}
				}
			}

			// Re-execute the failed step
			stepIndex := o.findStepIndex(plan, failedStepID)
			stepCfg := o.resolveStepConfig(failedPlanStep, availableTools)
			stepTools := stepCfg.AllowedTools
			if len(stepTools) == 0 {
				stepTools = availableTools
			}
			maxSteps := stepCfg.MaxSteps
			if maxSteps == 0 {
				maxSteps = o.maxSteps
			}

			taskDef := o.buildStepTask(failedPlanStep, stepIndex, *plan, completedSteps, stepTools, bb, userMessage, stepRetryContext, maxSteps)

			// Resolve model metadata
			modelMeta := o.resolveModelMeta(ctx)

			// Build system prompt
			var systemPrompt string
			switch {
			case stepCfg.SystemPrompt != "":
				systemPrompt = stepCfg.SystemPrompt
			case o.cfg.SystemPrompt != nil:
				systemPrompt = o.cfg.SystemPrompt(ctx, failedPlanStep.Description, modelMeta)
			default:
				systemPrompt = defaultSystemPrompt(ctx, failedPlanStep.Description)
			}
			if stepCfg.SystemPromptSuffix != "" {
				systemPrompt += "\n\n" + stepCfg.SystemPromptSuffix
			}

			// Create context manager
			var cm agent.ContextManager
			if o.cfg.ContextFactory != nil {
				cm = o.cfg.ContextFactory(systemPrompt, modelMeta, stepCfg.CompactionStrategy, PruningOverride{
					KeepLastN:      stepCfg.KeepLastN,
					ProtectedTools: stepCfg.ProtectedTools,
				})
			} else {
				return sessionReflections
			}

			// Allow consumer to inject task-specific context
			if o.cfg.ContextSetup != nil {
				o.cfg.ContextSetup(cm, taskDef.task)
			}

			retryCaller := o.callerForStep(cm, failedStepID)
			executor := agent.NewExecutor(retryCaller, o.cfg.Tools, o.cfg.TokenCounter, maxSteps, scopedEvents, true, o.cfg.ToolResultBudget, o.cfg.CircuitBreaker)
			executor.SetPlanContext(failedStepID, stepIndex+1, len(plan.Steps))
			o.configureExecutor(executor, stepCfg)

			todoUpdateFunc := func(stepID string, items []agent.TodoItem) {
				scopedEvents.OnStepTodoUpdate(stepID, items)
			}

			retryTask := agent.SubAgentTask{
				StepID:         failedStepID,
				Executor:       executor,
				CM:             cm,
				TaskTools:      taskDef.tools,
				TaskDesc:       taskDef.task,
				Emitter:        scopedEvents,
				TodoUpdateFunc: todoUpdateFunc,
			}

			// Execute single step
			o.events.OnServiceMeta(fmt.Sprintf("Retrying step %d/%d...", stepIndex+1, len(plan.Steps)), map[string]any{"phase": "orchestration"})
			o.events.OnStepStarted(failedStepID, failedPlanStep.Description, failedPlanStep.Summary)
			stepStartTime := time.Now()

			retryTasks := []agent.SubAgentTask{retryTask}
			retryResults := agent.RunSubAgentsParallel(ctx, retryTasks)

			for _, rr := range retryResults {
				if rr.Error != nil {
					o.events.OnStepCompleted(rr.StepID, false, time.Since(stepStartTime), rr.Error.Error())
				} else {
					o.events.OnStepCompleted(rr.StepID, true, time.Since(stepStartTime), "")
				}

				cs := CompletedStep{
					StepID: rr.StepID,
					Output: rr.Output,
					Error:  rr.Error,
					Steps:  rr.Steps,
				}
				completedSteps[rr.StepID] = cs
				// Update the completedList to replace the failed entry
				for i, c := range *completedList {
					if c.StepID == rr.StepID {
						(*completedList)[i] = cs
						break
					}
				}

				if rr.Error == nil {
					bb.SetStepResult(rr.StepID, rr.Output, nil, rr.Steps)
					break stepRetryLoop
				}
				bb.SetStepResult(rr.StepID, rr.Output, rr.Error, rr.Steps)
			}
		}
	}
	return sessionReflections
}

// stepTaskDef holds the result of buildStepTask.
type stepTaskDef struct {
	task  string
	tools []tools.ToolDescriptor
}

// buildStepTask creates the task description for a plan step executor.
func (o *Orchestrator) buildStepTask(
	step PlanStep, stepIndex int, plan Plan,
	completedSteps map[string]CompletedStep,
	stepTools []tools.ToolDescriptor,
	bb Blackboard,
	userMessage, retryContext string,
	maxSteps int,
) stepTaskDef {
	var b strings.Builder

	// Step position header
	fmt.Fprintf(&b, "[Step %d of %d] Your task: %s\n\n", stepIndex+1, len(plan.Steps), step.Description)

	// Budget awareness
	fmt.Fprintf(&b, "Tool call budget: %d iterations. Plan your approach to finish within this budget.\n\n", maxSteps)

	// Scoping instruction
	b.WriteString("IMPORTANT: You are executing ONE step in a multi-step plan. Complete ONLY this step's objective. Do NOT produce final deliverables or perform work belonging to other steps.\n\n")

	// Plan overview with markers
	b.WriteString("Plan overview:\n")
	for i, s := range plan.Steps {
		label := s.Summary
		if label == "" {
			label = s.Description
		}
		switch {
		case i < stepIndex:
			fmt.Fprintf(&b, "  ✓ Step %d: %s\n", i+1, label)
		case i == stepIndex:
			fmt.Fprintf(&b, "  → Step %d: %s (THIS STEP)\n", i+1, label)
		default:
			fmt.Fprintf(&b, "  · Step %d: %s\n", i+1, label)
		}
	}

	// Original user request
	if userMessage != "" {
		b.WriteString("\n## Original User Request\nThe following is the original request from the user. Use it as reference for any data, URLs, files, or other resources mentioned. Do NOT expand your scope beyond this step's objective.\n\n")
		b.WriteString(userMessage)
		b.WriteString("\n")
	}

	// Exploration context from planner research
	if plan.ExplorationContext != "" {
		b.WriteString("\n## Planner Research Context\nThe following was discovered during planning-phase exploration:\n\n")
		b.WriteString(plan.ExplorationContext)
		b.WriteString("\n")
	}

	// Specific objective
	fmt.Fprintf(&b, "\nYour specific objective: %s\n\n", step.Description)
	b.WriteString("**Your primary objective is to satisfy the acceptance criteria defined in the step description above. Every action you take should be directed toward meeting these criteria. Do not consider this step complete until all acceptance criteria are met.**\n\n")
	b.WriteString("## Pre-Finish Verification\nBefore calling the finish tool, you MUST:\n\n")
	b.WriteString("1. Re-read the acceptance criteria from the step description above.\n")
	b.WriteString("2. For EACH criterion, verify it is satisfied — use tool calls (read_file, ripgrep, bash_exec, etc.) to confirm, not assumptions.\n")
	b.WriteString("3. If ANY criterion is not met, continue working rather than calling finish.\n")
	b.WriteString("4. Only call finish when ALL criteria are verified.\n\n")
	b.WriteString("Do NOT call finish based on effort or time spent — call it only when every acceptance criterion is objectively satisfied.\n\n")
	b.WriteString("Produce output that is scoped to this step only. Later steps will build on your output.\n")
	b.WriteString("Pass your result through the finish tool. Write files ONLY if the file itself is the deliverable (code, config, etc.) — do NOT write files just to pass data to later steps.\n")

	// Context from previous steps
	if len(step.DependsOn) > 0 {
		var depBuf strings.Builder
		for _, depID := range step.DependsOn {
			summary := bb.GetStepSummary(depID)
			if summary != "" {
				fmt.Fprintf(&depBuf, "\n- [%s]: %s", depID, summary)
			}
		}
		depContext := depBuf.String()
		maxDepChars := o.cfg.MaxDependencyContextChars
		if maxDepChars == 0 {
			maxDepChars = 8000
		}
		if len(depContext) > maxDepChars {
			depContext = depContext[len(depContext)-maxDepChars:]
			if idx := strings.Index(depContext, "\n-["); idx >= 0 {
				depContext = depContext[idx:]
			}
		}
		if depContext != "" {
			b.WriteString("\nContext from previous steps:")
			b.WriteString(depContext)
			b.WriteString("\n\nIf the above summaries are insufficient, use `read_step_output` with the step ID to access the full output, or `list_step_outputs` to see all available outputs.")
		}
	}

	// Retry context
	if retryContext != "" {
		fmt.Fprintf(&b, "\n\n## Existing Files From Previous Attempt\n%s\nIMPORTANT: Fix these files IN PLACE. Do NOT create new files with different names.\n", retryContext)
	}

	return stepTaskDef{
		task:  b.String(),
		tools: stepTools,
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// callerForStep returns a step-local LLMCaller for the given ContextManager.
// resolveModelMeta returns model metadata for the active model.
// Uses cfg.Model to look up the registry; falls back to sensible defaults if unavailable.
func (o *Orchestrator) resolveModelMeta(ctx context.Context) llm.ModelMetadata {
	if o.cfg.ModelRegistry != nil && o.cfg.Model != "" {
		meta, _ := o.cfg.ModelRegistry.Resolve(ctx, o.cfg.Model)
		return meta
	}
	if o.cfg.ModelRegistry != nil {
		// Fallback: empty model still goes through registry (returns defaults)
		meta, _ := o.cfg.ModelRegistry.Resolve(ctx, "")
		return meta
	}
	return llm.ModelMetadata{}
}

// callerForStep returns a step-local LLMCaller for the given ContextManager and step ID.
// If CallerForStep is configured, it delegates to it; otherwise falls back to the shared LLM.
func (o *Orchestrator) callerForStep(cm agent.ContextManager, stepID string) agent.LLMCaller {
	if o.cfg.CallerForStep != nil {
		return o.cfg.CallerForStep(cm, stepID)
	}
	return o.cfg.LLM
}

// scopeEvents returns step-scoped events if the Events implementation supports it.
func (o *Orchestrator) scopeEvents(stepID string) Events {
	if s, ok := o.events.(StepScopable); ok {
		return s.WithStepID(stepID)
	}
	return o.events
}

// scopeRetryAttempt returns retry-scoped events if the Events implementation supports it.
func scopeRetryAttempt(events Events, attempt int) Events {
	if r, ok := events.(RetryScopable); ok {
		return r.WithRetryAttempt(attempt)
	}
	return events
}

// availableTools returns tool descriptors from the registry.
func (o *Orchestrator) availableTools() []tools.ToolDescriptor {
	if o.cfg.ToolRegistry != nil {
		return o.cfg.ToolRegistry.List()
	}
	return nil
}

// findStepIndex returns the 0-based index of a step in the plan.
func (o *Orchestrator) findStepIndex(plan *Plan, stepID string) int {
	for i, s := range plan.Steps {
		if s.ID == stepID {
			return i
		}
	}
	return -1
}

// configureExecutor applies shared executor settings from orchestrator config.
func (o *Orchestrator) configureExecutor(executor *agent.Executor, stepCfg StepConfig) {
	if o.cfg.StepLimitFunc != nil {
		executor.SetStepLimitFunc(o.cfg.StepLimitFunc)
	}
	if o.cfg.PreWarningPercent > 0 {
		executor.SetPreWarningPercent(o.cfg.PreWarningPercent)
	}
	if o.cfg.ToolCache != nil {
		executor.SetToolCache(o.cfg.ToolCache)
	}
	if o.cfg.PerToolTruncation != nil {
		executor.SetPerToolTruncation(o.cfg.PerToolTruncation)
	}
	if o.cfg.ReasoningEffort != "" {
		executor.SetReasoningEffort(o.cfg.ReasoningEffort)
	}
}

// SetReasoningEffort updates the reasoning effort used for all subsequent step executors.
func (o *Orchestrator) SetReasoningEffort(effort string) {
	o.cfg.ReasoningEffort = effort
}

// resolveStepConfig resolves step-specific configuration via StepConfigurator.
func (o *Orchestrator) resolveStepConfig(step PlanStep, allTools []tools.ToolDescriptor) StepConfig {
	if o.cfg.StepConfigurator != nil {
		return o.cfg.StepConfigurator(step, StepDefaults{
			MaxSteps: o.maxSteps,
			AllTools: allTools,
		})
	}
	return StepConfig{MaxSteps: o.maxSteps}
}

// emitPlanWithStatuses emits a plan with correct step statuses.
func (o *Orchestrator) emitPlanWithStatuses(plan *Plan, preCompleted map[string]CompletedStep) {
	planStepEvents := make([]PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		status := "pending"
		if preCompleted != nil {
			if _, ok := preCompleted[s.ID]; ok {
				status = "completed"
			}
		}
		planStepEvents[i] = PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: status, DependsOn: s.DependsOn}
	}
	o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)
}

// defaultSystemPrompt creates a minimal system prompt using prompt.SystemPromptBuilder.
// The builder supports cache-break markers that downstream consumers (e.g., memory.ContextWindow)
// split into multiple system messages for provider-level prompt caching.
func defaultSystemPrompt(_ context.Context, stepDescription string) string {
	return prompt.NewSystemPromptBuilder().
		Core("You are an AI assistant executing a task step.").
		CacheBreak().
		Dynamic("Your task: " + stepDescription).
		Build()
}
func (o *Orchestrator) ExecuteAdHocStep(
	ctx context.Context,
	bb Blackboard,
	step PlanStep,
	userMessage string,
	streaming bool,
) (*StepResult, error) {
	// 1. Extend the plan with the new step
	plan := bb.GetPlan()
	if plan == nil {
		plan = &Plan{Steps: []PlanStep{step}}
	} else {
		plan.Steps = append(plan.Steps, step)
	}
	bb.SetPlan(plan)

	// Get available tools
	availableTools := o.availableTools()

	// 2. Resolve step config and build step task
	stepIndex := len(plan.Steps) - 1 // New step is at the end
	stepCfg := o.resolveStepConfig(step, availableTools)
	stepTools := stepCfg.AllowedTools
	if len(stepTools) == 0 {
		stepTools = availableTools
	}
	maxSteps := stepCfg.MaxSteps
	if maxSteps == 0 {
		maxSteps = o.maxSteps
	}

	// Build completedSteps from existing blackboard results for dependency context
	allResults := bb.GetAllStepResults()
	completedSteps := make(map[string]CompletedStep, len(allResults))
	for stepID, sr := range allResults {
		completedSteps[stepID] = CompletedStep{
			StepID: stepID,
			Output: sr.FullOutput,
			Steps:  sr.Steps,
		}
	}

	taskDef := o.buildStepTask(step, stepIndex, *plan, completedSteps, stepTools, bb, userMessage, "", maxSteps)

	// Resolve model metadata
	modelMeta := o.resolveModelMeta(ctx)

	// 3. Build system prompt with role suffix from step's profile (if present)
	var systemPrompt string
	switch {
	case stepCfg.SystemPrompt != "":
		systemPrompt = stepCfg.SystemPrompt
	case o.cfg.SystemPrompt != nil:
		systemPrompt = o.cfg.SystemPrompt(ctx, step.Description, modelMeta)
	default:
		systemPrompt = defaultSystemPrompt(ctx, step.Description)
	}
	if stepCfg.SystemPromptSuffix != "" {
		systemPrompt += "\n\n" + stepCfg.SystemPromptSuffix
	}

	// Create context manager
	var cm agent.ContextManager
	if o.cfg.ContextFactory != nil {
		cm = o.cfg.ContextFactory(systemPrompt, modelMeta, stepCfg.CompactionStrategy, PruningOverride{
			KeepLastN:      stepCfg.KeepLastN,
			ProtectedTools: stepCfg.ProtectedTools,
		})
	} else {
		return nil, errors.New("ContextFactory is required but not configured")
	}

	// Allow consumer to inject task-specific context
	if o.cfg.ContextSetup != nil {
		o.cfg.ContextSetup(cm, taskDef.task)
	}

	// 4. Create executor infrastructure
	scopedEvents := o.scopeEvents(step.ID)
	stepCaller := o.callerForStep(cm, step.ID)
	executor := agent.NewExecutor(stepCaller, o.cfg.Tools, o.cfg.TokenCounter, maxSteps, scopedEvents, !streaming, o.cfg.ToolResultBudget, o.cfg.CircuitBreaker)
	executor.SetPlanContext(step.ID, stepIndex+1, len(plan.Steps))
	o.configureExecutor(executor, stepCfg)

	// 5. Inject StepOutputStore into context so tools can read step outputs
	ctx = agent.WithStepOutputStore(ctx, NewStepOutputStore(bb))

	// Inject FactStore into context so tools can access fact memory
	ctx = agent.WithFactStore(ctx, NewFactStore(bb))

	// 6. Build and execute the task
	o.log().Debug("orchestrator: OnStepStarted (subagent)", "stepID", step.ID, "summary", step.Summary, "description", step.Description)
	o.events.OnStepStarted(step.ID, step.Description, step.Summary)
	stepStartTime := time.Now()

	tasks := []agent.SubAgentTask{{
		StepID:    step.ID,
		Executor:  executor,
		CM:        cm,
		TaskTools: taskDef.tools,
		TaskDesc:  taskDef.task,
		Emitter:   scopedEvents,
	}}

	results := agent.RunSubAgentsParallel(ctx, tasks)

	// 8. Process results
	if len(results) == 0 {
		return nil, errors.New("no results returned from step execution")
	}

	r := results[0]
	duration := time.Since(stepStartTime)
	if r.Error != nil {
		o.events.OnStepCompleted(step.ID, false, duration, r.Error.Error())
	} else {
		o.events.OnStepCompleted(step.ID, true, duration, "")
	}

	// Propagate context cancellation as a function-level error (ReAct mode).
	if r.Error != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 9. Store results in Blackboard
	bb.SetStepResult(r.StepID, r.Output, r.Error, r.Steps)

	// 10. Return StepResult
	return &StepResult{
		StepID:     r.StepID,
		FullOutput: r.Output,
		Error:      r.Error,
		Steps:      r.Steps,
	}, nil
}
