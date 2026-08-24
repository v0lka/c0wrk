// Context events: context_fill, context_compaction, session_tokens

import { useEffect } from 'react'
import { onSessionEvent, reportDroppedEvent } from '@/api/runtime'
import { isContextFillData, isContextCompactionData, isSessionTokensData } from '@/types/events'
import type { ContextFillData } from '@/types/events'
import { useChatStore } from '@/stores/chatStore'
import type { TokenInfo } from '@/types/models'
import { generateMessageId } from '@/lib/ids'

/** Minimal store surface handleContextFill needs — the chatStore subset. */
export interface ContextFillStore {
  setStepContextFill: (sessionId: string, stepId: string, fill: number) => void
  setSessionTokens: (sessionId: string, tokens: Partial<TokenInfo>) => void
}

/**
 * Applies a context_fill event to the store.
 *
 * Two shapes arrive on this channel:
 * - Step-scoped copies (plan_step_id set — subagent/executor steps): update
 *   only the step fill and the token totals. A subagent's own fill must not
 *   clobber the conductor's session-level fill the status bar renders.
 * - Session-root events (no plan_step_id — conductor emissions AND the
 *   SetDisplayContextWindowForModel re-broadcast that corrects the window
 *   after a lazy local-model probe lands): also refresh the session-level
 *   fill_percent/used_tokens/max_tokens. Without this merge, a window
 *   correction arriving after the last LLM call (idle status bar) would
 *   never reach the UI — the next session_tokens event only comes with the
 *   next LLM call.
 *
 * The optional-spread guards mirror the session_tokens handler: the type
 * guard (isContextFillData) does not verify these fields, so coercing an
 * absent value to 0 would overwrite a previously-valid fill.
 */
export function handleContextFill(store: ContextFillStore, sessionId: string, data: ContextFillData): void {
  // Guard every optional-typed field the same way the session_tokens handler
  // guards fill_percent/used_tokens/max_tokens: isContextFillData only verifies
  // fill_percent/status, so the token totals (and model/family) may be absent at
  // runtime. Coercing an absent token total to 0 (or setting model/family to
  // undefined) would overwrite a previously-valid value on the store's shallow
  // merge.
  const totals: Partial<TokenInfo> = {
    ...(typeof data.session_input_tokens === 'number' ? { total_input_tokens: data.session_input_tokens } : {}),
    ...(typeof data.session_output_tokens === 'number' ? { total_output_tokens: data.session_output_tokens } : {}),
    ...(typeof data.model === 'string' ? { model: data.model } : {}),
    ...(typeof data.family === 'string' ? { family: data.family } : {}),
  }
  if (data.plan_step_id) {
    store.setStepContextFill(sessionId, data.plan_step_id, data.fill_percent)
    store.setSessionTokens(sessionId, totals)
    return
  }
  store.setSessionTokens(sessionId, {
    ...totals,
    ...(typeof data.fill_percent === 'number' ? { fill_percent: data.fill_percent } : {}),
    ...(typeof data.used_tokens === 'number' ? { used_tokens: data.used_tokens } : {}),
    ...(typeof data.max_tokens === 'number' ? { max_tokens: data.max_tokens } : {}),
  })
}

export function useContextEvents(sessionId: string | null): void {
  useEffect(() => {
    if (!sessionId) return

    const cleanups: Array<() => void> = []

    // --- context_fill ---
    cleanups.push(
      onSessionEvent(sessionId, 'context_fill', (data) => {
        if (!isContextFillData(data)) { reportDroppedEvent('context_fill', data); return }
        handleContextFill(useChatStore.getState(), sessionId, data)
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
    // Session-root context_fill events (see handleContextFill) update the same
    // cached fill; step-scoped ones update only token totals, preserving the
    // session-level fill. used_tokens/max_tokens follow the same
    // session-root-only cache path as fill_percent, so the status bar can
    // render a "N of M" tooltip.
    cleanups.push(
      onSessionEvent(sessionId, 'session_tokens', (data) => {
        if (!isSessionTokensData(data)) { reportDroppedEvent('session_tokens', data); return }
        useChatStore.getState().setSessionTokens(sessionId, {
          total_input_tokens: data.session_input_tokens,
          total_output_tokens: data.session_output_tokens,
          model: data.model,
          family: data.family,
          // Only forward fill_percent / used_tokens / max_tokens when actually
          // present. The type guard (isSessionTokensData) does not require these
          // fields, so coercing an absent value to 0 would overwrite a previously-
          // valid fill and make ContextFillStatus show a false "0%". Omitting them
          // lets the store's merge semantics preserve the last known session-level
          // values.
          ...(typeof data.fill_percent === 'number' ? { fill_percent: data.fill_percent } : {}),
          ...(typeof data.used_tokens === 'number' ? { used_tokens: data.used_tokens } : {}),
          ...(typeof data.max_tokens === 'number' ? { max_tokens: data.max_tokens } : {}),
        })
      }),
    )

    return () => cleanups.forEach(fn => fn())
  }, [sessionId])
}
