package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrE2SGoalConflict is returned by HandleMessage when both HandleOptions.E2S
// and HandleOptions.Goal are set. The two modes own competing early-return
// branches of the same request, so silently preferring one would hide the
// caller's wiring mistake — the conflict is surfaced as an explicit error.
var ErrE2SGoalConflict = errors.New("e2s: E2S and Goal modes are mutually exclusive — set exactly one of HandleOptions.E2S / HandleOptions.Goal")

// ErrE2SModeDisabled is returned by HandleMessage when HandleOptions.E2S is
// set while the effective E2S availability is off (experimental.enabled is
// false — the builder maps it onto the master toggle). It is defense in depth
// behind the frontend API gate, which already rejects a gated send before any
// side effect: a direct core caller cannot run E2S while the mode is disabled.
var ErrE2SModeDisabled = errors.New("e2s: E2S mode is disabled — enable experimental features to use it")

// e2sStrippedToolNames are the plan-workflow tools removed from the E2S
// available-tool catalog. E2S replaces the plan/roadmap machinery with the
// explicit state Σ + checklist: declare_plan/execute_plan would resurrect a
// parallel plan workflow over the same task, declare_step_complete targets
// plan steps that cannot exist here, update_checklist writes the per-step
// todo list (in E2S the Σ "checklist" core key IS the checklist, and the
// tool's StepTodoUpdate event has no consumer here), and reflect replays a
// trajectory the E2S protocol already externalizes into Σ. (Goal-only tools
// are stripped by tools.StripGoalModeTools, applied alongside this set.)
//
// These names without a core constant (declare_plan, execute_plan, reflect)
// are referenced as literals because no constant is exported for them — the
// same convention as verifierExcludedToolNames.
var e2sStrippedToolNames = map[string]struct{}{
	"declare_plan":          {},
	"execute_plan":          {},
	ToolDeclareStepComplete: {},
	ToolUpdateChecklist:     {},
	"reflect":               {},
}

// stripE2SUnavailableTools removes the plan-workflow tools from a descriptor
// list, leaving everything else (file/search/execute tools, delegate,
// cancel_delegation, memory, ask_user …) available to the E2S action
// dispatch.
func stripE2SUnavailableTools(in []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	if len(in) == 0 {
		return in
	}
	out := make([]sdktools.ToolDescriptor, 0, len(in))
	for _, d := range in {
		if _, stripped := e2sStrippedToolNames[d.Name]; stripped {
			continue
		}
		out = append(out, d)
	}
	return out
}

// e2sRegistryAdapter adapts the orchestrator's tool-execution surface to the
// e2s.Loop Registry contract. The catalog (List) is the FILTERED E2S view —
// goal-only and plan-workflow tools never reach the model — while Execute
// dispatches to the SAME agent.ToolExecutor the Conductor's executor uses
// (the per-session policy view), so every security gate (group policies,
// judge, HITL confirmation, verify-on-edit) applies to the target tool
// exactly as in a Conductor run. Dispatching a stripped name is rejected
// fail-closed: the catalog is the contract, and a hallucinated plan/goal tool
// call must not silently execute the real tool.
type e2sRegistryAdapter struct {
	inner   agent.ToolExecutor
	descs   []sdktools.ToolDescriptor
	allowed map[string]struct{}
}

// newE2SRegistryAdapter builds the filtered registry view over the given
// descriptors (already stripped of goal-only and plan tools).
func newE2SRegistryAdapter(inner agent.ToolExecutor, descs []sdktools.ToolDescriptor) *e2sRegistryAdapter {
	allowed := make(map[string]struct{}, len(descs))
	for _, d := range descs {
		allowed[d.Name] = struct{}{}
	}
	return &e2sRegistryAdapter{inner: inner, descs: descs, allowed: allowed}
}

func (a *e2sRegistryAdapter) List() []sdktools.ToolDescriptor {
	return a.descs
}

func (a *e2sRegistryAdapter) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	if _, ok := a.allowed[name]; !ok {
		return sdktools.ErrorResult("e2s: tool %q is not available in E2S mode (plan and goal-loop tools are disabled; the state Σ and its checklist replace them)", name), nil
	}
	if a.inner == nil {
		return sdktools.ErrorResult("e2s: no tool registry configured"), nil
	}
	return a.inner.Execute(ctx, name, input)
}

func (a *e2sRegistryAdapter) IsToolUntrusted(name string) bool {
	if a.inner == nil {
		return false
	}
	return a.inner.IsToolUntrusted(name)
}

