package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/safeio"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// trajectoryHolder is a thread-safe TrajectoryStore: the executor syncs its
// step history here each loop iteration; reflect reads it for
// scope="trajectory". See specs/domains/session-lifecycle.md.
type trajectoryHolder struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (h *trajectoryHolder) Sync(steps []agent.Step) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Length-monotonic: a shorter sync is ignored — a mis-wired executor can
	// never erase the parent's task checkpoint. See specs/domains/session-lifecycle.md.
	if len(steps) < len(h.steps) {
		return
	}
	h.steps = make([]agent.Step, len(steps))
	copy(h.steps, steps)
}

func (h *trajectoryHolder) Steps() []agent.Step {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.steps
}

// planRunState tracks whether the plan workflow is active in the CURRENT run
// (declared | continuable); the plan-workflow guards consult this, not the
// raw blackboard plan — specs/domains/orchestration/conductor.md.
type planRunState struct {
	mu          sync.Mutex
	declared    bool
	continuable bool
	// standaloneChecklist: a standalone checklist was emitted EARLIER in this
	// run (only while the plan is inactive) — the monotone flag (conductor.md).
	standaloneChecklist bool
}

// newPlanRunState creates the per-run plan state. continuable seeds the
// resumed-with-plan flag (see planRunState doc).
func newPlanRunState(continuable bool) *planRunState {
	return &planRunState{continuable: continuable}
}

// markDeclared records that declare_plan ran in this Conductor run.
func (s *planRunState) markDeclared() {
	s.mu.Lock()
	s.declared = true
	s.mu.Unlock()
}

// unmarkDeclared deactivates the plan workflow (declare_plan abandon); only
// declared is cleared — abandon cannot reach continuable (conductor.md).
func (s *planRunState) unmarkDeclared() {
	s.mu.Lock()
	s.declared = false
	s.mu.Unlock()
}

// isDeclared reports whether a plan was declared in this Conductor run.
func (s *planRunState) isDeclared() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.declared
}

// isContinuable reports whether this run resumed a paused task whose declared
// plan still has unreached steps (the flag is seeded once, at run start).
func (s *planRunState) isContinuable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.continuable
}

// isActive reports whether the plan workflow is active in this run (declared
// OR continuable) — what the plan-workflow guards actually mean to ask. See
// specs/domains/orchestration/conductor.md.
func (s *planRunState) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.declared || s.continuable
}

// markStandaloneChecklist records that a standalone (empty step_id) checklist
// was emitted in this run. See the standaloneChecklist field for the invariant
// this participates in.
func (s *planRunState) markStandaloneChecklist() {
	s.mu.Lock()
	s.standaloneChecklist = true
	s.mu.Unlock()
}

// hasStandaloneChecklist reports whether a standalone checklist was emitted
// earlier in this run — the ChecklistGuard's exception (conductor.md).
func (s *planRunState) hasStandaloneChecklist() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.standaloneChecklist
}

// newChecklistGuard builds the ChecklistGuardFunc: rejects a standalone
// checklist while the plan is active in this run, unless one was emitted
// earlier (planRunState.standaloneChecklist; conductor.md "Checklist Guard").
func newChecklistGuard(planState *planRunState) agent.ChecklistGuardFunc {
	return func(stepID string) string {
		if stepID != "" || planState == nil {
			return ""
		}
		if planState.isActive() && !planState.hasStandaloneChecklist() {
			return "a plan has been declared; a standalone checklist (without step_id) is only valid for plan-less tasks — pass the step_id of the plan step you are executing, and do not list plan steps as checklist items (a checklist tracks sub-tasks within a single step)"
		}
		return ""
	}
}

// compositeTrajectoryStore implements agent.TrajectoryStore: in-memory copy
// for reflect + best-effort non-blocking full-snapshot DB upsert per Sync.
// See specs/domains/session-lifecycle.md ("Task trajectory persistence").
type compositeTrajectoryStore struct {
	memory *trajectoryHolder
	taskID string
	store  TaskPersistence
	logger *slog.Logger

	// mu guards the async-writer lifecycle (wg Add/Wait transitions, upsert
	// interleaving): async subagents inherit this store, so Sync and Flush run
	// on different goroutines.
	mu sync.Mutex

	// sem caps concurrent DB writes at one (send acquires, receive releases).
	sem chan struct{}

	// wg tracks the in-flight writer so Flush can drain it before the final
	// synchronous write.
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

// wireDelegationSpecSink records every unit at register time (spec at
// register) in the unit ledger and, with a task store, the legacy
// DelegationSpec; plan-step registries pass a nil store (delegation.md).
func wireDelegationSpecSink(registry *tools.DelegationRegistry, ledger units.Ledger, parentID, taskID string, store TaskPersistence, logger *slog.Logger) {
	registry.SetSpecSink(parentID, func(spec tools.DelegationSpec) {
		// Unit ledger — spec at register.
		if ledger != nil {
			var raw json.RawMessage
			if data, err := json.Marshal(spec); err == nil {
				raw = data
			}
			if err := ledger.Begin(units.UnitRecord{
				ID:       spec.Task.ID,
				Kind:     unitKindForDelegation(spec.Kind),
				ParentID: spec.ParentID,
				Depth:    spec.Depth,
				Spec:     raw,
			}); err != nil && logger != nil {
				logger.Debug("unit ledger begin failed (unit stays in-memory only)",
					"task_id", taskID, "unit_id", spec.Task.ID, "error", err)
			}
		}
		// Legacy DelegationSpec — the record the Resume wave reads.
		if store == nil {
			return
		}
		if err := store.PersistDelegationSpec(taskID, spec); err != nil {
			if logger != nil {
				logger.Warn("persist delegation spec failed (delegation remains in-memory; it cannot be auto-resumed after this run)",
					"task_id", taskID, "delegation_id", spec.Task.ID, "error", err)
			}
		}
	})
}

// unitKindForDelegation maps a delegation-spec kind to its unit-ledger kind.
// DelegationKindSubagent (and the empty zero value) map to UnitKindSubagent.
func unitKindForDelegation(kind tools.DelegationKind) units.UnitKind {
	switch kind {
	case tools.DelegationKindPlanStep:
		return units.UnitKindPlanStep
	default:
		return units.UnitKindSubagent
	}
}

// Sync updates the holder synchronously and kicks off the best-effort DB
// persist (session-lifecycle.md); the shallow copy relies on the executor's
// append-only step invariant — deep-copy if that ever changes.
func (c *compositeTrajectoryStore) Sync(steps []agent.Step) {
	c.memory.Sync(steps)

	if c.store == nil || c.taskID == "" {
		return
	}

	// Serialize against a concurrent Flush's wg.Wait (WaitGroup reuse rule).
	c.mu.Lock()

	// One outstanding write; skip when busy — a later Sync or the final Flush
	// persists a fresher snapshot.
	select {
	case c.sem <- struct{}{}:
	default:
		c.mu.Unlock()
		return
	}

	// Copy the guarded holder trajectory for the async writer (never-shrink
	// invariant; Steps() returns the live slice).
	effective := c.memory.Steps()
	snapshot := make([]agent.Step, len(effective))
	copy(snapshot, effective)

	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() { <-c.sem }()
		if err := c.store.SaveTrajectory(c.taskID, snapshot); err != nil && c.logger != nil {
			c.logger.Debug("trajectory persistence failed (best-effort)", "taskID", c.taskID, "error", err)
		}
	}()
}

