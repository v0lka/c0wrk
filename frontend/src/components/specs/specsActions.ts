// Dispatch helpers for the SPECS panel.
//
// Every mutating gesture of the panel is a declared lifecycle ACTION of the
// active framework: the DSL carries its label, its agent skill and its
// instruction prompt, and the panel only turns that declaration into a
// `sendMessage` call (the researchActions.ts pattern). No action, no button —
// the frontend owns no gesture vocabulary of its own and never writes to the
// tree.

import type { SDDAction, SDDFinding, SDDNode } from '@/types/models'

/** The reserved action kind whose gesture is the findings badge: a framework
 *  that declares it opts into "fix the findings" as one dispatched task. */
export const FINDINGS_FIX_KIND = 'fix-findings'

/** One ready-to-send dispatch: the skill to activate and the instruction
 *  prompt (placeholders already substituted). */
export interface SpecsDispatch {
  /** Stable identity for keys/tests: the action kind, suffixed by the node
   *  path for a node-scoped gesture. */
  key: string
  skill: string
  prompt: string
}

/** Where an action appears: `node` — the action declares a `concept`, so it
 *  renders on every node instance of that concept (Apply on a change, Plan on
 *  a feature); `toolbar` — a panel-level gesture (the quick-actions row). */
export type SDDActionScope = 'node' | 'toolbar'

export function actionScope(action: SDDAction): SDDActionScope {
  return action.concept !== undefined && action.concept !== '' ? 'node' : 'toolbar'
}

/** Substitute the node placeholders a declared prompt may carry: `{path}` is
 *  the node's workspace-relative path, `{name}` its base name. ONE callback
 *  pass inserts both values LITERALLY — sequential string replacements would
 *  interpret `$`-sequences in a file path (`$$`, `$&`) as replacement
 *  templates, and a second pass over the substituted text would rewrite a
 *  literal `{name}` inside a path. Unknown placeholders are left untouched —
 *  the text belongs to the framework document, and mangling it would hide a
 *  declaration mistake. */
export function substituteActionText(text: string, node?: Pick<SDDNode, 'path' | 'name'>): string {
  if (node === undefined) return text.replace(/\{(path|name)\}/g, '')
  return text.replace(/\{(path|name)\}/g, (_, key: string) =>
    key === 'path' ? node.path : node.name,
  )
}

/**
 * Turn a declared action into a dispatch, or null when the framework declared
 * no `skill` — an action without a skill is not a dispatchable gesture and the
 * panel must not invent one. The prompt may carry `{path}`/`{name}`, so the
 * node context is threaded through it.
 */
export function actionToDispatch(
  action: SDDAction,
  node?: Pick<SDDNode, 'path' | 'name'>,
): SpecsDispatch | null {
  const skill = action.skill ?? ''
  if (skill === '') return null
  const prompt = substituteActionText(action.prompt ?? action.label, node)
  return {
    key: node === undefined ? action.kind : `${action.kind}:${node.path}`,
    skill,
    prompt,
  }
}

/** The actions that render on a node of `concept`: node-scoped, carrying a
 *  skill, and not the reserved findings gesture. An action whose declared
 *  requirements are unmet stays in the list — the row renders it DISABLED
 *  with the reason (the ADR-126 contract: an unavailable action names why and
 *  what to do next; hiding it hides the workflow's next step). Callers gate
 *  the actual dispatch on `available`. */
export function nodeActionsFor(actions: SDDAction[], concept: string): SDDAction[] {
  if (concept === '') return []
  return actions.filter(
    (action) =>
      action.concept === concept &&
      action.kind !== FINDINGS_FIX_KIND &&
      (action.skill ?? '') !== '',
  )
}

/** Whether a node action may run on THIS node: the tree-level `available` gate
 *  plus the backend's per-node refinement, which lists the holder nodes where a
 *  `companion:`/`absent:` requirement fails — the feature that already has its
 *  plan.md, versus the one that still lacks it. The disabled row keeps naming
 *  the reason through `inertActionReason`. */
export function nodeActionEnabled(action: SDDAction, node: SDDNode): boolean {
  if (!action.available) return false
  return !(action.unavailable_at ?? []).includes(node.path)
}

/** The quick-actions row: panel-level gestures only — node actions belong to
 *  their nodes, and the findings gesture belongs to the findings badge. */
export function toolbarActions(actions: SDDAction[]): SDDAction[] {
  return actions.filter(
    (action) => actionScope(action) === 'toolbar' && action.kind !== FINDINGS_FIX_KIND,
  )
}

/** The framework's declared findings-fix gesture, or null when it declared
 *  none — in which case the findings badge renders INERT (it reports, it does
 *  not act). */
export function findingsFixAction(actions: SDDAction[]): SDDAction | null {
  const action = actions.find((candidate) => candidate.kind === FINDINGS_FIX_KIND)
  return action ?? null
}

/** The gesture that may run without an existing tree (an `init`): the empty
 *  state's CTA. Recognised by declaring no requirement at all, so it stays
 *  framework-agnostic — a framework that offers no bootstrap simply has no
 *  CTA. */
