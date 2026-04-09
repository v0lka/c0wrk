package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// maxDependencyContextChars is the maximum total character length for all
// dependency summaries injected into a step's task description (~2000 tokens).
const maxDependencyContextChars = 8000

// Orchestrator is the generic Plan&Execute engine. It composes strategy
// interfaces (Planner, Evaluator, Reflector, etc.) to run a DAG-based
// execution loop with optional retry/replan.
type Orchestrator struct {
	cfg        Config
	events     Events // non-nil (NoopEvents as default)
	maxRetries int    // resolved from Config (default 2)
	maxSteps   int    // resolved from Config (default 30)
}

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

// Execute runs the full Plan&Execute loop for a user request.
func (o *Orchestrator) Execute(ctx context.Context, userMessage string, criteria []Criterion) (*ExecutionResult, error) {
	// 1. Create blackboard
	var bb Blackboard
	if o.cfg.StateFactory != nil {
		bb = o.cfg.StateFactory("task")
	} else {
		bb = NewMapBlackboard()
	}
	bb.SetOriginalRequest(userMessage)
	bb.SetCriteria(criteria)

	// 2. Extract criteria (optional)
	if o.cfg.Criteria != nil && len(criteria) == 0 {
		extracted, err := o.cfg.Criteria.Extract(ctx, userMessage)
		if err != nil {
			return nil, fmt.Errorf("criteria extraction failed: %w", err)
		}
		criteria = extracted
		bb.SetCriteria(criteria)
		o.events.OnCriteriaExtracted(len(criteria), buildEvalCriterionEventsFromCriteria(criteria))
	}

	// Get available tools
	availableTools := o.availableTools()

	return o.runPlanExecute(ctx, userMessage, criteria, availableTools, nil, bb, nil, nil)
}

// Resume continues execution from a previously persisted blackboard state.
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard) (*ExecutionResult, error) {
	plan := bb.GetPlan()
	if plan == nil {
		return nil, errors.New("blackboard has no plan to resume")
	}
	criteria := bb.GetCriteria()
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
		planStepEvents[i] = PlanStepEvent{ID: s.ID, Description: s.Description, Status: status, DependsOn: s.DependsOn}
	}
	o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)

	// Emit criteria if available
	if len(criteria) > 0 {
		o.events.OnCriteriaExtracted(len(criteria), buildEvalCriterionEventsFromCriteria(criteria))
	}

	return o.runPlanExecute(ctx, userMessage, criteria, availableTools, reflections, bb, plan, preCompleted)
}

