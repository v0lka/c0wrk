// Attach-files toolbar action: opens the native picker, ensures a session
// exists (mirroring terminal-mode auto-create), stages the files and pushes
// the returned pending list into the store. Errors surface as a runtime_error
// toast (the app's existing error pattern).
//
// Vision gating: when the effective model lacks vision capability, image
// files (png/jpg/jpeg/gif/webp) are filtered out before staging and an error
// banner is shown via the attachmentsStore. Document attachments are staged
// normally regardless of model capability.

import { useCallback } from 'react'
import { emit } from '@/api/runtime'
import { pickAttachmentFiles, attachFiles, isImagePath } from '@/api/attachments'
import { createSession } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import { findModelInfo, bareModel } from '@/lib/modelId'
import { logger } from '@/lib/logger'

/**
 * @param activeSessionId active chat session id (may be null).
 * @returns handleAttach() bound to the current session, creating one on demand.
 */
export function useAttachmentsInput(activeSessionId: string | null): {
  handleAttach: () => Promise<void>
} {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const { allModels, defaultModel } = useConfigData()

  const handleAttach = useCallback(async () => {
    try {
      const paths = await pickAttachmentFiles()
      if (!paths?.length) return // user cancelled

      // Resolve the effective model's vision capability. selectedModel is a
      // per-message override (composite "provider/name" or null = global
      // default_model); findModelInfo handles both forms.
      const effectiveModel = selectedModel ?? defaultModel
      const modelInfo = findModelInfo(allModels, effectiveModel)
      const supportsVision = modelInfo?.vision ?? false

      // Partition selected paths into images and documents. When the model
      // lacks vision, images are rejected (banner shown) and only documents
      // are staged.
      const imagePaths: string[] = []
      const docPaths: string[] = []
      for (const p of paths) {
        if (isImagePath(p)) {
          imagePaths.push(p)
        } else {
          docPaths.push(p)
        }
      }

      if (imagePaths.length > 0 && !supportsVision) {
        const modelName = modelInfo ? bareModel(modelInfo.name) : effectiveModel || 'текущая модель'
        useAttachmentsStore
          .getState()
          .setImageError(
            `Модель ${modelName} не поддерживает изображения. Выберите мультимодальную модель (vision).`,
          )
        // Only stage documents; skip images entirely.
        if (docPaths.length === 0) return
      } else {
        // Clear any stale error from a previous attempt.
        useAttachmentsStore.getState().setImageError(null)
      }

      const toAttach = supportsVision ? paths : docPaths

      // Ensure a session exists — terminal mode does the same on demand.
      let sessionId = activeSessionId
      if (!sessionId) {
        const newSession = await createSession()
        useSessionStore.getState().addSession(newSession)
        useSessionStore.getState().setActiveSessionId(newSession.id)
        sessionId = newSession.id
      }

      // attachFiles returns the FULL current pending list (already mapped).
      const list = await attachFiles(sessionId, toAttach)
      useAttachmentsStore.getState().setAttachments(list)
    } catch (err) {
      logger.error('Failed to attach files:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to attach files',
      })
    }
  }, [activeSessionId, selectedModel, allModels, defaultModel])

  return { handleAttach }
}
