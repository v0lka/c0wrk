// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxSteps   int
	KeepFirst  int // for sliding window compaction
	KeepLast   int // for sliding window compaction
	MaxRetries int // max retry attempts after failed evaluation (default: 3)
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager

// Orchestrator coordinates the agent's reasoning cycle.
// Phase 2: Full implementation with Router, Planner, Evaluator.
// Phase 3: Retry-loop with Reflector.
type Orchestrator struct {
	router              *Router
	acExtractor         *ACExtractor
	planner             *Planner
	evaluator           *Evaluator
	reflector           *Reflector // Phase 3: for self-correction
	llm                 LLMCaller
	tools               ToolExecutor
	toolRegistry        *tools.ToolRegistry
	tokenCounter        llm.TokenCounter
	modelRegistry       *llm.ModelRegistry // NEW: for resolving model metadata
	config              OrchestratorConfig
	contextFactory      ContextManagerFactory
	maxRetries          int           // max retry attempts (from config or default 3)
	conversationHistory []llm.Message // accumulated conversation for router context
	logger              *slog.Logger  // structured logger (nil-safe)
	emitter             Emitter       // event emitter (nil-safe, uses noopEmitter if nil)
	toolResultBudget    ToolResultBudget
	intentVerifier      *IntentVerifier // optional Tier 2 intent verifier (nil-safe)
}

// NewOrchestrator creates a new Orchestrator with all Phase 2 components.
// reflector, logger, emitter, and intentVerifier are optional (nil-safe) for Phase 3 features.
func NewOrchestrator(
	router *Router,
	acExtractor *ACExtractor,
	planner *Planner,
	evaluator *Evaluator,
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
	intentVerifier *IntentVerifier, // optional, nil-safe
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
	maxRetries := cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2 // default: 3 total attempts per mode
	}
	// Use noopEmitter if nil to avoid nil checks throughout the code
	if emitter == nil {
		emitter = &noopEmitter{}
	}
	return &Orchestrator{
		router:           router,
		acExtractor:      acExtractor,
		planner:          planner,
		evaluator:        evaluator,
		reflector:        reflector,
		llm:              llmCaller,
		tools:            toolExec,
		toolRegistry:     toolReg,
		tokenCounter:     counter,
		modelRegistry:    modelRegistry,
		config:           cfg,
		contextFactory:   contextFactory,
		maxRetries:       maxRetries,
		logger:           logger,
		emitter:          emitter,
		toolResultBudget: toolResultBudget,
		intentVerifier:   intentVerifier,
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

// logWarn logs a WARN level message if logger is not nil.
func (o *Orchestrator) logWarn(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Warn(msg, args...)
	}
}

// Handle executes the agent reasoning cycle for the given user message.
// This is the main entry point for Phase 2.
// Returns a HandleResult with rich output including routing decision, plan, and evaluation.
func (o *Orchestrator) Handle(ctx context.Context, userMessage string) (*HandleResult, error) {
	// 0. Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	{
		var effectiveMax int
		if o.modelRegistry != nil {
			meta := o.modelRegistry.Resolve("")
			if meta.ContextWindow > 0 {
				safetyMargin := meta.ContextWindow * 5 / 100
				effectiveMax = meta.ContextWindow - meta.OutputLimit - safetyMargin
			}
		}
		o.emitter.ContextFill(0, 0, effectiveMax, "ok", "")
	}

	// 1. Get available tools
	availableTools := o.toolRegistry.List()

	// 2. Extract raw acceptance criteria (Phase 1 — before routing)
	o.emitter.ServiceWithMeta("Extracting acceptance criteria...", map[string]any{"phase": "orchestration"})
	rawCriteria, err := o.acExtractor.ExtractRaw(ctx, userMessage)
	if err != nil {
		// Non-fatal: proceed with empty criteria if extraction fails
		o.logWarn("raw AC extraction failed, proceeding without criteria", "error", err)
		rawCriteria = nil
	}

	// 3. Route the request (with raw criteria for informed routing)
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})
	routing, err := o.router.Route(ctx, userMessage, rawCriteria, availableTools, o.conversationHistory)
	if err != nil {
		return nil, fmt.Errorf("routing failed: %w", err)
	}

	// Emit routing decision
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// 4. Enrich acceptance criteria with domain context (Phase 2 — after routing)
	o.emitter.ServiceWithMeta("Enriching acceptance criteria...", map[string]any{"phase": "orchestration"})
	var ac []AcceptanceCriterion
	enrichedAC, enrichErr := o.acExtractor.Enrich(ctx, rawCriteria, routing)
	if enrichErr != nil {
		o.logWarn("AC enrichment failed, proceeding without criteria", "error", enrichErr)
	} else {
		ac = enrichedAC
	}

	// Emit AC extraction with criteria details
	acEvents := make([]EvalCriterionEvent, len(ac))
	for i, c := range ac {
		acEvents[i] = EvalCriterionEvent{Name: c.ID, Description: c.Description}
	}
	o.emitter.ACExtracted(len(ac), acEvents)
	o.logInfo("ac_extracted", "count", len(ac))
	o.logDebug("acceptance_criteria", "criteria", ac)

	// 5. Handle execution — always use Plan & Execute
	var result *HandleResult
	if routing.NeedsClarification {
		result = &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
		}
	} else {
		result, err = o.handlePlanExecute(ctx, userMessage, routing, availableTools, nil, ac)
	}

	if err != nil {
		return nil, err
	}

	// 7. Accumulate conversation history for future routing context
	if result != nil {
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "user", Content: userMessage},
			llm.Message{Role: "assistant", Content: result.Output},
		)
		const maxHistoryMessages = 20
		if len(o.conversationHistory) > maxHistoryMessages {
			o.conversationHistory = o.conversationHistory[len(o.conversationHistory)-maxHistoryMessages:]
		}
	}

	return result, nil
}