// runPlanExecute is the core plan-execute-evaluate-reflect loop shared by Execute and Resume.
func (o *Orchestrator) runPlanExecute(
	ctx context.Context,
	userMessage string,
	criteria []Criterion,
	availableTools []tools.ToolDescriptor,
	sessionReflections []Reflection,
	bb Blackboard,
	initialPlan *Plan,
	initialPreCompleted map[string]CompletedStep,
) (*ExecutionResult, error) {
	var currentPlan *Plan
	var preCompleted map[string]CompletedStep

	if initialPlan != nil {
		currentPlan = initialPlan
		preCompleted = initialPreCompleted
	} else {
		// Generate a new plan
		o.events.OnServiceMeta("Creating execution plan...", map[string]any{"phase": "orchestration"})
		plan, err := o.cfg.Planner.Plan(ctx, userMessage, criteria, availableTools, sessionReflections)
		if err != nil {
			return nil, fmt.Errorf("planning failed: %w", err)
		}

		planStepEvents := make([]PlanStepEvent, len(plan.Steps))
		for i, s := range plan.Steps {
			planStepEvents[i] = PlanStepEvent{ID: s.ID, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
		}
		o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)
		bb.SetPlan(plan)
		currentPlan = plan
	}

	// Validate AC mapping (informational only)
	_ = ValidateACMapping(currentPlan, criteria)

	var lastOutput string
	var lastEvalResult *EvalResult
	var stepRetryContext string

	sharedWS := agent.NewSharedWorkspace()

	// Retry loop
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		if attempt > 0 {
			o.events.OnRetry(attempt, o.maxRetries+1)
		}

		// Execute the current plan
		finalOutput, completedSteps, aggErr := o.executePlanWithSteps(ctx, currentPlan, criteria, availableTools, preCompleted, sharedWS, userMessage, stepRetryContext, bb)
		lastOutput = finalOutput
		prevCompletedSteps := completedSteps

		// Handle incomplete plan execution (step failure)
		allExecuted := len(completedSteps) == len(currentPlan.Steps)
		if aggErr != nil && !allExecuted && attempt < o.maxRetries {
			syntheticEval := &EvalResult{
				AllPassed: false,
				Failed: []EvalDetail{{
					Criterion:  Criterion{ID: "execution_incomplete", Description: "Plan execution did not complete"},
					Diagnostic: fmt.Sprintf("FAILED: %d of %d steps executed. Error: %s", len(completedSteps), len(currentPlan.Steps), aggErr),
				}},
			}
			o.events.OnEvaluated(0, 1, buildEvalCriterionEvents(syntheticEval))

			if o.cfg.Reflection != nil {
				o.events.OnServiceMeta("Step execution failed, reflecting for replan...", map[string]any{"phase": "orchestration"})
				syntheticSteps := BuildPlanExecutionSteps(completedSteps, currentPlan)
				reflection, reflectErr := o.cfg.Reflection.Reflect(ctx, syntheticSteps, syntheticEval, currentPlan, sessionReflections)
				if reflectErr == nil && reflection.SuggestedAction != "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					bb.AddReflection(*reflection)
					o.events.OnReflected(reflection.Summary, reflection.Hypotheses, attempt+1, o.maxRetries)

					failedStep := FindFailedStep(completedSteps)
					newPlan, replanErr := o.cfg.Planner.Replan(ctx, currentPlan, completedSteps, failedStep, reflection, criteria, sessionReflections)
					if replanErr == nil {
						currentPlan = newPlan
						bb.SetPlan(currentPlan)
						preCompleted = BuildCarryForward(prevCompletedSteps, newPlan)
						stepRetryContext = ""
						sharedWS.Clear()
						o.emitPlanWithStatuses(currentPlan, preCompleted)
						continue
					}
				}
			}
			continue // retry with same plan if reflection/replan failed
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

		// Evaluate (optional)
		var evalResult *EvalResult
		switch {
		case o.cfg.Evaluation != nil && len(criteria) > 0:
			o.events.OnServiceMeta("Evaluating acceptance criteria...", map[string]any{"phase": "orchestration"})
			er, evalErr := o.cfg.Evaluation.Evaluate(ctx, finalOutput, criteria, bb)
			if evalErr != nil {
				return result, nil //nolint:nilerr // evaluation failure is non-fatal
			}
			evalResult = er
			result.EvalResult = evalResult
			lastEvalResult = evalResult
		case len(criteria) == 0:
			evalResult = &EvalResult{AllPassed: true}
			result.EvalResult = evalResult
			lastEvalResult = evalResult
		default:
			// No evaluator, accept result
			return result, nil
		}

		// Verify (optional): Tier 2 intent verification
		if evalResult.AllPassed && o.cfg.Verification != nil {
			completedMap := make(map[string]CompletedStep, len(completedSteps))
			for _, cs := range completedSteps {
				completedMap[cs.StepID] = cs
			}
			changeSummary := BuildChangeSummary(completedMap, currentPlan)
			verifyResult, verifyErr := o.cfg.Verification.Verify(ctx, userMessage, finalOutput, changeSummary)
			if verifyErr == nil && !verifyResult.Passed {
				evalResult.AllPassed = false
				evalResult.Failed = append(evalResult.Failed, EvalDetail{
					Criterion:  Criterion{ID: "ac_intent", Description: "Implementation matches the user's original intent"},
					Diagnostic: "FAILED:" + verifyResult.Feedback,
				})
			}
		}

		// Emit evaluation
		passed := len(evalResult.Passed)
		total := passed + len(evalResult.Failed) + len(evalResult.Unclear)
		o.events.OnEvaluated(passed, total, buildEvalCriterionEvents(evalResult))

		// Success
		if evalResult.AllPassed {
			return result, nil
		}

		// Failure with retries remaining
		if attempt < o.maxRetries {
			if o.cfg.Reflection != nil {
				syntheticSteps := BuildPlanExecutionSteps(completedSteps, currentPlan)
				o.events.OnServiceMeta("Some acceptance criteria not met, reflecting...", map[string]any{"phase": "orchestration"})
				reflection, reflectErr := o.cfg.Reflection.Reflect(ctx, syntheticSteps, evalResult, currentPlan, sessionReflections)
				if reflectErr != nil {
					continue // retry without reflection guidance
				}
				if reflection.SuggestedAction == "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					bb.AddReflection(*reflection)
					result.Reflections = sessionReflections
					result.Output = finalOutput + "\n\n[Evaluation: some criteria not met: " + formatFailedCriteria(evalResult) + ". Reflector suggests abort.]"
					return result, nil
				}

				o.events.OnReflected(reflection.Summary, reflection.Hypotheses, attempt+1, o.maxRetries)
				sessionReflections = append(sessionReflections, *reflection)
				bb.AddReflection(*reflection)

				if reflection.SuggestedAction == "replan" {
					var failedStep CompletedStep
					if len(completedSteps) > 0 {
						failedStep = completedSteps[len(completedSteps)-1]
					}
					newPlan, replanErr := o.cfg.Planner.Replan(ctx, currentPlan, completedSteps, failedStep, reflection, criteria, sessionReflections)
					if replanErr != nil {
						continue
					}
					currentPlan = newPlan
					bb.SetPlan(currentPlan)
					preCompleted = BuildCarryForward(prevCompletedSteps, newPlan)
					stepRetryContext = ""
					sharedWS.Clear()
					o.emitPlanWithStatuses(currentPlan, preCompleted)
				} else {
					// Step-level retry
					var failedIDs []string
					for _, f := range evalResult.Failed {
						failedIDs = append(failedIDs, f.Criterion.ID)
					}
					retrySet := ComputeRetrySteps(currentPlan, failedIDs)
					if retrySet != nil {
						preCompleted = make(map[string]CompletedStep)
						for _, cs := range prevCompletedSteps {
							if !retrySet[cs.StepID] {
								preCompleted[cs.StepID] = cs
							}
						}
					} else {
						preCompleted = nil
					}
				}
			}
		}
	}

	// Max retries exhausted
	bb.SetFinalResult(lastOutput)
	result := &ExecutionResult{
		Output:       lastOutput,
		Plan:         currentPlan,
		EvalResult:   lastEvalResult,
		Blackboard:   bb,
		AttemptCount: o.maxRetries + 1,
		Reflections:  sessionReflections,
	}
	if lastEvalResult != nil && !lastEvalResult.AllPassed {
		result.Output = lastOutput + "\n\n[Evaluation: some criteria not met after " + strconv.Itoa(o.maxRetries+1) + " attempts: " + formatFailedCriteria(lastEvalResult) + "]"
	}
	return result, nil
}

