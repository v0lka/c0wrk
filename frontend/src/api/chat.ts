// Chat / task API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isChatMessage, isTokenInfo, isArrayOf } from '@/types/guards'
import type { ChatMessage, TokenInfo } from '@/types/models'

/**
 * Send a user message to the session's agent.
 *
 * @param goal       Enable goal mode for the first message of a task (OR-ed
 *                   with any /goal prefix the message text carries).
 * @param goalBudget Optional JSON budget override ({"max_turns":N});
 *                   empty = unlimited.
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
  reviewMode: boolean = false,
): Promise<void> {
  try {
    const app = getApp()
    await app.SendMessage(sessionId, text, activeSkills, activeAgents, modelOverride, reasoningOverride, goal, goalBudget, reviewMode)
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

export async function getSessionHistory(sessionId: string): Promise<ChatMessage[]> {
  try {
    const app = getApp()
    const result = await app.GetSessionHistory(sessionId)
    if (!isArrayOf(result, isChatMessage)) {
      logger.error('getSessionHistory: unexpected response shape, returning []', result)
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

export async function cancelUnfinishedTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelUnfinishedTask(sessionId)
  } catch (err) {
    logger.error('Failed to cancel unfinished task:', err)
    throw err
  }
}

/** Live/persisted execution state of a session (see backend GetSessionRuntimeStatus). */
export interface SessionRuntimeStatus {
  active: boolean
  has_unfinished_task: boolean
  unfinished_task_id?: string
}

function isSessionRuntimeStatus(d: unknown): d is SessionRuntimeStatus {
  return typeof d === 'object' && d !== null
    && typeof (d as Record<string, unknown>).active === 'boolean'
    && typeof (d as Record<string, unknown>).has_unfinished_task === 'boolean'
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
