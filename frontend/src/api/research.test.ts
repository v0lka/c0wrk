// Unit tests for api/research.ts — boundary guards and node-shape sanitizing
// on the graph/status RPC paths ([47]: per-entry fail-closed).

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import {
  getResearchGraph,
  getResearchStatus,
  setActiveResearch,
  deleteResearch,
  setResearchPinned,
  setHypothesisPinned,
} from '@/api/research'

const validNode = { id: 'H-001', title: 'Root cause', status: 'open', parents: [] }

describe('getResearchGraph boundary validation', () => {
  beforeEach(() => {
    delete mockApp.GetResearchGraph
  })

  it('drops malformed node entries instead of crashing the render path', async () => {
    mockApp.GetResearchGraph = vi.fn(() =>
      Promise.resolve({
        project_id: 'R-001',
        has_report: false,
        graph: {
          nodes: [
            validNode,
            { id: 'H-002' }, // missing title/status → dropped
            { id: 7, title: 'bad id', status: 'open' }, // non-string id → dropped
            { id: 'H-003', title: 'ok', status: 'open', parents: 'not-an-array' }, // malformed parents → dropped
          ],
          edges: [],
        },
        metrics: {},
        log: [],
      }),
    )

    const res = await getResearchGraph('R-001')

    expect(res.graph.nodes).toEqual([validNode])
  })

  it('drops nodes with malformed long-form fields but keeps valid ones', async () => {
    mockApp.GetResearchGraph = vi.fn(() =>
      Promise.resolve({
        project_id: 'R-001',
        has_report: false,
        graph: {
          nodes: [
            {
              ...validNode,
              statement: 'Bundles can be parsed.',
              verification_criterion: 'Recover 95%.',
              experiment_notes: 'Run 1 passed.',
              decision: 'continue',
              timebox: '5 days',
              result: 'Recovered 97%.',
            },
            { ...validNode, id: 'H-004', statement: 42 }, // non-string statement → dropped
            { ...validNode, id: 'H-005', decision: ['continue'] }, // non-string decision → dropped
            { ...validNode, id: 'H-006', experiment_notes: null }, // null long-form is fine (absent)
          ],
          edges: [],
        },
        metrics: {},
        log: [],
      }),
    )

    const res = await getResearchGraph('R-001')

    expect(res.graph.nodes.map((n) => n.id)).toEqual(['H-001', 'H-006'])
    expect(res.graph.nodes[0]).toMatchObject({
      statement: 'Bundles can be parsed.',
      verification_criterion: 'Recover 95%.',
      experiment_notes: 'Run 1 passed.',
      decision: 'continue',
    })
  })

  it('normalizes backend null slices to empty arrays', async () => {
    mockApp.GetResearchGraph = vi.fn(() =>
      Promise.resolve({
        project_id: 'R-001',
        has_report: false,
        graph: { nodes: null, edges: null },
        metrics: {},
        log: null,
      }),
    )

    const res = await getResearchGraph('R-001')

    expect(res.graph.nodes).toEqual([])
    expect(res.graph.edges).toEqual([])
    expect(res.log).toEqual([])
  })

  it('rejects a payload whose outer shape is invalid', async () => {
    mockApp.GetResearchGraph = vi.fn(() => Promise.resolve({ nope: true }))

    await expect(getResearchGraph('R-001')).rejects.toThrow(/Invalid research graph/)
  })
})

