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
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrE2SGoalConflict is returned when a send sets both E2S and Goal: the two
// modes own competing early-return branches, so the wiring mistake is surfaced
// as an explicit error. See specs/domains/e2s.md.
var ErrE2SGoalConflict = errors.New("e2s: E2S and Goal modes are mutually exclusive — set exactly one of HandleOptions.E2S / HandleOptions.Goal")

// ErrE2SModeDisabled is returned when E2S is requested while the mode is off
// (experimental.enabled false) — defense in depth behind the frontend gate.
// See specs/domains/e2s.md.
var ErrE2SModeDisabled = errors.New("e2s: E2S mode is disabled — enable experimental features to use it")

// e2sStrippedToolNames are the plan-workflow and delegation tools removed from
// the E2S catalog: E2S is self-sufficient (the Σ checklist replaces the plan
// machinery and it does not delegate), so those tools — and the step-store
// readers that only serve launched subagents — have no consumer here. The
// blackboard memory stores (store_fact, search_facts, read_attachment) stay:
// they are E2S's external memory. See specs/domains/e2s.md. Literal names (no
// exported constant) — same convention as verifierExcludedToolNames.
var e2sStrippedToolNames = map[string]struct{}{
	"declare_plan":          {},
	"execute_plan":          {},
	ToolDeclareStepComplete: {},
	ToolUpdateChecklist:     {},
	"reflect":               {},
	"delegate":              {},
	"cancel_delegation":     {},
	"read_step_output":      {},
	"list_step_outputs":     {},
	"read_final_result":     {},
}

// stripE2SUnavailableTools removes e2sStrippedToolNames from a descriptor list.
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

// e2sCorePresetGroups are the groups the default core preset keeps (local
// read/write, execute, remote read). The group ALONE determines membership —
// undeclared groups match nothing, fail-closed per ADR-024. See
// specs/domains/e2s.md.
var e2sCorePresetGroups = map[sdktools.ToolGroup]struct{}{
	sdktools.GroupLocalRead:  {},
	sdktools.GroupLocalWrite: {},
	sdktools.GroupExecute:    {},
	sdktools.GroupRemoteRead: {},
}

// e2sCorePresetTools are the system-group plumbing the E2S protocol depends on
// (memory trio, batch/tool_result_read, ask_user); the rest of the system
// group stays out unless re-included via e2s.tools.allow. See specs/domains/e2s.md.
var e2sCorePresetTools = map[string]struct{}{
	"batch":            {},
	"tool_result_read": {},
	"ask_user":         {},
	"store_fact":       {},
	"search_facts":     {},
	"read_attachment":  {},
}

// filterE2SToolsByConfig applies the e2s.tools narrowing to the raw
// descriptors. It runs BEFORE the goal/plan/delegation stripping and its
// output is the E2S dispatch contract (filtered-out names are rejected
// fail-closed at dispatch). Preset "core" (default; anything unrecognized
// fails closed to core) = the e2sCorePresetGroups plus e2sCorePresetTools;
// "all" = the full surface; allow re-includes excluded names; deny removes
// with PRECEDENCE over both. The input is never mutated. See specs/domains/e2s.md.
func filterE2SToolsByConfig(in []sdktools.ToolDescriptor, cfg BuilderE2SToolsConfig) []sdktools.ToolDescriptor {
	deny := e2sNameSet(cfg.Deny)
	if strings.TrimSpace(cfg.Preset) == E2SToolsPresetAll {
		// Full former surface; only deny still applies.
		if len(deny) == 0 {
			return in
		}
		out := make([]sdktools.ToolDescriptor, 0, len(in))
		for _, d := range in {
			if _, blocked := deny[d.Name]; !blocked {
				out = append(out, d)
			}
		}
		return out
	}
	allow := e2sNameSet(cfg.Allow)
	out := make([]sdktools.ToolDescriptor, 0, len(in))
	for _, d := range in {
		if _, blocked := deny[d.Name]; blocked {
			continue
		}
		if _, ok := e2sCorePresetGroups[d.Group]; ok {
			out = append(out, d)
			continue
		}
		if _, ok := e2sCorePresetTools[d.Name]; ok {
			out = append(out, d)
			continue
		}
		if _, ok := allow[d.Name]; ok {
			out = append(out, d)
		}
	}
	return out
}

// e2sNameSet builds a name set from a config string list (nil-safe; empty
// for an empty list).
func e2sNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

// e2sRegistryAdapter adapts the orchestrator's tool-execution surface to the
// e2s.Loop Registry contract: List exposes the FILTERED catalog, Execute
// dispatches to the same agent.ToolExecutor the Conductor's executor uses
// (every security gate applies), and a stripped name is rejected fail-closed —
// the catalog is the dispatch contract. See specs/domains/e2s.md.
type e2sRegistryAdapter struct {
	inner   agent.ToolExecutor
	descs   []sdktools.ToolDescriptor
	allowed map[string]struct{}
}

