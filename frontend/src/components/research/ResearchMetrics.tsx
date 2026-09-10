import { useMemo } from 'react'
import { CheckCircle2, GitFork, Layers, Activity } from 'lucide-react'
import type { ResearchMetrics } from '@/types/models'
import { formatRate } from './researchDagRender'

interface ResearchMetricsRowProps {
  metrics: ResearchMetrics
}

interface Metric {
  label: string
  value: string
  Icon: typeof CheckCircle2
  tone: string
}

/**
 * Compact metrics row: confirmation rate, hypothesis budget (total),
 * structural depth, and the size of the active research front (the count of
 * unfinished hypotheses — the ids themselves live in the DAG/workspace).
 * Reads only the parsed metrics object — all formatting goes through the pure
 * `formatRate`.
 */
export function ResearchMetricsRow({ metrics }: ResearchMetricsRowProps) {
  const items = useMemo<Metric[]>(() => {
    const active = metrics.active_front ?? []
    return [
      {
        label: 'Confirmed',
        value: formatRate(metrics.confirmation_rate),
        Icon: CheckCircle2,
        tone: 'text-success',
      },
      {
        label: 'Hypotheses',
        value: String(metrics.total),
        Icon: Layers,
        tone: 'text-info',
      },
      {
        label: 'Depth',
        value: String(metrics.depth),
        Icon: GitFork,
        tone: 'text-warning',
      },
      {
        label: 'Active front',
        value: String(active.length),
        Icon: Activity,
        tone: 'text-primary',
      },
    ]
  }, [metrics])

  return (
    <div className="grid shrink-0 grid-cols-2 gap-px border-b border-border bg-border">
      {items.map(({ label, value, Icon, tone }) => (
        <div
          key={label}
          className="flex flex-col gap-0.5 bg-background px-3 py-2"
          title={`${label}: ${value}`}
        >
          <span className="flex items-center gap-1 text-[10px] uppercase tracking-wide text-muted-foreground">
            <Icon className={`size-3 ${tone}`} />
            {label}
          </span>
          <span className="truncate text-xs font-medium">{value}</span>
        </div>
      ))}
    </div>
  )
}
