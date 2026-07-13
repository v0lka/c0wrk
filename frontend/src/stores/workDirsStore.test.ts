// Unit tests for workDirsStore — state transitions and API call wiring.
//
// The @/api/workdirs module is mocked so the store is tested in isolation.

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { useWorkDirsStore } from '@/stores/workDirsStore'
import {
  listProjectWorkDirectories,
  listSessionWorkDirectories,
  addWorkDirectory,
  updateWorkDirectoryDescription,
  deleteWorkDirectory,
} from '@/api/workdirs'
import type { WorkDirectoryRecord } from '@/api/workdirs'

vi.mock('@/api/workdirs', () => ({
  listProjectWorkDirectories: vi.fn(),
  listSessionWorkDirectories: vi.fn(),
  addWorkDirectory: vi.fn(),
  updateWorkDirectoryDescription: vi.fn(),
  deleteWorkDirectory: vi.fn(),
}))

const mockedListProject = vi.mocked(listProjectWorkDirectories)
const mockedListSession = vi.mocked(listSessionWorkDirectories)
const mockedAdd = vi.mocked(addWorkDirectory)
const mockedUpdate = vi.mocked(updateWorkDirectoryDescription)
const mockedDelete = vi.mocked(deleteWorkDirectory)

const PROJ_REC: WorkDirectoryRecord = { id: '1', path: '/a', description: 'A', created_at: 't1' }
const SESS_REC: WorkDirectoryRecord = { id: '2', path: '/b', description: 'B', created_at: 't2' }

function resetStore() {
  useWorkDirsStore.setState({
    projectDirs: [],
    sessionDirs: [],
    open: false,
    loading: false,
    error: null,
    loadedProjectId: null,
    loadedSessionId: null,
  })
}

describe('workDirsStore', () => {
  beforeEach(() => {
    resetStore()
    vi.clearAllMocks()
  })

  // ── Initial state ──

  it('has correct initial state', () => {
    const s = useWorkDirsStore.getState()
    expect(s.projectDirs).toEqual([])
    expect(s.sessionDirs).toEqual([])
    expect(s.open).toBe(false)
    expect(s.loading).toBe(false)
    expect(s.error).toBeNull()
    expect(s.loadedProjectId).toBeNull()
    expect(s.loadedSessionId).toBeNull()
  })

  // ── setOpen ──

  it('setOpen toggles the open flag', () => {
    useWorkDirsStore.getState().setOpen(true)
    expect(useWorkDirsStore.getState().open).toBe(true)
    useWorkDirsStore.getState().setOpen(false)
    expect(useWorkDirsStore.getState().open).toBe(false)
  })

  // ── loadAll ──

  it('loadAll populates both lists and records the context', async () => {
    mockedListProject.mockResolvedValue([PROJ_REC])
    mockedListSession.mockResolvedValue([SESS_REC])

    await useWorkDirsStore.getState().loadAll('proj1', 'sess1')

    expect(mockedListProject).toHaveBeenCalledWith('proj1')
    expect(mockedListSession).toHaveBeenCalledWith('sess1')
    const s = useWorkDirsStore.getState()
    expect(s.projectDirs).toEqual([PROJ_REC])
    expect(s.sessionDirs).toEqual([SESS_REC])
    expect(s.loadedProjectId).toBe('proj1')
    expect(s.loadedSessionId).toBe('sess1')
    expect(s.loading).toBe(false)
  })

  it('loadAll skips fetching when both IDs are null', async () => {
    await useWorkDirsStore.getState().loadAll(null, null)
    expect(mockedListProject).not.toHaveBeenCalled()
    expect(mockedListSession).not.toHaveBeenCalled()
    expect(useWorkDirsStore.getState().projectDirs).toEqual([])
    expect(useWorkDirsStore.getState().sessionDirs).toEqual([])
  })

  // ── add ──

  it('add calls the api (reload is driven by the workdirs:changed event)', async () => {
    mockedListProject.mockResolvedValue([PROJ_REC])
    mockedListSession.mockResolvedValue([SESS_REC])
    await useWorkDirsStore.getState().loadAll('proj1', 'sess1')
    vi.clearAllMocks()

    mockedAdd.mockResolvedValue(undefined)

    await useWorkDirsStore.getState().add('project', 'proj1', '/c', 'new')

    expect(mockedAdd).toHaveBeenCalledWith('project', 'proj1', '/c', 'new')
    // #6a: store actions no longer refetch — the workdirs:changed event drives reload.
    expect(mockedListProject).not.toHaveBeenCalled()
  })

  // ── updateDescription ──

  it('updateDescription calls the api with owner scope-guard', async () => {
    mockedListProject.mockResolvedValue([])
    mockedListSession.mockResolvedValue([SESS_REC])
    await useWorkDirsStore.getState().loadAll('proj1', 'sess1')
    vi.clearAllMocks()

    mockedUpdate.mockResolvedValue(undefined)

    await useWorkDirsStore.getState().updateDescription('session', 'sess1', '2', 'updated')

    expect(mockedUpdate).toHaveBeenCalledWith('session', 'sess1', '2', 'updated')
  })

  // ── remove ──

  it('remove calls the api with owner scope-guard', async () => {
    mockedListProject.mockResolvedValue([])
    mockedListSession.mockResolvedValue([SESS_REC])
    await useWorkDirsStore.getState().loadAll('proj1', 'sess1')
    vi.clearAllMocks()

    mockedDelete.mockResolvedValue(undefined)

    await useWorkDirsStore.getState().remove('session', 'sess1', '2')

    expect(mockedDelete).toHaveBeenCalledWith('session', 'sess1', '2')
    // #6a: no local refetch — event-driven.
    expect(mockedListSession).not.toHaveBeenCalled()
  })

  // ── clear ──

  it('clear resets directories and context', async () => {
    mockedListProject.mockResolvedValue([PROJ_REC])
    mockedListSession.mockResolvedValue([SESS_REC])
    await useWorkDirsStore.getState().loadAll('proj1', 'sess1')

    useWorkDirsStore.getState().clear()

    const s = useWorkDirsStore.getState()
    expect(s.projectDirs).toEqual([])
    expect(s.sessionDirs).toEqual([])
    expect(s.loadedProjectId).toBeNull()
    expect(s.loadedSessionId).toBeNull()
  })
})