// Flush drains in-flight writes and persists the trajectory synchronously —
// once, after the ReAct loop stops (the final checkpoint).
func (c *compositeTrajectoryStore) Flush() {
	if c.store == nil || c.taskID == "" {
		return
	}

	// Serialize against a concurrent Flush (WaitGroup reuse rule, upsert order).
	c.mu.Lock()
	defer c.mu.Unlock()

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
	// disabledTools: mode-disabled tool names (CHAT/No-Project) — excluded from
	// subagent toolsets and the read-only base; nil = nothing disabled.
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
	// maxParallelSubagents caps concurrent subagents via the shared launcher
	// limiter; <= 0 = unlimited (delegation.md).
	maxParallelSubagents int
	reasoningEffort      string
	preWarningPct        int

	// toolCallTimeout bounds a SINGLE tool call on the main and every subagent
	// executor; 0 disables. Mirrors Orchestrator config.ToolCallTimeout.
	toolCallTimeout time.Duration

	// toolCallTimeoutExempt mirrors OrchestratorConfig.ToolCallTimeoutExemptTools:
	// nil keeps sp4rk's default exempt set, non-nil replaces it wholesale (main
	// executor + every subagent via configureExecutor).
	toolCallTimeoutExempt []string

	// lifecycle is the inline plan-step lifecycle tracker, created in
	// RunConductor and threaded in (no post-construction mutation); nil in
	// direct test construction — Execute nil-checks every access.
	lifecycle *inlineStepLifecycle

	// conversationHistory: prior dialogue for follow-up context; nil on Resume
	// (conductor.md, "Conversation History Injection").
	conversationHistory []llm.Message

	// contentBlocks: structured blocks (text+images) for the task message;
	// non-nil switches the engine to SetTaskWithBlocks. nil = text-only.
	contentBlocks []llm.ContentBlock

	// taskStore is the optional DB-backed persistence store: with it (and a
	// task ID on the blackboard) the trajectory persists best-effort per Sync;
	// nil keeps it in-memory (tests / non-persistent sessions).
	taskStore TaskPersistence

	// unitSink: ledger-bound sink for an ISOLATED run (goal verifier) — the
	// registry sink wires from it instead of the blackboard (delegation.md);
	// nil = mainline blackboard/store path.
	unitSink *unitSink

	// resumeSteps: persisted steps seeded into CM (StepSeedable) + Executor
	// (WithResumeSteps) on resume; nil = fresh start (conductor.md).
	resumeSteps []agent.Step

	// resumedWithPlan seeds continuable: resume of a task whose approved plan
	// has unreached steps — no re-declare (conductor.md).
	resumedWithPlan bool

	// forceCompactionStrategy: resume compaction override — replaces the
	// routing strategy for the whole run + forces CompactOnStart (compaction.md).
	forceCompactionStrategy string

	// systemPromptOverride: specialized-run prompt factory (goal derivation),
	// built on the shared project-context prefix (goal-mode.md); nil = standard.
	systemPromptOverride orchestration.SystemPromptFactory

	// verifyOnEdit, when non-nil, arms the mechanical edit-verification hook on
	// the main and every subagent executor (suppressed for CHAT mode and
	// specialized passes). See specs/domains/verify-on-edit.md.
	verifyOnEdit agent.EditVerifyRunner

	// verifyOnEditMaxOutputChars caps the injected verification output;
	// 0 falls back to agent.DefaultVerifyOnEditCap.
	verifyOnEditMaxOutputChars int

	// goalProposer: desktop hook deriveGoal uses to submit {condition, verify}
	// and block for approval; nil = propose_goal no-op (goal-mode.md).
	goalProposer tools.GoalProposer

	// agentResolver resolves a Subagent Profile name; nil = profiles
	// unavailable, non-empty `agent` rejected by delegate validation.
	agentResolver tools.AgentResolver

	// skillResolver resolves a profile's required skills, fail-closed
	// (delegation.md, "Profile Required Skills").
	skillResolver func(name string) (*skills.Skill, bool)

	// pauseChecker: the universal cooperative-pause signal checked at every
	// step boundary of every run and subagent executor; PauseSession flips it.
	// Nil disables pausing (goal-mode.md, delegation.md).
	pauseChecker func(context.Context) bool

	// userMessageSource: live user-message queue polled per step boundary;
	// delivered as a {role:user} interjection. Main runs only — subagents
	// never receive it; nil disables (session-lifecycle.md).
	userMessageSource func(context.Context) string

	// nudge: one-shot resume nudge delivered as the final user message of the
	// next LLM call; empty on fresh tasks.
	nudge string

	// stopTools: ordinary tools that terminate the run on success — the goal
	// loop sets declare_goal_status so turns end at the verdict (turn budget).
	// Empty = no-op (goal-mode.md).
	stopTools []string
}

// conductorLauncher implements tools.DelegationLauncher by building a fresh
// Executor + ContextManager per task and dispatching via RunSubAgent.
type conductorLauncher struct {
	deps conductorDeps
	bb   orchestration.Blackboard
	// asyncBaseCtx: the RUN-scoped context async delegations are based on —
	// the per-tool watchdog would kill a just-launched subagent otherwise.
	// Set once in RunConductor; nil in tests (delegation.md).
	asyncBaseCtx context.Context
	// runPlanStepWave executes one wave of ready plan steps; default
	// defaultPlanStepWave, overridden by tests for the DAG scheduler.
	runPlanStepWave func(ctx context.Context, ready []orchestration.PlanStep, registry *tools.DelegationRegistry) []planStepOutcome
	// planState is shared with the publisher so the delegate guard reflects
	// only plans declared in THIS run; nil only in tests.
	planState *planRunState
	// ledger is the durable unit ledger — the SINGLE writer of unit lifecycle
	// (delegation.md). Set at CONSTRUCTION (RunConductor); the resume-wave
	// launcher derives one lazily via unitLedger (memoized — register and
	// settle must land in the SAME ledger). Nil degrades to no-op writes.
	ledger units.Ledger
	// ledgerOnce memoizes the lazily derived ledger so the derivation is
	// executed exactly once and its result is safely published.
	ledgerOnce sync.Once
	// derivedLedger caches the ledger lazily derived from the blackboard.
	derivedLedger units.Ledger

	// slots is the launcher-wide concurrency limiter shared by every dispatch
	// path (delegation.md); lazily built from the cap so direct (test)
	// construction honors it too, published once.
	slotsOnce sync.Once
	slots     chan struct{}
}

// unitLedger returns the launcher's ledger: the wired one (RunConductor), else
// a blackboard-derived instance MEMOIZED so a unit's Begin and its settle land
// in the same ledger (shared overlay, no per-transition reload). A
// non-persistable blackboard yields nil — every write no-ops.
func (l *conductorLauncher) unitLedger() units.Ledger {
	if l.ledger != nil {
		return l.ledger
	}
	l.ledgerOnce.Do(func() {
		if pbb, ok := l.bb.(PersistableBlackboard); ok {
			l.derivedLedger = NewBlackboardLedger(pbb)
		}
	})
	return l.derivedLedger
}

// markUnitRunning records a unit's "running at start" transition in the ledger.
func (l *conductorLauncher) markUnitRunning(id string) {
	ledger := l.unitLedger()
	if ledger == nil {
		return
	}
	_ = ledger.Settle(id, units.UnitStatusRunning)
}

// settleUnit records a unit's terminal status in the ledger without touching
// the blackboard. Used for outcomes that never produced a step result: build
// failures, context cancellation, and unsatisfiable dependencies.
func (l *conductorLauncher) settleUnit(id string, execErr error) {
	l.recordUnitLedger(id, execErr, nil)
}

// recordUnitLedger durably records a settled unit — resume checkpoint (the
// ReAct steps) + terminal status — through the launcher's ledger and, for an
// ISOLATED run, the ledger-bound unit sink; both writes best-effort, and ids
// the isolated sink never registered are ignored. See
// specs/domains/orchestration/delegation.md.
func (l *conductorLauncher) recordUnitLedger(id string, execErr error, steps []agent.Step) {
	if ledger := l.unitLedger(); ledger != nil {
		if len(steps) > 0 {
			_ = ledger.Checkpoint(id, steps)
		}
		_ = ledger.Settle(id, unitStatusFor(execErr))
	}
	if l.deps.unitSink != nil {
		l.deps.unitSink.record(id, execErr, steps)
	}
}

// HasDeclaredPlan implements tools.PlanChecker: plan workflow active in the
// CURRENT run (planRunState, not the blackboard plan; nil falls back for
// direct test construction) — conductor.md.
func (l *conductorLauncher) HasDeclaredPlan() bool {
	if l.planState != nil {
		return l.planState.isActive()
	}
	return l.bb != nil && l.bb.GetPlan() != nil
}

// PlanContinuation reports whether THIS run resumed a task whose approved plan
// still has unreached steps (declare_plan reads it to return the soft
// "continue with execute_plan" hint instead of re-publishing the roadmap).
func (l *conductorLauncher) PlanContinuation() bool {
	return l.planState != nil && l.planState.isContinuable()
}

