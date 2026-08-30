import { useCallback } from 'react'
import { createSession, listSessions } from '@/api/sessions'
import { getProjectSwitchState, saveProjectSwitchState, switchProject } from '@/api/projects'
import { logger } from '@/lib/logger'
import { resolveRestoreSession } from '@/lib/sessionRestore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo } from '@/types/models'

async function performSwitch(nextProjectId: string, seq: number): Promise<void> {
  // A superseded switch must not touch state: its queue slot was released by
  // the watchdog while a NEWER switch is already running (or done). Every
  // store write and every follow-up RPC below is guarded — otherwise the
  // late body of switch N reverts activeProjectId/activeSessionId set by
  // switch N+1, leaving the frontend on the older project while the backend
  // is on the newer one. Under that desync ListDirectory persistently
  // rejects the frontend's rootPath and @-file completions stay empty until
  // an app restart.
  const superseded = (): boolean => seq !== switchSeq
  const abortIfSuperseded = (): boolean => {
    if (!superseded()) return false
    logger.warn(`Project switch to "${nextProjectId}" was superseded while in flight; discarding its remaining state writes`)
    return true
  }
  // First guard: a body found stale as early as its first await (the watchdog
  // overlap case) skips even the best-effort source-state save below, keeping
  // the guard symmetric — every RPC in this function has one after it. A body
  // suspended INSIDE the save RPC resumes past this point and is caught by
  // the follow-up guard instead.
  if (abortIfSuperseded()) return

  const projectState = useProjectStore.getState()
  const currentProjectId = projectState.activeProjectId

  if (!nextProjectId || nextProjectId === currentProjectId) {
    return
  }

  // No previously active project ⇒ this is the initial (startup) activation.
  // The file-viewer store was already rehydrated from localStorage with
  // exactly the tabs that were open at the last shutdown — the
  // authoritative, fresh source for open tabs (see fileViewerStore
  // `partialize`). The backend per-project open-tabs snapshot below, by
  // contrast, is only persisted when switching *away* from a project, so on
  // restart it is stale and may still reference tabs the user already
  // dismissed (e.g. a closed plan). Applying it on startup would silently
  // reopen those stale tabs, so we must NOT restore it here — trust
  // localStorage. The saved session id is unaffected by this staleness: it
  // is also persisted on every explicit selection (see selectSession), so
  // the restore below may trust it even on the initial activation.
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
  // A body suspended INSIDE the save RPC above (watchdog overlap) resumes
  // right here — past the early guard at the top of the function — so this
  // follow-up guard is what stops its stale writes.
  if (abortIfSuperseded()) return

  await switchProject(nextProjectId)
  if (abortIfSuperseded()) return

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
  if (abortIfSuperseded()) return

  // NOTE: on the initial (startup) activation we intentionally keep the
  // localStorage-rehydrated tabs untouched and skip this restore. The backend
  // savedTabs are stale on restart (only written on switch-away), so applying
  // them would reopen tabs the user already closed (e.g. a dismissed plan).
  if (!isInitialActivation) {
    fileViewer.restoreProjectFiles(savedTabs, savedActiveFile)
  }

  let sessions: SessionInfo[] = []
  let sessionsLoaded = false
  try {
    sessions = await listSessions()
    sessionsLoaded = true
  } catch (error) {
    logger.warn('Failed to list sessions during project switch restore', error)
  }
  // The store write must sit BELOW the supersede guard: resuming from the
  // listSessions suspension with a newer switch already running means this
  // stale list must never land — setSessions would clobber the newer
  // switch's session list while activeProjectId points into the new
  // project. An empty-but-successful list still clears the previous
  // project's sessions, so it is written too.
  if (abortIfSuperseded()) return
  if (sessionsLoaded) {
    sessionStore.setSessions(sessions)
  }

  // Deterministic restore order (see lib/sessionRestore):
  // 1) the project's saved session — valid while it still exists and is not
  //    archived. It is persisted on every explicit selection (selectSession)
  //    and on switch-away above, so it is authoritative and wins even over
  //    sessions with newer activity.
  // 2) the latest NON-archived session by activity — fallback for an empty,
  //    deleted, or archived saved entry (archived sessions are never
  //    candidates; the backend list RPCs do not filter them out).
  // 3) create a new session for an empty project.
  const restoredSession = resolveRestoreSession(sessions, savedSessionId)
  if (restoredSession) {
    sessionStore.setActiveSessionId(restoredSession.id)
    return
  }
  if (abortIfSuperseded()) return

  try {
    const created = await createSession()
    if (abortIfSuperseded()) return
    sessionStore.addSession(created)
    sessionStore.setActiveSessionId(created.id)
  } catch (error) {
    logger.error('Failed to create fallback session after project switch restore', error)
    if (!superseded()) {
      sessionStore.setActiveSessionId(null)
    }
  }
}

