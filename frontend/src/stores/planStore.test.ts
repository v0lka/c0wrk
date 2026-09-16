import { beforeEach, describe, expect, it } from 'vitest'
import { usePlanStore } from './planStore'
import type { AgentMetricsData } from '@/types/events'
import type { PlanGroup, PlanItem } from '@/types/models'

const metrics: AgentMetricsData = {
  finish: 'full',
  parse_errors: 1,
  nudges: { repeat: 1, same_tool: 0, fruitless: 0, parse: 1, truncation: 0 },
  aborts: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0, truncation: 0 },
  steps: 7,
  output_tokens: 1234,
  invalid_tool_calls: 0,
  model_profiles: { enabled: false, variants: [] },
}

describe('planStore sessionStats (agent_metrics)', () => {
  beforeEach(() => {
    usePlanStore.setState({ sessionStats: {} })
  })

  it('stores the agent_metrics report for the session', () => {
    usePlanStore.getState().setSessionStats('s1', { lastAgentMetrics: metrics })
    expect(usePlanStore.getState().sessionStats['s1']?.lastAgentMetrics).toEqual(metrics)
  })

  it('merges into existing routing stats without clobbering them', () => {
    usePlanStore.getState().setSessionStats('s1', { routingDomain: 'react', attemptCount: 2 })
    usePlanStore.getState().setSessionStats('s1', { lastAgentMetrics: metrics })
    const s = usePlanStore.getState().sessionStats['s1']
    expect(s?.routingDomain).toBe('react')
    expect(s?.attemptCount).toBe(2)
    expect(s?.lastAgentMetrics).toEqual(metrics)
  })

  it('replaces a previous report on the next task run', () => {
    usePlanStore.getState().setSessionStats('s1', { lastAgentMetrics: metrics })
    const next: AgentMetricsData = { ...metrics, finish: 'failed', steps: 2 }
    usePlanStore.getState().setSessionStats('s1', { lastAgentMetrics: next })
    expect(usePlanStore.getState().sessionStats['s1']?.lastAgentMetrics).toEqual(next)
  })

  it('keeps sessions isolated', () => {
    usePlanStore.getState().setSessionStats('s1', { lastAgentMetrics: metrics })
    usePlanStore.getState().setSessionStats('s2', { routingDomain: 'plan' })
    expect(usePlanStore.getState().sessionStats['s2']?.lastAgentMetrics).toBeUndefined()
  })
})

describe('planStore applyWorkUnitStatuses (durable work-unit reconcile)', () => {
  const plan = (statuses: PlanItem['status'][]): PlanGroup => ({
    id: 'plan-1',
    items: statuses.map((status, i) => ({
      id: `step_${i + 1}`,
      title: `step ${i + 1}`,
      description: `d${i + 1}`,
      status,
      dependsOn: [] as string[],
    })),
    completedCount: 0,
    failedCount: 0,
    totalCount: statuses.length,
  })

  beforeEach(() => {
    usePlanStore.setState({ planGroups: [] })
  })

  it('corrects a still-running step the ledger settled as interrupted', () => {
    usePlanStore.getState().setPlan(plan(['completed', 'running']))
    usePlanStore.getState().applyWorkUnitStatuses({ step_2: 'interrupted' })
    const items = usePlanStore.getState().planGroups[0]!.items
    expect(items[0]!.status).toBe('completed')
    expect(items[1]!.status).toBe('interrupted')
    // Counts stay derived from the corrected items.
    expect(usePlanStore.getState().planGroups[0]!.completedCount).toBe(1)
  })

  it('never regresses a step that is not running', () => {
    usePlanStore.getState().setPlan(plan(['completed', 'paused', 'failed']))
    usePlanStore.getState().applyWorkUnitStatuses({
      step_1: 'interrupted', step_2: 'running', step_3: 'interrupted',
    })
    const items = usePlanStore.getState().planGroups[0]!.items
    expect(items.map((i) => i.status)).toEqual(['completed', 'paused', 'failed'])
  })

  it('is a no-op (stable reference) when nothing changes', () => {
    usePlanStore.getState().setPlan(plan(['running']))
    const before = usePlanStore.getState().planGroups
    usePlanStore.getState().applyWorkUnitStatuses({})
    expect(usePlanStore.getState().planGroups).toBe(before)
  })
})
