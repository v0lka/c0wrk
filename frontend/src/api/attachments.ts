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
import { isAttachmentInfoRaw } from '@/types/events'
import type { AttachmentInfoRaw } from '@/types/events'
import type { AttachmentInfoUI } from '@/types/models'

export type { AttachmentInfoRaw } from '@/types/events'

/** Map a single snake_case backend record to camelCase UI record.
 *  Image-only fields (`is_image`/`thumbnail`) are forwarded when present so
 *  the UI can render thumbnails and gate image-only affordances. */
export function mapAttachment(raw: AttachmentInfoRaw): AttachmentInfoUI {
  return {
    id: raw.id,
    originalName: raw.original_name,
    format: raw.format,
    sizeBytes: raw.size_bytes,
    isImage: raw.is_image,
    thumbnail: raw.thumbnail,
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
