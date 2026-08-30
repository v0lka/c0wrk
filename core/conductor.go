package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// trajectoryHolder is a thread-safe TrajectoryStore for the Conductor.
// The executor syncs its step history here at each loop iteration; the
// reflect tool reads the current trajectory for scope="trajectory".
type trajectoryHolder struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (h *trajectoryHolder) Sync(steps []agent.Step) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steps = make([]agent.Step, len(steps))
	copy(h.steps, steps)
}

func (h *trajectoryHolder) Steps() []agent.Step {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.steps
}

// planRunState tracks whether a plan was declared during the CURRENT Conductor
// run (via declare_plan → conductorPublisher.Publish). A plan restored from a
// previous (completed) task is historical context only: on a continuation, the
// Conductor is free to act plan-less (standalone checklist), declare a new
// plan, or delegate — a restored plan must NOT lock it into the plan workflow.
//
// Both the ChecklistGuard (rejects standalone checklists) and the delegate
// guard (PlanChecker.HasDeclaredPlan) consult this instead of the raw
// blackboard plan, so a stale restored plan no longer trips either guard.
type planRunState struct {
	mu       sync.Mutex
	declared bool
}

// markDeclared records that declare_plan ran in this Conductor run.
func (s *planRunState) markDeclared() {
	s.mu.Lock()
	s.declared = true
	s.mu.Unlock()
}

// isDeclared reports whether a plan was declared in this Conductor run.
func (s *planRunState) isDeclared() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.declared
}

// compositeTrajectoryStore implements agent.TrajectoryStore. It keeps an
// in-memory copy of the current trajectory (so the reflect tool reads it
// synchronously) AND persists the full trajectory to the DB on every Sync.
//
// The DB write is best-effort and non-blocking: at most one write is
// outstanding at a time (a semaphore of size 1). If a previous write is still
// in flight when Sync is called again, the new write is queued only by
// retrying on the *next* Sync — it is never allowed to block the ReAct loop.
// Because every write is a full-snapshot upsert, dropping an intermediate Sync
// while a write is in flight is safe: the next completed Sync persists a
// fresher snapshot, and the persisted trajectory never goes backwards.
//
// When taskStore is nil or taskID is empty (e.g. tests / non-persistent
// sessions) the store degrades to the plain in-memory trajectoryHolder.
type compositeTrajectoryStore struct {
	memory *trajectoryHolder
	taskID string
	store  TaskPersistence
	logger *slog.Logger

	// sem limits concurrent DB writes to at most one. Send (non-blocking) to
	// acquire a write slot; receive releases it from the writer goroutine.
	sem chan struct{}

	// wg tracks the in-flight async writer so Flush can drain it before
	// performing a final synchronous write. Add(1) happens only when Sync
	// acquires the slot; Done() fires when the write goroutine exits.
	wg sync.WaitGroup
}

// newCompositeTrajectoryStore wires the in-memory holder together with the
// DB-backed store. memory must be non-nil; store and logger may be nil.
func newCompositeTrajectoryStore(memory *trajectoryHolder, taskID string, store TaskPersistence, logger *slog.Logger) *compositeTrajectoryStore {
	return &compositeTrajectoryStore{
		memory: memory,
		taskID: taskID,
		store:  store,
		logger: logger,
		sem:    make(chan struct{}, 1),
	}
}

// Sync updates the in-memory holder synchronously (so the reflect tool sees
// the latest trajectory immediately) and kicks off a best-effort, non-blocking
// DB persist. The DB write runs on a background goroutine; a slow DB never
// stalls the ReAct loop.
//
// Immutability invariant: the executor appends new steps to its trajectory but
// never mutates an already-appended step in place — this is enforced by the
// executor's trajectory lock (see Orchestrator.mu). The defensive snapshot
// below is therefore a shallow copy: it captures each Step value (slice
// headers included), and the shared backing arrays for ReasoningItems and
// Arguments are safe for the async writer to read concurrently because they
// are never written after the step is published. If that invariant ever
// changes, the snapshot must deep-copy the slice/byte backing arrays; the
// mutex guard comment above documents the required lock discipline.
func (c *compositeTrajectoryStore) Sync(steps []agent.Step) {
	c.memory.Sync(steps)

	// Nothing to persist when there is no DB store or no task to key on.
	if c.store == nil || c.taskID == "" {
		return
	}

	// Acquire the single write slot without blocking. If a previous write is
	// still in flight, skip — the next Sync (or the final Flush) persists a
	// fresher snapshot.
	select {
	case c.sem <- struct{}{}:
	default:
		return
	}

	// Defensive copy: the caller's slice is mutated across iterations, so the
	// async writer needs its own snapshot. Shallow copy is safe under the
	// immutability invariant documented above.
	snapshot := make([]agent.Step, len(steps))
	copy(snapshot, steps)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.sem }()
		if err := c.store.SaveTrajectory(c.taskID, snapshot); err != nil && c.logger != nil {
			c.logger.Debug("trajectory persistence failed (best-effort)", "taskID", c.taskID, "error", err)
		}
	}()
}

// Flush blocks until any in-flight async DB write has drained, then performs a
// final synchronous write of the current in-memory trajectory. This guarantees
// the freshest snapshot is persisted even when the last Sync was dropped
// because a write was already in flight.
//
// It is called once after the ReAct loop stops (success, error, or shutdown)
// so an interrupted task's checkpoint reflects its final steps. At that point
// the executor is no longer mutating its trajectory, so the snapshot is stable;
// the brief blocking write is acceptable because the ReAct loop has ended.
func (c *compositeTrajectoryStore) Flush() {
	// Nothing to persist when there is no DB store or no task to key on.
	if c.store == nil || c.taskID == "" {
		return
	}

	// Wait for the outstanding async write to finish before doing our own: two
	// concurrent full-snapshot upserts on the same taskID could interleave and
	// leave the DB with a stale ordering.
	c.wg.Wait()

	steps := c.memory.Steps()
	snapshot := make([]agent.Step, len(steps))
	copy(snapshot, steps)

	if err := c.store.SaveTrajectory(c.taskID, snapshot); err != nil && c.logger != nil {
		c.logger.Debug("trajectory final flush failed (best-effort)", "taskID", c.taskID, "error", err)
	}
}

// Steps delegates to the in-memory holder so the reflect tool reads the
// current trajectory synchronously.
func (c *compositeTrajectoryStore) Steps() []agent.Step {
	return c.memory.Steps()
}

// conductorDeps bundles the runtime dependencies needed by the Conductor's
// launcher, publisher, and reflection runner. It mirrors the subset of
// OrchestratorDeps required to build executors and publish plans.
type conductorDeps struct {
	contextFactory ContextManagerFactory
	toolExec       agent.ToolExecutor
	toolRegistry   *sdktools.ToolRegistry
	// disabledTools is the set of tool names disabled for the current mode
	// (e.g. CHAT / No-Project mode disables glob/ripgrep/semantic_search). The
	// Conductor uses it to (a) exclude dead tools from subagent toolsets and
	// (b) compute the per-mode mandatory read-only base. Mirrors
	// Orchestrator.coreToolRegistry.DisabledTools(); nil/empty = nothing disabled.
	disabledTools    map[string]bool
	llm              agent.LLMCaller
	modelRegistry    *llm.ModelRegistry
	model            string
	tokenCounter     llm.TokenCounter
	emitter          Emitter
	logger           *slog.Logger
	trackingCaller   *llm.TrackingCaller
	providerName     string
	stepDumpTracker  *orchestration.StepDumpTracker
	toolCache        *agent.ToolResultCache
	perToolTrunc     map[string]agent.ToolTruncationConfig
	toolResultBudget agent.ToolResultBudget
	circuitBreaker   agent.CircuitBreakerConfig
	hitlHandler      agent.HITLHandler
	reflector        *reflector.Reflector
	maxRedelegDepth  int
	maxDepCtxChars   int
	reasoningEffort  string
	preWarningPct    int

	// lifecycle is the inline plan-step lifecycle tracker. It is created in
	// RunConductor (from emitter + blackboard) and threaded in here so the
	// launcher can mark plan steps completed without post-construction field
	// mutation. Nil when the launcher is used outside RunConductor (e.g. in
	// tests); Execute guards every access with a nil check.
	lifecycle *inlineStepLifecycle

	// conversationHistory holds prior user/assistant exchanges from the
	// session. Injected into the Conductor's ContextManager so the LLM sees
	// dialogue context for follow-up messages. Nil for Resume (the Conductor
	// continues the same task — the original request is the task message).
	conversationHistory []llm.Message

	// contentBlocks carries structured content blocks (text + images) for the
	// task user message. When non-nil, RunConductor sets
	// ConductorConfig.ContentBlocks so the sp4rk engine calls
	// SetTaskWithBlocks (via the BlockTaskAware capability) instead of
	// SetTask — the ContextManager emits a Message carrying ContentBlocks
	// and providers give the blocks precedence over the plain Content
	// string. nil preserves the legacy text-only path (backward compatible).
	contentBlocks []llm.ContentBlock

	// taskStore is the optional DB-backed persistence store. When non-nil and
	// the blackboard carries a task ID, the Conductor's trajectory is persisted
	// (best-effort, non-blocking) on every ReAct iteration so it survives app
	// restart. Nil in tests / non-persistent sessions — the trajectory stays
	// purely in-memory.
	taskStore TaskPersistence

	// resumeSteps holds pre-existing ReAct steps for resuming an interrupted
	// task from a checkpoint. When non-empty, RunConductor sets
	// ConductorConfig.ResumeSteps so the sp4rk engine seeds the
	// ContextManager (StepSeedable) and the Executor (WithResumeSteps) with
	// these steps — the step counter continues from len(steps)+1 and the full
	// prior trajectory appears in the context window. Zero-value (nil/empty)
	// is the default fresh-start behavior.
	resumeSteps []agent.Step

	// forceCompactionStrategy is the one-shot manual-compaction override from
	// the resume flow (RequestResumeCompaction → Resume → runConductor /
	// resumeGoalLoop). When non-empty, RunConductor (a) replaces the
	// routing-derived compaction strategy with it for the ENTIRE run — every
	// compaction the run performs (the forced start-of-run pass AND any later
	// threshold-triggered reactive compactions) uses this strategy — and (b)
	// sets ConductorConfig.CompactOnStart so the sp4rk engine compacts the
	// merged trajectory (seeded resumeSteps + the resumed run) exactly once,
	// up front, BEFORE the first LLM call, regardless of fill thresholds.
	// Empty (the default) preserves the normal behavior: the strategy derives
	// from routing domain/complexity and compaction stays threshold-driven.
	forceCompactionStrategy string

	// systemPromptOverride, when non-nil, overrides the default Conductor
	// system-prompt factory (buildSystemPrompt + Conductor Guidance). Used by
	// goal derivation (deriveGoal) to run the Conductor with the specialized
	// derivation directive while STILL reusing the shared project-context
	// prefix (AGENTS.md, workspace, env, skills, ...) via
	// buildSpecializedSystemPrompt. All other conductor wiring — context
	// injection, trajectory store, toolset — is reused unchanged. Zero-value
	// (nil) is the default behavior: the standard orchestrator system prompt
	// is assembled.
	systemPromptOverride orchestration.SystemPromptFactory

	// verifyOnEdit, when non-nil, arms the mechanical edit-verification hook
	// (executor.verify_on_edit) on the main executor and every subagent
	// executor: after a successful write_file/edit_file the runner executes
	// the user-configured command and the executor injects its (truncated)
	// output as a system observation. Suppressed for CHAT mode (see
	// buildConductorDeps) and specialized goal passes (see RunConductor).
	verifyOnEdit agent.EditVerifyRunner

	// verifyOnEditMaxOutputChars caps the injected verification output;
	// 0 falls back to agent.DefaultVerifyOnEditCap.
	verifyOnEditMaxOutputChars int

	// goalProposer is the backend hook that submits a {condition, verify}
	// goal proposal to the user and blocks for approval. Used by deriveGoal,
	// which wraps it in a capturingProposer (to record the agent's proposal)
	// before injecting it into the Conductor context. Zero-value (nil) is the
	// default: propose_goal returns "no goal proposer in context" if invoked.
	goalProposer tools.GoalProposer

	// agentResolver resolves a Subagent Profile name to a *agents.Agent. It is
	// populated from the Orchestrator's agentManager in buildConductorDeps and
	// injected into the Conductor context so buildSubAgentTask can apply a
	// requested profile (system prompt, tools, max-steps, model, redelegate).
	// Nil when no agentManager is configured (profiles unavailable) — a
	// non-empty `agent` field is then rejected by delegate validation.
	agentResolver tools.AgentResolver

	// pauseChecker is the cooperative pause signal wired into every conductor
	// run via ConductorConfig.PauseChecker → executor.SetPauseChecker. It
	// reads the active request's pause signal (o.activePause) at each step
	// boundary; PauseSession flips it. Populated by buildConductorDeps so ALL
	// conductor runs — normal path and every goal-loop turn — are pauseable
	// through one universal signal. Subagent executors are wired with the same
	// checker via configureExecutor, so delegated/plan-step subagents pause at
	// their own step boundary too (instead of running to natural completion).
	// Nil disables pausing (default).
	pauseChecker func(context.Context) bool

	// userMessageSource is the live user-message queue wired into every
	// conductor run via ConductorConfig.UserMessageSource →
	// executor.SetUserMessageSource. The executor polls it at every step
	// boundary (right after the pause check); a non-empty return is delivered
	// to the LLM as a {role:user} interjection in the very next request.
	// Populated by buildConductorDeps so ALL conductor runs — normal path,
	// resume, and every goal-loop turn — deliver queued live messages. Nil
	// disables live delivery (default). Subagent executors are built
	// separately and never receive the source: a live message belongs to the
	// user's dialogue with the Conductor, not to a delegated subtask.
	userMessageSource func(context.Context) string

	// nudge is a one-shot resume-with-nudge user message threaded to
	// ConductorConfig.PendingUserInterjection. When non-empty it lands as the
	// final user message after the seeded step history in the very next LLM
	// call. Set per-resume; empty for fresh tasks (the default no-nudge path).
	nudge string
}