// buildEvalCriterionEvents converts EvalResult into EvalCriterionEvent slice.
func (o *Orchestrator) buildEvalCriterionEvents(evalResult *EvalResult) []EvalCriterionEvent {
	criteria := make([]EvalCriterionEvent, 0, len(evalResult.Passed)+len(evalResult.Failed)+len(evalResult.Unclear))
	for _, d := range evalResult.Passed {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: true, Status: "pass", Diagnostic: d.Diagnostic})
	}
	for _, d := range evalResult.Failed {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false, Status: "fail", Diagnostic: d.Diagnostic})
	}
	for _, d := range evalResult.Unclear {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false, Status: "unclear", Diagnostic: d.Diagnostic})
	}
	return criteria
}

// formatFailedCriteria extracts failed criteria descriptions.
func (o *Orchestrator) formatFailedCriteria(evalResult *EvalResult) string {
	failedCriteria := make([]string, 0, len(evalResult.Failed))
	for _, f := range evalResult.Failed {
		failedCriteria = append(failedCriteria, f.Criterion.Description)
	}
	return strings.Join(failedCriteria, ", ")
}

// resolveProfile returns the effective AgentProfile for a plan step,
// filling in defaults from the Orchestrator config.
func (o *Orchestrator) resolveProfile(step PlanStep) AgentProfile {
	if step.AgentProfile != nil {
		profile := *step.AgentProfile
		if profile.MaxSteps == 0 {
			profile.MaxSteps = o.config.MaxSteps
		}
		return profile
	}
	return AgentProfile{
		Role:     "executor",
		MaxSteps: o.config.MaxSteps,
	}
}

