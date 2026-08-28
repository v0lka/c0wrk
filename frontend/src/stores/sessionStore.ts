import { create } from 'zustand'
import type { SessionInfo } from '@/types/models'

// --- Helpers ---

function sortByActivity(sessions: SessionInfo[]): SessionInfo[] {
  return [...sessions].sort((a, b) => {
    // Pinned sessions always surface to the top, mirroring the backend's
    // `ORDER BY pinned DESC, COALESCE(last_active_at, created_at) DESC`.
    if (a.pinned !== b.pinned) return a.pinned ? -1 : 1
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
  setUnfinishedTask: (id: string, value: boolean) => void
  touchSession: (id: string) => void
  resetForProjectSwitch: () => void
}

// --- Store ---

export const useSessionStore = create<SessionState & SessionActions>((set) => ({
  sessions: null,
  activeSessionId: null,

  setSessions: (sessions) => set((s) => {
    const sorted = sortByActivity(sessions)
    // Skip if session IDs haven't changed (avoids duplicate event/RPC updates).
    // The live "task running" state is owned by chatStore.taskActive and read
    // via useSessionStatusIndicator — not by SessionInfo.active — so active
    // toggles no longer need to force a store update here.
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

  // Refresh a session's `has_unfinished_task` outside the list-load snapshot.
  // The list value goes stale the moment a task finishes or a session switch
  // fetches a fresh runtime status — the runtime reconcile (session switch)
  // and the terminal task events push the authoritative value through here so
  // isSessionBusy() stays truthful without an app restart. No-ops when the
  // session is unknown or the value already matches, keeping the `sessions`
  // array reference stable (stable-selector principle: a new array on every
  // call would needlessly re-render every subscriber of `sessions`).
  setUnfinishedTask: (id, value) => set((s) => {
    if (!s.sessions) return s
    const idx = s.sessions.findIndex(sess => sess.id === id)
    if (idx === -1 || s.sessions[idx]!.has_unfinished_task === value) return s
    const sessions = [...s.sessions]
    sessions[idx] = { ...sessions[idx]!, has_unfinished_task: value }
    // The flag change cannot affect activity ordering — return the copy as-is.
    return { sessions }
  }),

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

  resetForProjectSwitch: () => set((s) => {
    if (s.sessions === null && s.activeSessionId === null) return s
    return {
      sessions: null,
      activeSessionId: null,
    }
  }),
}))
