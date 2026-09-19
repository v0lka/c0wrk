import { beforeEach, describe, it, expect } from 'vitest'
import { useProjectStore } from './projectStore'
import type { ProjectInfo } from '@/types/models'

function makeProject(overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo {
  return {
    name: overrides.name ?? `Project ${overrides.id}`,
    workspace_path: overrides.workspace_path ?? '/tmp',
    is_external: overrides.is_external ?? false,
    is_no_project: overrides.is_no_project ?? false,
    created_at: overrides.created_at ?? '2026-01-01T00:00:00Z',
    last_active_at: overrides.last_active_at ?? '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function resetStore() {
  useProjectStore.setState({
    projects: null,
    activeProjectId: null,
    lastRealProjectId: null,
    createDialogOpen: false,
  })
}

describe('projectStore — createDialogOpen', () => {
  beforeEach(resetStore)

  it('defaults to false', () => {
    expect(useProjectStore.getState().createDialogOpen).toBe(false)
  })

  it('setCreateProjectDialogOpen(true) opens the dialog', () => {
    useProjectStore.getState().setCreateProjectDialogOpen(true)
    expect(useProjectStore.getState().createDialogOpen).toBe(true)
  })

  it('setCreateProjectDialogOpen(false) closes the dialog', () => {
    useProjectStore.getState().setCreateProjectDialogOpen(true)
    useProjectStore.getState().setCreateProjectDialogOpen(false)
    expect(useProjectStore.getState().createDialogOpen).toBe(false)
  })

  it('closing does not affect activeProjectId', () => {
    useProjectStore.getState().setActiveProjectId('real-1')
    useProjectStore.getState().setCreateProjectDialogOpen(true)
    useProjectStore.getState().setCreateProjectDialogOpen(false)
    expect(useProjectStore.getState().activeProjectId).toBe('real-1')
  })
})

describe('projectStore — setProjects (CODE-first ordering)', () => {
  beforeEach(resetStore)

  it('places No Project first regardless of activity', () => {
    const noProject = makeProject({ id: 'no-project', is_no_project: true, last_active_at: '2026-06-01T00:00:00Z' })
    const real = makeProject({ id: 'real-1', last_active_at: '2026-01-01T00:00:00Z' })
    useProjectStore.getState().setProjects([real, noProject])
    const ids = useProjectStore.getState().projects?.map((p) => p.id)
    expect(ids?.[0]).toBe('no-project')
  })

  it('keeps real projects sorted by most recent activity after No Project', () => {
    useProjectStore.getState().setProjects([
      makeProject({ id: 'no-project', is_no_project: true }),
      makeProject({ id: 'old', last_active_at: '2026-01-01T00:00:00Z' }),
      makeProject({ id: 'new', last_active_at: '2026-06-01T00:00:00Z' }),
    ])
    const ids = useProjectStore.getState().projects?.map((p) => p.id)
    expect(ids).toEqual(['no-project', 'new', 'old'])
  })
})

describe('projectStore — setActiveProjectId (lastRealProjectId tracking)', () => {
  beforeEach(resetStore)

  it('tracks the last real (non-No-Project) project id', () => {
    useProjectStore.getState().setProjects([
      makeProject({ id: 'no-project', is_no_project: true }),
      makeProject({ id: 'real-1' }),
    ])
    useProjectStore.getState().setActiveProjectId('real-1')
    expect(useProjectStore.getState().lastRealProjectId).toBe('real-1')
  })

  it('does not record No Project as the last real project', () => {
    useProjectStore.getState().setProjects([
      makeProject({ id: 'no-project', is_no_project: true }),
      makeProject({ id: 'real-1' }),
    ])
    useProjectStore.getState().setActiveProjectId('real-1')
    useProjectStore.getState().setActiveProjectId('no-project')
    expect(useProjectStore.getState().lastRealProjectId).toBe('real-1')
  })
})
