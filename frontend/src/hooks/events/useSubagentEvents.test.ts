import { describe, it, expect, beforeEach } from 'vitest'
import { handleSubAgentPaused } from '@/hooks/events/useSubagentEvents'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import type { SubAgentPausedData } from '@/types/events'
import type { PlanGroup } from '@/types/models'

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

describe('handleSubAgentPaused', () => {
  it('adds a durable chat message without touching the plan store', () => {
    seedPlan([{ id: 'step_1', title: 'Plan step', status: 'running', dependsOn: [] }])

    const data: SubAgentPausedData = { step_id: 'delegate-1', duration: 900 }
    handleSubAgentPaused('sess-1', data)

    // Delegated steps are not tracked in the plan panel.
    expect(usePlanStore.getState().planGroups[0]!.items[0]!.status).toBe('running')

    const order = useChatStore.getState().messageOrder['sess-1'] ?? []
    expect(order).toHaveLength(1)
    const msg = useChatStore.getState().messages['sess-1']![order[0]!]!
    expect(msg.type).toBe('subagent_paused')
    expect(msg.metadata).toEqual({ step_id: 'delegate-1', duration: 900 })
    expect(useChatStore.getState().activityStatus['sess-1']).toBe('Paused: sub-agent delegate-1')
  })
})
