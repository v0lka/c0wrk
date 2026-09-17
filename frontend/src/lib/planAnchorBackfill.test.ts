// Unit tests for the plan-anchor backfill helper (ensurePlanAnchorLoaded).
//
// Pins the contract of the mid-execution reload fix: when the durable
// work-unit snapshot proves the session's unfinished task is plan-driven but
// the accumulated messages hold no plan declaration row, the helper walks
// older pages backwards (via the store's keyset cursor) prepending each until
// the declaration is in the store, then rebuilds the plan panel and re-applies
// the work-unit overlay. Everything else — no snapshot, non-plan-driven units,
// anchor already loaded, no older pages, aborted walk, page cap — must issue
// NO history RPC (or stop early) and leave the panel untouched.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const { getSessionHistoryMock } = vi.hoisted(() => ({ getSessionHistoryMock: vi.fn() }))
vi.mock('@/api/chat', () => ({ getSessionHistory: getSessionHistoryMock }))

import { ensurePlanAnchorLoaded, PLAN_ANCHOR_MAX_PAGES } from './planAnchorBackfill'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { chatMessageToUI } from '@/lib/chatUtils'
import type { ChatMessageUI } from '@/types/messages'
import type { ChatMessage } from '@/types/models'
import type { WorkUnitSnapshot } from '@/api/chat'

const SID = 's1'
const PLAN_UNITS: WorkUnitSnapshot[] = [{ step_id: 'step_1', kind: 'plan_step', status: 'paused' }]

function uiMsg(id: string, type: ChatMessageUI['type'], metadata: Record<string, unknown> = {}): ChatMessageUI {
  return { id, sessionId: SID, type, content: '', metadata, timestamp: 0 }
}

function row(id: number, role: string, metadata: Record<string, unknown> = {}): ChatMessage {
  return {
    id,
    session_id: SID,
    role,
    content: '',
    // Plain-object metadata rides chatMessageToUI's test-only path (the type
    // is the Wails byte-array form; the converter also accepts objects).
    metadata: metadata as unknown as ChatMessage['metadata'],
    // Distinct per-row timestamps: buildHistoryId derives semantic ids from
    // them (e.g. `plan-<ts>`), so equal timestamps would collapse distinct
    // rows into one deduped id.
    created_at: `2026-01-01T00:${String(Math.floor(id / 60)).padStart(2, '0')}:${String(id % 60).padStart(2, '0')}Z`,
  }
}

function planRow(id: number): ChatMessage {
  return row(id, 'plan', { steps: [{ id: 'step_1', description: 'd1' }, { id: 'step_2', description: 'd2' }] })
}

/** Mimic the store state ChatArea's load effect leaves behind: the newest
 *  page merged into the store and the paging cursor/hasMore recorded. */
function seedNewestPage(msgs: ChatMessageUI[], cursor = 'c1', hasMore = true): void {
  const store = useChatStore.getState()
  store.setMessages(SID, msgs)
  store.setHistoryPageMeta(SID, cursor, hasMore)
}

