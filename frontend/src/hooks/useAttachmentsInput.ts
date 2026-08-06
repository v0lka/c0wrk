// Attach-files toolbar action: opens the native picker, then delegates the
// staging pipeline (vision gating, session-on-demand, attach + store sync) to
// useStageAttachments — the same pipeline used by drag-and-drop and
// paste-file. This hook stays a thin wrapper: pick → stage.
//
// The picker call is wrapped in its own try/catch so a picker failure surfaces
// the app's existing runtime_error toast pattern; staging errors are handled
// inside stageAttachmentPaths.

import { useCallback } from 'react'
import { emit } from '@/api/runtime'
import { pickAttachmentFiles } from '@/api/attachments'
import { useStageAttachments } from '@/hooks/useStageAttachments'
import { logger } from '@/lib/logger'

/**
 * @param activeSessionId active chat session id (may be null).
 * @returns handleAttach() bound to the current session, creating one on demand.
 */
export function useAttachmentsInput(activeSessionId: string | null): {
  handleAttach: () => Promise<void>
} {
  const { stageAttachmentPaths } = useStageAttachments()

  const handleAttach = useCallback(async () => {
    try {
      const paths = await pickAttachmentFiles()
      if (!paths?.length) return // user cancelled

      await stageAttachmentPaths(activeSessionId, paths)
    } catch (err) {
      logger.error('Failed to attach files:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to attach files',
      })
    }
  }, [activeSessionId, stageAttachmentPaths])

  return { handleAttach }
}
