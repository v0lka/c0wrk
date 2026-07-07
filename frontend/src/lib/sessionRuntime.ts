/**
 * Reconciliation of the visual session state with the backend runtime status.
 *
 * After history load the UI would otherwise default to "idle/completed" —
 * even for a session whose task is still running or died mid-way (app crash,
 * backend panic). This module aligns the chat store with the authoritative
 * backend answer from GetSessionRuntimeStatus.
 */
import type { SessionRuntimeStatus } from '@/api/chat'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI, MessageType } from '@/types/messages'

/** Message content for the synthetic resume banner injected on reload. */
const UNFINISHED_TASK_MESSAGE =
  'A previous task did not finish. You can resume it or discard it.'

/**
 * Interactive prompt types that block execution while awaiting a user
 * response. After an app restart (or backend crash) the goroutine that was
 * waiting for the response no longer exists, so these prompts can never be
 * answered — they must be resolved as stale during reconciliation.
 *
 * Note: `task_failed_resumable` is handled separately because it is replaced
 * by a Resume/Cancel banner rather than silently dismissed.
 */
const STALE_PROMPT_TYPES: ReadonlySet<MessageType> = new Set([
  'step_limit',
  'plan_review',
  'tool_confirm',
  'ask_user',
])

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
 */
export function reconcileRuntimeStatus(sessionId: string, status: SessionRuntimeStatus): void {
  const store = useChatStore.getState()
  store.setTaskActive(sessionId, status.active)

  if (status.active) {
    // A live task owns its pending prompts (step_limit, ask_user, ...);
    // leave them untouched.
    return
  }

  const order = store.messageOrder[sessionId] ?? []
  const index = store.messages[sessionId] ?? {}
  const unresolved: ChatMessageUI[] = []
  for (const id of order) {
    const msg = index[id]
    if (!msg) continue
    if (msg.metadata?.resolved === true) continue
    if (STALE_PROMPT_TYPES.has(msg.type) || msg.type === 'task_failed_resumable') {
      unresolved.push(msg)
    }
  }

  if (status.has_unfinished_task) {
    // Stale interactive prompts cannot be answered after a restart — the
    // executor waiting for the response no longer exists.
    for (const msg of unresolved) {
      if (STALE_PROMPT_TYPES.has(msg.type)) {
        store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
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
    return
  }

  // No unfinished task: any surviving prompts are stale.
  for (const msg of unresolved) {
    store.updateMessage(sessionId, msg.id, { metadata: { ...msg.metadata, resolved: true, stale: true } })
  }
}
