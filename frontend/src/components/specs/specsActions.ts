// Dispatch constants for the SPECS panel (ADR-106 D4, labels ADR-109).
//
// The panel turns every mutating lifecycle gesture into a dispatched
// `spec-*` skill (via sendMessage's activeSkills slot) instead of writing
// to specs/ itself — the researchActions.ts pattern: every dispatched
// prompt lives here as a constant so the wiring stays auditable and the
// components render only.

import type { SpecFinding } from '@/types/models'

/** A single quick action: a human label, the `spec-*` skill it activates
 *  (ADR-105), the constant prompt dispatched alongside the skill, and
 *  whether the gesture requires an existing specs/ tree. The labels are
 *  deliberately SHORT (the row is a narrow sidebar strip — ADR-109); the
 *  optional fullLabel spells the gesture out in the button's tooltip. */
export interface SpecQuickAction {
  key: string
  label: string
  /** The expanded gesture name for the tooltip; falls back to the label. */
  fullLabel?: string
  skill: string
  prompt: string
  /** True when the gesture needs a present specs/ tree (the button renders
   *  disabled with a reason tooltip while it is missing). Initialize is
   *  always available — it is also the empty state's CTA and may repair a
   *  partial tree. */
  requiresSpecs?: boolean
}

/** The Initialize gesture, shared verbatim by the quick-actions row and
 *  the empty state's CTA — one constant, one dispatch path. */
export const INITIALIZE_ACTION: SpecQuickAction = {
  key: 'initialize',
  label: 'Init',
  fullLabel: 'Initialize specs',
  skill: 'spec-init',
  prompt: 'Initialize the specs/ specification system for this project.',
}

/** The fixed quick-action row (ADR-106 D4): Init, Check (the tier-2
 *  semantic-drift surface, ADR-106 D5), Consult, Explore. */
export const QUICK_ACTIONS: SpecQuickAction[] = [
  INITIALIZE_ACTION,
  {
    key: 'check-drift',
    label: 'Check',
    fullLabel: 'Check drift',
    skill: 'spec-check',
    prompt:
      'Check the specs/ tree for drift between the specifications and the implementation, and report every finding.',
    requiresSpecs: true,
  },
  {
    key: 'consult',
    label: 'Consult',
    skill: 'spec-consult',
    prompt: 'Consult the specs relevant to the current task before starting work.',
    requiresSpecs: true,
  },
  {
    key: 'explore',
    label: 'Explore',
    skill: 'spec-explore',
    prompt:
      'Explore the specification system and turn the requested change into an approved roadmap with a spec footprint.',
    requiresSpecs: true,
  },
]

/** The verbatim "- [kind] file: detail" line for ONE finding — the single
 *  source of the deterministic finding format (ADR-109). The aggregate
 *  Fix-findings prompt, the per-finding Fix-finding prompt, and the finding
 *  card's copy button all render through it, so the dispatched task sees
 *  exactly what the panel shows and the surfaces cannot drift apart. */
export function findingLine(finding: SpecFinding): string {
  return `- [${finding.kind}] ${finding.file}: ${finding.detail}`
}

/** The repair instructions shared verbatim by the aggregate Fix-findings
 *  prompt and the per-finding Fix-finding prompt — one constant so the two
 *  `spec-update` gestures cannot drift apart in what they ask the task to
 *  do with the listed findings. */
const FIX_INSTRUCTIONS =
  'Update the spec files so every finding resolves: fix or remove broken paths and cross-references, renumber duplicate ADR records per the renumber protocol, register unlisted files in the INDEX.md directory listing, restore missing root files, and repair ADR numbering/supersession inconsistencies. When done, re-verify that the tree parses clean.'

/** Build the Fix-findings gesture for the CURRENT parse (ADR-109): the
 *  header's findings badge dispatches one `spec-update` task whose prompt
 *  enumerates every tier-1 finding, so the chat resolves all of them in
 *  one pass and re-verifies. The prompt is derived verbatim from the parse
 *  via findingLine — no prose paraphrasing that could drift from what the
 *  panel shows. */
export function buildFixFindingsAction(findings: SpecFinding[]): SpecQuickAction {
  const lines = findings.map(findingLine)
  const n = findings.length
  return {
    key: 'fix-findings',
    label: 'Fix findings',
    fullLabel: `Fix all ${n} integrity finding${n === 1 ? '' : 's'}`,
    skill: 'spec-update',
    prompt:
      'Fix every tier-1 integrity finding in the specs/ tree listed below. ' +
      FIX_INSTRUCTIONS +
      '\n\nFindings:\n' +
      lines.join('\n'),
  }
}

/** Build the per-finding Fix gesture (ADR-109): a finding card dispatches
 *  one `spec-update` task scoped to that single finding, so a targeted
 *  repair doesn't drag the whole parse's backlog into the chat. The prompt
 *  shares the aggregate's repair instructions (FIX_INSTRUCTIONS) and quotes
 *  the finding verbatim via findingLine — the line the card's copy button
 *  hands out is byte-identical to the one the dispatched task sees. */
export function buildFixFindingAction(finding: SpecFinding): SpecQuickAction {
  return {
    key: 'fix-finding',
    label: 'Fix finding',
    fullLabel: 'Fix this integrity finding',
    skill: 'spec-update',
    prompt:
      'Fix the following tier-1 integrity finding in the specs/ tree listed below. ' +
      FIX_INSTRUCTIONS +
      '\n\nFindings:\n' +
      findingLine(finding),
  }
}

/** The disabled-reason tooltip for a requiresSpecs action while no specs/
 *  tree exists (the research "no workflow-dead buttons" contract: every
 *  disabled button explains why). */
export const NO_SPECS_REASON = 'No specs/ directory yet — initialize first'
