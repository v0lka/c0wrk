// Dispatch helpers — the panel turns a framework's DECLARED action into a
// sendMessage call. Nothing here hardcodes a skill, a label or a gesture: the
// tests pin the declaration-driven behavior and the {path}/{name} substitution.
import { describe, expect, it } from 'vitest'
import { makeAction } from '@/test/sddFixture'
import type { SDDAction, SDDFinding, SDDNode } from '@/types/models'
import {
  FINDINGS_FIX_KIND,
  NODE_GATE_REASON,
  REQUIREMENT_UNMET_REASON,
  actionScope,
  actionToDispatch,
  bootstrapAction,
  dispatchableAction,
  findingLine,
  findingsFixAction,
  findingsFixDispatch,
  inertActionReason,
  nodeActionEnabled,
  nodeActionReason,
  nodeActionsFor,
  skillMissingReason,
  substituteActionText,
  toolbarActions,
} from './specsActions'

function action(overrides: Partial<SDDAction> = {}): SDDAction {
  return {
    kind: 'check',
    label: 'Check',
    skill: 'spec-check',
    prompt: 'Check the specifications for drift.',
    requires: ['root'],
    available: true,
    ...overrides,
  }
}

function finding(kind: string, file = 'specs/x.md', detail = 'missing'): SDDFinding {
  return { kind, file, detail, severity: 'error' }
}

describe('actionScope', () => {
  it('is node scope when the action declares a concept', () => {
    expect(actionScope(action({ concept: 'change' }))).toBe('node')
  })

  it('is toolbar scope otherwise', () => {
    expect(actionScope(action())).toBe('toolbar')
    expect(actionScope(action({ concept: '' }))).toBe('toolbar')
  })
})

describe('substituteActionText', () => {
  it('substitutes the node path and base name', () => {
    expect(
      substituteActionText('Apply {name} at {path}', {
        path: 'openspec/changes/add-auth',
        name: 'add-auth',
      }),
    ).toBe('Apply add-auth at openspec/changes/add-auth')
  })

  it('leaves unknown placeholders untouched', () => {
    expect(substituteActionText('Do {other}', { path: 'p', name: 'n' })).toBe('Do {other}')
  })

  it('drops node placeholders when no node is in context', () => {
    expect(substituteActionText('Apply {path}')).toBe('Apply ')
  })

  // The substituted values must land LITERALLY: a sequential string replace
  // would read `$`-sequences in a file path as replacement templates ($$ →
  // $, $& → the whole match) and a second pass would rewrite a literal
  // `{name}` that is part of a path. (Review 22.)
  it('inserts a path containing $$ literally', () => {
    expect(substituteActionText('Update {path} ({name}).', { path: 'docs/cost$$.md', name: 'cost$$.md' })).toBe(
      'Update docs/cost$$.md (cost$$.md).',
    )
  })

  it('inserts a path containing $& literally', () => {
    expect(substituteActionText('Update {path}', { path: 'docs/price$&tax.md', name: 'price$&tax.md' })).toBe(
      'Update docs/price$&tax.md',
    )
  })

  it('does not re-substitute a literal {name} inside the path', () => {
    expect(substituteActionText('Update {path} ({name}).', { path: 'docs/{name}.md', name: '{name}.md' })).toBe(
      'Update docs/{name}.md ({name}.md).',
    )
  })
})

describe('actionToDispatch', () => {
  it('carries the declared skill and prompt', () => {
    expect(actionToDispatch(action())).toEqual({
      key: 'check',
      skill: 'spec-check',
      prompt: 'Check the specifications for drift.',
    })
  })

  it('falls back to the label when the framework declares no prompt', () => {
    expect(actionToDispatch(action({ prompt: undefined }))?.prompt).toBe('Check')
  })

  it('keys a node gesture by kind + node path and substitutes the node', () => {
    const dispatch = actionToDispatch(
      action({ kind: 'apply', skill: 'openspec-apply-change', concept: 'change', prompt: 'Apply {name} ({path})' }),
      { path: 'openspec/changes/add-auth', name: 'add-auth' },
    )
    expect(dispatch).toEqual({
      key: 'apply:openspec/changes/add-auth',
      skill: 'openspec-apply-change',
      prompt: 'Apply add-auth (openspec/changes/add-auth)',
    })
  })

  it('is null without a declared skill (the panel invents no gesture)', () => {
    expect(actionToDispatch(action({ skill: undefined }))).toBeNull()
    expect(actionToDispatch(action({ skill: '' }))).toBeNull()
  })
})

