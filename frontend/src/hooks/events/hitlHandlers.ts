// Shared HITL event handlers — used by both the active-session event hooks
// (useActionEvents, useToolEvents) and the background-session watcher
// (useBackgroundSessionWatcher) so that pending-action messages reach the
// chat store regardless of which session the user is currently viewing.

import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import type {
  ToolConfirmData,
  AskUserData,
  StepLimitData,
  PlanReviewReadyData,
} from '@/types/events'

/** Handle a tool_confirm event for a session (active or background). */
export function handleToolConfirmEvent(sessionId: string, data: ToolConfirmData): void {
  const store = useChatStore.getState()
  const msgs = selectSessionMessages(store, sessionId)

  let toolMsgId: string | undefined
  let toolPlanStepId: string | undefined
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i]!
    if (m.type === 'tool_call' && m.metadata?.tool === data.tool) {
      toolMsgId = m.id
      toolPlanStepId = m.metadata?.plan_step_id as string | undefined
      store.updateMessage(sessionId, m.id, {
        metadata: { ...m.metadata, awaiting_confirmation: true },
      })
      break
    }
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
      tool_msg_id: toolMsgId,
      plan_step_id: toolPlanStepId,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  store.setActivityStatus('Awaiting confirmation...')
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
  useChatStore.getState().setActivityStatus('Waiting for your answer...')
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
      resolved: false,
    } as Record<string, unknown>,
    timestamp: Date.now(),
  })
  useChatStore.getState().setActivityStatus('Plan is ready for review...')
}