// newE2SRegistryAdapter builds the filtered registry view.
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
		return sdktools.ErrorResult("e2s: tool %q is not available in E2S mode (plan, delegation, and goal-loop tools are disabled; the state Σ and its checklist replace them)", name), nil
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
// and hooks the e2s_state snapshots: each applied state patch triggers the
// onState callback, which persists Σ per step. See specs/domains/e2s.md.
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
// state checkpointing, implemented by the backend task adapter; core stays
// decoupled and degrades to no persistence without it. See specs/domains/e2s.md.
type e2sStatePersister interface {
	// PersistE2SState persists the E2S state checkpoint for a task.
	PersistE2SState(taskID string, state *e2s.E2SState) error
	// LoadE2SState restores the persisted state; nil, nil when none.
	LoadE2SState(taskID string) (*e2s.E2SState, error)
}

// persistE2SStateBestEffort checkpoints the E2S state for the current task:
// a no-op without the persistence capability, and a persistence failure is
// logged but never propagated (it degrades resumability only). See
// specs/domains/e2s.md.
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

// loadE2SResumeState restores a persisted, resumable E2S state for the task:
// nil when there is nothing to resume (no capability/task id, nothing
// persisted, a terminal status, or a core-schema mismatch — the task resumes
// fresh instead of being patched under new rules). See specs/domains/e2s.md.
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
// into the E2S loop (active = interrupted, paused = cooperative checkpoint;
// terminal statuses never resume). See specs/domains/e2s.md.
func e2sStatusResumable(s e2s.StateStatus) bool {
	return s == e2s.StateStatusActive || s == e2s.StateStatusPaused
}

// runE2SLoop is the E2S-mode driver entered from HandleMessage (fresh task or
// continuation); it never routes or runs the Conductor — the explicit-state
// protocol replaces them. See specs/domains/e2s.md.
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

	// A resumable checkpoint (e.g. a re-send into a paused E2S task) resumes
	// with the same Σ; the incoming message rides as the turn-1 observation
	// (see e2sResumeNote). Otherwise a fresh state starts. ResumeState takes
	// precedence over the new message, so a mode takeover must terminalize
	// the abandoned run's persisted Σ first (see
	// specs/domains/e2s.md, Invariants).
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
// checkpoint (Resume-side mirror of runE2SLoop's continuation branch); Σ
// continues exactly where the pause stopped it. Unlike the send gate, resume
// does NOT re-check availability — refusing would strand checkpointed work
// with no way to continue. See specs/domains/e2s.md.
// Skills requested at send time are likewise not re-resolved (no restored
// message to re-parse refs from; E2S has no subagent roster); MCP-server
// mentions are recovered by the durable task preparation before the wave.
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
	// Restore staged IMAGE-ONLY content blocks for the resumed run (mirrors
	// the Conductor resume path): the fresh HandleOptions carries no
	// PendingImages, and a provider renders ContentBlocks INSTEAD of Content
	// whenever they carry a text block — so wrapping the images with text
	// would silently drop Σ, the model's entire memory.
	resumeOpts := HandleOptions{}
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		if imageBlocks := imageBlocksForRequest(o.historySnapshot(), origReq); len(imageBlocks) > 0 {
			resumeOpts.PendingImages = imageBlocks
		}
	}
	return o.runE2SWithState(ctx, bb.GetOriginalRequest(), bb, availableTools, resumeOpts, es, e2sResumeNote(nudge))
}

