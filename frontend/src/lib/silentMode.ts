/**
 * Presentation metadata for silent mode (security.silent_mode). The enum VALUES
 * are owned by the backend (config.Silent*); this module only carries the human
 * labels, descriptions, and option order for the three sub-policy dropdowns, so
 * SecuritySettings stays a thin view.
 */

/** The three silent-mode sub-policies, keyed like SilentModeSettings. */
export type SilentSubPolicyKey = 'tool_confirm' | 'step_limit' | 'ask_user'

export interface SilentModeSubPolicyMeta {
  key: SilentSubPolicyKey
  label: string
  description: string
  options: { value: string; label: string }[]
  /**
   * Option values that route the decision through the strict judge. While no
   * LLM model is configured for the judge these modes resolve fail-closed
   * (every such decision is an automatic denial), so the settings UI disables
   * them in the dropdowns and warns while one is selected.
   */
  requiresJudge?: readonly string[]
}

export const SILENT_SUB_POLICIES: readonly SilentModeSubPolicyMeta[] = [
  {
    key: 'tool_confirm',
    label: 'Tool confirmations',
    description:
      'A tool call that would open a confirmation card. Judge routes it through the strict OWASP ASI judge (a strict allow runs — except a canonical hard reason, which is always denied — anything else denies); Allow runs a confirmation-gated call with no hard reason (a hard reason still escalates to the judge); Deny blocks them all.',
    options: [
      { value: 'judge', label: 'Judge' },
      { value: 'allow', label: 'Allow' },
      { value: 'deny', label: 'Deny' },
    ],
    requiresJudge: ['judge'],
  },
  {
    key: 'step_limit',
    label: 'Step limit',
    description:
      'A step-budget / circuit-breaker boundary. Auto lets the strict judge decide (it may grant more steps or stop); the fixed modes pin one response; Stop keeps the interactive card (opts this gate out of silent mode).',
    options: [
      { value: 'auto', label: 'Auto (judge decides)' },
      { value: 'allow_once', label: 'Allow once' },
      { value: 'allow_more', label: 'Allow more' },
      { value: 'allow_always', label: 'Allow always' },
      { value: 'deny', label: 'Deny (stop)' },
      { value: 'stop', label: 'Stop (prompt)' },
    ],
    requiresJudge: ['auto'],
  },
  {
    key: 'ask_user',
    label: 'Ask user',
    description:
      'The ask_user tool. Disable registers it in a form that reports itself as unavailable, so the agent can never block on a question; Enable keeps it fully available.',
    options: [
      { value: 'disable', label: 'Disable' },
      { value: 'enable', label: 'Enable' },
    ],
  },
]

/** Human-readable label for a sub-policy mode, falling back to the raw value. */
export function silentModeLabel(key: SilentSubPolicyKey, mode: string): string {
  const meta = SILENT_SUB_POLICIES.find((m) => m.key === key)
  return meta?.options.find((o) => o.value === mode)?.label ?? mode
}

/**
 * Whether a sub-policy mode routes its decision through the strict judge
 * (see SilentModeSubPolicyMeta.requiresJudge) — the modes that fail closed
 * into automatic denials while no judge is configured.
 */
export function modeRequiresJudge(key: SilentSubPolicyKey, mode: string): boolean {
  return SILENT_SUB_POLICIES.find((m) => m.key === key)?.requiresJudge?.includes(mode) ?? false
}
