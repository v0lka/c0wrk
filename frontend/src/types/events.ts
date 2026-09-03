import type { ProjectInfo, VectorIndexStatus, PasteKind } from '@/types/models'
import { isObj, has, isArrayOf } from '@/types/guards'

// Re-export isObj/has from guards for backward compatibility
export { isObj, has }

// Typed event system — session-scoped and global event payloads and maps

// --- Session event payload interfaces ---

export interface RoutingData { domain: string; complexity: string; mode: string }

export interface ToolCallData {
  tool_call_id?: string; step: number; tool: string; args: string
  parsed_args?: Record<string, unknown>; plan_step_id?: string
  source?: string; call_idx?: number; retry_attempt?: number
  /** Original file name for read_attachment (resolved by the backend so cards
   *  render the name after restart, when the frontend cache is empty). */
  attachment_name?: string
}

export interface ToolResultData {
  tool_call_id?: string; step: number; result_len: number; result: string
  result_preview?: string; plan_step_id?: string
  call_idx?: number; retry_attempt?: number
  /** True when the tool call finished with an error (backend emitter flag). */
  error?: boolean
}

export interface ThoughtData { step_num: number; content: string; reasoning?: string; plan_step_id?: string }
export interface StepData { step_num: number }
export interface ErrorData { error: string }

export interface PlanStepData { id?: string; description: string; summary?: string; status: string; depends_on?: string[] }

export interface PlanData {
  step_count: number; steps?: PlanStepData[]; progress?: number
  current_step_index?: number; completed_count?: number; total_count?: number
}

export interface PlanStepStartData { step_id: string; description: string; summary?: string }
export interface PlanStepCompleteData { step_id: string; success: boolean; duration: number; error?: string; progress?: number; current_step_index?: number; completed_count?: number; total_count?: number }
/** Cooperative pause checkpoint: mirrors plan_step_complete's shape minus the
 *  `success` field — a pause is a recoverable state, NOT a completion
 *  (completed_count is untouched; a Resume re-enters the step). */
export interface PlanStepPausedData { step_id: string; duration: number; error?: string; progress?: number; current_step_index?: number; completed_count?: number; total_count?: number }

export interface ToolConfirmData {
  confirm_id: string
  tool: string
  args: string
  reasoning?: string
  /** tool_call_id of the triggering tool_call, for precise correlation. */
  tool_call_id?: string
  /** True when the strict automatic judge (Smart Approve) already evaluated
   *  this call; the advisory "Ask Agent" button should be hidden. */
  disable_judge?: boolean
}

export interface AskUserQuestion {
  id: string; question: string
  options: Array<{ label: string; value: string }>
  multi_select?: boolean; recommended?: string[]
}
export interface AskUserData { request_id: string; questions: AskUserQuestion[] }
export interface StepLimitData { request_id: string; current_step: number; max_steps: number; reason?: string }

export interface ContextFillData {
  fill_percent: number; used_tokens: number; max_tokens: number; status: string
  plan_step_id?: string; session_input_tokens: number; session_output_tokens: number
  model: string; family: string
}

export interface ContextCompactionData { before_percent: number; after_percent: number; plan_step_id?: string }
/** Manual context compaction flow started (CompactSessionContext accepted). */
export interface CompactionStartedData { strategy: string }
/**
 * Manual context compaction flow reached a terminal state. Exactly one of
 * success / cancelled / error applies (success covers the nothing-to-compact
 * no-op too; cancelled when the user aborted — history untouched).
 * nothing_compacted is set when the strategy left the history unchanged (it
 * already fits within the compaction limits): the flow still succeeds, but no
 * marker row is persisted, the percentages are zero, and no context_compaction
 * card follows — the client must not wait for one; when the flow also did not
 * resume a task (idle session, no deferral), the client surfaces the
 * "Context already compacted" activity label instead. deferred_to_resume is
 * set when such a no-op hit a paused unfinished task: the flow armed the
 * orchestrator's one-shot resume compaction before auto-resuming, so the
 * card with the real numbers arrives from the resumed run (treat like
 * resumed). resumed reports whether the flow auto-resumed a task it had
 * paused; paused_without_resume is set when the task was left paused without
 * the flow's auto-resume — the auto-resume FAILED, or the flow honoured a
 * user-owned pause (never stolen; see the backend's session.pauseOwner) — a
 * paused checkpoint remains that the UI never saw (session_paused is
 * suppressed while compacting), so the client must re-apply the paused state.
 * compaction_noop is the post-flow no-op verdict recomputed by the backend on
 * the CURRENT history: true after a successful compaction (the dialogue now
 * fits the target) and after the nothing_compacted outcome; the untouched
 * history's own verdict after a cancelled/failed flow. The client refreshes
 * the compact button's disabled state from it without a status refetch.
 */
