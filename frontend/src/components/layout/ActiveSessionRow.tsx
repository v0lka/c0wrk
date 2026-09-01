// One row of the active-sessions dropdown (opened from the Radar indicator).
//
// Deliberately a NAVIGATION surface only: no rename/pin/fork/archive/delete
// overlay and no active-session Check icon — those belong to the per-project
// session lists (SessionListItem). The active session is marked with
// font-medium alone, exactly like the "current" hint in the chat header.
//
// Layout mirrors SessionListItem's shared row: [status dot][mode icon]
// [truncated name + overflow tooltip … flex-1] [relative time], so the two
// lists read as one visual system. Purely presentational — every value
// (status, mode, activity) arrives via props, keeping the row unit-testable
// without store setup.

import { Code2, MessageCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatRelativeTime } from '@/lib/formatters'
import { EllipsisHint } from '@/components/chat/toolCards/shared/EllipsisHint'
import type { SessionDisplayStatus } from '@/lib/activeSessions'
import type { SessionInfo } from '@/types/models'

/** Dot color per display status — same tokens as the badge cluster and the
 *  regular session list. `idle` has no dot (and no live row can be idle);
 *  `failed` is the badge's red, which the regular list never needs. */
const STATUS_DOT_CLASSES: Partial<Record<SessionDisplayStatus, string>> = {
  pending: 'bg-warning',
  failed: 'bg-destructive',
  active: 'bg-success',
  paused: 'bg-muted-foreground',
}

/** Dot hover hint — same wording as SessionListItem's status dots. */
const STATUS_DOT_TITLES: Partial<Record<SessionDisplayStatus, string>> = {
  pending: 'Awaiting your response',
  failed: 'Task failed — resume available',
  active: 'Task running',
  paused: 'Task paused',
}

export interface ActiveSessionRowProps {
  session: SessionInfo
  status: SessionDisplayStatus
  /** The session currently open in the chat — rendered font-medium. */
  isActive: boolean
  /** CHAT (No Project) vs CODE, from the owning project's is_no_project. */
  isChat: boolean
  /** Owning project name, shown (with a `•` separator) before the title for
   *  CODE sessions only. Omitted for CHAT (No Project) sessions. */
  projectName?: string
}

export function ActiveSessionRow({ session, status, isActive, isChat, projectName }: ActiveSessionRowProps) {
  const dotClass = STATUS_DOT_CLASSES[status]
  const showProject = !isChat && !!projectName
  return (
    <>
      <div className="flex min-w-0 flex-auto items-center gap-1.5">
        {dotClass && (
          <span className={cn('size-1.5 shrink-0 rounded-full', dotClass)} title={STATUS_DOT_TITLES[status]} />
        )}
        {isChat ? (
          <MessageCircle className="size-3 shrink-0" aria-label="Chat session" />
        ) : (
          <Code2 className="size-3 shrink-0" aria-label="Code session" />
        )}
        {showProject && (
          <>
            <span className="shrink-0 text-muted-foreground">{projectName}</span>
            <span className="shrink-0 text-muted-foreground" aria-hidden="true">•</span>
          </>
        )}
        <EllipsisHint fullText={session.name} className={cn('flex-auto truncate', isActive && 'font-medium')}>
          {session.name}
        </EllipsisHint>
      </div>
      <span className="shrink-0 text-[10px] text-muted-foreground">{formatRelativeTime(session.last_active_at)}</span>
    </>
  )
}
