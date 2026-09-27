import { memo, useEffect, useMemo, useState, useContext } from 'react'
import { Loader2, CheckCircle2, XCircle, RefreshCw, Circle, CirclePause, CircleSlash } from 'lucide-react'
import { cn } from '@/lib/utils'
import { bookmarkKey } from '@/lib/bookmarks'
import { areDisplayItemsEqual } from '@/lib/displayItemStability'
import { formatDuration } from '@/lib/formatters'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { ContextFillStatus } from '@/components/layout/ContextFillStatus'
import { StepTooltip } from './StepTooltip'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { BookmarkableContext } from './BookmarkableContext'
import { StepChecklistProgress } from './StepChecklistProgress'
import type { DisplayItem } from '@/types/messages'

type PlanStepItem = Extract<DisplayItem, { kind: 'plan_step' }>
type ChecklistChild = Extract<DisplayItem, { kind: 'checklist' }>

interface PlanStepBlockProps {
  item: PlanStepItem
}

export const PlanStepBlock = memo(function PlanStepBlock({ item }: PlanStepBlockProps) {
  const { stepId, stepNum, title, description, status, duration, error, isRetry, children } = item
  // Plan groups are rebuilt per session switch, so the displayed steps always
  // belong to the active session. The nested lookup returns a primitive
  // (stable selector — no allocation).
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const bookmarkable = useContext(BookmarkableContext)
  const stepContextFill = useChatStore(s => {
    const fills = activeSessionId ? s.stepContextFill[activeSessionId] : undefined
    return fills ? fills[stepId] : undefined
  })
  // Per-step token totals from the same step-scoped context_fill events —
  // feeds the reusable ContextFillStatus indicator its "N of M" tooltip. The
  // nested lookup returns the stored totals object (stable reference across
  // unrelated writes — stable selector, no allocation).
  const stepContextTokens = useChatStore(s => {
    const tokens = activeSessionId ? s.stepContextTokens[activeSessionId] : undefined
    return tokens ? tokens[stepId] : undefined
  })

  // Collapsed by default; the user can expand it. Reset the override whenever
  // the status changes so a settled/failed step re-collapses to the default
  // closed state instead of staying open.
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? false

  // Reset user override when status changes
  useEffect(() => { setUserOverride(null) }, [status])

  const statusConfig = {
    running:   { border: 'border-info',         Icon: Loader2,      iconClass: 'text-info animate-spin', accent: 'info' },
    completed: { border: 'border-success',      Icon: CheckCircle2, iconClass: 'text-success', accent: 'success' },
    failed:    { border: 'border-destructive',  Icon: XCircle,      iconClass: 'text-destructive', accent: 'destructive' },
    paused:    { border: 'border-warning',      Icon: CirclePause,  iconClass: 'text-warning', accent: 'warning' },
    // Abandoned before it settled (crash/app exit) — applied by the session-load
    // work-unit reconciliation, never by a live event.
    interrupted: { border: 'border-border',     Icon: CircleSlash,  iconClass: 'text-muted-foreground', accent: 'muted' },
    pending:   { border: 'border-border',       Icon: Circle,       iconClass: 'text-muted-foreground', accent: 'muted' },
  } as const

  const cfg = statusConfig[status] ?? statusConfig.pending
  const borderColor = cfg.border
  const StatusIcon = cfg.Icon
  const iconClass = cfg.iconClass
  const accent = cfg.accent

  const fullDesc = description || title

  // The step's checklist progress, derived from its own children — a checklist
  // nested in a plan_step carries this step's stepId, and handleStepTodoUpdate
  // keeps exactly ONE per level (each update supersedes the previous one), so
  // the first (and only) checklist child is authoritative. useMemo keeps the
  // counts stable across renders whose item object is fresh but equal.
  const checklist = useMemo(
    () => children.find((c): c is ChecklistChild => c.kind === 'checklist'),
    [children],
  )
  const checklistDone = checklist ? checklist.items.filter(i => i.checked).length : 0
  const checklistTotal = checklist?.items.length ?? 0

  const headerExtra = useMemo(() => (
    <>
      {isRetry && <RefreshCw className="h-3 w-3 text-warning" />}
      {status === 'failed' && error && (
        <span className="text-xs text-destructive truncate min-w-0" title={error}>— {error}</span>
      )}
      {status === 'interrupted' && (
        <span className="text-xs text-muted-foreground truncate min-w-0">— interrupted</span>
      )}
      {checklistTotal > 0 && (
        <StepChecklistProgress total={checklistTotal} completed={checklistDone} accent={accent} />
      )}
      {typeof stepContextFill === 'number' && (
        <span className="ml-2 inline-flex shrink-0">
          <ContextFillStatus
            percent={stepContextFill}
            usedTokens={stepContextTokens?.used_tokens}
            maxTokens={stepContextTokens?.max_tokens}
          />
        </span>
      )}
      {duration !== undefined && (
        <span className="ml-auto text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded shrink-0">
          {formatDuration(duration)}
        </span>
      )}
    </>
  ), [isRetry, status, error, stepContextFill, stepContextTokens, checklistTotal, checklistDone, accent, duration])

  const icon = useMemo(() => (
    <StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />
  ), [StatusIcon, iconClass])

  const label = useMemo(() => (
    <StepTooltip description={fullDesc} enabled={!!description}>
      <span className='text-sm min-w-0 truncate'>
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
        revealId={bookmarkKey(item)}
        headerExtra={headerExtra}
      >
        <div className={cn('mt-2 border-l-2 rounded pl-3 py-2 space-y-3 min-w-0', borderColor)}>
          <ChatMessageRenderer items={children} bookmarkable={bookmarkable} />
        </div>
      </CollapsibleBlock>
    </div>
  )
}, (prev, next) => prev.item === next.item || areDisplayItemsEqual(prev.item, next.item))
