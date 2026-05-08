import { useState, useEffect } from 'react'
import { Loader2, CheckCircle2, XCircle, RefreshCw, Square, CheckSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatDuration } from '@/lib/formatters'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { StepTooltip } from './StepTooltip'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import type { DisplayItem } from '@/types/messages'

type PlanStepItem = Extract<DisplayItem, { kind: 'plan_step' }>

interface PlanStepBlockProps {
  item: PlanStepItem
}

export function PlanStepBlock({ item }: PlanStepBlockProps) {
  const { stepId, stepNum, title, description, status, duration, error, isRetry, children } = item
  const stepContextFill = useChatStore(s => s.stepContextFill[stepId])
  const todoItems = usePlanStore(s => {
    const latest = s.planGroups[0]
    if (!latest) return undefined
    const planItem = latest.items.find(it => it.id === stepId)
    return planItem?.todoItems
  })

  // Derived open state — auto-opens when running, auto-closes otherwise
  const isAutoOpen = status === 'running'
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? isAutoOpen

  // Reset user override when status changes
  useEffect(() => { setUserOverride(null) }, [status])

  const borderColor = status === 'running' ? 'border-info'
    : status === 'completed' ? 'border-success'
      : 'border-destructive'

  const StatusIcon = status === 'running' ? Loader2 : status === 'completed' ? CheckCircle2 : XCircle
  const iconClass = status === 'running' ? 'text-info animate-spin'
    : status === 'completed' ? 'text-success' : 'text-destructive'

  const fullDesc = description || title

  const headerExtra = (
    <>
      {isRetry && <RefreshCw className="h-3 w-3 text-warning" />}
      {status === 'failed' && error && (
        <span className="text-xs text-destructive truncate min-w-0" title={error}>— {error}</span>
      )}
      {typeof stepContextFill === 'number' && (
        <span className="text-xs text-muted-foreground ml-2">{Math.round(stepContextFill)}%</span>
      )}
      {duration !== undefined && (
        <span className="ml-auto text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded shrink-0">
          {formatDuration(duration)}
        </span>
      )}
    </>
  )

  return (
    <div data-step-id={stepId}>
      <CollapsibleBlock
        icon={<StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />}
        label={
          <StepTooltip description={fullDesc} enabled={!!description}>
            <span className={cn('text-sm truncate', description && 'cursor-default')}>
              Step {stepNum}: {title}
            </span>
          </StepTooltip>
        }
        open={isOpen}
        onOpenChange={(open) => setUserOverride(open)}
        headerExtra={headerExtra}
      >
        <div className={cn('mt-2 border-l-2 rounded pl-3 py-2 space-y-3 min-w-0', borderColor)}>
          {todoItems && todoItems.length > 0 && (
            <ul className="space-y-1">
              {todoItems.map((todo, i) => (
                <li key={i} className="flex items-center gap-2 text-xs text-muted-foreground">
                  {todo.checked ? (
                    <CheckSquare className="h-3.5 w-3.5 text-success shrink-0" />
                  ) : (
                    <Square className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                  )}
                  <span className={cn(todo.checked && 'line-through opacity-60')}>{todo.text}</span>
                </li>
              ))}
            </ul>
          )}
          <ChatMessageRenderer items={children} />
        </div>
      </CollapsibleBlock>
    </div>
  )
}
