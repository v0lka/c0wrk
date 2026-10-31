package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// prepareRequestContext assembles the task context (plan-mode key, injection
// defense, model-profiles prompt profile, RAG hints, vision resolver) and
// emits the initial 0% context_fill baseline.
func (o *Orchestrator) prepareRequestContext(ctx context.Context, message string) context.Context {
	// Always set plan mode context key (planning always happens first).
	ctx = context.WithValue(ctx, PlanModeKey, true)
	if o.config.InjectionDefenseEnabled {
		ctx = context.WithValue(ctx, InjectionDefenseKey, true)
	}

	ctx = o.applyModelProfilesPromptProfile(ctx)

	ctx = o.injectVectorSearchHints(ctx, message)

	// Per-document vision resolution: every markitdown conversion in this
	// task (incl. inherited subagent delegations) captions images with the
	// model active at conversion time. Nil-safe no-op.
	ctx = markitdown.WithVisionResolver(ctx, o.visionResolver)

	o.emitInitialContextFill()

	return ctx
}

// applyModelProfilesPromptProfile carries the model-profile SystemPrompt
// sub-toggle flags into ctx (gated on the effective master toggle AND Lite —
// defense-in-depth; effective settings, so a runtime toggle needs no
// restart). Variant semantics: specs/domains/model-profiles.md. This is the
// single place the key is set (fresh path here, resume path in Resume): a
// resumed goal's verifier — a specialized Lite-eligible run — would otherwise
// miss the swap the fresh path applied.
func (o *Orchestrator) applyModelProfilesPromptProfile(ctx context.Context) context.Context {
	sc := o.modelProfilesSettings()
	if sc.Enabled && sc.SystemPrompt.Lite {
		ctx = withModelProfilesPromptProfile(ctx, modelProfilesPromptProfile{
			Lite:              sc.SystemPrompt.Lite,
			FewShot:           sc.SystemPrompt.FewShot,
			ReasoningScaffold: sc.SystemPrompt.ReasoningScaffold,
		})
	}
	return ctx
}

// setupBlackboard creates a fresh Blackboard (first message) or restores one
// from persistence (continuation); pending attachments are flushed in both
// paths so they are available to the task and persisted.
func (o *Orchestrator) setupBlackboard(message, sessionID, taskID string, pendingAttachments []orchestration.Attachment) (orchestration.Blackboard, error) {
	if taskID == "" {
		id := uuid.New().String()
		var bb orchestration.Blackboard
		if o.bbFactory != nil {
			bb = o.bbFactory(id)
		} else {
			bb = orchestration.NewMapBlackboard()
		}
		bb.SetOriginalRequest(message)

		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.SetEmitter(o.emitter)
		}

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

	pbb.SetEmitter(o.emitter)

	if facts := pbb.GetFacts(); len(facts) > 0 {
		o.emitter.MemoryRead(0, fmt.Sprintf("Loaded %d facts from previous execution", len(facts)))
	}

	for _, a := range pendingAttachments {
		pbb.AddAttachment(a)
	}

	// NOTE: deliberately NOT reactivated here — reactivation flips the anchor
	// task's terminal status back to in_progress and must only happen at the
	// continuation's commit point (reactivateContinuationTask, called by
	// HandleMessage). A pre-commit failure (e.g. a routing error) left the
	// anchor orphaned in_progress: the manager's fallback created a NEW task
	// row, nothing closed the reactivated one, the session reported
	// has_unfinished_task=true forever, and every restart re-injected the
	// "Task failed / Resume" banner over a completed session.

	o.wireAttachmentNameResolver(pbb)
	return pbb, nil
}

// reactivateContinuationTask flips a restored continuation task back to
// in_progress at the continuation's commit point — after routing succeeded
// (normal path) or the goal loop was entered (goal path) — so a task whose
// continuation fails BEFORE execution starts keeps its prior terminal status
// instead of being orphaned in_progress. No-op for fresh tasks: their
// PersistNewTask row already starts in_progress.
func (o *Orchestrator) reactivateContinuationTask(bb orchestration.Blackboard, taskID string) {
	if taskID == "" {
		return
	}
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.ReactivateTask()
	}
}

// wireAttachmentNameResolver connects the emitter (when it supports
// attachment name resolution) to the task's blackboard. The resolver closure
// reads bb at event-emission time, so it sees attachments whether they were
// flushed from pending or rehydrated from persistence. Nil-safe.
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

