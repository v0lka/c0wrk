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
