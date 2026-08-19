import { describe, it, expect } from 'vitest'
import { buildGoalTransitionNotice, goalStatusToActiveGoal } from './goalTransition'
import type { GoalStatusData } from '@/types/events'
import type { ActiveGoal } from '@/stores/goalStore'

function status(over: Partial<GoalStatusData> = {}): GoalStatusData {
  return {
    status: 'met',
    turn: 2,
    condition: 'ship it',
    max_turns: 5,
    ...over,
  }
}

describe('buildGoalTransitionNotice', () => {
  it('prefers a terminal status over a trailing retry marker', () => {
    // A rejected met claim that then exhausted the budget emits status
    // "exhausted" alongside verification "rejected" and a synthesized
    // not_met verdict. It must read as the terminal outcome, not "retrying".
    const notice = buildGoalTransitionNotice(status({
      status: 'exhausted',
      verdict: 'not_met',
      verification: 'rejected',
      reason: 'still failing',
    }))
    expect(notice).toBe('Goal not reached — turn budget exhausted (turn 2/5)')
  })

  it('renders a rejected-verification retry notice for an active snapshot', () => {
    const notice = buildGoalTransitionNotice(status({
      status: 'active',
      verification: 'rejected',
      reason: 'tests red',
    }))
    expect(notice).toContain('verifier rejected, retrying')
    expect(notice).toContain('tests red')
  })

  it('renders a not_met retry notice for an active snapshot', () => {
    const notice = buildGoalTransitionNotice(status({ status: 'active', verdict: 'not_met' }))
    expect(notice).toContain('retrying')
  })

  it('returns null for a bare active snapshot', () => {
    expect(buildGoalTransitionNotice(status({ status: 'active' }))).toBeNull()
  })
})

describe('goalStatusToActiveGoal', () => {
  it('maps snapshot fields and preserves a previously-confirmed verify/mode', () => {
    const prev: ActiveGoal = {
      condition: 'ship it',
      status: 'active',
      turn: 0,
      verify: 'go test ./...',
      verificationMode: 're_derivation',
    }
    const goal = goalStatusToActiveGoal(status({ verification_mode: '' }), prev)
    expect(goal.verify).toBe('go test ./...')
    expect(goal.verificationMode).toBe('re_derivation')
    expect(goal.status).toBe('met')
    expect(goal.turn).toBe(2)
  })

  it('prefers the snapshot verification mode when present', () => {
    const goal = goalStatusToActiveGoal(status({ verification_mode: 'executable' }))
    expect(goal.verificationMode).toBe('executable')
  })

  it('maps the per-run created_at identity onto createdAt', () => {
    const goal = goalStatusToActiveGoal(status({ created_at: 1724073600000 }))
    expect(goal.createdAt).toBe(1724073600000)
  })

  it('leaves createdAt undefined when the snapshot omits it', () => {
    const goal = goalStatusToActiveGoal(status())
    expect(goal.createdAt).toBeUndefined()
  })
})
