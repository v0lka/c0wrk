// Chat / task API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isChatMessage, isTokenInfo, isArrayOf } from '@/types/guards'
import { isCompactionAvailability } from '@/types/events'
import type { ChatMessage, TokenInfo, CompactionAvailability } from '@/types/models'

/**
 * Send a user message to the session's agent.
 *
 * @param goal       Enable goal mode for the first message of a task (OR-ed
 *                   with any /goal prefix the message text carries).
 * @param goalBudget Optional JSON budget override ({"max_turns":N});
 *                   empty = unlimited.
 * @param e2s        Enable the explicit-execution-state (E2S) loop for the
 *                   first message of a task. Mutually exclusive with goal.
 * @param reviewMode Marks the message as code review feedback the agent must
 *                   address (the system prompt gains a Code Review section).
 */
export async function sendMessage(
  sessionId: string,
  text: string,
  activeSkills: string[] = [],
  activeAgents: string[] = [],
  modelOverride: string = '',
  reasoningOverride: string = '',
  goal: boolean = false,
  goalBudget: string = '',
  e2s: boolean = false,
  reviewMode: boolean = false,
): Promise<void> {
  try {
    const app = getApp()
    // Positional args must match the Go SendMessage binding EXACTLY
    // (id, text, skills, agents, modelOverride, reasoning, goal, goalBudget,
    // e2s, reviewMode) — a drift silently drops mode flags before they reach
    // HandleOptions.
    await app.SendMessage(sessionId, text, activeSkills, activeAgents, modelOverride, reasoningOverride, goal, goalBudget, e2s, reviewMode)
  } catch (err) {
    logger.error('Failed to send message:', err)
    throw err
  }
}

export async function cancelTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelTask(sessionId)
  } catch (err) {
    logger.error('Failed to cancel task:', err)
    throw err
  }
}

/**
 * Fetch a session's full content history, oldest-first, in a single call.
 * Go marshals a nil slice to JSON null, so a session with no history
 * legitimately arrives as null — normalize it to [] for consumers.
 */
export async function getSessionHistory(sessionId: string): Promise<ChatMessage[]> {
  try {
    const app = getApp()
    const result = await app.GetSessionHistory(sessionId)
    // Go marshals a nil slice to JSON null — treat it as an empty history.
    if (result === undefined || result === null) return []
    if (!isArrayOf(result, isChatMessage)) {
      logger.error('getSessionHistory: unexpected response shape, returning empty history', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to get session history:', err)
    throw err
  }
}

export async function getSessionTokens(sessionId: string): Promise<TokenInfo> {
  try {
    const app = getApp()
    const result = await app.GetSessionTokens(sessionId)
    if (!isTokenInfo(result)) {
      throw new Error('getSessionTokens: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get session tokens:', err)
    throw err
  }
}

export async function resumeTask(sessionId: string, modelOverride: string = '', reasoningOverride: string = ''): Promise<void> {
  try {
    const app = getApp()
    await app.ResumeTask(sessionId, modelOverride, reasoningOverride)
  } catch (err) {
    logger.error('Failed to resume task:', err)
    throw err
  }
}

/**
 * Cooperatively pause the running task in a session. The executor checks the
 * pause signal at every step boundary, stops at a checkpoint, and the task is
 * persisted as paused so a later resume (or nudge-resume) can re-enter.
 * No-op when no request is in flight.
 */
export async function pauseSession(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.PauseSession(sessionId)
  } catch (err) {
    logger.error('Failed to pause session:', err)
    throw err
  }
}

/**
 * Resume a paused task. The optional modelOverride/reasoningOverride apply the
 * user's current selection to the resumed task. The optional nudge seeds the
 * resumed turn with a trailing user message (used by the UI's nudge input on a
 * paused session). Returns nil if there is nothing to resume.
 */
export async function resumeSession(sessionId: string, modelOverride: string = '', reasoningOverride: string = '', nudge: string = ''): Promise<void> {
  try {
    const app = getApp()
    await app.ResumeSession(sessionId, modelOverride, reasoningOverride, nudge)
  } catch (err) {
    logger.error('Failed to resume session:', err)
    throw err
  }
}

/**
 * Start a manual context compaction of the session's conversation history
 * with the named strategy ("sliding_window" | "summarization" |
 * "hierarchical"). Asynchronous: a running task is paused first (exactly like
 * pauseSession, waiting for its checkpoint), then compacted, then auto-resumed.
 * Progress arrives via the compaction_started / compaction_finished session
 * events. Rejects immediately for an unknown strategy or when a compaction is
 * already in flight.
 */
export async function compactSessionContext(sessionId: string, strategy: string): Promise<void> {
  try {
    const app = getApp()
    await app.CompactSessionContext(sessionId, strategy)
  } catch (err) {
    logger.error('Failed to start context compaction:', err)
    throw err
  }
}

/**
 * Cancel an in-flight manual context compaction. When the flow is still
 * waiting for the running task's pause checkpoint it skips the compaction
 * (the history stays untouched); when the compaction is running it aborts the
 * summarize calls. No-op when nothing is compacting.
 */
export async function cancelSessionCompaction(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelSessionCompaction(sessionId)
  } catch (err) {
    logger.error('Failed to cancel context compaction:', err)
    throw err
  }
}

export async function cancelUnfinishedTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelUnfinishedTask(sessionId)
  } catch (err) {
    logger.error('Failed to cancel unfinished task:', err)
    throw err
  }
}

