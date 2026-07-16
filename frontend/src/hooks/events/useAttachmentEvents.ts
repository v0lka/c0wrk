// Attachment events: attachments:changed (session-scoped).
//
// The backend emits `attachments:changed` carrying an AttachmentsChangedData
// object: the FULL current pending list (replace the store) and an optional
// `failed` array of per-file failures. On SendMessage the pending list is
// flushed into the blackboard and the event carries an empty `attachments`
// list, so the chips clear automatically. On session switch we clear the store
// then best-effort load the persisted pending list via getAttachments.

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent, emit } from '@/api/runtime'
import { isAttachmentsChangedData } from '@/types/events'
import { getAttachments, mapAttachments } from '@/api/attachments'
import { useAttachmentsStore } from '@/stores/attachmentsStore'

export function useAttachmentEvents(sessionId: string | null): void {
  useEffect(() => {
    const store = useAttachmentsStore.getState()

    if (!sessionId) {
      store.clear()
      return
    }

    // Clear previous session's attachments, then best-effort load the new
    // session's pending list. Errors are ignored — the store simply stays
    // empty and live events will populate it.
    store.clear()
    getAttachments(sessionId)
      .then((list) => useAttachmentsStore.getState().setAttachments(list))
      .catch(() => { /* ignore — will stay empty */ })

    const cleanup = onSessionEvent(sessionId, 'attachments:changed', (data) => {
      if (!isAttachmentsChangedData(data)) {
        reportDroppedEvent('attachments:changed', data)
        return
      }
      useAttachmentsStore.getState().setAttachments(mapAttachments(data.attachments))

      // Surface per-file failures (e.g. unsupported format picked via the
      // "All files" filter). The backend already sends the basename as `path`.
      if (data.failed && data.failed.length > 0) {
        const names = data.failed.map((f) => f.path).join(', ')
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: `Could not attach: ${names}`,
        })
      }
    })

    return cleanup
  }, [sessionId])
}
