// Sidebar session status indicator — derives the green/yellow circle state
// for a session from the chat store, which is the single source of truth for
// live execution and pending-action state.
//
// The previous design mirrored `chatStore.taskActive` into
// `sessionStore.sessions[].active` (dual bookkeeping). That mirroring was
// incomplete — several taskActive transitions (task_resumed, background
// completion, runtime reconciliation) never updated `sessionStore.active` —
// so the green circle disappeared in those paths. Instead of patching every
// mirroring site, the indicator now reads `chatStore` directly.

import { useMemo } from 'react'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI, MessageType } from '@/types/messages'

/**
 * HITL message types that block execution while awaiting a user response.
 * A session with any unresolved message of one of these types shows a yellow
 * indicator instead of the green "task running" one.
 */
export const HITL_PROMPT_TYPES: ReadonlySet<MessageType> = new Set([
  'tool_confirm',
  'ask_user',
  'step_limit',
  'plan_review',
])

/** Sidebar indicator state for a session. */
export type SessionIndicatorStatus = 'pending' | 'active' | 'idle'

/**
 * Pure check: does the ordered message list contain an unresolved HITL prompt
 * (tool_confirm / ask_user / step_limit / plan_review)? Exported so the
 * detection logic is unit-testable without React rendering.
 */
export function hasUnresolvedHITL(messages: ChatMessageUI[]): boolean {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]!
    if (m.metadata?.resolved === true) continue
    if (HITL_PROMPT_TYPES.has(m.type)) return true
  }
  return false
}

/**
 * Pure derivation of the sidebar indicator from a session's running flag and
 * ordered messages. Exported for unit testing without React rendering.
 *
 * Pending takes precedence over active because a task blocked on a HITL
 * prompt is not "actively processing" — the user's response is the next
 * step, so the awaiting-reaction state is the more informative signal.
 */
export function deriveSessionIndicatorStatus(isRunning: boolean, messages: ChatMessageUI[]): SessionIndicatorStatus {
  if (hasUnresolvedHITL(messages)) return 'pending'
  return isRunning ? 'active' : 'idle'
}

/**
 * Derive the sidebar status indicator for a session from the chat store:
 *
 * - `'pending'` (yellow) — an unresolved HITL prompt is awaiting the user.
 * - `'active'`  (green)  — a task is currently running.
 * - `'idle'`             — neither.
 */
export function useSessionStatusIndicator(sessionId: string | null): SessionIndicatorStatus {
  const isRunning = useChatStore(s => (sessionId ? s.taskActive[sessionId] ?? false : false))
  const messageOrder = useChatStore(s => (sessionId ? s.messageOrder[sessionId] : undefined))
  const messageIndex = useChatStore(s => (sessionId ? s.messages[sessionId] : undefined))

  return useMemo(() => {
    const messages: ChatMessageUI[] = []
    if (messageOrder && messageIndex) {
      for (const id of messageOrder) {
        const m = messageIndex[id]
        if (m) messages.push(m)
      }
    }
    return deriveSessionIndicatorStatus(isRunning, messages)
  }, [messageOrder, messageIndex, isRunning])
}
