// Unit tests for api/specs.ts — boundary guards and per-entry fail-closed
// normalization on the GetSpecsStatus path, plus the SetActiveSDD wrapper
// (the framework selection the panel persists).

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { getSpecsStatus, setActiveSDD } from '@/api/specs'

const validFramework = {
  id: 'sdd',
  label: 'Specification System',
  origin: 'builtin',
  scope: 'project',
  tree_present: true,
  root_present: true,
  marker_present: true,
  skills: [{ name: 'vibespec-check', dir: '/home/.agents/skills/vibespec-check', scope: 'global' }],
}
const validRoot = {
  id: 'specs',
  path: 'specs',
  label: 'Specification tree',
  required: true,
  present: true,
}
const validDocument = {
  path: 'specs/INDEX.md',
  root: 'specs',
  concept: 'index',
  name: 'INDEX.md',
  slug: 'INDEX',
  title: 'Specs Index',
  headings: [],
  key_files: [],
  cross_refs: [],
  requirements: [],
  scenarios: [],
  deltas: [],
  principles: [],
  user_stories: [],
  task_items: [],
  tasks: [],
  missing: [],
}
const validNode = {
  id: 'specs/INDEX.md',
  kind: 'document',
  path: 'specs/INDEX.md',
  label: 'Specs Index',
  concept: 'index',
  name: 'INDEX.md',
  children: [],
}
const validView = {
  id: 'tree',
  label: 'Specification tree',
  kind: 'tree',
  nodes: ['specs/INDEX.md'],
}
const validFinding = {
  kind: 'INDEX_STALE',
  file: 'specs/domains/testing.md',
  detail: 'not listed in INDEX.md',
  message: 'every spec file must be listed in the INDEX',
  severity: 'error',
}
const validAction = {
  kind: 'check',
  label: 'Check',
  skill: 'vibespec-check',
  prompt: 'Check the specifications for drift.',
  requires: ['root'],
  available: true,
}

function fullPayload(): Record<string, unknown> {
  return {
    present: true,
    tree_present: true,
    root: '/ws/p1/specs',
    workspace: '/ws/p1',
    detected: [validFramework],
    active_id: 'sdd',
    active: validFramework,
    selector_needed: false,
    roots: [validRoot],
    documents: [validDocument],
    nodes: [validNode],
    views: [validView],
    findings: [validFinding],
    actions: [validAction],
    counts: {
      documents: 99,
      findings: 99,
      by_concept: { BOGUS: 99 },
      findings_by_kind: { BOGUS: 99 },
    },
    warnings: [],
  }
}

beforeEach(() => {
  for (const key of Object.keys(mockApp)) delete mockApp[key]
})

