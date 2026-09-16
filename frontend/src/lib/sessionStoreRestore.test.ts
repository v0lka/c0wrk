// Unit tests for the session store-state restore helpers used by the initial
// history load (ChatArea) and the older-page pagination path
// (useOlderHistoryLoader).
import { describe, it, expect, beforeEach } from 'vitest'
import { usePlanStore } from '@/stores/planStore'
import { useChatStore } from '@/stores/chatStore'
import {
  restorePlanAndGoalFromHistory,
  applyWorkUnitOverlayToPlan,
  restoreAgentMetricsIfAbsent,
} from './sessionStoreRestore'
import type { ChatMessageUI } from '@/types/messages'

function msg(id: string, type: ChatMessageUI['type'], metadata: Record<string, unknown> = {}): ChatMessageUI {
  return { id, sessionId: 's1', type, content: '', metadata, timestamp: 0 }
}

const METRICS_META = {
  finish: 'completed',
  parse_errors: 0,
  steps: 3,
  output_tokens: 100,
  nudges: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0 },
  aborts: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0 },
  model_profiles: { enabled: false, variants: [] },
}

describe('restorePlanAndGoalFromHistory', () => {
  beforeEach(() => {
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
    useChatStore.setState({ workUnitStatus: {}, workUnitEventAt: {} })
  })

  it('rebuilds the plan group from a plan declaration row', () => {
    const plan = msg('p1', 'plan', { steps: [{ id: 'step_1', description: 'd1' }, { id: 'step_2', description: 'd2' }] })
    restorePlanAndGoalFromHistory('s1', [plan])
    const groups = usePlanStore.getState().planGroups
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items.map((i) => i.id)).toEqual(['step_1', 'step_2'])
  })

  it('clears the panel when the accumulated history has no plan row (idempotent replay)', () => {
    usePlanStore.setState({ planGroups: [{ id: 'stale', items: [] }] })
    restorePlanAndGoalFromHistory('s1', [msg('a1', 'assistant')])
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })
})

describe('applyWorkUnitOverlayToPlan', () => {
  beforeEach(() => {
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
    useChatStore.setState({ workUnitStatus: {}, workUnitEventAt: {} })
  })

  it('settles a plan step the durable snapshot reports as interrupted', () => {
    usePlanStore.setState({
      planGroups: [{ id: 'g', items: [{ id: 'step_1', title: 't', status: 'running', dependsOn: [] }] }],
    })
    useChatStore.setState({ workUnitStatus: { s1: { step_1: 'interrupted' } } })

    applyWorkUnitOverlayToPlan('s1')

    expect(usePlanStore.getState().planGroups[0]!.items[0]!.status).toBe('interrupted')
  })

  it('is a no-op when the session has no snapshot', () => {
    usePlanStore.setState({
      planGroups: [{ id: 'g', items: [{ id: 'step_1', title: 't', status: 'running', dependsOn: [] }] }],
    })
    applyWorkUnitOverlayToPlan('s1')
    expect(usePlanStore.getState().planGroups[0]!.items[0]!.status).toBe('running')
  })
})

describe('restoreAgentMetricsIfAbsent', () => {
  beforeEach(() => {
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
  })

  it('restores the report when the store holds none', () => {
    restoreAgentMetricsIfAbsent('s1', [msg('am', 'status', METRICS_META)])
    expect(usePlanStore.getState().sessionStats['s1']?.lastAgentMetrics).toBeDefined()
  })

  it('does not overwrite an existing (newer) report', () => {
    restoreAgentMetricsIfAbsent('s1', [msg('am', 'status', METRICS_META)])
    const existing = usePlanStore.getState().sessionStats['s1']!.lastAgentMetrics

    restoreAgentMetricsIfAbsent('s1', [msg('am2', 'status', { ...METRICS_META, steps: 99 })])

    expect(usePlanStore.getState().sessionStats['s1']!.lastAgentMetrics).toBe(existing)
  })
})