// ToolSource mirrors agent.ToolExecutor.GetToolSource ("core" for builtins,
// the MCP source tag mcp:<server> otherwise) so the loop emits the real
// tool-call source — the UI renders genuine MCP tools as MCP and everything
// else as built-in instead of badge-stamping every E2S action.
func (a *e2sRegistryAdapter) ToolSource(name string) string {
	if a.inner == nil {
		return "core"
	}
	return a.inner.GetToolSource(name)
}

// e2sStatePersistingEmitter forwards every event to the wrapped core Emitter
// and hooks the e2s_state snapshots: each applied state patch (emitted by the
// loop after every turn) triggers the onState callback, which the orchestrator
// uses to persist Σ per step. Embedding the Emitter interface forwards all
// agent.Events + core methods; only E2SState is overridden. The wrapper
// satisfies both the loop's e2s.Emitter (a structural subset of
// agent.Events) and its optional e2s.StateEmitter capability.
type e2sStatePersistingEmitter struct {
	Emitter
	onState func(data map[string]any)
}

func (e *e2sStatePersistingEmitter) E2SState(data map[string]any) {
	e.Emitter.E2SState(data)
	if e.onState != nil {
		e.onState(data)
	}
}

// e2sStatePersister is the optional TaskPersistence capability behind E2S
// state checkpointing: the backend task adapter implements it (mirroring
// PersistGoalState/LoadGoalState) with the task_e2s_state table. Core stays
// decoupled — the orchestrator type-asserts its task store and degrades to a
// no-op (no persistence, run still executes) when the capability is absent,
// so core compiles and tests pass independently of the backend layer.
type e2sStatePersister interface {
	// PersistE2SState persists the E2S state checkpoint for a task so a
	// paused/active run survives app restart and resumes with the same Σ.
	PersistE2SState(taskID string, state *e2s.E2SState) error
	// LoadE2SState restores the persisted E2S state for a task. Returns
	// nil, nil when nothing has been persisted.
	LoadE2SState(taskID string) (*e2s.E2SState, error)
}

// persistE2SStateBestEffort checkpoints the E2S state for the current task.
// Best-effort, mirroring persistGoalStateBestEffort: without a task store,
// PersistableBlackboard, task id, or the optional persister capability it is
// a no-op, and a persistence failure is logged but never propagated — losing
// the checkpoint degrades resumability only, not the current run.
func (o *Orchestrator) persistE2SStateBestEffort(bb orchestration.Blackboard, es *e2s.E2SState) {
	if es == nil || o.taskStore == nil {
		return
	}
	persister, ok := o.taskStore.(e2sStatePersister)
	if !ok {
		return
	}
	pbb, ok := bb.(PersistableBlackboard)
	if !ok {
		return
	}
	taskID := pbb.TaskID()
	if taskID == "" {
		return
	}
	if err := persister.PersistE2SState(taskID, es); err != nil {
		o.logDebug("e2s_loop: failed to persist state (best-effort)", "taskID", taskID, "error", err)
	}
}

// loadE2SResumeState restores a persisted, resumable E2S state for the task
// on the blackboard. Returns nil when there is nothing to resume: no task
// store / capability / task id, no persisted state, a terminal status (met /
// failed / cancelled — the run is over), or a core-schema mismatch (the
// persisted Σ predates a schema change and must not be patched under the new
// rules; the task resumes as a fresh E2S run instead).
func (o *Orchestrator) loadE2SResumeState(bb orchestration.Blackboard) *e2s.E2SState {
	if o.taskStore == nil {
		return nil
	}
	persister, ok := o.taskStore.(e2sStatePersister)
	if !ok {
		return nil
	}
	pbb, ok := bb.(PersistableBlackboard)
	if !ok {
		return nil
	}
	taskID := pbb.TaskID()
	if taskID == "" {
		return nil
	}
	es, err := persister.LoadE2SState(taskID)
	if err != nil {
		o.logDebug("e2s_loop: failed to load persisted state", "taskID", taskID, "error", err)
		return nil
	}
	if es == nil || !e2sStatusResumable(es.Status) {
		return nil
	}
	if es.Schema != "" && es.Schema != e2s.SchemaFingerprint() {
		o.logInfo("e2s_loop: persisted state predates the current core schema — starting a fresh state", "taskID", taskID)
		return nil
	}
	return es
}

