import { useMemo } from 'react'
import { create } from 'zustand'
import type { SessionBookmark } from '@/types/models'
import {
  addBookmark as apiAddBookmark,
  listBookmarks as apiListBookmarks,
  deleteBookmark as apiDeleteBookmark,
  renameBookmark as apiRenameBookmark,
} from '@/api/bookmarks'
import { logger } from '@/lib/logger'

interface BookmarkState {
  // Bookmarks indexed by session: sessionId -> bookmarks (oldest first).
  bySession: Record<string, SessionBookmark[]>
  loadBookmarks: (sessionId: string) => Promise<void>
  addBookmark: (sessionId: string, eventKey: string, title: string) => Promise<void>
  removeBookmark: (sessionId: string, bookmarkId: string) => Promise<void>
  renameBookmark: (sessionId: string, bookmarkId: string, title: string) => Promise<void>
  clearSession: (sessionId: string) => void
}

const EMPTY: SessionBookmark[] = []

/** Replace any existing bookmark with the same event_key and append `item`. */
function upsertByEventKey(list: SessionBookmark[], item: SessionBookmark): SessionBookmark[] {
  const next = list.filter((b) => b.event_key !== item.event_key)
  next.push(item)
  return next
}

export const useBookmarkStore = create<BookmarkState>((set, get) => ({
  bySession: {},

  loadBookmarks: async (sessionId) => {
    try {
      const list = await apiListBookmarks(sessionId)
      set((s) => ({ bySession: { ...s.bySession, [sessionId]: list } }))
    } catch (err) {
      logger.error('Failed to load bookmarks:', err)
    }
  },

  addBookmark: async (sessionId, eventKey, title) => {
    // Optimistic add so the star fills immediately; the server-assigned id and
    // created_at are reconciled once the RPC resolves.
    const optimistic: SessionBookmark = {
      id: `pending-${eventKey}`,
      session_id: sessionId,
      event_key: eventKey,
      title,
      created_at: new Date().toISOString(),
    }
    set((s) => ({
      bySession: { ...s.bySession, [sessionId]: upsertByEventKey(s.bySession[sessionId] ?? [], optimistic) },
    }))
    try {
      const created = await apiAddBookmark(sessionId, eventKey, title)
      set((s) => ({
        bySession: { ...s.bySession, [sessionId]: upsertByEventKey(s.bySession[sessionId] ?? [], created) },
      }))
    } catch (err) {
      logger.error('Failed to add bookmark:', err)
      // Roll back the optimistic entry so the star returns to its prior state.
      set((s) => {
        const current = s.bySession[sessionId] ?? []
        return { bySession: { ...s.bySession, [sessionId]: current.filter((b) => b.event_key !== eventKey) } }
      })
    }
  },

  removeBookmark: async (sessionId, bookmarkId) => {
    // Optimistic removal so the star flips immediately; re-sync on failure.
    const prev = get().bySession[sessionId] ?? EMPTY
    set((s) => {
      const current = s.bySession[sessionId] ?? []
      return { bySession: { ...s.bySession, [sessionId]: current.filter((b) => b.id !== bookmarkId) } }
    })
    try {
      await apiDeleteBookmark(sessionId, bookmarkId)
    } catch (err) {
      logger.error('Failed to delete bookmark:', err)
      try {
        const list = await apiListBookmarks(sessionId)
        set((s) => ({ bySession: { ...s.bySession, [sessionId]: list } }))
      } catch (resyncErr) {
        // If the re-sync also fails, restore the pre-removal list rather than
        // wiping the session's bookmarks to an empty array.
        logger.error('Failed to re-sync bookmarks after delete failure:', resyncErr)
        set((s) => ({ bySession: { ...s.bySession, [sessionId]: prev } }))
      }
    }
  },

  renameBookmark: async (sessionId, bookmarkId, title) => {
    // Optimistic rename; re-sync on failure.
    const prev = get().bySession[sessionId] ?? EMPTY
    set((s) => {
      const current = s.bySession[sessionId] ?? []
      return {
        bySession: {
          ...s.bySession,
          [sessionId]: current.map((b) => (b.id === bookmarkId ? { ...b, title } : b)),
        },
      }
    })
    try {
      await apiRenameBookmark(sessionId, bookmarkId, title)
    } catch (err) {
      logger.error('Failed to rename bookmark:', err)
      try {
        const list = await apiListBookmarks(sessionId)
        set((s) => ({ bySession: { ...s.bySession, [sessionId]: list } }))
      } catch (resyncErr) {
        // Restore the pre-rename list rather than wiping the session's bookmarks.
        logger.error('Failed to re-sync bookmarks after rename failure:', resyncErr)
        set((s) => ({ bySession: { ...s.bySession, [sessionId]: prev } }))
      }
    }
  },

  clearSession: (sessionId) => set((s) => {
    const { [sessionId]: _dropped, ...rest } = s.bySession
    return { bySession: rest }
  }),
}))

/**
 * Reactive, referentially-stable bookmarks for a session. Returns a stable
 * empty array when the session has no loaded bookmarks (avoids re-render
 * churn and selector allocation).
 */
export function useSessionBookmarks(sessionId: string | null): SessionBookmark[] {
  const bookmarks = useBookmarkStore((s) => (sessionId ? s.bySession[sessionId] : undefined))
  return useMemo(() => bookmarks ?? EMPTY, [bookmarks])
}
