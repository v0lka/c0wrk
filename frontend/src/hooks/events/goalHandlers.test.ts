// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { useGoalStore } from '@/stores/goalStore'
import { handleGoalStatusEvent } from './goalHandlers'
import type { GoalStatusData } from '@/types/events'

const SESSION = 'sess-evidence'

function makeStatus(over: Partial<GoalStatusData> = {}): GoalStatusData {
  return {
    content: 'Goal status: met',
    phase: 'goal_status',
    status: 'met',
    turn: 3,
    condition: 'ship it',
    max_turns: 10,
    verdict: 'met',
    reason: 'all green',
    evidence: [{ type: 'file', ref: 'core/x.go', summary: 'the fix' }],
    verification: 'confirmed',
    verification_reason: 'verifier: tests pass',
    verification_evidence: [{ type: 'command', ref: 'go test ./...', summary: 'all pass' }],
    verification_mode: 'executable',
    ...over,
  }
}

describe('handleGoalStatusEvent — evidence + verification', () => {
  beforeEach(() => {
    useGoalStore.getState().clearAll()
  })

  it('maps verdict evidence and verification outcome onto the active goal', () => {
    handleGoalStatusEvent(SESSION, makeStatus())

    const goal = useGoalStore.getState().activeGoal[SESSION]
    expect(goal).toBeDefined()
    expect(goal!.verdict).toBe('met')
    expect(goal!.reason).toBe('all green')
    expect(goal!.evidence).toEqual([{ type: 'file', ref: 'core/x.go', summary: 'the fix' }])
    expect(goal!.verification).toBe('confirmed')
    expect(goal!.verificationReason).toBe('verifier: tests pass')
    expect(goal!.verificationEvidence).toEqual([
      { type: 'command', ref: 'go test ./...', summary: 'all pass' },
    ])
  })

  it('keeps goalStatus in sync with the snapshot status', () => {
    handleGoalStatusEvent(SESSION, makeStatus({ status: 'exhausted' }))
    expect(useGoalStore.getState().goalStatus[SESSION]).toBe('exhausted')
  })

  it('leaves evidence/verification undefined when the snapshot carries no verdict', () => {
    handleGoalStatusEvent(SESSION, makeStatus({ verdict: undefined, reason: undefined, evidence: undefined, verification: undefined, verification_reason: undefined, verification_evidence: undefined }))

    const goal = useGoalStore.getState().activeGoal[SESSION]
    expect(goal!.verdict).toBeUndefined()
    expect(goal!.evidence).toBeUndefined()
    expect(goal!.verification).toBeUndefined()
    expect(goal!.verificationEvidence).toBeUndefined()
  })

  it('preserves a previously-confirmed verify across status snapshots', () => {
    // Seed an approved goal (mirrors GoalProposalPanel.onApprove seeding).
    useGoalStore.getState().setActiveGoal(SESSION, {
      condition: 'ship it',
      verify: 'run go test',
      status: 'active',
      turn: 0,
    })
    // A later status snapshot does not echo verify back.
    handleGoalStatusEvent(SESSION, makeStatus())

    expect(useGoalStore.getState().activeGoal[SESSION]!.verify).toBe('run go test')
  })
})
