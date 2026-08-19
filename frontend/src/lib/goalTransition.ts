// Pure goal-status helpers shared by the live goal event handlers
// (hooks/events/goalHandlers.ts) and the history-rebuild path
// (lib/chatUtils.ts). Keeping them here avoids an awkward dependency from the
// pure history/rendering layer onto a hooks module.

import type { GoalStatusData } from '@/types/events'
import type { ActiveGoal } from '@/stores/goalStore'

/** The subset of a prior ActiveGoal that a status snapshot must carry forward:
 *  the verify clause (never echoed by goal_status) and the verification mode
 *  (absent on older backend snapshots). Callers pass a previously-confirmed
 *  ActiveGoal (live path) or the fields recovered from the persisted proposal
 *  (history-rebuild path). */
export type GoalCarryOver = Pick<ActiveGoal, 'verify' | 'verificationMode'>

/** Map a goal_status snapshot to the store's ActiveGoal shape.
 *
 *  Mirrors the live handler's mapping. `prev` optionally carries a
 *  previously-confirmed `verify` clause and/or `verificationMode` to preserve
 *  across snapshots (the status event does not always echo them back).
 */
export function goalStatusToActiveGoal(data: GoalStatusData, prev?: GoalCarryOver): ActiveGoal {
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
    createdAt: data.created_at,
  }
  if (prev?.verify !== undefined) goal.verify = prev.verify
  // Thread the verification mode: prefer the snapshot's value, else preserve a
  // previously-seen one (e.g. from the approval), else leave unset (the default
  // 'executable' is implied by absence).
  if (data.verification_mode) {
    goal.verificationMode = data.verification_mode
  } else if (prev?.verificationMode) {
    goal.verificationMode = prev.verificationMode
  }
  return goal
}

/** buildGoalTransitionNotice returns a human-readable, one-line summary of a
 *  goal_status snapshot's turn transition, or null when the snapshot carries no
 *  user-relevant transition (e.g. a bare mid-loop active status without a
 *  verdict or verification outcome). */
export function buildGoalTransitionNotice(data: GoalStatusData): string | null {
  // max_turns <= 0 (or unset) means an unlimited turn budget (see
  // goal.GoalBudget.MaxTurns contract: 0 = unlimited). Render the ∞ glyph so a
  // met goal reads "turn 1/∞" rather than the misleading "turn 1/0". Mirrors
  // GoalStatusIndicator.tsx and BudgetCombobox.tsx.
  const budgetLabel = data.max_turns > 0 ? String(data.max_turns) : '∞'
  const turnLabel = `turn ${data.turn}/${budgetLabel}`

  // Terminal outcomes take precedence over the retry branches: a terminal
  // snapshot can still carry a trailing "rejected" verification marker or a
  // "not_met" verdict from the final turn (e.g. a rejected met claim that then
  // exhausted the budget). Rendering that as "retrying" after the loop has
  // already ended would misreport the outcome.
  switch (data.status) {
    case 'met':
      return `Goal met (${turnLabel})`
    case 'exhausted':
      return `Goal not reached — turn budget exhausted (${turnLabel})`
    case 'blocked_idle':
      return `Goal blocked — agent could not make progress (${turnLabel})`
    default:
      break
  }

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
  return null
}

/** truncate clamps a string to `max` chars, appending an ellipsis when cut. */
function truncate(s: string, max: number): string {
  const trimmed = s.trim().replace(/\s+/g, ' ')
  if (trimmed.length <= max) return trimmed
  return trimmed.slice(0, max - 1).trimEnd() + '…'
}