// conductorLauncher implements tools.DelegationLauncher by building a fresh
// Executor + ContextManager per task and dispatching via RunSubAgent.
type conductorLauncher struct {
	deps conductorDeps
	bb   orchestration.Blackboard
	// runPlanStepWave executes one wave of ready plan steps concurrently and
	// returns their outcomes. Defaults to defaultPlanStepWave (which builds an
	// isolated subagent executor per step and runs them via
	// agent.RunSubAgentsParallel). Tests override it to exercise the Execute
	// DAG scheduler — wave detection, failure cascade, cycle detection, and
	// cancellation — without real LLM-driven executors.
	runPlanStepWave func(ctx context.Context, ready []orchestration.PlanStep, registry *tools.DelegationRegistry) []planStepOutcome
	// planState tracks whether a plan was declared in THIS Conductor run. It
	// is shared with the conductorPublisher so HasDeclaredPlan (the delegate
	// guard) reflects only plans declared via declare_plan, not restored ones.
	// Nil only under direct (test) construction — see HasDeclaredPlan fallback.
	planState *planRunState
}

// HasDeclaredPlan implements tools.PlanChecker. Returns true when a plan has
// been declared in the CURRENT Conductor run via declare_plan. Used by the
// delegate guard to enforce orthogonality: once a plan is declared, delegate is
// disabled and execute_plan is the only execution path for plan steps.
//
// Crucially, this consults planRunState (set by conductorPublisher.Publish),
// NOT the raw blackboard plan: on a continuation, a restored plan from a
// previous (completed) task must NOT lock the new run into the plan workflow.
// When planState is nil (direct construction in tests), it falls back to the
// blackboard-plan check so existing direct-construction tests keep working.
func (l *conductorLauncher) HasDeclaredPlan() bool {
	if l.planState != nil {
		return l.planState.isDeclared()
	}
	return l.bb != nil && l.bb.GetPlan() != nil
}

// Execute runs the declared plan's steps in DAG order with parallelism for
// independent steps. It implements tools.PlanStepExecutor.
//
// A plan restored from a previous (completed) task is refused: planRunState
// (fresh per run) tracks whether declare_plan ran in THIS run, and Execute
// will not re-run a restored plan's steps (which would duplicate side
// effects). The Conductor must publish a new plan via declare_plan, or use
// delegate for plan-less work.
//
// Execute is resumable. A step that already has a successful result on the
// blackboard (StepResult.Error == nil) is skipped, so a second call re-runs
// only failed or never-started steps instead of repeating side effects. An
// explicit stepIDs list forces the named steps — and any step that depends on
// them, directly or transitively — to re-run even if they previously
// succeeded; other already-successful steps are still skipped.
//
// Each step runs as an isolated subagent (its own Executor + ContextManager),
// but events are emitted as plan_step_start/plan_step_complete (not
// subagent_launch/subagent_complete) via the planStepEventTranslator. This
// reuses the full subagent machinery (parallel execution, context isolation,
// dependency injection) while presenting the work as plan steps in the UI.
//
// After each step completes, its result is:
//   - stored on the blackboard (SetStepResult) — accessible via read_step_output
//   - marked in the inlineStepLifecycle (markCompleted) — prevents the
//     finish-fallback completeAll from double-completing
func (l *conductorLauncher) Execute(ctx context.Context, stepIDs []string) ([]tools.PlanStepResult, error) {
	plan := l.bb.GetPlan()
	if plan == nil || len(plan.Steps) == 0 {
		return nil, errors.New("no plan declared — call declare_plan first")
	}

	// Restored-plan guard: on a continuation, the blackboard may carry a plan
	// restored from a previous (completed) task. planRunState (wired in
	// RunConductor) is fresh — not declared — so HasDeclaredPlan() returns
	// false for such a plan. Refuse to execute it: re-running a completed
	// task's steps would duplicate side effects (file edits, etc.) for no
	// benefit. When planState is nil (direct test construction), this guard is
	// inert and Execute proceeds on whatever plan is on the blackboard,
	// preserving the behavior the DAG-scheduler tests rely on.
	if l.planState != nil && !l.planState.isDeclared() {
		return nil, errors.New("execute_plan: the plan on the blackboard was restored from a previous (completed) task and was not declared in this run — call declare_plan to publish a new plan, or use delegate for plan-less work")
	}

	// Compute the forced-rerun set when explicit step IDs were requested.
	var forced map[string]bool
	if len(stepIDs) > 0 {
		var err error
		forced, err = forcedRerunSet(plan, stepIDs)
		if err != nil {
			return nil, err
		}
	}

	// Local registry for dependency resolution between plan steps. This is
	// SEPARATE from the Conductor's main delegation registry — plan steps are
	// not delegations and should not appear in cancel_delegation.
	localReg := tools.NewDelegationRegistry()

	// indexByID preserves plan-declaration order for deterministic result
	// ordering (map iteration is randomised, so the aggregated result would
	// otherwise be non-reproducible across runs).
	indexByID := make(map[string]int, len(plan.Steps))
	pending := make(map[string]orchestration.PlanStep, len(plan.Steps))
	results := make([]tools.PlanStepResult, 0, len(plan.Steps))

	for i, step := range plan.Steps {
		indexByID[step.ID] = i
		if err := localReg.Register(step.ID, step.Summary, step.DependsOn, "blocking"); err != nil {
			return nil, fmt.Errorf("register plan step %q: %w", step.ID, err)
		}

		// Skip an already-successful step unless it is in the forced-rerun set.
		// Its previous result is replayed into the local registry so dependents
		// see their dependency as satisfied and can read its output.
		if !forced[step.ID] {
			if sr, ok := l.successfulStepResult(step.ID); ok {
				localReg.Complete(step.ID, sr.FullOutput, nil, sr.Steps)
				results = append(results, tools.PlanStepResult{
					StepID: step.ID, Summary: step.Summary,
					Status: "completed", Output: sr.FullOutput,
				})
				// The step already succeeded on the blackboard. If this run has
				// not yet recorded its terminal state (the plan was re-declared
				// on a continuation), emit a synthesized success pair so the
				// plan panel does not leave it "pending". On a same-run retry
				// the step is already completed and needs no new events.
				if l.deps.lifecycle != nil && !l.deps.lifecycle.isCompleted(step.ID) {
					l.emitSkippedStepCompleted(step.ID)
					l.deps.lifecycle.markCompleted(step.ID)
				}
				continue
			}
		}
		pending[step.ID] = step
	}

	// Resolve the wave dispatcher: the production default builds isolated
	// subagent executors per step; tests inject a stub to exercise the DAG
	// scheduler without real LLM executors.
	dispatch := l.runPlanStepWave
	if dispatch == nil {
		dispatch = l.defaultPlanStepWave
	}

	subCtx := subagentCtx(ctx)

	for len(pending) > 0 {
		if ctx.Err() != nil {
			for id, step := range pending {
				results = append(results, tools.PlanStepResult{
					StepID: id, Summary: step.Summary,
					Status: "failed", Error: ctx.Err(),
				})
			}
			return results, ctx.Err()
		}

		// Find ready steps (all dependencies completed successfully).
		var ready []orchestration.PlanStep
		for _, step := range pending {
			if l.planStepDepsReady(step, localReg) {
				ready = append(ready, step)
			}
		}
		if len(ready) == 0 {
			// Dependencies unsatisfiable — every remaining step is blocked by
			// an upstream failure. None of them launched, so the translator
			// never emitted PlanStepStart/Complete for them. Emit a terminal
			// pair directly (mirroring completeAll) so they do not linger
			// "pending" in the plan panel, then mark them completed so the
			// finish fallback does not double-complete.
			for id, step := range pending {
				err := fmt.Errorf("step %q: dependencies could not be satisfied (upstream failure)", id)
				results = append(results, tools.PlanStepResult{
					StepID: id, Summary: step.Summary,
					Status: "failed", Error: err,
				})
				// The step never launched, so the translator did not emit a
				// terminal pair for it — emit one directly so it is not left
				// "pending" in the plan panel.
				l.emitNeverStartedStep(id, err.Error())
				if l.deps.lifecycle != nil {
					l.deps.lifecycle.markCompleted(id)
				}
			}
			return results, nil
		}

		// Execute the ready wave (builds subagents + runs concurrently in
		// production; returns canned outcomes under a test stub).
		outcomes := dispatch(subCtx, ready, localReg)
		pausedWave := false
		for _, oc := range outcomes {
			step := pending[oc.stepID]

			if isPaused(oc.err) {
				localReg.CompletePaused(oc.stepID, oc.output, oc.steps)
				l.bb.SetStepResult(oc.stepID, oc.output, oc.err, oc.steps)
				results = append(results, tools.PlanStepResult{
					StepID: oc.stepID, Summary: step.Summary,
					Status: "paused", Output: oc.output, Error: oc.err,
				})
				if l.deps.lifecycle != nil {
					l.deps.lifecycle.markCompleted(oc.stepID)
				}
				delete(pending, oc.stepID)
				pausedWave = true
				continue
			}

			localReg.Complete(oc.stepID, oc.output, oc.err, oc.steps)
			l.bb.SetStepResult(oc.stepID, oc.output, oc.err, oc.steps)

			status := "completed"
			if oc.err != nil {
				status = "failed"
			}
			results = append(results, tools.PlanStepResult{
				StepID: oc.stepID, Summary: step.Summary,
				Status: status, Output: oc.output, Error: oc.err,
			})
			if l.deps.lifecycle != nil {
				l.deps.lifecycle.markCompleted(oc.stepID)
			}
			delete(pending, oc.stepID)
		}

		// A cooperative pause stopped one or more steps in this wave. Stop
		// scheduling further waves; the remaining steps are left unrun and
		// will be (re)dispatched on resume via a fresh execute_plan call.
		if pausedWave {
			break
		}
	}

	// Deterministic result ordering by plan-declaration index. Without this
	// the aggregated summary order is randomised by map iteration.
	slices.SortFunc(results, func(a, b tools.PlanStepResult) int {
		return cmp.Compare(indexByID[a.StepID], indexByID[b.StepID])
	})
	return results, nil
}

// successfulStepResult returns the blackboard's successful StepResult for the
// step, if one exists. A step with no result (never ran) or an error result
// (failed) is not successful and therefore eligible to (re)run.
func (l *conductorLauncher) successfulStepResult(stepID string) (orchestration.StepResult, bool) {
	sr, ok := l.bb.GetStepResult(stepID)
	if !ok || sr.Error != nil {
		return orchestration.StepResult{}, false
	}
	return sr, true
}

