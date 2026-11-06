// specsActions — the auditable dispatch constants (ADR-106 D4). A quick
// action is fully determined by its constant: the label the button renders,
// the spec-* skill it activates (ADR-105), and the prompt dispatched with
// it. These tests pin that wiring so a silent edit (wrong skill, empty
// prompt) cannot slip through.
import { describe, expect, it } from 'vitest'
import {
  INITIALIZE_ACTION,
  NO_SPECS_REASON,
  QUICK_ACTIONS,
  buildFixFindingAction,
  buildFixFindingsAction,
  findingLine,
} from './specsActions'
import type { SpecFinding } from '@/types/models'

describe('specsActions — QUICK_ACTIONS', () => {
  it('renders exactly the four lifecycle gestures in the fixed order', () => {
    expect(QUICK_ACTIONS.map((a) => a.key)).toEqual([
      'initialize',
      'check-drift',
      'consult',
      'explore',
    ])
  })

  it('maps every gesture to its ADR-105 skill', () => {
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

  it('the sidebar-strip labels are the SHORT forms; the tooltips spell the gestures', () => {
    // ADR-109: the narrow sidebar carries short button names; the full
    // gesture name moves to the tooltip (fullLabel).
    const byKey = Object.fromEntries(QUICK_ACTIONS.map((a) => [a.key, a]))
    expect(byKey['initialize']!.label).toBe('Init')
    expect(byKey['check-drift']!.label).toBe('Check')
    expect(byKey['initialize']!.fullLabel).toBe('Initialize specs')
    expect(byKey['check-drift']!.fullLabel).toBe('Check drift')
    for (const action of QUICK_ACTIONS) {
      if (action.fullLabel === undefined) continue
      expect(action.fullLabel.length).toBeGreaterThan(action.label.length)
    }
  })
})

describe('specsActions — buildFixFindingsAction', () => {
  const findings: SpecFinding[] = [
    { kind: 'PATH_MISSING', file: 'domains/app.md', detail: 'key file does not exist: specs/gone.md' },
    { kind: 'SUPERSEDED_CYCLE', file: 'decisions/001-a.md', detail: 'supersession cycle: a -> b -> a' },
  ]

  it('dispatches spec-update with a prompt enumerating every finding verbatim', () => {
    const action = buildFixFindingsAction(findings)
    expect(action.key).toBe('fix-findings')
    expect(action.skill).toBe('spec-update')
    expect(action.label).toBe('Fix findings')
    expect(action.prompt).toContain('Findings:')
    // Every finding appears as its deterministic "[kind] file: detail" line
    // via findingLine — the single source the card's copy button renders
    // too, so the dispatched task sees exactly what the panel shows.
    for (const f of findings) {
      expect(action.prompt).toContain(findingLine(f))
    }
  })

  it('the tooltip names the count the task will resolve', () => {
    expect(buildFixFindingsAction(findings).fullLabel).toBe('Fix all 2 integrity findings')
    const single = buildFixFindingsAction([findings[0]!])
    expect(single.fullLabel).toBe('Fix all 1 integrity finding')
  })

  it('an empty findings list still produces a well-formed action', () => {
    const action = buildFixFindingsAction([])
    expect(action.prompt).toContain('Findings:')
    expect(action.prompt.endsWith('Findings:\n')).toBe(true)
  })
})

describe('specsActions — buildFixFindingAction', () => {
  const finding: SpecFinding = {
    kind: 'CROSS_REF_BROKEN',
    file: 'decisions/041-slm-profiles.md',
    detail: 'references missing decisions/099-nothing.md',
  }
  const other: SpecFinding = {
    kind: 'PATH_MISSING',
    file: 'domains/app.md',
    detail: 'key file does not exist: specs/gone.md',
  }

  it('pins the dispatch wiring: fix-finding key, spec-update skill, short label, full tooltip', () => {
    const action = buildFixFindingAction(finding)
    expect(action.key).toBe('fix-finding')
    expect(action.skill).toBe('spec-update')
    expect(action.label).toBe('Fix finding')
    expect(action.fullLabel).toBe('Fix this integrity finding')
  })

  it('the prompt carries the shared repair instructions and the finding verbatim', () => {
    const action = buildFixFindingAction(finding)
    expect(action.prompt).toContain('Findings:')
    // The verbatim line is the same findingLine the card's copy button
    // hands out — one format, no drift between panel and dispatched task.
    expect(action.prompt).toContain(findingLine(finding))
    expect(action.prompt.endsWith('\n' + findingLine(finding))).toBe(true)
  })

  it('the prompt enumerates ONLY the one finding — no foreign lines', () => {
    const action = buildFixFindingAction(finding)
    expect(action.prompt).not.toContain(findingLine(other))
    // Exactly one bullet line in the Findings section, and it is the
    // target finding's.
    const bullets = action.prompt
      .split('\n')
      .filter((line) => line.startsWith('- ['))
    expect(bullets).toEqual([findingLine(finding)])
  })

  it('findingLine is the single source of the verbatim card/prompt format', () => {
    expect(findingLine(finding)).toBe(
      `- [${finding.kind}] ${finding.file}: ${finding.detail}`,
    )
    // The aggregate builder renders the same line per finding.
    const aggregate = buildFixFindingsAction([finding, other])
    for (const f of [finding, other]) {
      expect(aggregate.prompt).toContain(findingLine(f))
    }
  })
})
