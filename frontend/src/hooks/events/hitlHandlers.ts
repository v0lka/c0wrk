// Shared HITL event handlers — used by both the active-session event hooks
// (useActionEvents, useToolEvents) and the background-session watcher
// (useBackgroundSessionWatcher) so that pending-action messages reach the
// chat store regardless of which session the user is currently viewing.

import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useGoalStore } from '@/stores/goalStore'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { getPendingActions } from '@/api/chat'
import { logger } from '@/lib/logger'
import { generateMessageId } from '@/lib/ids'
import { autonomyDecisionContent } from '@/lib/autonomyDecision'
import {
  toolConfirmResolved,
  stepLimitResolved,
  getToolConfirmResolution,
  getStepLimitResolution,
  type StepLimitDecision,
} from '@/types/messages'
import type {
  ToolConfirmData,
  AskUserData,
  StepLimitData,
  PlanReviewReadyData,
  ToolJudgePhaseData,
  AutonomyDecisionData,
  HitlResolvedData,
  HitlResolvedKind,
} from '@/types/events'

/**
 * Live proof of a running task: the arrival of a HITL prompt event. A prompt
 * can only be emitted by a task goroutine that is alive and blocked on the
 * user's answer (the pending map's entry exists exactly while its waiter
 * waits — see reconcilePendingActions), so a `taskActive` flag that a stale
 * runtime-status snapshot reverted to false (the snapshot's read raced the
 * backend's activation of a just-sent task) is healed here, mirroring the
 * `step_start` re-assertion in useLifecycleEvents. The write is
 * one-directional (TRUE only, stamped in `taskFlagsEventAt` so later
 * snapshots cannot fight it back): pause/terminal events own the false
 * direction — and they can only fire after the waiter is gone, i.e. after
 * this handler's moment. This matters most for the waits with NO further
 * events: a task blocked at the goal-suggestion gate (or on ask_user /
 * plan_review) never emits a step_start, so nothing else would re-open the
 * live turn's work block that the stale flip settled to `interrupted`.
 */
export function assertTaskActiveFromPrompt(sessionId: string): void {
  const store = useChatStore.getState()
  if (store.taskActive[sessionId] !== true) {
    store.setTaskActive(sessionId, true)
  }
}

