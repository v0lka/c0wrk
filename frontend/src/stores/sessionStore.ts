import { create } from 'zustand'
import type { SessionInfo } from '@/types/models'

// --- Helpers ---

function sortByActivity(sessions: SessionInfo[]): SessionInfo[] {
  return [...sessions].sort((a, b) => {
    const aTime = new Date(a.last_active_at || a.created_at).getTime()
    const bTime = new Date(b.last_active_at || b.created_at).getTime()
    return bTime - aTime
  })
}

// --- State types ---

interface SessionState {
  sessions: SessionInfo[] | null // null = not yet loaded
  activeSessionId: string | null
}

interface SessionActions {
  setSessions: (sessions: SessionInfo[]) => void
  setActiveSessionId: (id: string | null) => void
  addSession: (session: SessionInfo) => void
  removeSession: (id: string) => void
  updateSession: (id: string, updates: Partial<SessionInfo>) => void
  touchSession: (id: string) => void
}

// --- Store ---

export const useSessionStore = create<SessionState & SessionActions>((set) => ({
  sessions: null,
  activeSessionId: null,

  setSessions: (sessions) => set((s) => {
    const sorted = sortByActivity(sessions)
    // Skip if session IDs haven't changed (avoids duplicate event/RPC updates)
    if (s.sessions && s.sessions.length === sorted.length &&
        s.sessions.every((sess, i) => sess.id === sorted[i]?.id)) {
      return s
    }
    return { sessions: sorted }
  }),

  setActiveSessionId: (id) => set((s) =>
    s.activeSessionId === id ? s : { activeSessionId: id }
  ),

  addSession: (session) => set((s) => ({
    sessions: sortByActivity([session, ...(s.sessions ?? [])]),
  })),

  removeSession: (id) => set((s) => ({
    sessions: (s.sessions ?? []).filter(sess => sess.id !== id),
    activeSessionId: s.activeSessionId === id ? null : s.activeSessionId,
  })),

  updateSession: (id, updates) => set((s) => ({
    sessions: sortByActivity(
      (s.sessions ?? []).map(sess => sess.id === id ? { ...sess, ...updates } : sess)
    ),
  })),

  touchSession: (id) => set((s) => {
    const now = new Date().toISOString()
    return {
      sessions: sortByActivity(
        (s.sessions ?? []).map(sess =>
          sess.id === id ? { ...sess, last_active_at: now } : sess
        )
      ),
    }
  }),
}))