// forcedRerunSet computes the set of step IDs that must re-run when explicit
// step IDs are passed to execute_plan: the targets themselves plus every step
// that transitively depends on a target (its output is now stale). Returns an
// error if any target is not part of the plan.
func forcedRerunSet(plan *orchestration.Plan, targets []string) (map[string]bool, error) {
	known := make(map[string]bool, len(plan.Steps))
	for _, s := range plan.Steps {
		known[s.ID] = true
	}

	forced := make(map[string]bool, len(targets))
	for _, id := range targets {
		if !known[id] {
			return nil, fmt.Errorf("unknown step %q", id)
		}
		forced[id] = true
	}

	// Fixpoint over direct dependents: a step is forced if it is a target or
	// any of its dependencies is forced.
	for {
		changed := false
		for _, s := range plan.Steps {
			if forced[s.ID] {
				continue
			}
			for _, dep := range s.DependsOn {
				if forced[dep] {
					forced[s.ID] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	return forced, nil
}

// planStepOutcome is the result of executing one plan step within a wave. It
// is the contract between Execute's DAG scheduler and the wave dispatcher.
type planStepOutcome struct {
	stepID string
	output string
	steps  []agent.Step
	err    error
}

// defaultPlanStepWave is the production wave dispatcher: it builds an isolated
// subagent executor per ready step and runs the wave concurrently via
// agent.RunSubAgentsParallel. Steps whose task construction fails are returned
// as failed outcomes with a directly-emitted terminal event pair (the
// translator never ran, so no SubAgentLaunch/Complete fired for them).
func (l *conductorLauncher) defaultPlanStepWave(ctx context.Context, ready []orchestration.PlanStep, registry *tools.DelegationRegistry) []planStepOutcome {
	// Plan-step subagents get the same narrowed context as every other
	// subagent launch path: goal-loop sinks cleared (defense in depth on top
	// of resolveTaskTools stripping the goal tools from every toolset), no
	// Conductor-only delegation/plan machinery, and no subagent-roster prompt
	// sections. Plan steps are plain blocking subagents — they need none of
	// the Conductor's context handles (the registry is passed explicitly and
	// the profile resolver survives the narrowing).
	ctx = subagentCtx(ctx)
	subTasks := make([]agent.SubAgentTask, 0, len(ready))
	outcomes := make([]planStepOutcome, 0, len(ready))
	for _, step := range ready {
		task := tools.DelegationTask{
			ID:        step.ID,
			Summary:   step.Summary,
			Task:      step.Description,
			DependsOn: step.DependsOn,
			Mode:      "blocking",
			Agent:     step.Agent,
		}
		scopedEvents := l.scopePlanStepEvents(step.ID, step.Summary)
		st, err := l.buildSubAgentTask(ctx, task, registry, scopedEvents)
		if err != nil {
			// Build failed — the subagent never launched, so the
			// planStepEventTranslator did not emit PlanStepStart/Complete.
			// Emit a terminal pair directly so the step is not left "pending".
			l.emitNeverStartedStep(step.ID, err.Error())
			outcomes = append(outcomes, planStepOutcome{stepID: step.ID, err: err})
			continue
		}
		registry.Start(step.ID, nil)
		subTasks = append(subTasks, st)
	}

	for _, sr := range agent.RunSubAgentsParallel(ctx, subTasks) {
		if isPaused(sr.Error) {
			l.bb.SetStepResult(sr.StepID, sr.Output, sr.Error, sr.Steps)
			outcomes = append(outcomes, planStepOutcome{
				stepID: sr.StepID,
				output: sr.Output,
				steps:  sr.Steps,
				err:    sr.Error,
			})
			continue
		}
		var execErr error
		if sr.Error != nil {
			execErr = sr.Error
		}
		outcomes = append(outcomes, planStepOutcome{
			stepID: sr.StepID,
			output: sr.Output,
			steps:  sr.Steps,
			err:    execErr,
		})
	}
	return outcomes
}

// emitNeverStartedStep emits the PlanStepStart + PlanStepComplete(success=false)
// terminal pair for a plan step that never launched (unsatisfiable dependency
// or build failure). PlanStepStart dedups on the root emitter, so this is safe
// even if a stray start was already recorded.
func (l *conductorLauncher) emitNeverStartedStep(stepID, errMsg string) {
	if l.deps.emitter == nil {
		return
	}
	desc, summary := lookupStepDesc(l.bb, stepID)
	l.deps.emitter.PlanStepStart(stepID, desc, summary)
	l.deps.emitter.PlanStepComplete(stepID, false, 0, errMsg)
}

// emitSkippedStepCompleted emits the PlanStepStart + PlanStepComplete(success=true)
// terminal pair for a step skipped by the resume path because it already has a
// successful result on the blackboard. Mirrors emitNeverStartedStep, but the
// step succeeded, so the terminal pair records success and an empty error.
func (l *conductorLauncher) emitSkippedStepCompleted(stepID string) {
	if l.deps.emitter == nil {
		return
	}
	desc, summary := lookupStepDesc(l.bb, stepID)
	l.deps.emitter.PlanStepStart(stepID, desc, summary)
	l.deps.emitter.PlanStepComplete(stepID, true, 0, "")
}

// planStepDepsReady checks whether all dependencies of a plan step are
// completed successfully in the local registry.
func (l *conductorLauncher) planStepDepsReady(step orchestration.PlanStep, registry *tools.DelegationRegistry) bool {
	for _, dep := range step.DependsOn {
		if !registry.IsCompleted(dep) {
			return false
		}
		d := registry.Get(dep)
		if d != nil && (d.Status == tools.DelegationStatusFailed || d.Status == tools.DelegationStatusCancelled) {
			return false
		}
	}
	return true
}

func (l *conductorLauncher) Launch(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	results := make([]tools.DelegationResult, 0, len(tasks))
	pending := make(map[string]tools.DelegationTask, len(tasks))
	for _, t := range tasks {
		pending[t.ID] = t
	}

	for len(pending) > 0 {
		if ctx.Err() != nil {
			for id := range pending {
				registry.Complete(id, "", ctx.Err(), nil)
				results = append(results, tools.DelegationResult{ID: id, Status: tools.DelegationStatusCancelled, Error: ctx.Err()})
			}
			return results
		}

		var ready []tools.DelegationTask
		for _, t := range pending {
			if l.depsReady(t, registry) {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			for id := range pending {
				err := fmt.Errorf("delegation %q: dependencies could not be satisfied", id)
				registry.Complete(id, "", err, nil)
				results = append(results, tools.DelegationResult{ID: id, Status: tools.DelegationStatusFailed, Error: err})
			}
			return results
		}

		waveResults := l.runWave(ctx, ready, registry)
		paused := false
		for _, r := range waveResults {
			results = append(results, r)
			delete(pending, r.ID)
			if r.Status == tools.DelegationStatusPaused {
				paused = true
			}
		}
		// A cooperative pause stopped one or more subagents in this wave. Stop
		// scheduling further waves; remaining tasks are left unrun and will be
		// (re)dispatched on resume.
		if paused {
			return results
		}
	}
	return results
}

func (l *conductorLauncher) depsReady(t tools.DelegationTask, registry *tools.DelegationRegistry) bool {
	for _, dep := range t.DependsOn {
		if !registry.IsCompleted(dep) {
			return false
		}
		d := registry.Get(dep)
		if d != nil && (d.Status == tools.DelegationStatusFailed || d.Status == tools.DelegationStatusCancelled) {
			return false
		}
	}
	return true
}

func (l *conductorLauncher) runWave(ctx context.Context, ready []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	var blocking []tools.DelegationTask
	var asyncTasks []tools.DelegationTask
	for _, t := range ready {
		mode := t.Mode
		if mode == "" {
			mode = "blocking"
		}
		if mode == "async" {
			asyncTasks = append(asyncTasks, t)
		} else {
			blocking = append(blocking, t)
		}
	}

	var results []tools.DelegationResult

	for _, t := range asyncTasks {
		r := l.launchAsync(ctx, t, registry)
		results = append(results, r)
	}

	if len(blocking) > 0 {
		blockResults := l.runBlocking(ctx, blocking, registry)
		results = append(results, blockResults...)
	}

	return results
}

func (l *conductorLauncher) runBlocking(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	// Split tasks into regular and redelegating. Redelegating tasks need
	// per-task contexts (child registry + launcher), so they can't share
	// the same ctx path through RunSubAgentsParallel.
	var regularTasks []tools.DelegationTask
	var redelegTasks []tools.DelegationTask
	for _, t := range tasks {
		// A profile's allow-redelegate only ever upgrades (never downgrades) —
		// a profile with allow-redelegate: true wins over a false task flag, but
		// a false profile value never revokes an explicitly-granted task flag
		// (ADR-021 §5). The other profile fields (prompt/tools/max-steps/model)
		// are applied in buildSubAgentTask; only allow-redelegate must be
		// resolved here because it selects the dispatch path.
		if t.Agent != "" {
			t.AllowRedelegate = l.resolveAgentAllowRedelegate(ctx, t.Agent, t.AllowRedelegate)
		}
		if t.AllowRedelegate {
			redelegTasks = append(redelegTasks, t)
		} else {
			regularTasks = append(regularTasks, t)
		}
	}

	var results []tools.DelegationResult

	// Launch regular tasks in parallel.
	if len(regularTasks) > 0 {
		results = append(results, l.runRegularBlocking(ctx, regularTasks, registry)...)
	}

	// Launch redelegating tasks individually (per-task context).
	for _, t := range redelegTasks {
		results = append(results, l.runRedelegBlocking(ctx, t, registry))
	}

	return results
}

// resolveAgentAllowRedelegate applies a profile's allow-redelegate override.
// The override only ever upgrades (never downgrades): a profile that sets
// AllowRedelegate=true wins over a false task flag, while a profile with false
// preserves the task flag unchanged — the profile can grant nested delegation
// but never revoke a delegation the caller explicitly granted (ADR-021 §5).
// When the profile is not found (or no resolver), the task flag is preserved
// unchanged — validateDelegationTasks already rejected unknown agents, so a
// not-found here is a benign race (agent removed mid-run) that keeps the safer
// (task) default.
func (l *conductorLauncher) resolveAgentAllowRedelegate(ctx context.Context, agentName string, taskFlag bool) bool {
	resolver := tools.AgentResolverFrom(ctx)
	if resolver == nil {
		return taskFlag
	}
	profile, ok := resolver(agentName)
	if !ok {
		return taskFlag
	}
	if profile.Metadata.AllowRedelegate {
		return true
	}
	return taskFlag
}

// subagentCtx strips the Conductor-only context values from ctx. It must be
// used for any subagent that is NOT explicitly granted allow_redelegate.
//
// Two classes of values are cleared:
//
//  1. Delegation-machinery handles (DelegationRegistry, DelegationLauncher,
//     PlanPublisher, ReflectionRunner, PlanChecker). resolveTaskTools already
//     filters delegate/cancel_delegation/declare_plan/reflect out of the
//     subagent's tool descriptor list, but that alone is not sufficient
//     defense-in-depth since a subagent's tool set can also be influenced by
//     explicit task.Tools lists — stripping the context values ensures those
//     tools are inert (return "not running inside a Conductor") even if a
//     descriptor for them ever reaches the subagent.
//  2. The subagent roster (availableAgentsKey/userAgentsKey). Both roster
//     sections ("Available Subagents" / "Requested Subagents") are
//     Conductor-only by design (ADR-021 §4) and are rendered into the system
//     prompt only when present in context. Without clearing them, a generic
//     no-profile subagent — whose prompt is built via the normal
//     buildSystemPrompt path — would inherit the Conductor's "you MUST
//     delegate via delegate(agent:)" directive while having no delegate tool
//     available, producing a contradictory prompt.
func subagentCtx(ctx context.Context) context.Context {
	ctx = tools.WithDelegationRegistry(ctx, nil)
	ctx = tools.WithDelegationLauncher(ctx, nil)
	ctx = tools.WithPlanPublisher(ctx, nil)
	ctx = tools.WithReflectionRunner(ctx, nil)
	ctx = tools.WithPlanChecker(ctx, nil)
	ctx = orchestration.WithDelegationRegistry(ctx, nil)
	// Clear the goal-loop sinks so no delegated subagent can write goal-status
	// or verification verdicts into the loop it serves under: those channels
	// belong to the goal-loop Conductor and the independent verifier only.
	// resolveTaskTools already strips the goal tools from every subagent
	// toolset; this nil-ing is defense in depth (a future GroupSystem tool
	// or a hand-written toolset cannot reach the sinks either). The verifier
	// itself does not run through subagentCtx — it is a direct RunConductor
	// call with its own fresh sink (defaultGoalVerifier), and the re-derivation
	// sub-agent it delegates SHOULD land here with the sink cleared.
	ctx = tools.WithGoalStatusSink(ctx, nil)
	ctx = tools.WithVerificationSink(ctx, nil)
	// Clear the Conductor-only subagent roster so no subagent inherits the
	// "Available Subagents"/"Requested Subagents" prompt sections (ADR-021 §4).
	ctx = WithAvailableAgents(ctx, nil)
	ctx = WithUserAgents(ctx, nil)
	return ctx
}

// isPaused reports whether err is a cooperative-pause sentinel. It handles
// both the in-memory sentinel (agent.ErrPaused) and the string-reconstructed
// error produced by a persistence round-trip: TaskPersistence.PersistStepResult
// stores ErrorText and LoadTaskState rebuilds it via errors.New(ErrorText), so
// a paused checkpoint must remain detectable after the task is restored on
// resume. The ErrPaused message is a unique, stable sentinel string, so
// equality is safe here.
func isPaused(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, agent.ErrPaused) || err.Error() == agent.ErrPaused.Error()
}

// pausedCheckpoint returns the blackboard's paused checkpoint for a step, if
// one exists. A paused checkpoint is a StepResult whose Error is the
// cooperative-pause sentinel and whose Steps hold the subagent's partial
// trajectory — the seed used to resume that subagent from where it stopped.
func (l *conductorLauncher) pausedCheckpoint(stepID string) (orchestration.StepResult, bool) {
	if l.bb == nil {
		return orchestration.StepResult{}, false
	}
	sr, ok := l.bb.GetStepResult(stepID)
	if !ok || !isPaused(sr.Error) {
		return orchestration.StepResult{}, false
	}
	return sr, true
}

func (l *conductorLauncher) runRegularBlocking(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	subCtx := subagentCtx(ctx)
	subTasks := make([]agent.SubAgentTask, 0, len(tasks))
	buildFailures := make([]tools.DelegationResult, 0)
	for _, t := range tasks {
		st, err := l.buildSubAgentTask(subCtx, t, registry, l.scopeEvents(t.ID))
		if err != nil {
			registry.Complete(t.ID, "", err, nil)
			buildFailures = append(buildFailures, tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err})
			continue
		}
		registry.Start(t.ID, nil)
		subTasks = append(subTasks, st)
	}

	if len(subTasks) == 0 {
		return buildFailures
	}

	subResults := agent.RunSubAgentsParallel(subCtx, subTasks)
	out := make([]tools.DelegationResult, 0, len(subResults)+len(buildFailures))
	out = append(out, buildFailures...)
	for _, sr := range subResults {
		if isPaused(sr.Error) {
			l.bb.SetStepResult(sr.StepID, sr.Output, sr.Error, sr.Steps)
			registry.CompletePaused(sr.StepID, sr.Output, sr.Steps)
			out = append(out, tools.DelegationResult{ID: sr.StepID, Status: tools.DelegationStatusPaused, Output: sr.Output, Error: sr.Error})
			continue
		}
		var execErr error
		if sr.Error != nil {
			execErr = sr.Error
		}
		registry.Complete(sr.StepID, sr.Output, execErr, sr.Steps)
		l.bb.SetStepResult(sr.StepID, sr.Output, execErr, sr.Steps)
		status := tools.DelegationStatusCompleted
		if execErr != nil {
			status = tools.DelegationStatusFailed
		}
		out = append(out, tools.DelegationResult{ID: sr.StepID, Status: status, Output: sr.Output, Error: execErr})
	}
	return out
}

func (l *conductorLauncher) runRedelegBlocking(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	if registry.Depth() >= l.deps.maxRedelegDepth {
		err := fmt.Errorf("delegation %q: allow_redelegate requested at depth %d but cap is %d", t.ID, registry.Depth(), l.deps.maxRedelegDepth)
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Create a child registry one level deeper and inject it + the launcher
	// into a per-task context so the subagent can use delegate/cancel_delegation.
	childReg := tools.NewDelegationRegistryWithDepth(registry.Depth() + 1)
	taskCtx := tools.WithDelegationRegistry(ctx, childReg)
	taskCtx = tools.WithDelegationLauncher(taskCtx, l)
	// Also inject into the sp4rk-level context key so the sp4rk Conductor's
	// finish guard (executor.SetFinishGuard) can find the child registry and
	// reject finish while sub-delegations are still pending.
	taskCtx = orchestration.WithDelegationRegistry(taskCtx, childReg)
	// Clear the Conductor-only subagent roster so the redelegating subagent
	// does not inherit the parent's "Available Subagents"/"Requested Subagents"
	// prompt sections (ADR-021 §4). This path bypasses subagentCtx (it must
	// keep delegation machinery to re-inject the child registry), so the roster
	// is cleared explicitly here.
	taskCtx = WithAvailableAgents(taskCtx, nil)
	taskCtx = WithUserAgents(taskCtx, nil)
	// Clear the goal-loop sinks here for the same reason: a redelegating
	// subagent holds delegate/cancel_delegation but must still never write
	// goal-status or verification verdicts (resolveTaskTools strips the goal
	// tools from its toolset; this is the second layer).
	taskCtx = tools.WithGoalStatusSink(taskCtx, nil)
	taskCtx = tools.WithVerificationSink(taskCtx, nil)

	// Build the subagent task. The finish guard in the sp4rk Conductor
	// (SetFinishGuard) rejects finish with a nudge while pending async
	// sub-delegations exist, since the child registry is in the context.
	st, err := l.buildSubAgentTask(taskCtx, t, registry, l.scopeEvents(t.ID))
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Add delegate + cancel_delegation to the subagent's tool set, with their
	// real schema (resolveTaskTools always strips these for the redelegating
	// path too, so they must be re-added here explicitly). Looking them up
	// from the shared registry — rather than constructing bare
	// ToolDescriptor{} literals — ensures the subagent's LLM sees the actual
	// input schema (e.g. delegate's "tasks" array), not an empty schema that
	// providers would otherwise fall back to a generic object type for.
	redelegTools := make([]sdktools.ToolDescriptor, 0, len(st.TaskTools)+2)
	redelegTools = append(redelegTools, st.TaskTools...)
	for _, name := range []string{"delegate", "cancel_delegation"} {
		if d, ok := l.toolDescriptorByName(name); ok {
			redelegTools = append(redelegTools, d)
		}
	}

	registry.Start(t.ID, nil)
	ch := agent.RunSubAgent(taskCtx, t.ID, st.Executor, st.CM, redelegTools, st.TaskDesc, st.Emitter, st.TodoUpdateFunc)
	sr := <-ch

	if isPaused(sr.Error) {
		l.bb.SetStepResult(t.ID, sr.Output, sr.Error, sr.Steps)
		registry.CompletePaused(t.ID, sr.Output, sr.Steps)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusPaused, Output: sr.Output, Error: sr.Error}
	}

	var execErr error
	if sr.Error != nil {
		execErr = sr.Error
	}
	registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
	l.bb.SetStepResult(t.ID, sr.Output, execErr, sr.Steps)
	status := tools.DelegationStatusCompleted
	if execErr != nil {
		status = tools.DelegationStatusFailed
	}
	return tools.DelegationResult{ID: t.ID, Status: status, Output: sr.Output, Error: execErr}
}

func (l *conductorLauncher) launchAsync(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	// Async delegations always take the non-redelegating path today (runWave
	// dispatches all async tasks here regardless of AllowRedelegate), so the
	// Conductor-only context values must always be stripped — see subagentCtx.
	ctx = subagentCtx(ctx)
	subTask, err := l.buildSubAgentTask(ctx, t, registry, l.scopeEvents(t.ID))
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	asyncCtx, cancel := context.WithCancel(ctx)
	registry.Start(t.ID, cancel)

	go func() {
		defer cancel()
		ch := agent.RunSubAgent(asyncCtx, t.ID, subTask.Executor, subTask.CM, subTask.TaskTools, subTask.TaskDesc, subTask.Emitter, subTask.TodoUpdateFunc)
		select {
		case sr := <-ch:
			if isPaused(sr.Error) {
				l.bb.SetStepResult(t.ID, sr.Output, sr.Error, sr.Steps)
				registry.CompletePaused(t.ID, sr.Output, sr.Steps)
				return
			}
			var execErr error
			if sr.Error != nil {
				execErr = sr.Error
			}
			registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
			l.bb.SetStepResult(t.ID, sr.Output, execErr, sr.Steps)
		case <-asyncCtx.Done():
			registry.Complete(t.ID, "", asyncCtx.Err(), nil)
		}
	}()

	return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusRunning}
}

func (l *conductorLauncher) buildSubAgentTask(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry, scopedEvents agent.Events) (agent.SubAgentTask, error) {
	// Resolve an agent profile if one is requested. A non-empty `agent` field
	// was already validated for existence by validateDelegationTasks (when a
	// resolver is present), but buildSubAgentTask resolves it again so a
	// profile removed between validation and launch surfaces a clear error
	// rather than launching a profile-less subagent.
	var profile *agents.Agent
	if t.Agent != "" {
		resolver := tools.AgentResolverFrom(ctx)
		if resolver == nil {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: agent %q requested but no agent resolver is configured", t.ID, t.Agent)
		}
		var ok bool
		profile, ok = resolver(t.Agent)
		if !ok {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: unknown agent %q — no Subagent Profile with that name was found", t.ID, t.Agent)
		}
	}

	maxSteps := t.MaxSteps
	// A profile's max-steps (when >0) overrides both the task field and the
	// complexity-derived default. The profile is the authoritative budget for
	// a specialized subagent.
	if profile != nil && profile.Metadata.MaxSteps > 0 {
		maxSteps = profile.Metadata.MaxSteps
	}
	if maxSteps <= 0 {
		// Default budget derives from routing complexity (inherited via
		// context), matching the Conductor's own limit.
		maxSteps = ComplexityFromContext(ctx) * stepsPerComplexity
	}

	// A profile's tool preference (when non-nil) overrides the task field. The
	// profile is the authoritative tool grant for a specialized subagent;
	// resolveTaskTools accepts ToolPreferenceWithError's []string group-token form
	// directly (parseToolGroups handles both []string and []any). An invalid
	// `tools` field (unknown/duplicate group token) fails the delegation —
	// never silently widened to the full toolset.
	if profile != nil {
		pref, err := profile.ToolPreferenceWithError()
		if err != nil {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: profile %q: %w", t.ID, t.Agent, err)
		}
		if pref != nil {
			t.Tools = pref
		}
	}
	taskTools, err := l.resolveTaskTools(t)
	if err != nil {
		return agent.SubAgentTask{}, err
	}
	taskDesc := l.buildTaskDescription(t, registry, maxSteps)

	// Resume a paused subagent from its checkpoint: the partial trajectory
	// (Steps) persisted on the blackboard is seeded into both the executor and
	// the ContextManager so the subagent continues from where it stopped
	// instead of restarting. Only a cooperative-pause checkpoint is resumed; a
	// failed step is re-run fresh.
	var resumeSteps []agent.Step
	if cp, ok := l.pausedCheckpoint(t.ID); ok {
		resumeSteps = append([]agent.Step(nil), cp.Steps...)
	}

	// Derive compaction strategy from the Conductor's routing domain +
	// complexity (inherited via context). Subagents inherit the same
	// domain/complexity as the Conductor unless the task overrides it.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)

	modelMeta := l.resolveModelMeta(ctx)

	// System prompt: a profile's body REPLACES the OrchestratorSystem core
	// directive while preserving the shared project-context prefix (workspace,
	// AGENTS.md, env, active skills, ...) via buildSpecializedSystemPrompt.
	// Without a profile, the standard orchestrator prompt is assembled.
	systemPrompt := buildSystemPrompt(ctx, t.Task, modelMeta)
	if profile != nil && profile.Body != "" {
		systemPrompt = buildSpecializedSystemPrompt(ctx, t.Task, modelMeta, profile.Body)
	}

	var cm agent.ContextManager
	if l.deps.contextFactory != nil {
		cm = l.deps.contextFactory(systemPrompt, modelMeta, compactionStrategy)
	} else {
		return agent.SubAgentTask{}, fmt.Errorf("delegation %q: context factory not configured", t.ID)
	}
	if ccm, ok := cm.(interface{ SetTask(string) }); ok {
		ccm.SetTask(taskDesc)
	}
	if len(resumeSteps) > 0 {
		sscm, ok := cm.(orchestration.StepSeedable)
		if !ok {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: paused checkpoint requires a StepSeedable context manager (got %T)", t.ID, cm)
		}
		sscm.SeedSteps(resumeSteps)
	}

	caller := l.callerForStep(cm, t.ID)
	// A profile's per-agent model override forces req.Model on every call.
	// NewModelOverrideCaller is a no-op (returns inner unchanged) when the
	// model string is empty, so this is safe unconditionally.
	if profile != nil {
		caller = agent.NewModelOverrideCaller(caller, profile.Metadata.Model)
	}
	execOpts := []agent.Option{
		agent.WithTokenCounter(l.deps.tokenCounter),
		agent.WithEvents(scopedEvents),
		agent.WithToolResultBudget(l.deps.toolResultBudget),
		agent.WithCircuitBreaker(l.deps.circuitBreaker),
		agent.WithHITL(l.deps.hitlHandler),
	}
	if len(resumeSteps) > 0 {
		execOpts = append(execOpts, agent.WithResumeSteps(resumeSteps))
	}
	executor := agent.NewExecutor(caller, l.deps.toolExec, maxSteps, execOpts...)
	executor.SetPlanContext(t.ID, 0, 0)
	l.configureExecutor(executor)

	return agent.SubAgentTask{
		StepID:         t.ID,
		Executor:       executor,
		CM:             cm,
		TaskTools:      taskTools,
		TaskDesc:       taskDesc,
		Emitter:        scopedEvents,
		TodoUpdateFunc: subagentTodoCallback(l.deps.emitter),
	}, nil
}

// conductorOnlyToolNames are tools that must never be handed to a regular
// (non-redelegating) subagent: delegate/cancel_delegation would let it spawn
// further subagents outside the allow_redelegate/depth-cap machinery, and
// declare_plan/reflect operate on the Conductor's own blackboard/trajectory.
var conductorOnlyToolNames = map[string]struct{}{
	"delegate":          {},
	"cancel_delegation": {},
	"declare_plan":      {},
	"execute_plan":      {},
	"reflect":           {},
}

// coreNonCacheableToolNames are c0wrk-specific meta-tools whose results should
// not be cached. These are registered by core/tools and extend the sp4rk-provided
// defaultNonCacheableTools set via Executor.AddNonCacheableTools. They produce
// tiny or stateful outputs where caching adds overhead.
var coreNonCacheableToolNames = []string{
	"delegate",
	"cancel_delegation",
	"declare_plan",
	"execute_plan",
	"reflect",
	"declare_step_complete",
	"ask_user",
}

func stripConductorOnlyTools(descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	out := make([]sdktools.ToolDescriptor, 0, len(descs))
	for _, d := range descs {
		if _, ok := conductorOnlyToolNames[d.Name]; ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

// stripSubagentTools removes everything a delegated task agent must never
// hold, layered on the mode-disabled filter the caller already applied: the
// Conductor-only orchestration tools (delegate, declare_plan, reflect, ...)
// and the goal-loop actor tools (propose_goal, declare_goal_status,
// declare_verification). The goal tools are GroupSystem, so without this
// strip they would ride the always-granted system group into every subagent —
// but only the goal-loop Conductor and the independent verifier may ever call
// them (the verifier's toolset is built by verifierToolFilter from the raw
// registry list, not via resolveTaskTools, and keeps declare_verification).
// A task subagent that declared a goal status or verification verdict would
// forge the goal loop's state-machine inputs; subagentCtx additionally clears
// the goal/verification sinks as a second layer.
func stripSubagentTools(descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	return tools.StripGoalModeTools(stripConductorOnlyTools(descs))
}

// resolveTaskTools computes a delegation task's toolset from the capability
// groups carried by DelegationTask.Tools:
//
//   - nil (or the strings ""/"all"): everything, minus Conductor-only and
//     mode-disabled tools — the default full grant.
//   - "read-only": the read-only preset = system ∪ local_read ∪ remote_read.
//     MCP tools are NOT part of the preset — installing an MCP server must
//     never silently widen a read-only delegation (see the regression test in
//     conductor_tools_test.go).
//   - a non-empty array of group tokens (kebab "local-read" or underscore
//     "local_read" spelling): system ∪ the granted groups. The system group
//     (finish, fact memory, checklist, meta tools) is always granted — a
//     subagent that cannot finish or record facts is unusable — and nothing
//     else is added: the grant is exactly what was requested. An empty array
//     is an error, not a valid way to request a system-only subagent (that is
//     ["system"]): the resolver fails closed instead of silently stripping
//     every working tool.
//
// Anything else fails the delegation with a comprehensible error (fail-closed
// rather than silently degrading to a minimal set). A tool whose capability
// group is not declared (zero ToolGroup) matches no grant. Conductor-only
// tools AND the goal-loop actor tools are stripped in every branch (see
// stripSubagentTools) — the latter ride the always-granted system group but
// belong to the goal-loop Conductor and the independent verifier only; the
// redelegating path re-adds delegate and cancel_delegation explicitly (see
// runRedelegBlocking).
func (l *conductorLauncher) resolveTaskTools(t tools.DelegationTask) ([]sdktools.ToolDescriptor, error) {
	all := l.stripDisabled(l.allToolDescriptors())

	// No explicit tool request: give everything (minus conductor-only and
	// mode-disabled tools).
	if t.Tools == nil {
		return stripSubagentTools(all), nil
	}
	switch v := t.Tools.(type) {
	case string:
		switch v {
		case "", "all":
			return stripSubagentTools(all), nil
		case "read-only":
			grants := map[sdktools.ToolGroup]struct{}{
				sdktools.GroupSystem:     {},
				sdktools.GroupLocalRead:  {},
				sdktools.GroupRemoteRead: {},
			}
			return stripSubagentTools(l.toolsByGroups(all, grants)), nil
		default:
			return nil, fmt.Errorf("unknown tools value %q (valid: \"all\", \"read-only\", or an array of group names: %s)",
				v, strings.Join(agents.ToolGroupTokens(), ", "))
		}
	default:
		grants, err := parseToolGroups(t.Tools)
		if err != nil {
			return nil, err
		}
		grants[sdktools.GroupSystem] = struct{}{}
		return stripSubagentTools(l.toolsByGroups(all, grants)), nil
	}
}

// toolsByGroups returns the descriptors whose capability group is in grants,
// deduplicated by name (duplicates make LLM providers reject the request).
// Descriptors with an undeclared (zero) group never match — fail-closed for
// tools that forgot to declare their group.
func (l *conductorLauncher) toolsByGroups(all []sdktools.ToolDescriptor, grants map[sdktools.ToolGroup]struct{}) []sdktools.ToolDescriptor {
	out := make([]sdktools.ToolDescriptor, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, ok := seen[d.Name]; ok {
			continue
		}
		if _, ok := grants[d.Group]; !ok {
			continue
		}
		seen[d.Name] = struct{}{}
		out = append(out, d)
	}
	return out
}

// parseToolGroups parses DelegationTask.Tools' array form — either []any from
// the delegate tool's JSON input or []string from an agent profile's
// ToolPreference — into a set of capability groups. Accepts kebab and
// underscore spellings; unknown tokens fail with the list of valid groups.
// An empty token list fails too: it would otherwise resolve to a grant of
// only the always-added system group — a subagent stripped of every working
// tool (validateDelegationTools rejects the JSON form up front; this guards
// programmatic DelegationTask construction).
func parseToolGroups(v any) (map[sdktools.ToolGroup]struct{}, error) {
	var tokens []string
	switch arr := v.(type) {
	case []any:
		tokens = make([]string, 0, len(arr))
		for _, item := range arr {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("tools: group names must be strings, got %T", item)
			}
			tokens = append(tokens, s)
		}
	case []string:
		tokens = arr
	default:
		return nil, fmt.Errorf("unexpected tools type %T (valid: \"all\", \"read-only\", or an array of group names)", v)
	}
	if len(tokens) == 0 {
		return nil, errors.New("tools: empty array — omit the field (or pass \"all\") for the full toolset; to grant only the always-included system group, pass [\"system\"]")
	}
	grants := make(map[sdktools.ToolGroup]struct{}, len(tokens))
	for _, tok := range tokens {
		g, err := parseToolGroupToken(tok)
		if err != nil {
			return nil, err
		}
		grants[g] = struct{}{}
	}
	return grants, nil
}

// parseToolGroupToken canonicalizes a single group token (kebab or underscore)
// to its sdktools.ToolGroup value, failing on unknown tokens.
func parseToolGroupToken(tok string) (sdktools.ToolGroup, error) {
	g := sdktools.ToolGroup(strings.ReplaceAll(strings.TrimSpace(tok), "-", "_"))
	if !sdktools.IsValidToolGroup(g) {
		return "", fmt.Errorf("unknown tool group %q (valid groups: %s)", tok, strings.Join(agents.ToolGroupTokens(), ", "))
	}
	return g, nil
}

func (l *conductorLauncher) allToolDescriptors() []sdktools.ToolDescriptor {
	if l.deps.toolRegistry != nil {
		return l.deps.toolRegistry.List()
	}
	return nil
}

// stripDisabled removes tools disabled for the current mode (e.g. CHAT /
// No-Project mode) from the descriptor list. Disabled tools must never be
// advertised to a subagent's LLM even though runtime Execute() would block
// them too — advertising dead tools wastes the tool-call budget and misleads
// the model.
func (l *conductorLauncher) stripDisabled(descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	if len(l.deps.disabledTools) == 0 {
		return descs
	}
	out := make([]sdktools.ToolDescriptor, 0, len(descs))
	for _, d := range descs {
		if !l.deps.disabledTools[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// toolDescriptorByName looks up a single tool's full descriptor (including
// its InputSchema) from the shared registry.
func (l *conductorLauncher) toolDescriptorByName(name string) (sdktools.ToolDescriptor, bool) {
	for _, d := range l.allToolDescriptors() {
		if d.Name == name {
			return d, true
		}
	}
	return sdktools.ToolDescriptor{}, false
}

func (l *conductorLauncher) buildTaskDescription(t tools.DelegationTask, registry *tools.DelegationRegistry, maxSteps int) string {
	var b strings.Builder
	// The agent name (when set) prefixes the delegation header so SubAgentLaunch
	// UI events surface which specialized profile is running.
	if t.Agent != "" {
		fmt.Fprintf(&b, "[Delegation %s · agent: %s] %s\n\n", t.ID, t.Agent, t.Task)
	} else {
		fmt.Fprintf(&b, "[Delegation %s] %s\n\n", t.ID, t.Task)
	}
	fmt.Fprintf(&b, "Tool call budget: %d iterations. Plan your approach to finish within this budget.\n\n", maxSteps)
	b.WriteString("IMPORTANT: You are executing ONE delegated task. Complete ONLY this task's objective. Do NOT perform work outside the task scope.\n\n")
	b.WriteString("## Checklist\nBuild a checklist at the start (update_checklist — omit step_id, it is inferred from your execution context) listing the concrete sub-tasks for this work, and report progress on each item as you complete it.\n\n")

	if len(t.AcceptanceCriteria) > 0 {
		b.WriteString("## Acceptance Criteria\n")
		for _, c := range t.AcceptanceCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n## Pre-Finish Verification\nBefore calling the finish tool, verify each acceptance criterion is satisfied. Use tool calls to confirm, not assumptions.\n\n")
	} else {
		b.WriteString("## Pre-Finish Verification\nBefore calling the finish tool, verify the task objective is satisfied. Use tool calls to confirm, not assumptions.\n\n")
	}

	if len(t.DependsOn) > 0 {
		var depBuf strings.Builder
		for _, depID := range t.DependsOn {
			d := registry.Get(depID)
			if d != nil && d.Output != "" {
				fmt.Fprintf(&depBuf, "\n### [%s]: %s\n%s\n", depID, d.Summary, d.Output)
			}
		}
		depContext := depBuf.String()
		maxDepChars := l.deps.maxDepCtxChars
		if maxDepChars <= 0 {
			maxDepChars = 8000
		}
		if len(depContext) > maxDepChars {
			start := len(depContext) - maxDepChars
			// Walk forward to the next UTF-8 rune boundary so the tail does
			// not begin inside a multi-byte sequence (which would inject
			// invalid UTF-8 into the subagent's task description).
			for start < len(depContext) && !utf8.RuneStart(depContext[start]) {
				start++
			}
			depContext = depContext[start:]
		}
		if depContext != "" {
			b.WriteString("\n## Context from previous delegations\n")
			b.WriteString(depContext)
			b.WriteString("\n\nIf insufficient, use read_step_output with the delegation ID to access the full output.\n")
		}
	}

	b.WriteString("Pass your result through the finish tool.\n")
	return b.String()
}

func (l *conductorLauncher) resolveModelMeta(ctx context.Context) llm.ModelMetadata {
	// Resolve always returns usable metadata — the ok flag indicates whether
	// the model was found in a known source, but the fallback
	// (ContextWindow=128000, OutputLimit=32768) is always usable. A zero
	// ContextWindow disables compaction, causing unbounded conversation growth.
	if l.deps.modelRegistry != nil && l.deps.model != "" {
		if meta, _ := l.deps.modelRegistry.Resolve(ctx, l.deps.model); meta.ContextWindow > 0 {
			return meta
		}
	}
	if l.deps.modelRegistry != nil {
		if meta, _ := l.deps.modelRegistry.Resolve(ctx, ""); meta.ContextWindow > 0 {
			return meta
		}
	}
	return llm.ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   32768,
		TokenizerType: "approximate",
	}
}

func (l *conductorLauncher) callerForStep(cm agent.ContextManager, stepID string) agent.LLMCaller {
	if l.deps.trackingCaller == nil {
		return l.deps.llm
	}
	var caller agent.LLMCaller = l.deps.trackingCaller
	if ctm, ok := cm.(interface {
		ContextTracker() *llm.ContextTokenTracker
	}); ok {
		caller = l.deps.trackingCaller.WithContextTracker(ctm.ContextTracker())
	}
	// Logging is independent of step dumps: it must remain active whenever a
	// provider name and logger are available (mirroring callerForConductor),
	// so that step/subagent LLM calls are logged even when step dumps are
	// disabled. Step dumps are still gated on stepDumpTracker below.
	if l.deps.providerName != "" && l.deps.logger != nil {
		caller = agent.NewLoggingLLMCaller(caller, l.deps.providerName, l.deps.logger)
	}
	if l.deps.stepDumpTracker != nil {
		if w := l.deps.stepDumpTracker.OpenStepDump(stepID); w != nil {
			caller = agent.NewDumpCaller(caller, w, l.deps.logger)
		}
	}
	return caller
}

func (l *conductorLauncher) scopeEvents(stepID string) agent.Events {
	if sc, ok := l.deps.emitter.(interface {
		WithPlanStepID(string) Emitter
	}); ok {
		return sc.WithPlanStepID(stepID)
	}
	return &agent.NoopEvents{}
}

// planStepEventTranslator wraps a scoped emitter and translates the sp4rk
// subagent lifecycle events (SubAgentLaunch/SubAgentComplete — which are
// hard-coded inside RunSubAgent) into plan-step lifecycle events
// (PlanStepStart/PlanStepComplete). This lets plan steps reuse the same
// subagent machinery (RunSubAgentsParallel, isolated context, parallel waves)
// while appearing in the UI as plan steps, not delegations.
//
// The translator holds TWO emitter references:
//   - scoped (embedded): the WithPlanStepID copy — child events (ToolCall,
//     Thought, AssistantChunk, etc.) carry plan_step_id so they nest under
//     the step block in the frontend.
//   - root: the session-root emitter — PlanStepStart/PlanStepComplete are
//     called here because the scoped copy does NOT share planStartedSet /
//     planCompletedSet / planTotalSteps (WithPlanStepID creates a shallow
//     copy without those fields). Calling on root ensures correct progress
//     tracking and duplicate suppression.
type planStepEventTranslator struct {
	Emitter         // scoped copy (child events with plan_step_id)
	root    Emitter // session-root emitter (plan-step lifecycle + progress)
	summary string  // short UI label for PlanStepStart
}

func (t *planStepEventTranslator) SubAgentLaunch(stepID, description string) {
	t.root.PlanStepStart(stepID, description, t.summary)
}

func (t *planStepEventTranslator) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	t.root.PlanStepComplete(stepID, success, duration, "")
}

// scopePlanStepEvents returns an agent.Events suitable for plan-step execution:
// a planStepEventTranslator wrapping the WithPlanStepID-scoped emitter (for
// child events) and the session-root emitter (for plan-step lifecycle events).
// If the emitter does not support WithPlanStepID, returns NoopEvents.
func (l *conductorLauncher) scopePlanStepEvents(stepID, summary string) agent.Events {
	sc, ok := l.deps.emitter.(interface {
		WithPlanStepID(string) Emitter
	})
	if !ok {
		return &agent.NoopEvents{}
	}
	scoped := sc.WithPlanStepID(stepID)
	return &planStepEventTranslator{
		Emitter: scoped,
		root:    l.deps.emitter,
		summary: summary,
	}
}

func (l *conductorLauncher) configureExecutor(executor *agent.Executor) {
	// Cooperative pause signal: subagent executors must observe the same
	// universal pause signal as the Conductor so a PauseSession pauses them at
	// their own step boundary (mid-task) instead of letting them run to
	// natural completion. Nil disables pausing (default) — mirrors the
	// Conductor's ConductorConfig.PauseChecker wiring.
	if l.deps.pauseChecker != nil {
		executor.SetPauseChecker(l.deps.pauseChecker)
	}
	if l.deps.hitlHandler != nil {
		executor.SetHITLHandler(l.deps.hitlHandler)
	}
	if l.deps.preWarningPct > 0 {
		executor.SetPreWarningPercent(l.deps.preWarningPct)
	}
	if l.deps.toolCache != nil {
		executor.SetToolCache(l.deps.toolCache)
	}
	if l.deps.perToolTrunc != nil {
		executor.SetPerToolTruncation(l.deps.perToolTrunc)
	}
	if l.deps.reasoningEffort != "" {
		executor.SetReasoningEffort(l.deps.reasoningEffort)
	}
	// Mechanical edit verification (executor.verify_on_edit): subagent
	// executors that perform file edits get the same hook as the main
	// executor. RunConductor nils deps.verifyOnEdit for specialized goal
	// passes before the launcher is built, so this is automatically inert
	// there.
	if l.deps.verifyOnEdit != nil {
		executor.SetVerifyOnEdit(l.deps.verifyOnEdit, l.deps.verifyOnEditMaxOutputChars)
	}
	executor.AddNonCacheableTools(coreNonCacheableToolNames...)
}

// conductorPublisher implements tools.PlanPublisher.
type conductorPublisher struct {
	emitter   Emitter
	bb        orchestration.Blackboard
	plansDir  string
	logger    *slog.Logger
	lastMD    string // markdown from the most recent Publish call
	planState *planRunState
}

func (p *conductorPublisher) Publish(ctx context.Context, tasks []tools.PlanTaskInput) (string, error) {
	plan := &orchestration.Plan{}
	for _, t := range tasks {
		plan.Steps = append(plan.Steps, orchestration.PlanStep{
			ID:          t.ID,
			Summary:     t.Summary,
			Description: t.Description,
			DependsOn:   append([]string(nil), t.DependsOn...),
			Agent:       t.Agent,
		})
	}

	if p.plansDir == "" {
		return "", errors.New("declare_plan: session plans directory not configured")
	}
	if err := os.MkdirAll(p.plansDir, 0o755); err != nil {
		return "", fmt.Errorf("declare_plan: failed to create plans directory: %w", err)
	}
	md := SerializePlan(plan)
	p.lastMD = md
	suffix := RandomSuffix()
	path := filepath.Join(p.plansDir, fmt.Sprintf("plan_%s.md", suffix))
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "", fmt.Errorf("declare_plan: failed to write plan file: %w", err)
	}

	// Emit plan events and set plan on blackboard only after the file is
	// persisted — if the write fails, the UI should not show a plan that
	// can't be reviewed or approved.
	planEvents := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planEvents[i] = orchestration.PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
	}
	p.emitter.PlanGenerated(len(plan.Steps), planEvents)
	p.bb.SetPlan(plan)
	// Record that a plan was declared in THIS Conductor run. This is what
	// activates the ChecklistGuard / delegate guard — a restored plan from a
	// previous (completed) task never reaches here, so continuations stay free
	// to act plan-less, declare a new plan, or delegate.
	if p.planState != nil {
		p.planState.markDeclared()
	}

	if p.logger != nil {
		p.logger.Debug("declare_plan: plan published", "path", path, "steps", len(plan.Steps))
	}
	return path, nil
}

// LastPlanMarkdown returns the markdown content from the most recent Publish call.
func (p *conductorPublisher) LastPlanMarkdown() string {
	return p.lastMD
}

// conductorReflectionRunner implements tools.ReflectionRunner.
type conductorReflectionRunner struct {
	reflector *reflector.Reflector
	bb        orchestration.Blackboard
	emitter   Emitter
	logger    *slog.Logger
	traj      agent.TrajectoryStore
}

func (r *conductorReflectionRunner) Reflect(ctx context.Context, scope, delegationID string) (tools.ReflectionResult, error) {
	if r.reflector == nil {
		return tools.ReflectionResult{}, errors.New("reflect: no reflector configured")
	}

	var trajectory []agent.Step
	if scope == "delegation" {
		reg := tools.DelegationRegistryFrom(ctx)
		if reg == nil {
			return tools.ReflectionResult{}, errors.New("reflect: no delegation registry in context")
		}
		d := reg.Get(delegationID)
		if d == nil {
			return tools.ReflectionResult{}, fmt.Errorf("reflect: unknown delegation id %q", delegationID)
		}
		trajectory = d.Steps
	} else if r.traj != nil {
		trajectory = r.traj.Steps()
	}

	plan := r.bb.GetPlan()
	prevReflections := r.bb.GetReflections()

	reflection, err := r.reflector.Reflect(ctx, trajectory, plan, prevReflections)
	if err != nil {
		return tools.ReflectionResult{}, fmt.Errorf("reflect failed: %w", err)
	}
	if reflection == nil {
		return tools.ReflectionResult{Summary: "no reflection produced"}, nil
	}

	r.bb.AddReflection(*reflection)
	r.emitter.Reflection(reflection, 0, 0)
	if r.logger != nil {
		r.logger.Debug("reflect: reflection produced", "summary", reflection.Summary, "suggested_action", reflection.SuggestedAction)
	}

	return tools.ReflectionResult{
		Summary:         reflection.Summary,
		SuggestedAction: reflection.SuggestedAction,
		RootCause:       reflection.RootCause,
		ActionPlan:      reflection.ActionPlan,
	}, nil
}

// stepsPerComplexity is the multiplier that derives the ReAct iteration
// limit from the routing complexity: limit = complexity × stepsPerComplexity.
// It applies uniformly to the Conductor's own loop and to every subagent
// (delegates and plan-step executors) unless a delegation overrides it via
// its per-task max_steps.
const stepsPerComplexity = 30

// compactionStrategyForDomain maps a routing domain + complexity to the
// compaction strategy per ADR-012 / specs/domains/orchestration/router.md:
//
//	code     → sliding_window (keep recent edits visible)
//	research → summarization (condense findings)
//	general  → sliding_window, or hierarchical if complexity >= 4
//	mixed    → sliding_window
func compactionStrategyForDomain(domain string, complexity int) string {
	switch domain {
	case "research":
		return "summarization"
	case "general":
		if complexity >= 4 {
			return "hierarchical"
		}
		return "sliding_window"
	default:
		return "sliding_window"
	}
}

// conductorGuidanceForComplexity returns a system-prompt section that guides
// the Conductor on when to plan, delegate, or handle inline, based on the
// routing complexity.
//
// Planning and delegation are ORTHOGONAL mechanisms:
//   - Planning (declare_plan + execute_plan) is for managing task COMPLEXITY.
//     declare_plan publishes a roadmap to the USER and optionally blocks for
//     approval. execute_plan runs ALL plan steps in DAG order with parallelism.
//   - Delegation (delegate) is for optimizing Conductor context usage and
//     session time. It is for plan-less tasks only — once a plan is declared,
//     delegate is disabled and execute_plan is the only execution path for
//     plan steps.
//
// These two mechanisms must never be mixed: once a plan is declared, use
// execute_plan, never delegate, to execute plan steps.
func conductorGuidanceForComplexity(complexity int) string {
	// Skill-prescribed planning is a GLOBAL clause emitted once, above the
	// complexity bands, so a future edit to an individual band cannot silently
	// drop it. Dropping it is exactly the regression that made the `explore`
	// skill stop entering Plan Mode: the band text said "do NOT call
	// declare_plan" and there was no overriding clause. The clause is
	// instruction-level ("soft by form") enforcement — c0wrk skills are pure
	// markdown with no agent-specific metadata (see ADR-012), and their bodies
	// are already injected into the system prompt, so the Conductor reads the
	// approval-gate language directly from the active skill.
	skillClause := "## Skill-prescribed planning — overrides the bands below\n" +
		"If any active skill's instructions mandate presenting a plan or roadmap and obtaining user approval before implementation — e.g. an approval gate, a sign-off step, or an explicit \"do not proceed without an approved roadmap\" rule — you MUST call declare_plan with mode=await_approval before starting implementation, regardless of complexity. This supersedes any \"do not plan\" guidance in the bands below.\n\n"

	orthogonality := "\n\n## Planning vs Delegation (orthogonal mechanisms)\n" +
		"Planning (declare_plan + execute_plan) manages task COMPLEXITY and gets user sign-off. " +
		"Delegation (delegate) optimizes Conductor context and session time. " +
		"They are ORTHOGONAL — never mix them. Once a plan is declared, delegate is disabled; use execute_plan to execute plan steps.\n"

	switch {
	case complexity <= 1:
		return skillClause +
			"## Conductor Guidance\nYou are the Conductor: you own this task end-to-end. This is a simple task — handle it inline (read files, search, answer, call finish). No checklist, delegate, or plan needed.\n"
	default:
		// complexity >= 2: the Conductor decides for itself whether to plan.
		// Planning is recommended (not required) above complexity 3 or when the
		// task decomposes into a DAG of independent steps; the only mandatory
		// trigger is the skill clause above.
		return skillClause +
			"## Conductor Guidance\n" +
			"You are the Conductor: you own this task end-to-end. You decide whether this task needs a plan.\n\n" +
			"Planning is RECOMMENDED (not required) when complexity is high (>3) OR when the task can be solved more efficiently by decomposing it into a DAG of independent steps — call declare_plan with mode=await_approval so the user signs off before any implementation, then execute_plan runs the steps in dependency-ordered parallel waves after approval. Use mode=present (display-only) only for low-stakes progress-shaping. Otherwise handle it plan-less: proceed inline, or delegate coherent units to subagents. The decision is yours — weigh whether user sign-off or parallelism genuinely helps before planning.\n\n" +
			"If you go plan-less, you MUST build a checklist (update_checklist with an empty step_id) at the start and report progress on each item as you complete it.\n\n" +
			"You MAY call delegate to break coherent units of work into isolated subagents — to keep your context lean or to parallelize work. Each delegate also builds its own checklist and reports progress. delegate does NOT require a plan. When a named subagent fits the work (see the \"Available Subagents\" section), target it via delegate(agent: \"name\") so the work runs with that agent's specialty and tool budget. If the user named specific subagents via #mentions (see \"Requested Subagents\"), you MUST delegate the corresponding work to those agents rather than handling it inline.\n\n" +
			"When the trajectory looks wrong, call reflect.\n\n" +
			"Call finish when the task is complete." + orthogonality
	}
}

// RunConductor builds and launches the Conductor: a single Executor.Run that
// owns the task end-to-end. The Conductor's tool set includes the standard
// file/search/internal tools plus the Conductor tools (delegate, declare_plan,
// reflect, cancel_delegation), wired via context-injected implementations.
//
// The core layer is responsible for:
//   - Injecting Conductor tools (DelegationRegistry, DelegationLauncher,
//     PlanPublisher, ReflectionRunner, TrajectoryStore) into the context
//   - Appending the Conductor Guidance section to the system prompt
//
// The sp4rk-level orchestration.Conductor handles:
//   - Model metadata resolution and system prompt construction
//   - ContextManager creation
//   - Executor construction and configuration
//   - finish guard via executor.SetFinishGuard (rejects finish with a nudge while async delegations are pending)
//   - StepOutputStore and FactStore injection
//   - ExecutionResult assembly
func RunConductor(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	deps conductorDeps,
	plansDir string,
) (*orchestration.ExecutionResult, error) {
	// planRunState is shared between the publisher (marks it on declare_plan),
	// the launcher (HasDeclaredPlan reads it), and the inline-step lifecycle
	// (completeAll gates plan-step synthesis on it). One fresh instance per
	// Conductor run, so a restored plan from a previous (completed) task does
	// not count as "declared" — a continuation stays free to act plan-less,
	// declare a new plan, or delegate.
	planState := &planRunState{}

	// Build the inline-step lifecycle up front so it can be threaded into deps
	// (consumed by Execute) rather than wired by post-construction field
	// mutation. It is shared with update_checklist / declare_step_complete via
	// the context callbacks below. planState is wired in so completeAll does
	// not synthesize terminal events for a restored plan on a continuation.
	inlineLifecycle := newInlineStepLifecycle(deps.emitter, bb)
	inlineLifecycle.planState = planState
	deps.lifecycle = inlineLifecycle

	registry := tools.NewDelegationRegistry()
	launcher := &conductorLauncher{deps: deps, bb: bb, planState: planState}
	publisher := &conductorPublisher{emitter: deps.emitter, bb: bb, plansDir: plansDir, logger: deps.logger, planState: planState}

	// Build the trajectory store. The in-memory holder feeds the reflect tool
	// synchronously; the composite layer additionally persists the full
	// trajectory to the DB (best-effort) so it survives app restart. The task
	// ID is only available on a PersistableBlackboard — without it the store
	// degrades to purely in-memory.
	trajHolder := &trajectoryHolder{}
	taskID := ""
	if pbb, ok := bb.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	trajStore := newCompositeTrajectoryStore(trajHolder, taskID, deps.taskStore, deps.logger)
	runner := &conductorReflectionRunner{reflector: deps.reflector, bb: bb, emitter: deps.emitter, logger: deps.logger, traj: trajStore}

	// Derive compaction strategy from routing domain + complexity.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)
	// One-shot manual-compaction override (resume with a user-selected
	// strategy): replace the routing-derived strategy for the WHOLE run and
	// force a start-of-run compaction pass. The override intentionally spans
	// the entire run, not just the forced pass: the user picked the strategy
	// for this resumed task, so reactive threshold-driven compactions later in
	// the run must use it too rather than switching back to the routing
	// default mid-run. CompactOnStart makes the sp4rk engine compact the
	// merged trajectory (seeded resumeSteps + this run) exactly once, before
	// the first LLM call; Compact is a no-op when there is nothing to compact.
	if deps.forceCompactionStrategy != "" {
		compactionStrategy = deps.forceCompactionStrategy
	}
	guidance := conductorGuidanceForComplexity(complexity)

	// Inject Conductor-specific context values so the tools (delegate,
	// declare_plan, reflect, cancel_delegation) can find their dependencies.
	ctx = tools.WithDelegationRegistry(ctx, registry)
	ctx = tools.WithDelegationLauncher(ctx, launcher)
	ctx = tools.WithPlanPublisher(ctx, publisher)
	ctx = tools.WithReflectionRunner(ctx, runner)
	ctx = tools.WithPlanChecker(ctx, launcher)
	// The agent resolver is inherited by subagent contexts (subagentCtx does
	// NOT strip it), so a redelegating subagent can itself delegate to a named
	// agent profile. Nil resolver = profiles unavailable (delegate validation
	// rejects a non-empty `agent` field).
	ctx = tools.WithAgentResolver(ctx, deps.agentResolver)
	ctx = agent.WithTrajectoryStore(ctx, trajStore)
	ctx = orchestration.WithDelegationRegistry(ctx, registry)

	// Inject the inline-step lifecycle so update_checklist can emit
	// StepTodoUpdate + inferred PlanStepStart, and declare_step_complete can
	// emit PlanStepComplete. Subagents get their own (observation-only)
	// callback via buildSubAgentTask.TodoUpdateFunc. The lifecycle was created
	// above and is shared with the launcher via deps.lifecycle.
	ctx = agent.WithStepTodoUpdateFunc(ctx, inlineLifecycle.onChecklistUpdate)
	ctx = tools.WithStepCompleteFunc(ctx, inlineLifecycle.completeStep)
	ctx = tools.WithPlanStepExecutor(ctx, launcher)

	// Checklist guard: once a plan is declared IN THIS RUN, reject standalone
	// (empty step_id) checklists. A standalone checklist is only valid for
	// plan-less tasks. With a plan, every update_checklist must target a
	// specific step. This consults launcher.HasDeclaredPlan() (planRunState)
	// rather than the raw blackboard plan, so a plan restored from a previous
	// (completed) task does NOT trip the guard on a continuation — the
	// continuation is free to act plan-less, declare its own plan, or delegate.
	ctx = agent.WithChecklistGuard(ctx, func(stepID string) string {
		if stepID == "" && launcher.HasDeclaredPlan() {
			return "a plan has been declared; a standalone checklist (without step_id) is only valid for plan-less tasks — pass the step_id of the plan step you are executing, and do not list plan steps as checklist items (a checklist tracks sub-tasks within a single step)"
		}
		return ""
	})

	// Build the system prompt with complexity-based Conductor Guidance.
	systemPromptFactory := func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		prompt := buildSystemPrompt(ctx, msg, modelMeta)
		prompt += "\n\n" + guidance
		return prompt
	}
	// Goal derivation (and other specialized runs) override the standard
	// system prompt. The override is expected to build on the shared
	// project-context prefix (via buildSpecializedSystemPrompt) rather than
	// return a bare directive, so the specialized agent keeps the same
	// project context as a normal run. All other conductor wiring is reused.
	if deps.systemPromptOverride != nil {
		systemPromptFactory = deps.systemPromptOverride
	}

	// maxSteps bounds the ReAct loop. It derives from routing complexity:
	// limit = complexity × stepsPerComplexity. This applies uniformly to
	// normal runs and specialized passes (including the goal-verification
	// pass), so the verifier is never more limited than the executor it
	// checks — it is bounded exactly like a normal executor run.
	maxSteps := complexity * stepsPerComplexity

	// verify-on-edit is suppressed for specialized passes (goal derivation /
	// goal verification / ask-user) — those override the system prompt and run
	// with model-specific budgets; mechanical edit verification belongs to
	// ordinary CODE-task execution only. Nilling deps.verifyOnEdit here (deps
	// is a value copy) also disarms the subagent executors the launcher later
	// builds from these deps.
	if deps.systemPromptOverride != nil {
		deps.verifyOnEdit = nil
	}

	cfg := orchestration.ConductorConfig{
		LLM:                        callerForConductor(deps),
		Tools:                      deps.toolExec,
		ToolRegistry:               deps.toolRegistry,
		TokenCounter:               deps.tokenCounter,
		Model:                      deps.model,
		ModelRegistry:              deps.modelRegistry,
		ContextFactory:             adaptContextFactory(deps.contextFactory),
		SystemPrompt:               systemPromptFactory,
		MaxSteps:                   maxSteps,
		ToolResultBudget:           deps.toolResultBudget,
		CircuitBreaker:             deps.circuitBreaker,
		HITLHandler:                deps.hitlHandler,
		ToolCache:                  deps.toolCache,
		PerToolTruncation:          deps.perToolTrunc,
		ReasoningEffort:            deps.reasoningEffort,
		PreWarningPercent:          deps.preWarningPct,
		NonCacheableTools:          coreNonCacheableToolNames,
		ConversationHistory:        deps.conversationHistory,
		ResumeSteps:                deps.resumeSteps,
		CompactOnStart:             deps.forceCompactionStrategy != "",
		ContentBlocks:              deps.contentBlocks,
		PendingUserInterjection:    deps.nudge,
		PauseChecker:               deps.pauseChecker,
		UserMessageSource:          deps.userMessageSource,
		VerifyOnEdit:               deps.verifyOnEdit,
		VerifyOnEditMaxOutputChars: deps.verifyOnEditMaxOutputChars,
	}

	var events agent.Events = &agent.NoopEvents{}
	if deps.emitter != nil {
		events = deps.emitter
	}

	conductor := orchestration.NewConductor(cfg)
	result, err := conductor.Run(ctx, message, bb, availableTools, events, compactionStrategy)
	// Finish fallback: auto-complete any inline steps that were started but
	// not explicitly completed via declare_step_complete. This prevents
	// steps from being stuck in "running" state if the Conductor forgot to
	// call declare_step_complete before finishing. On failure, propagate the
	// conductor error so the UI can show why each pending step failed.
	success := err == nil && result != nil && result.Status == orchestration.ExecutionStatusSuccess
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	inlineLifecycle.completeAll(success, errMsg)

	// Final trajectory flush: the ReAct loop has ended, so no more Syncs will
	// arrive. This drains any in-flight async write and persists the freshest
	// snapshot synchronously, guaranteeing an interrupted task's checkpoint
	// reflects its final steps (the last Sync is the most likely to be dropped
	// by the non-blocking semaphore).
	trajStore.Flush()
	return result, err
}

// callerForConductor returns the LLMCaller used by the Conductor's main ReAct
// loop — the loop that carries the task message (and any image/content blocks).
//
// It returns deps.llm directly rather than rebuilding the caller stack from
// deps.trackingCaller. deps.llm (== o.llm) already carries the full stack
// assembled in the orchestrator builder:
//
//	NewDumpCaller(NewLoggingLLMCaller(trackingCaller, provider, logger),
//	              sessionDumpWriter, logger)
//
// The previous implementation rebuilt logging(trackingCaller) WITHOUT the
// dump wrapper, so the session-level dump writer was silently dropped: a
// failed LLM call in the main ReAct loop never produced a request+response
// dump record (even though subagent steps, which use callerForStep with their
// own per-step dump, were recorded fine). Returning deps.llm restores the
// session dump for the main loop.
//
// Reasoning effort is unaffected: it is applied to the executor via
// executor.SetReasoningEffort (see the Conductor in sp4rk), independent of the
// caller. Likewise the Conductor's optional context-tracker injection relies
// on a WithContextTracker capability that NEITHER loggingCaller nor dumpCaller
// implements, so wrapping the outermost layer in dumpCaller changes nothing
// there (the type assertion fails identically either way).
func callerForConductor(deps conductorDeps) agent.LLMCaller {
	return deps.llm
}

// adaptContextFactory converts a core ContextManagerFactory (which returns
// core.ContextManager and takes variadic PruningOverride) to the sp4rk
// ContextManagerFactory signature (which returns agent.ContextManager and
// takes variadic PruningOverride). The core ContextManager embeds
// agent.ContextManager, so the return type is compatible.
func adaptContextFactory(cf ContextManagerFactory) orchestration.ContextManagerFactory {
	if cf == nil {
		return nil
	}
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) agent.ContextManager {
		coreOverrides := make([]orchestration.PruningOverride, len(pruningOverrides))
		copy(coreOverrides, pruningOverrides)
		return cf(systemPrompt, modelMeta, compactionStrategy, coreOverrides...)
	}
}

