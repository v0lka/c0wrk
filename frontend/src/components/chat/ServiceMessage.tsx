import { GitBranch, RotateCcw, ArrowUpRight, ListChecks } from 'lucide-react'
import React from 'react'

interface ServiceMessageProps {
  id: string
  variant: 'routing' | 'retry' | 'escalation' | 'ac_extracted'
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
  escalation: {
    icon: ArrowUpRight,
    label: 'Escalation',
  },
  ac_extracted: {
    icon: ListChecks,
    label: 'AC Extracted',
  },
}

// Human-readable value mappings for routing messages
const domainLabels: Record<string, string> = {
  general: 'General',
  code: 'Code',
  research: 'Research',
  mixed: 'Mixed',
}

const modeLabels: Record<string, string> = {
  direct: 'Direct',
  react: 'ReAct',
  plan_execute: 'Plan&Execute',
}

const complexityStars: Record<string, string> = {
  '1': '★☆☆☆☆',
  '2': '★★☆☆☆',
  '3': '★★★☆☆',
  '4': '★★★★☆',
  '5': '★★★★★',
}

function formatRoutingContent(metadata?: Record<string, unknown>): React.ReactNode {
  if (!metadata) return null
  
  const mode = String(metadata.mode || '')
  const domain = String(metadata.domain || '')
  const complexity = String(metadata.complexity || '')
  
  const domainDisplay = domainLabels[domain] || domain || 'Unknown'
  const modeDisplay = modeLabels[mode] || mode || 'Unknown'
  const complexityDisplay = complexityStars[complexity] || complexity || '☆☆☆☆☆'
  
  return (
    <>
      <span className="text-muted-foreground">Domain:</span>{' '}
      <span className="text-foreground/80">{domainDisplay}</span>
      <span className="text-muted-foreground"> | </span>
      <span className="text-muted-foreground">Mode:</span>{' '}
      <span className="text-foreground/80">{modeDisplay}</span>
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
  const isRoutingWithMetadata = variant === 'routing' && metadata?.mode && metadata?.domain && metadata?.complexity

  return (
    <div className="flex items-center gap-1.5 text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
      <span className="text-xs">
        {isRoutingWithMetadata ? formatRoutingContent(metadata) : content}
      </span>
    </div>
  )
}
