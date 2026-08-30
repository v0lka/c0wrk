// Reusable attachment-staging pipeline shared by every entry point that turns
// a list of file paths into pending attachments: the 📎 picker
// (useAttachmentsInput), drag-and-drop, and paste-file.
//
// Encapsulates the three concerns that were previously inlined in
// useAttachmentsInput.handleAttach:
//   1. Vision gating — when the effective model lacks vision capability,
//      image files (png/jpg/jpeg/gif/webp) are filtered out before staging and
//      a dismissible error banner is surfaced via the attachmentsStore.
//      Documents are staged regardless of model capability.
//   2. Session-on-demand — when no active session exists yet, one is created
//      (mirroring terminal-mode auto-create) before staging.
//   3. Staging + store sync — attachFiles stages the paths and returns the FULL
//      current pending list; we push it into the store in one shot.
//
// Every store write is keyed to the session captured at stage time (the
// caller's activeSessionId, or the session created on demand) — NEVER to the
// currently-visible session. The attachFiles round-trip can straddle a
// session switch; its result must land in the session the user staged into.
//
// The picker (native file dialog) stays in useAttachmentsInput; only the
// post-pick staging logic lives here so drop/paste can reuse it verbatim.

import { useCallback } from 'react'
import { emit } from '@/api/runtime'
import { attachFiles, attachmentBaseName, isImagePath } from '@/api/attachments'
import {
  beginAttachmentUploadsFresh,
  completeAttachmentUploads,
  failAttachmentUploads,
} from '@/lib/attachmentUploads'
import { createSession } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { NULL_SESSION_KEY } from '@/stores/chatInputStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import { resolveModelContext, visionRejectionMessage } from '@/lib/vision'
import { logger } from '@/lib/logger'

/**
 * Reusable attachment-staging hook.
 *
 * @returns stageAttachmentPaths() bound to the effective model's vision
 *   capability. The callback takes the active session id (null = create one)
 *   and the file paths to stage.
 */
export function useStageAttachments(): {
  stageAttachmentPaths: (activeSessionId: string | null, paths: string[]) => Promise<void>
} {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const { allModels, defaultModel } = useConfigData()

  const stageAttachmentPaths = useCallback(
    async (activeSessionId: string | null, paths: string[]): Promise<void> => {
      try {
        // Resolve the effective model's vision capability. selectedModel is a
        // per-message override (composite "provider/name" or null = global
        // default_model); findModelInfo handles both forms.
        const { effectiveModel, modelInfo } = resolveModelContext(allModels, selectedModel, defaultModel)
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

        const visionRejected = imagePaths.length > 0 && !supportsVision
        if (visionRejected && docPaths.length === 0) {
          // Nothing stageable: surface the rejection without creating a
          // session. With no active session the banner is keyed under the
          // NULL_SESSION_KEY sentinel so it still renders in the input.
          useAttachmentsStore
            .getState()
            .setImageError(
              activeSessionId ?? NULL_SESSION_KEY,
              visionRejectionMessage(effectiveModel, modelInfo),
            )
          return
        }

        // Ensure a session exists — terminal mode does the same on demand.
        // From here on every write is keyed to this id, captured BEFORE the
        // awaits below: if the user switches sessions while attachFiles is in
        // flight, the result still lands in this (origin) session's key.
        let sessionId = activeSessionId
        if (!sessionId) {
          const newSession = await createSession()
          useSessionStore.getState().addSession(newSession)
          useSessionStore.getState().setActiveSessionId(newSession.id)
          sessionId = newSession.id
        }

        if (visionRejected) {
          useAttachmentsStore
            .getState()
            .setImageError(sessionId, visionRejectionMessage(effectiveModel, modelInfo))
        } else {
          // Clear any stale error from a previous attempt.
          useAttachmentsStore.getState().setImageError(sessionId, null)
        }

        const toAttach = supportsVision ? paths : docPaths

        // Optimistic upload placeholders: spinner chips appear and drain as
        // the backend's incremental `attachments:changed` events land
        // matching attachments (or when this RPC settles). Uploads cancelled
        // via their chip's X are stripped here and removed from the backend.
        // The fresh-baseline variant unions the backend's authoritative
        // pending list into the claim window first so a stale store slice
        // cannot shrink it.
        const uploads = await beginAttachmentUploadsFresh(
          sessionId,
          toAttach.map((p) => ({
            path: p,
            fileName: attachmentBaseName(p),
            isImage: isImagePath(p),
          })),
        )
        try {
          // attachFiles returns the FULL current pending list (already mapped).
          const list = await attachFiles(sessionId, toAttach)
          const kept = completeAttachmentUploads(sessionId, uploads, list)
          useAttachmentsStore.getState().setAttachments(sessionId, kept)
        } catch (err) {
          failAttachmentUploads(uploads)
          throw err
        }
      } catch (err) {
        logger.error('Failed to attach files:', err)
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to attach files',
        })
      }
    },
    [selectedModel, allModels, defaultModel],
  )

  return { stageAttachmentPaths }
}