// (o *Orchestrator) runConductor assembles a conductorDeps from the
// orchestrator's stored fields and delegates to RunConductor. Used by
// HandleMessage and Resume.
//
// conversationHistory is the prior dialogue to inject into the Conductor's
// context. For HandleMessage, pass o.conversationHistory so the agent sees
// previous exchanges. For Resume, pass nil — the Conductor continues the
// same task and the original request is already the task message.
//
// forceCompactionStrategy threads the one-shot resume-compaction request
// (RequestResumeCompaction) into the Conductor run: non-empty overrides the
// routing-derived compaction strategy and forces a start-of-run compaction
// (CompactOnStart) of the merged trajectory. Empty ("" — the HandleMessage
// default and an unarmed Resume) preserves the normal behavior.
func (o *Orchestrator) runConductor(ctx context.Context, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, resumeSteps []agent.Step, contentBlocks []llm.ContentBlock, nudge, forceCompactionStrategy string) (*orchestration.ExecutionResult, error) {
	deps := o.buildConductorDeps(conversationHistory, resumeSteps)
	deps.contentBlocks = contentBlocks
	deps.nudge = nudge
	deps.forceCompactionStrategy = forceCompactionStrategy
	return RunConductor(ctx, message, bb, availableTools, deps, plansDir)
}

