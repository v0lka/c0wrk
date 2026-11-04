// specsActions — the auditable dispatch constants (ADR-105 D4). A quick
// action is fully determined by its constant: the label the button renders,
// the spec-* skill it activates (ADR-104), and the prompt dispatched with
// it. These tests pin that wiring so a silent edit (wrong skill, empty
// prompt) cannot slip through.
import { describe, expect, it } from 'vitest'
import { INITIALIZE_ACTION, NO_SPECS_REASON, QUICK_ACTIONS } from './specsActions'

describe('specsActions — QUICK_ACTIONS', () => {
  it('renders exactly the four lifecycle gestures in the fixed order', () => {
    expect(QUICK_ACTIONS.map((a) => a.key)).toEqual([
      'initialize',
      'check-drift',
      'consult',
      'explore',
    ])
  })

  it('maps every gesture to its ADR-104 skill', () => {
    const byKey = Object.fromEntries(QUICK_ACTIONS.map((a) => [a.key, a]))
    expect(byKey['initialize']!.skill).toBe('spec-init')
    expect(byKey['check-drift']!.skill).toBe('spec-check')
    expect(byKey['consult']!.skill).toBe('spec-consult')
    expect(byKey['explore']!.skill).toBe('spec-explore')
  })

  it('every prompt is a non-empty constant and names its intent', () => {
    for (const action of QUICK_ACTIONS) {
      expect(action.prompt.length).toBeGreaterThan(10)
    }
    expect(
      QUICK_ACTIONS.find((a) => a.key === 'initialize')!.prompt,
    ).toMatch(/initializ/i)
    expect(
      QUICK_ACTIONS.find((a) => a.key === 'check-drift')!.prompt,
    ).toMatch(/drift/i)
  })

  it('only Initialize is available without a specs/ tree', () => {
    for (const action of QUICK_ACTIONS) {
      if (action.key === 'initialize') {
        expect(action.requiresSpecs).toBeFalsy()
      } else {
        expect(action.requiresSpecs).toBe(true)
      }
    }
  })

  it('the empty-state CTA is the same constant the quick-actions row renders', () => {
    expect(QUICK_ACTIONS[0]).toBe(INITIALIZE_ACTION)
    expect(INITIALIZE_ACTION.skill).toBe('spec-init')
    expect(NO_SPECS_REASON.length).toBeGreaterThan(0)
  })
})
