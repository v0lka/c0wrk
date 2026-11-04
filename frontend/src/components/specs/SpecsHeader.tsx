// Specs panel header (ADR-105): the integrity badge — the tier-1
// deterministic-findings signal computed per parse — plus the aggregate
// counts. Pure presentational over SpecsStatus.

import { AlertTriangle, BookOpen, CheckCircle2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SpecsStatus } from '@/types/models'

/** The integrity badge: 0 findings → a success "Integrity OK" chip; any
 *  finding → a warning chip naming the count (the findings list below
 *  carries the detail). */
function IntegrityBadge({ findings }: { findings: number }) {
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
    <span
      data-testid="specs-integrity-badge"
      className="inline-flex items-center gap-1 rounded bg-warning/10 px-1.5 py-0.5 text-xs text-warning"
    >
      <AlertTriangle className="size-3" />
      {findings} {findings === 1 ? 'finding' : 'findings'}
    </span>
  )
}

/** The header row: book icon, the integrity badge, and the aggregate
 *  counts. The absolute specs root rides along as the row's tooltip. */
export function SpecsHeader({ status }: { status: SpecsStatus }) {
  const { counts, root } = status
  return (
    <div
      data-testid="specs-header"
      title={root}
      className="flex shrink-0 items-center gap-2 border-b border-border bg-secondary/30 px-2 py-1 min-h-[32px]"
    >
      <BookOpen className="size-3.5 shrink-0 text-info" />
      <IntegrityBadge findings={counts.findings} />
      <span className={cn('text-xs text-muted-foreground truncate')}>
        {counts.files} spec files · {counts.adrs} ADRs
      </span>
    </div>
  )
}
