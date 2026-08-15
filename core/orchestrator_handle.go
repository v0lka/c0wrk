package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// prepareRequestContext sets plan-mode key, injection-defense key, vector hints,
// and emits the initial 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) prepareRequestContext(ctx context.Context, message string) context.Context {
	// Always set plan mode context key (planning always happens first).
	ctx = context.WithValue(ctx, PlanModeKey, true)
	if o.config.InjectionDefenseEnabled {
		ctx = context.WithValue(ctx, InjectionDefenseKey, true)
	}

	// Small-LLM prompt profile: carry the SystemPrompt sub-toggle flags so
	// buildSystemPromptWith can gate the lite directive, reasoning scaffold,
	// and few-shot examples independently. Gated on BOTH the master
	// SmallLLM.Enabled toggle and the SystemPrompt variant being active (Lite
	// on) (defense-in-depth) — when either is off the ctx value is absent and
	// buildSystemPromptWith uses the default verbose directive with no
	// scaffold/few-shot additions.
	sc := o.config.SmallLLM
	if sc.Enabled && sc.SystemPrompt.Lite {
		ctx = withSmallLLMPromptProfile(ctx, smallLLMPromptProfile{
			Lite:              sc.SystemPrompt.Lite,
			FewShot:           sc.SystemPrompt.FewShot,
			ReasoningScaffold: sc.SystemPrompt.ReasoningScaffold,
		})
	}

	// Generate RAG hints from vector index (non-blocking, 2s timeout).
	ctx = o.injectVectorSearchHints(ctx, message)

	// Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.emitInitialContextFill()

	return ctx
}

// setupBlackboard creates a fresh Blackboard (first message) or restores an
// existing one from persistence (continuation). Pending attachments are flushed
// into the blackboard in both paths so they are available to the task and
// persisted alongside the rest of the blackboard state.
func (o *Orchestrator) setupBlackboard(message, sessionID, taskID string, pendingAttachments []orchestration.Attachment) (orchestration.Blackboard, error) {
	if taskID == "" {
		// First message: create clean BB
		id := uuid.New().String()
		var bb orchestration.Blackboard
		if o.bbFactory != nil {
			bb = o.bbFactory(id)
		} else {
			bb = orchestration.NewMapBlackboard()
		}
		bb.SetOriginalRequest(message)

		// Wire emitter if PersistentBlackboard
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetEmitter(o.emitter)
		}

		// Flush pending attachments before execution.
		for _, a := range pendingAttachments {
			bb.AddAttachment(a)
		}
		o.wireAttachmentNameResolver(bb)
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

	// Flush pending attachments into the restored blackboard.
	for _, a := range pendingAttachments {
		pbb.AddAttachment(a)
	}

	// Reactivate task
	pbb.ReactivateTask()

	o.wireAttachmentNameResolver(pbb)
	return pbb, nil
}

// wireAttachmentNameResolver connects the emitter (when it supports attachment
// name resolution) to the task's blackboard so read_attachment tool-call
// events carry the original file name in their persisted metadata. The
// resolver is a closure over bb and is read at event-emission time, so it sees
// attachments regardless of whether they were flushed from pending or
// rehydrated from persistence. Nil-safe: a nil emitter type-asserts to false.
func (o *Orchestrator) wireAttachmentNameResolver(bb orchestration.Blackboard) {
	if r, ok := o.emitter.(AttachmentNameResolver); ok {
		r.SetAttachmentNameResolver(func(id string) string {
			if a, found := bb.GetAttachment(id); found {
				return a.OriginalName
			}
			return ""
		})
	}
}