export interface CompactionFinishedData {
  strategy: string; success: boolean; cancelled?: boolean; error?: string
  before_percent: number; after_percent: number; resumed?: boolean; paused_without_resume?: boolean
  nothing_compacted?: boolean; deferred_to_resume?: boolean; compaction_noop?: boolean
}
export interface SessionTokensData { session_input_tokens: number; session_output_tokens: number; model: string; family: string; fill_percent?: number; used_tokens?: number; max_tokens?: number }
export interface AssistantChunkData { content: string; accumulated_content?: string }
export interface TaskCompleteData {
  session_id?: string; output?: string; attempt_count?: number; routing_decision?: Record<string, unknown>
  /** Typed success contract: false for partial/failed/aborted executions delivered as task_complete. */
  success?: boolean
  /** Refines the outcome: 'full' | 'partial' | 'failed' | 'aborted'. */
  completion?: string
  /** Number of plan steps that finished with an error in the final attempt. */
  failed_steps?: number
}
export interface SubAgentLaunchData { step_id: string; description: string; plan_step_id?: string }
export interface SubAgentCompleteData { step_id: string; success: boolean; duration: number; plan_step_id?: string }
/** Cooperative pause checkpoint for pure delegate runs (recoverable, not a
 *  failure — no success field; Resume restores the trajectory). For plan-step
 *  subagents the backend translator re-emits plan_step_paused instead. */
export interface SubAgentPausedData { step_id: string; duration: number }
export interface RetryData { attempt: number; max_attempts: number }
export interface StepRetryData { step_id: string; attempt: number; max_attempts: number }
export interface ServiceData { content: string; phase?: string }
export interface SessionRenamedData { new_name: string; old_name?: string; id?: string }
export interface TaskFailedResumableData { message?: string }
export interface ReflectionData {
  summary: string
  insights?: string[]
  suggested_action?: string
  root_cause?: string
  failure_analysis?: string
  action_plan?: string
  reasoning?: string
  attempt: number
  max_attempts: number
}

export interface ToolJudgeResponseData { confirm_id: string; reasoning?: string; error?: string }
/**
 * Strict-judge (Smart Approve) evaluation phase. Emitted around the automatic
 * judge LLM call that runs BEFORE a confirmation card exists — `started` marks
 * the evaluation in flight, `finished` its completion (the tool then executes
 * or a tool_confirm card appears).
 */
export interface ToolJudgePhaseData { tool: string }
export interface TerminalOutputData { data: string }
export interface SkillsActivatedData { skills: string[] }
export interface ToolsAssignedData { tools: string[] }

/** Per-run agent quality counters, as emitted in the `agent_metrics` event
 *  payload on task finish/abort. Mirrors the Go `AgentMetricsData` struct. */
export interface AgentMetricsCounters {
  readonly repeat: number
  readonly same_tool: number
  readonly fruitless: number
  readonly parse: number
  readonly truncation: number
}

export interface AgentMetricsSmallLLM {
  readonly enabled: boolean
  readonly variants: readonly string[]
}

export interface AgentMetricsData {
  readonly finish: string
  readonly parse_errors: number
  readonly nudges: AgentMetricsCounters
  readonly aborts: AgentMetricsCounters
  readonly steps: number
  readonly output_tokens: number
  readonly invalid_tool_calls: number
  readonly small_llm: AgentMetricsSmallLLM
}
export interface BlackboardUpdatedData { change_type: string }

/** Backend AttachmentInfo record (snake_case), as emitted in the
 *  `attachments:changed` event payload and returned by the attachment RPCs.
 *  `is_image`/`thumbnail` are present for image attachments (png/jpg/gif/webp);
 *  `thumbnail` is a JPEG data URI. Both are optional for backward compatibility
 *  with older payloads that predate image support. */
export interface AttachmentInfoRaw {
  readonly id: string
  readonly original_name: string
  readonly format: string
  readonly size_bytes: number
  /** True for image attachments (png/jpg/jpeg/gif/webp). */
  readonly is_image?: boolean
  /** JPEG data URI (64px) for image attachments; omitted for non-images. */
  readonly thumbnail?: string
  /** On-disk location of the staged image; omitted for non-images. Mirrored
   *  into the optimistic user-message metadata so image thumbnails render
   *  immediately instead of after a session reload. */
  readonly path?: string
  /** MIME type of the staged image (e.g. "image/png"); omitted for non-images. */
  readonly media_type?: string
}

/** Backend PasteResult record (snake_case), as returned by the
 *  PasteFromClipboard RPC. `kind` discriminates which fields are populated:
 *  image/files → `files`; text → `text`; image-rejected → `rejected`.
 *  `skipped_images` counts image-ext files dropped because the model lacks
 *  vision (kind=files). `files` uses the same snake_case AttachmentInfoRaw
 *  shape as the attachment RPCs. */
export interface PasteResultRaw {
  readonly kind: PasteKind
  readonly text?: string
  readonly files?: readonly AttachmentInfoRaw[]
  readonly rejected?: string
  readonly skipped_images?: number
}

/** A single file that could not be converted/staged (snake_case backend record). */
export interface AttachmentFailureRaw {
  readonly path: string
  readonly error: string
}

/** Payload of the `attachments:changed` event. `attachments` is the full current
 *  pending list — replace the store. `failed` carries per-file failures from the
 *  most recent attach operation (absent on remove/send-clear). */
export interface AttachmentsChangedData {
  readonly attachments: readonly AttachmentInfoRaw[]
  readonly failed?: readonly AttachmentFailureRaw[]
}

export interface TodoItemData { text: string; checked: boolean }
export interface StepTodoUpdateData {
  step_id: string
  items: TodoItemData[]
  completed_count: number
  total_count: number
}

// --- Plan review event payloads ---

export interface PlanReviewReadyData { request_id: string; plan_path: string; plan_content: string }

