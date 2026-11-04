// Specs panel header (ADR-105): the integrity badge — the tier-1
// deterministic-findings signal computed per parse — plus the aggregate
// counts. Pure presentational over SpecsStatus.

import { AlertTriangle, CheckCircle2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SpecsStatus } from '@/types/models'

/** The integrity badge: 0 findings → a success "Integrity OK" chip; any
 *  finding → a warning BUTTON naming the count — clicking it dispatches
 *  the fix-findings task into the chat (Shift = a new session, ADR-108),
 *  so "N findings" is the one affordance that acts on what it reports. */
function IntegrityBadge({
  findings,
  onFixFindings,
}: {
  findings: number
  onFixFindings?: (newSession: boolean) => void
}) {
  if (findings === 0) {
    return (
      <span
        data-testid="specs-integrity-badge"
        className="inline-flex items-center gap-1 rounded bg-success/10 px-1.5 py-0.5 text-xs text-success"
      >
        <CheckCircle2 className="size-3" />
        Integrity OK
      </span>
    )
  }
  return (
    <button
      type="button"
      data-testid="specs-integrity-badge"
      onClick={(e) => onFixFindings?.(e.shiftKey)}
      disabled={onFixFindings === undefined}
      title={
        onFixFindings !== undefined
          ? `Fix all ${findings} integrity findings in the chat — Shift = new session`
          : undefined
      }
      className={cn(
        'inline-flex items-center gap-1 rounded bg-warning/10 px-1.5 py-0.5 text-xs text-warning',
        'transition-colors hover:bg-warning/20 disabled:pointer-events-none',
      )}
    >
      <AlertTriangle className="size-3" />
      {findings} {findings === 1 ? 'finding' : 'findings'}
    </button>
  )
}

/** The header row: the integrity badge (the findings count doubles as the
 *  fix-findings dispatch, ADR-108) and the aggregate counts. The absolute
 *  specs root rides along as the row's tooltip. */
export function SpecsHeader({
  status,
  onFixFindings,
}: {
  status: SpecsStatus
  onFixFindings?: (newSession: boolean) => void
}) {
  const { counts, root } = status
  return (
    <div
      data-testid="specs-header"
      title={root}
      className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/30 px-2 py-1 min-h-[32px]"
    >
      <IntegrityBadge findings={counts.findings} onFixFindings={onFixFindings} />
      <span className={cn('text-xs text-muted-foreground truncate')}>
        {counts.files} spec files · {counts.adrs} ADRs
      </span>
    </div>
  )
}
