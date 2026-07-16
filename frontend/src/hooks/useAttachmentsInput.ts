// Attach-files toolbar action: opens the native picker, ensures a session
// exists (mirroring terminal-mode auto-create), stages the files and pushes
// the returned pending list into the store. Errors surface as a runtime_error
// toast (the app's existing error pattern).

import { useCallback } from 'react'
import { emit } from '@/api/runtime'
import { pickAttachmentFiles, attachFiles } from '@/api/attachments'
import { createSession } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { logger } from '@/lib/logger'

/**
 * @param activeSessionId active chat session id (may be null).
 * @returns handleAttach() bound to the current session, creating one on demand.
 */
export function useAttachmentsInput(activeSessionId: string | null): {
  handleAttach: () => Promise<void>
} {
  const handleAttach = useCallback(async () => {
    try {
      const paths = await pickAttachmentFiles()
      if (!paths?.length) return // user cancelled

      // Ensure a session exists — terminal mode does the same on demand.
      let sessionId = activeSessionId
      if (!sessionId) {
        const newSession = await createSession()
        useSessionStore.getState().addSession(newSession)
        useSessionStore.getState().setActiveSessionId(newSession.id)
        sessionId = newSession.id
      }

      // attachFiles returns the FULL current pending list (already mapped).
      const list = await attachFiles(sessionId, paths)
      useAttachmentsStore.getState().setAttachments(list)
    } catch (err) {
      logger.error('Failed to attach files:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to attach files',
      })
    }
  }, [activeSessionId])

  return { handleAttach }
}
