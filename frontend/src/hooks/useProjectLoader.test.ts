// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { apiMocks, switchMocks } = vi.hoisted(() => ({
  apiMocks: {
    listProjects: vi.fn(),
    getLastActiveProjectID: vi.fn(),
  },
  switchMocks: {
    switchProjectWithState: vi.fn(),
  },
}))

vi.mock('@/api/projects', () => apiMocks)
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))
vi.mock('@/hooks/useProjectSwitchState', () => ({
  useProjectSwitchState: () => switchMocks.switchProjectWithState,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { pickMostRecentRealProject, pickStartupRestoreTarget, useProjectLoader } from './useProjectLoader'
import { useProjectStore } from '@/stores/projectStore'
import type { ProjectInfo } from '@/types/models'

const NO_PROJECT_ID = '__no_project__'

function makeProject(overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo {
  return {
    name: overrides.name ?? `Project ${overrides.id}`,
    workspace_path: overrides.workspace_path ?? '/tmp',
    is_external: overrides.is_external ?? false,
    is_no_project: overrides.is_no_project ?? false,
    research_root: overrides.research_root ?? '',
    is_research: overrides.is_research ?? false,
    created_at: overrides.created_at ?? '2026-01-01T00:00:00Z',
    last_active_at: overrides.last_active_at ?? '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('pickMostRecentRealProject', () => {
  it('returns null for an empty list', () => {
    expect(pickMostRecentRealProject([])).toBeNull()
  })

  it('returns null when only No Project exists', () => {
    const projects = [makeProject({ id: 'no-project', is_no_project: true })]
    expect(pickMostRecentRealProject(projects)).toBeNull()
  })

  it('ignores No Project and returns the single real project', () => {
    const noProject = makeProject({ id: 'no-project', is_no_project: true })
    const real = makeProject({ id: 'real-1' })
    expect(pickMostRecentRealProject([noProject, real])).toEqual(real)
  })

  it('picks the most recently active real project', () => {
    const older = makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' })
    const newer = makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' })
    expect(pickMostRecentRealProject([older, newer])).toEqual(newer)
  })

  it('tie-breaks deterministically by timestamp regardless of input order', () => {
    const newer = makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' })
    const older = makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' })
    expect(pickMostRecentRealProject([newer, older])).toEqual(newer)
  })

  it('falls back to created_at when last_active_at is missing', () => {
    const a = makeProject({ id: 'a', last_active_at: '', created_at: '2026-01-01T00:00:00Z' })
    const b = makeProject({ id: 'b', last_active_at: '', created_at: '2026-05-01T00:00:00Z' })
    expect(pickMostRecentRealProject([a, b])).toEqual(b)
  })

  it('falls back to 0 for invalid timestamps without throwing', () => {
    const a = makeProject({ id: 'a', last_active_at: 'not-a-date', created_at: 'also-bad' })
    const b = makeProject({ id: 'b', last_active_at: '2026-05-01T00:00:00Z' })
    expect(pickMostRecentRealProject([a, b])).toEqual(b)
  })

  it('does not mutate the input array', () => {
    const projects = [
      makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' }),
      makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' }),
    ]
    pickMostRecentRealProject(projects)
    expect(projects.map((p) => p.id)).toEqual(['old', 'new'])
  })
})

describe('pickStartupRestoreTarget', () => {
  it('returns null for an empty id', () => {
    const projects = [makeProject({ id: 'real-1' })]
    expect(pickStartupRestoreTarget(projects, '')).toBeNull()
  })

  it('returns null when the id is not in the list (project deleted while closed)', () => {
    const projects = [makeProject({ id: 'real-1' })]
    expect(pickStartupRestoreTarget(projects, 'deleted-project')).toBeNull()
  })

  it('returns the matching real project', () => {
    const a = makeProject({ id: 'a' })
    const b = makeProject({ id: 'b' })
    expect(pickStartupRestoreTarget([a, b], 'b')).toEqual(b)
  })

  it('returns the No Project entry when it matches (CHAT restore)', () => {
    const noProject = makeProject({ id: NO_PROJECT_ID, is_no_project: true })
    const real = makeProject({ id: 'real-1' })
    expect(pickStartupRestoreTarget([noProject, real], NO_PROJECT_ID)).toEqual(noProject)
  })
})

// --- Hook-level startup restore flow ---

let root: Root | null = null
let container: HTMLDivElement | null = null

function Harness() {
  useProjectLoader()
  return null
}

function renderLoader(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Give a fire-and-forget effect chain a chance to settle past a store write. */
async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

beforeEach(() => {
  apiMocks.listProjects.mockReset()
  apiMocks.getLastActiveProjectID.mockReset()
  switchMocks.switchProjectWithState.mockReset()
  switchMocks.switchProjectWithState.mockResolvedValue(undefined)
  useProjectStore.setState({
    projects: null,
    activeProjectId: null,
    lastRealProjectId: null,
    createDialogOpen: false,
  })
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('useProjectLoader — startup restore', () => {
  it('restores CHAT mode when No Project was active at exit', async () => {
    const noProject = makeProject({ id: NO_PROJECT_ID, is_no_project: true })
    const real = makeProject({ id: 'real-a', last_active_at: '2026-06-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([noProject, real])
    apiMocks.getLastActiveProjectID.mockResolvedValue(NO_PROJECT_ID)

    renderLoader()

    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith(NO_PROJECT_ID)
    })
    expect(switchMocks.switchProjectWithState).toHaveBeenCalledTimes(1)
    expect(useProjectStore.getState().createDialogOpen).toBe(false)
  })

  it('restores the last active real project instead of the most recent one', async () => {
    const a = makeProject({ id: 'proj-a', last_active_at: '2026-06-01T00:00:00Z' })
    const b = makeProject({ id: 'proj-b', last_active_at: '2026-01-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([a, b])
    apiMocks.getLastActiveProjectID.mockResolvedValue('proj-b')

    renderLoader()

    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith('proj-b')
    })
    expect(switchMocks.switchProjectWithState).toHaveBeenCalledTimes(1)
    expect(useProjectStore.getState().createDialogOpen).toBe(false)
  })

  it('falls back to the most recent real project when no last active id was persisted', async () => {
    // No Project carries the NEWEST timestamp to prove the CODE-first filter
    // still excludes it from the fallback.
    const noProject = makeProject({ id: NO_PROJECT_ID, is_no_project: true, last_active_at: '2026-07-01T00:00:00Z' })
    const older = makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' })
    const newer = makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([noProject, older, newer])
    apiMocks.getLastActiveProjectID.mockResolvedValue('')

    renderLoader()

    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith('new')
    })
    expect(switchMocks.switchProjectWithState).toHaveBeenCalledTimes(1)
  })

  it('falls back to the most recent real project when the last active project was deleted', async () => {
    const a = makeProject({ id: 'proj-a', last_active_at: '2026-06-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([a])
    apiMocks.getLastActiveProjectID.mockResolvedValue('deleted-project')

    renderLoader()

    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith('proj-a')
    })
    expect(switchMocks.switchProjectWithState).toHaveBeenCalledTimes(1)
  })

  it('falls back to CODE-first when CHAT was active but No Project is missing from the list', async () => {
    const a = makeProject({ id: 'proj-a', last_active_at: '2026-06-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([a])
    apiMocks.getLastActiveProjectID.mockResolvedValue(NO_PROJECT_ID)

    renderLoader()

    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith('proj-a')
    })
  })

  it('opens the Create Project dialog when nothing was persisted and no real project exists', async () => {
    const noProject = makeProject({ id: NO_PROJECT_ID, is_no_project: true })
    apiMocks.listProjects.mockResolvedValue([noProject])
    apiMocks.getLastActiveProjectID.mockResolvedValue('')

    renderLoader()

    await vi.waitFor(() => {
      expect(useProjectStore.getState().createDialogOpen).toBe(true)
    })
    expect(switchMocks.switchProjectWithState).not.toHaveBeenCalled()
  })

  it('does not block startup when the last-active RPC fails', async () => {
    const a = makeProject({ id: 'proj-a', last_active_at: '2026-06-01T00:00:00Z' })
    const b = makeProject({ id: 'proj-b', last_active_at: '2026-01-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([a, b])
    apiMocks.getLastActiveProjectID.mockRejectedValue(new Error('RPC unavailable'))

    renderLoader()

    // Startup continues with the CODE-first default despite the RPC failure.
    await vi.waitFor(() => {
      expect(switchMocks.switchProjectWithState).toHaveBeenCalledWith('proj-a')
    })
    expect(useProjectStore.getState().projects).toHaveLength(2)
    expect(useProjectStore.getState().createDialogOpen).toBe(false)
  })

  it('does not override a project the user activated while the last-active RPC was in flight', async () => {
    let releaseLastActive: ((id: string) => void) | undefined
    const gate = new Promise<string>((resolve) => {
      releaseLastActive = resolve
    })
    const a = makeProject({ id: 'proj-a', last_active_at: '2026-06-01T00:00:00Z' })
    const b = makeProject({ id: 'proj-b', last_active_at: '2026-01-01T00:00:00Z' })
    apiMocks.listProjects.mockResolvedValue([a, b])
    apiMocks.getLastActiveProjectID.mockImplementation(() => gate)

    renderLoader()
    await vi.waitFor(() => {
      expect(useProjectStore.getState().projects).toHaveLength(2)
    })

    // The user switches projects manually while the restore RPC is pending.
    act(() => {
      useProjectStore.getState().setActiveProjectId('proj-user')
    })
    releaseLastActive?.('proj-b')
    await flushMicrotasks()

    expect(switchMocks.switchProjectWithState).not.toHaveBeenCalled()
    expect(useProjectStore.getState().activeProjectId).toBe('proj-user')
  })

  it('does not query the last active id when a project is already active', async () => {
    useProjectStore.setState({ activeProjectId: 'proj-a' })
    const a = makeProject({ id: 'proj-a' })
    apiMocks.listProjects.mockResolvedValue([a])
    apiMocks.getLastActiveProjectID.mockResolvedValue('proj-b')

    renderLoader()

    await vi.waitFor(() => {
      expect(useProjectStore.getState().projects).toHaveLength(1)
    })
    await flushMicrotasks()
    expect(apiMocks.getLastActiveProjectID).not.toHaveBeenCalled()
    expect(switchMocks.switchProjectWithState).not.toHaveBeenCalled()
  })
})
