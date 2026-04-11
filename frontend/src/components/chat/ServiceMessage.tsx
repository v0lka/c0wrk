import { GitBranch, RotateCcw, Activity } from 'lucide-react'
import React from 'react'
import { domainLabels, complexityStars } from '@/constants/routingLabels'

interface ServiceMessageProps {
  id: string
  variant: 'routing' | 'retry' | 'step_retry' | 'status'
  content: string
  metadata?: Record<string, unknown>
}

const variantConfig = {
  routing: {
    icon: GitBranch,
    label: 'Routing',
  },
  retry: {
    icon: RotateCcw,
    label: 'Retry',
  },
  step_retry: {
    icon: RotateCcw,
    label: 'Step Retry',
  },
  status: {
    icon: Activity,
    label: 'Status',
  },
}

function formatRoutingContent(metadata?: Record<string, unknown>): React.ReactNode {
  if (!metadata) return null

  const domain = typeof metadata.domain === 'string' ? metadata.domain : ''
  const complexity = typeof metadata.complexity === 'string' ? metadata.complexity : typeof metadata.complexity === 'number' ? String(metadata.complexity) : ''

  const domainDisplay = domainLabels[domain] || domain || 'Unknown'
  const complexityDisplay = complexityStars[complexity] || complexity || '☆☆☆☆☆'

  return (
    <>
      <span className="text-muted-foreground">Domain:</span>{' '}
      <span className="text-foreground/80">{domainDisplay}</span>
      <span className="text-muted-foreground"> | </span>
      <span className="text-muted-foreground">Complexity:</span>{' '}
      <span className="text-foreground/80">{complexityDisplay}</span>
    </>
  )
}

export function ServiceMessage({ variant, content, metadata }: ServiceMessageProps) {
  const config = variantConfig[variant]
  const Icon = config.icon

  // For routing messages, render human-readable format
  const isRoutingWithMetadata = variant === 'routing' && metadata?.domain && metadata?.complexity

  return (
    <div className="flex items-center gap-1.5 text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
      <span className="text-xs">
        {isRoutingWithMetadata ? formatRoutingContent(metadata) : content}
      </span>
    </div>
  )
}
