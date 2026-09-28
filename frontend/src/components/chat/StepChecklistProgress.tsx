import { ListChecks } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * The status accent the indicator follows: the current color of the owning
 * block's status icon (blue spinner / green check / red cross / amber pause /
 * muted idle). Static class records keep the classes Tailwind-JIT-visible —
 * never interpolate them.
 */
export type StepStatusAccent = 'info' | 'success' | 'destructive' | 'warning' | 'muted'

const ACCENT_TEXT: Record<StepStatusAccent, string> = {
  info: 'text-info',
  success: 'text-success',
  destructive: 'text-destructive',
  warning: 'text-warning',
  muted: 'text-muted-foreground',
}

const ACCENT_BAR: Record<StepStatusAccent, string> = {
  info: 'bg-info',
  success: 'bg-success',
  destructive: 'bg-destructive',
  warning: 'bg-warning',
  muted: 'bg-muted-foreground',
}

interface StepChecklistProgressProps {
  /** Total checklist entries of the step's checklist. A non-positive total hides the indicator. */
  total: number
  /** Entries checked off so far (clamped into [0, total]). */
  completed: number
  /** Status accent of the owning block — tints the icon and the bar fill. */
  accent: StepStatusAccent
}

/**
 * Compact checklist progress for a plan-step / subagent header: the checklist
 * icon, a mini bar and an "N/M" tally, all tinted with the owning block's
 * current status accent (the same color as its status icon — running blue,
 * completed green, failed red, paused amber, idle muted). The tooltip reads
 * "Checklist: N of M".
 *
 * The step owns exactly one checklist per level (updates supersede their
 * predecessor — see handleStepTodoUpdate), so the counts arrive already
 * resolved; this component stays a pure presentational view like
 * ContextFillStatus and hides itself while there is nothing to count.
 */
export function StepChecklistProgress({ total, completed, accent }: StepChecklistProgressProps) {
  if (!Number.isFinite(total) || total <= 0) return null
  const done = Math.max(0, Math.min(total, completed))
  const p = Math.round((done / total) * 100)
  const label = `Checklist: ${done} of ${total}`

  return (
    <div
      className="flex shrink-0 items-center gap-1.5 text-xs"
      role="progressbar"
      aria-valuenow={done}
      aria-valuemin={0}
      aria-valuemax={total}
      title={label}
      aria-label={label}
    >
      <ListChecks className={cn('h-3.5 w-3.5 shrink-0', ACCENT_TEXT[accent])} />
      <span className="relative h-1.5 w-12 overflow-hidden rounded-full bg-muted">
        <span
          className={cn('absolute inset-y-0 left-0 rounded-full', ACCENT_BAR[accent])}
          style={{ width: `${p}%` }}
        />
      </span>
      <span className="shrink-0 tabular-nums text-muted-foreground">{done}/{total}</span>
    </div>
  )
}
