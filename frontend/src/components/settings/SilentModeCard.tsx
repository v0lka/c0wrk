import { useState } from 'react'
import { AlertTriangle, ShieldAlert } from 'lucide-react'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { UserConfirmDangerDialog } from './UserConfirmDangerDialog'
import {
  SILENT_SUB_POLICIES,
  modeRequiresJudge,
  silentModeLabel,
  type SilentSubPolicyKey,
  type SilentModeSubPolicyMeta,
} from '@/lib/silentMode'
import type { AutonomyMode, SilentModeSettings } from '@/types/models'

interface SilentModeCardProps {
  /**
   * The current autonomy posture (security.autonomy_mode). The card is
   * rendered only while it is "silent" — the mode owns liveness, the card
   * has no master switch of its own — but it receives the mode so its
   * header and tests stay honest about what makes the policies live.
   */
  mode: AutonomyMode
  value: SilentModeSettings
  /**
   * Whether the strict judge (a configured LLM model) is available. The
   * judge-dependent modes (tool_confirm: Judge, step_limit: Auto) fail
   * closed — every such decision is an automatic denial — while it is
   * missing. That is a warning here, NOT a block: the modes stay selectable
   * because the stored posture is the operator's decision (fail-closed is
   * the documented behavior, not a broken state).
   */
  judgeAvailable: boolean
  onModeChange: (key: SilentSubPolicyKey, mode: string) => void
}

/**
 * Silent mode (security.silent_mode): the four sub-policies that decide how
 * each interactive decision (tool confirmation — refined by user
 * confirmations, step-limit exhaustion, ask_user) resolves without a human.
 * The card has
 * no enable switch — the autonomy mode (security.autonomy_mode = "silent")
 * owns liveness, and SecuritySettings renders this card only in that mode —
 * and the autonomy warning is always visible so the risk is stated up front.
 *
 * Judge gating: while no LLM model is configured for the strict judge, a
 * warning names the selected judge-dependent modes and explains the
 * fail-closed behavior. It does not disable anything — picking such a mode
 * without a judge is a valid (documented) posture, and the stored posture
 * may pre-date the judge's removal, so the warning must appear for a
 * selected mode, not only block picking one.
 */
export function SilentModeCard({ mode, value, judgeAvailable, onModeChange }: SilentModeCardProps) {
  // The selected judge-dependent sub-policies while the judge is unavailable —
  // see the judge-gating note above.
  const judgeStarved: SilentModeSubPolicyMeta[] = !judgeAvailable
    ? SILENT_SUB_POLICIES.filter((sp) => modeRequiresJudge(sp.key, value[sp.key].mode))
    : []

  // The danger-gated "Confirm" user_confirm selection. The dropdown never saves
  // it — it opens the confirmation dialog instead, and only the dialog's
  // explicit confirm routes the save through onModeChange, where
  // SecuritySettings persists it. A dismissal is a cancel: the controlled
  // dropdown keeps rendering the stored value, which is the revert.
  const [pendingUserConfirm, setPendingUserConfirm] = useState(false)

  const handleModeChange = (key: SilentSubPolicyKey, next: string) => {
    if (key === 'user_confirm' && next === 'confirm') {
      setPendingUserConfirm(true)
      return
    }
    onModeChange(key, next)
  }

  return (
    <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50" data-testid="silent-mode-card">
      <div className="flex flex-col gap-0.5">
        <span className="flex items-center gap-1.5 text-sm font-medium">
          <ShieldAlert className="h-3.5 w-3.5" />
          Silent-mode policies
        </span>
        <span className="text-xs text-muted-foreground">
          {mode === 'silent'
            ? 'Live — the autonomy mode is Silent.'
            : 'Inert — these policies take effect only while the autonomy mode is Silent.'}
        </span>
      </div>

      <div
        role="note"
        data-testid="silent-mode-warning"
        className="flex items-start gap-2 text-xs text-warning"
      >
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
        <span>
          <strong>Autonomy warning.</strong> Silent mode lets the agent act without you: confirmation
          cards, step-limit halts, and questions are resolved automatically, and the post-task review
          page is not reopened. A task can then run to completion unattended — enable it only when you
          intend the agent to operate without supervision, and review the sub-policies below.
        </span>
      </div>

      <div className="flex flex-col gap-3" data-testid="silent-mode-subpolicies">
        {SILENT_SUB_POLICIES.map((sp) => (
          <div key={sp.key} className="flex items-start justify-between gap-3">
            <div className="flex flex-col gap-0.5 min-w-0">
              <span className="text-xs font-medium">{sp.label}</span>
              <span className="text-xs text-muted-foreground">{sp.description}</span>
            </div>
            <Combobox
              ariaLabel={`${sp.label} mode`}
              value={value[sp.key].mode}
              onChange={(v) => handleModeChange(sp.key, v)}
              className="h-8 w-auto px-2 text-xs min-w-[130px] shrink-0"
              options={sp.options as readonly ComboboxOption[]}
            />
          </div>
        ))}

        {value.user_confirm.mode === 'confirm' && (
          <div
            role="note"
            data-testid="silent-mode-user-confirm-warning"
            className="flex items-start gap-2 text-xs text-destructive"
          >
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
            <span>
              <strong>Unattended confirm is on.</strong> A confirmation-gated call whose strict-judge
              outcome is CONFIRM — including a missing, failing, timed-out, or unparseable judge —
              executes with no confirmation and is audited as an allow. A deliberate judge DENY is
              still blocked. Switch to Deny to restore the fail-closed auto-denial.
            </span>
          </div>
        )}

        {judgeStarved.length > 0 && (
          <div
            role="note"
            data-testid="silent-mode-judge-warning"
            className="flex items-start gap-2 text-xs text-warning"
          >
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
            <span>
              <strong>Judge unavailable.</strong> No LLM model is configured for the strict judge,
              and these selected modes rely on it:{' '}
              {judgeStarved.map((sp) => `${sp.label} (${silentModeLabel(sp.key, value[sp.key].mode)})`).join(', ')}.
              Until a provider is configured, every judge-dependent decision fails closed — gated
              tool calls are denied and the run stops at the first step limit. Enable at least one
              LLM provider in settings, or accept the fail-closed behavior by keeping these modes.
            </span>
          </div>
        )}
      </div>

      <UserConfirmDangerDialog
        open={pendingUserConfirm}
        onOpenChange={(open) => {
          // A dismissal is a cancel: drop the pending selection and leave the
          // controlled dropdown showing the stored value.
          if (!open) setPendingUserConfirm(false)
        }}
        onConfirm={() => {
          setPendingUserConfirm(false)
          onModeChange('user_confirm', 'confirm')
        }}
      />
    </div>
  )
}
