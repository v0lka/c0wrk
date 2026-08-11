// ArchivedBanner replaces the message input for archived sessions.
//
// An archived session is read-only: the user cannot send messages or run tools,
// so the entire input shell (editor, toolbar, attachments, drop-zone) is hidden.
// This banner surfaces the archived state and offers a one-click restore action
// (ArchiveSession is a toggle on the backend, so the same call unarchives).
//
// Styled per the design system (warning color #d19a66 via Tailwind `text-warning`
// / `bg-warning` tokens), matching the ArchiveRestore icon used in SessionListItem.

import { useCallback, useState } from 'react'
import { ArchiveRestore, Loader2 } from 'lucide-react'
import { useSessionStore } from '@/stores/sessionStore'
import { archiveSession } from '@/api/sessions'
import { logger } from '@/lib/logger'

export function ArchivedBanner({ sessionId }: { sessionId: string }): React.JSX.Element {
  const updateSession = useSessionStore((s) => s.updateSession)
  const [restoring, setRestoring] = useState(false)

  const handleUnarchive = useCallback(async () => {
    setRestoring(true)
    try {
      // archiveSession is a toggle on the backend (ArchiveSession flips the
      // flag). The active session is already archived, so this unarchives it.
      await archiveSession(sessionId)
      updateSession(sessionId, { archived: false })
    } catch (error) {
      logger.error('Failed to unarchive session:', error)
    } finally {
      setRestoring(false)
    }
  }, [sessionId, updateSession])

  return (
    <div className="flex items-center gap-2 px-3 py-2 shrink-0 border-t border-x border-warning/30 bg-warning/5">
      <ArchiveRestore className="size-4 shrink-0 text-warning" />
      <span className="text-xs text-warning flex-1 min-w-0">
        This session is archived — restore it to send messages.
      </span>
      <button
        type="button"
        onClick={handleUnarchive}
        disabled={restoring}
        className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-warning border border-warning/40 hover:bg-warning/10 active:bg-warning/20 disabled:opacity-50 shrink-0"
      >
        {restoring ? (
          <Loader2 className="size-3 animate-spin" />
        ) : (
          <ArchiveRestore className="size-3" />
        )}
        Restore
      </button>
    </div>
  )
}
