import { GitBranch, RotateCcw, Activity } from 'lucide-react'
import { memo, type ReactNode } from 'react'
import type { DisplayItem } from '@/types/messages'
import { areDisplayItemsEqual } from '@/lib/displayItemStability'
import { domainLabels, complexityStars } from '@/constants/routingLabels'
import { isAutonomyDecisionData } from '@/types/events'
import { cn } from '@/lib/utils'

type ServiceItem = Extract<DisplayItem, { kind: 'service' }>

interface ServiceMessageProps {
  item: ServiceItem
}

const variantConfig = {
  routing: { icon: GitBranch },
  retry: { icon: RotateCcw },
  step_retry: { icon: RotateCcw },
  status: { icon: Activity },
} as const

/** Autonomy-decision kinds (mirrors the Go AutonomyDecision payload contract). */
const autonomyKinds = new Set(['tool_confirm', 'assisted_deny', 'step_limit'])

/**
 * Icon tone for an automatic autonomy-decision notice (silent/assisted mode).
 * The verdict decides the color so the audit receipt is scannable at a glance:
 * an ALLOW-family verdict paints the glyph success green, a DENY paints it
 * destructive red. Non-autonomy rows (and unknown verdicts) keep the muted
 * default. The payload rides in metadata on both the live and reloaded rows.
 */
function autonomyIconTone(metadata: Record<string, unknown> | undefined): string | undefined {
  if (metadata === undefined || !isAutonomyDecisionData(metadata)) return undefined
  if (!autonomyKinds.has(metadata.kind)) return undefined
  const verdict = metadata.verdict
  if (verdict.startsWith('allow')) return 'text-success'
  if (verdict === 'deny') return 'text-destructive'
  return undefined
}

function formatRoutingContent(metadata?: Record<string, unknown>): ReactNode {
  if (!metadata) return null
  const domain = typeof metadata.domain === 'string' ? metadata.domain : ''
  const complexity = typeof metadata.complexity === 'string'
    ? metadata.complexity
    : typeof metadata.complexity === 'number' ? String(metadata.complexity) : ''

  return (
    <>
      <span className="text-muted-foreground">{'Domain:'}</span>{' '}
      <span className="text-foreground/80">{domainLabels[domain] || domain || 'Unknown'}</span>
      <span className="text-muted-foreground">{' | Complexity: '}</span>
      <span className="text-foreground/80">{complexityStars[complexity] || '☆☆☆☆☆'}</span>
    </>
  )
}

export const ServiceMessage = memo(function ServiceMessage({ item }: ServiceMessageProps) {
  const Icon = variantConfig[item.variant].icon
  const isRouting = item.variant === 'routing' && item.metadata?.domain && item.metadata?.complexity
  const iconTone = autonomyIconTone(item.metadata)

  return (
    <div className="flex items-center gap-1.5 text-muted-foreground">
      <Icon className={cn('h-3.5 w-3.5 shrink-0', iconTone)} />
      <span className="text-xs">
        {isRouting ? formatRoutingContent(item.metadata) : item.content}
      </span>
    </div>
  )
}, (prev, next) => prev.item === next.item || areDisplayItemsEqual(prev.item, next.item))
