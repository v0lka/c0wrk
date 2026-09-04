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

/** Initialise the edit draft from a hypothesis node (missing fields → '').
 *  Status falls back to DEFAULT_STATUS so an unknown/empty card status still
 *  renders a valid `<select>` value and never produces an empty `status`
 *  update (which the backend rejects as an unknown status). This mirrors the
 *  `original.status || DEFAULT_STATUS` baseline in buildUpdateFields. */
export function draftFromNode(node: HypothesisNode): HypothesisDraft {
  return {
    status: node.status || DEFAULT_STATUS,
    result: node.result ?? '',
    timebox: node.timebox ?? '',
  }
}

/**
 * Derive the `HypothesisUpdateFields` to send for a save, including only the
 * fields whose draft value differs from the original node. An empty object
 * means "nothing changed" (callers skip the RPC). Pure and unit-tested.
 */
export function buildUpdateFields(
  original: Pick<HypothesisNode, 'status' | 'result' | 'timebox'>,
  draft: HypothesisDraft,
): HypothesisUpdateFields {
  const fields: HypothesisUpdateFields = {}
  if (draft.status !== (original.status || DEFAULT_STATUS)) {
    fields.status = draft.status as HypothesisStatus
  }
  if (draft.result !== (original.result ?? '')) {
    fields.result = draft.result
  }
  if (draft.timebox !== (original.timebox ?? '')) {
    fields.timebox = draft.timebox
  }
  return fields
}
