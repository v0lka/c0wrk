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
import { useSessionStore } from '@/stores/sessionStore'
import type { ChatMessageUI } from '@/types/messages'
import { hasUnresolvedHITL } from '@/lib/hitlTypes'

// Re-exported for existing importers; the implementation lives in
// lib/hitlTypes.ts so lib/activeSessions.ts can share it without a
// hooks → lib dependency inversion.
export { hasUnresolvedHITL }

/** Sidebar indicator state for a session. */
export type SessionIndicatorStatus = 'pending' | 'active' | 'paused' | 'idle'

/**
 * Pure derivation of the sidebar indicator from a session's running flag,
 * paused flag, and ordered messages. Exported for unit testing without React
 * rendering.
 *
 * Pending takes precedence because a task blocked on a HITL prompt is not
 * "actively processing" — the user's response is the next step, so the
 * awaiting-reaction state is the more informative signal. Paused takes
 * precedence over active because a cooperatively suspended task has
 * taskActive=false; the gray dot distinguishes it from a genuinely idle
 * session.
 */
export function deriveSessionIndicatorStatus(isRunning: boolean, isPaused: boolean, messages: ChatMessageUI[]): SessionIndicatorStatus {
  if (hasUnresolvedHITL(messages)) return 'pending'
  if (isPaused) return 'paused'
  return isRunning ? 'active' : 'idle'
}

/**
 * Derive the sidebar status indicator for a session from the chat store:
 *
 * - `'pending'` (yellow) — an unresolved HITL prompt is awaiting the user.
 * - `'paused'`  (gray)   — a cooperatively paused task (suspended at a checkpoint).
 * - `'active'`  (green)  — a task is currently running.
 * - `'idle'`             — neither.
 */
export function useSessionStatusIndicator(sessionId: string | null): SessionIndicatorStatus {
  const isRunning = useChatStore(s => (sessionId ? s.taskActive[sessionId] ?? false : false))
  const isPaused = useChatStore(s => (sessionId ? s.paused[sessionId] ?? false : false))
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
    return deriveSessionIndicatorStatus(isRunning, isPaused, messages)
  }, [messageOrder, messageIndex, isRunning, isPaused])
}

/**
 * Synchronous (non-hook) busy check for non-render code paths (e.g. session
 * action handlers). Mirrors the row's busy flag: a session is busy when it has
 * a running task, a paused task, or an unfinished task — destructive to
 * archive/delete, so those actions request confirmation. A 'pending' session
 * (awaiting a HITL response) is NOT busy.
 */
export function isSessionBusy(sessionId: string): boolean {
  const chat = useChatStore.getState()
  const isRunning = chat.taskActive[sessionId] ?? false
  const isPaused = chat.paused[sessionId] ?? false

  const messages: ChatMessageUI[] = []
  const messageOrder = chat.messageOrder[sessionId]
  const messageIndex = chat.messages[sessionId]
  if (messageOrder && messageIndex) {
    for (const id of messageOrder) {
      const m = messageIndex[id]
      if (m) messages.push(m)
    }
  }

  const status = deriveSessionIndicatorStatus(isRunning, isPaused, messages)
  if (status === 'active' || status === 'paused') return true

  const session = useSessionStore.getState().sessions?.find((s) => s.id === sessionId)
  return session?.has_unfinished_task ?? false
}
