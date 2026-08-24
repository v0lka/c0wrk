import { describe, it, expect, beforeEach } from 'vitest'
import { handlePlanGenerated } from '@/hooks/events/usePlanEvents'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import type { PlanData } from '@/types/events'

function makeData(overrides: Partial<PlanData> = {}): PlanData {
  return { step_count: 1, ...overrides }
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
