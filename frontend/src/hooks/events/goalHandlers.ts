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
  const goal: ActiveGoal = {
    condition: data.condition,
    status: data.status,
    turn: data.turn,
    maxTurns: data.max_turns,
    verdict: data.verdict,
    reason: data.reason,
    evidence: data.evidence,
    verification: data.verification,
    verificationReason: data.verification_reason,
    verificationEvidence: data.verification_evidence,
  }
  const store = useGoalStore.getState()
  const prev = store.activeGoal[sessionId]
  // Preserve a previously-confirmed verify instruction across status
  // snapshots — the status event does not always echo it back. verify is seeded
  // into the store on approval (GoalProposalPanel.onApprove), so this branch
  // keeps the user's approved verify clause available to any consumer of
  // activeGoal.
  if (prev?.verify !== undefined) goal.verify = prev.verify
  // Thread the verification mode: prefer the snapshot's value, else preserve a
  // previously-seen one (e.g. from the approval), else leave unset (the default
  // 'executable' is implied by absence).
  if (data.verification_mode) {
    goal.verificationMode = data.verification_mode
  } else if (prev?.verificationMode) {
    goal.verificationMode = prev.verificationMode
  }
  store.setActiveGoal(sessionId, goal)

  // Surface a visible turn-transition notification in the chat stream. Without
  // this, the user sees no signal that the verifier finished or that the goal
  // loop advanced to a new turn — the previous service-piggybacked emission was
  // lost in transit and nothing rendered regardless. Rendered as a `status`
  // message → ServiceMessage (the same compact one-liner used for routing
  // notices). The id is deterministic per (turn, status) so a re-emission of
  // the same snapshot is idempotent.
  const notice = buildGoalTransitionNotice(data)
  if (notice) {
    useChatStore.getState().addMessage(sessionId, {
      id: `goal-status-${data.turn}-${data.status}`,
      sessionId,
      type: 'status',
      content: notice,
      timestamp: Date.now(),
    })
    useChatStore.getState().setActivityStatus(sessionId, notice)
  }
}

/** buildGoalTransitionNotice returns a human-readable, one-line summary of a
 *  goal_status snapshot's turn transition, or null when the snapshot carries no
 *  user-relevant transition (e.g. a bare mid-loop active status without a
 *  verdict or verification outcome). */
function buildGoalTransitionNotice(data: GoalStatusData): string | null {
  // max_turns <= 0 (or unset) means an unlimited turn budget (see
  // goal.GoalBudget.MaxTurns contract: 0 = unlimited). Render the ∞ glyph so a
  // met goal reads "turn 1/∞" rather than the misleading "turn 1/0". Mirrors
  // GoalStatusIndicator.tsx and BudgetCombobox.tsx.
  const budgetLabel = data.max_turns > 0 ? String(data.max_turns) : '∞'
  const turnLabel = `turn ${data.turn}/${budgetLabel}`
  // The verifier rejected the agent's "met" claim → the loop will retry. This
  // is the transition the user most needs to see (it explains why work
  // continues after the agent declared success).
  if (data.verification === 'rejected') {
    const why = data.reason ? ` — ${truncate(data.reason, 140)}` : ''
    return `Goal not met (${turnLabel}) — verifier rejected, retrying${why}`
  }
  // The agent self-declared the goal not met → retrying.
  if (data.verdict === 'not_met') {
    const why = data.reason ? ` — ${truncate(data.reason, 140)}` : ''
    return `Goal not met (${turnLabel}) — retrying${why}`
  }
  // Terminal outcomes.
  switch (data.status) {
    case 'met':
      return `Goal met (${turnLabel})`
    case 'exhausted':
      return `Goal not reached — turn budget exhausted (${turnLabel})`
    case 'blocked_idle':
      return `Goal blocked — agent could not make progress (${turnLabel})`
    case 'paused':
      return `Goal paused (${turnLabel})`
    default:
      return null
  }
}

/** truncate clamps a string to `max` chars, appending an ellipsis when cut. */
function truncate(s: string, max: number): string {
  const trimmed = s.trim().replace(/\s+/g, ' ')
  if (trimmed.length <= max) return trimmed
  return trimmed.slice(0, max - 1).trimEnd() + '…'
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
