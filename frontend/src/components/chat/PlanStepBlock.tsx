import { useState, useEffect } from 'react'
import { Loader2, CheckCircle2, XCircle, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { formatDuration } from '@/lib/formatters'
import type { DisplayItem } from '@/stores/chatStore'

interface PlanStepBlockProps {
  stepId: string
  stepNum: number
  title: string
  status: 'running' | 'completed' | 'failed'
  duration?: number
  isRetry?: boolean
  children: DisplayItem[]
  renderItem: (item: DisplayItem, index: number) => React.ReactNode
}

export function PlanStepBlock({ stepId, stepNum, title, status, duration, isRetry, children, renderItem }: PlanStepBlockProps) {
  const [isOpen, setIsOpen] = useState(status === 'running')

  // Auto-collapse when status transitions to completed/failed
  useEffect(() => {
    if (status === 'completed' || status === 'failed') {
      setIsOpen(false)
    } else if (status === 'running') {
      setIsOpen(true)
    }
  }, [status])

  const borderColor = status === 'running'
    ? 'border-blue-500'
    : status === 'completed'
      ? 'border-emerald-500'
      : 'border-red-500'

  const StatusIcon = status === 'running' ? Loader2 : status === 'completed' ? CheckCircle2 : XCircle
  const iconClass = status === 'running'
    ? 'text-blue-500 animate-spin'
    : status === 'completed'
      ? 'text-emerald-500'
      : 'text-red-500'

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} data-step-id={stepId}>
      <CollapsibleTrigger className="flex items-center gap-1.5 w-full text-muted-foreground hover:text-foreground transition-colors">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0" />
        )}
        <StatusIcon className={`h-3.5 w-3.5 shrink-0 ${iconClass}`} />
        <span className="text-sm truncate">Step {stepNum}: {title}{isRetry ? ' (retry)' : ''}</span>
        {duration !== undefined && (
          <span className="ml-auto text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded shrink-0">
            {formatDuration(duration)}
          </span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className={`mt-2 border-l-2 ${borderColor} rounded pl-3 py-2 space-y-3 min-w-0`}>
          {children.map((child, idx) => renderItem(child, idx))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
