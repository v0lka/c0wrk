/**
 * Chat input lock policy (live-send).
 *
 * Pure helpers extracted from useChatInputController so the input-lock matrix
 * is unit-testable without the CodeMirror-backed hook harness.
 */

export interface ChatInputLockInput {
  /** A task is currently executing in the session. */
  taskActive: boolean
  /** The task is cooperatively paused (checkpoint landed). */
  paused: boolean
  /** Pause requested, checkpoint not yet landed (the pausing window). */
  pausing: boolean
  /** No project is active (CHAT-less state). */
  isNoProject: boolean
  /** A manual context compaction is in flight (the whole input area locks). */
  compacting: boolean
}

/**
 * Whether the chat input is disabled.
 *
 * - `taskActive` alone does NOT lock the input: a message sent while a task
 *   runs is live-delivered to the running request's next LLM call.
 * - The pausing window DOES lock it: a send in that window races the
 *   pause→paused transition, so the backend rejects it (ErrPausePending) and
 *   the UI mirrors that by disabling input.
 * - A cooperatively paused task leaves the input unlocked (nudge-resume).
 * - Manual compaction locks the input: the compaction flow owns the session
 *   (pause-wait → history swap → auto-resume), and the backend rejects sends
 *   with ErrSessionCompacting for the whole window.
 * - No project locks the input outright.
 */
export function computeChatInputDisabled(s: ChatInputLockInput): boolean {
  return s.compacting || s.pausing || s.isNoProject
}

/** The input placeholder for the current session state (first match wins). */
export function computeChatPlaceholder(s: ChatInputLockInput): string {
  if (s.isNoProject) return 'Select or create a project to start'
  if (s.compacting) return 'Compacting context — the input unlocks when it finishes'
  if (s.pausing) return 'Pausing — the input unlocks once the pause lands'
  if (s.paused) return 'Paused — send a message to nudge-resume, or press Resume'
  if (s.taskActive) return 'Working — your message joins the next request to the model'
  return 'Type a message... (Enter to send, Shift+Enter for new line)'
}