// e2sStatusResumable reports whether a persisted domain status allows re-entry
// into the E2S loop. Terminal statuses (met, failed, cancelled) never resume;
// active marks a run interrupted by shutdown/cancel, paused a cooperative
// pause checkpoint.
func e2sStatusResumable(s e2s.StateStatus) bool {
	return s == e2s.StateStatusActive || s == e2s.StateStatusPaused
}

// e2sDelegateDirective is the "## Delegation" section body injected into the
// E2S system prompt. Delegation in E2S goes through the SAME delegate tool
// and conductorLauncher as a Conductor run (injected via
// tools.WithDelegationLauncher): subagent isolation, budgets, and security
// gates are identical; only the caller is the E2S loop instead of the ReAct
// executor.
const e2sDelegateDirective = `The delegate tool launches specialized subagents through the same machinery a Conductor run uses. Each delegation carries an id, a short summary, a full task description (What/How/Where/Acceptance criteria), and optional tool-group grants, dependencies, and agent profile. Prefer delegate for self-contained work that benefits from isolation or parallelism (exploration, review, focused implementation); when the observation returns, distill the outcome into your state_patch (findings/decisions) — the raw delegation output is NOT kept between turns.`

// runE2SLoop is the E2S-mode driver, entered from HandleMessage when
// HandleOptions.E2S is set — on a fresh task (TaskID == "") and on a
// continuation (TaskID != ""), mirroring the goal-mode early return. The
// single-flight guard in HandleMessage is already held. It NEVER calls
// routeOrContinue or runConductor: routing, plans, and the ReAct loop are all
// replaced by the explicit-state protocol (bounded O(1) context per turn).
func (o *Orchestrator) runE2SLoop(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
) (*HandleResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// A continuation may carry a resumable E2S checkpoint (e.g. the user
	// re-sent into a paused E2S task); resume it with the same Σ. Otherwise
	// start a fresh state. On the resume branch the incoming message is the
	// user's follow-up and is delivered as the turn-1 observation — Σ
	// precedence would otherwise drop it (see e2sResumeNote).
	if es := o.loadE2SResumeState(bb); es != nil {
		o.logInfo("e2s_loop: resuming persisted state", "turn", es.TurnCount, "status", es.Status)
		return o.runE2SWithState(ctx, message, bb, availableTools, opts, es, e2sResumeNote(message))
	}

	objective := o.augmentWithAttachments(message, bb)
	es := e2s.NewE2SState(objective, time.Now().UTC())
	o.logInfo("e2s_loop: fresh state seeded")
	return o.runE2SWithState(ctx, message, bb, availableTools, opts, &es, "")
}

// resumeE2SLoop re-enters the E2S loop from a persisted, non-terminal
// checkpoint. It is the Resume-side mirror of runE2SLoop's continuation
// branch, called by Orchestrator.Resume when the restored task carries an
// E2S state. Σ continues exactly where the pause stopped it.
//
// Deliberate asymmetry with the HandleMessage gate: resume does NOT re-check
// the E2S availability (o.config.E2S.Enabled). The gate guards *arming* a new
// E2S send; a task already checkpointed with a non-terminal Σ is existing work,
// and refusing to resume it would strand the task with no way to continue the
// run. Skills requested at send time are likewise not re-resolved here — the
// E2S session's Σ is the continuation point, and (unlike the plan/goal resume
// paths) there is no restored message to re-parse skill refs from, and the
// fresh send's HandleOptions.UserAgents ("#agent" → "## Requested Subagents")
// is not persisted, so only the Available Subagents roster rebuilt from the
// live agent catalog carries over.
//
// A user follow-up (nudge), when present, is delivered as the turn-1
// observation (e2sResumeNote) so the resumed run reacts to it rather than
// silently continuing the old objective. Staged image attachments ARE
// restored (mirroring the Conductor resume path: imageBlocksForRequest over
// the history, re-attached to every turn). The restored trajectory
// (resumeSteps) is intentionally NOT consumed: in E2S the Σ — not a replayed
// trajectory — is the model's memory, so replaying prior steps would add
// nothing.
func (o *Orchestrator) resumeE2SLoop(
	ctx context.Context,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	routing *router.RoutingDecision,
	es *e2s.E2SState,
	nudge string,
) (*HandleResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if es == nil {
		return nil, errors.New("e2s_loop: resume without a persisted state")
	}
	_ = routing // routing is preserved by Resume's caller; E2S runs unrouted
	o.logInfo("e2s_loop: resuming from checkpoint", "turn", es.TurnCount, "status", es.Status, "nudge", nudge != "")
	// Restore staged image content blocks for the resumed run (mirrors the
	// Conductor resume path): the fresh HandleOptions below carries no
	// PendingImages, so an image-bearing task resumed into E2S would
	// otherwise lose its images entirely. The blocks must be IMAGE-ONLY:
	// E2S sends Σ + the observation as the user message's Content, and a
	// provider renders ContentBlocks INSTEAD of Content whenever they carry a
	// text block (llm.NormalizeContentBlocks) — so wrapping the images with a
	// text block here would silently drop the model's entire memory. With an
	// image-only list the provider prepends Content as the text block, so both
	// the images and Σ reach the model.
	resumeOpts := HandleOptions{}
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		if imageBlocks := imageBlocksForRequest(o.historySnapshot(), origReq); len(imageBlocks) > 0 {
			resumeOpts.PendingImages = imageBlocks
		}
	}
	return o.runE2SWithState(ctx, bb.GetOriginalRequest(), bb, availableTools, resumeOpts, es, e2sResumeNote(nudge))
}