// --- Goal event payloads ---

/**
 * Pending goal proposal awaiting user sign-off. Emitted as a DISTINCT
 * `goal_proposal` session event by the desktop goal proposer when the
 * derivation agent calls propose_goal. Surfaces as a pending action that
 * blocks the agent until the user confirms/cancels.
 */
export interface GoalProposalData {
  readonly request_id: string
  readonly session_id: string
  readonly condition: string
  readonly verify: string
  /** Per-goal verification mode chosen by the derivation agent
   *  ('executable' | 're_derivation'); absent/empty means the default
   *  ('executable'). Surfaced so the panel can show/edit how the goal will be
   *  verified and round-trip a user edit back through confirmGoal. */
  readonly verification_mode?: string
}

/**
 * A single piece of evidence supporting a verdict. Mirrors the backend
 * `goal.GoalEvidence` struct. Evidence is what makes a verdict trustworthy
 * rather than a bare assertion: each entry points at something concrete the
 * agent (or user) can inspect.
 *
 * `type` categorizes the evidence:
 *  - test_output — output of a test run (Ref = test name/id or command).
 *  - file        — a file on disk (Ref = path). Rendered as a clickable link.
 *  - command     — a shell command and its output (Ref = command string).
 *  - qualitative — a human judgment (Ref is free text).
 */
export interface GoalEvidence {
  readonly type: string    // test_output | file | command | qualitative
  readonly ref: string     // artifact reference (path, command, id, or note)
  readonly summary: string // human-readable description of what this shows
}

/**
 * Goal status snapshot. The backend emits this as its OWN dedicated
 * `goal_status` session event (not the phase-discriminated `service` channel),
 * carrying the full goal state. The payload is the goal meta fields directly —
 * no `content`/`phase` wrapper.
 */
export interface GoalStatusData {
  readonly status: string
  readonly turn: number
  readonly condition: string
  readonly max_turns: number
  readonly verdict?: string
  readonly reason?: string
  /** The agent's supporting artifacts backing the verdict (goal.LastVerdict.
   *  Evidence). Present whenever a verdict is declared; absent otherwise. */
  readonly evidence?: readonly GoalEvidence[]
  /** Outcome of the independent verifier on the most recent "met" attempt:
   *  "confirmed", "rejected", or "off". Absent when no verification ran. */
  readonly verification?: string
  /** The independent verifier's reason for confirming the goal (present only
   *  when verification === 'confirmed'). */
  readonly verification_reason?: string
  /** The independent verifier's supporting artifacts (present only when
   *  verification === 'confirmed'). */
  readonly verification_evidence?: readonly GoalEvidence[]
  /** Per-goal verification mode echoed from GoalState ('executable' |
   *  're_derivation'). Absent on older backend snapshots; consumers fall back
   *  to a previously-seen value or the default ('executable'). */
  readonly verification_mode?: string
  /** Per-run identity stamped from GoalState.CreatedAt (Unix milliseconds).
   *  Turn counts reset per goal run, so this discriminates consecutive runs in
   *  the same session. Absent on older backend snapshots. */
  readonly created_at?: number
}

/**
 * Mid-loop goal progress telemetry. Emitted as its OWN dedicated
 * `goal_progress` session event after a non-terminal turn.
 */
export interface GoalProgressData {
  readonly turn: number
  readonly max_turns: number
  readonly condition: string
}

// --- Tool manager event payloads ---

export interface ToolManagerToolInfo { readonly name: string; readonly version: string }

export interface ToolManagerStartData { readonly tools: readonly ToolManagerToolInfo[] }

export interface ToolManagerProgressData {
  readonly tool: string
  readonly stage: 'download' | 'extract' | 'python_bootstrap'
  readonly bytes_done: number
  readonly bytes_total: number
}

export interface ToolManagerDoneData {
  readonly installed_count: number
  readonly skipped_count: number
}

// --- Session event map ---