// filterToolsByProfile returns the subset of tools allowed by the profile.
// If AllowedTools is empty, all tools are returned.
func (o *Orchestrator) filterToolsByProfile(allTools []tools.ToolDescriptor, profile AgentProfile) []tools.ToolDescriptor {
	if len(profile.AllowedTools) == 0 {
		return allTools
	}
	allowed := make(map[string]bool, len(profile.AllowedTools))
	for _, name := range profile.AllowedTools {
		allowed[name] = true
	}
	var filtered []tools.ToolDescriptor
	for _, t := range allTools {
		if allowed[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// handlePlanExecute handles plan_execute mode - DAG planning and execution with retry.
func (o *Orchestrator) handlePlanExecute(ctx context.Context, userMessage string, routing *RoutingDecision, availableTools []tools.ToolDescriptor, escalatedReflections []Reflection, ac []AcceptanceCriterion) (*HandleResult, error) {
	// Initialize session reflections from escalated reflections (if any)
	var sessionReflections []Reflection
	if len(escalatedReflections) > 0 {
		sessionReflections = make([]Reflection, len(escalatedReflections))
		copy(sessionReflections, escalatedReflections)
	}

	// 2. Generate initial plan
	o.emitter.ServiceWithMeta("Creating execution plan...", map[string]any{"phase": "orchestration"})
	plan, err := o.planner.Plan(ctx, userMessage, ac, availableTools, sessionReflections)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	// Emit plan generation
	planStepEvents := make([]PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planStepEvents[i] = PlanStepEvent{ID: s.ID, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
	}
	o.emitter.PlanGenerated(len(plan.Steps), planStepEvents)
	o.logInfo("plan_generated", "step_count", len(plan.Steps))
	o.logDebug("plan_steps", "steps", plan.Steps)

	// Validate AC-to-step mapping
	if unmappedAC := validateACMapping(plan, ac); len(unmappedAC) > 0 {
		o.logWarn("unmapped acceptance criteria detected", "unmapped_ac", unmappedAC, "hint", "these criteria are not assigned to any plan step's RelevantAC")
	}

	// Track state for retry loop
	var lastOutput string
	var lastEvalResult *EvalResult
	var currentPlan = plan
	var preCompleted map[string]CompletedStep
	var stepRetryContext string // workspace change summary for step-level retries

	// Create shared workspace for inter-agent communication
	sharedWS := NewSharedWorkspace()

	// 3. Retry loop
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		// Emit retry attempt (skip for first attempt)
		if attempt > 0 {
			o.emitter.Retry(attempt, o.maxRetries+1)
			o.logInfo("retry", "attempt", attempt, "max_attempts", o.maxRetries+1)
		}

		// Execute the current plan
		finalOutput, completedSteps, aggErr := o.executePlanWithSteps(ctx, currentPlan, ac, routing, availableTools, sessionReflections, preCompleted, sharedWS, userMessage, stepRetryContext)
		// Note: step execution errors are handled within executePlanWithSteps
		lastOutput = finalOutput
		prevCompletedSteps := completedSteps

		// --- Handle incomplete plan execution (step failure) ---
		allExecuted := len(completedSteps) == len(currentPlan.Steps)
		if aggErr != nil && !allExecuted && attempt < o.maxRetries {
			o.logWarn("plan_execution_incomplete",
				"completed", len(completedSteps),
				"total", len(currentPlan.Steps),
				"error", aggErr)

			// Synthesize failure for reflection
			syntheticEval := &EvalResult{
				AllPassed: false,
				Failed: []EvalDetail{{
					Criterion:  AcceptanceCriterion{ID: "execution_incomplete", Description: "Plan execution did not complete"},
					Diagnostic: fmt.Sprintf("FAILED: %d of %d steps executed. Error: %s", len(completedSteps), len(currentPlan.Steps), aggErr),
				}},
			}
			syntheticSteps := buildPlanExecutionSteps(completedSteps, currentPlan)

			// Emit evaluation showing incomplete state
			o.emitter.Evaluation(0, 1, o.buildEvalCriterionEvents(syntheticEval))

			// Reflect and force replan
			if o.reflector != nil {
				o.emitter.ServiceWithMeta("Step execution failed, reflecting for replan...", map[string]any{"phase": "orchestration"})
				reflection, reflectErr := o.reflector.Reflect(ctx, syntheticSteps, syntheticEval, currentPlan, sessionReflections)
				if reflectErr == nil && reflection.SuggestedAction != "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					o.emitter.Reflection(reflection.Summary, reflection.Hypotheses, attempt+1, o.maxRetries)

					// Force replan — step failure suggests plan decomposition issue
					newPlan, replanErr := o.planner.Replan(ctx, currentPlan, completedSteps, findFailedStep(completedSteps), reflection, ac, sessionReflections)
					if replanErr == nil {
						currentPlan = newPlan
						preCompleted = buildCarryForward(prevCompletedSteps, newPlan)
						stepRetryContext = ""
						sharedWS.Clear()

						// Re-emit new plan
						planStepEvents := make([]PlanStepEvent, len(currentPlan.Steps))
						for i, s := range currentPlan.Steps {
							status := "pending"
							if preCompleted != nil {
								if _, ok := preCompleted[s.ID]; ok {
									status = "completed"
								}
							}
							planStepEvents[i] = PlanStepEvent{ID: s.ID, Description: s.Description, Status: status, DependsOn: s.DependsOn}
						}
						o.emitter.PlanGenerated(len(currentPlan.Steps), planStepEvents)

						if unmappedAC := validateACMapping(currentPlan, ac); len(unmappedAC) > 0 {
							o.logWarn("unmapped acceptance criteria in replanned plan", "unmapped_ac", unmappedAC)
						}
						continue // next retry attempt with new plan
					}
					o.logWarn("replan failed after step failure, retrying with current plan", "error", replanErr)
				} else if reflectErr != nil {
					o.logWarn("reflection failed after step failure, retrying without guidance", "error", reflectErr)
				}
			}
			continue // retry with same plan if reflection/replan failed
		}
		// --- END incomplete execution handling ---

		// Safety net: warn if evaluation is reached with incomplete execution
		if len(completedSteps) < len(currentPlan.Steps) {
			o.logWarn("evaluation_on_incomplete_plan",
				"completed", len(completedSteps),
				"total", len(currentPlan.Steps),
				"hint", "this should have been caught by incomplete execution handler above")
		}

		handleResult := &HandleResult{
			Output:          finalOutput,
			RoutingDecision: routing,
			Plan:            currentPlan,
			AttemptCount:    attempt + 1,
			Reflections:     sessionReflections,
		}

		// Final evaluation
		var evalResult *EvalResult
		syntheticSteps := buildPlanExecutionSteps(completedSteps, currentPlan)

		if len(ac) == 0 {
			// No Tier 1 criteria — synthesize empty passing result so Tier 2 still runs
			evalResult = &EvalResult{AllPassed: true}
			handleResult.EvalResult = evalResult
			lastEvalResult = evalResult
		} else {
			o.emitter.ServiceWithMeta("Evaluating acceptance criteria...", map[string]any{"phase": "orchestration"})
			er, evalErr := o.evaluator.Evaluate(ctx, finalOutput, ac, syntheticSteps)
			if evalErr != nil {
				o.logWarn("evaluation failed, returning result without evaluation", "error", evalErr)
				o.emitter.ServiceWithMeta("Evaluation failed: "+evalErr.Error(), map[string]any{"phase": "orchestration", "severity": "warning"})
				//nolint:nilerr // evaluation failure is non-fatal; return best-effort result without eval
				return handleResult, nil
			}
			evalResult = er
			handleResult.EvalResult = evalResult
			lastEvalResult = evalResult
		}

		// Define intent criterion (always present)
		intentAC := AcceptanceCriterion{
			ID:          "ac_intent",
			Description: "Implementation matches the user's original intent",
			CheckType:   "intent_verification",
		}

		// Run Tier 2 only if Tier 1 passed and intent verifier is available
		if evalResult.AllPassed && o.intentVerifier != nil {
			completedMap := make(map[string]CompletedStep, len(completedSteps))
			for _, cs := range completedSteps {
				completedMap[cs.StepID] = cs
			}
			changeSummary := buildChangeSummary(completedMap, currentPlan)
			intentResult, intentErr := o.intentVerifier.Verify(
				ctx, userMessage, finalOutput, changeSummary,
			)

			intentDetail := EvalDetail{Criterion: intentAC}
			switch {
			case intentErr != nil:
				intentDetail.Diagnostic = "UNCLEAR:intent verification error: " + intentErr.Error()
				evalResult.Unclear = append(evalResult.Unclear, intentDetail)
			case intentResult.Passed:
				intentDetail.Diagnostic = "PASSED:" + intentResult.Feedback
				evalResult.Passed = append(evalResult.Passed, intentDetail)
			default:
				intentDetail.Diagnostic = "FAILED:" + intentResult.Feedback
				evalResult.Failed = append(evalResult.Failed, intentDetail)
				evalResult.AllPassed = false
			}
		} else if !evalResult.AllPassed {
			// Tier 1 failed, skip intent verification
			evalResult.Unclear = append(evalResult.Unclear, EvalDetail{
				Criterion:  intentAC,
				Diagnostic: "SKIPPED:tier 1 criteria not passed",
			})
		}

		// Emit evaluation result (including intent criterion)
		passed := len(evalResult.Passed)
		total := passed + len(evalResult.Failed) + len(evalResult.Unclear)
		o.emitter.Evaluation(passed, total, o.buildEvalCriterionEvents(evalResult))
		o.logInfo("evaluation", "passed", passed, "total", total, "all_passed", evalResult.AllPassed)
		o.logDebug("evaluation_details", "passed", evalResult.Passed, "failed", evalResult.Failed, "unclear", evalResult.Unclear)

		// Success - all criteria passed
		if evalResult.AllPassed {
			return handleResult, nil
		}

		// Failure - check if we should retry (eval failure)
		if attempt < o.maxRetries {
			if o.reflector != nil {
				// For plan_execute, provide synthetic execution trajectory from completed steps
				o.emitter.ServiceWithMeta("Some acceptance criteria not met, reflecting...", map[string]any{"phase": "orchestration"})
				reflection, reflectErr := o.reflector.Reflect(ctx, syntheticSteps, evalResult, currentPlan, sessionReflections)
				if reflectErr != nil {
					// Reflection failed — still continue retry without reflection guidance
					o.logWarn("reflection failed in plan_execute, retrying without guidance", "error", reflectErr, "attempt", attempt+1)
					continue
				}

				// Check if reflector suggests abort
				if reflection.SuggestedAction == "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					handleResult.Reflections = sessionReflections
					failedCriteria := o.formatFailedCriteria(evalResult)
					handleResult.Output = finalOutput + "\n\n[Evaluation: some criteria not met: " + failedCriteria + ". Reflector suggests abort.]"
					return handleResult, nil
				}

				// Emit reflection
				o.emitter.Reflection(reflection.Summary, reflection.Hypotheses, attempt+1, o.maxRetries)
				o.logInfo("reflection", "summary", reflection.Summary, "suggested_action", reflection.SuggestedAction)
				o.logDebug("reflection_details", "hypotheses", reflection.Hypotheses, "reasoning", reflection.Reasoning, "failure_analysis", reflection.FailureAnalysis, "root_cause", reflection.RootCause, "action_plan", reflection.ActionPlan)

				sessionReflections = append(sessionReflections, *reflection)

				// Replan if suggested, otherwise retry with same plan
				if reflection.SuggestedAction == "replan" {
					// Find failed step (for replan context) - use first completed step as proxy
					var failedStep CompletedStep
					if len(completedSteps) > 0 {
						failedStep = completedSteps[len(completedSteps)-1]
					}

					newPlan, replanErr := o.planner.Replan(ctx, currentPlan, completedSteps, failedStep, reflection, ac, sessionReflections)
					if replanErr != nil {
						// Replan failed, continue with current plan
						continue
					}
					currentPlan = newPlan
					// Carry forward completed step results that the new plan preserves (by ID).
					preCompleted = buildCarryForward(prevCompletedSteps, newPlan)
					stepRetryContext = "" // reset retry context on full replan
					sharedWS.Clear()
					// Validate AC mapping in new plan
					if unmappedAC := validateACMapping(currentPlan, ac); len(unmappedAC) > 0 {
						o.logWarn("unmapped acceptance criteria in replanned plan", "unmapped_ac", unmappedAC)
					}
				} else {
					// Step-level retry: only re-execute steps responsible for failed criteria
					var failedIDs []string
					for _, f := range evalResult.Failed {
						failedIDs = append(failedIDs, f.Criterion.ID)
					}
					retrySet := computeRetrySteps(currentPlan, failedIDs)
					if retrySet != nil {
						// Build preCompleted from previous results, excluding retry steps
						preCompleted = make(map[string]CompletedStep)
						for _, cs := range prevCompletedSteps {
							if !retrySet[cs.StepID] {
								preCompleted[cs.StepID] = cs
							}
						}
						o.logInfo("step_level_retry", "retry_steps", len(retrySet), "preserved_steps", len(preCompleted))
					} else {
						// No criteria-to-step mapping — fall back to full retry
						preCompleted = nil
						o.logInfo("step_level_retry_fallback", "reason", "no RelevantAC mapping found")
					}
				}
			} else {
				o.logWarn("reflector not available, retrying plan without reflection", "attempt", attempt+1)
			}
		}
	}

	// Max retries exhausted
	handleResult := &HandleResult{
		Output:          lastOutput,
		RoutingDecision: routing,
		Plan:            currentPlan,
		EvalResult:      lastEvalResult,
		AttemptCount:    o.maxRetries + 1,
		Reflections:     sessionReflections,
	}

	if lastEvalResult != nil && !lastEvalResult.AllPassed {
		failedCriteria := o.formatFailedCriteria(lastEvalResult)
		handleResult.Output = lastOutput + "\n\n[Evaluation: some criteria not met after " + strconv.Itoa(o.maxRetries+1) + " attempts: " + failedCriteria + "]"
	}

	return handleResult, nil
}

// executePlanWithSteps executes a DAG plan and returns completed steps for replan.
func (o *Orchestrator) executePlanWithSteps(ctx context.Context, plan *Plan, ac []AcceptanceCriterion, routing *RoutingDecision, availableTools []tools.ToolDescriptor, sessionReflections []Reflection, preCompleted map[string]CompletedStep, sharedWS *SharedWorkspace, userMessage, retryContext string) (string, []CompletedStep, error) {
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
		// Find steps that are ready to execute (all dependencies completed)
		readySteps := o.findReadySteps(plan, completedSteps)
		if len(readySteps) == 0 {
			if len(completedSteps) < len(plan.Steps) {
				var stuckSteps []string
				var stuckReasons []string
				for _, s := range plan.Steps {
					if _, done := completedSteps[s.ID]; !done {
						blockedBy := "unknown"
						for _, depID := range s.DependsOn {
							if cs, ok := completedSteps[depID]; ok && cs.Error != nil {
								blockedBy = fmt.Sprintf("dependency %s failed", depID)
								break
							}
							if _, ok := completedSteps[depID]; !ok {
								blockedBy = fmt.Sprintf("dependency %s not executed", depID)
								break
							}
						}
						stuckSteps = append(stuckSteps, s.ID)
						stuckReasons = append(stuckReasons, s.ID+": "+blockedBy)
					}
				}
				o.logWarn("plan_execution_stuck",
					"completed", len(completedSteps),
					"total", len(plan.Steps),
					"stuck_steps", stuckSteps,
					"reasons", stuckReasons)
			}
			break
		}

		// Run steps via SubAgents (works for both single and parallel execution)
		tasks := make([]agent.SubAgentTask, 0, len(readySteps))

		// Emit PlanStepStart for all steps before launching
		stepStartTimes := make(map[string]time.Time)
		for _, step := range readySteps {
			stepIndex := 0
			for j, s := range plan.Steps {
				if s.ID == step.ID {
					stepIndex = j
					break
				}
			}
			o.emitter.ServiceWithMeta(fmt.Sprintf("Executing step %d/%d: %s", stepIndex+1, len(plan.Steps), step.Description), map[string]any{"phase": "orchestration"})
			o.emitter.PlanStepStart(step.ID, step.Description)
			stepStartTimes[step.ID] = time.Now()
		}

		for _, step := range readySteps {
			// Find the step index in the plan
			stepIndex := -1
			for i, s := range plan.Steps {
				if s.ID == step.ID {
					stepIndex = i
					break
				}
			}

			// Resolve profile and filter tools
			profile := o.resolveProfile(step)
			stepTools := o.filterToolsByProfile(availableTools, profile)

			// Resolve max steps budget from profile (fallback to orchestrator config)
			maxSteps := profile.MaxSteps
			if maxSteps == 0 {
				maxSteps = o.config.MaxSteps
			}

			taskDef := o.buildStepTask(step, stepIndex, *plan, ac, completedSteps, stepTools, sharedWS, userMessage, retryContext, maxSteps)

			// Use profile-specific system prompt if provided
			var systemPrompt string
			if profile.SystemPrompt != "" {
				systemPrompt = profile.SystemPrompt
			} else {
				systemPrompt = o.buildSystemPrompt(ctx, step.Description, ac)
			}

			// Resolve model metadata for the profile's LLM role
			var modelMeta llm.ModelMetadata
			if o.modelRegistry != nil {
				modelMeta = o.modelRegistry.Resolve("")
			}

			// Resolve compaction strategy from step domain (fallback to routing domain)
			stepDomain := "general"
			if profile.Domain != "" {
				stepDomain = profile.Domain
			}
			stepCompactionStrategy := applyCompactionStrategy(stepDomain, 3) // default complexity for step-level

			cm := o.contextFactory(systemPrompt, modelMeta, stepCompactionStrategy)

			// Set the task and acceptance criteria into the context window
			cm.SetTask(taskDef.Task, taskDef.Criteria)
			// Scope emitter to plan step for event association
			scopedEmitter := scopeEmitterToStep(o.emitter, step.ID)
			executor := agent.NewExecutor(o.llm, o.tools, o.tokenCounter, maxSteps, o.logger, scopedEmitter, true, o.toolResultBudget)
			executor.SetPlanContext(step.ID, stepIndex+1, len(plan.Steps))

			tasks = append(tasks, agent.SubAgentTask{
				StepID:    step.ID,
				Executor:  executor,
				CM:        cm,
				TaskTools: taskDef.Tools,
				TaskDesc:  taskDef.Task,
				Emitter:   scopedEmitter,
			})
		}

		results := agent.RunSubAgentsParallel(ctx, tasks)
		var stepFailed bool
		for _, r := range results {
			// Emit PlanStepComplete for each result
			o.emitter.PlanStepComplete(r.StepID, r.Error == nil, time.Since(stepStartTimes[r.StepID]))

			// Store output in workspace
			if sharedWS != nil && r.Error == nil {
				sharedWS.Store(r.StepID+"/output", r.Output, r.StepID)
			}

			cs := CompletedStep(r)
			completedSteps[r.StepID] = cs
			completedList = append(completedList, cs)
			if r.Error != nil {
				stepFailed = true
			}
		}
		if stepFailed {
			var skippedIDs []string
			for _, s := range plan.Steps {
				if _, done := completedSteps[s.ID]; !done {
					skippedIDs = append(skippedIDs, s.ID)
				}
			}
			o.logWarn("plan_execution_aborted", "reason", "step_failure", "skipped_steps", skippedIDs)
			break
		}
	}

	// Check for any step errors and return aggregated error
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

	// Aggregate all completed step outputs
	return o.aggregateOutput(completedSteps, plan), completedList, aggErr
}

