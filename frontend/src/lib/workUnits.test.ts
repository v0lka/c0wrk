import { describe, it, expect, beforeEach } from 'vitest'
import { reconcileWorkUnits, applyWorkUnitSettled, workUnitBlockStatus } from './sessionRuntime'
import { groupMessages } from '@/lib/chatUtils'
import { useChatStore } from '@/stores/chatStore'
import type { ChatMessageUI } from '@/types/messages'
import type { WorkUnitSnapshot } from '@/api/chat'

const SESSION = 'sess-work-units'

function resetStore(): void {
  useChatStore.setState({
    messages: {},
    messageOrder: {},
    taskActive: {},
    unfinishedTaskStatus: {},
    streamingText: {},
    activityStatus: {},
    paused: {},
    pausing: {},
    compacting: {},
    compactionAvailability: {},
    runtimeEventAt: {},
    taskFlagsEventAt: {},
    workUnitStatus: {},
    workUnitEventAt: {},
  })
}

function uiMsg(
  id: string,
  type: ChatMessageUI['type'],
  metadata: Record<string, unknown>,
): ChatMessageUI {
  return { id, sessionId: SESSION, type, content: '', metadata, timestamp: Date.now() }
}

/** The subagent block for a step id, as groupMessages renders it. */
function subagentStatus(messages: ChatMessageUI[], stepId: string): string | undefined {
  const overlay = useChatStore.getState().workUnitStatus[SESSION]
  const item = groupMessages(messages, overlay).items.find(
    i => (i.kind === 'subagent' || i.kind === 'plan_step') && i.stepId === stepId,
  )
  return item && (item.kind === 'subagent' || item.kind === 'plan_step') ? item.status : undefined
}

describe('workUnitBlockStatus', () => {
  it('maps durable unit statuses onto block statuses', () => {
    expect(workUnitBlockStatus('running')).toBe('running')
    expect(workUnitBlockStatus('paused')).toBe('paused')
    expect(workUnitBlockStatus('completed')).toBe('completed')
    expect(workUnitBlockStatus('failed')).toBe('failed')
    expect(workUnitBlockStatus('interrupted')).toBe('interrupted')
    // A registered-but-unstarted unit has no pending visual on a subagent
    // block, so it renders as running.
    expect(workUnitBlockStatus('pending')).toBe('running')
    // Unknown / malformed → skipped.
    expect(workUnitBlockStatus('bogus')).toBeNull()
  })
})

describe('reconcileWorkUnits', () => {
  beforeEach(resetStore)

  it('stores a step-id overlay from the durable snapshot', () => {
    const units: WorkUnitSnapshot[] = [
      { step_id: 'del_1', kind: 'subagent', status: 'interrupted' },
      { step_id: 'step_2', kind: 'plan_step', status: 'paused' },
      { step_id: 'del_9', kind: 'subagent', status: 'bogus' },
    ]
    const overlay = reconcileWorkUnits(SESSION, units)
    expect(overlay).toEqual({ del_1: 'interrupted', step_2: 'paused' })
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual(overlay)
  })

  it('distinguishes "no data" from "no units"', () => {
    reconcileWorkUnits(SESSION, [{ step_id: 'del_1', status: 'paused' }])
    // An ABSENT snapshot is no data (an older backend / no durable units): the
    // overlay must be left alone, not silently cleared.
    expect(reconcileWorkUnits(SESSION, undefined)).toEqual({ del_1: 'paused' })
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_1: 'paused' })
    // An EMPTY snapshot is the backend's authoritative "no reconcilable units".
    expect(reconcileWorkUnits(SESSION, [])).toEqual({})
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({})
  })

  it('does not let a stale snapshot clobber a fresher live settlement', () => {
    // The snapshot was read a moment before the settle event landed.
    const snapshotReadAt = Date.now() - 1000
    useChatStore.getState().settleWorkUnit(SESSION, 'del_live', 'interrupted')
    reconcileWorkUnits(SESSION, [{ step_id: 'del_live', status: 'running' }], snapshotReadAt)
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_live: 'interrupted' })
  })

  // Regression (finding 29): a fresh live relaunch fires clearWorkUnitStep for
  // a step whose overlay entry is already absent (the prior run's snapshot
  // holds it paused/interrupted, but no live entry exists). The clear must
  // STILL stamp workUnitEventAt so a status read issued BEFORE the launch
  // cannot re-add the stale entry — the no-entry path is a live write too.
  it('a clear of a step with no overlay entry still outranks an older snapshot read', () => {
    const snapshotReadAt = Date.now() - 1000
    const statusBefore = useChatStore.getState().workUnitStatus
    // No overlay entry exists for the step; the fresh launch clears it.
    useChatStore.getState().clearWorkUnitStep(SESSION, 'del_relaunch')
    // The no-entry clear stamped the live-write time for the step...
    expect(useChatStore.getState().workUnitEventAt[SESSION]?.['del_relaunch']).toBeGreaterThan(snapshotReadAt)
    // ...while leaving the workUnitStatus map reference untouched (React #185).
    expect(useChatStore.getState().workUnitStatus).toBe(statusBefore)
    // The in-flight snapshot (read before the launch) still records it paused.
    reconcileWorkUnits(SESSION, [{ step_id: 'del_relaunch', status: 'paused' }], snapshotReadAt)
    // The older snapshot could not re-add the entry.
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({})
  })
})

