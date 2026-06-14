import { GitBranch, RotateCcw, Activity } from 'lucide-react'
import type { ReactNode } from 'react'
import type { DisplayItem } from '@/types/messages'
import { domainLabels, complexityStars } from '@/constants/routingLabels'

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

export function ServiceMessage({ item }: ServiceMessageProps) {
  const Icon = variantConfig[item.variant].icon
  const isRouting = item.variant === 'routing' && item.metadata?.domain && item.metadata?.complexity

  return (
    <div className="flex items-center gap-1.5 text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
      <span className="text-xs">
        {isRouting ? formatRoutingContent(item.metadata) : item.content}
      </span>
    </div>
  )
}