// routeOrContinue wraps routeAndActivateSkills with a continuation fast-path:
// on a restored task with a persisted routing decision the router is skipped
// entirely — it only sees the flat conversation history and is blind to the
// restored state, so "continue step 10" would be misclassified. The restored
// decision is reused and skills reactivated from it.
func (o *Orchestrator) routeOrContinue(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (context.Context, *router.RoutingDecision, []skills.SkillDescriptor, *HandleResult, error) {
	// Subagent roster + MCP mentions ride the context before routing —
	// routing-independent, so this covers the fast-path and fresh routing in
	// one spot (the Conductor prompt renders them as prompt sections;
	// specs/domains/capabilities.md).
	ctx = o.enrichAgentContext(ctx, opts.UserAgents, opts.UserMCPServers)

	if opts.TaskID != "" {
		if pbb, ok := bb.(PersistableBlackboard); ok {
			if plan := pbb.GetPlan(); plan != nil {
				if restored := pbb.Routing(); restored != nil {
					o.logDebug("orchestrator: continuation fast-path (routing skipped — existing plan found)", "taskID", opts.TaskID)
					o.emitter.ServiceWithMeta("Continuation (routing skipped — existing routing decision found)", map[string]any{"phase": "orchestration"})
					o.emitter.Routing("conductor", restored.Domain, strconv.Itoa(restored.Complexity))
					o.logInfo("routing_decision", "domain", restored.Domain, "complexity", restored.Complexity)
					ctx = o.reactivateSkills(ctx, restored, opts.UserSkills)
					activeSkills := o.collectActiveSkillDescriptors(ctx, restored, opts.UserSkills)
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

// enrichAgentContext attaches the discovered subagent catalog, explicit
// /-mention names, and MCP-server mentions to the context for the Conductor
// prompt's roster/directive sections. The catalog is filtered through the
// task's capability snapshot BEFORE formatting (auto entries only —
// specs/domains/capabilities.md); nil/empty inputs leave the context
// unchanged.
func (o *Orchestrator) enrichAgentContext(ctx context.Context, userAgents, userMCPServers []string) context.Context {
	if o.agentManager != nil {
		if descriptors := filterRosterAgents(ctx, o.agentManager.List()); len(descriptors) > 0 {
			ctx = WithAvailableAgents(ctx, descriptors)
		}
	}
	if len(userAgents) > 0 {
		ctx = WithUserAgents(ctx, userAgents)
	}
	if len(userMCPServers) > 0 {
		ctx = WithUserMCPServers(ctx, userMCPServers)
	}
	return ctx
}

// reactivateSkills re-applies skill activation from a restored routing
// decision — the continuation fast-path's router-free skill wiring. The
// restored decision is filtered through the task's capability snapshot
// (implicitSkillNames): a skill that has since turned manual/disabled must
// not silently reactivate; user mentions were filtered at task entry
// (specs/domains/capabilities.md).
func (o *Orchestrator) reactivateSkills(ctx context.Context, routing *router.RoutingDecision, userSkills []string) context.Context {
	implicitMatched := implicitSkillNames(routing.MatchedSkills, SkillCapabilityModesFromContext(ctx))
	mergedSkillNames := mergeSkillNames(implicitMatched, userSkills)
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
	}
	return ctx
}

// collectActiveSkillDescriptors builds the SkillDescriptor slice for a
// restored routing decision (what routeAndActivateSkills would have produced),
// filtered through the task's capability snapshot the same way reactivateSkills
// does (specs/domains/capabilities.md).
func (o *Orchestrator) collectActiveSkillDescriptors(ctx context.Context, routing *router.RoutingDecision, userSkills []string) []skills.SkillDescriptor {
	implicitMatched := implicitSkillNames(routing.MatchedSkills, SkillCapabilityModesFromContext(ctx))
	mergedSkillNames := mergeSkillNames(implicitMatched, userSkills)
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

// routeAndActivateSkills performs routing and activates matched skills;
// clarification is not handled here — the Conductor decides via ask_user
// (ADR-012).
func (o *Orchestrator) routeAndActivateSkills(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (context.Context, *router.RoutingDecision, []skills.SkillDescriptor, *HandleResult, error) {
	o.logDebug("orchestrator: starting routing")
	// Activity-only notice: phase "routing" keeps it out of the chat rows —
	// the phase meta is the chat-visibility discriminator
	// (specs/contracts/event-catalog.md, "service").
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "routing"})

	var skillDescriptors []skills.SkillDescriptor
	if o.skillManager != nil {
		// Auto-only catalog (specs/domains/capabilities.md): manual skills are
		// invisible to router matching; the task-context snapshot decides.
		skillDescriptors = implicitSkillDescriptors(o.skillManager.List(), SkillCapabilityModesFromContext(ctx))
	}

	// /skill-name invocations were stripped from the message; restore the
	// skill context for the router only (same logic as resolveTaskMessage).
	routingMessage := o.resolveTaskMessage(message, opts.UserSkills)

	// Convert github.com/v0lka/sp4rk/skills.SkillDescriptor to github.com/v0lka/sp4rk/agent/router.SkillDescriptor
	routerSkills := make([]router.SkillDescriptor, len(skillDescriptors))
	for i, sd := range skillDescriptors {
		routerSkills[i] = router.SkillDescriptor{Name: sd.Name, Description: sd.Description}
	}

	routing, err := o.router.Route(ctx, routingMessage, availableTools, o.historySnapshot(), routerSkills)
	if err != nil {
		// Model Profiles degradation: with essential-tools narrowing active
		// and the routing JSON unparseable even after the router's repair
		// retry, fail safe — continue with a default routing decision (the
		// static tool filter then applies).
		if errors.Is(err, router.ErrRoutingParse) && o.modelProfilesEssentialToolsEnabled() {
			if o.logger != nil {
				o.logger.Warn("orchestrator: routing decision unparseable after repair retry; continuing with default routing",
					"error", err)
			}
			if o.emitter != nil {
				o.emitter.ServiceWithMeta(
					"Routing fallback: unparseable routing JSON — continuing with default routing",
					map[string]any{
						"phase":    "orchestration",
						"fallback": "routing_parse",
						"error":    err.Error(),
					},
				)
			}
			routing = &router.RoutingDecision{
				Domain:     router.DomainGeneral,
				Complexity: defaultResumeComplexity,
			}
		} else {
			o.logDebug("orchestrator: routing failed", "error", err)
			// Commit-point semantics (mirrors reactivateContinuationTask):
			// FailTask may only flip a LIVE task row — a fresh task (already
			// in_progress) or an already-reactivated anchor (the goal path
			// reactivates before routing). A normal-path continuation has not
			// been reactivated yet, so its restored status must stay
			// untouched: flipping a terminal anchor to failed made the
			// session report an unfinished task forever and dead-ended the
			// manager's fresh-retry fallback on the resume banner.
			if pbb, ok := bb.(PersistableBlackboard); ok && (opts.TaskID == "" || opts.Goal) {
				pbb.FailTask()
			}
			return ctx, nil, nil, nil, fmt.Errorf("routing failed: %w", err)
		}
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("conductor", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// Activate matched skills (merge router-matched + user-specified).
	var activeSkillDescriptors []skills.SkillDescriptor
	// Routing-matched names are re-filtered through the auto-only rule as
	// defense-in-depth (specs/domains/capabilities.md); a RESTORED decision
	// may predate a capability change.
	implicitMatched := implicitSkillNames(routing.MatchedSkills, SkillCapabilityModesFromContext(ctx))
	mergedSkillNames := mergeSkillNames(implicitMatched, opts.UserSkills)
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
		}
	}

	// Clarification was removed in ADR-012: the Conductor asks via ask_user;
	// Router.NeedsClarification is ignored.

	return ctx, routing, activeSkillDescriptors, nil, nil
}

// finalizeResult persists the routing decision and builds the HandleResult.
// History updates happen centrally in recordConversationOutcome (deferred in
// HandleMessage), keeping them uniform across terminal outcomes; execResult
// must never be nil (callers guard with a zero-value fallback).
func (o *Orchestrator) finalizeResult(bb orchestration.Blackboard, routing *router.RoutingDecision, execResult *orchestration.ExecutionResult) *HandleResult {
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

	if plan := bb.GetPlan(); plan != nil {
		result.Plan = plan
	}

	o.logDebug("orchestrator: handle_message completed")

	return result
}

// persistTaskOutcome maps the typed execution status onto the persisted task
// status: paused/partial stay resumable, failed/aborted are marked failed
// (also resumable), and only genuine success closes the task as completed.
func persistTaskOutcome(pbb PersistableBlackboard, execResult *orchestration.ExecutionResult) {
	switch execResult.Status {
	case orchestration.ExecutionStatusPaused:
		// Cooperative pause = recoverable checkpoint; the trajectory was
		// already flushed by RunConductor, so the checkpoint is live.
		pbb.PauseTask()
	case orchestration.ExecutionStatusPartial, orchestration.ExecutionStatusCancelled:
		// Keep in_progress — resumable; the manager persists cancellation itself.
	case orchestration.ExecutionStatusFailed, orchestration.ExecutionStatusAborted:
		pbb.FailTask()
	default:
		// Success or legacy empty status: record the output as the final
		// result, then CompleteTask persists it to tasks.final_output
		// (specs/domains/memory/blackboard.md).
		pbb.SetFinalResult(execResult.Output)
		pbb.CompleteTask(execResult.AttemptCount)
	}
}