// e2sResumeNote renders the turn-1 observation for a resumed E2S run. The
// E2S turn exposes no separate message channel (the model sees only Σ + O),
// so — unlike the plan/goal resume paths that append the follow-up to the
// resumed task message — the nudge is delivered as the first observation.
//
// With a user follow-up, the note carries it verbatim (a new directive from
// the user, not tool output). Without one (a plain Resume), the note still
// informs the model of the two facts a continuation must know: the budget
// was refreshed (the [turn N of M] header counts the NEW budget while Σ's
// recorded turns are cumulative) and Σ — not any remembered trajectory — is
// the continuation point.
func e2sResumeNote(nudge string) string {
	nudge = strings.TrimSpace(nudge)
	if nudge == "" {
		return "The run was interrupted and has been resumed with a fresh turn budget (the [turn N of M] header counts the new budget). " +
			"Your state Σ below is the continuation point — everything not recorded there is forgotten. " +
			"Build on what Σ already holds and wrap the task up efficiently."
	}
	return "User follow-up on resume (a new directive from the user, not tool output; your turn budget was also refreshed):\n\n" + nudge
}

// runE2SWithState is the shared body of runE2SLoop / resumeE2SLoop: it wires
// the delegation seam, the filtered registry, the trajectory store, and the
// per-step Σ persistence, runs e2s.Loop to completion, and finalizes the
// HandleResult.
func (o *Orchestrator) runE2SWithState(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	opts HandleOptions,
	es *e2s.E2SState,
	resumeNote string,
) (*HandleResult, error) {
	// Conductor deps carry the full caller stack (model overrides already
	// applied by HandleMessage's ApplyRequestOverrides), the tool executor,
	// model/agent/skill resolvers, and the universal pause checker — the
	// delegation launcher reuses them unchanged.
	deps := o.buildConductorDeps(nil, nil)

	// Delegation seam: the SAME conductorLauncher a Conductor run uses, with
	// an INERT plan state (newPlanRunState(false) — no plan workflow ever
	// activates in E2S). Only the pair the delegate tool reads is injected:
	// registry + launcher. PlanChecker/PlanPublisher/ReflectionRunner/
	// StepCompleteFunc are deliberately NOT injected — their tools are
	// stripped below, and a stray call fails closed on the missing context
	// dependency instead of resurrecting the plan workflow.
	planState := newPlanRunState(false)
	registry := tools.NewDelegationRegistry()
	launcher := tools.DelegationLauncher(&conductorLauncher{deps: deps, bb: bb, planState: planState})
	if o.e2sLauncher != nil {
		launcher = o.e2sLauncher
	}
	ctx = tools.WithDelegationRegistry(ctx, registry)
	ctx = tools.WithDelegationLauncher(ctx, launcher)
	// Also inject the registry under the sp4rk orchestration context key so
	// subagent executors launched from E2S inherit a working finish guard
	// (their SetFinishGuard resolves the registry via this key — the same
	// reason RunConductor sets it before building the launcher).
	ctx = orchestration.WithDelegationRegistry(ctx, registry)

	// Context parity with RunConductor (the E2S loop replaces the executor,
	// so every ctx value the engine — or its tools — set must be mirrored
	// here):
	//   - the subagent profile resolver (delegate's `agent` field resolves
	//     profiles; without it profile-targeted delegation fails closed);
	//   - the agent roster + explicit #mentions (the prompt sections below
	//     and the delegate tool both read them);
	//   - routing seeds: a fresh E2S handle never passes routeOrContinue, so
	//     domain/complexity would be unset and buildSubAgentTask's default
	//     budget (complexity × steps-per-complexity) would collapse to 0 —
	//     seed the same neutral pair the synthesized RoutingDecision below
	//     carries. The seed is authoritative: an E2S run is unrouted, so any
	//     routing restored by a resume is intentionally overwritten here;
	//   - the blackboard-backed stores so read_step_output / list_step_outputs
	//     / read_final_result / store_fact / search_facts / read_attachment
	//     work in E2S exactly as in a Conductor run (the catalog advertises
	//     them; without the stores every call errors "store not available");
	//   - the task context so the strict judge sees the actual task rather
	//     than an empty string on user-confirm escalations.
	ctx = tools.WithAgentResolver(ctx, deps.agentResolver)
	ctx = o.enrichAgentContext(ctx, opts.UserAgents)
	ctx = WithDomain(ctx, "general")
	ctx = WithComplexity(ctx, defaultResumeComplexity)
	ctx = agent.WithStepOutputStore(ctx, orchestration.NewStepOutputStore(bb))
	ctx = agent.WithFactStore(ctx, orchestration.NewFactStore(bb))
	ctx = agent.WithAttachmentStore(ctx, orchestration.NewAttachmentStore(bb))
	ctx = agent.WithFinalResultStore(ctx, orchestration.NewFinalResultStore(bb))
	ctx = sdktools.WithTaskContext(ctx, message)

	// Filtered catalog: goal-only tools (propose_goal, declare_goal_status,
	// declare_verification) and plan-workflow tools never reach the model;
	// Execute stays on the real registry (all security gates intact) and
	// rejects stripped names fail-closed.
	// Model Profiles essential-tools narrowing applies to E2S too — full parity
	// with the Conductor path (goal mode is the only documented exception):
	// the same static selection (always-present list + protected
	// orchestration tools + MCP tools + the turn-scoped delegate guarantee)
	// narrows the E2S catalog exactly once here, before stripping.
	e2sTools := stripE2SUnavailableTools(tools.StripGoalModeTools(
		o.applyModelProfilesToolFilter(availableTools, modelProfilesAgentGuaranteedTools(ctx)...)))

	// Trajectory: same composite store as a Conductor run (in-memory for
	// synchronous reads + best-effort DB persistence), synced by the loop
	// after every step and flushed once here at the end.
	trajHolder := &trajectoryHolder{}
	taskID := ""
	if pbb, ok := bb.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	// Delegation spec sink (mirrors RunConductor's wiring): persist every
	// delegation registered in this run as a full spec so the auto-resume
	// wave can rebuild in-flight delegations after a pause or shutdown.
	// Without the sink a resumed E2S run silently drops its delegated work.
	if taskID != "" && deps.taskStore != nil {
		var unitLedger units.Ledger
		if pbb, ok := bb.(PersistableBlackboard); ok {
			unitLedger = NewBlackboardLedger(pbb)
		}
		wireDelegationSpecSink(registry, unitLedger, "", taskID, deps.taskStore, deps.logger)
	}
	trajStore := newCompositeTrajectoryStore(trajHolder, taskID, deps.taskStore, deps.logger)
	defer trajStore.Flush()

	// Emitter: forward everything to the session emitter and checkpoint Σ on
	// every applied state patch (the loop emits e2s_state after each turn).
	emitter := o.emitter
	if emitter == nil {
		emitter = &noopEmitter{}
	}
	snapshotBase := *es
	perStep := &e2sStatePersistingEmitter{
		Emitter: emitter,
		onState: func(data map[string]any) {
			sigma, _ := data["state"].(map[string]any)
			// Cumulative applied patches across ALL runs of the task (the
			// persisted checkpoint's continuation point). Falls back to the
			// run-local turn for emitters predating the total_turns field.
			turn, _ := data["total_turns"].(int)
			if turn == 0 {
				turn, _ = data["turn"].(int)
			}
			snapshot := snapshotBase
			snapshot.Sigma = sigma
			snapshot.TurnCount = turn
			snapshot.Status = e2s.StateStatusActive
			snapshot.UpdatedAt = time.Now().UTC()
			o.persistE2SStateBestEffort(bb, &snapshot)
		},
	}

	// Active skills: explicitly requested skills (/name refs) are resolved and
	// injected both into the E2S system prompt (skills sections) and into the
	// context (so read_skill_resource resolves them during action dispatch).
	skillSections, activeSkills := o.resolveE2SSkills(opts.UserSkills)
	if activeSkills != nil {
		ctx = WithActiveSkills(ctx, activeSkills)
	}

	// Model metadata for the per-call output limit and the flat context-fill
	// window. ResolveLocal keeps this network-free (mirrors
	// emitInitialContextFill).
	var maxTokens, contextWindow int
	if o.modelRegistry != nil {
		if meta, ok := o.modelRegistry.ResolveLocal(deps.model); ok {
			maxTokens = meta.OutputLimit
			contextWindow = meta.ContextWindow
		}
	}

	e2sCfg := o.e2sSettings()
	// Model Profiles loop-hardening parity: the executor's circuit breaker gets a
	// tighter repeat-nudge threshold under the profile; the E2S anti-spin
	// nudge is the same concept, so the override applies here too (the
	// abort threshold keeps its fail-safe strictly-greater ordering via
	// Config.withDefaults).
	spinNudge := e2sCfg.RepeatNudgeThreshold
	if sc := o.modelProfilesSettings(); sc.Enabled && sc.LoopHardening.Enabled && sc.LoopHardening.RepeatNudgeThreshold > 0 {
		// The profile override must stay strictly below the configured abort
		// threshold: at or above it Config.withDefaults would silently raise
		// abort to nudge+1, reintroducing the divergence the e2s config
		// validation rejects up front. Fall back to the configured nudge
		// (which the config guarantees is < abort) rather than diverge.
		if sc.LoopHardening.RepeatNudgeThreshold < e2sCfg.RepeatAbortThreshold {
			spinNudge = sc.LoopHardening.RepeatNudgeThreshold
		} else {
			o.logWarn("e2s_loop: ignoring Model Profiles repeat-nudge override — not strictly below the configured abort threshold",
				"override", sc.LoopHardening.RepeatNudgeThreshold,
				"abort_threshold", e2sCfg.RepeatAbortThreshold,
				"using_nudge", e2sCfg.RepeatNudgeThreshold)
		}
	}
	cfg := e2s.Config{
		Model:               deps.model,
		MaxTokens:           maxTokens,
		ContextWindowTokens: contextWindow,
		// Behavior knobs from the e2s.* config section. Zero values (e.g. a
		// hand-built OrchestratorConfig) fall back to the core/e2s defaults
		// via Config.withDefaults, so the documented defaults and the
		// compiled-in ones cannot fight.
		MaxSteps:            e2sCfg.MaxSteps,
		StateByteLimit:      e2sCfg.StateByteLimit,
		PatchRetries:        e2sCfg.PatchRetries,
		MaxObservationChars: e2sCfg.MaxObservationChars,
		SpinNudgeThreshold:  spinNudge,
		SpinAbortThreshold:  e2sCfg.RepeatAbortThreshold,
		// ResumeState seeds the ENTIRE state (fresh runs pass the canonically
		// seeded NewE2SState; resumed runs pass the persisted checkpoint), so
		// a continuation inherits the accumulated Σ — core keys and
		// extensions — not just extension seeds. Task remains as the
		// no-checkpoint fallback for the objective.
		Task: bb.GetOriginalRequest(),
		// Image attachments staged for this send ride on every turn's user
		// message (each turn is a fresh dialog; see e2s.Config.ContentBlocks).
		ContentBlocks:     opts.PendingImages,
		ResumeState:       es,
		ResumeNote:        resumeNote,
		WorkspacePath:     sdktools.WorkspacePathFrom(ctx),
		TempDir:           sdktools.TempDirFrom(ctx),
		DelegateDirective: e2sDelegateDirective,
		Skills:            skillSections,
		Trajectory:        trajStore,
		// Shared tool-result cache (same instance as the Conductor's
		// executor): E2S truncation becomes cache-on-truncate with a
		// tool_result_read recovery nudge, and tool_result_read actions
		// resolve through the dispatch context.
		ToolCache:    deps.toolCache,
		PauseChecker: deps.pauseChecker,
		// Conductor-parity knobs (review fix cycle): the resolved reasoning
		// effort (per-message override / Model Profiles sampling), the
		// config-gated injection-defense directive, the subagent prompt
		// sections (roster + explicit #mentions), the Model Profiles Lite prompt
		// swap, the verify-on-edit hook, and the finish-join guard over
		// pending async delegations.
		ReasoningEffort:    deps.reasoningEffort,
		InjectionDefense:   o.config.InjectionDefenseEnabled,
		AgentSections:      formatAvailableAgents(ctx) + formatRequestedAgents(ctx),
		SystemPrompt:       e2sCoreDirective(ctx),
		EditVerify:         deps.verifyOnEdit,
		EditVerifyMaxChars: deps.verifyOnEditMaxOutputChars,
		FinishGuard:        e2sFinishGuard(registry),
		Logger:             o.logger,
	}

	loop := e2s.New(deps.llm, newE2SRegistryAdapter(deps.toolExec, e2sTools), perStep, cfg)
	res, runErr := loop.Run(ctx)

	// Final Σ checkpoint: the loop's Snapshot is the authoritative terminal
	// state (turn count, domain status). Persisted even on error paths —
	// a paused run's checkpoint is what a later Resume re-enters.
	if res != nil {
		final := res.Snapshot
		final.Status = e2sDomainStatus(res)
		// Keep the Σ "status" core key in step with the typed status so a
		// persisted terminal/paused checkpoint is self-consistent (the UI and
		// resume read the typed field, but the Σ mirror should never disagree).
		if final.Sigma != nil {
			final.Sigma[e2s.CoreKeyStatus] = string(final.Status)
		}
		if final.CreatedAt.IsZero() {
			final.CreatedAt = time.Now().UTC()
		}
		o.persistE2SStateBestEffort(bb, &final)
	}

	// E2S runs without routing (no router call — the loop's context is
	// bounded and domain-agnostic). Synthesize the neutral routing decision
	// the rest of the pipeline expects, and persist it like any other mode.
	routing := &router.RoutingDecision{Domain: "general", Complexity: defaultResumeComplexity}
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	output := message
	status := orchestration.ExecutionStatusFailed
	if res != nil {
		if res.Answer != "" {
			output = res.Answer
		}
		status = e2sExecutionStatus(res)
		// Empty-answer terminations surface an explicit, honest outcome
		// instead of echoing the original user message: the frontend renders
		// a non-empty output as the assistant's final message, so a finished
		// run that produced no answer (step_limit / spin_stop, the two
		// statuses rewritten below) must not return the user's own words as
		// its answer. Paused/failed runs keep `output = message`, but the UI
		// surfaces their Resume/error affordance rather than a final message.
		// Both rewritten statuses persist the Σ checkpoint, but only
		// step_limit is resumable: it maps to the
		// non-terminal active status, whereas spin_stop maps to failed
		// (terminal — e2sStatusResumable excludes it), so the spin_stop
		// message must not promise a Resume that re-enters the E2S loop.
		if res.Answer == "" {
			switch res.Status {
			case e2s.RunStatusStepLimit:
				output = fmt.Sprintf(
					"E2S run stopped: the turn budget (%d steps) was exhausted before the task completed. "+
						"The working state Σ was preserved with %d recorded turns — use Resume to continue with a fresh budget.",
					e2s.EffectiveMaxSteps(e2s.Config{MaxSteps: e2sCfg.MaxSteps}), res.Snapshot.TurnCount)
			case e2s.RunStatusSpinStop:
				output = fmt.Sprintf(
					"E2S run aborted: the same action was repeated past the anti-spin limit without progress. "+
						"This abort is terminal and cannot be resumed — the %d-turn Σ snapshot is kept on the task "+
						"for the record only. Restart the task with a different approach.",
					res.Snapshot.TurnCount)
			}
		}
	}
	execResult := &orchestration.ExecutionResult{Output: output, Status: status}

	// Cooperative pause: clean, recoverable checkpoint — surface it, persist
	// the task paused (finalizeResult → persistTaskOutcome), and return a nil
	// error so the backend offers Resume instead of an error (mirrors the
	// Conductor's agent.ErrPaused handling).
	if runErr != nil && errors.Is(runErr, e2s.ErrPaused) {
		o.emitSessionPaused()
		runErr = nil
	}

	result := o.finalizeResult(bb, routing, execResult)
	if runErr != nil {
		// Terminal failure (LLM error, cancellation): the HandleResult still
		// carries the checkpointed output/status; the error tells the manager
		// the run did not complete cleanly.
		return result, runErr
	}
	return result, nil
}

