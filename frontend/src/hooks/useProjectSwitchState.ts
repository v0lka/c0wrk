import { useCallback } from 'react'
import { createSession, listSessions } from '@/api/sessions'
import { getProjectSwitchState, saveProjectSwitchState, switchProject } from '@/api/projects'
import { logger } from '@/lib/logger'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo } from '@/types/models'

function isSessionForProject(sessionId: string, sessions: SessionInfo[]): boolean {
  return sessions.some((session) => session.id === sessionId)
}

function getSessionActivityMs(session: SessionInfo): number {
  const timestamp = Date.parse(session.last_active_at || session.created_at)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

function pickLatestSession(sessions: SessionInfo[]): SessionInfo | null {
  if (sessions.length === 0) {
    return null
  }

  return sessions.slice(1).reduce<SessionInfo>((latest, candidate) => {
    return getSessionActivityMs(candidate) > getSessionActivityMs(latest) ? candidate : latest
  }, sessions[0]!)
}

export function useProjectSwitchState() {
  return useCallback(async (nextProjectId: string): Promise<void> => {
    const projectState = useProjectStore.getState()
    const currentProjectId = projectState.activeProjectId

    if (!nextProjectId || nextProjectId === currentProjectId) {
      return
    }

    // No previously active project ⇒ this is the initial (startup) activation.
    // The file-viewer store was already rehydrated from localStorage with
    // exactly the tabs that were open at the last shutdown — the
    // authoritative, fresh source for open tabs (see fileViewerStore
    // `partialize`). The backend per-project switch state below, by contrast,
    // is only persisted when switching *away* from a project, so on restart it
    // is stale and may still reference tabs the user already dismissed (e.g. a
    // closed plan). Applying it on startup would silently reopen those stale
    // tabs, so we must NOT restore from the backend here — trust localStorage.
    // Mirrors the savedSessionId staleness handling at the bottom of this hook.
    const isInitialActivation = !currentProjectId

    // Best-effort save of source project UI state before changing active project.
    if (currentProjectId) {
      try {
        const fileViewer = useFileViewerStore.getState()
        const sessionStore = useSessionStore.getState()

        await saveProjectSwitchState({
          project_id: currentProjectId,
          open_tabs: fileViewer.openTabs,
          active_file: fileViewer.activeFile ?? undefined,
          saved_session_id: sessionStore.activeSessionId ?? undefined,
        })
      } catch (error) {
        logger.warn('Failed to persist source project switch state; continuing switch', error)
      }
    }

    await switchProject(nextProjectId)

    const sessionStore = useSessionStore.getState()
    const fileViewer = useFileViewerStore.getState()

    // Clear previous project session state before loading destination sessions.
    sessionStore.resetForProjectSwitch()

    useProjectStore.getState().setActiveProjectId(nextProjectId)

    let savedSessionId = ''
    let savedTabs: string[] = []
    let savedActiveFile: string | null = null

    try {
      const saved = await getProjectSwitchState(nextProjectId)
      if (saved) {
        savedSessionId = saved.saved_session_id
        savedTabs = saved.open_tabs
        savedActiveFile = saved.active_file || null
      }
    } catch (error) {
      logger.warn('Failed to restore persisted project switch state; using fallback', error)
    }

    // NOTE: on the initial (startup) activation we intentionally keep the
    // localStorage-rehydrated tabs untouched and skip this restore. The backend
    // savedTabs are stale on restart (only written on switch-away), so applying
    // them would reopen tabs the user already closed (e.g. a dismissed plan).
    if (!isInitialActivation) {
      fileViewer.restoreProjectFiles(savedTabs, savedActiveFile)
    }

    let sessions: SessionInfo[] = []
    try {
      sessions = await listSessions()
      sessionStore.setSessions(sessions)
    } catch (error) {
      logger.warn('Failed to list sessions during project switch restore', error)
    }

    // Deterministic fallback:
    // 1) latest session by last_active_at (most reliable — based on actual activity)
    // 2) saved session from previous project switch (only valid if it still exists)
    // 3) create new session for empty project
    //
    // IMPORTANT: pickLatestSession must come FIRST. On app restart, savedSessionId
    // is stale because it's only persisted when switching *away* from a project.
    // If the user created newer sessions after the last project switch but before
    // closing the app, savedSessionId would point to an older session. Picking the
    // latest by last_active_at prevents this stale-session bug.
    const latestSession = pickLatestSession(sessions)
    if (latestSession) {
      sessionStore.setActiveSessionId(latestSession.id)
      return
    }

    if (savedSessionId && isSessionForProject(savedSessionId, sessions)) {
      sessionStore.setActiveSessionId(savedSessionId)
      return
    }

    try {
      const created = await createSession()
      sessionStore.addSession(created)
      sessionStore.setActiveSessionId(created.id)
    } catch (error) {
      logger.error('Failed to create fallback session after project switch restore', error)
      sessionStore.setActiveSessionId(null)
    }
  }, [])
}