// routeOrContinue wraps routeAndActivateSkills with a continuation fast-path.
//
// When the user sends a follow-up message to a restored task (opts.TaskID !=
// "") and the restored blackboard has an existing routing decision, the
// router is skipped entirely. The router only sees the flat conversation
// history (user/assistant pairs) and is blind to the restored state — so a
// message like "continue step 10" would be misclassified. The fast-path
// reuses the restored routing decision and reactivates skills from it,
// then falls through to the Conductor.
func (o *Orchestrator) routeOrContinue(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (context.Context, *router.RoutingDecision, []skills.SkillDescriptor, *HandleResult, error) {
	// Enrich the context with the subagent roster (discovered catalog + any
	// explicit #mentions) before routing. Both are routing-independent, so
	// enriching here covers the continuation fast-path and fresh routing in a
	// single spot. The context flows into the Conductor's system prompt, which
	// renders "Available Subagents" (implicit delegation view) and, when
	// present, "Requested Subagents" (explicit delegation directive). Nil
	// agentManager / empty opts leave the context untouched (no regression).
	ctx = o.enrichAgentContext(ctx, opts.UserAgents)

	if opts.TaskID != "" {
		if pbb, ok := bb.(PersistableBlackboard); ok {
			if plan := pbb.GetPlan(); plan != nil {
				if restored := pbb.Routing(); restored != nil {
					o.logDebug("orchestrator: continuation fast-path (routing skipped — existing plan found)", "taskID", opts.TaskID)
					o.emitter.ServiceWithMeta("Continuation (routing skipped — existing routing decision found)", map[string]any{"phase": "orchestration"})
					o.emitter.Routing("conductor", restored.Domain, strconv.Itoa(restored.Complexity))
					o.logInfo("routing_decision", "domain", restored.Domain, "complexity", restored.Complexity)
					ctx = o.reactivateSkills(ctx, restored, opts.UserSkills)
					activeSkills := o.collectActiveSkillDescriptors(restored, opts.UserSkills)
					ctx = WithDomain(ctx, restored.Domain)
					ctx = WithComplexity(ctx, restored.Complexity)
					if len(opts.UserSkills) > 0 {
						ctx = WithUserSkills(ctx, opts.UserSkills)
					}
					return ctx, restored, activeSkills, nil, nil
				}
			}
		}
	}
	return o.routeAndActivateSkills(ctx, message, opts, bb, availableTools)
}

// enrichAgentContext attaches the discovered subagent catalog and any explicit
// #agent-name mentions to the context so the Conductor's system prompt can
// render the "Available Subagents" and "Requested Subagents" sections. The
// catalog is the full discovery list (including hidden agents — the prompt
// formatter filters hidden entries for the public roster but keeps them
// resolvable for explicit mentions). Both attachments are nil/empty-safe:
// a nil agentManager or empty userAgents leaves the context unchanged, so a
// project with no subagents sees no regression.
func (o *Orchestrator) enrichAgentContext(ctx context.Context, userAgents []string) context.Context {
	if o.agentManager != nil {
		if descriptors := o.agentManager.List(); len(descriptors) > 0 {
			ctx = WithAvailableAgents(ctx, descriptors)
		}
	}
	if len(userAgents) > 0 {
		ctx = WithUserAgents(ctx, userAgents)
	}
	return ctx
}

// reactivateSkills re-applies skill activation (context value + tool policy
// overrides + emitter event) from a restored routing decision. Used by the
// continuation fast-path to avoid calling the router while still wiring skills.
func (o *Orchestrator) reactivateSkills(ctx context.Context, routing *router.RoutingDecision, userSkills []string) context.Context {
	mergedSkillNames := mergeSkillNames(routing.MatchedSkills, userSkills)
	if len(mergedSkillNames) == 0 || o.skillManager == nil {
		return ctx
	}
	var activeSkills []*skills.Skill
	var activatedNames []string
	for _, name := range mergedSkillNames {
		if s, ok := o.skillManager.Get(name); ok {
			activeSkills = append(activeSkills, s)
			activatedNames = append(activatedNames, name)
		} else {
			o.logDebug("orchestrator: matched skill not found", "name", name)
		}
	}
	if len(activeSkills) > 0 {
		ctx = WithActiveSkills(ctx, &ActiveSkills{Skills: activeSkills})
		o.emitter.SkillsActivated(activatedNames)
		o.logInfo("skills_activated", "skills", activatedNames)
		skillOverrides := o.buildSkillPolicyOverrides(activeSkills)
		if len(skillOverrides) > 0 && o.coreToolRegistry != nil {
			o.coreToolRegistry.SetSkillPolicyOverrides(skillOverrides)
		}
	}
	return ctx
}

// collectActiveSkillDescriptors builds the SkillDescriptor slice for a restored
// routing decision (mirrors what routeAndActivateSkills would produce).
func (o *Orchestrator) collectActiveSkillDescriptors(routing *router.RoutingDecision, userSkills []string) []skills.SkillDescriptor {
	mergedSkillNames := mergeSkillNames(routing.MatchedSkills, userSkills)
	if len(mergedSkillNames) == 0 || o.skillManager == nil {
		return nil
	}
	var descriptors []skills.SkillDescriptor
	for _, name := range mergedSkillNames {
		if s, ok := o.skillManager.Get(name); ok {
			descriptors = append(descriptors, s.Descriptor())
		}
	}
	return descriptors
}

// routeAndActivateSkills performs routing, activates matched skills, and applies
// skill policy overrides. Clarification is not handled here — the Conductor
// decides when to ask via the ask_user tool (ADR-012).
func (o *Orchestrator) routeAndActivateSkills(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (context.Context, *router.RoutingDecision, []skills.SkillDescriptor, *HandleResult, error) {
	o.logDebug("orchestrator: starting routing")
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})

	var skillDescriptors []skills.SkillDescriptor
	if o.skillManager != nil {
		skillDescriptors = o.skillManager.List()
	}

	// When the user explicitly invoked skill(s) via /skill-name, the message
	// has the skill reference stripped. Build a routing-specific message that
	// restores the skill context (same logic as resolveTaskMessage, applied
	// here to the raw message the router receives).
	routingMessage := o.resolveTaskMessage(message, opts.UserSkills)

	// Convert github.com/v0lka/sp4rk/skills.SkillDescriptor to github.com/v0lka/sp4rk/agent/router.SkillDescriptor
	routerSkills := make([]router.SkillDescriptor, len(skillDescriptors))
	for i, sd := range skillDescriptors {
		routerSkills[i] = router.SkillDescriptor{Name: sd.Name, Description: sd.Description}
	}

	routing, err := o.router.Route(ctx, routingMessage, availableTools, o.conversationHistory, routerSkills)
	if err != nil {
		o.logDebug("orchestrator: routing failed", "error", err)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return ctx, nil, nil, nil, fmt.Errorf("routing failed: %w", err)
	}

	// No Project mode: override code domain to general so that
	// code-oriented planning and execution strategies are not applied.
	// Must happen BEFORE emitting the routing event so the UI sees the corrected domain.
	if o.isNoProject && routing.Domain == "code" {
		routing.Domain = "general"
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("conductor", routing.Domain, strconv.Itoa(routing.Complexity))
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
			// Skill *policy* stays task-wide here — the tool registry is
			// shared across the task and tools are keyed by name, not by step,
			// so per-step policy is not meaningful. Deliberate asymmetry.
			skillOverrides := o.buildSkillPolicyOverrides(activeSkills)
			if len(skillOverrides) > 0 && o.coreToolRegistry != nil {
				o.coreToolRegistry.SetSkillPolicyOverrides(skillOverrides)
			}
		}
	}

	// Handle clarification — removed in ADR-012.
	// The Conductor handles clarification itself via the ask_user tool.
	// Router.NeedsClarification is ignored: the Conductor decides when to ask.

	return ctx, routing, activeSkillDescriptors, nil, nil
}