export interface SessionEventMap {
  readonly routing: RoutingData
  readonly step_start: StepData
  readonly step_complete: StepData
  readonly thought: ThoughtData
  readonly tool_call: ToolCallData
  readonly tool_result: ToolResultData
  readonly tool_confirm: ToolConfirmData
  readonly ask_user: AskUserData
  readonly step_limit: StepLimitData
  readonly plan_generated: PlanData
  readonly plan_step_start: PlanStepStartData
  readonly plan_step_complete: PlanStepCompleteData
  /** Cooperative pause checkpoint for a plan step (recoverable, not a
   *  completion — no success field, completed_count untouched). */
  readonly plan_step_paused: PlanStepPausedData
  readonly assistant_chunk: AssistantChunkData
  readonly assistant_done: { readonly content: string; readonly input_tokens: number; readonly output_tokens: number }
  readonly error: ErrorData
  readonly task_complete: TaskCompleteData
  readonly task_cancelled: void
  readonly retry: RetryData
  readonly step_retry: StepRetryData
  readonly service: ServiceData
  readonly subagent_launch: SubAgentLaunchData
  readonly subagent_complete: SubAgentCompleteData
  /** Cooperative pause checkpoint for a pure delegate run (plan-step
   *  subagents surface as plan_step_paused via the backend translator). */
  readonly subagent_paused: SubAgentPausedData
  readonly context_fill: ContextFillData
  readonly context_compaction: ContextCompactionData
  readonly compaction_started: CompactionStartedData
  readonly compaction_finished: CompactionFinishedData
  readonly session_tokens: SessionTokensData
  readonly task_failed_resumable: TaskFailedResumableData
  readonly task_resumed: void
  /** Emitted when a running task cooperatively pauses at a step-boundary
   *  checkpoint (PauseSession / mid-turn pause signal). The UI enters a paused
   *  state: input unlocked, Resume/Stop controls. Complementary to
   *  session_resumed. */
  readonly session_paused: void
  /** Emitted when a paused task resumes (ResumeSession / nudge-resume). Clears
   *  the UI's paused state: input re-locks, Pause/Stop controls return. */
  readonly session_resumed: void
  readonly tool_judge_response: ToolJudgeResponseData
  readonly tool_judge_started: ToolJudgePhaseData
  readonly tool_judge_finished: ToolJudgePhaseData
  readonly finishing: void
  readonly reflection: ReflectionData
  readonly session_renamed: SessionRenamedData
  readonly terminal_output: TerminalOutputData
  /** Fired when a session's shell process exits on its own (user typed
   *  `exit`, shell crash). No payload — the event itself is the signal. The
   *  UI keeps the terminal instance mounted and resurrects the shell lazily
   *  on next activation. Not fired for explicit stop (session deletion, app
   *  shutdown, StartTerminalInDir restarts). */
  readonly terminal_exited: void
  readonly skills_activated: SkillsActivatedData
  readonly tools_assigned: ToolsAssignedData
  readonly agent_metrics: AgentMetricsData
  readonly blackboard_updated: BlackboardUpdatedData
  readonly step_todo_update: StepTodoUpdateData
  readonly plan_review_ready: PlanReviewReadyData
  readonly memory_read: { readonly step_num: number; readonly content: string }
  /** Goal lifecycle events, each its OWN dedicated session event:
   *  goal_proposal (a pending proposal awaiting approval), goal_status (the
   *  full goal state snapshot, emitted on every turn transition), and
   *  goal_progress (mid-loop turn/budget telemetry). */
  readonly goal_proposal: GoalProposalData
  readonly goal_status: GoalStatusData
  readonly goal_progress: GoalProgressData
  /** Attachment list + optional per-file failures. Replace the store, toast failures. */
  readonly 'attachments:changed': AttachmentsChangedData
}

export type SessionEventKey = keyof SessionEventMap

// --- Global event map ---

/** Payload of the global `files:dropped` event (native OS drag-and-drop, fired
 *  by the Wails `OnFileDrop` callback). `paths` are absolute file paths; `x`
 *  and `y` are the drop coordinates in webview pixels. */
export interface FilesDroppedData {
  readonly paths: readonly string[]
  readonly x: number
  readonly y: number
}

// --- App exit-guard payloads ---
//
// Mirror the backend DTOs in desktop/exit_guard.go (snake_case JSON keys).
// Emitted when a quit attempt is intercepted because sessions have live work.

/** One session with live background work (running task or in-flight manual
 *  compaction) whose work would be interrupted by quitting. */
export interface ExitRequestedSession {
  readonly id: string
  readonly name: string
  /** True when the live work is a manual compaction, not a running task. */
  readonly compacting: boolean
}

/** Payload of the global `app:exit_requested` event. The user answers through
 *  the ConfirmExit RPC (never a response event — the decision must reach the
 *  process that owns the exit-confirmed flag). */
export interface ExitRequestedData {
  readonly sessions: readonly ExitRequestedSession[]
  /** True when the intercepted quit belongs to a pending self-update
   *  (ApplyUpdate already launched the staged updater): the confirmation
   *  modal presents restart context. Optional — an absent flag means a
   *  plain quit. The backend arms it only while the staged updater is
   *  still waiting, so a cancelled-then-retried quit after its window
   *  degrades to a plain quit context. */
  readonly update_pending?: boolean
}

// --- Self-update event payloads ---
//
// Mirror the backend DTOs in backend/frontend_api_updater.go (snake_case JSON
// keys). Emitted by the FrontendAPI updater methods as global events so the UI
// can react to check/download/apply progress without polling.

/** Outcome of an update check. `available` is false for "up to date" or when
 *  the latest release was skipped by the user. Mirrors backend UpdateInfo. */
export interface UpdateInfoData {
  readonly available: boolean
  readonly current_version: string
  readonly latest_version: string
  readonly release_notes: string
  readonly published_at: string
  readonly html_url: string
  readonly asset_name: string
}

/** Progress telemetry for the in-flight update download (bytes done / total). */
export interface UpdateProgressData {
  readonly done: number
  readonly total: number
}

/** Structured error emitted when any update step fails. Carries a single
 *  human-readable `message`. */
export interface UpdateErrorData {
  readonly message: string
}

// --- Git-config risk warning payloads ---
//
// Mirror the backend DTOs in backend/frontend_api_gitconfig_risk.go
// (snake_case JSON keys). Emitted when a project switch or an added work
// directory opens a repository whose .git/config carries dangerous keys;
// a clean repository emits nothing.

/** One detected dangerous git-config key (or a synthetic marker such as
 *  "(config unreadable)" for dangers that are not a single key). */
export interface GitConfigRiskFinding {
  readonly key: string
  readonly description: string
}