// buildConductorDeps assembles the conductorDeps struct from the
// orchestrator's stored fields, so a goal loop turn can build deps, swap in
// counting wrappers (tool exec + LLM caller) and a goal-proposer override,
// and then call RunConductor directly — without re-reading every stored field.
// conversationHistory is the prior dialogue to inject into the Conductor's
// context; resumeSteps holds pre-existing ReAct steps for resume (nil/empty =
// fresh start). goalProposer, when non-nil, is injected into deps so the
// propose_goal tool can reach the desktop approval flow during derivation.
func (o *Orchestrator) buildConductorDeps(conversationHistory []llm.Message, resumeSteps []agent.Step) conductorDeps {
	return conductorDeps{
		contextFactory:      o.contextFactory,
		toolExec:            o.toolExec,
		toolRegistry:        o.toolRegistry,
		disabledTools:       o.disabledToolNames(),
		llm:                 o.llm,
		modelRegistry:       o.modelRegistry,
		model:               o.currentModel(),
		tokenCounter:        o.tokenCounter,
		emitter:             o.emitter,
		logger:              o.logger,
		trackingCaller:      o.trackingCaller,
		providerName:        o.providerName,
		stepDumpTracker:     o.stepDumpTracker,
		toolCache:           o.toolCache,
		perToolTrunc:        o.perToolTrunc,
		toolResultBudget:    o.toolResultBudget,
		circuitBreaker:      o.circuitBreaker,
		hitlHandler:         o.config.HITLHandler,
		reflector:           o.reflector,
		maxRedelegDepth:     o.config.MaxRedelegationDepth,
		maxDepCtxChars:      o.config.MaxDependencyContextChars,
		reasoningEffort:     o.currentReasoningEffort(),
		preWarningPct:       o.config.PreWarningPercent,
		conversationHistory: conversationHistory,
		taskStore:           o.taskStore,
		resumeSteps:         resumeSteps,
		goalProposer:        o.goalProposer,
		// agentResolver exposes the discovered Subagent Profiles to the
		// Conductor context so buildSubAgentTask can apply a requested
		// profile. Built from the agentManager; nil-safe when none configured
		// (delegate validation then rejects a non-empty `agent` field).
		agentResolver: func(name string) (*agents.Agent, bool) {
			if o.agentManager == nil {
				return nil, false
			}
			return o.agentManager.Get(name)
		},
		// pauseChecker wires the universal pause signal into every conductor
		// run (normal path + every goal-loop turn). It reads o.activePause
		// live; nil-safe when no request is in flight.
		pauseChecker: o.newPauseChecker(),
		// userMessageSource wires the live user-message queue into every
		// conductor run (normal path + every goal-loop turn). The executor
		// drains one message per step boundary; nil-safe when the queue is
		// empty (returns "").
		userMessageSource: func(context.Context) string { return o.DrainLiveUserMessages() },
		// verify-on-edit: suppressed in No Project (CHAT) mode — CHAT exposes
		// no file-edit tools, so verification there is never meaningful. The
		// runner itself is built by the builder from user config only.
		verifyOnEdit:               o.verifyOnEditForMode(),
		verifyOnEditMaxOutputChars: o.verifyOnEditMaxOutputChars,
	}
}