describe('nodeActionsFor', () => {
  const actions: SDDAction[] = [
    action({ kind: 'apply', label: 'Apply', concept: 'change', skill: 'openspec-apply-change' }),
    action({ kind: 'plan', label: 'Plan', concept: 'feature', skill: 'speckit-plan' }),
    action({ kind: 'apply-unavailable', label: 'Apply', concept: 'change', skill: 'x', available: false }),
    action({ kind: 'skillless', label: 'Nope', concept: 'change', skill: undefined }),
    action({ kind: FINDINGS_FIX_KIND, label: 'Fix', concept: 'change', skill: 'spec-update' }),
    action({ kind: 'check', label: 'Check' }),
  ]

  it('returns the skill-carrying actions of that concept, unavailable ones INCLUDED', () => {
    // An unmet requirement stays in the render model: the row shows the
    // action DISABLED with the reason instead of hiding the next workflow
    // step. Dispatch gating happens at render/dispatch time on `available`.
    expect(nodeActionsFor(actions, 'change').map((a) => a.kind)).toEqual([
      'apply',
      'apply-unavailable',
    ])
    expect(nodeActionsFor(actions, 'feature').map((a) => a.kind)).toEqual(['plan'])
  })

  it('returns nothing for an unknown or empty concept', () => {
    expect(nodeActionsFor(actions, 'nope')).toEqual([])
    expect(nodeActionsFor(actions, '')).toEqual([])
  })
})

describe('toolbarActions', () => {
  it('keeps panel-level actions and drops node + findings gestures', () => {
    const actions: SDDAction[] = [
      action({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] }),
      action({ kind: 'apply', label: 'Apply', concept: 'change', skill: 'openspec-apply-change' }),
      action({ kind: FINDINGS_FIX_KIND, label: 'Fix findings', skill: 'spec-update' }),
    ]
    expect(toolbarActions(actions).map((a) => a.kind)).toEqual(['init'])
  })
})

describe('findingsFixAction', () => {
  it('finds the reserved kind', () => {
    const declared = action({ kind: FINDINGS_FIX_KIND, label: 'Fix findings', skill: 'spec-update' })
    expect(findingsFixAction([action(), declared])).toBe(declared)
  })

  it('is null when the framework declared none (the badge stays inert)', () => {
    expect(findingsFixAction([action()])).toBeNull()
    expect(findingsFixAction([])).toBeNull()
  })
})

describe('bootstrapAction', () => {
  it('is the toolbar action that declares no requirement', () => {
    const init = action({ kind: 'init', skill: 'spec-init', requires: [] })
    expect(bootstrapAction([action(), init])).toBe(init)
  })

  it('is null when every declared gesture requires a tree', () => {
    expect(bootstrapAction([action({ requires: ['root'] })])).toBeNull()
    expect(bootstrapAction([])).toBeNull()
  })
})

describe('findingLine', () => {
  it('renders the deterministic "- [kind] file: detail" line', () => {
    expect(findingLine(finding('INDEX_STALE', 'specs/domains/x.md', 'not listed'))).toBe(
      '- [INDEX_STALE] specs/domains/x.md: not listed',
    )
  })
})

describe('findingsFixDispatch', () => {
  const declared = action({
    kind: FINDINGS_FIX_KIND,
    label: 'Fix findings',
    skill: 'spec-update',
    prompt: 'Repair every finding, then re-check the tree.',
    requires: ['root', 'findings'],
  })

  it('appends the findings verbatim to the framework’s own instruction', () => {
    const dispatch = findingsFixDispatch(declared, [
      finding('INDEX_STALE', 'specs/a.md', 'not listed'),
      finding('PATH_MISSING', 'specs/b.md', 'core/gone.go'),
    ])
    expect(dispatch).toEqual({
      key: FINDINGS_FIX_KIND,
      skill: 'spec-update',
      prompt:
        'Repair every finding, then re-check the tree.\n\nFindings:\n' +
        '- [INDEX_STALE] specs/a.md: not listed\n' +
        '- [PATH_MISSING] specs/b.md: core/gone.go',
    })
  })

  it('is null without a declared action or skill', () => {
    expect(findingsFixDispatch(null, [finding('X')])).toBeNull()
    expect(findingsFixDispatch(action({ kind: FINDINGS_FIX_KIND, skill: undefined }), [])).toBeNull()
  })

  // An unavailable gesture must not dispatch, whatever surface built it:
  // declared requirements the parsed tree does not satisfy keep the badge,
  // the per-finding chips AND this dispatch inert. (Review 18.)
  it('is null when the declared action is not available for the tree', () => {
    expect(
      findingsFixDispatch(action({ kind: FINDINGS_FIX_KIND, skill: 'spec-update', available: false }), [
        finding('X'),
      ]),
    ).toBeNull()
  })
})

