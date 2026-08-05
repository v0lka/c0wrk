/**
 * Reconciliation of the visual session state with the backend runtime status.
 *
 * After history load the UI would otherwise default to "idle/completed" —
 * even for a session whose task is still running or died mid-way (app crash,
 * backend panic). This module aligns the chat store with the authoritative
 * backend answers from GetSessionRuntimeStatus / GetPendingActions.
 *
 * IMPORTANT: these functions only mutate the in-memory Zustand store. They are
 * invoked by ChatArea AFTER history is merged (see loadSessionHistory), so the
 * store is populated when they run — this is what fixes the prior race where a
 * fast status/pending RPC resolved before the (slower) history RPC and thus
 * reconciled an empty store.
 *
 * Each function returns the messages it resolved as stale so the caller can
 * persist that resolution to the backend (otherwise the prompts reappear on
 * the next reload).
 */
import type { PendingActionsResponse, SessionRuntimeStatus } from '@/api/chat'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { useGoalStore } from '@/stores/goalStore'
import type { ChatMessageUI, MessageType } from '@/types/messages'
import { HITL_PROMPT_TYPES } from '@/lib/hitlTypes'

/** Message content for the synthetic resume banner injected on reload. */
const UNFINISHED_TASK_MESSAGE =
  'A previous task did not finish. You can resume it or discard it.'

/**
 * Maps a stale prompt type to the metadata field that identifies the specific
 * persisted message, so its resolution can be persisted via ResolvePendingMessage.
 * Returns null for types that cannot be persisted by id (e.g. synthetic banners).
 */
export function stalePromptMatchField(type: MessageType): string | null {
  switch (type) {
    case 'tool_confirm': return 'confirm_id'
    case 'ask_user':
    case 'step_limit':
    case 'plan_review':
    case 'goal_proposal': return 'request_id'
    case 'task_failed_resumable': return 'task_id'
    default: return null
  }
}

/**
 * Align chat-store state for `sessionId` with the backend runtime status.
 *
 * - `active` → restore the "task running" flag (input disabled, no false idle).
 * - not active + unfinished task persisted → ensure an unresolved
 *   `task_failed_resumable` banner exists (Resume/Cancel affordance) and
 *   resolve stale interactive prompts (step_limit, plan_review, tool_confirm,
 *   ask_user) whose owning executor is gone.
 * - not active + no unfinished task → resolve all stale prompts left over
 *   from previous runs.
 *
 * Returns the messages that were resolved as stale (so the caller can persist
 * the resolution to the backend). Messages already resolved are skipped.
 */
export function reconcileRuntimeStatus(sessionId: string, status: SessionRuntimeStatus): ChatMessageUI[] {
  const store = useChatStore.getState()
  store.setTaskActive(sessionId, status.active)

  if (status.active) {
    // A live task owns its pending prompts (step_limit, ask_user, ...);
    // leave them untouched.
    return []
  }

  const order = store.messageOrder[sessionId] ?? []
  const index = store.messages[sessionId] ?? {}
  const unresolved: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (!msg) continue
    if (msg.metadata?.resolved === true) continue
    if (HITL_PROMPT_TYPES.has(msg.type) || msg.type === 'task_failed_resumable') {
      unresolved.push(msg)
    }
  }

  if (status.has_unfinished_task) {
    // Stale interactive prompts cannot be answered after a restart — the
    // executor waiting for the response no longer exists.
    const resolvedStale: ChatMessageUI[] = []
    for (const msg of unresolved) {
      if (HITL_PROMPT_TYPES.has(msg.type)) {
        store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
        resolvedStale.push(msg)
      }
    }
    const hasResumable = unresolved.some(m => m.type === 'task_failed_resumable')
    if (!hasResumable) {
      store.addMessage(sessionId, {
        id: `resume-runtime-${Date.now()}`,
        sessionId,
        type: 'task_failed_resumable',
        content: UNFINISHED_TASK_MESSAGE,
        metadata: { resolved: false },
        timestamp: Date.now(),
      })
    }
    return resolvedStale
  }

  // No unfinished task: any surviving prompts are stale.
  const resolvedStale: ChatMessageUI[] = []
  for (const msg of unresolved) {
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
    resolvedStale.push(msg)
  }
  return resolvedStale
}

/**
 * Reconcile persisted HITL messages against the live pending-action set.
 *
 * For each HITL message in the store whose id is NOT reported as pending by
 * the backend, mark it resolved (the response was already given or the
 * channel was drained on shutdown). For each pending item NOT yet in the
 * store, add it (its event was missed while the session was in the
 * background).
 *
 * Returns the messages that were resolved as stale (so the caller can persist
 * the resolution to the backend).
 */
