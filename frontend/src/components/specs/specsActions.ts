// Dispatch constants for the SPECS panel (ADR-105 D4).
//
// The panel turns every mutating lifecycle gesture into a dispatched
// `spec-*` skill (via sendMessage's activeSkills slot) instead of writing
// to specs/ itself — the researchActions.ts pattern: every dispatched
// prompt lives here as a constant so the wiring stays auditable and the
// components render only.

/** A single quick action: a human label, the `spec-*` skill it activates
 *  (ADR-104), the constant prompt dispatched alongside the skill, and
 *  whether the gesture requires an existing specs/ tree. */
export interface SpecQuickAction {
  key: string
  label: string
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
  label: 'Initialize specs',
  skill: 'spec-init',
  prompt: 'Initialize the specs/ specification system for this project.',
}

/** The fixed quick-action row (ADR-105 D4): Initialize specs, Check drift
 *  (the tier-2 semantic-drift surface, ADR-105 D5), Consult, Explore. */
export const QUICK_ACTIONS: SpecQuickAction[] = [
  INITIALIZE_ACTION,
  {
    key: 'check-drift',
    label: 'Check drift',
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

/** The disabled-reason tooltip for a requiresSpecs action while no specs/
 *  tree exists (the research "no workflow-dead buttons" contract: every
 *  disabled button explains why). */
export const NO_SPECS_REASON = 'No specs/ directory yet — initialize first'
