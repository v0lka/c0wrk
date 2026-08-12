// Typed parser for the unified user-message metadata blob persisted in
// ChatMessage.Metadata by the Go backend
// (backend/session/manager_attachment.go → UserMessageMetadata).
//
// The backend persists a SNAKE_CASE JSON blob:
//   { goal?: true, images?: StoredImageMeta[], attachments?: StoredAttachmentMeta[] }
// Every field is omitempty, so a single-signal message serializes to just that
// field (e.g. image-only → {"images":[...]}, matching the legacy
// StoredImagesMetadata shape for backward compatibility). This module safely
// extracts and validates that blob so downstream UI code works with typed
// records instead of raw `Record<string, unknown>`.
//
// Invalid or incomplete array elements are silently dropped (never thrown) — a
// malformed image/attachment record from a Wails serialization edge case must
// not break message reconstruction.

import { isObj } from '@/types/guards'

/** Per-image record persisted in ChatMessage.Metadata. Mirrors the backend
 *  StoredImageMetadata (snake_case). Carries the thumbnail data URI and the
 *  on-disk file path; the full base64 image data is never persisted. */
export interface StoredImageMeta {
  readonly id: string
  readonly name: string
  readonly thumbnail: string
  readonly path: string
  readonly media_type: string
}

/** Per-document record persisted in ChatMessage.Metadata. Mirrors the backend
 *  StoredAttachmentMeta (snake_case): just name/format/size — enough to render
 *  a chip for an already-sent document attachment on reload. The converted
 *  markdown content is never persisted here. */
export interface StoredAttachmentMeta {
  readonly original_name: string
  readonly format: string
  readonly size_bytes: number
}

/** Typed view of the unified user-message metadata blob. All fields optional
 *  to mirror the backend's omitempty serialization. */
export interface UserMessageMeta {
  readonly goal?: boolean
  readonly images?: StoredImageMeta[]
  readonly attachments?: StoredAttachmentMeta[]
  /** True when this user message was sent into a paused session as a nudge
   *  (nudge-resume path). Merged by the backend (mergeIsNudgeMetadata) so the
   *  nudge is distinguishable from a fresh request on reload. */
  readonly is_nudge?: boolean
}

/** Type guard for a single StoredImageMeta: requires all five string fields.
 *  An image missing any field (e.g. path) is considered incomplete and dropped. */
function isStoredImageMeta(v: unknown): v is StoredImageMeta {
  return (
    isObj(v) &&
    typeof v.id === 'string' &&
    typeof v.name === 'string' &&
    typeof v.thumbnail === 'string' &&
    typeof v.path === 'string' &&
    typeof v.media_type === 'string'
  )
}

/** Type guard for a single StoredAttachmentMeta: requires original_name and
 *  format (string) plus size_bytes (number). */
function isStoredAttachmentMeta(v: unknown): v is StoredAttachmentMeta {
  return (
    isObj(v) &&
    typeof v.original_name === 'string' &&
    typeof v.format === 'string' &&
    typeof v.size_bytes === 'number'
  )
}

/** Keep only the elements of `value` that pass `guard`. Returns an empty array
 *  when value is not an array or yields no valid elements. Never throws. */
function filterValid<T>(value: unknown, guard: (item: unknown) => item is T): T[] {
  return Array.isArray(value) ? value.filter((item): item is T => guard(item)) : []
}

/**
 * Safely parse the unified user-message metadata blob into a typed record.
 *
 * - `undefined` or non-object metadata → `{}`.
 * - `goal` is included only when strictly `true` (mirrors the backend's
 *   `omitempty` boolean serialization, where `goal` is absent unless true).
 * - `images`/`attachments` are included only when at least one valid element
 *   survives validation; incomplete or malformed elements are dropped silently.
 *
 * Never throws — safe to call on untrusted backend data.
 */
export function parseUserMessageMeta(
  metadata: Record<string, unknown> | undefined,
): UserMessageMeta {
  if (!isObj(metadata)) return {}

  // Mutable intermediate; the readonly UserMessageMeta contract applies at the
  // return boundary (mutable is assignable to readonly).
  const result: {
    goal?: boolean
    is_nudge?: boolean
    images?: StoredImageMeta[]
    attachments?: StoredAttachmentMeta[]
  } = {}

  if (metadata.goal === true) {
    result.goal = true
  }

  if (metadata.is_nudge === true) {
    result.is_nudge = true
  }

  const images = filterValid(metadata.images, isStoredImageMeta)
  if (images.length > 0) {
    result.images = images
  }

  const attachments = filterValid(metadata.attachments, isStoredAttachmentMeta)
  if (attachments.length > 0) {
    result.attachments = attachments
  }

  return result
}
