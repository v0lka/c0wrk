import { CheckCircle2, Circle, Loader2, XCircle, Clock, Square, CheckSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatDuration } from '@/lib/formatters'
import { usePlanStore } from '@/stores/planStore'
import { useScrollContext } from './ScrollContext'
import { StepTooltip } from './StepTooltip'
import type { PlanItem } from '@/types/models'

function StatusIcon({ status }: { status: PlanItem['status'] }) {
  switch (status) {
    case 'completed':
      return <CheckCircle2 className="h-3.5 w-3.5 text-success shrink-0" />
    case 'running':
      return <Loader2 className="h-3.5 w-3.5 text-info animate-spin shrink-0" />
    case 'pending':
      return <Circle className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
    case 'failed':
      return <XCircle className="h-3.5 w-3.5 text-destructive shrink-0" />
  }
}

function TodoChecklist({ items }: { items: PlanItem['todoItems'] }) {
  if (!items || items.length === 0) return null
  return (
    <ul className="space-y-0.5 ml-5.5">
      {items.map((todo, i) => (
        <li key={i} className="flex items-center gap-1.5 text-[10px] text-muted-foreground/70">
          {todo.checked ? (
            <CheckSquare className="h-3 w-3 text-success shrink-0" />
          ) : (
            <Square className="h-3 w-3 text-muted-foreground/50 shrink-0" />
          )}
          <span className={cn('truncate', todo.checked && 'line-through opacity-50')}>{todo.text}</span>
        </li>
      ))}
    </ul>
  )
}

function PlanStepItem({ item, onClick }: { item: PlanItem; onClick?: () => void }) {
  const hasDescription = !!item.description && item.description !== item.title
  const hasTodos = !!item.todoItems && item.todoItems.length > 0

  return (
    <div
      className={cn(
        'px-1 -mx-1 rounded transition-colors',
        onClick && 'hover:bg-muted/50 cursor-pointer',
      )}
      onClick={onClick}
    >
      <div className="flex items-center gap-2 h-[24px] w-full text-left">
        <StatusIcon status={item.status} />
        <StepTooltip description={item.description || item.title} enabled={hasDescription}>
          <span className="text-xs text-muted-foreground truncate min-w-0">{item.title}</span>
        </StepTooltip>
        {item.duration !== undefined && (item.status === 'completed' || item.status === 'failed') && (
          <span className="flex items-center gap-0.5 text-[10px] text-muted-foreground/60 ml-auto shrink-0">
            <Clock className="h-2.5 w-2.5" />
            {formatDuration(item.duration)}
          </span>
        )}
      </div>
      {hasTodos && <TodoChecklist items={item.todoItems} />}
    </div>
  )
}

export function PlanView() {
  const latestGroup = usePlanStore(s => s.planGroups[0] ?? null)
  const scrollToStep = useScrollContext().scrollToStep

  if (!latestGroup || latestGroup.items.length === 0) return null

  return (
    <div className="min-w-0">
      {latestGroup.items.map((item) => (
        <PlanStepItem
          key={item.id}
          item={item}
          onClick={scrollToStep ? () => scrollToStep(item.id) : undefined}
        />
      ))}
    </div>
  )
}
