// Context events: context_fill, context_compaction, session_tokens

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isContextFillData, isContextCompactionData, isSessionTokensData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import { generateMessageId } from '@/lib/ids'

export function useContextEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- context_fill ---
    cleanups.push(
      onSessionEvent(sessionId, 'context_fill', (data) => {
        if (!isContextFillData(data)) { reportDroppedEvent('context_fill', data); return }
        const store = useChatStore.getState()
        if (data.plan_step_id) {
          store.setStepContextFill(data.plan_step_id, data.fill_percent)
        }
        store.setSessionTokens(sessionId, {
          total_input_tokens: data.session_input_tokens ?? 0,
          total_output_tokens: data.session_output_tokens ?? 0,
          model: data.model,
          family: data.family,
        })
      }),
    )

    // --- context_compaction ---
    cleanups.push(
      onSessionEvent(sessionId, 'context_compaction', (data) => {
        if (!isContextCompactionData(data)) { reportDroppedEvent('context_compaction', data); return }
        useChatStore.getState().addMessage(sessionId, {
          id: generateMessageId(),
          sessionId,
          type: 'context_compaction',
          content: `Context compacted from ${Math.round(data.before_percent)}% to ${Math.round(data.after_percent)}%`,
          metadata: { before_percent: data.before_percent, after_percent: data.after_percent, plan_step_id: data.plan_step_id },
          timestamp: Date.now(),
        })
      }),
    )

    // --- session_tokens ---
    // Carries the conductor's context-window fill_percent (guarded server-side by
    // the isSessionRoot emitter, so subagent emissions never leak their own fill).
    // context_fill events deliberately omit fill_percent here; with merge
    // semantics they update only token totals, preserving the session-level fill.
    cleanups.push(
      onSessionEvent(sessionId, 'session_tokens', (data) => {
        if (!isSessionTokensData(data)) { reportDroppedEvent('session_tokens', data); return }
        useChatStore.getState().setSessionTokens(sessionId, {
          total_input_tokens: data.session_input_tokens,
          total_output_tokens: data.session_output_tokens,
          model: data.model,
          family: data.family,
          // Only forward fill_percent when actually present. The type guard
          // (isSessionTokensData) does not require this field, so coercing an
          // absent value to 0 would overwrite a previously-valid fill and make
          // ContextFillStatus show a false "0%". Omitting it lets the store's
          // merge semantics preserve the last known session-level fill.
          ...(typeof data.fill_percent === 'number' ? { fill_percent: data.fill_percent } : {}),
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
