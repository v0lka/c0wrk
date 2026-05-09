// Session loader — loads sessions when active project changes.

import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { listSessions } from '@/api/sessions'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { isSessionInfo, isArrayOf } from '@/types/guards'

export function useSessionLoader(): void {
  const activeProjectId = useProjectStore(s => s.activeProjectId)

  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false

    const cleanups: Array<() => void> = []
    const store = () => useSessionStore.getState()

    // Subscribe to sessions:loaded for push updates
    cleanups.push(
      subscribe('sessions:loaded', (data: unknown) => {
        if (cancelled) return
        if (!Array.isArray(data) || !isArrayOf(data, isSessionInfo)) return
        store().setSessions(data)
        // Auto-select first if none active
        if (!store().activeSessionId && data.length > 0) {
          store().setActiveSessionId(data[0]!.id)
        }
      }),
    )

    // Fetch sessions for active project
    listSessions()
      .then((sessions) => {
        if (cancelled) return
        store().setSessions(sessions)
        if (!store().activeSessionId && sessions.length > 0) {
          store().setActiveSessionId(sessions[0]!.id)
        }
      })
      .catch(() => { /* ignore */ })

    return () => {
      cancelled = true
      cleanups.forEach(fn => fn())
    }
  }, [activeProjectId])
}