// e2sResumeNote renders the turn-1 observation for a resumed E2S run — the E2S
// turn has no separate message channel, so the nudge rides as the first
// observation: verbatim as a user follow-up when present, otherwise a note
// that the budget was refreshed and Σ (not any remembered trajectory) is the
// continuation point. See specs/domains/e2s.md.
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
	// model/agent/skill resolvers, and the universal pause checker.
	deps := o.buildConductorDeps(nil, nil)

	// E2S is self-sufficient and does NOT delegate, so the delegation wiring a
	// Conductor run installs is deliberately absent (a delegation action can
	// never reach the real registry — the adapter rejects stripped names
	// fail-closed). What remains: the blackboard memory stores (E2S's external
	// memory) and the task context for the judge on escalations. The
	// step-output/final-result stores are NOT wired (their readers are
	// delegation-only and stripped).
	ctx = agent.WithFactStore(ctx, orchestration.NewFactStore(bb))
	ctx = agent.WithAttachmentStore(ctx, orchestration.NewAttachmentStore(bb))
	ctx = sdktools.WithTaskContext(ctx, message)

	// Effective E2S settings, resolved once for this run (the runtime
	// override wins over the build-time snapshot): the catalog narrowing
	// below and the loop knobs further down both read it.
	e2sCfg := o.e2sSettings()

	// Filtered catalog, narrowing inward: (1) the e2s.tools operator narrowing
	// (deny is absolute — no later stage can resurrect a name), (2) Model
	// Profiles essential-tools (identity when off), (3) StripGoalModeTools +
	// stripE2SUnavailableTools. Execute stays on the real registry (all
	// security gates intact) and rejects every filtered-out name fail-closed.
	e2sTools := stripE2SUnavailableTools(tools.StripGoalModeTools(
		o.applyModelProfilesToolFilter(
			filterE2SToolsByConfig(availableTools, e2sCfg.Tools))))

	// Trajectory: same composite store as a Conductor run (in-memory for
	// synchronous reads + best-effort DB persistence), synced by the loop
	// after every step and flushed once here at the end.
	trajHolder := &trajectoryHolder{}
	taskID := ""
	if pbb, ok := bb.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
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
		MaxSteps:                e2sCfg.MaxSteps,
		StateByteLimit:          e2sCfg.StateByteLimit,
		PatchRetries:            e2sCfg.PatchRetries,
		MaxObservationChars:     e2sCfg.MaxObservationChars,
		ObservationBudgetTokens: e2sCfg.ObservationBudgetTokens,
		ObservationFillFraction: e2sCfg.ObservationFillFraction,
		SpinNudgeThreshold:      spinNudge,
		SpinAbortThreshold:      e2sCfg.RepeatAbortThreshold,
		// ResumeState seeds the ENTIRE state (fresh runs pass the canonically
		// seeded NewE2SState; resumed runs pass the persisted checkpoint), so
		// a continuation inherits the accumulated Σ — core keys and
		// extensions — not just extension seeds. Task remains as the
		// no-checkpoint fallback for the objective.
		Task: bb.GetOriginalRequest(),
		// Image attachments staged for this send ride on every turn's user
		// message (each turn is a fresh dialog; see e2s.Config.ContentBlocks).
		ContentBlocks: opts.PendingImages,
		ResumeState:   es,
		ResumeNote:    resumeNote,
		WorkspacePath: sdktools.WorkspacePathFrom(ctx),
		TempDir:       sdktools.TempDirFrom(ctx),
		Skills:        skillSections,
		Trajectory:    trajStore,
		// Shared tool-result cache (same instance as the Conductor's
		// executor): E2S truncation becomes cache-on-truncate with a
		// tool_result_read recovery nudge, and tool_result_read actions
		// resolve through the dispatch context.
		ToolCache:    deps.toolCache,
		PauseChecker: deps.pauseChecker,
		// Per-tool-call ceiling: the E2S loop has its own dispatch (it bypasses
		// the sp4rk executor entirely), so it must thread the SAME bound the
		// executor gets — a stuck action tool fails the run with ErrToolTimeout
		// instead of hanging the loop. 0 disables the bound.
		ToolCallTimeout: deps.toolCallTimeout,
		// Conductor-parity knobs (review fix cycle): the resolved reasoning
		// effort (per-message override / Model Profiles sampling), the
		// config-gated injection-defense directive, the Model Profiles Lite
		// prompt swap, and the verify-on-edit hook. (The subagent prompt
		// sections and the finish-join guard were removed with delegation —
		// E2S does not delegate.)
		ReasoningEffort:    deps.reasoningEffort,
		InjectionDefense:   o.config.InjectionDefenseEnabled,
		SystemPrompt:       e2sCoreDirective(ctx),
		EditVerify:         deps.verifyOnEdit,
		EditVerifyMaxChars: deps.verifyOnEditMaxOutputChars,
		Logger:             o.logger,
	}

	// deps.llm IS the session TrackingCaller chain, so every E2S turn feeds the
	// same session UsageTracker — E2S is covered by the throughput seam, not
	// excluded; see core/e2s/tracking_test.go.
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
		// Empty-answer terminations must not echo the user's own words back as
		// the assistant's final message; both rewrites persist the Σ
		// checkpoint, but only step_limit is resumable (spin_stop is terminal),
		// so its message must not promise a Resume.
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

// e2sCoreDirective selects the E2S core directive: the Lite variant under the
// Model Profiles prompt profile (profile keys ride the same request context),
// "" (the e2s package default) otherwise.
func e2sCoreDirective(ctx context.Context) string {
	if modelProfilesLiteFromCtx(ctx) {
		return prompts.E2SSystemLite
	}
	return ""
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
// status vocabulary. See specs/domains/e2s.md.
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
		// Budget exhaustion is execution-INCOMPLETE, not failed: the task
		// stays resumable, and resume re-enters with the accumulated Σ plus a
		// fresh turn budget.
		return orchestration.ExecutionStatusPartial
	default: // spin_stop, failed
		return orchestration.ExecutionStatusFailed
	}
}

// e2sDomainStatus maps the loop's terminal disposition onto the persisted
// domain status that drives resume decisions (e2sStatusResumable).
//
// A context cancellation maps to NON-terminal active, mirroring the goal loop:
// the orchestrator cannot distinguish a user cancel from an app shutdown, and
// terminalizing here would make every shutdown-interrupted run un-resumable —
// the manager layer decides (a user cancel terminalizes via
// abandonE2SIfUnfinished; a shutdown leaves it active).
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
		// A NON-terminal checkpoint: the accumulated Σ is the valuable
		// artifact, so Resume re-enters with it and a fresh turn budget.
		return e2s.StateStatusActive
	default: // spin_stop, failed
		return e2s.StateStatusFailed
	}
}
