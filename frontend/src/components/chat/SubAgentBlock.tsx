import { useContext, useEffect, useMemo, useState } from 'react'
import { Bot, Loader2, CheckCircle2, XCircle, CirclePause } from 'lucide-react'
import { cn } from '@/lib/utils'
import { bookmarkKey } from '@/lib/bookmarks'
import { formatDuration } from '@/lib/formatters'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { StepTooltip } from './StepTooltip'
import { ChatMessageRenderer } from './ChatMessageRenderer'
import { BookmarkableContext } from './BookmarkableContext'
import type { DisplayItem } from '@/types/messages'

type SubAgentItem = Extract<DisplayItem, { kind: 'subagent' }>

const statusConfig = {
  running:   { Icon: Loader2,      iconClass: 'text-info animate-spin' },
  completed: { Icon: CheckCircle2, iconClass: 'text-success' },
  failed:    { Icon: XCircle,      iconClass: 'text-destructive' },
  paused:    { Icon: CirclePause,  iconClass: 'text-warning' },
} as const

export function SubAgentBlock({ item }: { item: SubAgentItem }) {
  const { stepId, description, status, duration, error, children } = item
  const bookmarkable = useContext(BookmarkableContext)

  // Collapsed by default; user can expand. Auto-collapses again on status change.
  const [userOverride, setUserOverride] = useState<boolean | null>(null)
  const isOpen = userOverride ?? false
  useEffect(() => { setUserOverride(null) }, [status])

  const cfg = statusConfig[status] ?? statusConfig.running
  const StatusIcon = cfg.Icon
  const iconClass = cfg.iconClass

  const statusIcon = useMemo(() => (
    <StatusIcon className={cn('h-3.5 w-3.5 shrink-0', iconClass)} />
  ), [StatusIcon, iconClass])

  const icon = useMemo(() => (
    <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
  ), [])

  const label = useMemo(() => (
    <StepTooltip description={description || ''} enabled={!!description}>
      <span className={cn('text-sm min-w-0 truncate', description && 'cursor-default')}>
        Delegated: {stepId}
      </span>
    </StepTooltip>
  ), [description, stepId])

  const headerExtra = useMemo(() => (
    <>
      {status === 'failed' && error && (
        <span className="text-xs text-destructive truncate min-w-0" title={error}>— {error}</span>
      )}
      {duration !== undefined && (
        <span className="ml-auto text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded shrink-0">
          {formatDuration(duration)}
        </span>
      )}
    </>
  ), [status, error, duration])

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
}
