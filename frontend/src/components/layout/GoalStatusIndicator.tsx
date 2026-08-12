import { Target } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { useSessionStore } from '@/stores/sessionStore'
import { useActiveGoal, useGoalStatus } from '@/stores/goalStore'

// The indicator is only relevant while a goal loop is running. A cooperative
// pause leaves the goal `active` (resume re-enters), so only `active` keeps it
// visible; `idle` (no goal), `met` (goal satisfied) and any other terminal
// state hide it.
//
// This is a read-only status badge (icon, turn counter, turn budget). The
// Pause/Resume/Stop controls are session-level and live in the ChatInputToolbar
// — a goal pause is a task-level pause now, not a goal-specific action.
const VISIBLE_STATUSES = new Set(['active'])

/**
 * Persistent goal status badge shown in the status bar's left cluster. Reflects
 * the active session's committed goal lifecycle as a compact summary (icon,
 * turn counter, turn budget). Hidden when there is no active session or the
 * goal status is anything other than `active` (covers `idle`, `met`,
 * `exhausted`, `blocked_idle`).
 */
export function GoalStatusIndicator() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const status = useGoalStatus(activeSessionId) ?? 'idle'
  const activeGoal = useActiveGoal(activeSessionId)

  if (!activeSessionId || !VISIBLE_STATUSES.has(status)) return null

  const turn = activeGoal?.turn ?? 0
  // maxTurns <= 0 (or unset) means an unlimited turn budget.
  const budget = activeGoal?.maxTurns && activeGoal.maxTurns > 0 ? activeGoal.maxTurns : '∞'
  const title = activeGoal?.condition ? `Goal: ${activeGoal.condition}` : 'Goal'

  return (
    <Badge
      variant="outline"
      className="h-5 shrink-0 items-center gap-1 px-1.5 text-[10px]"
      title={title}
    >
      <Target className="size-3 shrink-0" />
      <span className="shrink-0">Goal</span>
      <span className="shrink-0 text-muted-foreground/70">· turn {turn}</span>
      <span className="shrink-0 text-muted-foreground/70">· budget {budget}</span>
    </Badge>
  )
}