// e2sCoreDirective selects the E2S core system directive: the Lite variant
// when the Model Profiles prompt profile is active on the context (set by
// prepareRequestContext before the E2S branch — the profile keys ride the
// same request context), the compiled-in full directive otherwise ("" lets
// the e2s package apply its own default).
func e2sCoreDirective(ctx context.Context) string {
	if modelProfilesLiteFromCtx(ctx) {
		return prompts.E2SSystemLite
	}
	return ""
}

// e2sFinishGuard mirrors the sp4rk Conductor's finish-join guard for the
// E2S loop: finish is vetoed while async delegations are still pending, so
// the run cannot silently abandon background subagents (the model must
// cancel or await them, then finish).
func e2sFinishGuard(registry *tools.DelegationRegistry) func(context.Context) error {
	return func(context.Context) error {
		if pending := registry.ListPending(); len(pending) > 0 {
			return fmt.Errorf("you have %d pending async delegation(s): %s. Call cancel_delegation for each if you no longer need them, or wait for them to complete via read_step_output before calling finish", len(pending), strings.Join(pending, ", "))
		}
		return nil
	}
}

// resolveE2SSkills resolves explicitly requested skill names into prompt
// sections and the context-injectable ActiveSkills. Unknown names are skipped
// silently (the router-side activation path has already logged them); with no
// skill manager or no matches both returns are nil/empty.
func (o *Orchestrator) resolveE2SSkills(names []string) ([]e2s.SkillSection, *ActiveSkills) {
	if len(names) == 0 || o.skillManager == nil {
		return nil, nil
	}
	sections := make([]e2s.SkillSection, 0, len(names))
	var active []*skills.Skill
	for _, name := range names {
		s, ok := o.skillManager.Get(name)
		if !ok || s == nil {
			continue
		}
		sections = append(sections, e2s.SkillSection{
			Name:        s.Metadata.Name,
			Description: s.Metadata.Description,
			Body:        s.Body,
		})
		active = append(active, s)
	}
	if len(sections) == 0 {
		return nil, nil
	}
	return sections, &ActiveSkills{Skills: active}
}

