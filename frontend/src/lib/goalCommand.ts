// Frontend mirror of the backend "/goal" command grammar
// (core/message_preprocess.go DetectAndStripGoalMode):
//
//   /goal     — arms goal mode, no turn budget
//   /goal:N   — arms goal mode with an N-turn budget (N ≥ 1)
//
// Invalid ":N" forms ("0", non-digit, no digit after the colon, out of range)
// are INERT: no activation, the text stays verbatim. Mirroring the backend
// byte-for-byte keeps the optimistic Goal badge (useMessageSender) honest
// about what the send will actually arm. specs/domains/goal-mode.md.

// The boundary class is ASCII-only on purpose: Go's RE2 `\s` is
// [\t\n\f\r ], while JavaScript `\s` also accepts a trailing Unicode line
// separator the backend would reject (same parity note as
// parseReferences.REF_BOUNDARY_SPACE_SOURCE).
const GOAL_COMMAND_RE = /^\/goal(?:[ \t\n\r\f]+|$|:(\d+)(?:[ \t\n\r\f]+|$))/

export interface GoalCommandMatch {
  /** True when a leading "/goal"/"/goal:N" arms goal mode for this message. */
  activates: boolean
  /** The "/goal:N" turn budget; null for the bare form or a non-match. */
  budgetTurns: number | null
}

/** Detect a leading "/goal"/"/goal:N" command on the RAW message text (the
 *  optimistic-send mirror of DetectAndStripGoalMode; the authoritative
 *  detection runs backend-side on the post-preprocessing text, so a prefix
 *  exposed by stripping a /-mention is stamped by the persisted metadata and
 *  surfaces on reload). */
export function detectGoalCommand(text: string): GoalCommandMatch {
  const m = GOAL_COMMAND_RE.exec(text)
  if (m === null) return { activates: false, budgetTurns: null }
  if (m[1] !== undefined) {
    const n = Number(m[1])
    // Atoi parity: the backend rejects 0 outright and anything beyond int64
    // (ErrRange); MAX_SAFE_INTEGER stands in for the absurd upper bound here.
    if (!Number.isSafeInteger(n) || n < 1) return { activates: false, budgetTurns: null }
    return { activates: true, budgetTurns: n }
  }
  return { activates: true, budgetTurns: null }
}
