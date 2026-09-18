/**
 * Presentation metadata for the autonomy modes (security.autonomy_mode). The
 * enum VALUES are owned by the backend (config.AutonomyMode*); this module
 * only carries the human labels and the per-mode descriptions — which state
 * the TERMINAL difference honestly: in each mode, which gated decisions end
 * at the human terminal (a card) and which terminate automatically (execute
 * or deny with no human). Settings → Security renders them under the
 * segmented control, so SecuritySettings stays a thin view (see
 * lib/silentMode.ts for the silent-mode sub-policy metadata).
 */
import { isAutonomyMode } from '@/types/guards'
import type { AutonomyMode } from '@/types/models'

export interface AutonomyModeMeta {
  value: AutonomyMode
  label: string
  description: string
}

export const AUTONOMY_MODES: readonly AutonomyModeMeta[] = [
  {
    value: 'standard',
    label: 'Standard',
    description:
      'Every gated decision stops at your terminal: confirmation cards, step-limit halts, and ask_user questions all wait for a human. Nothing executes — and nothing is denied — without you.',
  },
  {
    value: 'assisted',
    label: 'Assisted',
    description:
      'The strict OWASP ASI judge (ASI01–ASI10) auto-evaluates escalated tool confirmations. Terminal difference: a strict allow executes with no card; a strict deny terminates the call outright — no card, no second chance (audited as an autonomy decision); CONFIRM verdicts, judge failures, and canonical hard reasons still open a card. Requires a configured LLM provider — without one, every judged call degrades to the standard card.',
  },
  {
    value: 'silent',
    label: 'Silent',
    description:
      'Unattended operation: no card can ever open. Tool confirmations, step-limit halts, ask_user, and the post-task review prompt are resolved automatically per the sub-policies below, and a task can run to completion — or terminal denial — without supervision. Judge-dependent sub-policies fail closed (automatic denial) while no judge is configured.',
  },
]

/**
 * Coerce a backend (or stored) autonomy-mode value into the enum, failing
 * safe to "standard" — mirroring the backend's own "empty or unrecognized
 * behaves as standard" rule, so a drifting value can never latch the UI (or
 * the app-wide store) into an autonomous posture the config loader would
 * reject.
 */
export function normalizeAutonomyMode(v: unknown): AutonomyMode {
  return isAutonomyMode(v) ? v : 'standard'
}

/** Presentation metadata for one autonomy mode (throws on an unknown value). */
export function autonomyModeMeta(mode: AutonomyMode): AutonomyModeMeta {
  const meta = AUTONOMY_MODES.find((m) => m.value === mode)
  if (!meta) throw new Error(`autonomyModeMeta: unknown autonomy mode "${mode}"`)
  return meta
}
