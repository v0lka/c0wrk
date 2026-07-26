import { useState, useEffect, useMemo } from 'react'
import { Loader2, CheckCircle2, XCircle, RefreshCw, Circle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatDuration } from '@/lib/formatters'
import { useChatStore } from '@/stores/chatStore'
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

  // Derived open state — auto-opens when running, auto-closes otherwise
  const isAutoOpen = status === 'running'
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? isAutoOpen

  // Reset user override when status changes
  useEffect(() => { setUserOverride(null) }, [status])

  const statusConfig = {
    running:   { border: 'border-info',         Icon: Loader2,      iconClass: 'text-info animate-spin' },
    completed: { border: 'border-success',      Icon: CheckCircle2, iconClass: 'text-success' },
    failed:    { border: 'border-destructive',  Icon: XCircle,      iconClass: 'text-destructive' },
    pending:   { border: 'border-border',       Icon: Circle,       iconClass: 'text-muted-foreground' },
  } as const

  const cfg = statusConfig[status] ?? statusConfig.pending
  const borderColor = cfg.border
  const StatusIcon = cfg.Icon
  const iconClass = cfg.iconClass

  const fullDesc = description || title

  const headerExtra = useMemo(() => (
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
  ), [isRetry, status, error, stepContextFill, duration])

  const icon = useMemo(() => (
    <StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />
  ), [StatusIcon, iconClass])

  const label = useMemo(() => (
    <StepTooltip description={fullDesc} enabled={!!description}>
      <span className={cn('text-sm min-w-0 truncate', description && 'cursor-default')}>
        Step {stepNum}: {title}
      </span>
    </StepTooltip>
  ), [fullDesc, description, stepNum, title])

  return (
    <div data-step-id={stepId}>
      <CollapsibleBlock
        icon={icon}
        label={label}
        open={isOpen}
        onOpenChange={(open) => setUserOverride(open)}
        headerExtra={headerExtra}
      >
        <div className={cn('mt-2 border-l-2 rounded pl-3 py-2 space-y-3 min-w-0', borderColor)}>
          <ChatMessageRenderer items={children} />
        </div>
      </CollapsibleBlock>
    </div>
  )
}
