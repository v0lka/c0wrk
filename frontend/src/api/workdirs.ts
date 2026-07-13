// Working-directory management API wrappers.
//
// Auxiliary working directories (project-scoped or session-scoped) are managed
// via five desktop.App RPC methods. The backend persists each record and emits
// a global `workdirs:changed` event on every mutation so the UI can refetch.

import { getApp } from './runtime'
import { pickDirectory } from './projects'
import { logger } from '@/lib/logger'

/** Scope of a working-directory record — selects the owning entity. */
export type WorkDirScope = 'project' | 'session'

/**
 * Backend record (project.WorkDirectoryRecord). JSON keys use snake_case.
 */
export interface WorkDirectoryRecord {
  id: string
  path: string
  description: string
  created_at: string
}

/** Validate that an unknown value matches the WorkDirectoryRecord shape. */
function isWorkDirectoryRecord(v: unknown): v is WorkDirectoryRecord {
  if (typeof v !== 'object' || v === null) return false
  const r = v as Record<string, unknown>
  return typeof r.id === 'string'
    && typeof r.path === 'string'
    && typeof r.description === 'string'
    && typeof r.created_at === 'string'
}

function coerceList(raw: unknown): WorkDirectoryRecord[] {
  if (!Array.isArray(raw)) return []
  return raw.filter(isWorkDirectoryRecord)
}

/** List all project-scoped working directories for a project. */
export async function listProjectWorkDirectories(projectID: string): Promise<WorkDirectoryRecord[]> {
  try {
    const app = getApp()
    return coerceList(await app.ListProjectWorkDirectories(projectID))
  } catch (err) {
    logger.error('Failed to list project work directories:', err)
    throw err
  }
}

/** List all session-scoped working directories for a session. */
export async function listSessionWorkDirectories(sessionID: string): Promise<WorkDirectoryRecord[]> {
  try {
    const app = getApp()
    return coerceList(await app.ListSessionWorkDirectories(sessionID))
  } catch (err) {
    logger.error('Failed to list session work directories:', err)
    throw err
  }
}

/** Create a working directory entry under the given scope. */
export async function addWorkDirectory(
  scope: WorkDirScope,
  ownerID: string,
  path: string,
  description: string,
): Promise<void> {
  try {
    const app = getApp()
    await app.AddWorkDirectory(scope, ownerID, path, description)
  } catch (err) {
    logger.error('Failed to add working directory:', err)
    throw err
  }
}

/** Update the description of an existing working-directory entry. */
export async function updateWorkDirectoryDescription(
  scope: WorkDirScope,
  ownerID: string,
  id: string,
  description: string,
): Promise<void> {
  try {
    const app = getApp()
    await app.UpdateWorkDirectoryDescription(scope, ownerID, id, description)
  } catch (err) {
    logger.error('Failed to update working directory description:', err)
    throw err
  }
}

/** Delete a working-directory entry. */
export async function deleteWorkDirectory(
  scope: WorkDirScope,
  ownerID: string,
  id: string,
): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteWorkDirectory(scope, ownerID, id)
  } catch (err) {
    logger.error('Failed to delete working directory:', err)
    throw err
  }
}

/** Re-exported path picker so callers depend only on this module. */
export { pickDirectory }
