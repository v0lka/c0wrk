// Attachment events: attachments:changed (session-scoped).
//
// The backend emits `attachments:changed` carrying an AttachmentsChangedData
// object: the FULL current pending list (replace the session's slice) and an
// optional `failed` array of per-file failures. On SendMessage the pending
// list is flushed into the blackboard and the event carries an empty
// `attachments` list, so the chips clear automatically.
//
// The store is keyed by session id and there is NO clear-on-switch: switching
// away leaves the previous session's slice intact, and the initial
// getAttachments fetch + live events always write into THIS effect's session
// key. An in-flight fetch that settles after a switch can therefore only
// ever touch its own session's slice — the now-active session's state is
// never clobbered with stale data.

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent, emit } from '@/api/runtime'
import { isAttachmentsChangedData } from '@/types/events'
import { getAttachments, mapAttachments } from '@/api/attachments'
import { useAttachmentsStore } from '@/stores/attachmentsStore'

export function useAttachmentEvents(sessionId: string | null): void {
  useEffect(() => {
    // No active session → nothing to subscribe to (per-session state stays).
    if (!sessionId) return

    // Guard against writes after this subscription is gone (e.g. the session
    // was deleted while the initial fetch was in flight, which would
    // otherwise resurrect its dropped slice).
    let cancelled = false

    // Best-effort load of this session's persisted pending list, refreshing
    // whatever the slice already holds (e.g. state staged before a switch
    // away and back). Errors are ignored — the slice stays as-is and live
    // events will correct it.
    getAttachments(sessionId)
      .then((list) => { if (!cancelled) useAttachmentsStore.getState().setAttachments(sessionId, list) })
      .catch(() => { /* ignore — keep the existing slice */ })

    const cleanup = onSessionEvent(sessionId, 'attachments:changed', (data) => {
      if (!isAttachmentsChangedData(data)) {
        reportDroppedEvent('attachments:changed', data)
        return
      }
      useAttachmentsStore.getState().setAttachments(sessionId, mapAttachments(data.attachments))

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

    return () => { cancelled = true; cleanup() }
  }, [sessionId])
}
