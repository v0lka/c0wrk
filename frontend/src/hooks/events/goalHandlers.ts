// Shared goal event handlers — used by both the active-session event hook
// (useGoalEvents) and the background-session watcher
// (useBackgroundSessionWatcher) so goal state reaches the goal store
// regardless of which session the user is currently viewing. Mirrors the
// hitlHandlers.ts pattern: a background session that blocks on a goal
// proposal must not lose its event (the agent goroutine would then hang
// forever with no UI to respond).

import { useGoalStore } from '@/stores/goalStore'
import { useChatStore } from '@/stores/chatStore'
import type { ActiveGoal, GoalProgress } from '@/stores/goalStore'
import type { GoalProposalData, GoalStatusData, GoalProgressData } from '@/types/events'

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
    content: data.needs_clarification && data.clarification
      ? data.clarification
      : data.condition,
    metadata: {
      request_id: data.request_id,
      condition: data.condition,
      verify: data.verify,
      clarification: data.clarification ?? '',
      needs_clarification: data.needs_clarification,
      resolved: false,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  useChatStore.getState().setActivityStatus(sessionId, 'Goal proposed — awaiting your approval...')
}

/** Apply a goal_status snapshot to a session's goal store entry. */
export function handleGoalStatusEvent(sessionId: string, data: GoalStatusData): void {
  const goal: ActiveGoal = {
    condition: data.condition,
    status: data.status,
    turn: data.turn,
    maxTurns: data.max_turns,
    maxTokens: data.max_tokens,
    tokens: data.tokens,
    deadline: data.deadline,
    verdict: data.verdict,
    reason: data.reason,
  }
  const store = useGoalStore.getState()
  // Preserve a previously-confirmed verify instruction across status
  // snapshots — the status event does not echo it back. verify is seeded into
  // the store on approval (GoalProposalPanel.onApprove), so this branch keeps
  // the user's approved verify clause available to any consumer of activeGoal.
  const prev = store.activeGoal[sessionId]
  if (prev?.verify !== undefined) goal.verify = prev.verify
  store.setActiveGoal(sessionId, goal)
}

/** Apply mid-loop goal progress telemetry to a session's goal store entry. */
export function handleGoalProgressEvent(sessionId: string, data: GoalProgressData): void {
  const progress: GoalProgress = {
    turn: data.turn,
    maxTurns: data.max_turns,
    tokens: data.tokens,
    maxTokens: data.max_tokens,
    condition: data.condition,
  }
  useGoalStore.getState().setGoalProgress(sessionId, progress)
}
