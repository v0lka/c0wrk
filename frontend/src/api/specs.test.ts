// Unit tests for api/specs.ts — boundary guards and per-entry fail-closed
// normalization on the GetSpecsStatus path (the research contract).

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { getSpecsStatus } from '@/api/specs'

const validFile = { path: 'INDEX.md', title: 'Specs Index' }
const validADR = {
  number: '105',
  slug: 'specs-sidebar-panel',
  file: 'decisions/105-specs-sidebar-panel.md',
  status: 'accepted',
}
const validFinding = {
  kind: 'INDEX_STALE',
  file: 'domains/testing.md',
  detail: 'not listed in INDEX.md',
}

function fullPayload(): Record<string, unknown> {
  return {
    present: true,
    root: '/ws/p1/specs',
    files: [validFile],
    index_tasks: [{ task: 'Add a tool', specs: ['contracts/tool-system.md'] }],
    adrs: [validADR],
    findings: [validFinding],
    counts: {
      files: 1,
      adrs: 1,
      findings: 1,
      findings_by_kind: { INDEX_STALE: 1 },
    },
  }
}

describe('getSpecsStatus boundary validation', () => {
  beforeEach(() => {
    delete mockApp.GetSpecsStatus
  })

  it('passes a well-formed payload through', async () => {
    mockApp.GetSpecsStatus = vi.fn(() => Promise.resolve(fullPayload()))

    const res = await getSpecsStatus('p1')

    expect(mockApp.GetSpecsStatus).toHaveBeenCalledWith('p1')
    expect(res.present).toBe(true)
    expect(res.files).toEqual([validFile])
    expect(res.index_tasks).toEqual([{ task: 'Add a tool', specs: ['contracts/tool-system.md'] }])
    expect(res.adrs).toEqual([validADR])
    expect(res.findings).toEqual([validFinding])
    expect(res.counts).toEqual({
      files: 1,
      adrs: 1,
      findings: 1,
      findings_by_kind: { INDEX_STALE: 1 },
    })
  })

  it('normalizes backend null/missing collections to empty arrays and recomputes counts', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({ present: false, root: '/ws/p1/specs', counts: null }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.files).toEqual([])
    expect(res.index_tasks).toEqual([])
    expect(res.adrs).toEqual([])
    expect(res.findings).toEqual([])
    expect(res.counts).toEqual({
      files: 0,
      adrs: 0,
      findings: 0,
      findings_by_kind: {},
    })
  })

  it('drops malformed entries per-entry instead of rejecting the payload', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        ...fullPayload(),
        files: [
          validFile,
          { title: 'no path' }, // missing path → dropped
          { path: 7, title: 'bad path' }, // non-string path → dropped
          { path: 'domains/x.md', key_files: 'core/x.go' }, // non-array key_files → dropped
          { path: 'domains/y.md', cross_refs: [42] }, // non-string entry → dropped
        ],
        index_tasks: [
          { task: 'Add a tool', specs: ['contracts/tool-system.md'] },
          { task: 7 }, // non-string task → dropped
          { specs: ['contracts/x.md'] }, // missing task → dropped
        ],
        adrs: [
          validADR,
          { number: '106' }, // missing slug/file/status → dropped
          { number: '106', slug: 's', file: 'decisions/106-s.md', status: 'draft', title: 42 }, // non-string title → dropped
          { number: '106', slug: 's', file: 'decisions/106-s.md', status: 'draft', date: 42 }, // non-string date → dropped
        ],
        findings: [
          validFinding,
          { kind: 'PATH_MISSING', file: 'domains/z.md' }, // missing detail → dropped
          { kind: '', file: 'domains/z.md', detail: 'empty kind' }, // empty kind → dropped
          { file: 'domains/z.md', detail: 'no kind' }, // missing kind → dropped
        ],
        // Deliberately inconsistent with the collections — must be recomputed.
        counts: {
          files: 99,
          adrs: 99,
          findings: 99,
          findings_by_kind: { BOGUS: 99 },
        },
      }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.files).toEqual([validFile])
    expect(res.index_tasks).toEqual([{ task: 'Add a tool', specs: ['contracts/tool-system.md'] }])
    expect(res.adrs).toEqual([validADR])
    expect(res.findings).toEqual([validFinding])
    expect(res.counts).toEqual({
      files: 1,
      adrs: 1,
      findings: 1,
      findings_by_kind: { INDEX_STALE: 1 },
    })
  })

  it('keeps a future-tier finding kind instead of dropping it', async () => {
    const futureFinding = { kind: 'FUTURE_KIND', file: 'META.md', detail: 'from a newer backend' }
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({ ...fullPayload(), findings: [validFinding, futureFinding] }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.findings.map((f) => f.kind)).toEqual(['INDEX_STALE', 'FUTURE_KIND'])
    expect(res.counts.findings).toBe(2)
    expect(res.counts.findings_by_kind).toEqual({ INDEX_STALE: 1, FUTURE_KIND: 1 })
  })

  it('rejects payloads whose outer shape is invalid (fail-closed)', async () => {
    const cases: unknown[] = [
      null,
      { nope: true }, // missing present/root
      { present: 'yes', root: '/ws' }, // non-boolean present
      { present: true, root: 7 }, // non-string root
      { present: true, root: '/ws', files: 'nope' }, // non-array collection
      { present: true, root: '/ws', index_tasks: 3 },
      { present: true, root: '/ws', adrs: {} },
      { present: true, root: '/ws', findings: 'x' },
      { present: true, root: '/ws', counts: 5 }, // non-record counts
    ]
    for (const payload of cases) {
      mockApp.GetSpecsStatus = vi.fn(() => Promise.resolve(payload))
      await expect(getSpecsStatus('p1')).rejects.toThrow(/Invalid specs status/)
    }
  })

  it('propagates binding failures', async () => {
    mockApp.GetSpecsStatus = vi.fn(() => Promise.reject(new Error('rpc down')))

    await expect(getSpecsStatus('p1')).rejects.toThrow('rpc down')
  })
})