// findReadySteps returns steps whose dependencies are all completed successfully.
// Steps with failed dependencies are not considered ready.
func (o *Orchestrator) findReadySteps(plan *Plan, completed map[string]CompletedStep) []PlanStep {
	ready := []PlanStep{}
	for _, step := range plan.Steps {
		// Skip if already completed
		if _, done := completed[step.ID]; done {
			continue
		}

		// Check if all dependencies are completed successfully
		allDepsComplete := true
		for _, depID := range step.DependsOn {
			cs, done := completed[depID]
			if !done || cs.Error != nil {
				allDepsComplete = false
				break
			}
		}

		if allDepsComplete {
			ready = append(ready, step)
		}
	}
	return ready
}

// buildCriteriaToStepsMap builds a reverse mapping from AcceptanceCriterion ID to the
// plan step IDs that declared that criterion in their RelevantAC field.
func buildCriteriaToStepsMap(plan *Plan) map[string][]string {
	m := make(map[string][]string)
	for _, step := range plan.Steps {
		for _, acID := range step.RelevantAC {
			m[acID] = append(m[acID], step.ID)
		}
	}
	return m
}

// computeRetrySteps determines the minimal set of step IDs that need re-execution
// given a list of failed acceptance-criteria IDs. It maps failed criteria back to
// their responsible steps (via PlanStep.RelevantAC). Returns nil if no criteria
// map to any step, signaling the caller should fall back to full plan retry.
// Downstream dependents are NOT expanded — they keep their successful output.
// If the retry changes a dependency's output, downstream mismatches will be
// caught by the next evaluation cycle.
func computeRetrySteps(plan *Plan, failedCriteriaIDs []string) map[string]bool {
	acMap := buildCriteriaToStepsMap(plan)

	retrySet := make(map[string]bool)
	for _, acID := range failedCriteriaIDs {
		for _, stepID := range acMap[acID] {
			retrySet[stepID] = true
		}
	}

	// No mapping found — caller should fall back to full retry
	if len(retrySet) == 0 {
		return nil
	}

	return retrySet
}

