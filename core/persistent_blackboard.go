package core

import (
	"context"
	"log/slog"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
)

// PersistableBlackboard extends orchestration.Blackboard with persistence
// lifecycle methods (the concrete type lives in backend/session).
// See specs/domains/memory/blackboard.md.
type PersistableBlackboard interface {
	orchestration.Blackboard
	SetEmitter(emitter Emitter)
	SetRouting(routing *router.RoutingDecision)
	Routing() *router.RoutingDecision
	CompleteTask(attemptCount int)
	FailTask()
	CancelTask()
	// PauseTask marks the in-progress task as paused, persisting a resumable
	// checkpoint; like CancelTask it writes synchronously before returning.
	PauseTask()
	ReactivateTask()
	TaskID() string
}

// BlackboardRestoreFunc restores a PersistableBlackboard from persistence;
// nil, nil when the task is not found. See specs/domains/memory/blackboard.md.
type BlackboardRestoreFunc func(taskID, sessionID string, store TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (PersistableBlackboard, error)

// DelegationSpecReader is an optional PersistableBlackboard capability exposing
// the task's persisted delegation specs (the Resume auto-resume wave asserts for it).
type DelegationSpecReader interface {
	DelegationSpecs() []tools.DelegationSpec
}

// UnitStoreProvider is an optional PersistableBlackboard capability exposing the
// durable unit store (absent capability / store = in-memory ledger fallback).
// See specs/decisions/048-unified-recovery-ledger.md.
type UnitStoreProvider interface {
	UnitStore() units.Store
}

// UnitLedgerProvider is an optional capability exposing the blackboard's cached
// mainline ledger (all callers share one). See specs/decisions/048-unified-recovery-ledger.md.
type UnitLedgerProvider interface {
	UnitLedger() units.Ledger
}

// NewBlackboardLedger builds the MAINLINE unit ledger: task from TaskID(),
// store from the optional UnitStoreProvider capability, cached ledger
// preferred — it never fails, degrading to a best-effort in-memory ledger.
// See specs/decisions/048-unified-recovery-ledger.md.
func NewBlackboardLedger(bb PersistableBlackboard) units.Ledger {
	if bb == nil {
		return units.NewLedger(nil, "", "")
	}
	if p, ok := bb.(UnitLedgerProvider); ok {
		if l := p.UnitLedger(); l != nil {
			return l
		}
	}
	var store units.Store
	if p, ok := bb.(UnitStoreProvider); ok {
		store = p.UnitStore()
	}
	return units.NewLedger(store, bb.TaskID(), "")
}

// TaskPersistence is the core-side task-storage abstraction — implementations
// live in backend/session (core must not import backend); implementations
// must be safe for concurrent use. See specs/domains/memory/blackboard.md.
type TaskPersistence interface {
	// PersistMCPMentions atomically unions server names into durable task intent.
	PersistMCPMentions(ctx context.Context, taskID string, names []string) error
	// LoadMCPMentions returns the ordered task intent; legacy empty state is not an error.
	LoadMCPMentions(ctx context.Context, taskID string) ([]string, error)
	PersistNewTask(taskID, sessionID, originalRequest string) error
	PersistPlan(taskID string, plan *orchestration.Plan) error
	PersistRouting(taskID string, routing *router.RoutingDecision) error
	PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error
	PersistReflection(taskID string, r orchestration.Reflection) error
	PersistCompletion(taskID, finalOutput string, attemptCount int) error
	PersistFailure(taskID string) error
	PersistCancellation(taskID string) error
	// PersistPause marks an in-progress task as paused (resumable checkpoint).
	PersistPause(taskID string) error
	PersistFacts(taskID string, facts []orchestration.Fact) error
	// PersistAttachments persists the full attachments list (rehydrated on continuation).
	PersistAttachments(taskID string, attachments []orchestration.Attachment) error
	// SaveTrajectory persists the Conductor's full []agent.Step trajectory (survives restart).
	SaveTrajectory(taskID string, steps []agent.Step) error
	// LoadTrajectory restores the task's []agent.Step trajectory; nil, nil when none persisted.
	LoadTrajectory(taskID string) ([]agent.Step, error)
	// PersistGoalState persists the goal-loop's *goal.GoalState (survives restart; resumes into the loop).
	PersistGoalState(taskID string, gs *goal.GoalState) error
	// LoadGoalState restores the task's *goal.GoalState; nil, nil when none persisted.
	LoadGoalState(taskID string) (*goal.GoalState, error)
	// PersistDelegationSpec persists a delegation's full rebuild spec, so a
	// paused delegation resumes without any LLM decision.
	PersistDelegationSpec(taskID string, spec tools.DelegationSpec) error
	// LoadDelegationSpecs restores all persisted delegation specs; nil, nil when none.
	LoadDelegationSpecs(taskID string) ([]tools.DelegationSpec, error)
	// Restoration
	LoadTaskState(taskID string) (*TaskState, error)
	GetUnfinishedTaskID(sessionID string) (string, error) // returns "" if none
	// Task lifecycle
	ReactivateTask(taskID string) error
}

// TaskState holds the restored state of a persisted task.
type TaskState struct {
	TaskID          string
	SessionID       string
	OriginalRequest string
	RoutingDecision *router.RoutingDecision
	Plan            *orchestration.Plan
	StepResults     map[string]orchestration.StepResult
	Reflections     []orchestration.Reflection
	FinalOutput     string
	Facts           []orchestration.Fact       // keyword-tagged facts
	Attachments     []orchestration.Attachment // user-attached files converted to markdown
	GoalState       *goal.GoalState            // goal-loop state (nil for non-goal tasks)
	// Delegations holds the persisted delegation specs so Resume can rebuild
	// and auto-resume paused delegates; empty for plan-only tasks.
	Delegations []tools.DelegationSpec
	Status      string // "in_progress", "completed", "failed", "cancelled", "paused"
}
