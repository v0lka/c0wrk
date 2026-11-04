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
// The bare command's leading token only: "/goal" at a whitespace/EOF
// boundary, no ":N" — the one spelling the mention pipeline can extract and
// strip as a plain /-token (a "/goal:N" token is never a mention: the colon
// breaks the plain-token boundary).
const PLAIN_LEADING_GOAL_RE = /^\/goal(?:[ \t\n\r\f]+|$)/

/** The built-in goal command's token — the one plain slash-name that is a
 *  command, never a catalog name (ADR-106). The send path's mention
 *  partition exempts it from the permissive skill passthrough. */
export const GOAL_COMMAND_NAME = 'goal'

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

/** True when the message's LEADING plain "/goal" token was partitioned as a
 *  mention — "goal" rides one of the threaded mention arrays, the ADR-106
 *  shadowing configuration: the backend's catalog-gated mention strip then
 *  consumes the token (it is in the caller-provided list), no goal arms,
 *  and the optimistic goal stamp must stay off. The budgeted "/goal:N"
 *  spelling is never a mention token, so an activation from that form is
 *  always honest; a "goal" threading with no leading plain token (a
 *  qualified mention elsewhere in the text) leaves an exposed prefix to be
 *  stamped by the backend's persisted metadata instead. */
export function goalCommandShadowedByMention(
  text: string,
  activeSkills?: string[],
  activeAgents?: string[],
  activeMCPServers?: string[],
): boolean {
  if (!PLAIN_LEADING_GOAL_RE.test(text)) return false
  return (
    (activeSkills?.includes(GOAL_COMMAND_NAME) ?? false) ||
    (activeAgents?.includes(GOAL_COMMAND_NAME) ?? false) ||
    (activeMCPServers?.includes(GOAL_COMMAND_NAME) ?? false)
  )
}
