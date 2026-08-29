// Zustand store for per-session pending file attachments.
//
// Keying by session id means:
//  - an upload staged in session A that completes after the user switched to
//    session B lands its list in A's key (the staging hooks capture the
//    session id before their awaits) — B's slice is never clobbered;
//  - the chips / image-error banner render only the ACTIVE session's state;
//  - pending lists survive session switches (no clear-on-switch), so
//    switching back shows the session's own list immediately.
//
// namesById stays a global accumulating cache (never wiped on switch): it
// resolves committed attachment ids to file names for read_attachment tool
// cards across sessions.
//
// Stable selectors: every hook returns a primitive or a direct store
// reference — the per-session list or the module-level EMPTY_ATTACHMENTS
// constant. Never allocate inside a useStore selector; React 19's
// useSyncExternalStore compares snapshots by reference (error #185).

import { create } from 'zustand'
import type { AttachmentInfoUI, AttachmentUploadUI } from '@/types/models'

/**
 * Stable empty list returned for sessions with no pending attachments.
 * Referentially stable (module constant) so the useAttachments selector
 * never allocates.
 */
export const EMPTY_ATTACHMENTS: AttachmentInfoUI[] = []

/**
 * Stable empty list for sessions with no in-flight uploads (same
 * referential-stability contract as EMPTY_ATTACHMENTS).
 */
export const EMPTY_UPLOADS: AttachmentUploadUI[] = []

interface AttachmentsState {
  /** Pending attachments per session; sparse — absent key = none. */
  attachmentsBySession: Record<string, AttachmentInfoUI[]>
  /**
   * Optimistic in-flight upload placeholders per session; sparse — absent
   * key = none. Written the moment a staging RPC starts (spinner chips) and
   * drained as the backend's incremental `attachments:changed` events land
   * their records (the lib's ID-window claim pass calls endUploads) or when
   * the staging RPC settles. Same sparse-key discipline as
   * attachmentsBySession.
   */
  uploadsBySession: Record<string, AttachmentUploadUI[]>
  /**
   * Accumulated attachment-id → original-name map, folded from every list the
   * store receives. Entries are never removed individually so committed
   * attachments stay resolvable after the pending chips are cleared — the
   * read_attachment tool card uses this to show the file name instead of the
   * opaque attachment id. Deliberately NOT keyed by session: names resolve
   * across sessions and survive switches (global cache).
   */
  namesById: Record<string, string>
  /**
   * Transient image-attachment error per session. Set when the user tries to
   * attach image files while a non-vision model is selected; cleared on
   * dismiss or successful attach. Absent key = no banner. Callers that can
   * act without an active session key it under the NULL_SESSION_KEY sentinel
   * ('' from chatInputStore) so the banner remains visible in the input.
   */
  imageErrorBySession: Record<string, string>
}

interface AttachmentsActions {
  /** Replace a session's entire pending list (the backend always sends the full list). */
  setAttachments: (sessionId: string, attachments: AttachmentInfoUI[]) => void
  /** Append optimistic upload placeholders (spinner chips) for a session. */
  beginUploads: (sessionId: string, uploads: AttachmentUploadUI[]) => void
  /** Remove listed upload placeholders (staging settled / upload cancelled /
   *  its staged record landed and was claimed by the lib's registry). */
  endUploads: (sessionId: string, uploadIds: string[]) => void
  /** Set a session's transient image-attachment error (null to clear). */
  setImageError: (sessionId: string, message: string | null) => void
  /** Drop the slices of deleted sessions (keeps the maps bounded; namesById stays). */
  dropSessions: (sessionIds: string[]) => void
}