describe('inertActionReason', () => {
  it('names the missing skill for a skillless declared action', () => {
    expect(inertActionReason(makeAction({ skill: '' }))).toBe(
      'The framework declares this action with no skill to run',
    )
  })

  it('falls back to the requirement reason when a skill is present', () => {
    expect(inertActionReason(makeAction({ available: false }))).toBe(REQUIREMENT_UNMET_REASON)
  })

  // A partially installed framework (detection matches all_of plus one any_of)
  // can block a gesture on a COMPLETE tree. The two causes need different
  // remedies — install the skill versus build the tree — so the reason names
  // the skill instead of repeating the requirement wording.
  it('names the uninstalled skill rather than the tree', () => {
    const reason = inertActionReason(
      makeAction({ skill: 'spec-init', available: false, skill_available: false }),
    )
    expect(reason).toBe(skillMissingReason('spec-init'))
    expect(reason).toContain('spec-init')
    expect(reason).not.toBe(REQUIREMENT_UNMET_REASON)
  })

  it('keeps the declaration wording when no skill is declared at all', () => {
    expect(
      inertActionReason(makeAction({ skill: '', skill_available: false })),
    ).toBe('The framework declares this action with no skill to run')
  })
})

// The ONE predicate every dispatch surface shares: a gesture renders live
// only when it is declared, carries a skill and is available for the parsed
// tree — no surface may show a live button another surface refuses. (Reviews
// 54/62: the header badge, the finding cards, the quick actions and the node
// actions must agree.)
describe('dispatchableAction', () => {
  it('accepts a declared, skilled, available action', () => {
    expect(dispatchableAction(action())).toBe(true)
  })

  it('rejects null, a skillless action and an unavailable action', () => {
    expect(dispatchableAction(null)).toBe(false)
    expect(dispatchableAction(action({ skill: undefined }))).toBe(false)
    expect(dispatchableAction(action({ skill: '' }))).toBe(false)
    expect(dispatchableAction(action({ available: false }))).toBe(false)
  })
})

// Review 111: the tree-level `available` gate answers "could this gesture apply
// anywhere?"; the backend's per-node list answers "does it apply HERE?", so a
// feature that already has its plan.md loses Plan while an unplanned one keeps
// it — in the same tree.
describe('nodeActionEnabled', () => {
  const planned = { path: 'specs/001-a/spec.md' } as SDDNode
  const unplanned = { path: 'specs/002-b/spec.md' } as SDDNode

  it('is true for an available action with no per-node refinement', () => {
    expect(nodeActionEnabled(action({ concept: 'feature' }), planned)).toBe(true)
  })

  it('is false only on the nodes the backend refined as blocked', () => {
    const plan = action({
      kind: 'plan',
      concept: 'feature',
      unavailable_at: ['specs/001-a/spec.md'],
    })
    expect(nodeActionEnabled(plan, planned)).toBe(false)
    expect(nodeActionEnabled(plan, unplanned)).toBe(true)
  })

  it('is false for a tree-level unavailable action whatever the node list says', () => {
    expect(nodeActionEnabled(action({ available: false, unavailable_at: [] }), planned)).toBe(false)
  })

  it('treats an absent per-node list as no refinement', () => {
    expect(nodeActionEnabled(action({ unavailable_at: undefined }), planned)).toBe(true)
  })
})

// Review 111: a per-node block must not borrow the tree-level "requirement
// unmet" wording — there the prerequisite is satisfiable, and the node's own
// artifact is already in place.
describe('nodeActionReason', () => {
  const planned = { path: 'specs/001-a/spec.md' } as SDDNode
  const unplanned = { path: 'specs/002-b/spec.md' } as SDDNode
  const plan = action({
    kind: 'plan',
    concept: 'feature',
    unavailable_at: ['specs/001-a/spec.md'],
  })

  it('names the per-node gate on a blocked node', () => {
    expect(nodeActionReason(plan, planned)).toBe(NODE_GATE_REASON)
    expect(NODE_GATE_REASON).not.toBe(REQUIREMENT_UNMET_REASON)
  })

  it('has no reason on an enabled node', () => {
    expect(nodeActionReason(plan, unplanned)).toBe('')
  })

  it('keeps the tree-level wording when the whole gesture is unavailable', () => {
    expect(nodeActionReason(action({ available: false }), planned)).toBe(REQUIREMENT_UNMET_REASON)
    expect(nodeActionReason(action({ skill: '' }), planned)).toBe(
      'The framework declares this action with no skill to run',
    )
  })
})