describe('groupMessages with the work-unit overlay', () => {
  beforeEach(resetStore)

  it('renders an interrupted delegate as interrupted, not running (simulated restart)', () => {
    // Replayed history: the delegate was launched, then the app died — no
    // terminal event was ever persisted, so without reconciliation the block
    // would render "running" forever.
    const messages = [uiMsg('launch-del_1', 'subagent_launch', { step_id: 'del_1', description: 'do work' })]

    // Before reconciliation the block is (misleadingly) running.
    expect(subagentStatus(messages, 'del_1')).toBe('running')

    // The durable snapshot from the restart settles it as interrupted.
    reconcileWorkUnits(SESSION, [{ step_id: 'del_1', kind: 'subagent', status: 'interrupted' }])
    expect(subagentStatus(messages, 'del_1')).toBe('interrupted')
  })

  it('renders a paused delegate as paused', () => {
    const messages = [uiMsg('launch-del_2', 'subagent_launch', { step_id: 'del_2', description: 'pausable' })]
    reconcileWorkUnits(SESSION, [{ step_id: 'del_2', status: 'paused' }])
    expect(subagentStatus(messages, 'del_2')).toBe('paused')
  })

  it('never downgrades a message-derived terminal status', () => {
    // A terminal event DID persist: the snapshot must not resurrect it.
    const messages = [
      uiMsg('launch-del_3', 'subagent_launch', { step_id: 'del_3', description: 'done' }),
      uiMsg('complete-del_3', 'subagent_complete', { step_id: 'del_3', success: true, duration: 5 }),
    ]
    reconcileWorkUnits(SESSION, [{ step_id: 'del_3', status: 'running' }])
    expect(subagentStatus(messages, 'del_3')).toBe('completed')
  })

  it('applies to plan-step blocks too', () => {
    const messages = [
      uiMsg('plan-1', 'plan', { steps: [{ id: 'step_1', description: 'step one' }] }),
      uiMsg('start-step_1', 'plan_step_start', { step_id: 'step_1', description: 'step one' }),
    ]
    reconcileWorkUnits(SESSION, [{ step_id: 'step_1', kind: 'plan_step', status: 'interrupted' }])
    expect(subagentStatus(messages, 'step_1')).toBe('interrupted')
  })

  it('leaves blocks untouched when there is no overlay entry', () => {
    const messages = [uiMsg('launch-del_4', 'subagent_launch', { step_id: 'del_4', description: 'x' })]
    reconcileWorkUnits(SESSION, [{ step_id: 'other', status: 'interrupted' }])
    expect(subagentStatus(messages, 'del_4')).toBe('running')
  })

  it('never overrides a message-derived paused block', () => {
    // A cooperatively paused subagent: the pause event set the block 'paused',
    // while a stale snapshot entry (seeded from an active load) still says
    // running. The overlay must not resurrect the spinner on a resumable block.
    const messages = [
      uiMsg('launch-del_p', 'subagent_launch', { step_id: 'del_p', description: 'pausable' }),
      uiMsg('pause-del_p', 'subagent_paused', { step_id: 'del_p' }),
    ]
    reconcileWorkUnits(SESSION, [{ step_id: 'del_p', status: 'running' }])
    expect(subagentStatus(messages, 'del_p')).toBe('paused')
  })
})

describe('live work-unit settlement', () => {
  beforeEach(resetStore)

  it('applyWorkUnitSettled records a settled unit in the overlay', () => {
    applyWorkUnitSettled(SESSION, 'del_5', 'interrupted')
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_5: 'interrupted' })

    // A second, valid settlement merges without dropping the first.
    applyWorkUnitSettled(SESSION, 'del_6', 'paused')
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_5: 'interrupted', del_6: 'paused' })

    // Malformed statuses are ignored.
    applyWorkUnitSettled(SESSION, 'del_7', 'bogus')
    expect(useChatStore.getState().workUnitStatus[SESSION]?.del_7).toBeUndefined()
  })

  it('clearWorkUnitStep drops one entry so a resumed block stops rendering interrupted', () => {
    reconcileWorkUnits(SESSION, [
      { step_id: 'del_8', status: 'interrupted' },
      { step_id: 'del_9', status: 'paused' },
    ])
    useChatStore.getState().clearWorkUnitStep(SESSION, 'del_8')
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_9: 'paused' })

    // A no-op for an unknown step / session.
    useChatStore.getState().clearWorkUnitStep(SESSION, 'missing')
    useChatStore.getState().clearWorkUnitStep('other-session', 'del_9')
    expect(useChatStore.getState().workUnitStatus[SESSION]).toEqual({ del_9: 'paused' })
  })
})