// Execute runs the declared plan's steps in DAG order with parallelism
// (tools.PlanStepExecutor): resumable (successful steps skipped unless
// forced via stepIDs), continuable resumes run without a re-declare, restored
// plans refused. See specs/domains/orchestration/conductor.md.
func (l *conductorLauncher) Execute(ctx context.Context, stepIDs []string) ([]tools.PlanStepResult, error) {
	plan := l.bb.GetPlan()
	if plan == nil || len(plan.Steps) == 0 {
		return nil, errors.New("no plan declared — call declare_plan first")
	}

	// Restored-plan guard: the plan must be active in THIS run (declared or
	// continuable); restored/abandoned plans are refused. Inert when nil.
	if l.planState != nil && !l.planState.isActive() {
		return nil, errors.New("execute_plan: the plan on the blackboard was not declared in this run and is not a continuable resume — it is either a plan restored from a previous (completed) task or one abandoned at the approval prompt — call declare_plan to publish a new plan, or use delegate for plan-less work")
	}

	// Well-formedness preflight for plans that bypassed declare_plan validation
	// (restored/legacy) — fail fast; the tool wrapper adds the error prefix.
	if err := planStepsWellFormed(plan.Steps); err != nil {
		return nil, err
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

	// Local registry for plan-step dependency resolution — separate from the
	// Conductor's main delegation registry: plan steps are not delegations.
	localReg := tools.NewDelegationRegistry()
	// Same spec sink as delegates (spec at register); nil store — plan steps
	// are rebuilt from the blackboard plan.
	wireDelegationSpecSink(localReg, l.unitLedger(), "", "", nil, l.deps.logger)

	// indexByID preserves declaration order for deterministic result ordering.
	indexByID := make(map[string]int, len(plan.Steps))
	pending := make(map[string]orchestration.PlanStep, len(plan.Steps))
	results := make([]tools.PlanStepResult, 0, len(plan.Steps))

	for i, step := range plan.Steps {
		indexByID[step.ID] = i
		// RegisterTaskKind: identical to Register, but fires the registration
		// sink (unit ledger, spec at register).
		if err := localReg.RegisterTaskKind(tools.DelegationKindPlanStep, tools.DelegationTask{
			ID:        step.ID,
			Summary:   step.Summary,
			Task:      step.Description,
			DependsOn: step.DependsOn,
			Mode:      "blocking",
			Agent:     step.Agent,
		}); err != nil {
			return nil, fmt.Errorf("register plan step %q: %w", step.ID, err)
		}

		// Skip an already-successful step unless forced; replay its result into
		// the registry so dependents see the dependency as satisfied.
		if !forced[step.ID] {
			if sr, ok := l.successfulStepResult(step.ID); ok {
				localReg.Complete(step.ID, sr.FullOutput, nil, sr.Steps)
				l.settleUnit(step.ID, nil)
				results = append(results, tools.PlanStepResult{
					StepID: step.ID, Summary: step.Summary,
					Status: "completed", Output: sr.FullOutput,
				})
				// Terminal-state settlement for a skipped step (issue #99):
				// re-declared → re-emit success; continuation → settle
				// silently (history already shows it).
				if l.deps.lifecycle != nil && !l.deps.lifecycle.isCompleted(step.ID) {
					if l.deps.lifecycle.planDeclaredThisRun() {
						l.emitSkippedStepCompleted(step.ID)
					}
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
				l.settleUnit(id, ctx.Err())
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
			// Dependencies unsatisfiable — nothing launched, so emit the
			// terminal pairs directly (they would otherwise linger "pending").
			for id, step := range pending {
				err := fmt.Errorf("step %q: dependencies could not be satisfied (upstream failure)", id)
				l.settleUnit(id, err)
				results = append(results, tools.PlanStepResult{
					StepID: id, Summary: step.Summary,
					Status: "failed", Error: err,
				})
				// Never launched — emit the terminal pair directly.
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
				l.persistUnitOutcome(oc.stepID, oc.output, oc.err, oc.steps)
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
			l.persistUnitOutcome(oc.stepID, oc.output, oc.err, oc.steps)

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

		// A cooperative pause stopped this wave; remaining steps are
		// (re)dispatched on resume via a fresh execute_plan call.
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

// CompletedStep implements tools.DelegationLauncher: the settled outcome for
// an id with a successful StepResult (the delegate duplicate guard replays it).
func (l *conductorLauncher) CompletedStep(stepID string) (tools.DelegationCompletedStep, bool) {
	sr, ok := l.successfulStepResult(stepID)
	if !ok {
		return tools.DelegationCompletedStep{}, false
	}
	return tools.DelegationCompletedStep{Output: sr.FullOutput, Steps: sr.Steps}, true
}

// malformedPlanFix is the actionable suffix shared by every
// planStepsWellFormed error: the only correct recovery from a malformed plan
// is to publish a fresh, valid one.
const malformedPlanFix = "re-declare the plan with declare_plan (every task needs a unique non-empty id, summary, description)"

// planStepsWellFormed verifies non-empty unique step IDs (Execute keys its
// registry/deps/results off them). declare_plan validates too; this guards
// restored/legacy plans. 1-based numbers in errors; the wrapper adds the prefix.
func planStepsWellFormed(steps []orchestration.PlanStep) error {
	seen := make(map[string]int, len(steps))
	for i, step := range steps {
		if step.ID == "" {
			return fmt.Errorf("malformed plan: step %d has an empty id — %s", i+1, malformedPlanFix)
		}
		if first, dup := seen[step.ID]; dup {
			return fmt.Errorf("malformed plan: steps %d and %d share id %q — %s", first+1, i+1, step.ID, malformedPlanFix)
		}
		seen[step.ID] = i
	}
	return nil
}

// forcedRerunSet: explicit execute_plan step IDs force the targets plus their
// transitive dependents to re-run (their outputs are stale).
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

// subagentSlots returns the launcher-wide subagent concurrency limiter,
// building it once from deps.maxParallelSubagents. A cap <= 0 means
// "unlimited" (returns nil, so no slot accounting happens).
func (l *conductorLauncher) subagentSlots() chan struct{} {
	l.slotsOnce.Do(func() {
		if n := l.deps.maxParallelSubagents; n > 0 {
			l.slots = make(chan struct{}, n)
		}
	})
	return l.slots
}

// acquireSubagentSlot blocks until a running slot is free (no-op when the
// limiter is unlimited). Every acquire must be paired with exactly one
// releaseSubagentSlot once the subagent settles.
func (l *conductorLauncher) acquireSubagentSlot() {
	if slots := l.subagentSlots(); slots != nil {
		slots <- struct{}{}
	}
}

// releaseSubagentSlot frees a running slot (no-op when unlimited).
func (l *conductorLauncher) releaseSubagentSlot() {
	if slots := l.subagentSlots(); slots != nil {
		<-slots
	}
}

// runSubAgentsParallel is the SINGLE fan-out for plan-wave and blocking
// dispatch: every worker draws from the launcher-wide limiter (async launches
// use the same one), so max_parallel_subagents caps all concurrent paths;
// input order preserved. No slot-holder can itself delegate → no nested
// deadlock. See specs/domains/orchestration/delegation.md.
func (l *conductorLauncher) runSubAgentsParallel(ctx context.Context, tasks []agent.SubAgentTask) []agent.SubAgentResult {
	if len(tasks) == 0 {
		return nil
	}
	slots := l.subagentSlots()
	if slots == nil {
		return agent.RunSubAgentsParallel(ctx, tasks)
	}

	results := make([]agent.SubAgentResult, len(tasks))
	var wg sync.WaitGroup
	for i := range tasks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots <- struct{}{}        // acquire a running slot
			defer func() { <-slots }() // release it once this subagent settles
			t := tasks[i]
			results[i] = <-agent.RunSubAgent(ctx, t.StepID, t.Executor, t.CM, t.TaskTools, t.TaskDesc, t.Emitter, t.TodoUpdateFunc)
		}(i)
	}
	wg.Wait()
	return results
}

// defaultPlanStepWave: an isolated subagent executor per ready step, run
// concurrently; build failures get directly-emitted terminal pairs.
func (l *conductorLauncher) defaultPlanStepWave(ctx context.Context, ready []orchestration.PlanStep, registry *tools.DelegationRegistry) []planStepOutcome {
	// Plan-step subagents run under the same narrowed context as every other
	// launch path (see subagentCtx / delegation.md "Subagent Context
	// Narrowing"): plan steps are plain blocking subagents.
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
			// Build failed — no translator events ran; emit the terminal pair directly.
			l.emitNeverStartedStep(step.ID, err.Error())
			l.settleUnit(step.ID, err)
			outcomes = append(outcomes, planStepOutcome{stepID: step.ID, err: err})
			continue
		}
		registry.Start(step.ID, nil)
		l.markUnitRunning(step.ID)
		subTasks = append(subTasks, st)
	}

	for _, sr := range l.runSubAgentsParallel(ctx, subTasks) {
		if isPaused(sr.Error) {
			l.persistUnitOutcome(sr.StepID, sr.Output, sr.Error, sr.Steps)
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
// pair for a step that never launched; PlanStepStart dedups on the root
// emitter, so a stray start is harmless.
func (l *conductorLauncher) emitNeverStartedStep(stepID, errMsg string) {
	if l.deps.emitter == nil {
		return
	}
	desc, summary := lookupStepDesc(l.bb, stepID)
	l.deps.emitter.PlanStepStart(stepID, desc, summary)
	l.deps.emitter.PlanStepComplete(stepID, false, 0, errMsg)
}

// emitSkippedStepCompleted mirrors emitNeverStartedStep with success=true, for
// a step the resume path skipped because it already succeeded on the
// blackboard.
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

	// The duplicate guard (no silent re-run of a successful id) lives in the
	// delegate tool; Launch's callers (guarded tool, resume wave) cannot carry
	// one — see delegate.go.
	for _, t := range tasks {
		pending[t.ID] = t
	}

	for len(pending) > 0 {
		if ctx.Err() != nil {
			for id := range pending {
				registry.Complete(id, "", ctx.Err(), nil)
				l.settleUnit(id, ctx.Err())
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
				l.settleUnit(id, err)
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
	// Redelegating tasks need per-task contexts (child registry + launcher),
	// so they launch individually outside RunSubAgentsParallel.
	var regularTasks []tools.DelegationTask
	var redelegTasks []tools.DelegationTask
	for _, t := range tasks {
		// A profile's allow-redelegate only ever upgrades; resolved here
		// because it selects the dispatch path (the other profile fields are
		// applied in buildSubAgentTask).
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

// resolveAgentAllowRedelegate applies a profile's allow-redelegate override —
// upgrade-only. A not-found profile (benign mid-run removal) keeps the task
// flag. See specs/decisions/021-subagents.md.
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

// subagentCtx strips Conductor-only context values — required for any
// non-redelegating subagent (three cleared classes + trajectory detach).
// See specs/domains/orchestration/delegation.md ("Subagent Context Narrowing").
func subagentCtx(ctx context.Context) context.Context {
	ctx = tools.WithDelegationRegistry(ctx, nil)
	ctx = tools.WithDelegationLauncher(ctx, nil)
	ctx = tools.WithPlanPublisher(ctx, nil)
	ctx = tools.WithReflectionRunner(ctx, nil)
	ctx = tools.WithPlanChecker(ctx, nil)
	ctx = orchestration.WithDelegationRegistry(ctx, nil)
	// Goal-loop sinks: defense in depth behind resolveTaskTools (the verifier
	// itself does not run through subagentCtx — its re-derivation subagent
	// SHOULD land here with sinks cleared).
	ctx = tools.WithGoalStatusSink(ctx, nil)
	ctx = tools.WithVerificationSink(ctx, nil)
	ctx = WithGoalState(ctx, nil)
	ctx = WithAvailableAgents(ctx, nil)
	ctx = WithUserAgents(ctx, nil)
	// Detach the trajectory store: a foreign executor must never sync into the
	// parent task checkpoint.
	ctx = agent.WithTrajectoryStore(ctx, &trajectoryHolder{})
	return ctx
}

// isPaused reports the pause sentinel including its persistence-round-trip
// string form (ErrPaused's message is unique and stable).
func isPaused(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, agent.ErrPaused) || err.Error() == agent.ErrPaused.Error()
}

// pausedRun reports a cooperative pause (sentinel or ExecutionStatusPaused) —
// a recoverable checkpoint, so the finish fallback must not sweep plan steps
// into terminal states.
func pausedRun(err error, result *orchestration.ExecutionResult) bool {
	if isPaused(err) {
		return true
	}
	return result != nil && result.Status == orchestration.ExecutionStatusPaused
}

// pausedCheckpoint: the blackboard's pause checkpoint for a step — sentinel
// error + partial trajectory seed (delegation.md).
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

// persistUnitOutcome mirrors a settled outcome to the blackboard (behind
// read_step_output and in-run resume) and records checkpoint + status in the
// ledger — what makes a pause checkpoint survive restart (delegation.md).
func (l *conductorLauncher) persistUnitOutcome(id, output string, execErr error, steps []agent.Step) {
	l.bb.SetStepResult(id, output, execErr, steps)
	l.recordUnitLedger(id, execErr, steps)
}

func (l *conductorLauncher) runRegularBlocking(ctx context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	subCtx := subagentCtx(ctx)
	subTasks := make([]agent.SubAgentTask, 0, len(tasks))
	buildFailures := make([]tools.DelegationResult, 0)
	for _, t := range tasks {
		st, err := l.buildSubAgentTask(subCtx, t, registry, l.scopeEvents(t.ID))
		if err != nil {
			registry.Complete(t.ID, "", err, nil)
			l.settleUnit(t.ID, err)
			buildFailures = append(buildFailures, tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err})
			continue
		}
		registry.Start(t.ID, nil)
		l.markUnitRunning(t.ID)
		subTasks = append(subTasks, st)
	}

	if len(subTasks) == 0 {
		return buildFailures
	}

	subResults := l.runSubAgentsParallel(subCtx, subTasks)
	out := make([]tools.DelegationResult, 0, len(subResults)+len(buildFailures))
	out = append(out, buildFailures...)
	for _, sr := range subResults {
		if isPaused(sr.Error) {
			l.persistUnitOutcome(sr.StepID, sr.Output, sr.Error, sr.Steps)
			registry.CompletePaused(sr.StepID, sr.Output, sr.Steps)
			out = append(out, tools.DelegationResult{ID: sr.StepID, Status: tools.DelegationStatusPaused, Output: sr.Output, Error: sr.Error})
			continue
		}
		var execErr error
		if sr.Error != nil {
			execErr = sr.Error
		}
		registry.Complete(sr.StepID, sr.Output, execErr, sr.Steps)
		l.persistUnitOutcome(sr.StepID, sr.Output, execErr, sr.Steps)
		status := tools.DelegationStatusCompleted
		if execErr != nil {
			status = tools.DelegationStatusFailed
		}
		out = append(out, tools.DelegationResult{ID: sr.StepID, Status: status, Output: sr.Output, Error: execErr})
	}
	return out
}

// redelegTaskCtx: per-task context for a RE-delegating subagent — keeps the
// delegation machinery, explicitly re-clears roster/goal/trajectory (see
// delegation.md "Subagent Context Narrowing").
func (l *conductorLauncher) redelegTaskCtx(ctx context.Context, childReg *tools.DelegationRegistry) context.Context {
	taskCtx := tools.WithDelegationRegistry(ctx, childReg)
	taskCtx = tools.WithDelegationLauncher(taskCtx, l)
	// Also inject into the sp4rk-level context key so the sp4rk Conductor's
	// finish guard (executor.SetFinishGuard) can find the child registry and
	// reject finish while sub-delegations are still pending.
	taskCtx = orchestration.WithDelegationRegistry(taskCtx, childReg)
	// Clear the Conductor-only subagent roster so the redelegating subagent
	// does not inherit the parent's "Available Subagents"/"Requested Subagents"
	// prompt sections (ADR-021 §4).
	taskCtx = WithAvailableAgents(taskCtx, nil)
	taskCtx = WithUserAgents(taskCtx, nil)
	// Clear the goal-loop state (class 3 of subagentCtx): the goal-turn
	// completion directive names declare_goal_status, which this subagent's
	// toolset does not contain.
	taskCtx = WithGoalState(taskCtx, nil)
	// Clear the goal-loop sinks: a redelegating subagent holds
	// delegate/cancel_delegation but must still never write goal-status or
	// verification verdicts (resolveTaskTools strips the goal tools from its
	// toolset; this is the second layer).
	taskCtx = tools.WithGoalStatusSink(taskCtx, nil)
	taskCtx = tools.WithVerificationSink(taskCtx, nil)
	// Detach the trajectory store (see subagentCtx): without this override the
	// redelegating subagent would sync its own step list into the delegating
	// executor's holder and could overwrite the parent task checkpoint.
	taskCtx = agent.WithTrajectoryStore(taskCtx, &trajectoryHolder{})
	return taskCtx
}

func (l *conductorLauncher) runRedelegBlocking(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	if registry.Depth() >= l.deps.maxRedelegDepth {
		err := fmt.Errorf("delegation %q: allow_redelegate requested at depth %d but cap is %d", t.ID, registry.Depth(), l.deps.maxRedelegDepth)
		registry.Complete(t.ID, "", err, nil)
		l.settleUnit(t.ID, err)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Create a child registry one level deeper and inject it + the launcher
	// into a per-task context so the subagent can use delegate/cancel_delegation.
	childReg := tools.NewDelegationRegistryWithDepth(registry.Depth() + 1)
	// Persist sub-delegation specs parented by this step's ID (nested resume
	// ordering); unitSink wins for isolated runs, else blackboard task/store.
	if l.deps.unitSink != nil {
		l.deps.unitSink.wire(childReg, t.ID)
	} else if pbb, ok := l.bb.(PersistableBlackboard); ok {
		if tid := pbb.TaskID(); tid != "" && l.deps.taskStore != nil {
			wireDelegationSpecSink(childReg, l.unitLedger(), t.ID, tid, l.deps.taskStore, l.deps.logger)
		}
	}
	taskCtx := l.redelegTaskCtx(ctx, childReg)

	// Build the subagent task. The finish guard in the sp4rk Conductor
	// (SetFinishGuard) rejects finish with a nudge while pending async
	// sub-delegations exist, since the child registry is in the context.
	st, err := l.buildSubAgentTask(taskCtx, t, registry, l.scopeEvents(t.ID))
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		l.settleUnit(t.ID, err)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	// Re-add delegate/cancel_delegation with their REAL schema (resolveTaskTools
	// strips them on every path) — registry lookup, not bare descriptors.
	redelegTools := make([]sdktools.ToolDescriptor, 0, len(st.TaskTools)+2)
	redelegTools = append(redelegTools, st.TaskTools...)
	for _, name := range []string{"delegate", "cancel_delegation"} {
		if d, ok := l.toolDescriptorByName(name); ok {
			redelegTools = append(redelegTools, d)
		}
	}

	registry.Start(t.ID, nil)
	l.markUnitRunning(t.ID)
	ch := agent.RunSubAgent(taskCtx, t.ID, st.Executor, st.CM, redelegTools, st.TaskDesc, st.Emitter, st.TodoUpdateFunc)
	sr := <-ch

	if isPaused(sr.Error) {
		l.persistUnitOutcome(t.ID, sr.Output, sr.Error, sr.Steps)
		registry.CompletePaused(t.ID, sr.Output, sr.Steps)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusPaused, Output: sr.Output, Error: sr.Error}
	}

	var execErr error
	if sr.Error != nil {
		execErr = sr.Error
	}
	registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
	l.persistUnitOutcome(t.ID, sr.Output, execErr, sr.Steps)
	status := tools.DelegationStatusCompleted
	if execErr != nil {
		status = tools.DelegationStatusFailed
	}
	return tools.DelegationResult{ID: t.ID, Status: status, Output: sr.Output, Error: execErr}
}

func (l *conductorLauncher) launchAsync(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry) tools.DelegationResult {
	// Base the async delegation on the RUN context (asyncBaseCtx) — the per-tool
	// watchdog would otherwise cancel the just-launched subagent (delegation.md).
	if l.asyncBaseCtx != nil {
		ctx = l.asyncBaseCtx
	}
	// Async delegations always take the non-redelegating path (runWave
	// dispatches them here regardless of AllowRedelegate).
	ctx = subagentCtx(ctx)
	subTask, err := l.buildSubAgentTask(ctx, t, registry, l.scopeEvents(t.ID))
	if err != nil {
		registry.Complete(t.ID, "", err, nil)
		l.settleUnit(t.ID, err)
		return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusFailed, Error: err}
	}

	asyncCtx, cancel := context.WithCancel(ctx)
	registry.Start(t.ID, cancel)
	l.markUnitRunning(t.ID)

	go func() {
		defer cancel()
		// Bound the async fan-out by the launcher-wide limiter, exactly as the
		// blocking/plan-wave fan-out does (delegation.md).
		l.acquireSubagentSlot()
		defer l.releaseSubagentSlot()
		ch := agent.RunSubAgent(asyncCtx, t.ID, subTask.Executor, subTask.CM, subTask.TaskTools, subTask.TaskDesc, subTask.Emitter, subTask.TodoUpdateFunc)
		select {
		case sr := <-ch:
			if isPaused(sr.Error) {
				l.persistUnitOutcome(t.ID, sr.Output, sr.Error, sr.Steps)
				registry.CompletePaused(t.ID, sr.Output, sr.Steps)
				return
			}
			var execErr error
			if sr.Error != nil {
				execErr = sr.Error
			}
			registry.Complete(t.ID, sr.Output, execErr, sr.Steps)
			l.persistUnitOutcome(t.ID, sr.Output, execErr, sr.Steps)
		case <-asyncCtx.Done():
			registry.Complete(t.ID, "", asyncCtx.Err(), nil)
			l.settleUnit(t.ID, asyncCtx.Err())
		}
	}()

	return tools.DelegationResult{ID: t.ID, Status: tools.DelegationStatusRunning}
}

func (l *conductorLauncher) buildSubAgentTask(ctx context.Context, t tools.DelegationTask, registry *tools.DelegationRegistry, scopedEvents agent.Events) (agent.SubAgentTask, error) {
	// Resolve the requested agent profile again at launch (validation checked
	// existence earlier): a profile removed mid-run surfaces a clear error
	// instead of a profile-less subagent.
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
		// Capability gate (fail-closed, mirrors the MCP registry gate): a
		// "disabled" snapshot refuses the delegation; absent snapshot blocks
		// nothing. See specs/domains/capabilities.md.
		if agentDelegationBlocked(ctx, t.Agent) {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: agent %q is disabled by capabilities.agents and cannot be delegated to", t.ID, t.Agent)
		}
	}

	maxSteps := t.MaxSteps
	// A profile's max-steps (when >0) overrides the task field and the
	// complexity-derived default (delegation.md, tools table).
	if profile != nil && profile.Metadata.MaxSteps > 0 {
		maxSteps = profile.Metadata.MaxSteps
	}
	if maxSteps <= 0 {
		// Default budget = complexity × stepsPerComplexity (the Conductor's own limit).
		maxSteps = ComplexityFromContext(ctx) * stepsPerComplexity
	}

	// A profile's tool preference (when non-nil) overrides the task field; an
	// invalid tools field fails the delegation — never silently widened.
	if profile != nil {
		pref, err := profile.ToolPreferenceWithError()
		if err != nil {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: profile %q: %w", t.ID, t.Agent, err)
		}
		if pref != nil {
			t.Tools = pref
		}
	}
	taskTools, err := l.resolveTaskTools(ctx, t)
	if err != nil {
		return agent.SubAgentTask{}, err
	}
	taskDesc := l.buildTaskDescription(t, registry, maxSteps)

	// Seed a cooperative-pause checkpoint into executor and ContextManager so
	// the subagent continues where it stopped; failed steps re-run fresh. See
	// specs/domains/orchestration/delegation.md ("Cooperative Subagent Pause
	// and Resume").
	var resumeSteps []agent.Step
	if cp, ok := l.pausedCheckpoint(t.ID); ok {
		resumeSteps = append([]agent.Step(nil), cp.Steps...)
	}

	// Subagents inherit the Conductor's routing domain/complexity → compaction strategy.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)

	modelMeta := l.resolveModelMeta(ctx)

	// Activate the profile's required skills (AGENT.md `skills:`), merged
	// inherited-first, fail-closed (delegation.md).
	var profileSkills []*skills.Skill
	if profile != nil {
		required, err := profile.RequiredSkills()
		if err != nil {
			return agent.SubAgentTask{}, fmt.Errorf("delegation %q: profile %q: %w", t.ID, t.Agent, err)
		}
		if len(required) > 0 {
			merged, err := l.resolveProfileSkills(ctx, t, required)
			if err != nil {
				return agent.SubAgentTask{}, err
			}
			ctx = WithActiveSkills(ctx, &ActiveSkills{Skills: merged})
			profileSkills = merged
			if l.deps.logger != nil {
				names := make([]string, len(merged))
				for i, s := range merged {
					names[i] = s.Metadata.Name
				}
				l.deps.logger.Info("subagent profile skills activated",
					"delegation", t.ID, "agent", t.Agent, "skills", names)
			}
		}
	}

	// A profile's body REPLACES the orchestrator core directive, preserving
	// the shared project-context prefix (buildSpecializedSystemPrompt).
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
	// A profile's model override forces req.Model on every call (no-op when
	// empty, so unconditional).
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
	toolExec := l.deps.toolExec
	// The merged skills must also reach the TOOL execution context
	// (read_skill_resource resolves from it) via the per-subagent decorator
	// below.
	if len(profileSkills) > 0 && toolExec != nil {
		toolExec = &activeSkillsToolExec{inner: toolExec, skills: &ActiveSkills{Skills: profileSkills}}
	}
	executor := agent.NewExecutor(caller, toolExec, maxSteps, execOpts...)
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

// conductorOnlyToolNames must never reach a regular subagent: delegation
// machinery and the Conductor's plan/reflect workflow. See
// specs/domains/orchestration/delegation.md.
var conductorOnlyToolNames = map[string]struct{}{
	"delegate":          {},
	"cancel_delegation": {},
	"declare_plan":      {},
	"execute_plan":      {},
	"reflect":           {},
}

// coreNonCacheableToolNames extend the sp4rk default non-cacheable set via
// Executor.AddNonCacheableTools (specs/domains/orchestration/executor.md).
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

// stripSubagentTools removes what a task subagent must never hold: the
// Conductor-only tools plus the GroupSystem goal-loop actor tools. See
// specs/contracts/conductor-tools.md.
func stripSubagentTools(descs []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	return tools.StripGoalModeTools(stripConductorOnlyTools(descs))
}

// resolveTaskTools computes a task's toolset from DelegationTask.Tools:
// nil/""/"all" = full grant minus conductor-only/mode-disabled; "read-only" =
// system ∪ local/remote_read (no MCP); a group-token array = system ∪ granted
// (empty array errors). Fail-closed; MCP-mode gated via stripGatedMCPTools.
// See specs/domains/orchestration/delegation.md + mcp-gateway.md.
func (l *conductorLauncher) resolveTaskTools(ctx context.Context, t tools.DelegationTask) ([]sdktools.ToolDescriptor, error) {
	all := stripGatedMCPTools(ctx, l.stripDisabled(l.allToolDescriptors()))

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

// toolsByGroups filters descriptors by granted group, deduped by name;
// zero-group tools never match (ADR-024 fail-closed).
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

// parseToolGroups parses the array form of DelegationTask.Tools into groups
// (kebab/underscore); unknown or empty token lists fail.
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

// stripDisabled drops mode-disabled tools — never advertised to a subagent's
// LLM (dead tools waste the tool-call budget and mislead the model).
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
	// Resolve always returns usable metadata; the fallback window (128000)
	// keeps compaction alive — a zero ContextWindow means unbounded growth.
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
	// Logging stays independent of step dumps: active whenever provider+logger
	// exist, so LLM calls are logged even when dumps are disabled.
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

// planStepEventTranslator translates sp4rk subagent lifecycle events into
// plan-step events, so plan steps reuse the subagent machinery while appearing
// as plan steps in the UI. Plan-step events go to the ROOT emitter (the
// WithPlanStepID copy does not share the plan progress sets); child events
// carry plan_step_id via the embedded scoped copy. See
// specs/domains/orchestration/conductor.md.
type planStepEventTranslator struct {
	Emitter         // scoped copy (child events with plan_step_id)
	root    Emitter // session-root emitter (plan-step lifecycle + progress)
	summary string  // short UI label for PlanStepStart
}

func (t *planStepEventTranslator) SubAgentLaunch(stepID, description string) {
	t.root.PlanStepStart(stepID, description, t.summary)
}

func (t *planStepEventTranslator) SubAgentComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	t.root.PlanStepComplete(stepID, success, duration, errMsg)
}

func (t *planStepEventTranslator) SubAgentPaused(stepID string, duration time.Duration) {
	t.root.PlanStepPaused(stepID, duration, "")
}

// scopePlanStepEvents: a planStepEventTranslator over the WithPlanStepID-scoped
// emitter + root; NoopEvents when the emitter cannot scope.
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
	// Subagents observe the same universal pause signal as the Conductor —
	// a PauseSession pauses them at their own step boundary.
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
	// Per-tool-call ceiling: a single tool call that blocks forever must not
	// hang a subagent's ReAct loop. Mirrors the Conductor's
	// ConductorConfig.ToolCallTimeout for the main executor (0 = disabled).
	if l.deps.toolCallTimeout > 0 {
		executor.SetToolCallTimeout(l.deps.toolCallTimeout)
	}
	// Exempt tool-name set: nil keeps sp4rk's defaults, configured replaces
	// wholesale (host-exempted long-running tools survive here too).
	if l.deps.toolCallTimeoutExempt != nil {
		executor.SetToolCallTimeoutExempt(l.deps.toolCallTimeoutExempt...)
	}
	// Subagents that edit files get the same verify-on-edit hook; inert for
	// specialized passes (RunConductor nils it before the launcher is built).
	if l.deps.verifyOnEdit != nil {
		executor.SetVerifyOnEdit(l.deps.verifyOnEdit, l.deps.verifyOnEditMaxOutputChars)
	}
	executor.AddNonCacheableTools(coreNonCacheableToolNames...)
}

// resolveProfileSkills resolves a profile's required skills against the
// catalog, fail-closed (error names skill + profile), and merges them
// inherited-first (delegation.md, "Profile Required Skills").
func (l *conductorLauncher) resolveProfileSkills(ctx context.Context, t tools.DelegationTask, required []string) ([]*skills.Skill, error) {
	resolved := make([]*skills.Skill, 0, len(required))
	for _, name := range required {
		if l.deps.skillResolver == nil {
			return nil, fmt.Errorf("delegation %q: profile %q requires skill %q but no skill resolver is configured", t.ID, t.Agent, name)
		}
		s, ok := l.deps.skillResolver(name)
		if !ok || s == nil {
			return nil, fmt.Errorf("delegation %q: profile %q requires unknown skill %q — no Agent Skill with that name was found", t.ID, t.Agent, name)
		}
		resolved = append(resolved, s)
	}
	return mergeActiveSkills(ActiveSkillsFromContext(ctx), resolved), nil
}

// mergeActiveSkills merges inherited + required skills, inherited first,
// deduped by name (first wins) — stable activation order.
func mergeActiveSkills(inherited *ActiveSkills, add []*skills.Skill) []*skills.Skill {
	var out []*skills.Skill
	seen := make(map[string]struct{})
	addOne := func(s *skills.Skill) {
		if s == nil {
			return
		}
		if _, ok := seen[s.Metadata.Name]; ok {
			return
		}
		seen[s.Metadata.Name] = struct{}{}
		out = append(out, s)
	}
	if inherited != nil {
		for _, s := range inherited.Skills {
			addOne(s)
		}
	}
	for _, s := range add {
		addOne(s)
	}
	return out
}

// activeSkillsToolExec injects the subagent's merged skills into every tool
// call's context (read_skill_resource resolves from ctx; delegation.md).
type activeSkillsToolExec struct {
	inner  agent.ToolExecutor
	skills *ActiveSkills
}

func (a *activeSkillsToolExec) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	return a.inner.Execute(WithActiveSkills(ctx, a.skills), name, input)
}
func (a *activeSkillsToolExec) GetToolSource(name string) string { return a.inner.GetToolSource(name) }
func (a *activeSkillsToolExec) IsToolUntrusted(name string) bool {
	return a.inner.IsToolUntrusted(name)
}
func (a *activeSkillsToolExec) CacheStrategy(ctx context.Context, name string, input json.RawMessage) sdktools.CacheMode {
	return a.inner.CacheStrategy(ctx, name, input)
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
		// Normalize ids/deps (TrimSpace) exactly as validation matched them, so
		// published steps match registered ids at execution time.
		deps := make([]string, 0, len(t.DependsOn))
		for _, d := range t.DependsOn {
			if trimmed := strings.TrimSpace(d); trimmed != "" {
				deps = append(deps, trimmed)
			}
		}
		plan.Steps = append(plan.Steps, orchestration.PlanStep{
			ID:          strings.TrimSpace(t.ID),
			Summary:     t.Summary,
			Description: t.Description,
			DependsOn:   deps,
			Agent:       t.Agent,
		})
	}

	if p.plansDir == "" {
		return "", errors.New("declare_plan: session plans directory not configured")
	}
	// Refuse any pre-existing link resolving outside the session directory —
	// a planted …/<sid>/plans → <outside> link fails closed instead of
	// redirecting the plan write (review finding #47).
	if err := safeio.MkdirAllRealWithin(filepath.Dir(p.plansDir), p.plansDir, 0o755); err != nil {
		return "", fmt.Errorf("declare_plan: failed to create plans directory: %w", err)
	}
	md := SerializePlan(plan)
	p.lastMD = md
	suffix := RandomSuffix()
	path := filepath.Join(p.plansDir, fmt.Sprintf("plan_%s.md", suffix))
	// Atomic temp+rename that refuses a planted symlink at the final name.
	if err := safeio.WriteFileAtomic(path, []byte(md), 0o644); err != nil {
		return "", fmt.Errorf("declare_plan: failed to write plan file: %w", err)
	}

	// Emit events / set the blackboard plan only after the file is persisted —
	// the UI must not show a plan that cannot be reviewed.
	planEvents := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planEvents[i] = orchestration.PlanStepEvent{ID: s.ID, Summary: s.Summary, Description: s.Description, Status: "pending", DependsOn: s.DependsOn}
	}
	p.emitter.PlanGenerated(len(plan.Steps), planEvents)
	p.bb.SetPlan(plan)
	// Marks the plan workflow active in THIS run (the guards' trigger) — a
	// restored plan never reaches here. See conductor.md plan-workflow
	// invariants.
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

// AbandonPlan (declare_plan abandon): clears planState.declared — the run
// regains plan-less behavior — and settles the abandoned steps (completeAll
// is gated on planDeclaredInRun). Full invariant: conductor.md.
func (p *conductorPublisher) AbandonPlan() {
	if p.planState != nil {
		p.planState.unmarkDeclared()
	}
	p.settleAbandonedPlanSteps()
}

// settleAbandonedPlanSteps emits one terminal PlanStepComplete per step
// ("plan abandoned") — no PlanStepStart, since approval precedes any
// execution. Best-effort UI hygiene; no-op without an emitter or a plan.
func (p *conductorPublisher) settleAbandonedPlanSteps() {
	if p.emitter == nil || p.bb == nil {
		return
	}
	plan := p.bb.GetPlan()
	if plan == nil {
		return
	}
	for _, step := range plan.Steps {
		p.emitter.PlanStepComplete(step.ID, false, 0, "plan abandoned")
	}
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

// stepsPerComplexity derives the ReAct limit (complexity × 30) uniformly for
// the Conductor and subagents unless a delegation overrides max_steps.
const stepsPerComplexity = 30

// compactionStrategyForDomain maps routing domain + complexity to the
// compaction strategy (table: specs/domains/orchestration/conductor.md).
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

// conductorGuidanceForComplexity renders the Conductor Guidance section;
// band semantics + orthogonality are specified in router.md / delegation.md.
func conductorGuidanceForComplexity(complexity int) string {
	// Emitted once above the bands so a band edit cannot silently drop it (the
	// regression that broke the `explore` skill's Plan Mode entry). See
	// specs/domains/orchestration/router.md.
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
		// complexity >= 2: planning is recommended, never mandatory (the
		// skill clause above is the only mandatory trigger).
		return skillClause +
			"## Conductor Guidance\n" +
			"You are the Conductor: you own this task end-to-end. You decide whether this task needs a plan.\n\n" +
			"Planning is RECOMMENDED (not required) when complexity is high (>3) OR when the task decomposes into ordered steps — call declare_plan with mode=await_approval so the user signs off before any implementation, then execute_plan runs the steps in dependency-ordered parallel waves after approval. Use mode=present (display-only) only for low-stakes progress-shaping. Otherwise handle it plan-less: proceed inline, or delegate coherent units to subagents. The decision is yours — weigh whether user sign-off or parallelism genuinely helps before planning.\n\n" +
			"Every plan task MUST declare depends_on — list the prerequisite task ids whose output, artifacts, or decisions it consumes, or declare an explicit [] (empty array) for a task with no dependencies; omitting the field fails validation. A spurious edge only serializes; a missing edge runs the steps in parallel.\n\n" +
			"If you go plan-less, you MUST build a checklist (update_checklist with an empty step_id) at the start and report progress on each item as you complete it.\n\n" +
			"You MAY call delegate to break coherent units of work into isolated subagents — to keep your context lean or to parallelize work. Each delegate also builds its own checklist and reports progress. delegate does NOT require a plan. When a named subagent fits the work (see the \"Available Subagents\" section), target it via delegate(agent: \"name\") so the work runs with that agent's specialty and tool budget. If the user named specific subagents via #mentions (see \"Requested Subagents\"), you MUST delegate the corresponding work to those agents rather than handling it inline.\n\n" +
			"When the trajectory looks wrong, call reflect.\n\n" +
			"Call finish when the task is complete." + orthogonality
	}
}

// RunConductor builds and launches the Conductor: one Executor.Run owning the
// task end-to-end, Conductor tools/context injected here, loop internals owned
// by the sp4rk Conductor (conductor.md).
func RunConductor(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	deps conductorDeps,
	plansDir string,
) (*orchestration.ExecutionResult, error) {
	// planRunState is shared by publisher/launcher/lifecycle; continuable when
	// resuming a plan with unreached steps (restored plans set nothing).
	planState := newPlanRunState(deps.resumedWithPlan)

	// Built up front and threaded into deps (no post-construction mutation);
	// shared with update_checklist / declare_step_complete via the context
	// callbacks below.
	inlineLifecycle := newInlineStepLifecycle(deps.emitter, bb)
	inlineLifecycle.planState = planState
	deps.lifecycle = inlineLifecycle

	registry := tools.NewDelegationRegistry()
	// verify-on-edit is suppressed for specialized passes (verify-on-edit.md).
	// MUST precede the launcher: deps are held by VALUE, so a later suppression
	// would leave subagent executors armed.
	if deps.systemPromptOverride != nil {
		deps.verifyOnEdit = nil
	}
	launcher := &conductorLauncher{deps: deps, bb: bb, planState: planState}
	publisher := &conductorPublisher{emitter: deps.emitter, bb: bb, plansDir: plansDir, logger: deps.logger, planState: planState}

	// Composite trajectory store (see compositeTrajectoryStore): persists the
	// task checkpoint best-effort when a PersistableBlackboard carries a task ID.
	trajHolder := &trajectoryHolder{}
	taskID := ""
	if pbb, ok := bb.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	trajStore := newCompositeTrajectoryStore(trajHolder, taskID, deps.taskStore, deps.logger)
	runner := &conductorReflectionRunner{reflector: deps.reflector, bb: bb, emitter: deps.emitter, logger: deps.logger, traj: trajStore}

	// Durable delegation-spec persistence for the Resume wave; an isolated run
	// wires unitSink instead — it wins over the blackboard (delegation.md).
	if deps.unitSink != nil {
		deps.unitSink.wire(registry, deps.unitSink.parentID)
	} else if taskID != "" && deps.taskStore != nil {
		wireDelegationSpecSink(registry, launcher.unitLedger(), "", taskID, deps.taskStore, deps.logger)
	}

	// Derive compaction strategy from routing domain + complexity.
	domain := DomainFromContext(ctx)
	complexity := ComplexityFromContext(ctx)
	compactionStrategy := compactionStrategyForDomain(domain, complexity)
	// One-shot manual-compaction override: replaces the routing strategy for
	// the WHOLE run + forces the start-of-run pass (memory/compaction.md).
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
	// The agent resolver survives subagentCtx so a redelegating subagent can
	// delegate to a named profile; nil = profiles unavailable.
	ctx = tools.WithAgentResolver(ctx, deps.agentResolver)
	ctx = agent.WithTrajectoryStore(ctx, trajStore)
	ctx = orchestration.WithDelegationRegistry(ctx, registry)

	// Inline-step lifecycle for update_checklist / declare_step_complete;
	// subagents get their own observation-only callback (buildSubAgentTask).
	ctx = agent.WithStepTodoUpdateFunc(ctx, inlineLifecycle.onChecklistUpdate)
	ctx = tools.WithStepCompleteFunc(ctx, inlineLifecycle.completeStep)
	ctx = tools.WithPlanStepExecutor(ctx, launcher)

	// Checklist guard: reject standalone checklists once a plan is active in
	// THIS run, unless one was emitted earlier in the run (conductor.md,
	// "Checklist Guard" — the monotone-flag exception).
	ctx = agent.WithChecklistGuard(ctx, newChecklistGuard(planState))

	// Build the system prompt with complexity-based Conductor Guidance.
	systemPromptFactory := func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		prompt := buildSystemPrompt(ctx, msg, modelMeta)
		prompt += "\n\n" + guidance
		return prompt
	}
	// Specialized runs (goal derivation) override the prompt factory; the
	// override keeps the shared project-context prefix (goal-mode.md).
	if deps.systemPromptOverride != nil {
		systemPromptFactory = deps.systemPromptOverride
	}

	// maxSteps = complexity × stepsPerComplexity — uniform for normal and
	// specialized runs, so the verifier is bounded exactly like the executor
	// it checks. See specs/domains/orchestration/conductor.md ("Step Limit").
	maxSteps := complexity * stepsPerComplexity

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
		ToolCallTimeout:            deps.toolCallTimeout,
		ToolCallTimeoutExemptTools: deps.toolCallTimeoutExempt,
		VerifyOnEdit:               deps.verifyOnEdit,
		VerifyOnEditMaxOutputChars: deps.verifyOnEditMaxOutputChars,
		StopTools:                  deps.stopTools,
	}

	var events agent.Events = &agent.NoopEvents{}
	if deps.emitter != nil {
		events = deps.emitter
	}

	// Bind the launcher's async base ctx AFTER all context values are injected
	// (async delegation lifetime — see asyncBaseCtx).
	launcher.asyncBaseCtx = ctx

	conductor := orchestration.NewConductor(cfg)
	result, err := conductor.Run(ctx, message, bb, availableTools, events, compactionStrategy)
	// Finish fallback: complete inline steps the Conductor never closed via
	// declare_step_complete; skipped on a cooperative pause (NOT terminal —
	// the plan stays resumable). See specs/domains/orchestration/conductor.md.
	if !pausedRun(err, result) {
		success := err == nil && result != nil && result.Status == orchestration.ExecutionStatusSuccess
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		inlineLifecycle.completeAll(success, errMsg)
	}

	// Final flush: the loop has ended, so drain in-flight writes and persist
	// the checkpoint synchronously (the last Sync is likeliest dropped).
	trajStore.Flush()
	return result, err
}

// callerForConductor returns the main loop's LLMCaller: deps.llm as-is — the
// builder's stack already carries logging + session-dump wrappers. Rebuilding
// logging(trackingCaller) here would silently drop the session dump writer
// (the bug this replaced). Reasoning effort and context tracking are
// independent of this caller (executor-level / capability assertion).
func callerForConductor(deps conductorDeps) agent.LLMCaller {
	return deps.llm
}

// adaptContextFactory converts the core ContextManagerFactory to the sp4rk
// signature (core.ContextManager embeds agent.ContextManager).
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

// runConductor assembles conductorDeps from the orchestrator's fields and
// delegates to RunConductor (HandleMessage + Resume). conversationHistory is
// the prior dialogue (nil on Resume); forceCompactionStrategy and
// resumedWithPlan thread the one-shot resume requests (see their deps docs).
func (o *Orchestrator) runConductor(ctx context.Context, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, resumeSteps []agent.Step, contentBlocks []llm.ContentBlock, nudge, forceCompactionStrategy string, resumedWithPlan bool) (*orchestration.ExecutionResult, error) {
	deps := o.buildConductorDeps(conversationHistory, resumeSteps)
	deps.contentBlocks = contentBlocks
	deps.nudge = nudge
	deps.forceCompactionStrategy = forceCompactionStrategy
	deps.resumedWithPlan = resumedWithPlan
	return RunConductor(ctx, message, bb, availableTools, deps, plansDir)
}

// buildConductorDeps assembles conductorDeps from the orchestrator's stored
// fields so a goal-loop turn can swap in counting wrappers and a proposer and
// call RunConductor directly. resumeSteps nil = fresh start.
func (o *Orchestrator) buildConductorDeps(conversationHistory []llm.Message, resumeSteps []agent.Step) conductorDeps {
	return conductorDeps{
		contextFactory:        o.contextFactory,
		toolExec:              o.toolExec,
		toolRegistry:          o.toolRegistry,
		disabledTools:         o.disabledToolNames(),
		llm:                   o.llm,
		modelRegistry:         o.modelRegistry,
		model:                 o.currentModel(),
		tokenCounter:          o.tokenCounter,
		emitter:               o.emitter,
		logger:                o.logger,
		trackingCaller:        o.trackingCaller,
		providerName:          o.providerName,
		stepDumpTracker:       o.stepDumpTracker,
		toolCache:             o.toolCache,
		perToolTrunc:          o.perToolTrunc,
		toolResultBudget:      o.toolResultBudget,
		circuitBreaker:        o.circuitBreaker,
		hitlHandler:           o.config.HITLHandler,
		reflector:             o.reflector,
		maxRedelegDepth:       o.config.MaxRedelegationDepth,
		maxDepCtxChars:        o.config.MaxDependencyContextChars,
		maxParallelSubagents:  o.config.MaxParallelSubagents,
		reasoningEffort:       o.currentReasoningEffort(),
		preWarningPct:         o.config.PreWarningPercent,
		toolCallTimeout:       o.config.ToolCallTimeout,
		toolCallTimeoutExempt: o.config.ToolCallTimeoutExemptTools,
		conversationHistory:   conversationHistory,
		taskStore:             o.taskStore,
		resumeSteps:           resumeSteps,
		goalProposer:          o.goalProposer,
		// Profiles for buildSubAgentTask; nil-safe (delegate validation rejects `agent`).
		agentResolver: func(name string) (*agents.Agent, bool) {
			if o.agentManager == nil {
				return nil, false
			}
			return o.agentManager.Get(name)
		},
		// Skills for profile requirements; fail-closed when the manager is nil.
		skillResolver: func(name string) (*skills.Skill, bool) {
			if o.skillManager == nil {
				return nil, false
			}
			return o.skillManager.Get(name)
		},
		// pauseChecker wires the universal pause signal into every conductor
		// run (normal path + every goal-loop turn). It reads o.activePause
		// live; nil-safe when no request is in flight.
		pauseChecker: o.newPauseChecker(),
		// Live-message queue drained one per step boundary; nil-safe when empty.
		userMessageSource: func(context.Context) string { return o.DrainLiveUserMessages() },
		// verify-on-edit: suppressed in No Project (CHAT) mode — CHAT exposes
		// no file-edit tools, so verification there is never meaningful. The
		// runner itself is built by the builder from user config only.
		verifyOnEdit:               o.verifyOnEditForMode(),
		verifyOnEditMaxOutputChars: o.verifyOnEditMaxOutputChars,
	}
}

// inlineStepLifecycle manages inline plan steps: Start inferred from the
// first checklist update, Complete from declare_step_complete or the finish
// fallback. See specs/domains/orchestration/conductor.md ("Inline Step Lifecycle").
type inlineStepLifecycle struct {
	emitter Emitter
	scoper  CurrentStepScopable // nil if emitter doesn't support dynamic scoping
	bb      orchestration.Blackboard
	// Shared planRunState: completeAll must not synthesize terminal events for
	// a restored plan on a plan-less continuation; nil only in direct test
	// construction (planDeclaredInRun fallback).
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

// onChecklistUpdate emits StepTodoUpdate and infers PlanStepStart on the
// first update (BEFORE it, so the UI nests the checklist under the step).
func (l *inlineStepLifecycle) onChecklistUpdate(stepID string, items []agent.TodoItem) {
	if l == nil || l.emitter == nil {
		return
	}

	if stepID == "" {
		// Standalone checklist — record it so the ChecklistGuard keeps accepting
		// it if a plan activates later in this run (the monotone flag,
		// conductor.md "Checklist Guard").
		if l.planState != nil {
			l.planState.markStandaloneChecklist()
		}
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

	// Scope events to the step before PlanStepStart so they nest under the
	// step block (mirrors subagents' WithPlanStepID); a completed step is not
	// re-started.
	if first {
		if l.scoper != nil {
			l.scoper.SetCurrentStepID(stepID)
		}
		desc, summary := lookupStepDesc(l.bb, stepID)
		l.emitter.PlanStepStart(stepID, desc, summary)
	}

	l.emitter.StepTodoUpdate(stepID, items)
}

// completeStep (declare_step_complete): emits PlanStepComplete, records
// completion; synthesizes a start for never-started declared-plan steps.
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
		// Synthesized start — real start time unknown, duration stays 0.
		desc, summary := lookupStepDesc(l.bb, stepID)
		l.emitter.PlanStepStart(stepID, desc, summary)
	}
	l.emitter.PlanStepComplete(stepID, success, duration, errMsg)
	// Clear the step scope so post-completion events are not mis-tagged.
	if l.scoper != nil {
		l.scoper.SetCurrentStepID("")
	}
}

// markCompleted records completion WITHOUT events — execute_plan's translator
// already emitted the terminal pair; this stops finish-fallback completeAll
// from double-completing.
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

// isCompleted reports a terminal state in this run's lifecycle (the
// execute_plan resume path uses it to decide on synthesized pairs).
func (l *inlineStepLifecycle) isCompleted(stepID string) bool {
	if l == nil || stepID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.completed[stepID]
}

// planDeclaredInRun mirrors HasDeclaredPlan (declared | continuable; restored
// plans don't count; blackboard fallback under nil planState).
func (l *inlineStepLifecycle) planDeclaredInRun() bool {
	if l == nil {
		return false
	}
	if l.planState != nil {
		return l.planState.isActive()
	}
	return l.bb != nil && l.bb.GetPlan() != nil
}

// planDeclaredThisRun: declared only (no continuable) — replayed steps stay
// silent on continuations (issue #99).
func (l *inlineStepLifecycle) planDeclaredThisRun() bool {
	if l == nil {
		return false
	}
	if l.planState != nil {
		return l.planState.isDeclared()
	}
	return l.bb != nil && l.bb.GetPlan() != nil
}

// stepSucceededPreviously: the step has an error-free blackboard StepResult
// (possibly restored) — completeAll must not repaint it with this run's failure.
func (l *inlineStepLifecycle) stepSucceededPreviously(stepID string) bool {
	if l == nil || l.bb == nil {
		return false
	}
	sr, ok := l.bb.GetStepResult(stepID)
	return ok && sr.Error == nil
}

// seedCompletedFromBlackboard silently marks blackboard-successful steps
// completed on resume — their events came from the executing run (issue #99).
func (l *inlineStepLifecycle) seedCompletedFromBlackboard() {
	if l == nil || l.bb == nil {
		return
	}
	plan := l.bb.GetPlan()
	if plan == nil {
		return
	}
	l.mu.Lock()
	for _, step := range plan.Steps {
		if sr, ok := l.bb.GetStepResult(step.ID); ok && sr.Error == nil {
			l.completed[step.ID] = true
			delete(l.started, step.ID)
			delete(l.startedAt, step.ID)
		}
	}
	l.mu.Unlock()
}

// completeAll (finish fallback) sweeps declared plan steps to a terminal
// state — started complete directly, never-started get a synthesized start;
// plan-less runs complete only the started set.
func (l *inlineStepLifecycle) completeAll(success bool, errMsg string) {
	if l == nil || l.emitter == nil {
		return
	}

	// Collect declared plan step IDs (blackboard read outside the lifecycle
	// lock). Gated on planDeclaredInRun() so a plan-less continuation with a
	// restored plan does not re-emit terminal events for those steps.
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
		// A previously-succeeded step must not be repainted with this run's
		// failure: re-declared → replay success; continuation → silent
		// (issue #99); unseeded = defensive fallback.
		if l.stepSucceededPreviously(id) {
			if l.planDeclaredThisRun() {
				desc, summary := lookupStepDesc(l.bb, id)
				l.emitter.PlanStepStart(id, desc, summary)
				l.emitter.PlanStepComplete(id, true, 0, "")
			}
			continue
		}
		desc, summary := lookupStepDesc(l.bb, id)
		l.emitter.PlanStepStart(id, desc, summary)
		l.emitter.PlanStepComplete(id, success, 0, errMsg)
	}
	// Clear the step scope so post-finish events are not mis-tagged.
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