/** One durable execution unit's lifecycle, as reported by
 *  GetSessionRuntimeStatus.work_units (see backend workUnitSnapshot). StepID is
 *  the same id the subagent_launch/plan_step_start chat events carry. */
export interface WorkUnitSnapshot {
  step_id: string
  /** Unit classification: 'subagent' | 'plan_step' | 'goal_verification' | ... */
  kind?: string
  /** 'pending' | 'running' | 'paused' | 'completed' | 'failed' | 'interrupted' */
  status: string
  parent_id?: string
}

/** Live/persisted execution state of a session (see backend GetSessionRuntimeStatus). */
export interface SessionRuntimeStatus {
  active: boolean
  has_unfinished_task: boolean
  unfinished_task_id?: string
  /** Raw persisted status of the resumable task ("in_progress" | "paused" |
   *  "failed"), or absent when there is none. Lets the reconcile seed the live
   *  overlay with the exact value instead of collapsing to "failed". */
  unfinished_task_status?: string
  /** True when the resumable unfinished task is cooperatively paused. */
  paused: boolean
  /** True while a manual context compaction is in flight. */
  compacting?: boolean
  /**
   * Per-strategy manual-compaction prediction for the session's current
   * conversation history (see backend GetSessionRuntimeStatus): whether each
   * strategy would actually shrink the dialogue right now, plus its predicted
   * reclaim. Absent/undefined (older backend, unknown session) fails OPEN: the
   * compact menu shows every strategy clickable, and a pointless click reports
   * the existing nothing_compacted outcome.
   */
  compaction_availability?: CompactionAvailability[]
  /**
   * Live activity label tracked by the backend emitter ("Thinking...",
   * "Routing request...", "Generating response...", ...). Authoritative only
   * while `active`; replaces the frozen activityStatus left over from before
   * a session/project switch. Empty when no tracked event has fired yet.
   */
  activity?: string
  /** True while an assistant stream is open (chunk without the closing done). */
  streaming?: boolean
  /**
   * Durable work-unit snapshot for the session's resumable task (see backend
   * GetSessionRuntimeStatus). The session-load reconciliation aligns
   * paused/interrupted delegate & plan-step chat blocks against it so they do
   * not render a stale "running" after a restart. Absent (older backend / no
   * resumable task) leaves the blocks driven by the replayed messages alone.
   */
  work_units?: WorkUnitSnapshot[]
}

// isWorkUnitSnapshot validates one work-unit entry: a step id and a status are
// required; kind/parent_id are optional metadata.
function isWorkUnitSnapshot(u: unknown): u is WorkUnitSnapshot {
  return typeof u === 'object' && u !== null
    && typeof (u as Record<string, unknown>).step_id === 'string'
    && typeof (u as Record<string, unknown>).status === 'string'
}

function isSessionRuntimeStatus(d: unknown): d is SessionRuntimeStatus {
  return typeof d === 'object' && d !== null
    && typeof (d as Record<string, unknown>).active === 'boolean'
    && typeof (d as Record<string, unknown>).has_unfinished_task === 'boolean'
    && (!('unfinished_task_status' in d)
      || (d as Record<string, unknown>).unfinished_task_status === undefined
      || typeof (d as Record<string, unknown>).unfinished_task_status === 'string')
    && (!('compaction_availability' in d)
      || (d as Record<string, unknown>).compaction_availability === undefined
      || isArrayOf((d as Record<string, unknown>).compaction_availability, isCompactionAvailability))
    && (!('compacting' in d)
      || (d as Record<string, unknown>).compacting === undefined
      || typeof (d as Record<string, unknown>).compacting === 'boolean')
    && (!('streaming' in d)
      || (d as Record<string, unknown>).streaming === undefined
      || typeof (d as Record<string, unknown>).streaming === 'boolean')
    && (!('activity' in d)
      || (d as Record<string, unknown>).activity === undefined
      || typeof (d as Record<string, unknown>).activity === 'string')
    && (!('work_units' in d)
      || (d as Record<string, unknown>).work_units === undefined
      || (d as Record<string, unknown>).work_units === null
      || isArrayOf((d as Record<string, unknown>).work_units, isWorkUnitSnapshot))
}