// buildCarryForward maps previously completed step outputs to the new plan.
// Steps whose IDs appear in the new plan AND completed without error are
// candidates for carry-forward. Steps whose dependencies (in the new plan)
// include a step that is NOT carried forward are transitively excluded,
// ensuring that a replanned step invalidates all its downstream dependents.
// Returns nil if no steps can be preserved (triggering full re-execution).
// findFailedStep returns the first CompletedStep with an error from the list.
func findFailedStep(completedSteps []CompletedStep) CompletedStep {
	for _, cs := range completedSteps {
		if cs.Error != nil {
			return cs
		}
	}
	return CompletedStep{}
}

func buildCarryForward(completed []CompletedStep, newPlan *Plan) map[string]CompletedStep {
	newStepIDs := make(map[string]bool, len(newPlan.Steps))
	for _, s := range newPlan.Steps {
		newStepIDs[s.ID] = true
	}

	// Phase 1: collect candidates — old steps that match new plan IDs and had no error.
	carried := make(map[string]CompletedStep)
	for _, cs := range completed {
		if newStepIDs[cs.StepID] && cs.Error == nil {
			carried[cs.StepID] = cs
		}
	}

	// Phase 2: transitively remove steps whose dependencies won't be carried forward.
	// If a step depends on a new-plan step that is NOT in `carried`, the dependency
	// will be re-executed, so this step's prior output is stale and must be re-run.
	changed := true
	for changed {
		changed = false
		for _, s := range newPlan.Steps {
			if _, ok := carried[s.ID]; !ok {
				continue // not a candidate
			}
			for _, depID := range s.DependsOn {
				if newStepIDs[depID] && carried[depID].StepID == "" {
					// Dependency exists in new plan but is not carried forward
					delete(carried, s.ID)
					changed = true
					break
				}
			}
		}
	}

	if len(carried) == 0 {
		return nil
	}
	return carried
}

