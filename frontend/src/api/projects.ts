// Project management API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isProjectInfo, isArrayOf } from '@/types/guards'
import type { ProjectInfo } from '@/types/models'

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

export async function pickDirectory(): Promise<string> {
  try {
    const app = getApp()
    return await app.PickDirectory() as string
  } catch (err) {
    logger.error('Failed to pick directory:', err)
    throw err
  }
}
