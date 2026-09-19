// Shared HITL event handlers — used by both the active-session event hooks
// (useActionEvents, useToolEvents) and the background-session watcher
// (useBackgroundSessionWatcher) so that pending-action messages reach the
// chat store regardless of which session the user is currently viewing.

import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'
import { autonomyDecisionContent } from '@/lib/autonomyDecision'
import type {
  ToolConfirmData,
  AskUserData,
  StepLimitData,
  PlanReviewReadyData,
  ToolJudgePhaseData,
  AutonomyDecisionData,
} from '@/types/events'

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
