import { ListChecks } from 'lucide-react'

interface StepChecklistProgressProps {
  /** Total checklist entries of the step's checklist. A non-positive total hides the indicator. */
  total: number
  /** Entries checked off so far (clamped into [0, total]). */
  completed: number
}

/**
 * Compact checklist progress for a plan-step / subagent header: the checklist
 * icon, a mini bar (always green — checklist progress is a success metric, not
 * a pressure metric like context fill) and an "N/M" tally. The tooltip reads
 * "Checklist: N of M".
 *
 * The step owns exactly one checklist per level (updates supersede their
 * predecessor — see handleStepTodoUpdate), so the counts arrive already
 * resolved; this component stays a pure presentational view like
 * ContextFillStatus and hides itself while there is nothing to count.
 */
export function StepChecklistProgress({ total, completed }: StepChecklistProgressProps) {
  if (!Number.isFinite(total) || total <= 0) return null
  const done = Math.max(0, Math.min(total, completed))
  const p = Math.round((done / total) * 100)
  const label = `Checklist: ${done} of ${total}`

  return (
    <div className="flex shrink-0 items-center gap-1.5 text-xs" title={label} aria-label={label}>
      <ListChecks className="h-3.5 w-3.5 shrink-0 text-success" />
      <span className="relative h-1.5 w-12 overflow-hidden rounded-full bg-muted">
        <span
          className="absolute inset-y-0 left-0 rounded-full bg-success"
          style={{ width: `${p}%` }}
        />
      </span>
      <span className="shrink-0 tabular-nums text-muted-foreground">{done}/{total}</span>
    </div>
  )
}
