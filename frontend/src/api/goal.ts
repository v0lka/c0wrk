// Goal lifecycle RPC wrappers.
//
// Thin wrappers over the desktop App bindings for the goal-mode control flow:
// confirm/cancel a pending proposal, pause/resume the running goal loop, and
// clear a session's goal. Mirror the backend signatures in
// backend/frontend_api_goal.go (ConfirmGoal(sessionID, requestID, condition,
// verify, verificationMode), CancelGoal(sessionID, requestID),
// Pause/Resume/ClearGoal(sessionID)).

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

/** Approve a pending goal proposal, optionally with user edits to the
 *  condition/verify fields and the per-goal verification mode. Unblocks the
 *  derivation agent's propose_goal call. */
export async function confirmGoal(sessionId: string, requestId: string, condition: string, verify: string, verificationMode: string): Promise<void> {
  try {
    const app = getApp()
    await app.ConfirmGoal(sessionId, requestId, condition, verify, verificationMode)
  } catch (err) {
    logger.error('Failed to confirm goal:', err)
    throw err
  }
}

/** Cancel a pending goal proposal. The agent exits the goal loop without
 *  committing to a goal. */
export async function cancelGoal(sessionId: string, requestId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelGoal(sessionId, requestId)
  } catch (err) {
    logger.error('Failed to cancel goal:', err)
    throw err
  }
}

/** Signal the running goal loop to pause at the top of its next turn. */
export async function pauseGoal(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.PauseGoal(sessionId)
  } catch (err) {
    logger.error('Failed to pause goal:', err)
    throw err
  }
}

/** Re-enter the goal loop for a paused (or still-active) goal. The optional
 *  modelOverride/reasoningOverride apply the user's current selection to the
 *  resumed goal instead of inheriting the interrupted task's settings. */
export async function resumeGoal(sessionId: string, modelOverride: string = '', reasoningOverride: string = ''): Promise<void> {
  try {
    const app = getApp()
    await app.ResumeGoal(sessionId, modelOverride, reasoningOverride)
  } catch (err) {
    logger.error('Failed to resume goal:', err)
    throw err
  }
}

/** Clear a session's goal: cancels the in-flight task and marks the persisted
 *  goal cancelled so it will not resume on restart. */
export async function clearGoal(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.ClearGoal(sessionId)
  } catch (err) {
    logger.error('Failed to clear goal:', err)
    throw err
  }
}
