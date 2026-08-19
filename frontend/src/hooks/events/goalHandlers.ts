// Shared goal event handlers — used by both the active-session event hook
// (useGoalEvents) and the background-session watcher
// (useBackgroundSessionWatcher) so goal state reaches the goal store
// regardless of which session the user is currently viewing. Mirrors the
// hitlHandlers.ts pattern: a background session that blocks on a goal
// proposal must not lose its event (the agent goroutine would then hang
// forever with no UI to respond).

import { useGoalStore } from '@/stores/goalStore'
import { useChatStore } from '@/stores/chatStore'
import type { GoalProgress } from '@/stores/goalStore'
import type { GoalProposalData, GoalStatusData, GoalProgressData } from '@/types/events'
import { buildGoalTransitionNotice, goalStatusToActiveGoal } from '@/lib/goalTransition'

/** Record a pending goal proposal for a session (active or background).
 *
 *  Writes BOTH the goal store entry (for the goal progress panel / active-goal
 *  display) AND a chat message of type `goal_proposal` (so the editable
 *  approval panel renders inline in the chat stream, mirroring plan_review).
 *  The message id is deterministic (`goal-proposal-${request_id}`) so the live
 *  card and the session-switch reconciliation share an id. */
export function handleGoalProposalEvent(sessionId: string, data: GoalProposalData): void {
  useGoalStore.getState().setPendingProposal(sessionId, data)
  useChatStore.getState().addMessage(sessionId, {
    id: `goal-proposal-${data.request_id}`,
    sessionId,
    type: 'goal_proposal',
    content: data.condition,
    metadata: {
      request_id: data.request_id,
      condition: data.condition,
      verify: data.verify,
      // Surface the derivation-chosen verification mode so the approval panel
      // can show/edit it and round-trip a user edit back via confirmGoal.
      verification_mode: data.verification_mode ?? '',
      resolved: false,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  useChatStore.getState().setActivityStatus(sessionId, 'Goal proposed — awaiting your approval...')
}

/** Apply a goal_status snapshot to a session's goal store entry. */
export function handleGoalStatusEvent(sessionId: string, data: GoalStatusData): void {
  const store = useGoalStore.getState()
  const prev = store.activeGoal[sessionId]
  // goalStatusToActiveGoal preserves a previously-confirmed verify clause and
  // verification mode across snapshots (the status event does not always echo
  // them back).
  store.setActiveGoal(sessionId, goalStatusToActiveGoal(data, prev))

  // Surface a visible turn-transition notification in the chat stream. Without
  // this, the user sees no signal that the verifier finished or that the goal
  // loop advanced to a new turn — the previous service-piggybacked emission was
  // lost in transit and nothing rendered regardless. Rendered as a `status`
  // message → ServiceMessage (the same compact one-liner used for routing
  // notices). The id is deterministic per (run, turn, status) so a re-emission
  // of the same snapshot is idempotent without colliding across runs.
  const notice = buildGoalTransitionNotice(data)
  if (notice) {
    // id is deterministic per (run, turn, status): run identity (created_at)
    // disambiguates consecutive goal runs whose turn counts both reset to 1,
    // so a re-emission of the same snapshot is idempotent without colliding
    // across runs.
    useChatStore.getState().addMessage(sessionId, {
      id: `goal-status-${data.created_at ?? 'legacy'}-${data.turn}-${data.status}`,
      sessionId,
      type: 'status',
      content: notice,
      timestamp: Date.now(),
    })
    useChatStore.getState().setActivityStatus(sessionId, notice)
  }
}

/** Apply mid-loop goal progress telemetry to a session's goal store entry. */
export function handleGoalProgressEvent(sessionId: string, data: GoalProgressData): void {
  const progress: GoalProgress = {
    turn: data.turn,
    maxTurns: data.max_turns,
    condition: data.condition,
  }
  useGoalStore.getState().setGoalProgress(sessionId, progress)
}
