import { memo, useContext, useEffect, useMemo, useState } from 'react'
import { Bot, Loader2, CheckCircle2, XCircle, CirclePause, CircleSlash } from 'lucide-react'
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

type SubAgentItem = Extract<DisplayItem, { kind: 'subagent' }>
type ChecklistChild = Extract<DisplayItem, { kind: 'checklist' }>

const statusConfig = {
  running:   { Icon: Loader2,      iconClass: 'text-info animate-spin', accent: 'info' },
  completed: { Icon: CheckCircle2, iconClass: 'text-success', accent: 'success' },
  failed:    { Icon: XCircle,      iconClass: 'text-destructive', accent: 'destructive' },
  paused:    { Icon: CirclePause,  iconClass: 'text-warning', accent: 'warning' },
  // Abandoned before it settled (crash/app exit) — applied by the session-load
  // work-unit reconciliation, never by a live event.
  interrupted: { Icon: CircleSlash, iconClass: 'text-muted-foreground', accent: 'muted' },
} as const

export const SubAgentBlock = memo(function SubAgentBlock({ item }: { item: SubAgentItem }) {
  const { stepId, description, status, duration, error, children } = item
  const bookmarkable = useContext(BookmarkableContext)
  // Plan groups are rebuilt per session switch, so the displayed steps always
  // belong to the active session. The nested lookup returns a primitive
  // (stable selector — no allocation).
  const activeSessionId = useSessionStore(s => s.activeSessionId)
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

  // Collapsed by default; user can expand. Auto-collapses again on status change.
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? false
  useEffect(() => { setUserOverride(null) }, [status])

  const cfg = statusConfig[status] ?? statusConfig.running
  const StatusIcon = cfg.Icon
  const iconClass = cfg.iconClass
  const accent = cfg.accent

  const statusIcon = useMemo(() => (
    <StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />
  ), [StatusIcon, iconClass])

  const icon = useMemo(() => (
    <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
  ), [])

  const label = useMemo(() => (
    <StepTooltip description={description || ''} enabled={!!description}>
      <span className='text-sm min-w-0 truncate'>
        Delegated: {stepId}
      </span>
    </StepTooltip>
  ), [description, stepId])

  // The delegate's checklist progress, derived from its own children — a
  // checklist nested under a subagent block carries this delegate's stepId,
  // and handleStepTodoUpdate keeps exactly ONE per level (each update
  // supersedes the previous one), so the first (and only) checklist child is
  // authoritative. useMemo keeps the counts stable across renders whose item
  // object is fresh but equal.
  const checklist = useMemo(
    () => children.find((c): c is ChecklistChild => c.kind === 'checklist'),
    [children],
  )
  const checklistDone = checklist ? checklist.items.filter(i => i.checked).length : 0
  const checklistTotal = checklist?.items.length ?? 0

  const headerExtra = useMemo(() => (
    <>
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
  ), [status, error, stepContextFill, stepContextTokens, checklistTotal, checklistDone, accent, duration])

  return (
    <CollapsibleBlock
      icon={icon}
      label={label}
      open={isOpen}
      onOpenChange={(open) => setUserOverride(open)}
      statusIcon={statusIcon}
      revealId={bookmarkKey(item)}
      headerExtra={headerExtra}
    >
      <div className="mt-2 border-l-2 border-border rounded pl-3 py-2 space-y-3 min-w-0">
        <ChatMessageRenderer items={children} bookmarkable={bookmarkable} />
      </div>
    </CollapsibleBlock>
  )
}, (prev, next) => prev.item === next.item || areDisplayItemsEqual(prev.item, next.item))