export function bootstrapAction(actions: SDDAction[]): SDDAction | null {
  const action = toolbarActions(actions).find(
    (candidate) => candidate.requires.length === 0 && (candidate.skill ?? '') !== '',
  )
  return action ?? null
}

/** The verbatim "- [kind] file: detail" line for ONE finding — the single
 *  source of the deterministic finding format. The dispatched findings-fix
 *  prompt and each finding card's copy button both render through it, so the
 *  task sees exactly what the panel shows and the surfaces cannot drift. */
export function findingLine(finding: SDDFinding): string {
  return `- [${finding.kind}] ${finding.file}: ${finding.detail}`
}

/**
 * Build the findings-fix dispatch: the framework's OWN instruction from the
 * declared action, followed by the current parse's findings verbatim, so one
 * dispatched task resolves everything the panel reports and re-verifies. Null
 * when the framework declared no such action or no skill to run it.
 */
export function findingsFixDispatch(
  action: SDDAction | null,
  findings: SDDFinding[],
): SpecsDispatch | null {
  if (action === null) return null
  const skill = action.skill ?? ''
  if (skill === '') return null
  // An unavailable gesture must not dispatch: the badge and the per-finding
  // chips render it inert with the reason instead (the toolbar's contract —
  // no workflow-invalid gesture runs, whatever surface built it).
  if (!action.available) return null
  const head = (action.prompt ?? action.label).trim()
  const lines = findings.map(findingLine).join('\n')
  return {
    key: FINDINGS_FIX_KIND,
    skill,
    prompt: `${head}\n\nFindings:\n${lines}`,
  }
}

/** Whether one declared action can actually run: declared, carrying a skill,
 *  and available for the parsed tree. The ONE predicate every dispatch
 *  surface shares — the quick-actions row, the findings badge, the per-finding
 *  chips and the node actions — so no surface can show a live button another
 *  surface correctly refuses. */
export function dispatchableAction(action: SDDAction | null): action is SDDAction {
  return action !== null && (action.skill ?? '') !== '' && action.available
}

/** The disabled-reason tooltip shared by every gesture whose declared
 *  requirement is unmet (the research "no workflow-dead buttons" contract:
 *  every disabled button explains why). */
export const REQUIREMENT_UNMET_REASON = 'Not available for this tree yet'

/** Why a DECLARED skill cannot run: the framework's declared skill set is
 *  satisfied only PARTIALLY — detection matches `all_of` plus one name of
 *  `any_of` — so this gesture's own skill was never installed, while the tree
 *  it would act on is complete. Distinct from both neighbours: the declaration
 *  is complete (unlike a skill-less action) and the tree may be complete too
 *  (unlike an unmet requirement), so the operator's remedy is to install the
 *  skill. Naming it is the point — a generic "not available" sends the reader
 *  hunting for a tree problem that does not exist. */
export function skillMissingReason(skill: string): string {
  return `The framework's skill \`${skill}\` is not installed in this workspace`
}

/** Why one DECLARED gesture renders inert: it carries no skill to run, its
 *  declared skill is not installed here, or its requirements are unmet for this
 *  tree. The single wording source for every disabled/inert tooltip, so no
 *  surface misdiagnoses the fault — a skillless action is not "requirements
 *  unmet", a missing skill is not a tree problem, and an unmet requirement is
 *  not a missing declaration. */
export function inertActionReason(action: SDDAction): string {
  const skill = action.skill ?? ''
  if (skill === '') return 'The framework declares this action with no skill to run'
  // Checked before the requirement branch: an absent skill is the decisive,
  // actionable fault, and it stays true whatever the tree looks like.
  if (action.skill_available === false) return skillMissingReason(skill)
  return REQUIREMENT_UNMET_REASON
}

/** The reason a node action is inert AT THIS NODE specifically: the tree-level
 *  gate is satisfiable (some holder still needs the step), but this node's own
 *  artifacts do not meet it — it already has the artifact the step creates, or
 *  still lacks the companion the step builds on. Deliberately distinct from
 *  REQUIREMENT_UNMET_REASON, which would claim a missing prerequisite while
 *  this node's problem is the opposite. */
export const NODE_GATE_REASON =
  "This item's own artifacts do not meet the step's prerequisite"

/** The inert reason for a node action, choosing the tree-level wording when the
 *  whole gesture is unavailable and the per-node wording when only this node is
 *  gated. */
export function nodeActionReason(action: SDDAction, node: SDDNode): string {
  if ((action.skill ?? '') === '' || !action.available) return inertActionReason(action)
  return nodeActionEnabled(action, node) ? '' : NODE_GATE_REASON
}

/** The inert reason for a findings-fix gesture the framework never declared
 *  at all — distinct from inertActionReason, which explains a DECLARED
 *  gesture. One wording source for the header badge and the finding cards. */
export const NO_FIX_FINDINGS_DECLARED_REASON =
  'The active framework declares no fix-findings action'