// inlineStepLifecycle manages the lifecycle of plan steps that the Conductor
// executes inline (without delegating to subagents). It decouples checklist
// updates from plan-step lifecycle: update_checklist emits only StepTodoUpdate
// (observational progress); PlanStepStart is inferred from the first checklist
// update for a step; PlanStepComplete is emitted by declare_step_complete or
// the finish fallback.
//
// Invariants:
//   - started holds step IDs that received PlanStepStart but not yet
//     PlanStepComplete.
//   - completed holds step IDs that have reached a terminal PlanStepComplete
//     (via completeStep or completeAll). It prevents double-completion and
//     re-Start from a late checklist update after completion.
type inlineStepLifecycle struct {
	emitter Emitter
	scoper  CurrentStepScopable // nil if emitter doesn't support dynamic scoping
	bb      orchestration.Blackboard
	// planState tracks whether a plan was declared in THIS Conductor run,
	// shared with the conductorPublisher and conductorLauncher. Used by
	// completeAll to avoid synthesizing terminal events for a restored plan
	// from a previous (completed) task on a plan-less continuation. Nil only
	// under direct (test) construction — see planDeclaredInRun fallback.
	planState *planRunState
	mu        sync.Mutex
	started   map[string]bool      // stepIDs that received PlanStepStart but not PlanStepComplete
	startedAt map[string]time.Time // wall-clock when PlanStepStart was emitted, for duration
	completed map[string]bool      // stepIDs that already reached PlanStepComplete
}

