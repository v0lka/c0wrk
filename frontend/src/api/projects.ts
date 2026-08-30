// Project management API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isProjectInfo, isProjectSwitchState, isArrayOf } from '@/types/guards'
import type { ProjectInfo, ProjectSwitchState, ProjectSwitchStatePayload } from '@/types/models'

export async function createProject(name: string, externalPath?: string): Promise<ProjectInfo> {
  try {
    const app = getApp()
    const result = await app.CreateProject(name, externalPath ?? '')
    if (!isProjectInfo(result)) {
      throw new Error('createProject: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to create project:', err)
    throw err
  }
}

export async function deleteProject(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteProject(id)
  } catch (err) {
    logger.error('Failed to delete project:', err)
    throw err
  }
}

export async function renameProject(id: string, name: string): Promise<void> {
  try {
    const app = getApp()
    await app.RenameProject(id, name)
  } catch (err) {
    logger.error('Failed to rename project:', err)
    throw err
  }
}

export async function listProjects(): Promise<ProjectInfo[]> {
  try {
    const app = getApp()
    const result = await app.ListProjects()
    if (!isArrayOf(result, isProjectInfo)) {
      logger.error('listProjects: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list projects:', err)
    throw err
  }
}

export async function switchProject(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.SwitchProject(id)
  } catch (err) {
    logger.error('Failed to switch project:', err)
    throw err
  }
}

/**
 * Fetch the project id that was active when the app last exited (persisted by
 * the backend on every project switch). Returns the No Project pseudo-project
 * id when CHAT mode was active, or '' when nothing was persisted yet. Callers
 * must treat a rejection as "no restore data" and fall back to their default
 * startup behavior — this RPC must never block startup.
 */
export async function getLastActiveProjectID(): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetLastActiveProjectID()
    if (typeof result !== 'string') {
      logger.warn('getLastActiveProjectID: unexpected response shape, returning ""', result)
      return ''
    }
    return result
  } catch (err) {
    logger.error('Failed to get last active project ID:', err)
    throw err
  }
}

function pickProjectStateSaveRPC(app: Record<string, (...args: unknown[]) => Promise<unknown>>) {
  return app.SaveProjectSwitchState
    ?? app.SaveProjectUIState
    ?? app.SaveProjectState
}

function pickProjectStateGetRPC(app: Record<string, (...args: unknown[]) => Promise<unknown>>) {
  return app.GetProjectSwitchState
    ?? app.GetProjectUIState
    ?? app.GetProjectState
}

export async function saveProjectSwitchState(payload: ProjectSwitchStatePayload): Promise<void> {
  try {
    const app = getApp()
    const rpc = pickProjectStateSaveRPC(app)
    if (typeof rpc !== 'function') {
      logger.debug('saveProjectSwitchState: backend method unavailable, skipping')
      return
    }
    await rpc(payload)
  } catch (err) {
    logger.error('Failed to save project switch state:', err)
    throw err
  }
}

export async function getProjectSwitchState(projectId: string): Promise<ProjectSwitchState | null> {
  try {
    const app = getApp()
    const rpc = pickProjectStateGetRPC(app)
    if (typeof rpc !== 'function') {
      logger.debug('getProjectSwitchState: backend method unavailable, skipping')
      return null
    }

    const result = await rpc(projectId)
    if (result === null || result === undefined) {
      return null
    }
    if (!isProjectSwitchState(result)) {
      logger.warn('getProjectSwitchState: unexpected response shape', result)
      return null
    }

    const openTabs = Array.isArray(result.open_tabs)
      ? result.open_tabs.filter((tab): tab is string => typeof tab === 'string')
      : []

    return {
      ...result,
      open_tabs: openTabs,
      saved_session_id: typeof result.saved_session_id === 'string' ? result.saved_session_id : '',
      active_file: typeof result.active_file === 'string' ? result.active_file : '',
      updated_at: typeof result.updated_at === 'string' ? result.updated_at : '',
    }
  } catch (err) {
    logger.error('Failed to get project switch state:', err)
    throw err
  }
}

/**
 * Persist the user's session selection as the project's saved_session_id,
 * leaving previously saved open tabs / active file untouched (the backend
 * updates ONLY saved_session_id). Deliberately thin: callers decide how to
 * surface failures — the session store calls this fire-and-forget and logs a
 * warning on rejection, so a failed persist never breaks selection.
 */
export async function saveProjectActiveSession(projectId: string, sessionId: string): Promise<void> {
  const app = getApp()
  await app.SaveProjectActiveSession(projectId, sessionId)
}

export async function pickDirectory(): Promise<string> {
  try {
    const app = getApp()
    const result = await app.PickDirectory()
    if (typeof result !== 'string') {
      throw new Error('pickDirectory: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to pick directory:', err)
    throw err
  }
}
