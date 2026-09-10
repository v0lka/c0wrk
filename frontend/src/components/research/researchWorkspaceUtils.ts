// Pure helpers for the Research workspace's editable hypothesis card.
//
// No React/DOM dependencies — fully unit-testable in isolation. The card draft
// and the change-set derivation (`buildUpdateFields`) live here so the
// ResearchWorkspace component file exports components only (fast-refresh safe).

import type {
  HypothesisNode,
  HypothesisUpdateFields,
  HypothesisStatus,
  HypothesisDraft,
} from '@/types/models'

/** Default status applied when the original node carries an unknown/empty one. */
const DEFAULT_STATUS = 'open'

/** Canonical zero-padding of hypothesis ids (H-NNN, three digits). */
const ID_PAD = 3

/**
 * Parse a free-form parents input string ("H-001, h2, H-003") into the
 * canonical id list: `H-?\d+` tokens, upper-cased, hyphenated, zero-padded to
 * the canonical three digits, de-duplicated, sorted. Mirrors the backend's
 * normalizeParents vocabulary (which drops every non-identifier token), so
 * what the dirty check compares is exactly what a save would send.
 */
export function parseParentIds(raw: string): string[] {
  const ids = new Set<string>()
  for (const m of raw.matchAll(/h-?(\d+)/gi)) {
    ids.add(`H-${m[1]!.padStart(ID_PAD, '0')}`)
  }
  return [...ids].sort()
}

/** Initialise the edit draft from a hypothesis node (missing fields → '').
 *  `parents` is rendered as the comma-separated input form of the node's
 *  parent list. Status falls back to DEFAULT_STATUS so an unknown/empty card
 *  status still renders a valid `<select>` value and never produces an empty
 *  `status` update (which the backend rejects as an unknown status). This
 *  mirrors the `original.status || DEFAULT_STATUS` baseline in
 *  buildUpdateFields. */
export function draftFromNode(node: HypothesisNode): HypothesisDraft {
  return {
    title: node.title,
    parents: (node.parents ?? []).join(', '),
    status: node.status || DEFAULT_STATUS,
    decision: node.decision ?? '',
    statement: node.statement ?? '',
    verification_criterion: node.verification_criterion ?? '',
    experiment_notes: node.experiment_notes ?? '',
    timebox: node.timebox ?? '',
    result: node.result ?? '',
  }
}

/** Same-length, same-order id-list equality (both sides canonical + sorted). */
function parentIdsEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  return a.every((id, i) => id === b[i])
}

/**
 * Derive the `HypothesisUpdateFields` to send for a save, including only the
 * fields whose draft value differs from the original node. An empty object
 * means "nothing changed" (callers skip the RPC). Parents are compared as
 * canonical sets: reformatting the input ("H-002, H-001" vs "H-001,H-002")
 * is not a change, while adding/removing/re-typing an id is. Pure and
 * unit-tested.
 */
export function buildUpdateFields(
  original: Pick<
    HypothesisNode,
    | 'title'
    | 'parents'
    | 'status'
    | 'decision'
    | 'statement'
    | 'verification_criterion'
    | 'experiment_notes'
    | 'timebox'
    | 'result'
  >,
  draft: HypothesisDraft,
): HypothesisUpdateFields {
  const fields: HypothesisUpdateFields = {}
  if (draft.title !== original.title) {
    fields.title = draft.title
  }
  const parents = parseParentIds(draft.parents)
  if (!parentIdsEqual(parents, parseParentIds((original.parents ?? []).join(', ')))) {
    fields.parents = parents
  }
  if (draft.status !== (original.status || DEFAULT_STATUS)) {
    fields.status = draft.status as HypothesisStatus
  }
  if (draft.decision !== (original.decision ?? '')) {
    fields.decision = draft.decision
  }
  if (draft.statement !== (original.statement ?? '')) {
    fields.statement = draft.statement
  }
  if (draft.verification_criterion !== (original.verification_criterion ?? '')) {
    fields.verification_criterion = draft.verification_criterion
  }
  if (draft.experiment_notes !== (original.experiment_notes ?? '')) {
    fields.experiment_notes = draft.experiment_notes
  }
  if (draft.timebox !== (original.timebox ?? '')) {
    fields.timebox = draft.timebox
  }
  if (draft.result !== (original.result ?? '')) {
    fields.result = draft.result
  }
  return fields
}
