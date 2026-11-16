// The SDD framework context store: the projection the workspace tab strip and
// the panel's framework selector read (the parse itself lives in specsStore).

import { describe, it, expect, beforeEach } from 'vitest'

import { useSddStore } from './sddStore'
import type { SDDFramework, SpecsStatus } from '@/types/models'

function frameworkOf(id: string, label = id): SDDFramework {
  return {
    id,
    label,
    origin: 'builtin',
    scope: 'project',
    tree_present: true,
    root_present: true,
    marker_present: true,
    skills: [],
  }
}

function statusWith(activeId: string, ids: string[]): SpecsStatus {
  const detected = ids.map((id) => frameworkOf(id))
  return {
    present: detected.length > 0,
    tree_present: detected.length > 0,
    root: detected.length > 0 ? '/ws/p1/specs' : '',
    workspace: '/ws/p1',
    detected,
    active_id: activeId,
    active: detected.find((fw) => fw.id === activeId),
    selector_needed: detected.length > 1 && activeId === '',
    roots: [],
    documents: [],
    nodes: [],
    views: [],
    findings: [],
    actions: [],
    counts: { documents: 0, findings: 0, by_concept: {}, findings_by_kind: {} },
    warnings: [],
  }
}

beforeEach(() => {
  useSddStore.getState().reset()
})

describe('sddStore — applyStatus', () => {
  it('adopts a first payload wholesale', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))

    const s = useSddStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.known).toBe(true)
    expect(s.present).toBe(true)
    expect(s.frameworks.map((fw) => fw.id)).toEqual(['sdd', 'openspec'])
    expect(s.activeId).toBe('sdd')
    expect(s.confirmedActiveId).toBe('sdd')
    expect(s.selectorNeeded).toBe(false)
  })

  it('replaces the context on a project switch (no inherited selection)', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))
    useSddStore.getState().beginSwitch('openspec')

    useSddStore.getState().applyStatus('p2', statusWith('spec-kit', ['spec-kit']))

    const s = useSddStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.frameworks.map((fw) => fw.id)).toEqual(['spec-kit'])
    expect(s.activeId).toBe('spec-kit')
    expect(s.pendingActiveId).toBeNull()
    expect(s.switching).toBe(false)
  })

  it('keeps the pending choice until the server confirms it', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))
    useSddStore.getState().beginSwitch('openspec')

    // A payload from a parse started BEFORE the switch: the intent stands.
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))
    expect(useSddStore.getState().activeId).toBe('openspec')
    expect(useSddStore.getState().pendingActiveId).toBe('openspec')

    // The server now reports the choice: it settles.
    useSddStore.getState().applyStatus('p1', statusWith('openspec', ['sdd', 'openspec']))
    const s = useSddStore.getState()
    expect(s.activeId).toBe('openspec')
    expect(s.confirmedActiveId).toBe('openspec')
    expect(s.pendingActiveId).toBeNull()
    expect(s.switching).toBe(false)
  })

  it('drops the surface when the payload reports no detected framework', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd']))
    useSddStore.getState().applyStatus('p1', statusWith('', []))

    expect(useSddStore.getState().present).toBe(false)
    expect(useSddStore.getState().frameworks).toEqual([])
  })
})

describe('sddStore — switch lifecycle', () => {
  it('reverts to the confirmed selection when the switch fails', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))
    const seq = useSddStore.getState().beginSwitch('openspec')

    useSddStore.getState().failSwitch(seq, 'not detected')

    const s = useSddStore.getState()
    expect(s.activeId).toBe('sdd')
    expect(s.pendingActiveId).toBeNull()
    expect(s.switching).toBe(false)
    expect(s.error).toBe('not detected')
  })

  it('ignores a superseded attempt (the newer one owns the state)', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec', 'spec-kit']))
    const first = useSddStore.getState().beginSwitch('openspec')
    useSddStore.getState().beginSwitch('spec-kit')

    useSddStore.getState().failSwitch(first, 'stale failure')

    const s = useSddStore.getState()
    expect(s.activeId).toBe('spec-kit')
    expect(s.switching).toBe(true)
    expect(s.error).toBeNull()
  })

  // Review 15: the switch is the persist + re-parse pair, and a re-parse
  // that ENDED IN ERROR has still ended — endSwitch (not just failSwitch)
  // must unlock the selector, or a transport failure after a successful
  // persist would disable it forever.
  it('endSwitch settles the switch whether the re-parse succeeded or failed', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec']))
    const seq = useSddStore.getState().beginSwitch('openspec')

    // The re-parse errored, so no applyStatus settled the pending choice.
    useSddStore.getState().endSwitch(seq)

    const s = useSddStore.getState()
    expect(s.switching).toBe(false)
    expect(s.pendingActiveId).toBeNull()
    // The persist landed, so the saved choice stays rendered (the panel
    // banner carries the refresh failure separately).
    expect(s.activeId).toBe('openspec')
  })

  it('endSwitch is a no-op for a superseded attempt', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd', 'openspec', 'spec-kit']))
    const first = useSddStore.getState().beginSwitch('openspec')
    useSddStore.getState().beginSwitch('spec-kit')

    useSddStore.getState().endSwitch(first)

    const s = useSddStore.getState()
    expect(s.activeId).toBe('spec-kit')
    expect(s.pendingActiveId).toBe('spec-kit')
    expect(s.switching).toBe(true)
  })
})

describe('sddStore — reset', () => {
  it('clears the context', () => {
    useSddStore.getState().applyStatus('p1', statusWith('sdd', ['sdd']))

    useSddStore.getState().reset()

    const s = useSddStore.getState()
    expect(s.projectId).toBeNull()
    expect(s.known).toBe(false)
    expect(s.frameworks).toEqual([])
    expect(s.activeId).toBe('')
    expect(s.warnings).toEqual([])
  })
})
