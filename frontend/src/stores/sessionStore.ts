import { create } from 'zustand'
import type { SessionInfo } from '@/lib/wails'

// Helper to sort sessions by last_active_at descending (most recent first).
// Used only for local mutations (addSession, updateSession, touchSession).
// setSessions receives pre-sorted data from the backend and skips sorting.
function sortSessionsByActivity(sessions: SessionInfo[]): SessionInfo[] {
  return [...sessions].sort((a, b) => {
    const aTime = new Date(a.last_active_at || a.created_at).getTime()
    const bTime = new Date(b.last_active_at || b.created_at).getTime()
    return bTime - aTime
  })
}

interface SessionState {
  sessions: SessionInfo[]
  activeSessionId: string | null
  setSessions: (sessions: SessionInfo[]) => void
  addSession: (session: SessionInfo) => void
  removeSession: (id: string) => void
  setActiveSession: (id: string | null) => void
  updateSession: (id: string, updates: Partial<SessionInfo>) => void
  touchSession: (id: string) => void
}

export const useSessionStore = create<SessionState>((set) => ({
  sessions: [],
  activeSessionId: null,
  setSessions: (sessions) => set({ sessions }),  // backend returns pre-sorted
  addSession: (session) => set((s) => {
    const updated = [session, ...s.sessions]
    return { sessions: sortSessionsByActivity(updated) }
  }),
  removeSession: (id) => set((s) => ({
    sessions: s.sessions.filter(sess => sess.id !== id),
    activeSessionId: s.activeSessionId === id ? null : s.activeSessionId,
  })),
  setActiveSession: (id) => set({ activeSessionId: id }),
  updateSession: (id, updates) => set((s) => {
    const updated = s.sessions.map(sess => sess.id === id ? { ...sess, ...updates } : sess)
    return { sessions: sortSessionsByActivity(updated) }
  }),
  touchSession: (id) => set((s) => {
    const now = new Date().toISOString()
    const updated = s.sessions.map(sess => 
      sess.id === id ? { ...sess, last_active_at: now } : sess
    )
    return { sessions: sortSessionsByActivity(updated) }
  }),
}))
