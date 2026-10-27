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

// Goal-verifier unit topology: the verifier runs as an ISOLATED Conductor pass
// on a throwaway seeded MapBlackboard, so its units persist through a units.Ledger
// (goal task scope, dedicated namespace, one parent unit). See specs/decisions/048-unified-recovery-ledger.md.
const (
	// verifierUnitNamespace keeps the verifier's unit ids from colliding with
	// the mainline's (namespace "") under the same task.
	verifierUnitNamespace = "goal_verification"
	// verifierUnitID is the parent unit every verifier delegation links to.
	verifierUnitID = "goal_verification"
)

// unitSink persists a Conductor run's units through a units.Ledger — the
// isolated-context counterpart to the persistent-blackboard wiring: a run on
// a throwaway seeded MapBlackboard still records its delegations (rebuild
// spec at registration, checkpoint at settle); writes are best-effort —
// logged, never propagated. See specs/decisions/048-unified-recovery-ledger.md.
type unitSink struct {
	ledger units.Ledger
	// parentID is the unit every delegation registered on a root registry is
	// parented under (the verifier unit id for the goal verifier).
	parentID string
	logger   *slog.Logger

	mu sync.Mutex
	// known holds the ids this sink began; record() ignores others, so a
	// plan-step or unrelated blackboard write cannot synthesize a phantom unit.
	known map[string]struct{}
}

func newUnitSink(ledger units.Ledger, parentID string, logger *slog.Logger) *unitSink {
	return &unitSink{
		ledger:   ledger,
		parentID: parentID,
		logger:   logger,
		known:    make(map[string]struct{}),
	}
}

// newVerifierUnitSink builds the ledger-bound sink for one goal-verification
// pass: a ledger scoped to the goal task + verifier namespace, with the pass
// itself recorded as the goal_verification parent unit. Returns nil when
// taskID is empty (RunConductor then uses blackboard-derived wiring); a nil
// store degrades to in-memory operation.
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

// wire installs the sink on a delegation registry: parentID is the verifier
// unit for the root registry, the delegating subagent's unit id for a child.
func (s *unitSink) wire(registry *tools.DelegationRegistry, parentID string) {
	if s == nil || registry == nil {
		return
	}
	registry.SetSpecSink(parentID, func(spec tools.DelegationSpec) {
		s.begin(parentID, spec)
	})
}

// begin records a newly-registered delegation as a running subagent unit
// (rebuild spec, parent link, depth), tracking its id for record().
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

// record persists a settled unit's resume checkpoint (the ReAct steps) and
// its terminal status, inferred from execErr.
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

// unitStatusFor maps a settled outcome's error onto the durable unit status:
// cancellation (graceful or not) is an abandonment — `interrupted`, never
// `failed` — so the resume funnel relaunches it fresh; nil is a completion.
// See specs/domains/orchestration/delegation.md (unit record).
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
