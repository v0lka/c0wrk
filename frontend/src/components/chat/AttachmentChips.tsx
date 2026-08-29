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
import { FileText, Image as ImageIcon, Loader2, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAttachments, useAttachmentUploads } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { removeAttachment } from '@/api/attachments'
import { cancelAttachmentUpload } from '@/lib/attachmentUploads'
import { emit } from '@/api/runtime'
import { formatBytes } from '@/lib/formatters'
import { logger } from '@/lib/logger'
import type { AttachmentInfoUI, AttachmentUploadUI } from '@/types/models'

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
  // Optimistic in-flight uploads render as spinner chips ahead of the staged
  // list; their X cancels the upload (placeholder + staged file removed).
  const uploads = useAttachmentUploads(activeSessionId)

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

  const handleCancelUpload = useCallback(
    (upload: AttachmentUploadUI) => {
      if (!activeSessionId) return
      cancelAttachmentUpload(activeSessionId, upload)
    },
    [activeSessionId],
  )

  if (attachments.length === 0 && uploads.length === 0) return null

  return (
    <div className={cn('flex flex-wrap items-center gap-1.5 px-3 py-1 shrink-0 border-b border-border')}>
      {uploads.map((u) => (
        <UploadingChip key={u.id} upload={u} onCancel={handleCancelUpload} />
      ))}
      {attachments.map((a) => (
        <AttachmentChip key={a.id} attachment={a} onRemove={handleRemove} />
      ))}
    </div>
  )
}

/** Spinner chip for an in-flight upload: icon + name + cancel. Format/size
 *  are unknown until the backend finishes processing, so the spinner stands
 *  in for them. */
function UploadingChip({
  upload,
  onCancel,
}: {
  upload: AttachmentUploadUI
  onCancel: (upload: AttachmentUploadUI) => void
}): React.JSX.Element {
  return (
    <span
      className="inline-flex items-center gap-1 max-w-[220px] h-6 pl-2 pr-1 rounded-md border border-border bg-muted/40 text-xs text-foreground"
      title={`Processing ${upload.fileName}…`}
    >
      {upload.isImage ? (
        <ImageIcon className="size-3 shrink-0 text-muted-foreground" />
      ) : (
        <FileText className="size-3 shrink-0 text-muted-foreground" />
      )}
      <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" aria-label="Processing" />
      <span className="truncate">{upload.fileName}</span>
      <button
        type="button"
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          onCancel(upload)
        }}
        className="ml-0.5 inline-flex items-center justify-center size-4 rounded-sm text-muted-foreground hover:text-destructive hover:bg-destructive/10"
        title="Cancel upload"
        aria-label={`Cancel uploading ${upload.fileName}`}
      >
        <X className="size-3" />
      </button>
    </span>
  )
}
