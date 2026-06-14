package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/core/skills"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// prepareRequestContext sets plan-mode key, injection-defense key, vector hints,
// and emits the initial 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) prepareRequestContext(ctx context.Context, message string) context.Context {
	// Always set plan mode context key (planning always happens first).
	ctx = context.WithValue(ctx, PlanModeKey, true)
	if o.config.InjectionDefenseEnabled {
		ctx = context.WithValue(ctx, InjectionDefenseKey, true)
	}

	// Generate RAG hints from vector index (non-blocking, 2s timeout).
	ctx = o.injectVectorSearchHints(ctx, message)

	// Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.emitInitialContextFill(ctx)

	return ctx
}

// setupBlackboard creates a fresh Blackboard (first message) or restores an
// existing one from persistence (continuation).
func (o *Orchestrator) setupBlackboard(message, sessionID, taskID string) (Blackboard, error) {
	if taskID == "" {
		// First message: create clean BB
		id := uuid.New().String()
		var bb Blackboard
		if o.bbFactory != nil {
			bb = o.bbFactory(id)
		} else {
			bb = NewMapBlackboard()
		}
		bb.SetOriginalRequest(message)

		// Wire emitter if PersistentBlackboard
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetEmitter(o.emitter)
		}
		return bb, nil
	}

	// Continuation: restore existing BB
	if o.taskStore == nil || o.bbRestoreFunc == nil {
		return nil, errors.New("task persistence not configured")
	}

	o.logDebug("orchestrator: restoring blackboard", "taskID", taskID)
	pbb, err := o.bbRestoreFunc(taskID, sessionID, o.taskStore, o.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to restore blackboard: %w", err)
	}
	if pbb == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	// Wire emitter
	pbb.SetEmitter(o.emitter)

	// Emit MemoryRead if facts were restored from persistence.
	if facts := pbb.GetFacts(); len(facts) > 0 {
		o.emitter.MemoryRead(0, fmt.Sprintf("Loaded %d facts from previous execution", len(facts)))
	}

	// Reactivate task
	pbb.ReactivateTask()

	return pbb, nil
}

// routeAndActivateSkills performs routing, activates matched skills, applies
// skill policy overrides, and checks for clarification. If clarification is
// needed (and no user-specified skills suppress it), a non-nil *HandleResult is
// returned that the caller should use as a short-circuit return value.
func (o *Orchestrator) routeAndActivateSkills(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (context.Context, *RoutingDecision, []skills.SkillDescriptor, *HandleResult, error) {
	o.logDebug("orchestrator: starting routing")
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})

	var skillDescriptors []skills.SkillDescriptor
	if o.skillManager != nil {
		skillDescriptors = o.skillManager.List()
	}

	// When the user explicitly invoked skill(s) via /skill-name, the message
	// has the skill reference stripped. Build a routing-specific message that
	// restores the skill context.
	routingMessage := message
	if len(opts.UserSkills) > 0 && o.skillManager != nil {
		routingMessage = o.buildSkillAugmentedRoutingMessage(message, opts.UserSkills)
	}

	routing, err := o.router.Route(ctx, routingMessage, availableTools, o.conversationHistory, skillDescriptors)
	if err != nil {
		o.logDebug("orchestrator: routing failed", "error", err)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return ctx, nil, nil, nil, fmt.Errorf("routing failed: %w", err)
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// Activate matched skills (merge router-matched + user-specified, deduplicated)
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

	// Handle clarification
	// When the user explicitly invoked skills via /skill-name, their intent is
	// clear. Suppress clarification as a safety net.
	if routing.NeedsClarification && len(opts.UserSkills) == 0 {
		o.logDebug("orchestrator: returning clarification request")
		// Close the task in the persistence layer: the planner never ran, so
		// there is nothing to resume.
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetRouting(routing)
			pbb.CompleteTask(0)
		}
		result := &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
			Blackboard:      bb,
		}
		return ctx, routing, activeSkillDescriptors, result, nil
	}
	if routing.NeedsClarification && len(opts.UserSkills) > 0 {
		o.logDebug("orchestrator: suppressing clarification — user explicitly invoked skills", "skills", opts.UserSkills)
		routing.NeedsClarification = false
	}

	return ctx, routing, activeSkillDescriptors, nil, nil
}

