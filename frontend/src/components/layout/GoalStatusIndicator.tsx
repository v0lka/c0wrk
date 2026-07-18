import { useCallback } from 'react'
import { Pause, Play, X, Target } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { goal as goalApi } from '@/api'
import { logger } from '@/lib/logger'
import { useSessionStore } from '@/stores/sessionStore'
import { useActiveGoal, useGoalStatus, useGoalStore } from '@/stores/goalStore'

// The indicator is only relevant while a goal loop is running or suspended.
// `idle` (no goal) and `met` (goal satisfied) — plus any terminal state — hide it.
const VISIBLE_STATUSES = new Set(['active', 'paused'])

/**
 * Persistent goal status badge with inline Pause/Resume/Clear controls, shown
 * in the status bar's left cluster. Reflects the active session's committed
 * goal lifecycle: renders a compact summary (icon, turn counter, turn budget)
 * plus context-sensitive action buttons.
 *
 * - `paused` → Resume button (Play); `active` → Pause button (Pause).
 * - Clear is always available while visible.
 *
 * Hidden when there is no active session or the goal status is anything other
 * than `active`/`paused` (covers `idle`, `met`, `exhausted`, `blocked_idle`).
 *
 * Controls fire the matching `goal.*` RPC and apply an optimistic local store
 * update so the UI reflects immediately; the backend's `goal_status` event
 * reconciles authoritatively. A failed RPC rolls the optimistic update back.
 */
export function GoalStatusIndicator() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const status = useGoalStatus(activeSessionId) ?? 'idle'
  const activeGoal = useActiveGoal(activeSessionId)

  // Imperative store actions (stable function refs) for optimistic updates.
  const setGoalStatus = useGoalStore((s) => s.setGoalStatus)
  const clearGoalState = useGoalStore((s) => s.clearGoal)

  const handlePause = useCallback(() => {
    if (!activeSessionId) return
    setGoalStatus(activeSessionId, 'paused')
    goalApi.pauseGoal(activeSessionId).catch((err) => {
      logger.error('GoalStatusIndicator: pauseGoal failed, rolling back', err)
      setGoalStatus(activeSessionId, 'active')
    })
  }, [activeSessionId, setGoalStatus])

  const handleResume = useCallback(() => {
    if (!activeSessionId) return
    setGoalStatus(activeSessionId, 'active')
    goalApi.resumeGoal(activeSessionId).catch((err) => {
      logger.error('GoalStatusIndicator: resumeGoal failed, rolling back', err)
      setGoalStatus(activeSessionId, 'paused')
    })
  }, [activeSessionId, setGoalStatus])

  const handleClear = useCallback(() => {
    if (!activeSessionId) return
    clearGoalState(activeSessionId)
    goalApi.clearGoal(activeSessionId).catch((err) => {
      logger.error('GoalStatusIndicator: clearGoal failed', err)
    })
  }, [activeSessionId, clearGoalState])

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
      {status === 'paused' ? (
        <Button
          variant="ghost"
          size="icon-xs"
          className="size-5"
          onClick={handleResume}
          aria-label="Resume goal"
          title="Resume goal"
        >
          <Play className="size-3" />
        </Button>
      ) : (
        <Button
          variant="ghost"
          size="icon-xs"
          className="size-5"
          onClick={handlePause}
          aria-label="Pause goal"
          title="Pause goal"
        >
          <Pause className="size-3" />
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-xs"
        className="size-5"
        onClick={handleClear}
        aria-label="Clear goal"
        title="Clear goal"
      >
        <X className="size-3" />
      </Button>
    </Badge>
  )
}