// buildPlanExecutionSteps converts completed plan steps into an execution
// trajectory ([]Step) for the Reflector and Evaluator. When a CompletedStep
// carries the actual executor steps (tool calls + observations), those are
// used directly so the evaluator sees real evidence. Otherwise, a synthetic
// summary step is created as a backward-compatible fallback.
func buildPlanExecutionSteps(completedList []CompletedStep, plan *Plan) []Step {
	// Build a map from step ID to plan step description
	stepDescriptions := make(map[string]string)
	for _, ps := range plan.Steps {
		stepDescriptions[ps.ID] = ps.Description
	}

	var steps []Step
	for _, cs := range completedList {
		if len(cs.Steps) > 0 {
			// Use actual executor steps (preserves tool calls + observations)
			steps = append(steps, cs.Steps...)
		} else {
			// Fallback: synthetic summary (backward compat)
			desc := stepDescriptions[cs.StepID]
			step := Step{
				Thought: fmt.Sprintf("Executing plan step %s: %s", cs.StepID, desc),
			}
			if cs.Error != nil {
				step.Observation = fmt.Sprintf("STEP FAILED: %s\nOutput: %s", cs.Error.Error(), cs.Output)
			} else {
				step.Observation = cs.Output
			}
			steps = append(steps, step)
		}
	}
	return steps
}