/** Payload of the global `project:git_config_risk` event. `notice` is the
 *  standing backend statement that repository-defined hooks never run inside
 *  c0wrk — the detected keys are blocked or neutralized on every git call. */
export interface GitConfigRiskData {
  readonly path: string
  readonly source: 'project' | 'workdir'
  readonly notice: string
  readonly findings: readonly GitConfigRiskFinding[]
}

export interface GlobalEventMap {
  readonly 'startup_error': { readonly message: string; readonly error: string; readonly error_code?: string }
  readonly 'runtime_error': { readonly id: string; readonly message: string; readonly error_code?: string }
  readonly 'backend:ready': void
  readonly 'projects:loaded': void
  readonly 'sessions:loaded': void
  readonly 'workspace:tree_changed': void
  /** RESEARCH toggle or artifact change (enable/disable, hypothesis/brief/prior-art write). */
  readonly 'research:changed': void
  readonly 'skills:changed': void
  readonly 'git:status_changed': string
  readonly 'vector_index:status': VectorIndexStatus
  readonly 'project:created': ProjectInfo
  readonly 'project:deleted': string
  readonly 'project:renamed': { readonly id: string; readonly name: string }
  readonly 'session:renamed': { readonly id: string; readonly name: string }
  readonly 'project:switched': ProjectInfo
  /** Dangerous git config detected in a newly opened project or added work
   *  directory (see backend/frontend_api_gitconfig_risk.go). A clean repo
   *  emits nothing. */
  readonly 'project:git_config_risk': GitConfigRiskData
  readonly 'tool_manager:start': ToolManagerStartData
  readonly 'tool_manager:progress': ToolManagerProgressData
  readonly 'tool_manager:done': ToolManagerDoneData
  readonly 'workdirs:changed': void
  readonly 'files:dropped': FilesDroppedData
  /** Quit attempt intercepted because sessions have live work; the user
   *  confirms through the ConfirmExit RPC. Emitted by the close guard
   *  (desktop/exit_guard.go, Wails OnBeforeClose). */
  readonly 'app:exit_requested': ExitRequestedData
  /** Self-update lifecycle: a newer release is available. Payload is the
   *  check result (UpdateInfo). Emitted by CheckForUpdates. */
  readonly 'update:available': UpdateInfoData
  /** Self-update download progress (bytes done / total). Emitted by
   *  DownloadUpdate at ~100ms intervals. */
  readonly 'update:progress': UpdateProgressData
  /** Self-update archive downloaded and integrity-verified, ready to apply. */
  readonly 'update:downloaded': { readonly archive: string }
  /** Self-update step failed. Payload carries a human-readable message. */
  readonly 'update:error': UpdateErrorData
  /** Self-update check found no newer release (up to date or skipped). */
  readonly 'update:none': UpdateInfoData
}

export type GlobalEventKey = keyof GlobalEventMap

// --- Type guard helpers ---

function isObjLocal(v: unknown): v is Record<string, unknown> {
  return isObj(v)
}

