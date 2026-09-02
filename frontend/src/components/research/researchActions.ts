// Dispatch constants for the RESEARCH control dashboard.
//
// The dashboard turns the panel from a passive mirror into an active control
// surface: it dispatches `research-*` skills (via sendMessage's activeSkills
// slot) instead of asking the user to type `/skill` refs by hand. Every
// dispatched prompt lives here as a constant so the wiring stays auditable and
// the components render only.

import type { ResearchActionKind, ResearchNextStep } from '@/types/models'

/** Prompt dispatched for each recommended next-step action kind. Keyed by the
 *  action kind (which also names the implementing research-* skill). */
export const NEXT_STEP_PROMPTS: Record<ResearchActionKind, string> = {
  'research-init': 'Initialize a new research project.',
  'research-hypothesis': 'Formulate the first hypothesis for the research project.',
  'research-experiment': 'Run an experiment for the leading active hypothesis.',
  'research-decision':
    'Review the research results and decide the next direction (continue, pivot, kill, or fork).',
  'research-synthesis': 'Synthesize the final research report.',
}

/** Build the dispatch prompt for a recommendation. When the recommendation is
 *  scoped to a hypothesis, the target is appended so the skill knows which
 *  hypothesis to operate on. */
export function buildNextStepPrompt(nextStep: ResearchNextStep): string {
  const base = NEXT_STEP_PROMPTS[nextStep.action]
  return nextStep.target ? `${base} Target hypothesis: ${nextStep.target}.` : base
}

/** A single quick action: a human label, the research-* skill it activates, and
 *  the constant prompt dispatched alongside the skill. */
export interface ResearchQuickAction {
  key: string
  label: string
  skill: string
  prompt: string
}

/** The fixed quick-action row. Each entry maps a research lifecycle gesture to
 *  the research-* skill that implements it. "Create hypothesis" and "Record
 *  result" both activate `research-hypothesis` (per its SKILL.md it owns
 *  "formulating a new hypothesis" and "recording experiment results / status"),
 *  differentiated only by the dispatched prompt. */
export const QUICK_ACTIONS: ResearchQuickAction[] = [
  {
    key: 'hypothesis',
    label: 'Create hypothesis',
    skill: 'research-hypothesis',
    prompt: 'Create a new hypothesis.',
  },
  {
    key: 'experiment',
    label: 'Run experiment',
    skill: 'research-experiment',
    prompt: 'Run an experiment for the active hypothesis.',
  },
  {
    key: 'record-result',
    label: 'Record result',
    skill: 'research-hypothesis',
    prompt: 'Record the result of the last experiment and update the hypothesis status.',
  },
  {
    key: 'decision',
    label: 'Decision',
    skill: 'research-decision',
    prompt: 'Review the research results and decide the next direction (continue, pivot, kill, or fork).',
  },
  {
    key: 'synthesize',
    label: 'Synthesize',
    skill: 'research-synthesis',
    prompt: 'Synthesize the final research report.',
  },
]