func newInlineStepLifecycle(emitter Emitter, bb orchestration.Blackboard) *inlineStepLifecycle {
	scoper, _ := emitter.(CurrentStepScopable)
	return &inlineStepLifecycle{
		emitter:   emitter,
		scoper:    scoper,
		bb:        bb,
		started:   make(map[string]bool),
		startedAt: make(map[string]time.Time),
		completed: make(map[string]bool),
	}
}

// onChecklistUpdate is the StepTodoUpdateFunc for the Conductor. It emits
// StepTodoUpdate (always — even for a standalone checklist with empty stepID)
// and infers PlanStepStart on the first checklist update for a given step.
// It never emits PlanStepComplete — that is the responsibility of
// declare_step_complete or the finish fallback.
//
// For step-associated checklists, PlanStepStart is emitted BEFORE
// StepTodoUpdate so the frontend opens the plan-step container before the
// checklist arrives, allowing the checklist to nest inside the step block.
func (l *inlineStepLifecycle) onChecklistUpdate(stepID string, items []agent.TodoItem) {
	if l == nil || l.emitter == nil {
		return
	}

	if stepID == "" {
		// Standalone checklist (Conductor without a declared plan) — no step lifecycle.
		l.emitter.StepTodoUpdate(stepID, items)
		return
	}

	l.mu.Lock()
	alreadyCompleted := l.completed[stepID]
	first := !l.started[stepID] && !alreadyCompleted
	if first {
		l.started[stepID] = true
		l.startedAt[stepID] = time.Now()
	}
	l.mu.Unlock()

	// Set the dynamic plan-step scope BEFORE emitting PlanStepStart so that
	// all subsequent executor events (tool_call, thought, assistant, etc.)
	// carry plan_step_id and nest under the step block in the frontend. This
	// mirrors the static scoping that subagent copies get via WithPlanStepID.
	// A step that was already completed is not re-started (defensive against
	// a late checklist update after declare_step_complete).
	if first {
		if l.scoper != nil {
			l.scoper.SetCurrentStepID(stepID)
		}
		// Emit PlanStepStart before StepTodoUpdate so the frontend's openSteps
		// map contains the step when the checklist update arrives, enabling
		// nesting inside the plan-step block.
		desc, summary := lookupStepDesc(l.bb, stepID)
		l.emitter.PlanStepStart(stepID, desc, summary)
	}

	l.emitter.StepTodoUpdate(stepID, items)
}

