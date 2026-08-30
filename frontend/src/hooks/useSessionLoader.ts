// Session loader — loads sessions when active project changes.

import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { getProjectSwitchState } from '@/api/projects'
import { listSessions } from '@/api/sessions'
import { resolveRestoreSession } from '@/lib/sessionRestore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { isSessionInfo, isArrayOf, isSessionRenamed } from '@/types/guards'
import type { SessionInfo } from '@/types/models'

export function useSessionLoader(): void {
  const activeProjectId = useProjectStore(s => s.activeProjectId)

  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false
    // The project's saved session id, fetched once per activation (below) and
    // shared by the initial load and the sessions:loaded push handler. It is
    // the primary restore candidate — persisted on every explicit selection
    // (selectSession → saveProjectActiveSession) and on switch-away, so it is
    // fresh; latest-by-activity is only the fallback for a missing or
    // archived entry (see lib/sessionRestore).
    let savedSessionId = ''
    // Released once the saved-session pointer has been fetched (successfully
    // or not). The sessions:loaded push handler awaits it before restoring:
    // a push that arrives while the initial fetch is still in flight must not
    // restore the latest-by-activity fallback in place of the saved session.
    let resolveSavedSessionIdReady!: () => void
    const savedSessionIdReady = new Promise<void>((resolve) => {
      resolveSavedSessionIdReady = resolve
    })

    const cleanups: Array<() => void> = []
    const store = () => useSessionStore.getState()

    // Clear stale active session from previous project while destination sessions are loading.
    store().setActiveSessionId(null)

    // Restore only while nothing is active yet: an explicit selection or an
    // already-completed restore must never be overridden.
    const restoreIfIdle = (sessions: SessionInfo[]) => {
      if (store().activeSessionId) return
      const restored = resolveRestoreSession(sessions, savedSessionId)
      if (restored) {
        store().setActiveSessionId(restored.id)
      }
    }

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
        // Wait for the saved-session pointer before touching the store: the
        // initial fetch may still be in flight, and restoring now would pick
        // the latest-by-activity fallback instead of the saved session (the
        // fallback then sticks — restoreIfIdle never overrides an active id).
        void savedSessionIdReady.then(() => {
          if (cancelled) return
          store().setSessions(owned)
          restoreIfIdle(owned)
        })
      }),
    )

    // Fetch sessions for the active project together with its saved-session
    // pointer (read once per activation). Both are best-effort: a failed
    // saved-state read just degrades the restore to the latest-by-activity
    // fallback, a failed session list leaves the store untouched.
    void Promise.all([
      getProjectSwitchState(activeProjectId).catch(() => null),
      listSessions().catch(() => null),
    ]).then(([saved, sessions]) => {
      // Capture the pointer and release the push gate in EVERY outcome —
      // including a failed session-list fetch — so later pushes still
      // restore against the saved pointer.
      savedSessionId = saved?.saved_session_id ?? ''
      resolveSavedSessionIdReady()
      if (cancelled || sessions === null) return
      store().setSessions(sessions)
      restoreIfIdle(sessions)
    })

    // Global session rename (manual or background auto-titling). Updates the
    // session title in the sidebar even when the renamed session is not the
    // active one — the session-scoped `session:{id}:session_renamed` event has
    // no listener for non-active sessions, so without this the title stays
    // stale ("Session <id>") until a project switch or app reload.
    // updateSession is a no-op when the session is not in the current list.
    cleanups.push(
      subscribe('session:renamed', (data: unknown) => {
        if (cancelled) return
        if (!isSessionRenamed(data)) return
        store().updateSession(data.id, { name: data.name })
      }),
    )

    return () => {
      cancelled = true
      cleanups.forEach(fn => fn())
    }
  }, [activeProjectId])
}
