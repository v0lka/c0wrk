// AttachmentChips renders the active session's pending attachments as compact
// chips above the chat input. Each chip shows the file name, format, size and a
// remove control. The component only renders when there are pending
// attachments.
//
// Self-contained: reads attachments + activeSessionId directly from the stores
// via stable selectors (direct references / primitives — never allocated inside
// a selector). Removal calls the RPC; the `attachments:changed` event then
// replaces the store, so no local refetch is needed (same model as workDirs).

import { useCallback, type MouseEvent } from 'react'
import { FileText, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAttachments } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { removeAttachment } from '@/api/attachments'
import { emit } from '@/api/runtime'
import { formatBytes } from '@/lib/formatters'
import { logger } from '@/lib/logger'
import type { AttachmentInfoUI } from '@/types/models'

function AttachmentChip({
  attachment,
  onRemove,
}: {
  attachment: AttachmentInfoUI
  onRemove: (id: string) => void
}): React.JSX.Element {
  const isImage = attachment.isImage === true && attachment.thumbnail
  return (
    <span
      className="inline-flex items-center gap-1 max-w-[220px] h-6 pl-2 pr-1 rounded-md border border-border bg-muted/40 text-xs text-foreground"
      title={attachment.originalName}
    >
      {isImage ? (
        <img
          src={attachment.thumbnail}
          alt={attachment.originalName}
          className="size-6 shrink-0 rounded-sm object-cover"
        />
      ) : (
        <FileText className="size-3 shrink-0 text-muted-foreground" />
      )}
      <span className="truncate">{attachment.originalName}</span>
      <span className="text-muted-foreground shrink-0">({attachment.format})</span>
      <span className="text-muted-foreground shrink-0">{formatBytes(attachment.sizeBytes)}</span>
      <button
        type="button"
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          onRemove(attachment.id)
        }}
        className="ml-0.5 inline-flex items-center justify-center size-4 rounded-sm text-muted-foreground hover:text-destructive hover:bg-destructive/10"
        title="Remove attachment"
        aria-label={`Remove attachment ${attachment.originalName}`}
      >
        <X className="size-3" />
      </button>
    </span>
  )
}

export function AttachmentChips(): React.JSX.Element | null {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  // The chips render only the active session's pending list; other sessions'
  // lists stay in the store untouched.
  const attachments = useAttachments(activeSessionId)

  const handleRemove = useCallback(
    async (id: string) => {
      if (!activeSessionId) return
      try {
        await removeAttachment(activeSessionId, id)
        // The `attachments:changed` event replaces the store; no local refetch.
      } catch (err) {
        logger.error('Failed to remove attachment:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to remove attachment',
        })
      }
    },
    [activeSessionId],
  )

  if (attachments.length === 0) return null

  return (
    <div className={cn('flex flex-wrap items-center gap-1.5 px-3 py-1 shrink-0 border-b border-border')}>
      {attachments.map((a) => (
        <AttachmentChip key={a.id} attachment={a} onRemove={handleRemove} />
      ))}
    </div>
  )
}
