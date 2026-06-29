// Session loader — loads sessions when active project changes.

import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { listSessions } from '@/api/sessions'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { isSessionInfo, isArrayOf } from '@/types/guards'
import type { SessionInfo } from '@/types/models'

// --- Helpers (mirror useProjectSwitchState for consistency) ---

function getSessionActivityMs(session: SessionInfo): number {
  const timestamp = Date.parse(session.last_active_at || session.created_at)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

/** Iterates through all sessions to find the one with the highest activity timestamp. */
function pickLatestSession(sessions: SessionInfo[]): SessionInfo | null {
  if (sessions.length === 0) {
    return null
  }
  return sessions.slice(1).reduce<SessionInfo>((latest, candidate) => {
    return getSessionActivityMs(candidate) > getSessionActivityMs(latest) ? candidate : latest
  }, sessions[0]!)
}

export function useSessionLoader(): void {
  const activeProjectId = useProjectStore(s => s.activeProjectId)

  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false

    const cleanups: Array<() => void> = []
    const store = () => useSessionStore.getState()

    // Clear stale active session from previous project while destination sessions are loading.
    store().setActiveSessionId(null)

    // Subscribe to sessions:loaded for push updates.
    // Guard against cross-project contamination: only accept sessions whose
    // project_id matches the currently active project.
    cleanups.push(
      subscribe('sessions:loaded', (data: unknown) => {
        if (cancelled) return
        if (!Array.isArray(data) || !isArrayOf(data, isSessionInfo)) return
        // Re-validate activeProjectId at handler time to avoid stale closures.
        const currentProjectId = useProjectStore.getState().activeProjectId
        if (!currentProjectId) return
        // Filter to sessions belonging to the active project.
        const owned = data.filter(s => s.project_id === currentProjectId)
        if (owned.length === 0) return
        store().setSessions(owned)
        // Auto-select most recent session using deterministic pickLatestSession
        // (iterates all sessions rather than trusting sorted order alone).
        const state = store()
        if (!state.activeSessionId) {
          const latest = pickLatestSession(owned)
          if (latest) {
            store().setActiveSessionId(latest.id)
          }
        }
      }),
    )

    // Fetch sessions for active project
    listSessions()
      .then((sessions) => {
        if (cancelled) return
        store().setSessions(sessions)
        // Auto-select most recent session using deterministic pickLatestSession.
        // Uses the same logic as useProjectSwitchState for consistency.
        const state = store()
        if (!state.activeSessionId) {
          const latest = pickLatestSession(sessions)
          if (latest) {
            store().setActiveSessionId(latest.id)
          }
        }
      })
      .catch(() => { /* ignore */ })

    return () => {
      cancelled = true
      cleanups.forEach(fn => fn())
    }
  }, [activeProjectId])
}
