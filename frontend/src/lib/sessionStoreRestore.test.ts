// Unit tests for the session store-state restore helper used by the initial
// history load (ChatArea).
import { describe, it, expect, beforeEach } from 'vitest'
import { usePlanStore } from '@/stores/planStore'
import { useChatStore } from '@/stores/chatStore'
import { restorePlanAndGoalFromHistory } from './sessionStoreRestore'
import type { ChatMessageUI } from '@/types/messages'

function msg(id: string, type: ChatMessageUI['type'], metadata: Record<string, unknown> = {}): ChatMessageUI {
  return { id, sessionId: 's1', type, content: '', metadata, timestamp: 0 }
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