export function reconcilePendingActions(sessionId: string, pending: PendingActionsResponse): ChatMessageUI[] {
  const store = useChatStore.getState()
  const msgs = selectSessionMessages(store, sessionId)

  // Collect the set of request/confirm IDs that are still pending.
  const pendingConfirmIds = new Set(pending.tool_confirms.map(c => c.confirm_id))
  const pendingStepLimitIds = new Set(pending.step_limits.map(s => s.request_id))
  const pendingPlanReviewIds = new Set(pending.plan_approvals.map(p => p.request_id))
  const pendingAskUserIds = new Set(pending.ask_user.map(a => a.request_id))

  const resolvedStale: ChatMessageUI[] = []

  // Reconcile existing HITL messages: mark resolved if NOT in pending.
  for (const m of msgs) {
    if (m.metadata?.resolved === true) continue
    if (m.type === 'tool_confirm') {
      const cid = m.metadata?.confirm_id as string | undefined
      if (cid && !pendingConfirmIds.has(cid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'step_limit') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingStepLimitIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'plan_review') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingPlanReviewIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    } else if (m.type === 'ask_user') {
      const rid = m.metadata?.request_id as string | undefined
      if (rid && !pendingAskUserIds.has(rid)) {
        store.updateMessage(sessionId, m.id, { metadata: { ...m.metadata, resolved: true, stale: true } })
        resolvedStale.push(m)
      }
    }
  }

  // Add messages for pending items that weren't in the store (event
  // was missed while the session was in the background).
  const existingIDs = new Set(msgs.map(m => m.id))

  for (const c of pending.tool_confirms) {
    const id = `tool-confirm-${c.confirm_id}`
    if (existingIDs.has(id)) continue
    // Link the confirmation to the tool_call that triggered it (mirroring the
    // live-event path in hitlHandlers) so that, once resolved, the decision
    // card anchors directly beneath the tool card rather than at the bottom.
    // Prefer a precise tool_call_id match; fall back to tool-name matching.
    let toolMsgId: string | undefined
    let toolPlanStepId: string | undefined
    const findTool = (predicate: (meta: Record<string, unknown>) => boolean): void => {
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]!
        if (m.type === 'tool_call' && m.metadata && predicate(m.metadata)) {
          toolMsgId = m.id
          toolPlanStepId = m.metadata.plan_step_id as string | undefined
          return
        }
      }
    }
    if (c.tool_call_id) findTool(meta => meta.tool_call_id === c.tool_call_id)
    if (!toolMsgId) findTool(meta => meta.tool === c.tool)

    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'tool_confirm',
      content: `Confirm: ${c.tool}`,
      metadata: {
        confirm_id: c.confirm_id,
        tool: c.tool,
        args: c.args,
        reasoning: c.reasoning ?? '',
        tool_call_id: c.tool_call_id,
        tool_msg_id: toolMsgId,
        plan_step_id: toolPlanStepId,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const s of pending.step_limits) {
    const id = `step-limit-${s.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'step_limit',
      content: s.reason
        ? `Circuit breaker: ${s.reason}`
        : `Step limit reached: ${s.current_step} of ${s.max_steps}`,
      metadata: {
        request_id: s.request_id,
        current_step: s.current_step,
        max_steps: s.max_steps,
        reason: s.reason ?? '',
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const p of pending.plan_approvals) {
    const id = `plan-review-${p.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'plan_review',
      content: p.plan_content,
      metadata: {
        request_id: p.request_id,
        plan_path: p.plan_path,
        resolved: false,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  for (const a of pending.ask_user) {
    const id = `ask-user-${a.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'ask_user',
      content: a.questions.map(q => q.question).join('; '),
      metadata: {
        request_id: a.request_id,
        questions: a.questions,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
  }

  // Goal proposals. NOTE: goal_proposal is intentionally NOT added to the
  // resolve-stale branch above: the backend's GetPendingActions does not yet
  // report goal_proposals, so the pending set is always empty here, and
  // treating "not in pending" as stale would falsely resolve every persisted
  // proposal. The genuine stale case (restart with no running task) is handled
  // by reconcileRuntimeStatus via HITL_PROMPT_TYPES. This add-missing branch is
  // forward-compatible — it stays a no-op until the backend populates the
  // goal_proposals field. Persisted proposals reappear via history reload
  // (role goal_proposal) regardless.
  for (const g of pending.goal_proposals) {
    const id = `goal-proposal-${g.request_id}`
    if (existingIDs.has(id)) continue
    store.addMessage(sessionId, {
      id,
      sessionId,
      type: 'goal_proposal',
      content: g.condition,
      metadata: {
        request_id: g.request_id,
        condition: g.condition,
        verify: g.verify,
        verification_mode: g.verification_mode ?? '',
        resolved: false,
      } as Record<string, unknown>,
      timestamp: Date.now(),
    })
    // Keep the goal store in sync for a background-delivered proposal that
    // surfaces here on session switch.
    useGoalStore.getState().setPendingProposal(sessionId, {
      request_id: g.request_id,
      session_id: sessionId,
      condition: g.condition,
      verify: g.verify,
      verification_mode: g.verification_mode,
    })
  }

  return resolvedStale
}
