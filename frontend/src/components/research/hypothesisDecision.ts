// Iteration-decision vocabulary shared by every research UI surface that
// records a hypothesis decision (the workspace detail card's select).
//
// Mirrors the research methodology (the research-decision skill): after each
// experiment the researcher chooses one of four decisions — continue (deepen
// the confirmed direction), pivot (chase the more promising direction), kill
// (prune the dead end), fork (investigate competing approaches in parallel).
// Unlike the status state machine, any decision may replace any other, and
// '' means "not yet decided" (the card template renders "—").
//
// The backend stores the card's Decision field verbatim (no vocabulary
// validation), so a card written by an older flow may carry a free-text
// value — decisionOptions keeps it visible so it stays re-savable and can be
// replaced with a canonical one.
//
// Pure data + a pure helper — no React/DOM dependencies, unit-testable in
// isolation.
import type { HypothesisDecision } from '@/types/models'

/** The canonical iteration decisions, in the methodology's order. */
export const DECISIONS: readonly HypothesisDecision[] = [
  'continue',
  'pivot',
  'kill',
  'fork',
]

/** The value an undecided hypothesis carries ('' — the card renders "—"). */
export const DECISION_UNDECIDED = ''

/** Whether `v` is one of the canonical decision values. */
export function isDecision(v: string): v is HypothesisDecision {
  return (DECISIONS as readonly string[]).includes(v)
}

/** Human label for a decision option ('' renders as "undecided", mirroring
 *  the select's placeholder semantics; canonical values render as-is). */
export function decisionLabel(v: string): string {
  return v === DECISION_UNDECIDED ? 'undecided' : v
}

// decisionOptions returns the undecided option followed by the canonical
// four. A non-canonical current value (legacy free text on an older card) is
// kept first so the controlled <select> always has a matching option and the
// value remains re-savable — the same fail-safe statusOptions applies to
// unknown statuses.
export function decisionOptions(current: string): string[] {
  const extra = current !== DECISION_UNDECIDED && !isDecision(current) ? [current] : []
  return [...extra, DECISION_UNDECIDED, ...DECISIONS]
}
