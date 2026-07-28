import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Check, X, Target, AlertTriangle, HelpCircle, Terminal, RefreshCw, CheckCircle2 } from 'lucide-react'
import { goal } from '@/api'
import type { DisplayItem } from '@/types/messages'
import { isResolved } from '@/types/messages'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useGoalStore, useActiveGoal } from '@/stores/goalStore'
import type { ActiveGoal } from '@/stores/goalStore'
import { FileLink } from '@/components/chat/toolCards/shared/FileLink'
import type { GoalEvidence } from '@/types/events'
import { logger } from '@/lib/logger'

interface GoalProposalPanelProps {
  item: Extract<DisplayItem, { kind: 'goal_proposal' }>
}

type Decision = 'approve' | 'cancel' | 'clarify'

// --- Settled-goal verdict rendering ---
//
// Once a goal reaches a verdict (met / not_met / partial …), the approved
// proposal card surfaces the verdict badge, its reason, and the backing
// evidence so a verdict is never a bare assertion. File-typed evidence renders
// as a clickable FileLink; other evidence types show their ref inline. When the
// independent verifier confirmed the goal, the verifier's structured outcome
// (reason + evidence) is preferred over the agent's own, since it is the
// authoritative confirmation.

const EVIDENCE_TYPE_FILE = 'file'

/** Map a verdict string to a Tailwind color class for the badge. */
function verdictBadgeClass(verdict: string): string {
  switch (verdict) {
    case 'met': return 'text-success border-success/40 bg-success/10'
    case 'not_met': return 'text-destructive border-destructive/40 bg-destructive/10'
    case 'partial': return 'text-warning border-warning/40 bg-warning/10'
    default: return 'text-muted-foreground border-border bg-background/50'
  }
}

/** A single evidence entry: clickable file link for type=file, inline ref
 *  otherwise, with the human-readable summary appended. */
function GoalEvidenceItem({ evidence }: { evidence: GoalEvidence }) {
  return (
    <span className="flex items-start gap-1">
      {evidence.type === EVIDENCE_TYPE_FILE ? (
        <FileLink path={evidence.ref} />
      ) : (
        <code className="font-mono text-xs text-info break-all">{evidence.ref}</code>
      )}
      {evidence.summary ? <span className="text-muted-foreground/70">— {evidence.summary}</span> : null}
    </span>
  )
}

/** Renders the verdict badge, reason, and evidence list for a settled goal.
 *  Pure presentational helper fed by the session's ActiveGoal snapshot. */
