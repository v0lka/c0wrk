// Unit tests for researchWorkspaceUtils — the hypothesis card's draft
// snapshotting and change-set derivation, including the parents set semantics.
import { describe, it, expect } from 'vitest'
import { draftFromNode, buildUpdateFields, parseParentIds } from './researchWorkspaceUtils'
import type { HypothesisNode } from '@/types/models'

const FULL_NODE: HypothesisNode = {
  id: 'H-002',
  title: 'Endpoint extraction',
  status: 'in-progress',
  parents: ['H-001', 'H-003'],
  timebox: '5 days',
  result: 'Recovered 97%.',
  statement: 'Endpoints can be extracted.',
  verification_criterion: 'Recover >= 95%.',
  experiment_notes: 'Run 1 passed.',
  decision: 'continue',
}

describe('parseParentIds', () => {
  it('canonicalizes tolerant input: case, hyphen, padding, garbage, order', () => {
    expect(parseParentIds('H-001, h2, h-003, H-001')).toEqual(['H-001', 'H-002', 'H-003'])
    expect(parseParentIds('parents: h-010 and h-009')).toEqual(['H-009', 'H-010'])
    expect(parseParentIds('—')).toEqual([])
    expect(parseParentIds('')).toEqual([])
  })
})

describe('draftFromNode', () => {
  it('snapshots every editable field, parents as input text', () => {
    expect(draftFromNode(FULL_NODE)).toEqual({
      title: 'Endpoint extraction',
      parents: 'H-001, H-003',
      status: 'in-progress',
      decision: 'continue',
      statement: 'Endpoints can be extracted.',
      verification_criterion: 'Recover >= 95%.',
      experiment_notes: 'Run 1 passed.',
      timebox: '5 days',
      result: 'Recovered 97%.',
    })
  })

  it('falls back to defaults for missing fields', () => {
    const draft = draftFromNode({ id: 'H-001', title: 'T', status: '' })
    expect(draft.status).toBe('open')
    expect(draft.parents).toBe('')
    expect(draft.decision).toBe('')
    expect(draft.result).toBe('')
  })
})

describe('buildUpdateFields', () => {
  it('returns an empty change-set for an untouched draft', () => {
    const draft = draftFromNode(FULL_NODE)
    expect(buildUpdateFields(FULL_NODE, draft)).toEqual({})
  })

  it('includes only changed fields', () => {
    const draft = { ...draftFromNode(FULL_NODE), title: 'New title', result: 'New finding' }
    expect(buildUpdateFields(FULL_NODE, draft)).toEqual({
      title: 'New title',
      result: 'New finding',
    })
  })

  it('treats parents as a canonical set: reformatting is not a change', () => {
    const draft = { ...draftFromNode(FULL_NODE), parents: 'H-003, h-1' }
    expect(buildUpdateFields(FULL_NODE, draft)).toEqual({})
  })

  it('sends parents when the set changes (add, remove, clear)', () => {
    const base = { ...FULL_NODE, parents: ['H-001'] }
    expect(buildUpdateFields(base, { ...draftFromNode(base), parents: 'H-001, H-003' })).toEqual({
      parents: ['H-001', 'H-003'],
    })
    expect(buildUpdateFields(base, { ...draftFromNode(base), parents: '' })).toEqual({
      parents: [],
    })
  })

  it('sends the parsed set faithfully, including a self-reference the backend will reject', () => {
    const self: HypothesisNode = { id: 'H-001', title: 'T', status: 'open', parents: [] }
    const draft = { ...draftFromNode(self), parents: 'H-001' }
    expect(buildUpdateFields(self, draft)).toEqual({ parents: ['H-001'] })
  })

  it('compares long-form sections verbatim', () => {
    const draft = {
      ...draftFromNode(FULL_NODE),
      statement: 'New statement.',
      experiment_notes: '',
    }
    expect(buildUpdateFields(FULL_NODE, draft)).toEqual({
      statement: 'New statement.',
      experiment_notes: '',
    })
  })
})
