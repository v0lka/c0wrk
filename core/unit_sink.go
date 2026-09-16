package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
)

// Goal-verifier unit topology.
//
// The verifier runs as an ISOLATED Conductor pass on a throwaway seeded
// MapBlackboard, so its run has no PersistableBlackboard to hang the usual
// delegation-spec / step-result persistence off. Its units are therefore
// recorded through a units.Ledger instead — scoped to the goal task the
// verifier is judging, under a dedicated namespace, and parented under a
// single verifier unit. That keeps the verifier's durable records visible to a
// later ledger scoped to the same task while its LLM view stays the isolated
// blackboard (the live task's incomplete state is never read by the verifier).
const (
	// verifierUnitNamespace scopes the verifier's units so their ids never
	// collide with the mainline's (namespace "") units under the same task.
	verifierUnitNamespace = "goal_verification"
	// verifierUnitID is the id of the goal-verification parent unit. Every
	// delegation the verifier spawns is parented under it, so the verifier's
	// units are both namespaced AND parent-linked as verifier units.
	verifierUnitID = "goal_verification"
)

// unitSink persists a Conductor run's units through a units.Ledger instead of
// the task store's delegation / step-result tables. It is the isolated-context
// counterpart to the persistent-blackboard wiring: a run whose blackboard is a
// throwaway seeded MapBlackboard (the goal verifier) still records the
// delegations it spawns — their rebuild spec at registration and their resume
// checkpoint at settle — as durable unit records, parented under a parent unit
// and scoped to a namespace.
//
// The sink writes ONLY to the ledger. It never makes the run's own blackboard
// durable, so the run's LLM view stays exactly what it was — the live task's
// incomplete state cannot leak in through persistence.
//
// All writes are best-effort: a ledger failure is logged, never propagated, so
// losing a checkpoint degrades resumability rather than the current run (the
// same contract wireDelegationSpecSink honors for the mainline path).
type unitSink struct {
	ledger units.Ledger
	// parentID is the unit every delegation registered on a root registry is
	// parented under (the verifier unit id for the goal verifier).
	parentID string
	logger   *slog.Logger

	mu sync.Mutex
	// known holds the ids this sink registered (via the spec sink). record()
	// ignores ids the sink never began, so a plan-step or an unrelated
	// blackboard write can never synthesize a phantom unit.
	known map[string]struct{}
}

// newUnitSink builds a ledger-bound sink whose top-level delegations are
// parented under parentID.
func newUnitSink(ledger units.Ledger, parentID string, logger *slog.Logger) *unitSink {
	return &unitSink{
		ledger:   ledger,
		parentID: parentID,
		logger:   logger,
		known:    make(map[string]struct{}),
	}
}

// newVerifierUnitSink builds the ledger-bound sink for one goal-verification
// pass. It scopes a fresh ledger to the goal task + verifier namespace, records
// the pass itself as a goal_verification parent unit (so a delegation's parent
// link resolves to a real record), and returns a sink whose delegations are
// parented under it.
//
// It returns nil when there is no task id (an in-memory-only run has nothing to
// persist under) — the caller then leaves deps.unitSink nil and RunConductor
// falls back to its blackboard-derived wiring. store may be nil: the ledger
// then degrades to best-effort in-memory operation.
func newVerifierUnitSink(taskID string, store units.Store, gs *goal.GoalState, logger *slog.Logger) *unitSink {
	if taskID == "" {
		return nil
	}
	ledger := units.NewLedger(store, taskID, verifierUnitNamespace)

	var spec json.RawMessage
	if gs != nil {
		if raw, err := json.Marshal(gs); err == nil {
			spec = raw
		} else if logger != nil {
			logger.Warn("unit sink: marshal goal spec failed", "task_id", taskID, "error", err)
		}
	}
	if err := ledger.Begin(units.UnitRecord{
		ID:     verifierUnitID,
		Kind:   units.UnitKindGoalVerification,
		Status: units.UnitStatusRunning,
		Spec:   spec,
	}); err != nil && logger != nil {
		logger.Warn("unit sink: begin verifier unit failed", "task_id", taskID, "error", err)
	}
	return newUnitSink(ledger, verifierUnitID, logger)
}

// wire installs the sink on a delegation registry: every RegisterTask records a
// subagent unit parented under parentID (the verifier unit for the root
// registry, the delegating subagent's unit id for a child registry). A nil sink
// or registry is a no-op, so callers can wire unconditionally.
func (s *unitSink) wire(registry *tools.DelegationRegistry, parentID string) {
	if s == nil || registry == nil {
		return
	}
	registry.SetSpecSink(parentID, func(spec tools.DelegationSpec) {
		s.begin(parentID, spec)
	})
}

// begin records a newly-registered delegation as a running subagent unit: its
// rebuild spec, its parent link (parentID) and its depth. The id is tracked so
// record() can update the same unit when the delegation settles.
func (s *unitSink) begin(parentID string, spec tools.DelegationSpec) {
	if s == nil || spec.Task.ID == "" {
		return
	}
	rec := units.UnitRecord{
		ID:       spec.Task.ID,
		Kind:     units.UnitKindSubagent,
		ParentID: parentID,
		Depth:    spec.Depth,
		Status:   units.UnitStatusRunning,
	}
	if raw, err := json.Marshal(spec); err == nil {
		rec.Spec = raw
	} else {
		s.warn("marshal delegation spec", spec.Task.ID, err)
	}
	if err := s.ledger.Begin(rec); err != nil {
		s.warn("begin", spec.Task.ID, err)
		return
	}
	s.mu.Lock()
	s.known[spec.Task.ID] = struct{}{}
	s.mu.Unlock()
}

// record persists a settled unit's resume checkpoint (the ReAct steps) and its
// terminal status, inferred from execErr (paused / failed / completed). Ids the
// sink never began are ignored, so a plan-step or unrelated id is a no-op.
func (s *unitSink) record(id string, execErr error, steps []agent.Step) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	_, known := s.known[id]
	s.mu.Unlock()
	if !known {
		return
	}
	if len(steps) > 0 {
		if err := s.ledger.Checkpoint(id, steps); err != nil {
			s.warn("checkpoint", id, err)
		}
	}
	if err := s.ledger.Settle(id, unitStatusFor(execErr)); err != nil {
		s.warn("settle", id, err)
	}
}

func (s *unitSink) warn(op, id string, err error) {
	if s.logger != nil {
		s.logger.Warn("unit sink "+op+" failed", "unit_id", id, "error", err)
	}
}

// unitStatusFor maps a settled outcome's error onto the durable unit status.
//
// A cooperative pause is a recoverable checkpoint (paused, not failed). A
// CANCELLATION is an abandonment, not a failure of the unit: a graceful app
// exit or a task cancel leaves the delegation mid-flight, exactly like the
// crash the ledger's `interrupted` status exists for. Mapping it to `failed`
// would make the unit terminal, so the resume funnel would DROP the in-flight
// delegated work instead of relaunching it fresh — whether abandoned work is
// recovered would then depend only on whether the exit was graceful. Any other
// non-nil error is a failure; a nil error is a completion.
func unitStatusFor(execErr error) units.UnitStatus {
	switch {
	case isPaused(execErr):
		return units.UnitStatusPaused
	case errors.Is(execErr, context.Canceled), errors.Is(execErr, context.DeadlineExceeded):
		return units.UnitStatusInterrupted
	case execErr != nil:
		return units.UnitStatusFailed
	default:
		return units.UnitStatusCompleted
	}
}
