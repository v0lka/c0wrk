import { beforeEach, describe, expect, it } from 'vitest'
import { usePlanStore } from './planStore'
import type { AgentMetricsData } from '@/types/events'

const metrics: AgentMetricsData = {
  finish: 'full',
  parse_errors: 1,
  nudges: { repeat: 1, same_tool: 0, fruitless: 0, parse: 1 },
  aborts: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0 },
  steps: 7,
  output_tokens: 1234,
  small_llm: { enabled: false, variants: [] },
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
