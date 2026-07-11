import { cn } from '@/lib/utils'

interface ContextFillStatusProps {
  /** Conductor's context-window fill percent (0-100). Undefined hides the indicator. */
  percent: number | undefined
}

type FillTier = 'ok' | 'warn' | 'crit'

// Thresholds mirror the backend compaction tiers (warning / emergency) so the
// status bar colour matches the agent's internal context-pressure state.
function fillTier(p: number): FillTier {
  if (p >= 85) return 'crit'
  if (p >= 70) return 'warn'
  return 'ok'
}

const TIER_TEXT: Record<FillTier, string> = {
  ok: 'text-muted-foreground',
  warn: 'text-warning',
  crit: 'text-destructive',
}

const TIER_BAR: Record<FillTier, string> = {
  ok: 'bg-info',
  warn: 'bg-warning',
  crit: 'bg-destructive',
}

/**
 * Renders the conductor's context-window fill as a compact bar + percentage,
 * colour-coded by pressure tier. Reflects the session-root (conductor) fill
 * only — subagent fills never reach this indicator.
 */
export function ContextFillStatus({ percent }: ContextFillStatusProps) {
  if (percent == null || !Number.isFinite(percent)) return null
  const p = Math.max(0, Math.min(100, Math.round(percent)))
  const tier = fillTier(p)
  const label = `Context fill: ${p}%`

  return (
    <div className="flex shrink-0 items-center gap-1.5 text-xs" title={label} aria-label={label}>
      <span className="relative h-1.5 w-12 overflow-hidden rounded-full bg-muted">
        <span
          className={cn('absolute inset-y-0 left-0 rounded-full', TIER_BAR[tier])}
          style={{ width: `${p}%` }}
        />
      </span>
      <span className={cn('shrink-0 tabular-nums', TIER_TEXT[tier])}>{p}%</span>
    </div>
  )
}
