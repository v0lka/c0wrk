// Async clipboard-paste handler shared by the chat editor.
//
// The CodeMirror paste extension (useChatEditor) takes the fast path for
// pure-text pastes and delegates everything else — images and copied files —
// to the async handler built here. This hook owns the "non-fast-path" body:
//
//   1. Vision resolution — the active model's image-input capability is
//      resolved exactly as useStageAttachments does (selectedModel override,
//      else global defaultModel; findModelInfo + .vision). supportsVision is
//      forwarded to the backend so image staging is gated there.
//   2. Session-on-demand — when no session is active yet, one is created
//      (mirroring terminal-mode auto-create) before pasting.
//   3. Backend probe + routing — pasteFromClipboard returns a discriminated
//      PasteResultUI; the handler reacts per kind:
//        image | files → the backend staged the attachments and returned the
//          FULL current pending list → push it into the store. When kind=image
//          and `rejected` is set, the image was NOT staged → surface the
//          rejection as the image-error banner.
//        text → insert the text at the cursor (native-style, no attachment).
//        empty → nothing to do.
//
// The handler is kept separate from useChatEditor so the paste body is
// unit-testable without spinning up a CodeMirror instance, and so the editor
// hook stays focused on CM lifecycle.

import { useCallback } from 'react'
import { emit } from '@/api/runtime'
import { pasteFromClipboard } from '@/api/attachments'
import {
  beginAttachmentUploads,
  collectPasteUploadDescriptors,
  completeAttachmentUploads,
  failAttachmentUploads,
} from '@/lib/attachmentUploads'
import type { AttachmentUploadUI } from '@/types/models'
import { createSession } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import { resolveModelContext, visionRejectionMessage, VISION_UNSUPPORTED } from '@/lib/vision'
import { logger } from '@/lib/logger'
import type { ChatEditorAPI } from '@/hooks/useChatEditor'

/**
 * Resolve the active model's image-input capability from the model store.
 *
 * Mirrors the resolution in useStageAttachments: selectedModel is a per-message
 * composite override (provider/name) or null (use the global default_model);
 * findModelInfo handles both forms. Returns false when the effective model
 * can't be resolved (treat as non-vision so the backend never stages an image).
 */
export function resolveSupportsVision(
  selectedModel: string | null,
  defaultModel: string,
  allModels: ReturnType<typeof useConfigData>['allModels'],
): boolean {
  const { modelInfo } = resolveModelContext(allModels, selectedModel, defaultModel)
  return modelInfo?.vision ?? false
}

// collectPasteUploadDescriptors moved to lib/attachmentUploads (it belongs
// with the upload lifecycle it feeds and is unit-testable there without the
// React hook harness). Re-exported so existing import sites keep working.
export { collectPasteUploadDescriptors }

/**
 * Build the async paste handler for the chat editor.
 *
 * @param editor the editor imperative API (for text insertion on text-paste).
 * @returns onPaste(data) — the callback useChatEditor's paste extension
 *   invokes for non-fast-path events.
 */
export function usePasteHandler(editor: ChatEditorAPI): {
  onPaste: (data: DataTransfer) => Promise<void>
} {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const { allModels, defaultModel } = useConfigData()

  const onPaste = useCallback(
    async (_data: DataTransfer): Promise<void> => {
      // MUST be read synchronously (see collectPasteUploadDescriptors).
      let pasteUploads: AttachmentUploadUI[] = []
      try {
        const supportsVision = resolveSupportsVision(selectedModel, defaultModel, allModels)
        const descriptors = collectPasteUploadDescriptors(_data, supportsVision)

        // Ensure a session exists before probing (the backend paste needs one).
        // Captured BEFORE the awaits: every store write below is keyed to this
        // id, so a paste that resolves after the user switched sessions still
        // lands in the session the paste was made from — never in the
        // currently-visible one.
        let sessionId = useSessionStore.getState().activeSessionId
        if (!sessionId) {
          const newSession = await createSession()
          useSessionStore.getState().addSession(newSession)
          useSessionStore.getState().setActiveSessionId(newSession.id)
          sessionId = newSession.id
        }

        // Optimistic spinner chips for the files the backend will stage.
        if (descriptors.length > 0) {
          pasteUploads = beginAttachmentUploads(sessionId, descriptors)
        }

        try {
          const result = await pasteFromClipboard(sessionId, supportsVision)

          switch (result.kind) {
            case 'image':
            case 'files': {
              // The backend staged the attachments and returned the FULL current
              // pending list (image and/or files). Push it into the origin
              // session's slice (cancelled uploads stripped + removed).
              const kept = completeAttachmentUploads(sessionId, pasteUploads, result.files)
              useAttachmentsStore.getState().setAttachments(sessionId, kept)
              // Vision rejection: a raw clipboard image declined (sentinel) OR
              // image-ext files skipped because the active model lacks vision.
              // Synthesize the localized banner so copy has a single source of
              // truth (the backend never sends localized text).
              const visionRejected =
                (result.kind === 'image' && result.rejected === VISION_UNSUPPORTED) ||
                (result.kind === 'files' && (result.skippedImages ?? 0) > 0)
              if (visionRejected) {
                const { effectiveModel, modelInfo } = resolveModelContext(
                  allModels,
                  selectedModel,
                  defaultModel,
                )
                useAttachmentsStore
                  .getState()
                  .setImageError(sessionId, visionRejectionMessage(effectiveModel, modelInfo))
              } else if (result.rejected) {
                // A real processing error (e.g. temp-file write failure) — show it
                // verbatim rather than masking it as a vision rejection.
                useAttachmentsStore.getState().setImageError(sessionId, result.rejected)
              } else if (result.files.length > 0) {
                // Successful attach — clear any stale banner. Only clear when
                // something was actually staged, so a concurrent drop's rejection
                // that is still relevant is not dismissed.
                useAttachmentsStore.getState().setImageError(sessionId, null)
              }
              break
            }
            case 'text': {
              // Nothing staged — drain any optimistic placeholders.
              failAttachmentUploads(pasteUploads)
              if (typeof result.text === 'string' && result.text.length > 0) {
                editor.insertAtCursor(result.text)
              }
              break
            }
            case 'empty':
              // Nothing on the clipboard we understand — drain placeholders.
              failAttachmentUploads(pasteUploads)
              break
          }
        } catch (err) {
          failAttachmentUploads(pasteUploads)
          throw err
        }
      } catch (err) {
        logger.error('Failed to handle paste:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to paste from clipboard',
        })
      }
    },
    [editor, selectedModel, allModels, defaultModel],
  )

  return { onPaste }
}