export function isRoutingData(d: unknown): d is RoutingData { return isObj(d) && has(d, 'domain', 'complexity') }
export function isStepData(d: unknown): d is StepData { return isObj(d) && has(d, 'step_num') }
export function isThoughtData(d: unknown): d is ThoughtData { return isObj(d) && has(d, 'content', 'step_num') }
export function isToolCallData(d: unknown): d is ToolCallData { return isObj(d) && has(d, 'tool', 'step') }
export function isToolResultData(d: unknown): d is ToolResultData { return isObj(d) && has(d, 'step', 'result_len') }
export function isToolConfirmData(d: unknown): d is ToolConfirmData { return isObj(d) && has(d, 'confirm_id', 'tool') }
export function isAskUserData(d: unknown): d is AskUserData { return isObj(d) && has(d, 'request_id', 'questions') }
export function isStepLimitData(d: unknown): d is StepLimitData { return isObj(d) && has(d, 'request_id', 'current_step', 'max_steps') }
export function isPlanData(d: unknown): d is PlanData { return isObj(d) && has(d, 'step_count') }
export function isPlanStepStartData(d: unknown): d is PlanStepStartData { return isObj(d) && has(d, 'step_id') }
export function isPlanStepCompleteData(d: unknown): d is PlanStepCompleteData {
  if (!isObj(d) || !has(d, 'step_id', 'success')) return false
  // Validate optional progress fields when present.
  if ('progress' in d && d.progress !== undefined && typeof d.progress !== 'number') return false
  if ('current_step_index' in d && d.current_step_index !== undefined && typeof d.current_step_index !== 'number') return false
  if ('completed_count' in d && d.completed_count !== undefined && typeof d.completed_count !== 'number') return false
  if ('total_count' in d && d.total_count !== undefined && typeof d.total_count !== 'number') return false
  return true
}
export function isPlanStepPausedData(d: unknown): d is PlanStepPausedData {
  if (!isObj(d) || !has(d, 'step_id', 'duration')) return false
  // Same optional-field validation as plan_step_complete, minus `success` —
  // the payload never carries it (a pause is not a completion).
  if ('progress' in d && d.progress !== undefined && typeof d.progress !== 'number') return false
  if ('current_step_index' in d && d.current_step_index !== undefined && typeof d.current_step_index !== 'number') return false
  if ('completed_count' in d && d.completed_count !== undefined && typeof d.completed_count !== 'number') return false
  if ('total_count' in d && d.total_count !== undefined && typeof d.total_count !== 'number') return false
  if ('error' in d && d.error !== undefined && typeof d.error !== 'string') return false
  return true
}
export function isAssistantChunkData(d: unknown): d is AssistantChunkData {
  if (!isObjLocal(d)) return false
  const hasContent = 'content' in d && typeof d.content === 'string'
  const hasAccumulated = 'accumulated_content' in d && typeof d.accumulated_content === 'string'
  return hasContent || hasAccumulated
}
export function isErrorData(d: unknown): d is ErrorData { return isObj(d) && has(d, 'error') }
export function isTaskCompleteData(d: unknown): d is TaskCompleteData {
  if (!isObjLocal(d)) return false
  const hasValidOutput = typeof d.output === 'string'
  const hasValidAttempt = typeof d.attempt_count === 'number'
  const hasValidRouting = isObj(d.routing_decision)
  const hasValidSuccess = typeof d.success === 'boolean'
  // Accept any valid field as sufficient evidence of a task_complete event;
  // a missing output field (e.g. Wails serialization edge case) is tolerable
  // as long as another field validates.
  if (!(hasValidOutput || hasValidAttempt || hasValidRouting || hasValidSuccess)) return false
  if ('completion' in d && d.completion !== undefined && typeof d.completion !== 'string') return false
  if ('failed_steps' in d && d.failed_steps !== undefined && typeof d.failed_steps !== 'number') return false
  if ('session_id' in d && d.session_id !== undefined && typeof d.session_id !== 'string') return false
  // Allow missing output when other validators pass (defensive fallback).
  if ('output' in d && d.output !== undefined && typeof d.output !== 'string') return false
  return true
}
export function isRetryData(d: unknown): d is RetryData { return isObj(d) && has(d, 'attempt', 'max_attempts') }
export function isStepRetryData(d: unknown): d is StepRetryData { return isObj(d) && has(d, 'step_id', 'attempt', 'max_attempts') }
export function isServiceData(d: unknown): d is ServiceData { return isObj(d) && has(d, 'content') }
export function isSubAgentLaunchData(d: unknown): d is SubAgentLaunchData { return isObj(d) && has(d, 'step_id') }
export function isSubAgentCompleteData(d: unknown): d is SubAgentCompleteData { return isObj(d) && has(d, 'step_id', 'success') }
export function isSubAgentPausedData(d: unknown): d is SubAgentPausedData {
  if (!isObj(d) || !has(d, 'step_id', 'duration')) return false
  if (typeof d.duration !== 'number') return false
  return true
}
export function isContextFillData(d: unknown): d is ContextFillData { return isObj(d) && has(d, 'fill_percent', 'status') }
export function isContextCompactionData(d: unknown): d is ContextCompactionData { return isObj(d) && has(d, 'before_percent', 'after_percent') }
export function isCompactionStartedData(d: unknown): d is CompactionStartedData { return isObj(d) && has(d, 'strategy') }
export function isCompactionFinishedData(d: unknown): d is CompactionFinishedData {
  if (!isObj(d) || !has(d, 'strategy', 'success', 'before_percent', 'after_percent')) return false
  // Validate the optional no-op outcome flags when present (additive fields —
  // older payloads and non-no-op flows simply omit them).
  if ('nothing_compacted' in d && d.nothing_compacted !== undefined && typeof d.nothing_compacted !== 'boolean') return false
  if ('deferred_to_resume' in d && d.deferred_to_resume !== undefined && typeof d.deferred_to_resume !== 'boolean') return false
  if ('compaction_noop' in d && d.compaction_noop !== undefined && typeof d.compaction_noop !== 'boolean') return false
  return true
}
export function isSessionTokensData(d: unknown): d is SessionTokensData { return isObj(d) && has(d, 'session_input_tokens', 'session_output_tokens') }
export function isSessionRenamedData(d: unknown): d is SessionRenamedData { return isObj(d) && has(d, 'new_name') }
export function isTaskFailedResumableData(d: unknown): d is TaskFailedResumableData {
  if (!isObjLocal(d)) return false
  return !('message' in d) || typeof d.message === 'string'
}
export function isTerminalOutputData(d: unknown): d is TerminalOutputData { return isObj(d) && typeof d.data === 'string' }
export function isSkillsActivatedData(d: unknown): d is SkillsActivatedData { return isObj(d) && Array.isArray(d.skills) }
export function isToolsAssignedData(d: unknown): d is ToolsAssignedData { return isObj(d) && Array.isArray(d.tools) }

function isAgentMetricsCounters(v: unknown): v is AgentMetricsCounters {
  if (!isObj(v)) return false
  return (
    typeof v.repeat === 'number' &&
    typeof v.same_tool === 'number' &&
    typeof v.fruitless === 'number' &&
    typeof v.parse === 'number' &&
    typeof v.truncation === 'number'
  )
}

