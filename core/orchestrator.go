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
}

// NewOrchestrator creates a new Orchestrator with all Phase 2 components.
// reflector, logger, and emitter are optional (nil-safe) for Phase 3 features.
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
		o.emitter.ContextFill(0, 0, effectiveMax, "ok")
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
	o.emitter.Routing(routing.Mode, routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "mode", routing.Mode, "domain", routing.Domain, "complexity", routing.Complexity)

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

	// 5. Handle based on routing mode
	var result *HandleResult
	switch routing.Mode {
	case "direct":
		result, err = o.handleDirect(ctx, userMessage, routing, availableTools, ac)
	case "react":
		result, err = o.handleReact(ctx, userMessage, routing, availableTools, ac)
	case "plan_execute":
		result, err = o.handlePlanExecute(ctx, userMessage, routing, availableTools, nil, ac)
	case "needs_clarification":
		result = &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
		}
	default:
		result, err = o.handleReact(ctx, userMessage, routing, availableTools, ac)
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

// handleDirect handles direct mode - single LLM call without tools.
func (o *Orchestrator) handleDirect(ctx context.Context, userMessage string, routing *RoutingDecision, availableTools []tools.ToolDescriptor, ac []AcceptanceCriterion) (*HandleResult, error) {
	// Build messages: prepend conversation history before the current user message
	messages := make([]llm.Message, 0, len(o.conversationHistory)+1)
	messages = append(messages, o.conversationHistory...)
	messages = append(messages, llm.Message{Role: "user", Content: userMessage})

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := o.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("direct LLM call failed: %w", err)
	}

	directOutput := resp.Message.Content

	// No AC available — return direct answer without evaluation
	if len(ac) == 0 {
		return &HandleResult{
			Output:          directOutput,
			RoutingDecision: routing,
		}, nil
	}

	// Lightweight evaluation against AC
	o.emitter.ServiceWithMeta("Evaluating acceptance criteria...", map[string]any{"phase": "orchestration"})
	evalResult, evalErr := o.evaluator.Evaluate(ctx, directOutput, ac, nil)
	if evalErr != nil {
		//nolint:nilerr // error is handled by returning result without evaluation
		return &HandleResult{
			Output:          directOutput,
			RoutingDecision: routing,
		}, nil
	}

	// Emit evaluation result
	passed := len(evalResult.Passed)
	total := passed + len(evalResult.Failed) + len(evalResult.Unclear)
	o.emitter.Evaluation(passed, total, o.buildEvalCriterionEvents(evalResult))
	o.logInfo("evaluation", "passed", passed, "total", total, "all_passed", evalResult.AllPassed)
	o.logDebug("evaluation_details", "passed", evalResult.Passed, "failed", evalResult.Failed, "unclear", evalResult.Unclear)

	// If eval passes, return direct answer with eval result
	if evalResult.AllPassed {
		return &HandleResult{
			Output:          directOutput,
			RoutingDecision: routing,
			EvalResult:      evalResult,
		}, nil
	}

	// Eval failed — escalate to react mode
	o.emitter.Escalation("direct", "react")
	o.logInfo("escalation", "from", "direct", "to", "react")
	escalatedRouting := &RoutingDecision{
		Mode:               "react",
		Domain:             routing.Domain,
		Complexity:         routing.Complexity + 1,
		CompactionStrategy: routing.CompactionStrategy,
		SuggestedTools:     routing.SuggestedTools,
		Confidence:         routing.Confidence,
	}
	result, err := o.handleReact(ctx, userMessage, escalatedRouting, availableTools, ac)
	if err != nil {
		return nil, err
	}
	if result != nil {
		result.Escalated = true
		result.OriginalMode = "direct"
	}
	return result, nil
}

