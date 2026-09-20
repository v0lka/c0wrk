import type { AutonomyDecisionData, NetworkDecisionData } from '@/types/events'

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
  const net = networkDecisionLine(data)
  const netSuffix = net ? ` [network: ${net}]` : ''
  return `${posture}${policy}: ${action}${target}${reason}${justification}${netSuffix}`
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

// ---------------------------------------------------------------------------
// Network-decision helpers — the diagnosable network data-flow of a shell
// decision (cradle / ingest / fetch + host + operands). Kept here so the tone
// and the copy that distinguish a canonical cradle from a non-canonical
// ingest live in one place rather than being duplicated across the notice and
// the card.
// ---------------------------------------------------------------------------

/** True for a canonical network flow (a download cradle — a fired control a
 *  host must never auto-override). False for an ingest (judge-clearable) and a
 *  clean fetch. */
export function isCanonicalNetworkFlow(flow: string): boolean {
  return flow === 'cradle'
}

/** Human label for a network flow. */
export function networkFlowLabel(flow: string): string {
  switch (flow) {
    case 'cradle': return 'Download cradle'
    case 'ingest': return 'External-content ingest'
    case 'fetch': return 'Network fetch'
    default: return 'Network flow'
  }
}

/**
 * The canonicality qualifier for a network flow, or '' when neither applies (a
 * clean fetch). This is the copy that makes a non-canonical ingest legible as
 * judge-clearable and a cradle legible as a control that cannot be waived.
 */
export function networkFlowQualifier(flow: string): string {
  if (flow === 'cradle') return 'canonical — cannot be waived'
  if (flow === 'ingest') return 'non-canonical — judge-clearable'
  return ''
}

/**
 * One-line summary of a network decision: the flow, the resolved egress
 * host(s), the affected operand(s), and — for a cradle/ingest — its
 * canonicality. e.g. "External-content ingest to example.com → report.pdf
 * (non-canonical — judge-clearable)".
 */
export function networkSummary(n: NetworkDecisionData): string {
  const host = n.hosts?.length ? ` to ${n.hosts.join(', ')}` : ''
  const operands = n.operands?.length ? ` → ${n.operands.join(', ')}` : ''
  const qualifier = networkFlowQualifier(n.flow)
  return `${networkFlowLabel(n.flow)}${host}${operands}${qualifier ? ` (${qualifier})` : ''}`
}

/**
 * The network summary for an autonomy decision, or null when the decision
 * carries no network flow (a non-network call or a step-limit gate). Shared by
 * the notice content and the card so a reloaded row reads identically.
 */
export function networkDecisionLine(data: AutonomyDecisionData): string | null {
  return data.network ? networkSummary(data.network) : null
}