/**
 * Serialize project switches: each switch's full state-mutating body
 * (backend RPC + store writes + session reload) must COMPLETE before the
 * next one starts. Rapid CHAT↔CODE toggles used to run concurrently and
 * interleave their store writes — e.g. activeSessionId picked from another
 * project's listSessions snapshot — leaving the file-tree rootPath pointing
 * outside the backend's active project. ListDirectory then rejects that
 * root ("path outside project workspace") on every call and @-file
 * completions in the chat input stay empty until an app restart.
 */
let switchChain: Promise<void> = Promise.resolve()

/**
 * Monotonic sequence of switch requests. Each queued switch captures its
 * sequence at enqueue time; when the watchdog releases the queue while an
 * earlier switch's body is still running, the stale body detects
 * `seq !== switchSeq` after every await and discards its remaining store
 * writes instead of reverting the newer switch's state (see performSwitch).
 */
let switchSeq = 0

/**
 * Watchdog budget for one queued switch: waiting for its predecessor plus
 * running its own body. Wails bindings have no built-in timeout, so without
 * this bound a single hung RPC (e.g. a stuck backend switchProject) would
 * keep every later toggle queued forever — the CHAT↔CODE toggles would
 * silently stop responding. When the watchdog fires, the queue link settles
 * (releasing the next queued switch) and the affected caller's promise
 * rejects with a timeout error. If the hung switch's RPC settles afterwards,
 * its late body resumes — but every store write is guarded by the switch
 * sequence (see performSwitch), so a superseded body discards its remaining
 * writes instead of reverting the newer switch's state.
 */
const SWITCH_QUEUE_TIMEOUT_MS = 30_000

function withSwitchTimeout<T>(promise: Promise<T>, nextProjectId: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      logger.warn(`Project switch to "${nextProjectId}" exceeded the ${SWITCH_QUEUE_TIMEOUT_MS}ms watchdog; releasing the switch queue`)
      reject(new Error(`project switch to "${nextProjectId}" timed out after ${SWITCH_QUEUE_TIMEOUT_MS}ms (previous switch hung or backend RPC stuck)`))
    }, SWITCH_QUEUE_TIMEOUT_MS)
    promise.then(
      (value) => {
        clearTimeout(timer)
        resolve(value)
      },
      (error) => {
        clearTimeout(timer)
        reject(error)
      },
    )
  })
}

export function useProjectSwitchState() {
  return useCallback((nextProjectId: string): Promise<void> => {
    const run = switchChain.then(
      // The sequence number is captured when the body STARTS, not when the
      // switch is requested: a merely-queued newer switch does not supersede
      // the currently-running body (it cannot start until the chain link
      // settles). Only a body that is still in flight when a NEWER body has
      // begun (the watchdog overlap case) must discard its remaining writes.
      () => performSwitch(nextProjectId, ++switchSeq),
      // The chain itself never rejects — a failed switch must not poison
      // later ones — but the caller still observes the original error via
      // the returned promise.
      () => performSwitch(nextProjectId, ++switchSeq),
    )
    const bounded = withSwitchTimeout(run, nextProjectId)
    // The chain link settles when the switch completes OR its watchdog
    // fires — either way the next queued switch is allowed to start. The
    // superseded early body (seq guard in performSwitch) keeps the late
    // writes of a watchdog-released switch from corrupting the newer one.
    switchChain = bounded.then(undefined, () => {})
    return bounded
  }, [])
}