// handleReact handles react mode - ReAct loop with evaluation and retry.
func (o *Orchestrator) handleReact(ctx context.Context, userMessage string, routing *RoutingDecision, availableTools []tools.ToolDescriptor, ac []AcceptanceCriterion) (*HandleResult, error) {
	// 2. Create task definition
	taskDef := TaskDefinition{
		Task:     userMessage,
		Criteria: ac,
		Tools:    availableTools,
	}

	// Track reflections from this session's attempts
	var sessionReflections []Reflection
	var lastResult *ExecutorResult
	var lastEvalResult *EvalResult

	// 4. Retry loop
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		// Emit retry attempt (skip for first attempt)
		if attempt > 0 {
			o.emitter.Retry(attempt, o.maxRetries+1)
			o.logInfo("retry", "attempt", attempt, "max_attempts", o.maxRetries+1)
		}

		// Create fresh context manager for each attempt
		systemPrompt := o.buildSystemPrompt(ctx, userMessage, ac, false) // false = not a step execution (full react mode)
		// Resolve model metadata for the executor role
		var modelMeta llm.ModelMetadata
		if o.modelRegistry != nil {
			modelMeta = o.modelRegistry.Resolve("")
		}
		cw := o.contextFactory(systemPrompt, modelMeta, routing.CompactionStrategy)

		// Set the task and acceptance criteria into the context window
		cw.SetTask(taskDef.Task, taskDef.Criteria)

		// Run executor (don't suppress assistant events for react mode)
		executor := NewExecutor(o.llm, o.tools, o.tokenCounter, o.config.MaxSteps, o.logger, o.emitter, false, o.toolResultBudget)
		ctx = tools.WithTaskContext(ctx, taskDef.Task)
		result, err := executor.Run(ctx, taskDef.Tools, cw)
		if err != nil {
			// Check if this is a recoverable API error (e.g., 400-class errors)
			if isRecoverableAPIError(err) {
				// Log the error and report to user rather than crashing
				o.logWarn("executor_api_error_recovered", "error", err, "attempt", attempt+1)
				// Return a user-visible error message
				return &HandleResult{
					Output:          fmt.Sprintf("I encountered an API error: %s. Please try again.", err),
					RoutingDecision: routing,
					AttemptCount:    attempt + 1,
					Reflections:     sessionReflections,
				}, nil
			}
			return nil, fmt.Errorf("executor failed: %w", err)
		}
		lastResult = result

		// Build initial handle result
		handleResult := &HandleResult{
			Output:          result.Output,
			RoutingDecision: routing,
			AttemptCount:    attempt + 1,
			Reflections:     sessionReflections,
		}

		// Evaluate results (if we have AC)
		if len(ac) == 0 {
			return handleResult, nil
		}

		o.emitter.ServiceWithMeta("Evaluating acceptance criteria...", map[string]any{"phase": "orchestration"})
		evalResult, evalErr := o.evaluator.Evaluate(ctx, result.Output, ac, result.Steps)
		if evalErr != nil {
			// Return result even if evaluation fails
			//nolint:nilerr // error is handled by embedding in result
			return handleResult, nil
		}
		handleResult.EvalResult = evalResult
		lastEvalResult = evalResult

		// Emit evaluation result
		passed := len(evalResult.Passed)
		total := passed + len(evalResult.Failed) + len(evalResult.Unclear)
		o.emitter.Evaluation(passed, total, o.buildEvalCriterionEvents(evalResult))
		o.logInfo("evaluation", "passed", passed, "total", total, "all_passed", evalResult.AllPassed)
		o.logDebug("evaluation_details", "passed", evalResult.Passed, "failed", evalResult.Failed, "unclear", evalResult.Unclear)

		// Success - all criteria passed
		if evalResult.AllPassed {
			return handleResult, nil
		}

		// Failure - check if we should retry
		if attempt < o.maxRetries {
			if o.reflector != nil {
				// Generate reflection
				o.emitter.ServiceWithMeta("Some acceptance criteria not met, reflecting...", map[string]any{"phase": "orchestration"})
				reflection, reflectErr := o.reflector.Reflect(ctx, result.Steps, evalResult, nil, sessionReflections)
				if reflectErr != nil {
					// Reflection failed — still continue retry without reflection guidance
					o.logWarn("reflection failed in react, retrying without guidance", "error", reflectErr, "attempt", attempt+1)
					continue
				}

				// Check if reflector suggests abort
				if reflection.SuggestedAction == "abort" {
					sessionReflections = append(sessionReflections, *reflection)
					handleResult.Reflections = sessionReflections
					failedCriteria := o.formatFailedCriteria(evalResult)
					handleResult.Output = result.Output + "\n\n[Evaluation: some criteria not met: " + failedCriteria + ". Reflector suggests abort.]"
					return handleResult, nil
				}

				// Check if reflector suggests escalate to plan_execute
				if reflection.SuggestedAction == "escalate" {
					sessionReflections = append(sessionReflections, *reflection)
					o.emitter.Escalation("react", "plan_execute")
					o.logInfo("escalation", "from", "react", "to", "plan_execute")
					result, escErr := o.handlePlanExecute(ctx, userMessage, routing, availableTools, sessionReflections, ac)
					if escErr != nil {
						return nil, escErr
					}
					if result != nil {
						result.Escalated = true
						result.OriginalMode = "react"
						result.Reflections = sessionReflections
					}
					return result, nil
				}

				// Emit reflection
				o.emitter.Reflection(reflection.Summary, reflection.Hypotheses, attempt+1, o.maxRetries)
				o.logInfo("reflection", "summary", reflection.Summary, "suggested_action", reflection.SuggestedAction)
				o.logDebug("reflection_details", "hypotheses", reflection.Hypotheses, "reasoning", reflection.Reasoning, "failure_analysis", reflection.FailureAnalysis, "root_cause", reflection.RootCause, "action_plan", reflection.ActionPlan)

				// Add reflection to session list for next attempt
				sessionReflections = append(sessionReflections, *reflection)
			} else {
				o.logWarn("reflector not available, retrying without reflection", "attempt", attempt+1)
			}
		}
	}

	// Max retries exhausted — last-resort escalation to plan_execute
	if o.planner != nil {
		o.emitter.Escalation("react", "plan_execute")
		o.logInfo("escalation", "from", "react", "to", "plan_execute", "reason", "max_retries_exhausted")
		escalatedRouting := &RoutingDecision{
			Mode:               "plan_execute",
			Domain:             routing.Domain,
			Complexity:         routing.Complexity + 1,
			CompactionStrategy: routing.CompactionStrategy,
			SuggestedTools:     routing.SuggestedTools,
			Confidence:         routing.Confidence,
		}
		result, escErr := o.handlePlanExecute(ctx, userMessage, escalatedRouting, availableTools, sessionReflections, ac)
		if escErr != nil {
			return nil, escErr
		}
		if result != nil {
			result.Escalated = true
			result.OriginalMode = "react"
			result.Reflections = sessionReflections
		}
		return result, nil
	}

	// Max retries exhausted
	handleResult := &HandleResult{
		Output:          lastResult.Output,
		RoutingDecision: routing,
		EvalResult:      lastEvalResult,
		AttemptCount:    o.maxRetries + 1,
		Reflections:     sessionReflections,
	}

	if lastEvalResult != nil && !lastEvalResult.AllPassed {
		failedCriteria := o.formatFailedCriteria(lastEvalResult)
		handleResult.Output = lastResult.Output + "\n\n[Evaluation: some criteria not met after " + strconv.Itoa(o.maxRetries+1) + " attempts: " + failedCriteria + "]"
	}

	return handleResult, nil
}

