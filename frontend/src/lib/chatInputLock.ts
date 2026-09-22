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

/**
 * Per-message EXECUTION-MODE lock (goal / E2S toggles and the goal budget
 * selector) — deliberately NARROWER than the model/reasoning selector lock:
 * model and reasoning unlock on a cooperative pause or a resumable failure
 * (the resume honors a freshly picked override), while goal/E2S do not.
 *
 * Why the asymmetry: goal and E2S are not resume-time overrides — they can
 * only arm a NEW task. A send that arrives while an unfinished task lingers
 * (paused, failed-resumable, or orphaned in_progress) is intercepted by the
 * backend's resume branch, and a goal- or E2S-armed send actively ABANDONS
 * the unfinished task (abandonUnfinishedTaskForGoal / abandonUnfinishedTaskForE2S:
 * the interrupted run is cancelled so the new mode starts clean). Flipping the
 * mode in those states therefore discards the suspended task — so the toggles
 * stay locked until the session returns to a clean state:
 *   - the task settled successfully (a follow-up send is a continuation on the
 *     inherited blackboard — the one place re-arming is meaningful), or
 *   - the failed task was cancelled (the next send starts fresh), or
 *   - the session had no task at all (fresh first message).
 */
export interface ModeToggleLockInput {
  /** A task is currently executing in the session. */
  taskActive: boolean
  /** Pause requested, checkpoint not yet landed (the pausing window). */
  pausing: boolean
  /** A manual context compaction is in flight. */
  compacting: boolean
  /** The task is cooperatively paused (checkpoint landed). */
  paused: boolean
  /** chatStore.unfinishedTaskStatus for the active session: '' = settled/none,
   *  otherwise a live unfinished task lingers ('paused' | 'failed' |
   *  'in_progress'). Any non-empty value keeps the toggles locked — every such
   *  state can still be continued or abandoned by the next send. */
  unfinishedTaskStatus: string
}

/** Whether the goal/E2S toggles (and the goal budget selector) are locked. */
export function computeModeTogglesLocked(s: ModeToggleLockInput): boolean {
  return s.taskActive || s.pausing || s.compacting || s.paused || s.unfinishedTaskStatus !== ''
}

/**
 * Why the mode toggles are locked, for the disabled control's title. Empty
 * string when unlocked. Precedence mirrors computeChatPlaceholder: the most
 * specific transient flow first, then the lingering-task states.
 */
export function modeToggleLockReason(s: ModeToggleLockInput): string {
  if (!computeModeTogglesLocked(s)) return ''
  if (s.compacting) return 'Locked while context compaction is in flight'
  if (s.paused) return 'Locked while the task is paused — resume or cancel it first'
  if (s.unfinishedTaskStatus === 'failed') return 'Locked while a failed task awaits resume or cancel'
  if (s.unfinishedTaskStatus !== '') return 'Locked while an unfinished task is pending'
  return 'Locked while the session is running'
}
