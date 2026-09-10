// Unit tests for the shared hypothesis-decision module: the vocabulary must
// mirror the research-decision skill (continue / pivot / kill / fork), and
// decisionOptions must always include the undecided option plus keep a
// legacy free-text value visible (the backend stores the card's Decision
// field verbatim, so the select must render a matching option). Pure module
// — no DOM needed.
import { describe, it, expect } from 'vitest'
import {
  DECISIONS,
  DECISION_UNDECIDED,
  isDecision,
  decisionLabel,
  decisionOptions,
} from './hypothesisDecision'

describe('DECISIONS', () => {
  it('mirrors the research-decision skill vocabulary in its order', () => {
    expect(DECISIONS).toEqual(['continue', 'pivot', 'kill', 'fork'])
  })
})

describe('isDecision', () => {
  it('accepts exactly the canonical values', () => {
    for (const d of DECISIONS) {
      expect(isDecision(d)).toBe(true)
    }
    expect(isDecision('')).toBe(false)
    expect(isDecision('Continue')).toBe(false)
    expect(isDecision('investigate deeper')).toBe(false)
  })
})

describe('decisionOptions', () => {
  it('offers undecided first, then the canonical four', () => {
    expect(decisionOptions('')).toEqual(['', 'continue', 'pivot', 'kill', 'fork'])
    expect(decisionOptions('continue')).toEqual(['', 'continue', 'pivot', 'kill', 'fork'])
  })

  it('keeps a legacy free-text value visible as the first option', () => {
    // An older card may carry a non-canonical Decision; the controlled
    // select needs a matching option so the value stays re-savable until
    // the user replaces it with a canonical one.
    expect(decisionOptions('investigate deeper')).toEqual([
      'investigate deeper',
      '',
      'continue',
      'pivot',
      'kill',
      'fork',
    ])
  })
})

describe('decisionLabel', () => {
  it('renders undecided for the empty value and the value as-is otherwise', () => {
    expect(decisionLabel(DECISION_UNDECIDED)).toBe('undecided')
    expect(decisionLabel('continue')).toBe('continue')
    expect(decisionLabel('investigate deeper')).toBe('investigate deeper')
  })
})