// executeFirstMessage generates a plan and runs it via the SDK P&E engine.
// The returned error may wrap orchestration.ErrExecutionIncomplete for partial
// success — in that case execResult is non-nil and contains best-effort output.
func (o *Orchestrator) executeFirstMessage(
	ctx context.Context,
	message string,
	bb Blackboard,
	availableTools []sdktools.ToolDescriptor,
	activeSkills []skills.SkillDescriptor,
	opts HandleOptions,
) (*orchestration.ExecutionResult, error) {
	singleStep := o.shouldUseSingleStep(opts.ExecutionMode)
	o.logDebug("orchestrator: generating plan", "mode", opts.ExecutionMode, "singleStep", singleStep)
	o.emitter.ServiceWithMeta("Planning approach...", map[string]any{"phase": "orchestration"})

	plan, planErr := o.planner.Plan(ctx, message, availableTools, nil, activeSkills, singleStep)
	if planErr != nil {
		o.logDebug("orchestrator: planning failed", "error", planErr)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, fmt.Errorf("planning failed: %w", planErr)
	}
	o.logDebug("orchestrator: plan ready", "steps", len(plan.Steps))

	// Execute in Plan&Execute mode
	o.logDebug("orchestrator: executing in full Plan&Execute mode")
	o.emitter.ServiceWithMeta("Preparing execution...", map[string]any{"phase": "orchestration", "step_count": len(plan.Steps)})

	// Store plan on blackboard for P&E execution.
	bb.SetPlan(plan)

	// Plan already set on blackboard; use Resume which picks up the existing plan.
	execResult, err := o.engine.Resume(ctx, bb)
	if err != nil {
		if errors.Is(err, orchestration.ErrExecutionIncomplete) {
			if execResult == nil {
				return nil, fmt.Errorf("orchestrator: ErrExecutionIncomplete with nil result in first message: %w", err)
			}
			o.logDebug("orchestrator: SDK engine reported incomplete execution", "error", err)
			return execResult, err
		}
		o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	o.logDebug("orchestrator: SDK engine completed", "attemptCount", execResult.AttemptCount)
	return execResult, nil
}

// executeContinuation generates a continuation plan, merges it with the
// existing plan, and resumes execution. Same error semantics as executeFirstMessage.
func (o *Orchestrator) executeContinuation(
	ctx context.Context,
	message string,
	bb Blackboard,
	availableTools []sdktools.ToolDescriptor,
	activeSkills []skills.SkillDescriptor,
	opts HandleOptions,
) (*orchestration.ExecutionResult, error) {
	o.logDebug("orchestrator: executing in Plan&Execute mode (continuation)")

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
	continuationPlan, planErr := o.planner.PlanContinuation(ctx, bb.GetOriginalRequest(), existingPlan, completedSteps, message, availableTools, activeSkills, singleStep)
	if planErr != nil {
		o.logDebug("orchestrator: PlanContinuation failed", "error", planErr)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, fmt.Errorf("continuation planning failed: %w", planErr)
	}

	// Merge continuation plan's steps into existing plan
	mergedPlan := &orchestration.Plan{
		Steps: append(existingPlan.Steps, continuationPlan.Steps...),
	}
	bb.SetPlan(mergedPlan)

	o.logDebug("orchestrator: merged continuation plan", "newSteps", len(continuationPlan.Steps), "totalSteps", len(mergedPlan.Steps))

	// Resume execution with the merged plan (picks up un-completed steps)
	execResult, err := o.engine.Resume(ctx, bb)
	if err != nil {
		if errors.Is(err, orchestration.ErrExecutionIncomplete) {
			if execResult == nil {
				return nil, fmt.Errorf("orchestrator: ErrExecutionIncomplete with nil result in continuation: %w", err)
			}
			o.logDebug("orchestrator: SDK engine reported incomplete execution (continuation)", "error", err)
			return execResult, err
		}
		o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	o.logDebug("orchestrator: SDK engine resume completed", "attemptCount", execResult.AttemptCount)
	return execResult, nil
}

// finalizeResult persists the routing decision, builds the HandleResult, and
// updates conversationHistory for future routing context.
// execResult must never be nil; callers must guard with a zero-value fallback.
func (o *Orchestrator) finalizeResult(bb Blackboard, routing *RoutingDecision, execResult *orchestration.ExecutionResult, message string) *HandleResult {
	// Persist routing decision on PersistentBlackboard (post-execution)
	o.logDebug("orchestrator: persisting routing decision")
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
		pbb.CompleteTask(execResult.AttemptCount)
	}

	// Build HandleResult
	result := &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Blackboard:      bb,
		AttemptCount:    execResult.AttemptCount,
		Reflections:     execResult.Reflections,
	}

	// Get plan from blackboard if available
	if plan := bb.GetPlan(); plan != nil {
		result.Plan = plan
	}

	o.logDebug("orchestrator: handle_message completed", "attemptCount", result.AttemptCount)

	// Accumulate conversation history for future routing context
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

	return result
}
