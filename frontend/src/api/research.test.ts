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

import { getResearchGraph, getResearchStatus } from '@/api/research'

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
})
