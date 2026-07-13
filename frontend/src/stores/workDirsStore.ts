// Zustand store for auxiliary working directories.
//
// Stable selectors: arrays are stored as direct properties and returned by
// reference from selectors — never allocated inside a useStore selector (React
// 19 useSyncExternalStore compares snapshots by reference; a fresh array on
// every render triggers an infinite re-render loop, error #185).

import { create } from 'zustand'
import {
  listProjectWorkDirectories,
  listSessionWorkDirectories,
  addWorkDirectory,
  updateWorkDirectoryDescription,
  deleteWorkDirectory,
} from '@/api/workdirs'
import type { WorkDirectoryRecord, WorkDirScope } from '@/api/workdirs'
import { logger } from '@/lib/logger'

export type { WorkDirectoryRecord, WorkDirScope } from '@/api/workdirs'

interface WorkDirsState {
  projectDirs: WorkDirectoryRecord[]
  sessionDirs: WorkDirectoryRecord[]
  open: boolean
  loading: boolean
  error: string | null
  /** Context captured at loadAll() time so mutations can refetch. */
  loadedProjectId: string | null
  loadedSessionId: string | null
}

interface WorkDirsActions {
  setOpen: (open: boolean) => void
  loadAll: (projectID: string | null, sessionID: string | null) => Promise<void>
  add: (scope: WorkDirScope, ownerID: string, path: string, description: string) => Promise<void>
  updateDescription: (
    scope: WorkDirScope,
    ownerID: string,
    id: string,
    description: string,
  ) => Promise<void>
  remove: (scope: WorkDirScope, ownerID: string, id: string) => Promise<void>
  /** Clear stored directories (e.g. on project/session switch). */
  clear: () => void
}

/**
 * Mutations do NOT refetch locally. The backend emits a `workdirs:changed`
 * event on every successful mutation, and WorkDirsModal listens for it and
 * refetches via loadAll(). Relying on the single event source avoids a double
 * fetch (store refetch + event refetch) and keeps cross-window sync correct.
 */
export const useWorkDirsStore = create<WorkDirsState & WorkDirsActions>((set) => ({
  projectDirs: [],
  sessionDirs: [],
  open: false,
  loading: false,
  error: null,
  loadedProjectId: null,
  loadedSessionId: null,

  setOpen: (open) => set({ open }),

  loadAll: async (projectID, sessionID) => {
    set({ loading: true, error: null, loadedProjectId: projectID, loadedSessionId: sessionID })
    try {
      const [projectDirs, sessionDirs] = await Promise.all([
        projectID
          ? listProjectWorkDirectories(projectID).catch((err) => {
              logger.error('workDirsStore.loadAll: project list failed', err)
              return [] as WorkDirectoryRecord[]
            })
          : Promise.resolve([] as WorkDirectoryRecord[]),
        sessionID
          ? listSessionWorkDirectories(sessionID).catch((err) => {
              logger.error('workDirsStore.loadAll: session list failed', err)
              return [] as WorkDirectoryRecord[]
            })
          : Promise.resolve([] as WorkDirectoryRecord[]),
      ])
      set({ projectDirs, sessionDirs, loading: false })
    } catch (err) {
      set({ loading: false, error: err instanceof Error ? err.message : String(err) })
    }
  },

  add: async (scope, ownerID, path, description) => {
    await addWorkDirectory(scope, ownerID, path, description)
    // No local refetch — the backend's workdirs:changed event triggers it.
  },

  updateDescription: async (scope, ownerID, id, description) => {
    await updateWorkDirectoryDescription(scope, ownerID, id, description)
  },

  remove: async (scope, ownerID, id) => {
    await deleteWorkDirectory(scope, ownerID, id)
  },

  clear: () =>
    set({
      projectDirs: [],
      sessionDirs: [],
      loadedProjectId: null,
      loadedSessionId: null,
      error: null,
    }),
}))
