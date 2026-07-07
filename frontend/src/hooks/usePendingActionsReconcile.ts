// Reconcile pending HITL actions on session switch.
//
// When the user switches to a session, the chat history is loaded from the
// DB. HITL messages (tool_confirm, ask_user, step_limit, plan_review) in
// that history may be stale — the user may have already responded (the
// backend resolved the channel), or the event may have been missed while
// the session was in the background (no live listener caught it, but the
// agent goroutine is still blocked).
//
// This hook calls GetPendingActions(sessionId) after the history loads and
// reconciles the chat store:
//   1. For each pending item returned by the backend, ensure the matching
//      message exists in the store with resolved: false (re-show the panel).
//      If the message wasn't in the history (event was lost), add it.
//   2. For each HITL message in the store that is NOT in the pending list,
//      mark it resolved: true (the response was already given or the channel
//      was drained on shutdown — either way, the panel should not show).
//
// This runs independently of the history-loading effect in ChatArea so a
// stale history RPC cannot prevent reconciliation.

import { useEffect } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore, selectSessionMessages } from '@/stores/chatStore'
import { getPendingActions } from '@/api/chat'
import { useLatestAsync } from '@/hooks/useLatestAsync'
import { logger } from '@/lib/logger'

export function usePendingActionsReconcile(): void {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const { wrap } = useLatestAsync()

  useEffect(() => {
    if (!activeSessionId) return

    wrap(getPendingActions(activeSessionId)).then((pending) => {
      if (!pending) return // stale — superseded by a newer session switch

      const store = useChatStore.getState()
      const msgs = selectSessionMessages(store, activeSessionId)

      // Collect the set of request/confirm IDs that are still pending.
      const pendingConfirmIds = new Set(pending.tool_confirms.map(c => c.confirm_id))
      const pendingStepLimitIds = new Set(pending.step_limits.map(s => s.request_id))
      const pendingPlanReviewIds = new Set(pending.plan_approvals.map(p => p.request_id))
      const pendingAskUserIds = new Set(pending.ask_user.map(a => a.request_id))

      // Reconcile existing HITL messages: mark resolved if NOT in pending.
      for (const m of msgs) {
        if (m.type === 'tool_confirm') {
          const cid = m.metadata?.confirm_id as string | undefined
          if (cid && !pendingConfirmIds.has(cid) && m.metadata?.resolved !== true) {
            store.updateMessage(activeSessionId, m.id, { metadata: { ...m.metadata, resolved: true } })
          }
        } else if (m.type === 'step_limit') {
          const rid = m.metadata?.request_id as string | undefined
          if (rid && !pendingStepLimitIds.has(rid) && m.metadata?.resolved !== true) {
            store.updateMessage(activeSessionId, m.id, { metadata: { ...m.metadata, resolved: true } })
          }
        } else if (m.type === 'plan_review') {
          const rid = m.metadata?.request_id as string | undefined
          if (rid && !pendingPlanReviewIds.has(rid) && m.metadata?.resolved !== true) {
            store.updateMessage(activeSessionId, m.id, { metadata: { ...m.metadata, resolved: true } })
          }
        } else if (m.type === 'ask_user') {
          const rid = m.metadata?.request_id as string | undefined
          if (rid && !pendingAskUserIds.has(rid) && m.metadata?.resolved !== true) {
            store.updateMessage(activeSessionId, m.id, { metadata: { ...m.metadata, resolved: true } })
          }
        }
      }

      // Add messages for pending items that weren't in the store (event
      // was missed while the session was in the background).
      const existingIds = new Set(msgs.map(m => m.id))

      for (const c of pending.tool_confirms) {
        const id = `tool-confirm-${c.confirm_id}`
        if (existingIds.has(id)) continue
        store.addMessage(activeSessionId, {
          id,
          sessionId: activeSessionId,
          type: 'tool_confirm',
          content: `Confirm: ${c.tool}`,
          metadata: {
            confirm_id: c.confirm_id,
            tool: c.tool,
            args: c.args,
            reasoning: c.reasoning ?? '',
          } as Record<string, unknown>,
          timestamp: Date.now(),
        })
      }

      for (const s of pending.step_limits) {
        const id = `step-limit-${s.request_id}`
        if (existingIds.has(id)) continue
        store.addMessage(activeSessionId, {
          id,
          sessionId: activeSessionId,
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
        if (existingIds.has(id)) continue
        store.addMessage(activeSessionId, {
          id,
          sessionId: activeSessionId,
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
        if (existingIds.has(id)) continue
        store.addMessage(activeSessionId, {
          id,
          sessionId: activeSessionId,
          type: 'ask_user',
          content: a.questions.map(q => q.question).join('; '),
          metadata: {
            request_id: a.request_id,
            questions: a.questions,
          } as Record<string, unknown>,
          timestamp: Date.now(),
        })
      }
    }).catch((err) => {
      logger.error('Failed to reconcile pending actions:', err)
    })
  }, [activeSessionId, wrap])
}