// buildEvalCriterionEvents converts EvalResult into EvalCriterionEvent slice.
func (o *Orchestrator) buildEvalCriterionEvents(evalResult *EvalResult) []EvalCriterionEvent {
	criteria := make([]EvalCriterionEvent, 0, len(evalResult.Passed)+len(evalResult.Failed)+len(evalResult.Unclear))
	for _, d := range evalResult.Passed {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: true})
	}
	for _, d := range evalResult.Failed {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false})
	}
	for _, d := range evalResult.Unclear {
		criteria = append(criteria, EvalCriterionEvent{Name: d.Criterion.ID, Description: d.Criterion.Description, Passed: false})
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

	// Create shared workspace for inter-agent communication
	workspace := NewSharedWorkspace()

	// 3. Retry loop
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		// Emit retry attempt (skip for first attempt)
		if attempt > 0 {
			o.emitter.Retry(attempt, o.maxRetries+1)
			o.logInfo("retry", "attempt", attempt, "max_attempts", o.maxRetries+1)
		}

		// Execute the current plan
		finalOutput, completedSteps, _ := o.executePlanWithSteps(ctx, currentPlan, ac, routing, availableTools, sessionReflections, preCompleted, workspace, userMessage)
		// Note: step execution errors are handled within executePlanWithSteps
		lastOutput = finalOutput
		prevCompletedSteps := completedSteps

		handleResult := &HandleResult{
			Output:          finalOutput,
			RoutingDecision: routing,
			Plan:            currentPlan,
			AttemptCount:    attempt + 1,
			Reflections:     sessionReflections,
		}

		// Final evaluation
		if len(ac) == 0 {
			return handleResult, nil
		}

		o.emitter.ServiceWithMeta("Evaluating acceptance criteria...", map[string]any{"phase": "orchestration"})
		syntheticSteps := buildPlanExecutionSteps(completedSteps, currentPlan)
		evalResult, evalErr := o.evaluator.Evaluate(ctx, finalOutput, ac, syntheticSteps)
		if evalErr != nil {
			o.logWarn("evaluation failed, returning result without evaluation", "error", evalErr)
			o.emitter.ServiceWithMeta("Evaluation failed: "+evalErr.Error(), map[string]any{"phase": "orchestration", "severity": "warning"})
			//nolint:nilerr // evaluation failure is non-fatal; return best-effort result without eval
			return handleResult, nil
		}
		handleResult.EvalResult = evalResult
		lastEvalResult = evalResult

		// Emit evaluation result
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
					preCompleted = nil
					workspace.Clear()
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
func (o *Orchestrator) executePlanWithSteps(ctx context.Context, plan *Plan, ac []AcceptanceCriterion, routing *RoutingDecision, availableTools []tools.ToolDescriptor, sessionReflections []Reflection, preCompleted map[string]CompletedStep, workspace *SharedWorkspace, userMessage string) (string, []CompletedStep, error) {
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
			break // All done or stuck
		}

		// Run steps via SubAgents (works for both single and parallel execution)
		tasks := make([]SubAgentTask, 0, len(readySteps))

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

			taskDef := o.buildStepTask(step, stepIndex, *plan, ac, completedSteps, stepTools, workspace, userMessage)

			// Use profile-specific system prompt if provided
			var systemPrompt string
			if profile.SystemPrompt != "" {
				systemPrompt = profile.SystemPrompt
			} else {
				systemPrompt = o.buildSystemPrompt(ctx, step.Description, ac, true) // true = step execution
			}

			// Resolve model metadata for the profile's LLM role
			var modelMeta llm.ModelMetadata
			if o.modelRegistry != nil {
				modelMeta = o.modelRegistry.Resolve("")
			}
			cm := o.contextFactory(systemPrompt, modelMeta, routing.CompactionStrategy)

			// Set the task and acceptance criteria into the context window
			cm.SetTask(taskDef.Task, taskDef.Criteria)
			// Scope emitter to plan step for event association
			scopedEmitter := scopeEmitterToStep(o.emitter, step.ID)

			// Suppress assistant events for plan-step executors
			maxSteps := profile.MaxSteps
			if maxSteps == 0 {
				maxSteps = o.config.MaxSteps
			}
			executor := NewExecutor(o.llm, o.tools, o.tokenCounter, maxSteps, o.logger, scopedEmitter, true, o.toolResultBudget)
			executor.SetPlanContext(step.ID, stepIndex+1, len(plan.Steps))

			tasks = append(tasks, SubAgentTask{
				StepID:    step.ID,
				Executor:  executor,
				CM:        cm,
				TaskTools: taskDef.Tools,
				TaskDesc:  taskDef.Task,
				Emitter:   scopedEmitter,
			})
		}

		results := RunSubAgentsParallel(ctx, tasks)
		for _, r := range results {
			// Emit PlanStepComplete for each result
			o.emitter.PlanStepComplete(r.StepID, r.Error == nil, time.Since(stepStartTimes[r.StepID]))

			// Store output in workspace
			if workspace != nil && r.Error == nil {
				workspace.Store(r.StepID+"/output", r.Output, r.StepID)
			}

			cs := CompletedStep(r)
			completedSteps[r.StepID] = cs
			completedList = append(completedList, cs)
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
// their responsible steps (via PlanStep.RelevantAC), then expands the set to include
// all transitive dependents in the DAG. Returns nil if no criteria map to any step,
// signaling the caller should fall back to full plan retry.
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

	// Expand with transitive dependents: any step that (transitively) depends on a retry step
	changed := true
	for changed {
		changed = false
		for _, step := range plan.Steps {
			if retrySet[step.ID] {
				continue
			}
			for _, depID := range step.DependsOn {
				if retrySet[depID] {
					retrySet[step.ID] = true
					changed = true
					break
				}
			}
		}
	}

	return retrySet
}

