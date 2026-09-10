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
 *  the constant prompt dispatched alongside the skill. `updatePrompt` is the
 *  alternate prompt dispatched when the action's outcome already exists (e.g.
 *  synthesize → update report). */
export interface ResearchQuickAction {
  key: string
  label: string
  skill: string
  prompt: string
  updatePrompt?: string
}

/** Build the dispatch prompt for a Run-experiment gesture scoped to a specific
 *  hypothesis: the target id and title are spelled out so the skill knows
 *  exactly which hypothesis to experiment on. */
export function buildExperimentPrompt(hypothesis: {
  id: string
  title: string
}): string {
  return `Run an experiment for hypothesis ${hypothesis.id}: ${hypothesis.title}.`
}

/** Build the dispatch prompt for a Record-result gesture scoped to a specific
 *  hypothesis: the target id and title are spelled out so the skill records
 *  the experiment result on the right card. */
export function buildRecordResultPrompt(hypothesis: {
  id: string
  title: string
}): string {
  return `Record the result of the last experiment for hypothesis ${hypothesis.id}: ${hypothesis.title}, and update its status.`
}

/** Build the dispatch prompt for a Decision gesture scoped to a specific
 *  hypothesis: the target id and title are spelled out so the review decides
 *  that card's next direction. */
export function buildDecisionPrompt(hypothesis: {
  id: string
  title: string
}): string {
  return `Review the results for hypothesis ${hypothesis.id}: ${hypothesis.title} and decide the next direction (continue, pivot, kill, or fork).`
}

/** The Create-hypothesis gesture rendered by the ResearchHypothesisPicker's
 *  plus button: dispatches `research-hypothesis` (which owns "formulating a
 *  new hypothesis" per its SKILL.md) with a constant prompt. Kept here (not
 *  in QUICK_ACTIONS, which renders only the panel's four lifecycle buttons)
 *  so every dispatched prompt stays in this auditable module. */
export const CREATE_HYPOTHESIS_ACTION: ResearchQuickAction = {
  key: 'hypothesis',
  label: 'Create hypothesis',
  skill: 'research-hypothesis',
  prompt: 'Create a new hypothesis.',
}

/** The fixed quick-action row: one button per research lifecycle gesture,
 *  each mapped to the research-* skill that implements it. "Record result"
 *  activates `research-hypothesis` (per its SKILL.md it owns "recording
 *  experiment results / status"), differentiated from Create hypothesis only
 *  by the dispatched prompt. Enablement is derived from the dashboard's
 *  selected hypothesis and the project's graph state (see
 *  ResearchQuickActions): Run experiment requires an open/in-progress
 *  selected card, Record result an in-progress one, Decision at least one
 *  terminal hypothesis, and Synthesize at least one hypothesis at all. */
export const QUICK_ACTIONS: ResearchQuickAction[] = [
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
    updatePrompt: 'Update the existing research report with the latest results.',
  },
]