export function GoalVerdictBody({ goal }: { goal: ActiveGoal }) {
  const verdict = goal.verdict
  if (!verdict) return null
  const verified = goal.verification === 'confirmed'
  // Prefer the independent verifier's outcome when it confirmed the goal;
  // otherwise fall back to the agent's own verdict reason/evidence.
  // NB: reason uses || (not ??) because the backend's nil-verifier seam
  // emits an empty string (not null) for verification_reason; || falls back to
  // the agent's reason, while evidence keeps ?? since an unset slice marshals
  // to null and ?? handles it correctly.
  const reason = verified ? (goal.verificationReason || goal.reason) : goal.reason
  const evidence = verified ? (goal.verificationEvidence ?? goal.evidence) : goal.evidence
  return (
    <div className="mt-1.5 space-y-1.5">
      <span
        className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide ${verdictBadgeClass(verdict)}`}
      >
        {verdict}
        {verified ? <CheckCircle2 className="h-3 w-3" aria-label="verified" /> : null}
      </span>
      {reason ? <p className="text-xs text-muted-foreground whitespace-pre-wrap">{reason}</p> : null}
      {evidence && evidence.length > 0 ? (
        <ul className="space-y-0.5">
          {evidence.map((ev, i) => (
            <li key={`${ev.type}-${ev.ref}-${i}`} className="text-xs">
              <GoalEvidenceItem evidence={ev} />
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

// --- Per-goal verification mode ---
//
// Mirrors core/goal VerificationMode constants. The derivation agent chooses a
// mode at propose_goal time and the user may override it at sign-off. The
// canonical values round-trip through the backend (GoalProposalPayload /
// ConfirmGoal) as plain strings.

/** Verify by executing the verify clause as a test/command (the default). */
const VERIFICATION_MODE_EXECUTABLE = 'executable'
/** Verify by re-deriving the goal from conversation state and comparing. */
const VERIFICATION_MODE_RE_DERIVATION = 're_derivation'
type VerificationMode = typeof VERIFICATION_MODE_EXECUTABLE | typeof VERIFICATION_MODE_RE_DERIVATION

/** Map a (possibly empty/absent) verification-mode string to a valid value,
 *  defaulting unknown/empty to 'executable' — matching goal.NormalizeVerificationMode
 *  so the panel never shows an invalid mode and never sends one. */
function normalizeVerificationMode(mode: string | undefined): VerificationMode {
  return mode === VERIFICATION_MODE_RE_DERIVATION
    ? VERIFICATION_MODE_RE_DERIVATION
    : VERIFICATION_MODE_EXECUTABLE
}

/**
 * Renders a pending goal proposal as an editable approval panel: two
 * pre-filled, EDITABLE textareas (the success condition + how it is verified)
 * plus Approve / Cancel. Approve sends the (possibly edited) values back via
 * goal.confirmGoal and optimistically marks the proposal resolved; Cancel calls
 * goal.cancelGoal and exits the goal loop.
 *
 * In needs_clarification mode the clarifying question is shown prominently and
 * the verify field starts empty and focused (the user is being asked to refine
 * how the goal should be verified, not to rubber-stamp a proposal).
 *
 * Mirrors PlanApprovalPanel's resolved-state rendering and optimistic-update
 * pattern so a confirmed/cancelled proposal collapses to a small settled card.
 */
export function GoalProposalPanel({ item }: GoalProposalPanelProps) {
  const sessionId = useSessionStore((s) => s.activeSessionId)
  const requestId = item.message.metadata?.request_id as string | undefined
  const decision = item.message.metadata?.decision as Decision | undefined
  const error = item.message.metadata?.error as string | undefined

  // In needs_clarification mode the verify field is intentionally empty so the
  // user types how the goal should be verified; otherwise it is pre-filled
  // with the proposed verify text (editable).
  const [condition, setCondition] = useState(item.condition)
  const [verify, setVerify] = useState(item.needs_clarification ? '' : item.verify)
  // The verification mode is pre-filled from the derivation-chosen value and is
  // user-editable at sign-off (only the approve path sends it). Normalized so an
  // empty/unknown mode defaults to 'executable'.
  const [verificationMode, setVerificationMode] = useState<VerificationMode>(
    () => normalizeVerificationMode(item.verification_mode),
  )
  const [submitting, setSubmitting] = useState(false)
  const verifyRef = useRef<HTMLTextAreaElement>(null)

  // The committed goal snapshot — read so the approved (settled) card can
  // surface the verdict badge + reason + evidence as the goal progresses and
  // settles. `useActiveGoal` returns a direct store reference (stable across
  // renders unless the entry is replaced — AGENTS.md §2.7), so this does not
  // allocate inside the selector.
  const activeGoal = useActiveGoal(sessionId)

  // needs_clarification: focus the verify textarea on mount so the user can
  // immediately refine the verification criteria.
  useEffect(() => {
    if (item.needs_clarification && !isResolved(item.message.metadata)) {
      verifyRef.current?.focus()
    }
  }, [item.needs_clarification, item.message.metadata])

  // --- Resolved (settled) states ---

  if (decision === 'approve') {
    // When the goal has reached a verdict, surface the verdict badge + reason
    // + evidence (file evidence clickable); otherwise keep the plain
    // "Goal approved" placeholder.
    const hasVerdict = !!activeGoal?.verdict
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Goal</span>
        </div>
        {hasVerdict && activeGoal ? (
          <GoalVerdictBody goal={activeGoal} />
        ) : (
          <p className="mt-1.5 text-xs text-muted-foreground">Goal approved</p>
        )}
      </div>
    )
  }
  if (decision === 'cancel') {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>Goal</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Goal cancelled</p>
      </div>
    )
  }
  if (decision === 'clarify') {
    return (
      <div className="rounded-md border border-warning/30 bg-warning/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <HelpCircle className="h-3.5 w-3.5 shrink-0 text-warning" />
          <span>Goal</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Clarification sent</p>
      </div>
    )
  }
  // Resolved without a recorded decision — stale prompt reconciled on reload.
  if (isResolved(item.message.metadata)) {
    return (
      <div className="rounded-md border border-border bg-background/50 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Target className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span>Goal</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Dismissed</p>
      </div>
    )
  }

  if (!requestId || !sessionId) return null

  const markResolved = (d: Decision, extra?: Record<string, unknown>) => {
    useChatStore.getState().updateMessage(sessionId, item.message.id, {
      metadata: { ...item.message.metadata, resolved: true, decision: d, ...(extra ?? {}) },
    })
    // Keep the goal store in sync so the active-goal panel doesn't keep a
    // stale pending proposal after the user has responded.
    useGoalStore.getState().clearPendingProposal(sessionId)
  }

  const onApprove = async (): Promise<void> => {
    setSubmitting(true)
    try {
      await goal.confirmGoal(sessionId, requestId, condition, verify, verificationMode)
      markResolved('approve', { condition, verify, verification_mode: verificationMode })
      // Seed the approved goal into the goal store so the user's (possibly
      // edited) condition/verify/mode are retained before the first goal_status
      // snapshot arrives. handleGoalStatusEvent preserves activeGoal.verify and
      // activeGoal.verificationMode across status snapshots (the status event
      // does not always echo them back), but that preservation only works if
      // they are seeded here.
      useGoalStore.getState().setActiveGoal(sessionId, {
        condition,
        verify: verify || undefined,
        status: 'active',
        turn: 0,
        verificationMode,
      })
    } catch (err) {
      logger.error('Failed to confirm goal proposal:', err)
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, error: 'Failed to approve goal' },
      })
    } finally {
      setSubmitting(false)
    }
  }

  const onClarify = async (): Promise<void> => {
    setSubmitting(true)
    try {
      // In needs_clarification mode the verify field is the user's refinement
      // of how the goal should be verified; send it as the clarification so
      // the agent revises and re-proposes.
      await goal.clarifyGoal(sessionId, requestId, verify)
      markResolved('clarify', { verify })
    } catch (err) {
      logger.error('Failed to send goal clarification:', err)
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, error: 'Failed to send clarification' },
      })
    } finally {
      setSubmitting(false)
    }
  }

  const onCancel = async (): Promise<void> => {
    setSubmitting(true)
    try {
      await goal.cancelGoal(sessionId, requestId)
      markResolved('cancel')
    } catch (err) {
      logger.error('Failed to cancel goal proposal:', err)
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, error: 'Failed to cancel goal' },
      })
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <Target className="h-3.5 w-3.5 shrink-0 text-info" />
        <span>Proposed Goal</span>
        {item.needs_clarification && (
          <span className="ml-auto flex items-center gap-1 text-warning">
            <HelpCircle className="h-3 w-3" /> Needs clarification
          </span>
        )}
      </div>

      <div className="mt-1.5 space-y-1.5">
        {/* needs_clarification: surface the clarifying question prominently. */}
        {item.needs_clarification && item.clarification && (
          <div className="rounded-md border border-warning/40 bg-warning/10 p-2">
            <div className="flex items-start gap-1.5 text-xs text-warning">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              <span className="whitespace-pre-wrap">{item.clarification}</span>
            </div>
          </div>
        )}

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 p-2">
            <p className="text-xs text-destructive">{error}</p>
          </div>
        )}

        <label className="block text-xs text-muted-foreground/80" htmlFor={`goal-cond-${item.message.id}`}>
          Success condition
        </label>
        <textarea
          id={`goal-cond-${item.message.id}`}
          className="w-full rounded-md border border-border bg-background p-2 text-xs resize-none custom-scrollbar"
          rows={3}
          value={condition}
          onChange={(e) => setCondition(e.target.value)}
          placeholder="Describe what success looks like..."
        />

        <label className="block text-xs text-muted-foreground/80" htmlFor={`goal-verify-${item.message.id}`}>
          How is it verified?
        </label>
        <textarea
          id={`goal-verify-${item.message.id}`}
          ref={verifyRef}
          className="w-full rounded-md border border-border bg-background p-2 text-xs resize-none custom-scrollbar"
          rows={2}
          value={verify}
          onChange={(e) => setVerify(e.target.value)}
          placeholder={item.needs_clarification ? 'How should this goal be verified?' : 'How to verify the goal is met...'}
        />

        {/* Verification mode: shows HOW the goal will be verified. Editable via a
            compact segmented toggle so the user can override the derivation-chosen
            mode at sign-off; the chosen value is sent through goal.confirmGoal. */}
        <span className="block text-xs text-muted-foreground/80">Verification mode</span>
        <div
          className="flex gap-1 rounded-md border border-border bg-background p-0.5"
          role="radiogroup"
          aria-label="Verification mode"
        >
          <button
            type="button"
            role="radio"
            aria-checked={verificationMode === VERIFICATION_MODE_EXECUTABLE}
            aria-label="Executable check"
            title="Run the verify clause as a command/test (default)"
            className={`flex flex-1 items-center justify-center gap-1 rounded px-2 py-1 text-xs transition-colors ${
              verificationMode === VERIFICATION_MODE_EXECUTABLE
                ? 'bg-info/15 text-info'
                : 'text-muted-foreground hover:text-foreground'
            }`}
            onClick={() => setVerificationMode(VERIFICATION_MODE_EXECUTABLE)}
          >
            <Terminal className="h-3 w-3 shrink-0" /> Executable check
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={verificationMode === VERIFICATION_MODE_RE_DERIVATION}
            aria-label="Re-run verification (re-derivation)"
            title="Re-derive the goal from conversation state and compare"
            className={`flex flex-1 items-center justify-center gap-1 rounded px-2 py-1 text-xs transition-colors ${
              verificationMode === VERIFICATION_MODE_RE_DERIVATION
                ? 'bg-info/15 text-info'
                : 'text-muted-foreground hover:text-foreground'
            }`}
            onClick={() => setVerificationMode(VERIFICATION_MODE_RE_DERIVATION)}
          >
            <RefreshCw className="h-3 w-3 shrink-0" /> Re-run verification
          </button>
        </div>

        <div className="flex gap-2 pt-0.5">
          {item.needs_clarification ? (
            <Button variant="default" size="sm" onClick={onClarify} disabled={submitting || verify.trim() === ''}>
              <HelpCircle className="size-3.5 mr-1" /> Send
            </Button>
          ) : (
            <Button variant="default" size="sm" onClick={onApprove} disabled={submitting || condition.trim() === ''}>
              <Check className="size-3.5 mr-1" /> Approve
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={submitting}>
            <X className="size-3.5 mr-1" /> Cancel
          </Button>
        </div>
      </div>
    </div>
  )
}