// completeStep is the StepCompleteFunc for declare_step_complete. It emits
// PlanStepComplete and moves the step to the completed set so that completeAll
// does not double-complete it.
//
// If the Conductor skipped update_checklist for this step (so PlanStepStart
// was never inferred), a PlanStepStart is synthesized first — but only for
// steps that are in the declared plan, so that the step transitions
// pending→running→completed in the plan panel instead of being stuck in
// pending. For plan-less/unknown IDs there is no plan panel entry to update,
// so the call is a no-op.
func (l *inlineStepLifecycle) completeStep(stepID string, success bool, errMsg string) {
	if l == nil || l.emitter == nil || stepID == "" {
		return
	}
	l.mu.Lock()
	wasStarted := l.started[stepID]
	start := l.startedAt[stepID]
	delete(l.started, stepID)
	delete(l.startedAt, stepID)
	l.completed[stepID] = true
	l.mu.Unlock()

	var duration time.Duration
	if wasStarted {
		duration = time.Since(start)
	}

	if !wasStarted {
		if !planStepExists(l.bb, stepID) {
			return
		}
		// Synthesized start: the Conductor completed the step without an
		// inferred PlanStepStart, so the real start time is unknown — duration
		// stays 0 (mirrors the pre-fix behavior for this edge case).
		desc, summary := lookupStepDesc(l.bb, stepID)
		l.emitter.PlanStepStart(stepID, desc, summary)
	}
	l.emitter.PlanStepComplete(stepID, success, duration, errMsg)
	// Clear the dynamic step scope so events after completion (delegation,
	// planning, the next step's update_checklist ToolCall) are not mis-tagged
	// with the completed step's plan_step_id.
	if l.scoper != nil {
		l.scoper.SetCurrentStepID("")
	}
}

// markCompleted records a plan step as completed WITHOUT emitting any event.
// Used by execute_plan after each step's SubAgentComplete has already emitted
// PlanStepComplete via the planStepEventTranslator. This prevents completeAll
// (the finish fallback) from double-completing steps that execute_plan already
// drove to a terminal state through the emitter adapter.
func (l *inlineStepLifecycle) markCompleted(stepID string) {
	if l == nil || stepID == "" {
		return
	}
	l.mu.Lock()
	l.completed[stepID] = true
	delete(l.started, stepID)
	delete(l.startedAt, stepID)
	l.mu.Unlock()
}

// isCompleted reports whether the step already reached a terminal state in this
// run's lifecycle. Used by the execute_plan resume path to decide whether a
// skipped (already-successful) step needs a synthesized terminal pair: on a
// same-run retry it is already recorded, on a re-declared continuation it is not.
func (l *inlineStepLifecycle) isCompleted(stepID string) bool {
	if l == nil || stepID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.completed[stepID]
}

// planDeclaredInRun reports whether a plan was declared in the CURRENT
// Conductor run. Mirrors conductorLauncher.HasDeclaredPlan: when planState is
// wired (production path via RunConductor), it reflects only plans declared
// via declare_plan; a restored plan from a previous (completed) task does NOT
// count. When planState is nil (direct construction in tests), it falls back
// to the raw blackboard plan so existing direct-construction tests keep
// working.
func (l *inlineStepLifecycle) planDeclaredInRun() bool {
	if l == nil {
		return false
	}
	if l.planState != nil {
		return l.planState.isDeclared()
	}
	return l.bb != nil && l.bb.GetPlan() != nil
}

// completeAll auto-completes any plan steps that have not reached a terminal
// state. Called as a finish fallback after the Conductor's executor.Run
// returns. errMsg is propagated to each auto-completed step's PlanStepComplete
// so the UI can show why the step failed (empty string on success).
//
// It sweeps every step in the declared plan that is not in the completed set:
//   - steps that were started (running) get a PlanStepComplete directly;
//   - steps that were never started (the Conductor forgot update_checklist or
//     the step was delegated and not marked inline) get a synthesized
//     PlanStepStart followed by PlanStepComplete.
//
// This guarantees no plan step is left stuck in "pending" or "running" once
// the Conductor finishes. For plan-less tasks (no plan declared in this run,
// including continuations whose blackboard carries a restored plan from a
// previous task) it only completes steps currently in the started set,
// matching prior behavior.
func (l *inlineStepLifecycle) completeAll(success bool, errMsg string) {
	if l == nil || l.emitter == nil {
		return
	}

	// Collect declared plan step IDs (if any). Read outside the lifecycle
	// lock — the blackboard is independently synchronized.
	//
	// This is gated on planDeclaredInRun() (not the raw blackboard plan) so
	// that a plan-less continuation — whose blackboard carries a restored
	// plan from a previous (completed) task — does NOT synthesize terminal
	// events for those restored steps. The fresh lifecycle has empty
	// started/completed sets, so without this gate every restored step would
	// fall into neverStarted and re-emit PlanStepComplete (PlanStepStart is
	// deduped by the emitter, but PlanStepComplete is not).
	var planStepIDs []string
	if l.planDeclaredInRun() {
		if plan := l.bb.GetPlan(); plan != nil {
			planStepIDs = make([]string, 0, len(plan.Steps))
			for _, s := range plan.Steps {
				planStepIDs = append(planStepIDs, s.ID)
			}
		}
	}

	l.mu.Lock()
	startedPending := make([]string, 0, len(l.started))
	startedSet := make(map[string]bool, len(l.started))
	startTimes := make(map[string]time.Time, len(l.started))
	for id := range l.started {
		startedPending = append(startedPending, id)
		startedSet[id] = true
		startTimes[id] = l.startedAt[id]
	}
	l.started = make(map[string]bool)
	l.startedAt = make(map[string]time.Time)

	// neverStarted = plan steps that are neither completed nor currently
	// running. These need a synthesized PlanStepStart before the complete.
	neverStarted := make([]string, 0)
	for _, id := range planStepIDs {
		if !l.completed[id] && !startedSet[id] {
			neverStarted = append(neverStarted, id)
		}
	}
	// Mark all plan steps as completed to prevent any future double-completion.
	for _, id := range planStepIDs {
		l.completed[id] = true
	}
	l.mu.Unlock()

	for _, id := range startedPending {
		l.emitter.PlanStepComplete(id, success, time.Since(startTimes[id]), errMsg)
	}
	for _, id := range neverStarted {
		desc, summary := lookupStepDesc(l.bb, id)
		l.emitter.PlanStepStart(id, desc, summary)
		l.emitter.PlanStepComplete(id, success, 0, errMsg)
	}
	// Clear the dynamic step scope after all steps are completed so any
	// post-finish events (e.g. Conductor's final assistant message) are not
	// mis-tagged with the last step's plan_step_id.
	if l.scoper != nil {
		l.scoper.SetCurrentStepID("")
	}
}

// lookupStepDesc returns the description and summary for a step ID from the
// blackboard's plan, or empty strings if the step is not found.
func lookupStepDesc(bb orchestration.Blackboard, stepID string) (desc, summary string) {
	if bb == nil {
		return
	}
	plan := bb.GetPlan()
	if plan == nil {
		return
	}
	for _, step := range plan.Steps {
		if step.ID == stepID {
			return step.Description, step.Summary
		}
	}
	return
}

// planStepExists reports whether stepID matches a step in the declared plan.
func planStepExists(bb orchestration.Blackboard, stepID string) bool {
	if bb == nil {
		return false
	}
	plan := bb.GetPlan()
	if plan == nil {
		return false
	}
	for _, step := range plan.Steps {
		if step.ID == stepID {
			return true
		}
	}
	return false
}

// subagentTodoCallback returns a StepTodoUpdateFunc for subagent execution.
// It only emits StepTodoUpdate — delegation progress is tracked via
// SubAgentLaunch/SubAgentComplete events from sp4rk, not plan-step events.
func subagentTodoCallback(emitter Emitter) agent.StepTodoUpdateFunc {
	if emitter == nil {
		return nil
	}
	return func(stepID string, items []agent.TodoItem) {
		emitter.StepTodoUpdate(stepID, items)
	}
}
