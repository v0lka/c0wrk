import type { ChatMessageUI, MessageType } from '@/types/messages'

/**
 * The four HITL (human-in-the-loop) prompt message types — interactive prompts
 * that block a session's agent goroutine while awaiting a user response:
 * tool_confirm, ask_user, step_limit, plan_review.
 *
 * Single source of truth shared by every consumer of "is this message a HITL
 * prompt?":
 *   - chatStore.mergeHistoryMessages — preserves an unresolved live card across
 *     a history reload (the combined emit delivers the event to the UI before
 *     persisting it, so a live card can transiently be absent from the loaded
 *     snapshot);
 *   - useSessionStatusIndicator — a session with any unresolved message of one
 *     of these types shows the yellow "pending" dot instead of the green
 *     "running" one;
 *   - sessionRuntime reconciliation — after a restart these prompts can never
 *     be answered (the executor awaiting the response is gone), so they are
 *     resolved as stale.
 *
 * Defined here — a module with no store dependency — so all three consumers
 * can import it without an import cycle (each of them is imported by, or
 * imports, the chat store).
 *
 * Note: `task_failed_resumable` is intentionally excluded — it is surfaced as a
 * Resume/Cancel banner rather than treated as a dismissible interactive prompt.
 */
export const HITL_PROMPT_TYPES: ReadonlySet<MessageType> = new Set([
  'tool_confirm',
  'ask_user',
  'step_limit',
  'plan_review',
  'goal_proposal',
])

/**
 * Pure check: does the ordered message list contain an unresolved HITL prompt
 * (tool_confirm / ask_user / step_limit / plan_review / goal_proposal)?
 * Lives here — a module with no store or React dependency — so every consumer
 * (useSessionStatusIndicator, lib/activeSessions) shares ONE implementation.
 */
export function hasUnresolvedHITL(messages: readonly ChatMessageUI[]): boolean {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]!
    if (m.metadata?.resolved === true) continue
    if (HITL_PROMPT_TYPES.has(m.type)) return true
  }
  return false
}