// executePlanWithSteps runs a DAG plan to completion and returns completed steps.
func (o *Orchestrator) executePlanWithSteps(
	ctx context.Context,
	plan *Plan,
	criteria []Criterion,
	availableTools []tools.ToolDescriptor,
	preCompleted map[string]CompletedStep,
	sharedWS *agent.SharedWorkspace,
	userMessage, retryContext string,
	bb Blackboard,
) (string, []CompletedStep, error) {
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

	for {
		readySteps := FindReadySteps(plan, completedSteps)
		if len(readySteps) == 0 {
			break
		}

		tasks := make([]agent.SubAgentTask, 0, len(readySteps))
		stepStartTimes := make(map[string]time.Time)

		for _, step := range readySteps {
			stepIndex := o.findStepIndex(plan, step.ID)
			o.events.OnServiceMeta(fmt.Sprintf("Executing step %d/%d: %s", stepIndex+1, len(plan.Steps), step.Description), map[string]any{"phase": "orchestration"})
			o.events.OnStepStarted(step.ID, step.Description)
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
			maxSteps := stepCfg.MaxSteps
			if maxSteps == 0 {
				maxSteps = o.maxSteps
			}

			taskDef := o.buildStepTask(step, stepIndex, *plan, criteria, completedSteps, stepTools, bb, userMessage, retryContext, maxSteps)

			// Build system prompt
			var systemPrompt string
			switch {
			case stepCfg.SystemPrompt != "":
				systemPrompt = stepCfg.SystemPrompt
			case o.cfg.SystemPrompt != nil:
				systemPrompt = o.cfg.SystemPrompt(ctx, step.Description, taskDef.criteria)
			default:
				systemPrompt = defaultSystemPrompt(ctx, step.Description, taskDef.criteria)
			}

			// Resolve model metadata
			var modelMeta llm.ModelMetadata
			if o.cfg.ModelRegistry != nil {
				modelMeta, _ = o.cfg.ModelRegistry.Resolve("")
			}

			// Create context manager
			var cm agent.ContextManager
			if o.cfg.ContextFactory != nil {
				cm = o.cfg.ContextFactory(systemPrompt, modelMeta, stepCfg.CompactionStrategy)
			} else {
				return "", completedList, errors.New("ContextFactory is required but not configured")
			}
			
			// Allow consumer to inject task-specific context
			if o.cfg.ContextSetup != nil {
				o.cfg.ContextSetup(cm, taskDef.task, taskDef.criteria)
			}
			
			scopedEvents := o.scopeEvents(step.ID)
			executor := agent.NewExecutor(o.cfg.LLM, o.cfg.Tools, o.cfg.TokenCounter, maxSteps, nil, scopedEvents, true, o.cfg.ToolResultBudget)
			executor.SetPlanContext(step.ID, stepIndex+1, len(plan.Steps))

			tasks = append(tasks, agent.SubAgentTask{
				StepID:    step.ID,
				Executor:  executor,
				CM:        cm,
				TaskTools: taskDef.tools,
				TaskDesc:  taskDef.task,
				Emitter:   scopedEvents,
			})
		}

		results := agent.RunSubAgentsParallel(ctx, tasks)
		var stepFailed bool
		for _, r := range results {
			o.events.OnStepCompleted(r.StepID, r.Error == nil, time.Since(stepStartTimes[r.StepID]))

			if sharedWS != nil && r.Error == nil {
				sharedWS.Store(r.StepID+"/output", r.Output, r.StepID)
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
				stepFailed = true
			}
		}
		if stepFailed {
			break
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

	return AggregateOutput(completedSteps, plan), completedList, aggErr
}

// stepTaskDef holds the result of buildStepTask.
type stepTaskDef struct {
	task     string
	criteria []Criterion
	tools    []tools.ToolDescriptor
}

// buildStepTask creates the task description and criteria for a plan step executor.
func (o *Orchestrator) buildStepTask(
	step PlanStep, stepIndex int, plan Plan,
	allCriteria []Criterion,
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
		switch {
		case i < stepIndex:
			fmt.Fprintf(&b, "  ✓ Step %d: %s\n", i+1, s.Description)
		case i == stepIndex:
			fmt.Fprintf(&b, "  → Step %d: %s (THIS STEP)\n", i+1, s.Description)
		default:
			fmt.Fprintf(&b, "  · Step %d: %s\n", i+1, s.Description)
		}
	}

	// Original user request
	if userMessage != "" {
		b.WriteString("\n## Original User Request\nThe following is the original request from the user. Use it as reference for any data, URLs, files, or other resources mentioned. Do NOT expand your scope beyond this step's objective.\n\n")
		b.WriteString(userMessage)
		b.WriteString("\n")
	}

	// Specific objective
	fmt.Fprintf(&b, "\nYour specific objective: %s\n\n", step.Description)
	b.WriteString("Produce output that is scoped to this step only. Later steps will build on your output.\n")

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
		if len(depContext) > maxDependencyContextChars {
			depContext = depContext[len(depContext)-maxDependencyContextChars:]
			if idx := strings.Index(depContext, "\n- ["); idx >= 0 {
				depContext = depContext[idx:]
			}
		}
		if depContext != "" {
			b.WriteString("\nContext from previous steps:")
			b.WriteString(depContext)
		}
	}

	// Build step criteria from RelevantAC
	var stepCriteria []Criterion
	if len(step.RelevantAC) > 0 && len(allCriteria) > 0 {
		acMap := make(map[string]Criterion)
		for _, c := range allCriteria {
			acMap[c.ID] = c
		}
		for _, acID := range step.RelevantAC {
			if c, ok := acMap[acID]; ok {
				stepCriteria = append(stepCriteria, c)
			}
		}
	}

	// For the last terminal step, inject unmapped ACs
	if o.isLastTerminalStep(step, stepIndex, plan) && len(allCriteria) > 0 {
		unmapped := ValidateACMapping(&plan, allCriteria)
		if len(unmapped) > 0 && len(unmapped) < len(allCriteria) {
			fmt.Fprintf(&b, "\n\nIMPORTANT: The following acceptance criteria are not explicitly assigned to any plan step. As the final step, ensure these are also addressed if possible:\n")
			acMap := make(map[string]Criterion)
			for _, c := range allCriteria {
				acMap[c.ID] = c
			}
			for _, id := range unmapped {
				if c, ok := acMap[id]; ok {
					fmt.Fprintf(&b, "- %s: %s\n", c.ID, c.Description)
					stepCriteria = append(stepCriteria, c)
				}
			}
		}
	}

	// Retry context
	if retryContext != "" {
		fmt.Fprintf(&b, "\n\n## Existing Files From Previous Attempt\n%s\nIMPORTANT: Fix these files IN PLACE. Do NOT create new files with different names.\n", retryContext)
	}

	return stepTaskDef{
		task:     b.String(),
		criteria: stepCriteria,
		tools:    stepTools,
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// scopeEvents returns step-scoped events if the Events implementation supports it.
func (o *Orchestrator) scopeEvents(stepID string) Events {
	if s, ok := o.events.(StepScopable); ok {
		return s.WithStepID(stepID)
	}
	return o.events
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

// isLastTerminalStep checks whether the given step is the last terminal step in plan order.
func (o *Orchestrator) isLastTerminalStep(step PlanStep, stepIndex int, plan Plan) bool {
	// Check if this step is terminal (no other step depends on it)
	isTerminal := true
	for _, s := range plan.Steps {
		for _, dep := range s.DependsOn {
			if dep == step.ID {
				isTerminal = false
				break
			}
		}
		if !isTerminal {
			break
		}
	}
	if !isTerminal {
		return false
	}
	// Check if any later step is also terminal
	for i := stepIndex + 1; i < len(plan.Steps); i++ {
		laterStep := plan.Steps[i]
		laterIsTerminal := true
		for _, s := range plan.Steps {
			for _, dep := range s.DependsOn {
				if dep == laterStep.ID {
					laterIsTerminal = false
					break
				}
			}
			if !laterIsTerminal {
				break
			}
		}
		if laterIsTerminal {
			return false // a later terminal step exists
		}
	}
	return true
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
		planStepEvents[i] = PlanStepEvent{ID: s.ID, Description: s.Description, Status: status, DependsOn: s.DependsOn}
	}
	o.events.OnPlanGenerated(len(plan.Steps), planStepEvents)
}

// defaultSystemPrompt creates a minimal system prompt when no factory is provided.
func defaultSystemPrompt(_ context.Context, stepDescription string, criteria []Criterion) string {
	var b strings.Builder
	b.WriteString("You are an AI assistant executing a task step.\n\n")
	b.WriteString("Your task: " + stepDescription + "\n")
	if len(criteria) > 0 {
		b.WriteString("\nAcceptance Criteria:\n")
		for _, c := range criteria {
			fmt.Fprintf(&b, "- %s: %s\n", c.ID, c.Description)
		}
	}
	return b.String()
}

// buildEvalCriterionEvents converts an EvalResult to a slice of EvalCriterionEvent.
func buildEvalCriterionEvents(evalResult *EvalResult) []EvalCriterionEvent {
	events := make([]EvalCriterionEvent, 0, len(evalResult.Passed)+len(evalResult.Failed)+len(evalResult.Unclear))
	for _, d := range evalResult.Passed {
		events = append(events, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: true, Status: "pass", Diagnostic: d.Diagnostic})
	}
	for _, d := range evalResult.Failed {
		events = append(events, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false, Status: "fail", Diagnostic: d.Diagnostic})
	}
	for _, d := range evalResult.Unclear {
		events = append(events, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false, Status: "unclear", Diagnostic: d.Diagnostic})
	}
	return events
}

// buildEvalCriterionEventsFromCriteria converts criteria to EvalCriterionEvent for emission.
func buildEvalCriterionEventsFromCriteria(criteria []Criterion) []EvalCriterionEvent {
	events := make([]EvalCriterionEvent, len(criteria))
	for i, c := range criteria {
		events[i] = EvalCriterionEvent{Name: c.ID, Description: c.Description}
	}
	return events
}

// formatFailedCriteria extracts failed criteria descriptions.
func formatFailedCriteria(evalResult *EvalResult) string {
	failed := make([]string, 0, len(evalResult.Failed))
	for _, f := range evalResult.Failed {
		failed = append(failed, f.Criterion.Description)
	}
	return strings.Join(failed, ", ")
}