// e2sExecutionStatus maps the loop's terminal disposition onto the executor
// status vocabulary used by HandleResult/finalizeResult.
func e2sExecutionStatus(res *e2s.Result) orchestration.ExecutionStatus {
	if res == nil {
		return orchestration.ExecutionStatusFailed
	}
	switch res.Status {
	case e2s.RunStatusFinished:
		return orchestration.ExecutionStatusSuccess
	case e2s.RunStatusPaused:
		return orchestration.ExecutionStatusPaused
	case e2s.RunStatusCanceled:
		return orchestration.ExecutionStatusCancelled
	case e2s.RunStatusStepLimit:
		// Budget exhaustion is execution-INCOMPLETE, not failed: persistTaskOutcome
		// keeps a partial task in_progress (resumable), and a later resume
		// re-enters the loop with the accumulated Σ plus a fresh turn budget.
		return orchestration.ExecutionStatusPartial
	default: // spin_stop, failed
		return orchestration.ExecutionStatusFailed
	}
}

// e2sDomainStatus maps the loop's terminal disposition onto the persisted
// domain status (e2s.StateStatus): the persisted checkpoint drives later resume
// decisions (e2sStatusResumable).
//
// A context cancellation maps to a NON-terminal status (active), mirroring the
// goal loop: the orchestrator cannot distinguish a user cancel (CancelTask)
// from an app shutdown — both cancel the task context. Terminalizing here would
// make EVERY shutdown-interrupted E2S run un-resumable (its Σ and its mode are
// silently lost on restart). Instead the manager layer decides: a user cancel
// terminalizes the state via abandonE2SIfUnfinished, while a shutdown leaves it
// active (resumable). See runGoalTurns's ctx.Err() branch for the precedent.
func e2sDomainStatus(res *e2s.Result) e2s.StateStatus {
	if res == nil {
		return e2s.StateStatusFailed
	}
	switch res.Status {
	case e2s.RunStatusFinished:
		return e2s.StateStatusMet
	case e2s.RunStatusPaused:
		return e2s.StateStatusPaused
	case e2s.RunStatusCanceled:
		return e2s.StateStatusActive
	case e2s.RunStatusStepLimit:
		// Budget exhaustion is a NON-terminal checkpoint (mirroring the
		// cancel/shutdown mapping above): the accumulated Σ is the valuable
		// artifact, and e2sStatusResumable admits active — so Resume
		// re-enters with the full working state and a fresh turn budget
		// instead of silently seeding a blank Σ.
		return e2s.StateStatusActive
	default: // spin_stop, failed
		return e2s.StateStatusFailed
	}
}
