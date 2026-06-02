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
      logger.warn('createProject: unexpected response shape', result)
    }
    return result as ProjectInfo
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
      logger.warn('listProjects: unexpected response shape', result)
    }
    return result as ProjectInfo[]
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
    const app = getApp() as Record<string, (...args: unknown[]) => Promise<unknown>>
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
    const app = getApp() as Record<string, (...args: unknown[]) => Promise<unknown>>
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

export async function pickDirectory(): Promise<string> {
  try {
    const app = getApp()
    return await app.PickDirectory() as string
  } catch (err) {
    logger.error('Failed to pick directory:', err)
    throw err
  }
}
