import { describe, it, expect, beforeEach } from 'vitest'
import {
  useResearchStore,
  selectEnabled,
  selectActiveProject,
} from './researchStore'
import type { ResearchStatus } from '@/types/models'

function statusOf(enabled: boolean, projectId = 'proj-1'): ResearchStatus {
  return {
    enabled,
    project_id: projectId,
    research_root: enabled ? '/ws/.research' : '',
    root: enabled
      ? {
          path: '/ws/.research',
          index: [],
          projects: [
            {
              id: 'r1',
              brief: { id: 'r1', title: 'Brief' },
              graph: {
                nodes: [{ id: 'h1', title: 'H1', status: 'open' }],
                edges: [],
              },
              metrics: {
                total: 1,
                by_status: { open: 1 },
                confirmation_rate: 0,
                depth: 1,
                breadth: 1,
              },
              prior_art_count: 0,
              has_report: false,
              log: [],
            },
          ],
        }
      : undefined,
  }
}

describe('researchStore', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('starts empty', () => {
    const s = useResearchStore.getState()
    expect(s.status).toBeNull()
    expect(s.isLoading).toBe(false)
    expect(s.isToggling).toBe(false)
    expect(s.error).toBeNull()
    expect(s.projectId).toBeNull()
  })

  it('loadStatus stores status, stamps projectId, clears loading+error', () => {
    useResearchStore.getState().setLoading(true)
    useResearchStore.getState().setError('boom')
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')

    const s = useResearchStore.getState()
    expect(s.status?.enabled).toBe(true)
    expect(s.projectId).toBe('proj-1')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('reset clears everything back to initial', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    useResearchStore.getState().setToggling(true)
    useResearchStore.getState().reset()
    const s = useResearchStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBeNull()
    expect(s.isToggling).toBe(false)
  })

  it('setToggling / setLoading / setError mutate only their slice', () => {
    useResearchStore.getState().setToggling(true)
    expect(useResearchStore.getState().isToggling).toBe(true)
    useResearchStore.getState().setLoading(true)
    expect(useResearchStore.getState().isLoading).toBe(true)
    useResearchStore.getState().setError('err')
    expect(useResearchStore.getState().error).toBe('err')
  })
})

describe('researchStore selectors', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('selectEnabled is false when no status loaded', () => {
    expect(selectEnabled(useResearchStore.getState())).toBe(false)
  })

  it('selectEnabled reflects status.enabled', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    expect(selectEnabled(useResearchStore.getState())).toBe(true)
    useResearchStore.getState().loadStatus(statusOf(false), 'proj-1')
    expect(selectEnabled(useResearchStore.getState())).toBe(false)
  })

  it('selectActiveProject returns the first project when on, null when off', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('r1')
    useResearchStore.getState().loadStatus(statusOf(false), 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())).toBeNull()
  })

  it('selectActiveProject follows active_project_id over projects[0] ordering', () => {
    // Two projects: r1 (sorted first) and r2. The active project is r2 (the
    // latest), carried as root.active_project_id by the backend. The selector
    // must follow it, not blindly pick projects[0].
    const status: ResearchStatus = {
      enabled: true,
      project_id: 'proj-1',
      research_root: '/ws/.research',
      root: {
        path: '/ws/.research',
        index: [],
        active_project_id: 'r2',
        projects: [
          {
            id: 'r1',
            brief: { id: 'r1', title: 'First' },
            graph: { nodes: [], edges: [] },
            metrics: {
              total: 0,
              by_status: {},
              confirmation_rate: 0,
              depth: 0,
              breadth: 0,
            },
            prior_art_count: 0,
            has_report: false,
            log: [],
          },
          {
            id: 'r2',
            brief: { id: 'r2', title: 'Second (latest)' },
            graph: { nodes: [], edges: [] },
            metrics: {
              total: 0,
              by_status: {},
              confirmation_rate: 0,
              depth: 0,
              breadth: 0,
            },
            prior_art_count: 0,
            has_report: false,
            log: [],
          },
        ],
      },
    }
    useResearchStore.getState().loadStatus(status, 'proj-1')
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('r2')
  })

  it('selectors return stable primitives/references (no per-call allocation)', () => {
    useResearchStore.getState().loadStatus(statusOf(true), 'proj-1')
    const st = useResearchStore.getState()
    // selectActiveProject returns the same object reference both calls.
    expect(selectActiveProject(st)).toBe(selectActiveProject(st))
    // selectEnabled is a primitive boolean.
    expect(typeof selectEnabled(st)).toBe('boolean')
  })
})