describe('getResearchStatus boundary validation', () => {
  beforeEach(() => {
    delete mockApp.GetResearchStatus
  })

  it('drops malformed node entries in every project graph', async () => {
    mockApp.GetResearchStatus = vi.fn(() =>
      Promise.resolve({
        enabled: true,
        project_id: 'R-001',
        research_root: '/ws/.research',
        root: {
          path: '/ws/.research',
          projects: [
            {
              id: 'R-001',
              graph: { nodes: [validNode, { bogus: true }], edges: [] },
              log: null,
            },
          ],
        },
      }),
    )

    const res = await getResearchStatus('R-001')

    expect(res.root?.projects[0]!.graph.nodes).toEqual([validNode])
    expect(res.root?.projects[0]!.log).toEqual([])
  })

  it('normalizes missing/null pins to empty collections', async () => {
    mockApp.GetResearchStatus = vi.fn(() =>
      Promise.resolve({
        enabled: false,
        project_id: 'R-001',
        research_root: '',
        pinned_research: null,
        pinned_hypotheses: null,
      }),
    )

    const res = await getResearchStatus('R-001')

    expect(res.pinned_research).toEqual([])
    expect(res.pinned_hypotheses).toEqual({})
  })

  it('keeps well-formed pins (same-key multi-project card lists)', async () => {
    mockApp.GetResearchStatus = vi.fn(() =>
      Promise.resolve({
        enabled: true,
        project_id: 'p1',
        research_root: '/ws/.research',
        pinned_research: ['R-001-web/brief.md'],
        pinned_hypotheses: {
          'H-001': [
            'R-001-web/hypotheses/H-001.md',
            'R-002-other/hypotheses/H-001.md',
          ],
        },
      }),
    )

    const res = await getResearchStatus('p1')

    expect(res.pinned_research).toEqual(['R-001-web/brief.md'])
    expect(res.pinned_hypotheses?.['H-001']).toEqual([
      'R-001-web/hypotheses/H-001.md',
      'R-002-other/hypotheses/H-001.md',
    ])
  })

  it('rejects malformed pins instead of accepting them (fail-closed)', async () => {
    const cases: Record<string, unknown>[] = [
      { pinned_research: 'R-001/brief.md' }, // not an array
      { pinned_research: [42] }, // non-string entry
      { pinned_hypotheses: 'nope' }, // not a record
      { pinned_hypotheses: { 'H-001': 'R-001/hypotheses/H-001.md' } }, // non-array value
      { pinned_hypotheses: { 'H-001': [7] } }, // non-string entry in a value
    ]
    for (const pins of cases) {
      mockApp.GetResearchStatus = vi.fn(() =>
        Promise.resolve({
          enabled: true,
          project_id: 'p1',
          research_root: '/ws/.research',
          ...pins,
        }),
      )
      await expect(getResearchStatus('p1')).rejects.toThrow(/Invalid research status/)
    }
  })
})

describe('setActiveResearch / deleteResearch boundary validation', () => {
  beforeEach(() => {
    delete mockApp.SetActiveResearch
    delete mockApp.DeleteResearch
  })

  it('validates and normalizes the refreshed status (incl. pins)', async () => {
    mockApp.SetActiveResearch = vi.fn(() =>
      Promise.resolve({
        enabled: true,
        project_id: 'p1',
        research_root: '/ws/.research',
        pinned_research: null,
        pinned_hypotheses: null,
      }),
    )

    const res = await setActiveResearch('p1', 'R-002')

    expect(mockApp.SetActiveResearch).toHaveBeenCalledWith('p1', 'R-002')
    expect(res.enabled).toBe(true)
    expect(res.pinned_research).toEqual([])
    expect(res.pinned_hypotheses).toEqual({})
  })

  it('rejects a malformed status payload from SetActiveResearch', async () => {
    mockApp.SetActiveResearch = vi.fn(() => Promise.resolve({ nope: true }))

    await expect(setActiveResearch('p1', 'R-002')).rejects.toThrow(/Invalid research status/)
  })

  it('deleteResearch validates the refreshed status the same way', async () => {
    mockApp.DeleteResearch = vi.fn(() =>
      Promise.resolve({
        enabled: true,
        project_id: 'p1',
        research_root: '/ws/.research',
      }),
    )

    const res = await deleteResearch('p1', 'R-001')

    expect(mockApp.DeleteResearch).toHaveBeenCalledWith('p1', 'R-001')
    expect(res.pinned_research).toEqual([])
    expect(res.pinned_hypotheses).toEqual({})

    mockApp.DeleteResearch = vi.fn(() => Promise.resolve({ nope: true }))
    await expect(deleteResearch('p1', 'R-001')).rejects.toThrow(/Invalid research status/)
  })
})

describe('setResearchPinned / setHypothesisPinned passthrough', () => {
  beforeEach(() => {
    delete mockApp.SetResearchPinned
    delete mockApp.SetHypothesisPinned
  })

  it('passes the arguments through and resolves on success', async () => {
    mockApp.SetResearchPinned = vi.fn(() => Promise.resolve())
    mockApp.SetHypothesisPinned = vi.fn(() => Promise.resolve())

    await setResearchPinned('p1', 'R-001', true)
    expect(mockApp.SetResearchPinned).toHaveBeenCalledWith('p1', 'R-001', true)

    await setHypothesisPinned('p1', 'R-001', 'H-001', false)
    expect(mockApp.SetHypothesisPinned).toHaveBeenCalledWith('p1', 'R-001', 'H-001', false)
  })

  it('propagates binding failures', async () => {
    mockApp.SetResearchPinned = vi.fn(() => Promise.reject(new Error('rpc down')))
    mockApp.SetHypothesisPinned = vi.fn(() => Promise.reject(new Error('rpc down')))

    await expect(setResearchPinned('p1', 'R-001', true)).rejects.toThrow('rpc down')
    await expect(setHypothesisPinned('p1', 'R-001', 'H-001', true)).rejects.toThrow('rpc down')
  })
})