// validateACMapping checks that every acceptance criterion is covered by at least
// one plan step's RelevantAC field. Returns the list of unmapped AC IDs.
func validateACMapping(plan *Plan, ac []AcceptanceCriterion) []string {
	covered := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, acID := range step.RelevantAC {
			covered[acID] = true
		}
	}

	var unmapped []string
	for _, criterion := range ac {
		if !covered[criterion.ID] {
			unmapped = append(unmapped, criterion.ID)
		}
	}
	return unmapped
}

// buildStepTask creates a TaskDefinition for a plan step.
// stepIndex is 0-based index of this step in the plan.
// retryContext is an optional string with workspace state from a previous attempt (empty on first attempt).
func (o *Orchestrator) buildStepTask(step PlanStep, stepIndex int, plan Plan, allAC []AcceptanceCriterion, completedSteps map[string]CompletedStep, availableTools []tools.ToolDescriptor, sharedWS *SharedWorkspace, userMessage, retryContext string, maxSteps int) TaskDefinition {
	// Build task description with scoping context
	var taskBuilder strings.Builder

	// Step position header
	fmt.Fprintf(&taskBuilder, "[Step %d of %d] Your task: %s\n\n", stepIndex+1, len(plan.Steps), step.Description)

	// Budget awareness: tell the executor how many iterations it has
	fmt.Fprintf(&taskBuilder, "Tool call budget: %d iterations. Plan your approach to finish within this budget.\n\n", maxSteps)

	// Explicit scoping instruction
	taskBuilder.WriteString("IMPORTANT: You are executing ONE step in a multi-step plan. Complete ONLY this step's objective. Do NOT produce final deliverables or perform work belonging to other steps.\n\n")

	// Plan overview with "YOU ARE HERE" marker
	taskBuilder.WriteString("Plan overview:\n")
	for i, s := range plan.Steps {
		switch {
		case i < stepIndex:
			// Completed step
			fmt.Fprintf(&taskBuilder, "  ✓ Step %d: %s\n", i+1, s.Description)
		case i == stepIndex:
			// Current step
			fmt.Fprintf(&taskBuilder, "  → Step %d: %s (THIS STEP)\n", i+1, s.Description)
		default:
			// Future step
			fmt.Fprintf(&taskBuilder, "  · Step %d: %s\n", i+1, s.Description)
		}
	}

	// Original user request (read-only reference for data, URLs, files, etc.)
	if userMessage != "" {
		taskBuilder.WriteString("\n## Original User Request\nThe following is the original request from the user. Use it as reference for any data, URLs, files, or other resources mentioned. Do NOT expand your scope beyond this step's objective.\n\n")
		taskBuilder.WriteString(userMessage)
		taskBuilder.WriteString("\n")
	}

	// Specific objective
	fmt.Fprintf(&taskBuilder, "\nYour specific objective: %s\n\n", step.Description)
	taskBuilder.WriteString("Produce output that is scoped to this step only. Later steps will build on your output.\n")

	// Context from previous steps
	if len(step.DependsOn) > 0 {
		taskBuilder.WriteString("\nContext from previous steps:")
		for _, depID := range step.DependsOn {
			if completed, ok := completedSteps[depID]; ok {
				fmt.Fprintf(&taskBuilder, "\n- [%s]: %s", depID, completed.Output)
			}
		}
	}

	// Inject workspace artifacts from completed dependencies
	if sharedWS != nil {
		var artifactContext strings.Builder
		for _, depID := range step.DependsOn {
			artifacts := sharedWS.GetByProducer(depID)
			for _, a := range artifacts {
				fmt.Fprintf(&artifactContext, "\n\n--- Artifact from %s ---\n%s", a.ProducedBy, a.Content)
			}
		}
		if artifactContext.Len() > 0 {
			taskBuilder.WriteString("\n\nContext from previous steps:")
			taskBuilder.WriteString(artifactContext.String())
		}
	}

	// Build criteria from step.RelevantAC by looking up the full AC objects
	var stepCriteria []AcceptanceCriterion
	if len(step.RelevantAC) > 0 && len(allAC) > 0 {
		// Build a map for quick lookup
		acMap := make(map[string]AcceptanceCriterion)
		for _, ac := range allAC {
			acMap[ac.ID] = ac
		}
		// Find matching criteria
		for _, acID := range step.RelevantAC {
			if ac, ok := acMap[acID]; ok {
				stepCriteria = append(stepCriteria, ac)
			}
		}
	}

	// For the last terminal step in plan order, append any unmapped AC so the
	// final executor has a chance to address uncovered criteria.
	// "Terminal" = no other step depends on this one.
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

	// Check if this is the LAST terminal step in plan order
	isLastTerminal := false
	if isTerminal {
		isLastTerminal = true
		// Check if any later step in the plan is also terminal
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
				isLastTerminal = false
				break
			}
		}
	}

	if isLastTerminal && len(allAC) > 0 {
		unmapped := validateACMapping(&plan, allAC)
		// Only inject when some (but not all) ACs are unmapped. If every AC is
		// unmapped the plan has no AC mapping at all, so blanket injection is
		// not useful.
		if len(unmapped) > 0 && len(unmapped) < len(allAC) {
			fmt.Fprintf(&taskBuilder, "\n\nIMPORTANT: The following acceptance criteria are not explicitly assigned to any plan step. As the final step, ensure these are also addressed if possible:\n")
			acMap := make(map[string]AcceptanceCriterion)
			for _, ac := range allAC {
				acMap[ac.ID] = ac
			}
			for _, id := range unmapped {
				if ac, ok := acMap[id]; ok {
					fmt.Fprintf(&taskBuilder, "- %s: %s\n", ac.ID, ac.Description)
					// Also add them to step criteria so evaluator checks them
					stepCriteria = append(stepCriteria, ac)
				}
			}
		}
	}

	// Append retry context if retrying a step
	if retryContext != "" {
		fmt.Fprintf(&taskBuilder, "\n\n## Existing Files From Previous Attempt\n%s\nIMPORTANT: Fix these files IN PLACE. Do NOT create new files with different names.\n", retryContext)
	}

	return TaskDefinition{
		Task:     taskBuilder.String(),
		Criteria: stepCriteria,
		Tools:    availableTools,
	}
}

