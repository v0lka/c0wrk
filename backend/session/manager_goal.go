package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/core/goal"
)

// PauseGoal signals the currently-running goal loop (if any) for the session
// to pause at the top of its next turn. It is a no-op when no goal loop is
// active or the orchestrator is not yet built. The pause is cooperative: the
// loop polls the signal, transitions the goal to StatusPaused, persists it,
// and exits so a later ResumeGoal can re-enter.
func (m *Manager) PauseGoal(sessionID string) error {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	orch := session.GetOrchestrator()
	if orch == nil {
		return errors.New("orchestrator not initialized")
	}
	orch.PauseGoal()
	return nil
}

// ResumeGoal re-enters the goal loop for a paused (or still-active, non-terminal)
// goal. It delegates to ResumeTask, which loads the unfinished task + persisted
// GoalState and dispatches to the orchestrator's resume path (resumeGoalLoop
// for non-terminal goals, the plain Conductor path otherwise). Returns nil if
// there is no resumable goal/task.
func (m *Manager) ResumeGoal(ctx context.Context, sessionID string) error {
	return m.ResumeTask(ctx, sessionID)
}

// ClearGoal abandons the goal for a session: it cancels any in-flight task
// (stopping a running goal loop) and THEN marks the persisted GoalState
// cancelled (a terminal state) so it will not resume on the next app restart.
//
// Ordering matters: the cancel-and-wait happens FIRST so the running goal
// loop's final best-effort persist settles BEFORE we overwrite the state with
// cancelled. Persisting cancelled first would let the loop's exit-time persist
// clobber it (the loop can't see the external cancel and writes its own
// paused/active state). A session with no goal task is a no-op.
//
// CancelTask's "no active task" (ErrNoActiveTask) is treated as non-fatal: the
// goal may have already exited its loop, in which case the cancelled-state
// persistence is still the part that matters.
//
// The task is looked up via GetLatestTaskID (status-agnostic) rather than
// GetUnfinishedTaskID: CancelTask flips the task row to "cancelled" as part of
// its completion path, so a task that WAS in-progress is no longer returned by
// GetUnfinishedTaskID by the time CancelTask returns. The status-agnostic
// lookup still locates the row so the goal state can be overwritten.
func (m *Manager) ClearGoal(sessionID string) error {
	// Cancel any running task first (and wait for it to finish). This stops a
	// live goal loop and lets its final best-effort persist settle before we
	// write the terminal cancelled state below. A session that has already
	// exited its loop returns ErrNoActiveTask, which is not an error here.
	if err := m.CancelTask(sessionID); err != nil && !errors.Is(err, ErrNoActiveTask) {
		return err
	}

	// Now mark the persisted goal state cancelled. Use the status-agnostic
	// GetLatestTaskID: CancelTask flips the just-cancelled task row to
	// "cancelled" in its completion path, so GetUnfinishedTaskID (which only
	// matches in_progress/failed) would miss it for the primary active-loop
	// case. The status-agnostic lookup still finds the row so we can overwrite
	// the goal state the loop persisted on its way out.
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter := NewTaskStoreAdapter(ts)
		if taskID, err := adapter.GetLatestTaskID(sessionID); err != nil {
			m.log().Warn("clear goal: failed to look up latest task", "session", sessionID, "error", err)
		} else if taskID != "" {
			if gs, err := adapter.LoadGoalState(taskID); err != nil {
				m.log().Warn("clear goal: failed to load goal state", "session", sessionID, "error", err)
			} else if gs != nil {
				gs.Status = goal.StatusCancelled
				if err := adapter.PersistGoalState(taskID, gs); err != nil {
					m.log().Warn("clear goal: failed to persist cancelled goal state", "session", sessionID, "error", err)
				}
			}
		}
	}

	return nil
}

// SetGoalProposalResolver installs the callback that delivers a user decision
// to a blocked goal-proposal channel. Desktop wires this after
// buildGoalProposalCallback registers its pending map. Without it,
// ResolveGoalProposal is a no-op (and the event-based path is the only
// resolution route).
func (m *Manager) SetGoalProposalResolver(fn func(requestID, decision, condition, verify, verificationMode, clarification string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.goalProposalResolver = fn
}

// ResolveGoalProposal delivers a user decision on a pending goal proposal.
// decision is "approve", "clarify", or "cancel". condition/verify carry the
// (possibly edited) approved values; verificationMode carries the (possibly
// edited) per-goal verification mode for the approve path; clarification
// carries a clarifying answer. Returns true when a pending proposal was found
// and resolved, false otherwise (including when no resolver is wired).
func (m *Manager) ResolveGoalProposal(requestID, decision, condition, verify, verificationMode, clarification string) bool {
	m.mu.RLock()
	fn := m.goalProposalResolver
	m.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(requestID, decision, condition, verify, verificationMode, clarification)
}