/** Handle a tool_confirm event for a session (active or background). */
export function handleToolConfirmEvent(sessionId: string, data: ToolConfirmData): void {
  const store = useChatStore.getState()
  const msgs = selectSessionMessages(store, sessionId)

  let toolMsgId: string | undefined
  let toolPlanStepId: string | undefined

  // Link the confirmation to the tool_call that triggered it. Prefer a precise
  // tool_call_id match (the backend attaches it when resolvable); fall back to
  // the most recent tool_call with a matching tool name, which is ambiguous
  // when two calls share a name.
  const linkToToolCall = (predicate: (meta: Record<string, unknown>) => boolean): boolean => {
    for (let i = msgs.length - 1; i >= 0; i--) {
      const m = msgs[i]!
      if (m.type === 'tool_call' && m.metadata && predicate(m.metadata)) {
        toolMsgId = m.id
        toolPlanStepId = m.metadata.plan_step_id as string | undefined
        store.updateMessage(sessionId, m.id, {
          metadata: { ...m.metadata, awaiting_confirmation: true },
        })
        return true
      }
    }
    return false
  }
  if (!data.tool_call_id || !linkToToolCall(meta => meta.tool_call_id === data.tool_call_id)) {
    linkToToolCall(meta => meta.tool === data.tool)
  }

  store.addMessage(sessionId, {
    id: `tool-confirm-${data.confirm_id}`,
    sessionId,
    type: 'tool_confirm',
    content: `Confirm: ${data.tool}`,
    metadata: {
      confirm_id: data.confirm_id,
      tool: data.tool,
      args: data.args,
      reasoning: data.reasoning,
      tool_call_id: data.tool_call_id,
      disable_judge: data.disable_judge,
      tool_msg_id: toolMsgId,
      plan_step_id: toolPlanStepId,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  assertTaskActiveFromPrompt(sessionId)
  store.setActivityStatus(sessionId, 'Awaiting confirmation...')
}

/** Handle an ask_user event for a session (active or background). */
export function handleAskUserEvent(sessionId: string, data: AskUserData): void {
  useChatStore.getState().addMessage(sessionId, {
    id: `ask-user-${data.request_id}`,
    sessionId,
    type: 'ask_user',
    content: data.questions.map(q => q.question).join('; '),
    metadata: {
      request_id: data.request_id,
      questions: data.questions,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  assertTaskActiveFromPrompt(sessionId)
  useChatStore.getState().setActivityStatus(sessionId, 'Waiting for your answer...')
}

/** Handle a step_limit event for a session (active or background). */
export function handleStepLimitEvent(sessionId: string, data: StepLimitData): void {
  useChatStore.getState().addMessage(sessionId, {
    id: `step-limit-${data.request_id}`,
    sessionId,
    type: 'step_limit',
    content: data.reason
      ? `Circuit breaker: ${data.reason}`
      : `Step limit reached: ${data.current_step} of ${data.max_steps}`,
    metadata: {
      request_id: data.request_id,
      current_step: data.current_step,
      max_steps: data.max_steps,
      reason: data.reason,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  assertTaskActiveFromPrompt(sessionId)
  useChatStore.getState().setActivityStatus(
    sessionId,
    data.reason
      ? 'Circuit breaker triggered — awaiting decision...'
      : 'Step limit reached — awaiting decision...',
  )
}

/** Handle a plan_review_ready event for a session (active or background). */
export function handlePlanReviewEvent(sessionId: string, data: PlanReviewReadyData): void {
  useChatStore.getState().addMessage(sessionId, {
    id: `plan-review-${data.request_id}`,
    sessionId,
    type: 'plan_review',
    content: data.plan_content,
    metadata: {
      request_id: data.request_id,
      plan_path: data.plan_path,
      // Mirror the persisted metadata shape (the Go persister writes the full
      // payload, including plan_content, as metadata) so live and reloaded
      // rows carry the same fields.
      plan_content: data.plan_content,
      resolved: false,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  assertTaskActiveFromPrompt(sessionId)
  useChatStore.getState().setActivityStatus(sessionId, 'Plan is ready for review...')
}

/**
 * Handle an autonomy_decision event for a session (active or background). An
 * automatic (no-human) security decision taken under an automatic autonomy
 * posture (assisted or silent) is surfaced as a NON-BLOCKING standard-format
 * `autonomy_decision` card (`AutonomyDecisionBlock`) in the transcript: there
 * is no prompt to answer, but the gate that was answered without a human must
 * stay visible and auditable (OWASP ASI10: the trajectory must be
 * reconstructable). The payload rides in metadata so a reloaded row renders
 * identically via reconstructContent.
 */
export function handleAutonomyDecisionEvent(sessionId: string, data: AutonomyDecisionData): void {
  useChatStore.getState().addMessage(sessionId, {
    id: `autonomy-decision-${generateMessageId()}`,
    sessionId,
    type: 'autonomy_decision',
    content: autonomyDecisionContent(data),
    metadata: { ...data } as Record<string, unknown>,
    timestamp: Date.now(),
  })
}

/**
 * Handle a tool_judge_started event (active session). The strict judge (Smart
 * Approve) evaluates an escalated call BEFORE any confirmation card exists,
 * so the honest activity label says a judge is working — not that the session
 * waits for the user.
 */
export function handleToolJudgeStartedEvent(sessionId: string, _data: ToolJudgePhaseData): void {
  useChatStore.getState().setActivityStatus(sessionId, 'Safety judge evaluating...')
}

/**
 * Handle a tool_judge_finished event (active session). The payload carries no
 * verdict, so the frontend cannot tell ALLOW (tool now executing) from CONFIRM
 * (card about to land) — don't guess. Clear the judge label and let the next
 * factual event set the accurate one: tool_confirm → "Awaiting
 * confirmation...", or the following step/assistant event after a strict-ALLOW
 * execution.
 */
export function handleToolJudgeFinishedEvent(sessionId: string, _data: ToolJudgePhaseData): void {
  useChatStore.getState().setActivityStatus(sessionId, null)
}

/**
 * Handle a hitl_resolved event (active or background session): the backend
 * broadcast that a pending HITL prompt was consumed — usually by ANOTHER
 * surface (a second browser tab, the webview while the user works in a
 * browser). Retire the local card immediately instead of waiting for the next
 * session-switch / focus-resync reconcile, and converge on the EFFECTIVE
 * decision (the first responder's answer) so a surface that raced the
 * resolution shows what actually drove the agent.
 *
 * Idempotent by design: a surface that performed the resolution already
 * marked its card resolved locally and skips the redundant write; a missing
 * card (never rendered, or already reconciled) is a no-op FOR THE CHAT — the
 * goal-store mirror is still settled above, because it lives independently of
 * the message list (progress panel / status badge), and the sticky
 * pending-HITL override bit is reconciled below for the same reason.
 */
/**
 * Wire vocabularies for hitl_resolved decisions, mirroring the Go emitters in
 * desktop/event_handlers.go (`confirmDecisionString`, `parseStepLimitResponse`).
 * A recognized decision maps to the UI resolution vocabulary; an unrecognized
 * non-empty value settles the card NEUTRALLY instead of being coerced to
 * "denied" or cast unchecked (CS #25 — the guard validates only "some string").
 */
const TOOL_CONFIRM_WIRE_DECISIONS: ReadonlySet<string> = new Set(['allow_once', 'deny', 'deny_and_stop'])
const STEP_LIMIT_WIRE_DECISIONS: ReadonlySet<string> = new Set(['allow_once', 'allow_more', 'allow_always', 'deny'])

/** Map a tool_confirm wire decision to the UI vocabulary; null = no recorded decision. */
function toolConfirmWireDecision(decision: string | undefined): 'confirmed' | 'denied' | null {
  if (decision === undefined || decision === '') return null
  if (decision === 'allow_once') return 'confirmed'
  if (TOOL_CONFIRM_WIRE_DECISIONS.has(decision)) return 'denied'
  return null
}

/** Validate a step_limit wire decision against the enum; null = no recorded decision. */
function stepLimitWireDecision(decision: string | undefined): StepLimitDecision | null {
  if (decision === undefined || decision === '') return null
  return STEP_LIMIT_WIRE_DECISIONS.has(decision) ? (decision as StepLimitDecision) : null
}

export function handleHitlResolvedEvent(sessionId: string, data: HitlResolvedData): void {
  // The goal store mirrors the pending prompt for the progress panel / badge:
  // clear the entry ONLY when it is still this exact prompt, so a newer
  // proposal (arrived between the answer and this event) is never wiped. Runs
  // BEFORE the chat-card lookup: the mirror exists even when this surface
  // never rendered the card.
  if (data.kind === 'goal_proposal' || data.kind === 'goal_suggestion') {
    const goal = useGoalStore.getState()
    if (data.kind === 'goal_proposal') {
      if (goal.pendingProposal[sessionId]?.request_id === data.id) goal.clearPendingProposal(sessionId)
    } else if (goal.pendingSuggestion[sessionId]?.request_id === data.id) {
      goal.clearPendingSuggestion(sessionId)
    }
  }

  // Pending-HITL override reconcile: a sticky `true` bit in
  // activeSessionsStore means this surface believes the session still blocks
  // on a prompt, yet a resolution broadcast just landed — the bit's only
  // `false` writer (this surface answering the prompt itself) is unreachable
  // here, so without this the bit would stick forever (yellow badge with no
  // actual prompt). Re-query the backend and apply the AUTHORITATIVE answer —
  // NOT a blind clear: a second prompt may already be pending. Runs BEFORE
  // the chat-card early return: the reconcile must also work when this
  // surface never rendered the card. Fire-and-forget keeps the handler sync;
  // a null result (RPC failure — getPendingActions never rejects) is not
  // authoritative and keeps the bit.
  if (useActiveSessionsStore.getState().pendingOverride[sessionId] === true) {
    void getPendingActions(sessionId).then(pending => {
      if (pending === null) {
        logger.debug('hitl_resolved: pending-actions re-query failed, keeping override for', sessionId)
        return
      }
      useActiveSessionsStore.getState().applyPendingActions(sessionId, pending)
    })
  }

  const store = useChatStore.getState()
  const idPrefix: Record<HitlResolvedKind, string> = {
    tool_confirm: 'tool-confirm-',
    ask_user: 'ask-user-',
    step_limit: 'step-limit-',
    plan_review: 'plan-review-',
    goal_proposal: 'goal-proposal-',
    goal_suggestion: 'goal-suggestion-',
  }
  const msg = selectSessionMessages(store, sessionId).find(m => m.id === idPrefix[data.kind] + data.id)
  if (!msg) return
  const alreadyResolved = msg.metadata?.resolved === true

  if (msg.type === 'tool_confirm') {
    // Converge on the effective decision: allow_once → confirmed,
    // deny/deny_and_stop → denied, anything else (empty or unrecognized wire
    // value) → resolved without a recorded decision (the neutral settled
    // card). Validation goes through the per-kind wire vocabulary — an
    // unknown non-empty decision must not render as "denied" (CS #25).
    const decision = toolConfirmWireDecision(data.decision)
    const tool = typeof msg.metadata?.tool === 'string' ? msg.metadata.tool : undefined
    if (alreadyResolved) {
      // The card was settled locally — by THIS surface's own answer, or by an
      // earlier broadcast. Overwrite ONLY when this event carries the
      // effective decision AND it differs from the local record: a surface
      // that raced the resolution (its click lost first-responder-wins) must
      // converge on what actually drove the agent. An event without a
      // decision never touches a settled card.
      if (decision === null) return
      const current = getToolConfirmResolution(msg.metadata)
      if (current === decision) return
      store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, ...toolConfirmResolved(decision) } })
      store.setActivityStatus(sessionId, decision === 'confirmed' && tool ? `Running tool: ${tool}...` : null)
      return
    }
    const resolution: Record<string, unknown> =
      decision !== null ? toolConfirmResolved(decision) : { resolved: true }
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, ...resolution } })
    // Release the linked tool card's awaiting flag (mirrors the local
    // ToolConfirmation.handleResponse path).
    const toolMsgId = typeof msg.metadata?.tool_msg_id === 'string' ? msg.metadata.tool_msg_id : undefined
    if (toolMsgId) {
      const toolMsg = selectSessionMessages(store, sessionId).find(m => m.id === toolMsgId)
      if (toolMsg && toolMsg.metadata?.awaiting_confirmation === true) {
        store.updateMessage(sessionId, toolMsg.id, {
          metadata: { ...toolMsg.metadata, awaiting_confirmation: false },
        })
      }
    }
    store.setActivityStatus(sessionId, decision === 'confirmed' && tool ? `Running tool: ${tool}...` : null)
    return
  }

  if (msg.type === 'step_limit') {
    // Validated against the wire enum; an unrecognized non-empty value settles
    // neutrally instead of an unchecked cast into StepLimitDecision (CS #25).
    const decision = stepLimitWireDecision(data.decision)
    if (alreadyResolved) {
      // Same convergence rule as tool_confirm above.
      if (decision === null) return
      const current = getStepLimitResolution(msg.metadata)
      if (current === decision) return
      store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, ...stepLimitResolved(decision) } })
      store.setActivityStatus(sessionId, null)
      return
    }
    const resolution: Record<string, unknown> =
      decision !== null ? stepLimitResolved(decision) : { resolved: true }
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, ...resolution } })
    store.setActivityStatus(sessionId, null)
    return
  }

  // ask_user / plan_review / goal_proposal / goal_suggestion: the answers and
  // feedback are owned by the answering surface — every other surface only
  // needs the settlement.
  if (!alreadyResolved) {
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true } })
    store.setActivityStatus(sessionId, null)
  }
}

