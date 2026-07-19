import { Target } from 'lucide-react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { cn } from '@/lib/utils'

/**
 * GoalToggle enables/disables goal mode for the next sent message. It is a
 * compact icon button in the chat toolbar that mirrors the visual treatment of
 * the chat/terminal mode toggles: muted when off, primary-highlighted when on.
 *
 * The toggle is always available — on both the first message of a task and on
 * continuations, where re-enabling it runs the goal loop on the inherited
 * blackboard of the prior task. The toggle state (`goalEnabled`) lives in
 * inputModeStore but is NOT persisted: goal mode is per-task opt-in, so
 * persisting the toggle would silently re-activate goal mode on every fresh
 * session. It resets to `false` on reload and after a successful send (see
 * useMessageSender), so the user explicitly opts in for each goal.
 * Selectors return direct store refs/primitives only — no allocations
 * (React 19 useSyncExternalStore #185 guard, AGENTS.md §2.7).
 */
export function GoalToggle() {
  const goalEnabled = useInputModeStore((s) => s.goalEnabled)
  const setGoalEnabled = useInputModeStore((s) => s.setGoalEnabled)

  return (
    <button
      type="button"
      onClick={() => setGoalEnabled(!goalEnabled)}
      className={cn(
        'flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors',
        goalEnabled && 'text-primary border-primary/50 bg-primary/10 hover:bg-primary/15',
      )}
      title={goalEnabled ? 'Goal mode on — click to turn off' : 'Goal mode off — click to turn on'}
      aria-pressed={goalEnabled}
      aria-label="Toggle goal mode"
    >
      <Target className="size-3.5" />
      <span>Goal</span>
    </button>
  )
}