// finalizeResult persists the routing decision and builds the HandleResult.
// The conversation history is updated centrally by recordConversationOutcome
// (deferred in HandleMessage), NOT here — this keeps history updates uniform
// across all terminal outcomes.
// execResult must never be nil; callers must guard with a zero-value fallback.
func (o *Orchestrator) finalizeResult(bb orchestration.Blackboard, routing *router.RoutingDecision, execResult *orchestration.ExecutionResult) *HandleResult {
	// Persist routing decision on PersistentBlackboard (post-execution)
	o.logDebug("orchestrator: persisting routing decision")
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
		persistTaskOutcome(pbb, execResult)
	}

	// Build HandleResult
	result := &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Blackboard:      bb,
		Reflections:     execResult.Reflections,
		Status:          execResult.Status,
	}

	// Get plan from blackboard if available
	if plan := bb.GetPlan(); plan != nil {
		result.Plan = plan
	}

	o.logDebug("orchestrator: handle_message completed")

	return result
}

// persistTaskOutcome maps the typed execution status onto the persisted task
// status. Partial executions are deliberately left "in_progress" so the
// resumable-task safety net (GetUnfinishedTask) can offer a Resume action;
// failed/aborted executions are marked "failed" (also resumable). Only a
// genuinely successful execution closes the task as "completed".
func persistTaskOutcome(pbb PersistableBlackboard, execResult *orchestration.ExecutionResult) {
	switch execResult.Status {
	case orchestration.ExecutionStatusPaused:
		// Paused: a cooperative pause is a recoverable checkpoint. Mark the
		// task paused so SessionRuntimeStatus.Paused can reflect it and a
		// later Resume re-enters. The trajectory was already flushed by the
		// conductor's RunConductor (trajStore.Flush) so the checkpoint is live.
		pbb.PauseTask()
	case orchestration.ExecutionStatusPartial, orchestration.ExecutionStatusCancelled:
		// Partial: keep in_progress — resumable.
		// Cancelled: the session manager persists the cancellation itself.
	case orchestration.ExecutionStatusFailed, orchestration.ExecutionStatusAborted:
		pbb.FailTask()
	default:
		// Success or legacy empty status. Record the Conductor's output as the
		// final result before completion; CompleteTask then persists it to the
		// tasks.final_output column (see specs/domains/memory/blackboard.md).
		pbb.SetFinalResult(execResult.Output)
		pbb.CompleteTask(execResult.AttemptCount)
	}
}
