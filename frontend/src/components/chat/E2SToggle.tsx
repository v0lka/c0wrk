import { Braces } from 'lucide-react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useExperimentalFeatures } from '@/hooks/useExperimentalFeatures'
import { cn } from '@/lib/utils'

/**
 * E2SToggle enables/disables the explicit-execution-state mode for the next
 * sent message. It mirrors GoalToggle's compact icon-button treatment: muted
 * when off, primary-highlighted when on.
 *
 * The toggle is gated behind the experimental master switch alone
 * (useExperimentalFeatures, config `experimental.enabled`): E2S is one of the
 * features behind that all-or-nothing gate, so flipping the switch reveals the
 * button — there is no separate `e2s.enabled` toggle. It renders nothing while
 * the switch is off, so the mode stays invisible in default installs and can
 * never be armed to a backend-rejected send.
 *
 * This is the VISIBILITY gate. The authoritative SEND gate — the armed toggle
 * combined with the same availability rule — is defined once in `lib/e2sGate`
 * (`isE2SSendEnabled`) and consumed by the send path.
 *
 * Like GoalToggle, the button is LOCKED while any live task state lingers
 * (running, pausing, compacting, cooperatively paused, or a failed task
 * awaiting resume/cancel — see lib/chatInputLock computeModeTogglesLocked): an
 * E2S-armed send in those states abandons the unfinished task
 * (abandonUnfinishedTaskForE2S), so the mode must not be flippable until the
 * session returns to a clean state — the task settled (continuation), the
 * failed task was cancelled (fresh start), or no task at all. `lockReason`
 * carries the task-state lock's title (empty string when the reason does not
 * apply); the static default covers the plain session-running lock.
 *
 * The toggle state (`e2sEnabled`) lives in inputModeStore and IS persisted
 * (an experimental workflow preference), unlike `goalEnabled`. The two modes
 * are mutually exclusive — enabling either disables the other in the store.
 * Selectors return direct store refs/primitives only — no allocations
 * (React 19 useSyncExternalStore #185 guard, AGENTS.md §2.7).
 */
export function E2SToggle({ disabled = false, lockReason = '' }: { disabled?: boolean; lockReason?: string }) {
  const experimentalEnabled = useExperimentalFeatures()
  const e2sEnabled = useInputModeStore((s) => s.e2sEnabled)
  const setE2sEnabled = useInputModeStore((s) => s.setE2sEnabled)

  if (!experimentalEnabled) return null

  return (
    <button
      type="button"
      onClick={() => setE2sEnabled(!e2sEnabled)}
      disabled={disabled}
      className={cn(
        'flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50 disabled:cursor-not-allowed',
        e2sEnabled && 'text-primary border-primary/50 bg-primary/10 hover:bg-primary/15',
      )}
      title={disabled ? (lockReason || 'Locked while the session is running') : e2sEnabled ? 'E2S mode on — click to turn off' : 'E2S mode off — click to turn on'}
      aria-pressed={e2sEnabled}
      aria-label="Toggle E2S mode"
    >
      <Braces className="size-3.5" />
      <span>E2S</span>
    </button>
  )
}