async function flushMicrotasks(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('ensurePlanAnchorLoaded', () => {
  beforeEach(() => {
    getSessionHistoryMock.mockReset()
    useChatStore.setState({
      messages: {}, messageOrder: {},
      historyCursor: {}, historyHasMore: {}, historyLoading: {},
      prependedHistoryIds: {}, prependCursor: {}, prependHasMore: {},
      workUnitStatus: {}, workUnitEventAt: {},
    })
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
  })

  it('backfills older pages until the plan row, then rebuilds the panel with the overlay', async () => {
    // Newest page (already loaded by ChatArea): only step events, no plan row.
    seedNewestPage([
      uiMsg('n1', 'plan_step_start', { step_id: 'step_1' }),
      uiMsg('n2', 'assistant'),
    ])
    // reconcileWorkUnits stored the durable overlay before the backfill ran.
    useChatStore.setState({ workUnitStatus: { [SID]: { step_1: 'paused' } } })
    const older1 = [row(20, 'assistant'), row(21, 'user')]
    const older2 = [planRow(1), row(2, 'user')]
    getSessionHistoryMock
      .mockResolvedValueOnce({ messages: older1, next_cursor: 'c0', has_more: true })
      .mockResolvedValueOnce({ messages: older2, next_cursor: '', has_more: false })

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })

    expect(restored).toBe(true)
    expect(getSessionHistoryMock).toHaveBeenCalledTimes(2)
    expect(getSessionHistoryMock).toHaveBeenNthCalledWith(1, SID, 50, 'c1')
    expect(getSessionHistoryMock).toHaveBeenNthCalledWith(2, SID, 50, 'c0')

    // The panel is rebuilt from the accumulated history (anchor + replay of
    // the plan_step_start above it), and the durable overlay settles step_1.
    const groups = usePlanStore.getState().planGroups
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items.map(i => i.id)).toEqual(['step_1', 'step_2'])
    expect(groups[0]!.items.find(i => i.id === 'step_1')!.status).toBe('paused')

    // Both pages are prepended AHEAD of the previously loaded newest page,
    // the cursor rests at the deepest fetched page, and the in-flight flag
    // is cleared so scroll-up pagination can resume.
    const s = useChatStore.getState()
    const expectedOrder = [
      ...older2.map(m => chatMessageToUI(m).id),
      ...older1.map(m => chatMessageToUI(m).id),
      'n1', 'n2',
    ]
    expect(s.messageOrder[SID]).toEqual(expectedOrder)
    expect(s.historyCursor[SID]).toBe('')
    expect(s.historyHasMore[SID]).toBe(false)
    expect(SID in s.historyLoading).toBe(false)
  })

  it('walks for subagent work units too (plan-driven kinds)', async () => {
    seedNewestPage([uiMsg('n1', 'subagent_launch', { step_id: 'sa1' })])
    getSessionHistoryMock.mockResolvedValueOnce({
      messages: [planRow(1)],
      next_cursor: '',
      has_more: false,
    })

    const restored = await ensurePlanAnchorLoaded(SID, [{ step_id: 'sa1', kind: 'subagent', status: 'running' }], { pageSize: 50 })

    expect(restored).toBe(true)
    expect(getSessionHistoryMock).toHaveBeenCalledTimes(1)
    expect(usePlanStore.getState().planGroups).toHaveLength(1)
  })

  it('issues no RPC when work_units are absent, empty, or not plan-driven', async () => {
    seedNewestPage([uiMsg('n1', 'assistant')])

    expect(await ensurePlanAnchorLoaded(SID, undefined, { pageSize: 50 })).toBe(false)
    expect(await ensurePlanAnchorLoaded(SID, [], { pageSize: 50 })).toBe(false)
    expect(await ensurePlanAnchorLoaded(SID, [{ step_id: 'g1', kind: 'goal_verification', status: 'running' }], { pageSize: 50 })).toBe(false)

    expect(getSessionHistoryMock).not.toHaveBeenCalled()
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('issues no RPC when the newest page already carries the plan row and the panel is built', async () => {
    seedNewestPage([
      uiMsg('p1', 'plan', { steps: [{ id: 'step_1', description: 'd1' }] }),
      uiMsg('n1', 'plan_step_start', { step_id: 'step_1' }),
    ])
    usePlanStore.setState({ planGroups: [{ id: 'existing', items: [] }] })

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })

    expect(restored).toBe(false)
    expect(getSessionHistoryMock).not.toHaveBeenCalled()
    // The already-built panel is left exactly as it was.
    expect(usePlanStore.getState().planGroups[0]!.id).toBe('existing')
  })

  it('rebuilds the panel locally (still no RPC) when the anchor row is in the store but the panel is empty', async () => {
    // Re-visit scenario: an earlier backfill prepended the anchor page and the
    // merge kept those rows, but ChatArea's own restore only saw the newest
    // page — the panel is empty while the anchor row is accumulated.
    seedNewestPage([
      uiMsg('p0', 'plan', { steps: [{ id: 'step_1', description: 'd1' }, { id: 'step_2', description: 'd2' }] }),
      uiMsg('n1', 'plan_step_start', { step_id: 'step_1' }),
    ])
    useChatStore.setState({ workUnitStatus: { [SID]: { step_1: 'interrupted' } } })

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })

    expect(restored).toBe(true)
    expect(getSessionHistoryMock).not.toHaveBeenCalled()
    const groups = usePlanStore.getState().planGroups
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items.find(i => i.id === 'step_1')!.status).toBe('interrupted')
  })

  it('issues no RPC when no older pages remain (hasMore false)', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })], '', false)

    expect(await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })).toBe(false)
    expect(getSessionHistoryMock).not.toHaveBeenCalled()
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('stops at the page cap when no plan row is ever found', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])
    getSessionHistoryMock.mockImplementation(async (_sid: string, _limit: number, before: string) => ({
      messages: [row(Number(before.slice(1)) * 10, 'assistant')],
      next_cursor: `c${Number(before.slice(1)) + 1}`,
      has_more: true,
    }))

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })

    expect(restored).toBe(false)
    expect(getSessionHistoryMock).toHaveBeenCalledTimes(PLAN_ANCHOR_MAX_PAGES)
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
    // The walk still leaves consistent paging state behind.
    const s = useChatStore.getState()
    expect(s.historyCursor[SID]).toBe('c6')
    expect(SID in s.historyLoading).toBe(false)
  })

  it('aborts before any fetch when shouldAbort is already true', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, {
      pageSize: 50,
      shouldAbort: () => true,
    })

    expect(restored).toBe(false)
    expect(getSessionHistoryMock).not.toHaveBeenCalled()
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('discards an in-flight page (no prepend, no panel rebuild) when the session switches mid-walk', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])
    let resolvePage: (v: unknown) => void = () => {}
    getSessionHistoryMock.mockReturnValue(new Promise(r => { resolvePage = r }))

    let aborted = false
    const promise = ensurePlanAnchorLoaded(SID, PLAN_UNITS, {
      pageSize: 50,
      shouldAbort: () => aborted,
    })
    await flushMicrotasks()
    // The fetch is in flight when the user switches sessions.
    aborted = true
    resolvePage({ messages: [planRow(1)], next_cursor: '', has_more: false })
    const restored = await promise

    // The page carried the anchor, but the walk was aborted: nothing is
    // prepended (the cursor never advanced) and — crucially — the plan panel
    // of the now-active session is not clobbered with this session's plan.
    expect(restored).toBe(false)
    expect(useChatStore.getState().messageOrder[SID]).toEqual(['n1'])
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
    expect(SID in useChatStore.getState().historyLoading).toBe(false)
  })

  it('clears the in-flight flag and reports false when the history RPC fails', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])
    getSessionHistoryMock.mockRejectedValue(new Error('rpc down'))

    const restored = await ensurePlanAnchorLoaded(SID, PLAN_UNITS, { pageSize: 50 })

    expect(restored).toBe(false)
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
    expect(SID in useChatStore.getState().historyLoading).toBe(false)
  })
})