describe('getSpecsStatus boundary validation', () => {
  it('passes a well-formed framework-aware payload through', async () => {
    mockApp.GetSpecsStatus = vi.fn(() => Promise.resolve(fullPayload()))

    const res = await getSpecsStatus('p1')

    expect(mockApp.GetSpecsStatus).toHaveBeenCalledWith('p1')
    expect(res.present).toBe(true)
    expect(res.tree_present).toBe(true)
    expect(res.workspace).toBe('/ws/p1')
    expect(res.detected).toEqual([validFramework])
    expect(res.active_id).toBe('sdd')
    expect(res.active?.id).toBe('sdd')
    expect(res.roots).toEqual([validRoot])
    expect(res.documents).toEqual([validDocument])
    expect(res.nodes).toEqual([validNode])
    expect(res.views).toEqual([validView])
    expect(res.findings).toEqual([validFinding])
    expect(res.actions).toEqual([validAction])
  })

  it('recomputes the counts from the normalized model', async () => {
    const directory = {
      id: 'openspec/changes/add-auth',
      kind: 'directory',
      path: 'openspec/changes/add-auth',
      label: 'Add auth',
      concept: 'change',
      name: 'add-auth',
      children: [],
    }
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        ...fullPayload(),
        nodes: [validNode, directory],
        counts: {
          documents: 99,
          findings: 99,
          by_concept: { BOGUS: 99 },
          findings_by_kind: { BOGUS: 99 },
        },
      }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.counts).toEqual({
      documents: 1,
      findings: 1,
      // Directory instances count too — the backend's own by_concept figure.
      by_concept: { index: 1, change: 1 },
      findings_by_kind: { INDEX_STALE: 1 },
    })
  })

  it('drops malformed entries per-entry instead of rejecting the payload', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        ...fullPayload(),
        detected: [validFramework, { id: '', label: 'no id' }, 'nope'],
        roots: [validRoot, { id: 'x' }],
        documents: [validDocument, { path: 7 }],
        nodes: [validNode, { id: 'n', kind: 'document' }],
        views: [
          validView,
          { id: 'v', label: 'v', kind: 'tree', nodes: 'no' },
          { id: 'v2', label: 'v2', kind: 'tree', nodes: [], paths: 'not-an-array' },
          { id: 'v3', label: 'v3', kind: 'tree', nodes: [], paths: ['ok', 7] },
          { id: 'v4', label: 'v4', kind: 'tree', nodes: [], paths: ['specs/INDEX.md'] },
          // Review 110: `paths: null` must collapse to "no membership filter",
          // so the key must not survive as a set-less null.
          { id: 'v5', label: 'v5', kind: 'tree', nodes: [], paths: null },
        ],
        findings: [validFinding, { kind: '', file: 'x', detail: 'y' }],
        actions: [
          validAction,
          { kind: 'a', label: 'a', requires: [] },
          // The skill fact is a wire boolean validated like `available`: a
          // non-boolean must drop the entry instead of slipping past the
          // `=== false` reason test, while a real `false` is kept (it is the
          // missing-skill reason).
          { ...validAction, kind: 'bad-skill', skill_available: 'yes' },
          { ...validAction, kind: 'missing-skill', skill_available: false },
        ],
        warnings: ['kept', 42],
      }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.detected).toEqual([validFramework])
    expect(res.roots).toEqual([validRoot])
    expect(res.documents).toEqual([validDocument])
    expect(res.nodes).toEqual([validNode])
    expect(res.findings).toEqual([validFinding])
    expect(res.actions).toEqual([
      validAction,
      { ...validAction, kind: 'missing-skill', skill_available: false },
    ])
    expect(res.warnings).toEqual(['kept'])
    expect(res.views).toEqual([
      validView,
      { id: 'v4', label: 'v4', kind: 'tree', nodes: [], paths: ['specs/INDEX.md'] },
      { id: 'v5', label: 'v5', kind: 'tree', nodes: [] },
    ])
    // `toEqual` treats an undefined property as absent, so pin the null case by
    // the key itself: "no filter" must be a missing key, not a null one.
    expect('paths' in (res.views[2] as object)).toBe(false)
  })

  // Review 100: the guards accept a null/absent collection for ENTRY fields
  // (Go serializes a nil slice as `null`, and a version-skewed payload may
  // omit it), so the normalizer must repair them. Otherwise the render path
  // iterates `node.children`, reads `view.nodes` and evaluates
  // `action.requires` on undefined and the ErrorBoundary replaces the whole UI.
  it('normalizes absent entry collections instead of leaving them undefined', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        ...fullPayload(),
        nodes: [{ id: 'n1', kind: 'folder', path: 'n1', label: 'n1', name: 'n1' }],
        views: [{ id: 'v1', label: 'v1', kind: 'tree' }],
        actions: [{ kind: 'a1', label: 'a1', available: true }],
      }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.nodes).toEqual([
      { id: 'n1', kind: 'folder', path: 'n1', label: 'n1', name: 'n1', children: [] },
    ])
    expect(res.views).toEqual([{ id: 'v1', label: 'v1', kind: 'tree', nodes: [] }])
    expect(res.actions).toEqual([{ kind: 'a1', label: 'a1', available: true, requires: [] }])
  })

  it('keeps an unknown finding token — the vocabulary belongs to the framework', async () => {
    const foreign = {
      kind: 'DELTA_SECTION_MISSING',
      file: 'openspec/changes/add/specs/x/spec.md',
      detail: 'no ADDED Requirements section',
      severity: 'error',
    }
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({ ...fullPayload(), findings: [validFinding, foreign] }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.findings.map((f) => f.kind)).toEqual(['INDEX_STALE', 'DELTA_SECTION_MISSING'])
    expect(res.counts.findings).toBe(2)
    expect(res.counts.findings_by_kind).toEqual({ INDEX_STALE: 1, DELTA_SECTION_MISSING: 1 })
  })

  it('drops an active record that names a different framework than active_id', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        ...fullPayload(),
        active_id: 'sdd',
        active: { ...validFramework, id: 'other' },
      }),
    )

    const res = await getSpecsStatus('p1')

    // The detection list carries the matching record, so the stale one is
    // replaced rather than shown beside a different active id.
    expect(res.active?.id).toBe('sdd')
  })

  it('normalizes null collections to empty arrays and maps', async () => {
    mockApp.GetSpecsStatus = vi.fn(() =>
      Promise.resolve({
        present: false,
        tree_present: false,
        root: '',
        workspace: '',
        detected: null,
        roots: null,
        documents: null,
        nodes: null,
        views: null,
        findings: null,
        actions: null,
        warnings: null,
        selector_needed: false,
        counts: null,
      }),
    )

    const res = await getSpecsStatus('p1')

    expect(res.detected).toEqual([])
    expect(res.roots).toEqual([])
    expect(res.documents).toEqual([])
    expect(res.nodes).toEqual([])
    expect(res.views).toEqual([])
    expect(res.findings).toEqual([])
    expect(res.actions).toEqual([])
    expect(res.warnings).toEqual([])
    expect(res.active).toBeUndefined()
    expect(res.active_id).toBe('')
    expect(res.counts).toEqual({
      documents: 0,
      findings: 0,
      by_concept: {},
      findings_by_kind: {},
    })
  })

  it('rejects payloads whose outer shape is invalid (fail-closed)', async () => {
    const cases: unknown[] = [
      null,
      { nope: true }, // missing present/tree_present/root
      { present: 'yes', tree_present: false, root: '/ws', workspace: '/ws' },
      { present: true, tree_present: 'no', root: '/ws', workspace: '/ws' },
      { present: true, tree_present: false, root: 7, workspace: '/ws' },
      { present: true, tree_present: false, root: '/ws', workspace: '/ws', nodes: 'nope' },
      { present: true, tree_present: false, root: '/ws', workspace: '/ws', views: 3 },
      { present: true, tree_present: false, root: '/ws', workspace: '/ws', findings: {} },
      { present: true, tree_present: false, root: '/ws', workspace: '/ws', counts: 5 },
      {
        present: true,
        tree_present: false,
        selector_needed: false,
        root: '/ws',
        workspace: '/ws',
        warnings: 'not-an-array',
      },
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

describe('setActiveSDD', () => {
  it('persists the selection through the binding', async () => {
    mockApp.SetActiveSDD = vi.fn(() => Promise.resolve())

    await setActiveSDD('p1', 'openspec')

    expect(mockApp.SetActiveSDD).toHaveBeenCalledWith('p1', 'openspec')
  })

  it('propagates a rejected selection (an undetected framework)', async () => {
    mockApp.SetActiveSDD = vi.fn(() =>
      Promise.reject(new Error('SDD framework "nope" is not detected for project "p1"')),
    )

    await expect(setActiveSDD('p1', 'nope')).rejects.toThrow(/not detected/)
  })
})
