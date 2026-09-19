import type { AutonomyDecisionData } from '@/types/events'

/**
 * Human-readable one-line notice for an automatic (no-human) security decision
 * taken under an automatic autonomy posture — assisted or silent. Shared by the
 * live `autonomy_decision` handler and the history-reload reconstruction
 * (`reconstructContent`) so a reloaded row reads identically to the live
 * notice — the audit trail must not change shape across a reload (OWASP ASI10).
 *
 * The text is deliberately explicit about the posture, the sub-policy, the
 * verdict and WHY, since no confirmation card was ever shown to the operator.
 */
export function autonomyDecisionContent(data: AutonomyDecisionData): string {
  const justification = data.justification ? ` — ${data.justification}` : ''
  const posture = data.mode === 'assisted' ? 'Assisted mode' : 'Silent mode'
  // The sub-policy is a posture setting, NOT the decision — render it as
  // `«Allow» policy` (capitalized, quoted, labeled) so it cannot be misread
  // as the verdict; the verdict follows after the colon ("allowed"/"denied").
  const policy = data.policy ? ` («${policyTitle(data.policy)}» policy)` : ''

  if (data.kind === 'step_limit') {
    const at = data.current_step !== undefined && data.max_steps !== undefined
      ? ` at step ${data.current_step}/${data.max_steps}`
      : ''
    const breaker = data.category === 'circuit_breaker' ? ' (circuit breaker)' : ''
    return `${posture}: step limit ${data.verdict}${at}${breaker}${justification}`
  }

  const action = data.verdict === 'allow' ? 'allowed' : 'denied'
  const target = data.tool ? ` ${data.tool}` : ''
  const reason = data.reason ? ` (${data.reason})` : ''
  return `${posture}${policy}: ${action}${target}${reason}${justification}`
}

/** Capitalize a sub-policy id for the notice title: "allow" → "Allow". */
function policyTitle(policy: string): string {
  return policy.charAt(0).toUpperCase() + policy.slice(1)
}

// ---------------------------------------------------------------------------
// Autonomy-decision card helpers — the structured counterpart to the
// one-line notice above. The card (AutonomyDecisionBlock) renders the verdict
// as an icon/tone and the decision as labeled rows, so these helpers keep the
// verdict→tone/title mapping in one place rather than in the component.
// ---------------------------------------------------------------------------

/**
 * True when a verdict is in the ALLOW family. `allow` covers a tool_confirm
 * allow; `allow_once` / `allow_more` / `allow_always` cover the step-limit
 * allows. The card paints the success tone for any allow-family verdict.
 */
export function isAutonomyAllowVerdict(verdict: string): boolean {
  return verdict.startsWith('allow')
}

/**
 * Header title naming the gate the automatic decision resolved. A tool-call
 * gate (a confirmation-gated call resolved in silent mode, or a strict-judge
 * auto-deny) reads "Tool Call (auto)"; a step-limit boundary reads
 * "Step Limit (auto)".
 */
export function autonomyDecisionTitle(data: AutonomyDecisionData): 'Tool Call (auto)' | 'Step Limit (auto)' {
  return data.kind === 'step_limit' ? 'Step Limit (auto)' : 'Tool Call (auto)'
}

/**
 * Concise one-line verdict for the card body: what was decided and, for a
 * tool-call gate, against which tool. The full audit sentence (posture,
 * sub-policy, trigger) stays on the notice content and the labeled rows.
 */
export function autonomyDecisionVerdictLine(data: AutonomyDecisionData): string {
  if (data.kind === 'step_limit') {
    const at = data.current_step !== undefined && data.max_steps !== undefined
      ? ` at step ${data.current_step}/${data.max_steps}`
      : ''
    return `Step limit: ${data.verdict}${at}`
  }
  const action = isAutonomyAllowVerdict(data.verdict) ? 'Allowed' : 'Denied'
  return data.tool ? `${action}: ${data.tool}` : action
}
