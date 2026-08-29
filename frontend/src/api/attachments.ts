// File-attachment API wrappers.
//
// Wraps the generated Wails bindings (window.go.desktop.App) so components
// never import wailsjs directly. The backend's AttachmentInfo is SNAKE_CASE
// ({id, original_name, format, size_bytes}); we map it to a camelCase
// AttachmentInfoUI at this boundary so the rest of the app works with idiomatic
// TS field names.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf } from '@/types/guards'
import { isAttachmentInfoRaw, isPasteResultRaw } from '@/types/events'
import type { AttachmentInfoRaw, PasteResultRaw } from '@/types/events'
import type { AttachmentInfoUI, PasteResultUI } from '@/types/models'

/** Re-export the raw event type for backward compatibility */
export type { AttachmentInfoRaw, PasteResultRaw } from '@/types/events'

/** Map a single snake_case backend record to camelCase UI record.
 *  Image-only fields (`is_image`/`thumbnail`/`path`/`media_type`) are
 *  forwarded when present so the UI can render thumbnails and gate image-only
 *  affordances. */
export function mapAttachment(raw: AttachmentInfoRaw): AttachmentInfoUI {
  return {
    id: raw.id,
    originalName: raw.original_name,
    format: raw.format,
    sizeBytes: raw.size_bytes,
    isImage: raw.is_image,
    thumbnail: raw.thumbnail,
    path: raw.path,
    mediaType: raw.media_type,
  }
}

/** Map a list of snake_case backend records to camelCase UI records. */
export function mapAttachments(raw: readonly AttachmentInfoRaw[]): AttachmentInfoUI[] {
  return raw.map(mapAttachment)
}

/** Image extensions recognised by the backend's isImageFormat (png/jpg/jpeg/gif/webp).
 *  Used to pre-filter image files before attach when the selected model lacks
 *  vision capability. Mirrors backend/session/manager_attachment.go. */
const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp'])

/** Report whether a file path has an image extension (png/jpg/jpeg/gif/webp).
 *  Mirrors the backend `isImageFormat` so the frontend can gate image uploads
 *  on model vision capability before staging. */
export function isImagePath(path: string): boolean {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  return IMAGE_EXTENSIONS.has(ext)
}

/** Basename of a staged file path — the display label for picker and
 *  drag-and-drop upload placeholders. Mirrors the backend's `filepath.Base`
 *  fallback for AttachmentInfo.OriginalName, so it understands BOTH path
 *  separators: the backend runs on Windows too, where absolute paths arrive
 *  with backslashes. */
export function attachmentBaseName(path: string): string {
  const idx = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return idx === -1 ? path : path.slice(idx + 1)
}

/**
 * Open the native file picker for attachment selection.
 * Returns the chosen absolute paths (empty if the user cancelled).
 */
export async function pickAttachmentFiles(): Promise<string[]> {
  try {
    const app = getApp()
    const result = await app.PickAttachmentFiles()
    if (!Array.isArray(result)) {
      throw new Error('pickAttachmentFiles: backend returned non-array data')
    }
    return result.filter((p: unknown): p is string => typeof p === 'string')
  } catch (err) {
    logger.error('Failed to pick attachment files:', err)
    throw err
  }
}

/**
 * Stage files as pending attachments for a session.
 * The backend returns the FULL current pending-attachment list; we map it to
 * UI records so the caller can replace its store in one shot.
 */
export async function attachFiles(sessionId: string, paths: string[]): Promise<AttachmentInfoUI[]> {
  try {
    const app = getApp()
    const result = await app.AttachFiles(sessionId, paths)
    if (!isArrayOf(result, isAttachmentInfoRaw)) {
      logger.error('attachFiles: unexpected response shape, returning []', result)
      return []
    }
    return mapAttachments(result)
  } catch (err) {
    logger.error('Failed to attach files:', err)
    throw err
  }
}

/** Remove a single pending attachment by id. */
export async function removeAttachment(sessionId: string, attachmentId: string): Promise<void> {
  try {
    const app = getApp()
    await app.RemoveAttachment(sessionId, attachmentId)
  } catch (err) {
    logger.error('Failed to remove attachment:', err)
    throw err
  }
}

/** Fetch the full current pending-attachment list for a session. */
export async function getAttachments(sessionId: string): Promise<AttachmentInfoUI[]> {
  try {
    const app = getApp()
    const result = await app.GetAttachments(sessionId)
    if (!isArrayOf(result, isAttachmentInfoRaw)) {
      logger.error('getAttachments: unexpected response shape, returning []', result)
      return []
    }
    return mapAttachments(result)
  } catch (err) {
    logger.error('Failed to get attachments:', err)
    throw err
  }
}

/**
 * Fetch the converted markdown content of a committed blackboard attachment.
 * Used when the user opens an attachment in the file viewer. Returns the raw
 * markdown string produced by markitdown.
 */
export async function getBlackboardAttachmentMarkdown(sessionId: string, attachmentId: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetBlackboardAttachmentMarkdown(sessionId, attachmentId)
    if (typeof result !== 'string') {
      throw new Error('getBlackboardAttachmentMarkdown: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get blackboard attachment markdown:', err)
    throw err
  }
}

/**
 * Probe the system clipboard and stage the highest-priority content found
 * (image → files → text) as a pending attachment on the session. Wraps the
 * backend PasteFromClipboard RPC.
 *
 * `supportsVision` reflects the active model's image-input capability and
 * gates image staging on the backend side. The discriminant `kind` in the
 * result tells the caller how to react: render an image/file chip (kind=image
 * with accepted files, or kind=files), surface a rejection (kind=image with
 * `rejected`), or insert text into the chat input (kind=text).
 *
 * On an unexpected response shape we return an `empty` result (logging the
 * anomaly) rather than throwing, so a malformed backend reply never crashes
 * a paste — the user simply sees nothing happen.
 */
export async function pasteFromClipboard(sessionId: string, supportsVision: boolean): Promise<PasteResultUI> {
  try {
    const app = getApp()
    const result = await app.PasteFromClipboard(sessionId, supportsVision)
    if (!isPasteResultRaw(result)) {
      logger.error('pasteFromClipboard: unexpected response shape, returning empty', result)
      return { kind: 'empty', files: [] }
    }
    return mapPasteResult(result)
  } catch (err) {
    logger.error('Failed to paste from clipboard:', err)
    throw err
  }
}

/** Map a snake_case backend PasteResult to the camelCase UI shape.
 *  `files` defaults to an empty array so the UI can branch on `kind` without
 *  a null check. */
export function mapPasteResult(raw: PasteResultRaw): PasteResultUI {
  return {
    kind: raw.kind,
    text: raw.text,
    files: raw.files ? mapAttachments(raw.files) : [],
    rejected: raw.rejected,
    skippedImages: raw.skipped_images,
  }
}
