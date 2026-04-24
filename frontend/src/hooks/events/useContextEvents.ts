// Context events: context_fill, context_compaction, session_tokens

import { useEffect } from 'react'
import { onSessionEvent } from '@/api/runtime'
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
        if (!isContextFillData(data)) return
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
        if (!isContextCompactionData(data)) return
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
    cleanups.push(
      onSessionEvent(sessionId, 'session_tokens', (data) => {
        if (!isSessionTokensData(data)) return
        useChatStore.getState().setSessionTokens(sessionId, {
          total_input_tokens: data.session_input_tokens,
          total_output_tokens: data.session_output_tokens,
          model: data.model,
          family: data.family,
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