/**
 * Query whether a task is running in the session and whether an unfinished
 * (resumable) task is persisted. Returns null on failure — callers must treat
 * null as "unknown", not as "idle".
 */
export async function getSessionRuntimeStatus(sessionId: string): Promise<SessionRuntimeStatus | null> {
  try {
    const app = getApp()
    const result = await app.GetSessionRuntimeStatus(sessionId)
    if (!isSessionRuntimeStatus(result)) {
      logger.error('getSessionRuntimeStatus: unexpected response shape', result)
      return null
    }
    return result
  } catch (err) {
    logger.error('Failed to get session runtime status:', err)
    return null
  }
}

// --- Pending HITL actions ---

export interface PendingToolConfirm {
  confirm_id: string
  tool: string
  args: string
  reasoning?: string
  tool_call_id?: string
  disable_judge?: boolean
}

export interface PendingStepLimit {
  request_id: string
  current_step: number
  max_steps: number
  reason?: string
}

export interface PendingPlanApproval {
  request_id: string
  plan_path: string
  plan_content: string
}

export interface PendingAskUser {
  request_id: string
  questions: Array<{ id: string; question: string; options: Array<{ label: string; value: string }>; multi_select?: boolean; recommended?: string[] }>
}

export interface PendingGoalProposal {
  request_id: string
  condition: string
  verify: string
  /** Per-goal verification mode ('executable' | 're_derivation'); absent means
   *  the default ('executable'). */
  verification_mode?: string
}

export interface PendingActionsResponse {
  tool_confirms: PendingToolConfirm[]
  step_limits: PendingStepLimit[]
  plan_approvals: PendingPlanApproval[]
  ask_user: PendingAskUser[]
  goal_proposals: PendingGoalProposal[]
}

// isPendingActionsResponse validates the GetPendingActions response shape.
// Each kind must be an array OR null/absent: Go's encoding/json marshals a nil
// slice to JSON `null` (not `[]`), so a session without a given kind of
// pending action legitimately produces null for that field. null/absent is
// treated as "no pending actions of this kind" and normalized to [] by the
// caller — rejecting it here would silently disable HITL reconciliation.
function isPendingActionsResponse(d: unknown): boolean {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  const kinds = [o.tool_confirms, o.step_limits, o.plan_approvals, o.ask_user, o.goal_proposals]
  return kinds.every(k => k === undefined || k === null || Array.isArray(k))
}

/**
 * Fetch all pending HITL prompts (tool_confirm, step_limit, plan_review,
 * ask_user) currently blocking a session's agent goroutine. Called on
 * session switch to resurface prompts whose events were missed while the
 * session was in the background, and to reconcile stale persisted prompts
 * (a persisted HITL message NOT in this response has already been resolved).
 */
export async function getPendingActions(sessionId: string): Promise<PendingActionsResponse | null> {
  try {
    const app = getApp()
    const result = await app.GetPendingActions(sessionId)
    if (!isPendingActionsResponse(result)) {
      logger.error('getPendingActions: unexpected response shape', result)
      return null
    }
    // Normalize null/absent kinds to empty arrays (Go nil-slice → JSON null)
    // so downstream `.map(...)` consumers never hit a null.
    const o = result as Record<string, unknown>
    return {
      tool_confirms: (o.tool_confirms as PendingToolConfirm[] | undefined) ?? [],
      step_limits: (o.step_limits as PendingStepLimit[] | undefined) ?? [],
      plan_approvals: (o.plan_approvals as PendingPlanApproval[] | undefined) ?? [],
      ask_user: (o.ask_user as PendingAskUser[] | undefined) ?? [],
      goal_proposals: (o.goal_proposals as PendingGoalProposal[] | undefined) ?? [],
    }
  } catch (err) {
    logger.error('Failed to get pending actions:', err)
    return null
  }
}

/**
 * Persist the resolution of a stale HITL prompt so it does not reappear as
 * pending on the next session reload. This is a best-effort write — the
 * in-memory store is already updated optimistically; this call makes the
 * resolution durable. The backend ResolvePendingMessage matches the persisted
 * message by (role, matchField, matchValue) and merges `extra` into its
 * metadata.
 */
export async function resolveStalePrompt(
  sessionId: string,
  role: string,
  matchField: string,
  matchValue: string,
  extra: Record<string, unknown>,
): Promise<void> {
  try {
    const app = getApp()
    await app.ResolvePendingMessage(sessionId, role, matchField, matchValue, extra)
  } catch (err) {
    // Best-effort: a missed persist is self-healing via stale reconciliation
    // on the next reload (the prompt resolves in-memory again).
    logger.error('Failed to persist stale prompt resolution:', err)
  }
}
