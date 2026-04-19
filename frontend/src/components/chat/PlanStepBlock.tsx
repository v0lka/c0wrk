import { useState } from 'react'
import { Loader2, CheckCircle2, XCircle, ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from '@/components/ui/tooltip'
import { formatDuration } from '@/lib/formatters'
import { useChatStore, type DisplayItem } from '@/stores/chatStore'

interface PlanStepBlockProps {
  stepId: string
  stepNum: number
  title: string
  description?: string
  status: 'running' | 'completed' | 'failed'
  duration?: number
  error?: string
  isRetry?: boolean
  children: DisplayItem[]
  renderItem: (item: DisplayItem, index: number) => React.ReactNode
}

function getStatusColor(status: string): string {
  switch (status) {
    case 'ok':
      return 'text-muted-foreground'
    case 'compact':
      return 'text-foreground'
    case 'warning':
      return 'text-amber-500'
    case 'emergency':
    case 'reject':
      return 'text-red-500'
    default:
      return 'text-muted-foreground'
  }
}

export function PlanStepBlock({ stepId, stepNum, title, description, status, duration, error, children, renderItem }: PlanStepBlockProps) {
  const [isOpen, setIsOpen] = useState(status === 'running')
  const stepContextFill = useChatStore(s => s.stepContextFill[stepId])
  const fullDesc = description || title
  const hasTooltip = !!fullDesc && fullDesc.length > 80

  // Adjust isOpen during render when status changes (avoids extra render cycle from useEffect)
  const [prevStatus, setPrevStatus] = useState(status)
  if (status !== prevStatus) {
    setPrevStatus(status)
    if (status === 'completed' || status === 'failed') {
      setIsOpen(false)
    } else if (status === 'running') {
      setIsOpen(true)
    }
  }

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
    <TooltipProvider delayDuration={400}>
      <Collapsible open={isOpen} onOpenChange={setIsOpen} data-step-id={stepId} className="group">
        <CollapsibleTrigger className="flex items-center gap-1.5 w-full text-muted-foreground hover:text-foreground transition-colors">
          <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
            {isOpen ? (
              <ChevronDown className="h-3.5 w-3.5 shrink-0" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 shrink-0" />
            )}
          </span>
          <StatusIcon className={`h-3.5 w-3.5 shrink-0 ${iconClass}`} />
          {hasTooltip ? (
            <Tooltip delayDuration={400}>
              <TooltipTrigger asChild>
                <span className="text-sm truncate">{`Step ${stepNum}: ${title}`}</span>
              </TooltipTrigger>
              <TooltipContent side="bottom" align="start" className="max-w-md text-left whitespace-pre-line p-3 bg-background text-foreground border border-border shadow-md">
                {fullDesc}
              </TooltipContent>
            </Tooltip>
          ) : (
            <span className="text-sm truncate">{`Step ${stepNum}: ${title}`}</span>
          )}
          {status === 'failed' && error && (
            <span className="text-xs text-red-400 truncate max-w-[300px]" title={error}>
              — {error.length > 150 ? error.slice(0, 150) + '…' : error}
            </span>
          )}
          {status === 'running' && stepContextFill && (
            <span className={`text-xs ml-2 ${getStatusColor(stepContextFill.status)}`}>
              {Math.round(stepContextFill.fillPercent)}%
            </span>
          )}
          {duration !== undefined && (
            <span className="ml-auto text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded shrink-0">
              {formatDuration(duration)}
            </span>
          )}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className={`mt-2 border-l-2 ${borderColor} rounded pl-3 py-2 space-y-3 min-w-0`}>
            {status === 'running' && stepContextFill && (
              <div className="text-xs text-muted-foreground flex items-center gap-2">
                <span>Context:</span>
                <span className={getStatusColor(stepContextFill.status)}>
                  {Math.round(stepContextFill.fillPercent)}%
                </span>
                <span className="text-muted-foreground/60">
                  ({stepContextFill.usedTokens.toLocaleString()} / {stepContextFill.maxTokens.toLocaleString()} tokens)
                </span>
              </div>
            )}
            {children.map((child, idx) => renderItem(child, idx))}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </TooltipProvider>
  )
}