/** Guard for the `agent_metrics` payload; validates shape, not semantics. */
export function isAgentMetricsData(d: unknown): d is AgentMetricsData {
  if (!isObj(d)) return false
  return (
    typeof d.finish === 'string' &&
    typeof d.parse_errors === 'number' &&
    typeof d.steps === 'number' &&
    typeof d.output_tokens === 'number' &&
    typeof d.invalid_tool_calls === 'number' &&
    isAgentMetricsCounters(d.nudges) &&
    isAgentMetricsCounters(d.aborts) &&
    isObj(d.small_llm) &&
    typeof (d.small_llm as { enabled?: unknown }).enabled === 'boolean' &&
    Array.isArray((d.small_llm as { variants?: unknown }).variants)
  )
}

/**
 * Normalize a persisted `agent_metrics` payload for history-load, tolerating
 * fields added after the row was saved. Older rows predate
 * `invalid_tool_calls` and the `truncation` abort counter; both are defaulted
 * to 0 here. The live `agent_metrics` event handler keeps using the strict
 * `isAgentMetricsData` guard (Go always serializes the full shape for fresh
 * events). Returns undefined when the payload is not an agent_metrics row.
 */
export function normalizeAgentMetricsData(d: unknown): AgentMetricsData | undefined {
  if (!isObj(d)) return undefined
  if (
    typeof d.finish !== 'string' ||
    typeof d.parse_errors !== 'number' ||
    typeof d.steps !== 'number' ||
    typeof d.output_tokens !== 'number'
  ) {
    return undefined
  }
  const counters = (v: unknown): AgentMetricsCounters | undefined => {
    if (!isObj(v)) return undefined
    if (
      typeof v.repeat !== 'number' ||
      typeof v.same_tool !== 'number' ||
      typeof v.fruitless !== 'number' ||
      typeof v.parse !== 'number'
    ) {
      return undefined
    }
    return {
      repeat: v.repeat,
      same_tool: v.same_tool,
      fruitless: v.fruitless,
      parse: v.parse,
      truncation: typeof v.truncation === 'number' ? v.truncation : 0,
    }
  }
  const nudges = counters(d.nudges)
  const aborts = counters(d.aborts)
  if (!nudges || !aborts) return undefined
  const small = d.small_llm
  if (!isObj(small) || typeof small.enabled !== 'boolean' || !Array.isArray(small.variants)) {
    return undefined
  }
  return {
    finish: d.finish,
    parse_errors: d.parse_errors,
    steps: d.steps,
    output_tokens: d.output_tokens,
    invalid_tool_calls: typeof d.invalid_tool_calls === 'number' ? d.invalid_tool_calls : 0,
    nudges,
    aborts,
    small_llm: {
      enabled: small.enabled,
      variants: small.variants,
    },
  }
}
export function isReflectionData(d: unknown): d is ReflectionData { return isObj(d) && has(d, 'summary', 'attempt') }
export function isToolJudgeResponseData(d: unknown): d is ToolJudgeResponseData { return isObj(d) && has(d, 'confirm_id') }
export function isToolJudgePhaseData(d: unknown): d is ToolJudgePhaseData { return isObj(d) && has(d, 'tool') }
export function isBlackboardUpdatedData(d: unknown): d is BlackboardUpdatedData { return isObj(d) && has(d, 'change_type') }

/** Guard for a single backend AttachmentInfo (snake_case). */
export function isAttachmentInfoRaw(v: unknown): v is AttachmentInfoRaw {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as AttachmentInfoRaw).id === 'string' &&
    typeof (v as AttachmentInfoRaw).original_name === 'string' &&
    typeof (v as AttachmentInfoRaw).format === 'string' &&
    typeof (v as AttachmentInfoRaw).size_bytes === 'number'
  )
}

/** Known PasteKind discriminators (mirrors the backend PasteKind enum). */
const PASTE_KINDS: ReadonlySet<string> = new Set(['image', 'files', 'text', 'empty'])

/** Guard for a backend PasteResult (snake_case). `kind` is required and must be
 *  a known PasteKind; `files`, when present, must be an array of valid
 *  AttachmentInfoRaw records. Used by the pasteFromClipboard wrapper to reject
 *  malformed/unexpected backend responses without throwing. */
export function isPasteResultRaw(v: unknown): v is PasteResultRaw {
  if (!isObj(v)) return false
  const raw = v as unknown as PasteResultRaw
  const kind = raw.kind
  if (typeof kind !== 'string' || !PASTE_KINDS.has(kind)) return false
  const files = raw.files
  if (files !== undefined && !isArrayOf(files, isAttachmentInfoRaw)) return false
  if (raw.skipped_images !== undefined && typeof raw.skipped_images !== 'number') return false
  return true
}

/** Guard for a single backend AttachmentFailure (snake_case): both `path`
 *  and `error` are plain strings (the backend always serializes both — no
 *  omitempty on the struct fields). */
export function isAttachmentFailureRaw(v: unknown): v is AttachmentFailureRaw {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as AttachmentFailureRaw).path === 'string' &&
    typeof (v as AttachmentFailureRaw).error === 'string'
  )
}

/** The `attachments:changed` payload is an object with an `attachments` array
 *  and an OPTIONAL `failed` array of per-file failures. Both arrays must fully
 *  validate: the handler consumes `failed[].path` to build the user-facing
 *  error toast, so a malformed entry must drop the event at the boundary
 *  instead of throwing inside the session event handler. */
