import { BrainCircuit } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatTokenCount } from '@/lib/formatters'

interface ContextFillStatusProps {
  /** Conductor's context-window fill percent (0-100). Undefined hides the indicator. */
  percent: number | undefined
  /** Conductor's context-window used token count (session-root only, from session_tokens events). */
  usedTokens?: number
  /** Conductor's context-window total capacity (session-root only, from session_tokens events). */
  maxTokens?: number
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
 *
 * Hidden while no tokens are spent (0 used). The tooltip shows "Context fill:
 * N of M" (used of capacity) when the token counts are available, falling back
 * to a percentage otherwise. The leading icon mirrors the reasoning-block icon
 * used in the chat area.
 */
export function ContextFillStatus({ percent, usedTokens, maxTokens }: ContextFillStatusProps) {
  if (percent == null || !Number.isFinite(percent)) return null
  const p = Math.max(0, Math.min(100, Math.round(percent)))

  // Hide while no tokens are spent in the context window. When usedTokens is
  // known (active execution), 0 means genuinely empty; otherwise fall back to
  // the fill percent (0% ⟺ 0 tokens).
  const hasUsage = usedTokens != null ? usedTokens > 0 : percent > 0
  if (!hasUsage) return null

  const tier = fillTier(p)
  const label = maxTokens && maxTokens > 0
    ? `Context fill: ${formatTokenCount(usedTokens ?? Math.round((percent / 100) * maxTokens))} of ${formatTokenCount(maxTokens)}`
    : `Context fill: ${p}%`

  return (
    <div className="flex shrink-0 items-center gap-1.5 text-xs" title={label} aria-label={label}>
      <BrainCircuit className={cn('h-3.5 w-3.5 shrink-0', TIER_TEXT[tier])} />
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