/**
 * Backstop for the hitl_resolved broadcast: when a tool_result arrives whose
 * `tool_call_id` matches an UNRESOLVED tool_confirm card, settle the card
 * neutrally. In the normal flow the backend's hitl_resolved event lands FIRST
 * (the gate resolves before the tool runs) and already settled the card with
 * the effective decision — this only fires when that push was missed (a
 * dropped websocket with no reconnect trigger yet), trading the decision
 * display for not leaving a ghost card that blocks the pending-actions bar
 * while the tool it gated has demonstrably already run. Never overrides an
 * existing resolution.
 */
export function resolveConfirmForToolResult(sessionId: string, toolCallId: string | undefined): void {
  if (!toolCallId) return
  const store = useChatStore.getState()
  const card = selectSessionMessages(store, sessionId).find(
    m => m.type === 'tool_confirm' && m.metadata?.resolved !== true && m.metadata?.tool_call_id === toolCallId,
  )
  if (!card) return
  store.updateMessage(sessionId, card.id, { metadata: { ...card.metadata, resolved: true } })
  const toolMsgId = typeof card.metadata?.tool_msg_id === 'string' ? card.metadata.tool_msg_id : undefined
  if (toolMsgId) {
    const toolMsg = selectSessionMessages(store, sessionId).find(m => m.id === toolMsgId)
    if (toolMsg?.metadata?.awaiting_confirmation === true) {
      store.updateMessage(sessionId, toolMsg.id, {
        metadata: { ...toolMsg.metadata, awaiting_confirmation: false },
      })
    }
  }
  store.setActivityStatus(sessionId, null)
}