export function isAttachmentsChangedData(d: unknown): d is AttachmentsChangedData {
  if (!isObj(d)) return false
  const atts = d.attachments
  if (!Array.isArray(atts) || !atts.every(isAttachmentInfoRaw)) return false
  const failed = d.failed
  if (failed !== undefined && (!Array.isArray(failed) || !failed.every(isAttachmentFailureRaw))) {
    return false
  }
  return true
}
export function isStepTodoUpdateData(d: unknown): d is StepTodoUpdateData {
  return isObj(d) && has(d, 'step_id', 'items') && Array.isArray(d.items)
}

export function isPlanReviewReadyData(d: unknown): d is PlanReviewReadyData {
  return isObj(d) && typeof d.request_id === 'string' && typeof d.plan_path === 'string' && typeof d.plan_content === 'string'
}

// --- Goal event type guards ---

/** Guard for a goal_proposal payload (distinct session event). */
export function isGoalProposalData(d: unknown): d is GoalProposalData {
  return isObj(d)
    && typeof d.request_id === 'string'
    && typeof d.session_id === 'string'
    && typeof d.condition === 'string'
    && typeof d.verify === 'string'
}

/**
 * Guard for a goal_status payload. The backend emits goal_status as a dedicated
 * session event; the required numeric/string goal fields must validate before
 * consumption.
 */
export function isGoalStatusData(d: unknown): d is GoalStatusData {
  if (!isObj(d)) return false
  return typeof d.status === 'string'
    && typeof d.turn === 'number'
    && typeof d.condition === 'string'
    && typeof d.max_turns === 'number'
}

/**
 * Guard for a goal_progress payload. Emitted as a dedicated session event.
 */
export function isGoalProgressData(d: unknown): d is GoalProgressData {
  if (!isObj(d)) return false
  return typeof d.turn === 'number'
    && typeof d.max_turns === 'number'
    && typeof d.condition === 'string'
}

// --- Global event type guards ---

export type StartupError = GlobalEventMap['startup_error']

export function isStartupError(d: unknown): d is StartupError {
  return isObj(d) && typeof d.message === 'string' && typeof d.error === 'string'
}

export type RuntimeError = GlobalEventMap['runtime_error']

export function isRuntimeError(d: unknown): d is RuntimeError {
  return isObj(d) && typeof d.id === 'string' && typeof d.message === 'string'
}

/** Guard for a `project:git_config_risk` payload (dangerous git config in a
 *  newly opened project or added work directory). `findings` must be non-empty
 *  and fully typed — the toast renders each key/description pair directly. */
export function isGitConfigRiskData(d: unknown): d is GitConfigRiskData {
  if (!isObj(d)) return false
  if (typeof d.path !== 'string' || typeof d.notice !== 'string') return false
  if (d.source !== 'project' && d.source !== 'workdir') return false
  if (!Array.isArray(d.findings) || d.findings.length === 0) return false
  return d.findings.every(
    (f) => isObj(f) && typeof f.key === 'string' && typeof f.description === 'string',
  )
}

/** Guard for a `files:dropped` payload (native OS drag-and-drop). */
export function isFilesDroppedData(d: unknown): d is FilesDroppedData {
  if (!isObj(d)) return false
  if (!Array.isArray(d.paths)) return false
  if (!d.paths.every((p) => typeof p === 'string')) return false
  return typeof d.x === 'number' && typeof d.y === 'number'
}

/** Guard for an `app:exit_requested` payload (intercepted quit). Strict:
 *  each session entry needs `id` and `name` strings, and `update_pending`,
 *  when present, must be a boolean. A payload that fails this guard never
 *  reaches the modal's session list — the consumer (useExitGuard) reports
 *  the drop and opens the generic list-less modal instead, because the
 *  backend has already prevented the quit and an unanswered one would
 *  leave the app unclosable. */
export function isExitRequestedData(d: unknown): d is ExitRequestedData {
  if (!isObj(d)) return false
  if (!Array.isArray(d.sessions)) return false
  if (d.update_pending !== undefined && typeof d.update_pending !== 'boolean') return false
  return d.sessions.every((s) => isObj(s) && typeof s.id === 'string' && typeof s.name === 'string')
}

const VALID_VECTOR_STATES: ReadonlySet<string> = new Set(['idle', 'indexing', 'ready', 'reindexing', 'unavailable'])

export function isVectorIndexPayload(d: unknown): d is VectorIndexStatus {
  if (!isObjLocal(d)) return false
  if (typeof d.state !== 'string' || !VALID_VECTOR_STATES.has(d.state)) return false
  if (typeof d.progress !== 'number') return false
  if (typeof d.files_indexed !== 'number') return false
  if (typeof d.total_files !== 'number') return false
  return true
}

// --- Tool manager event type guards ---

function isToolManagerToolInfo(d: unknown): d is ToolManagerToolInfo {
  return isObj(d) && typeof d.name === 'string' && typeof d.version === 'string'
}

export function isToolManagerStartData(d: unknown): d is ToolManagerStartData {
  return isObj(d) && Array.isArray(d.tools) && d.tools.every(isToolManagerToolInfo)
}

const VALID_TOOL_STAGES = new Set(['download', 'extract', 'python_bootstrap'])

export function isToolManagerProgressData(d: unknown): d is ToolManagerProgressData {
  return isObj(d) && typeof d.tool === 'string' && typeof d.stage === 'string' &&
    VALID_TOOL_STAGES.has(d.stage) &&
    typeof d.bytes_done === 'number' && typeof d.bytes_total === 'number'
}