// buildPlanExecutionSteps converts completed plan steps into a synthetic execution
// trajectory ([]Step) so that the Reflector and Evaluator can see what actually
// happened during Plan&Execute execution. Each CompletedStep maps to a Step with
// the step description as Thought and the step output (or error) as Observation.
func buildPlanExecutionSteps(completedList []CompletedStep, plan *Plan) []Step {
	// Build a map from step ID to plan step description
	stepDescriptions := make(map[string]string)
	for _, ps := range plan.Steps {
		stepDescriptions[ps.ID] = ps.Description
	}

	steps := make([]Step, 0, len(completedList))
	for _, cs := range completedList {
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
func (o *Orchestrator) buildStepTask(step PlanStep, stepIndex int, plan Plan, allAC []AcceptanceCriterion, completedSteps map[string]CompletedStep, availableTools []tools.ToolDescriptor, workspace *SharedWorkspace, userMessage string) TaskDefinition {
	// Build task description with scoping context
	var taskBuilder strings.Builder

	// Step position header
	fmt.Fprintf(&taskBuilder, "[Step %d of %d] Your task: %s\n\n", stepIndex+1, len(plan.Steps), step.Description)

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
	if workspace != nil {
		var artifactContext strings.Builder
		for _, depID := range step.DependsOn {
			artifacts := workspace.GetByProducer(depID)
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

	return TaskDefinition{
		Task:     taskBuilder.String(),
		Criteria: stepCriteria,
		Tools:    availableTools,
	}
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
// If isStepExecution is true, adds scoping instructions for single-step execution.
func (o *Orchestrator) buildSystemPrompt(ctx context.Context, userMessage string, criteria []AcceptanceCriterion, isStepExecution bool) string {
	// Build workspace context string
	var workspaceCtxStr string
	if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
		workspaceCtxStr = "## Workspace\nYour session workspace is: " + wsPath + "\nAll artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location."
	}

	// Build step scope string
	var stepScopeStr string
	if isStepExecution {
		stepScopeStr = `
STEP EXECUTION SCOPE: You are executing a single step in a multi-step plan. Your responsibility is ONLY to complete this specific step. Do NOT attempt to:

- Solve the entire problem
- Produce final deliverables
- Perform analysis or work that belongs to subsequent steps
  Focus narrowly on your step's objective and acceptance criteria. Your output will be consumed by downstream steps.
`
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
	result = strings.ReplaceAll(result, "STEP-SCOPE", stepScopeStr)
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

// isContextExceededError checks if an error indicates the context window was exceeded.
// This can happen when our token estimation is inaccurate and the API rejects the request.
func isContextExceededError(err error) bool {
	if err == nil {
		return false
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