// buildChangeSummary creates a markdown summary of what the executor did at each step.
// It iterates through plan steps in plan order so the summary is deterministic.
func buildChangeSummary(completedSteps map[string]CompletedStep, plan *Plan) string {
	var b strings.Builder
	for _, step := range plan.Steps {
		cs, ok := completedSteps[step.ID]
		if !ok || cs.Output == "" {
			continue
		}
		fmt.Fprintf(&b, "### Step %q: %s\n%s\n\n", step.ID, step.Description, cs.Output)
	}
	return strings.TrimSpace(b.String())
}

// aggregateOutput combines outputs from all completed steps.
func (o *Orchestrator) aggregateOutput(completedSteps map[string]CompletedStep, plan *Plan) string {
	// Find terminal steps (steps that no other step depends on)
	dependedUpon := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, depID := range step.DependsOn {
			dependedUpon[depID] = true
		}
	}

	// Collect outputs from terminal steps
	var outputs []string
	for _, step := range plan.Steps {
		if !dependedUpon[step.ID] {
			if completed, ok := completedSteps[step.ID]; ok && completed.Error == nil {
				outputs = append(outputs, completed.Output)
			}
		}
	}

	// If no terminal outputs, collect all outputs
	if len(outputs) == 0 {
		for _, step := range plan.Steps {
			if completed, ok := completedSteps[step.ID]; ok && completed.Error == nil {
				outputs = append(outputs, completed.Output)
			}
		}
	}

	return strings.Join(outputs, "\n\n")
}

// buildSystemPrompt creates the system prompt for executors.
func (o *Orchestrator) buildSystemPrompt(ctx context.Context, userMessage string, criteria []AcceptanceCriterion) string {
	// Build workspace context string
	var workspaceCtxStr string
	if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
		workspaceCtxStr = "## Workspace\nYour session workspace is: " + wsPath + "\nAll artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location."
	}

	// Build acceptance criteria string
	var criteriaStr string
	if len(criteria) > 0 {
		var cb strings.Builder
		cb.WriteString("Acceptance Criteria (you MUST satisfy ALL of these before calling finish):\n")
		for _, ac := range criteria {
			fmt.Fprintf(&cb, "- %s: %s\n", ac.ID, ac.Description)
		}
		cb.WriteString("\nDo NOT call the finish tool until you have addressed all acceptance criteria above.")
		criteriaStr = cb.String()
	}

	// Apply template substitutions
	result := prompts.OrchestratorSystem
	result = strings.ReplaceAll(result, "WORKSPACE-CONTEXT", workspaceCtxStr)
	result = strings.ReplaceAll(result, "ACCEPTANCE-CRITERIA", criteriaStr)

	return result
}

// Run is a backwards-compatible method that calls Handle.
// Kept for compatibility with Phase 1 code.
func (o *Orchestrator) Run(ctx context.Context, userMessage string) (*HandleResult, error) {
	return o.Handle(ctx, userMessage)
}

// isRecoverableAPIError checks if an error is a recoverable API error
// that should not crash the session (e.g., 400-class errors from malformed requests,
// or retryable LLM errors that exhausted Router-level retries).
func isRecoverableAPIError(err error) bool {
	if err == nil {
		return false
	}
	// Retryable LLM errors (rate limit, 5xx, network) that exhausted Router-level retries
	// should be handled gracefully rather than crashing the session.
	if llm.IsRetryable(err) {
		return true
	}
	errStr := err.Error()
	// 400-class errors from malformed requests — can be recovered by provider/executor fixes
	return strings.Contains(errStr, "status code: 400") ||
		strings.Contains(errStr, "missing field `content`") ||
		strings.Contains(errStr, "Failed to deserialize")
}

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *agent.Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}

// isContextExceededError checks if an error indicates the context window was exceeded.
// This can happen when our token estimation is inaccurate and the API rejects the request.
func isContextExceededError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrContextWindowExceeded) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	patterns := []string{
		"context length exceeded",
		"maximum context length",
		"context_length_exceeded",
		"too many tokens",
		"request too large",
		"input is too long",
		"prompt is too long",
	}
	for _, p := range patterns {
		if strings.Contains(errStr, p) {
			return true
		}
	}
	return false
}