export const useAttachmentsStore = create<AttachmentsState & AttachmentsActions>((set) => ({
  attachmentsBySession: {},
  uploadsBySession: {},
  namesById: {},
  imageErrorBySession: {},

  setAttachments: (sessionId, attachments) =>
    set((s) => {
      // Fold the freshly received attachment metadata into the id→name cache.
      // Reusing the incoming list keeps the slice reference stable; only the
      // touched records get new objects.
      let namesById = s.namesById
      for (const a of attachments) {
        if (namesById[a.id] !== a.originalName) {
          if (namesById === s.namesById) namesById = { ...s.namesById }
          namesById[a.id] = a.originalName
        }
      }

      // Empty lists (the send-flush) keep the record sparse: drop the
      // session's key instead of storing an empty array. Re-setting the same
      // array reference is a no-op so event replays don't churn subscribers.
      let attachmentsBySession = s.attachmentsBySession
      const current = attachmentsBySession[sessionId]
      if (attachments.length === 0) {
        if (current !== undefined) {
          attachmentsBySession = { ...s.attachmentsBySession }
          delete attachmentsBySession[sessionId]
        }
      } else if (current !== attachments) {
        attachmentsBySession = { ...s.attachmentsBySession, [sessionId]: attachments }
      }

      if (namesById === s.namesById && attachmentsBySession === s.attachmentsBySession) return s
      return { attachmentsBySession, namesById }
    }),

  beginUploads: (sessionId, uploads) => {
    if (uploads.length === 0) return
    set((s) => {
      const current = s.uploadsBySession[sessionId]
      return {
        uploadsBySession: {
          ...s.uploadsBySession,
          [sessionId]: current ? [...current, ...uploads] : uploads,
        },
      }
    })
  },

  endUploads: (sessionId, uploadIds) => {
    if (uploadIds.length === 0) return
    set((s) => {
      const current = s.uploadsBySession[sessionId]
      if (!current) return s
      const ids = new Set(uploadIds)
      const next = current.filter((u) => !ids.has(u.id))
      if (next.length === current.length) return s
      const uploadsBySession = { ...s.uploadsBySession }
      // Sparse discipline: an emptied slice drops its key entirely.
      if (next.length === 0) delete uploadsBySession[sessionId]
      else uploadsBySession[sessionId] = next
      return { uploadsBySession }
    })
  },

  setImageError: (sessionId, message) =>
    set((s) => {
      if (message === null) {
        if (!(sessionId in s.imageErrorBySession)) return s
        const imageErrorBySession = { ...s.imageErrorBySession }
        delete imageErrorBySession[sessionId]
        return { imageErrorBySession }
      }
      if (s.imageErrorBySession[sessionId] === message) return s
      return { imageErrorBySession: { ...s.imageErrorBySession, [sessionId]: message } }
    }),

  dropSessions: (sessionIds) =>
    set((s) => {
      let attachmentsBySession = s.attachmentsBySession
      for (const id of sessionIds) {
        if (id in attachmentsBySession) {
          if (attachmentsBySession === s.attachmentsBySession) {
            attachmentsBySession = { ...s.attachmentsBySession }
          }
          delete attachmentsBySession[id]
        }
      }
      let uploadsBySession = s.uploadsBySession
      for (const id of sessionIds) {
        if (id in uploadsBySession) {
          if (uploadsBySession === s.uploadsBySession) {
            uploadsBySession = { ...s.uploadsBySession }
          }
          delete uploadsBySession[id]
        }
      }
      let imageErrorBySession = s.imageErrorBySession
      for (const id of sessionIds) {
        if (id in imageErrorBySession) {
          if (imageErrorBySession === s.imageErrorBySession) {
            imageErrorBySession = { ...s.imageErrorBySession }
          }
          delete imageErrorBySession[id]
        }
      }
      if (
        attachmentsBySession === s.attachmentsBySession &&
        uploadsBySession === s.uploadsBySession &&
        imageErrorBySession === s.imageErrorBySession
      ) {
        return s
      }
      return { attachmentsBySession, uploadsBySession, imageErrorBySession }
    }),
}))

/**
 * Read a session's pending attachments.
 *
 * Returns the direct store slice reference — referentially stable as long as
 * the list itself is unchanged — or the shared EMPTY_ATTACHMENTS constant
 * when the session has none. Safe to pass to useStore directly.
 */
export function useAttachments(sessionId: string | null): AttachmentInfoUI[] {
  return useAttachmentsStore((s) =>
    sessionId ? s.attachmentsBySession[sessionId] ?? EMPTY_ATTACHMENTS : EMPTY_ATTACHMENTS,
  )
}

/**
 * Resolve an attachment id to its original file name. Returns undefined when
 * the id is unknown (e.g. an attachment committed before this app run, such
 * as after a restart). A primitive selector, so it only re-renders when the
 * resolved name actually changes.
 */
export function useAttachmentName(id: string | undefined | null): string | undefined {
  return useAttachmentsStore((s) => (id ? s.namesById[id] : undefined))
}

/**
 * Read a session's in-flight upload placeholders (spinner chips).
 *
 * Same stability contract as useAttachments: the direct slice reference or
 * the shared EMPTY_UPLOADS constant — never an allocation inside the
 * selector.
 */
export function useAttachmentUploads(sessionId: string | null): AttachmentUploadUI[] {
  return useAttachmentsStore((s) =>
    sessionId ? s.uploadsBySession[sessionId] ?? EMPTY_UPLOADS : EMPTY_UPLOADS,
  )
}

/**
 * Whether the session has any in-flight attachment uploads. Primitive
 * selector — drives the send-button lock without re-rendering on list
 * identity changes that preserve the empty/non-empty status.
 */
export function useHasActiveUploads(sessionId: string | null): boolean {
  return useAttachmentsStore((s) =>
    sessionId ? (s.uploadsBySession[sessionId]?.length ?? 0) > 0 : false,
  )
}
