import { describe, it, expect, beforeEach } from 'vitest'
import { handlePlanGenerated, handlePlanStepPaused } from '@/hooks/events/usePlanEvents'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import type { PlanData, PlanStepPausedData } from '@/types/events'
import type { PlanGroup } from '@/types/models'

function makeData(overrides: Partial<PlanData> = {}): PlanData {
  return { step_count: 1, ...overrides }
}

function seedPlan(items: PlanGroup['items']): void {
  usePlanStore.setState({
    planGroups: [{ id: 'g1', items, completedCount: 0, failedCount: 0, totalCount: items.length }],
  })
}

beforeEach(() => {
  useChatStore.setState({
    messages: {},
    messageOrder: {},
    streamingText: {},
    activityStatus: {},
    taskActive: {},
    paused: {},
    pausing: {},
    stepContextFill: {},
    runtimeEventAt: {},
  })
  usePlanStore.setState({ planGroups: [] })
})

describe('handlePlanGenerated', () => {
  it('drops the previous plan step fills for that session only', () => {
    // Plan step ids (step_1, ...) are reused by every new plan in a session;
    // without invalidation a re-executed step briefly shows the previous
    // plan's fill badge until its own context_fill arrives.
    useChatStore.setState({
      stepContextFill: {
        'sess-1': { step_1: 77, step_2: 12 },
        'sess-2': { step_1: 40 },
      },
    })
    handlePlanGenerated('sess-1', makeData())
    expect(useChatStore.getState().stepContextFill).toEqual({ 'sess-2': { step_1: 40 } })
  })

  it('is a no-op for fills when the session has none recorded', () => {
    useChatStore.setState({ stepContextFill: { 'sess-2': { step_1: 40 } } })
    handlePlanGenerated('sess-1', makeData())
    expect(useChatStore.getState().stepContextFill).toEqual({ 'sess-2': { step_1: 40 } })
  })

  it('sets the activity label and adds a plan message even without steps', () => {
    // A steps-less payload (validation requires only step_count) must not
    // skip the label/message side effects.
    handlePlanGenerated('sess-1', makeData())
    expect(useChatStore.getState().activityStatus['sess-1']).toBe('Executing plan...')
    const order = useChatStore.getState().messageOrder['sess-1'] ?? []
    expect(order).toHaveLength(1)
    expect(useChatStore.getState().messages['sess-1']![order[0]!]!.type).toBe('plan')
  })
})

describe('handlePlanStepPaused', () => {
  it('flips only the paused step to paused and adds a durable chat message', () => {
    seedPlan([
      { id: 'step_1', title: 'Setup', status: 'completed', dependsOn: [] },
      { id: 'step_2', title: 'Implement', status: 'running', dependsOn: ['step_1'] },
      { id: 'step_3', title: 'Test', status: 'pending', dependsOn: ['step_2'] },
    ])
    const data: PlanStepPausedData = { step_id: 'step_2', duration: 4200 }

    handlePlanStepPaused('sess-1', data)

    const items = usePlanStore.getState().planGroups[0]!.items
    expect(items[0]!.status).toBe('completed')
    expect(items[1]!.status).toBe('paused')
    expect(items[2]!.status).toBe('pending') // untouched steps stay pending

    const order = useChatStore.getState().messageOrder['sess-1'] ?? []
    expect(order).toHaveLength(1)
    const msg = useChatStore.getState().messages['sess-1']![order[0]!]!
    expect(msg.type).toBe('plan_step_paused')
    expect(msg.metadata).toMatchObject({ step_id: 'step_2', duration: 4200 })
    expect(useChatStore.getState().activityStatus['sess-1']).toBe('Paused at step step_2')
  })

  it('keeps plan completion counters untouched (a pause is not a completion)', () => {
    seedPlan([
      { id: 'step_1', title: 'Setup', status: 'completed', dependsOn: [] },
      { id: 'step_2', title: 'Implement', status: 'running', dependsOn: [] },
    ])
    usePlanStore.setState((s) => ({ planGroups: [{ ...s.planGroups[0]!, completedCount: 1 }] }))

    handlePlanStepPaused('sess-1', { step_id: 'step_2', duration: 100 })

    const group = usePlanStore.getState().planGroups[0]!
    expect(group.completedCount).toBe(1)
    expect(group.failedCount).toBe(0)
  })

  it('carries the optional error through the message metadata', () => {
    seedPlan([{ id: 'step_1', title: 'Work', status: 'running', dependsOn: [] }])

    handlePlanStepPaused('sess-1', { step_id: 'step_1', duration: 5, error: 'user pause' })

    const order = useChatStore.getState().messageOrder['sess-1'] ?? []
    const msg = useChatStore.getState().messages['sess-1']![order[0]!]!
    expect(msg.metadata?.error).toBe('user pause')
  })
})
