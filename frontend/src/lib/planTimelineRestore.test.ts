// Unit tests for the plan-timeline restore helper (restorePlanFromTimeline).
//
// Pins the contract of the mid-execution reload fix: when the panel is empty
// after a history load (the plan declaration sits behind the paged window),
// ONE plan-timeline RPC returns the declaration plus every
// plan_step_start/complete/paused row; the helper dedupes those rows into the
// chat store (cursor untouched — no page was fetched) and rebuilds the panel
// with full statuses plus the durable work-unit overlay. Everything else —
// panel already built, empty timeline, aborted RPC, session switch mid-flight,
// RPC failure — must leave the store and the panel untouched.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const { getPlanTimelineMock } = vi.hoisted(() => ({ getPlanTimelineMock: vi.fn() }))
vi.mock('@/api/chat', () => ({ getSessionPlanTimeline: getPlanTimelineMock }))

import { restorePlanFromTimeline } from './planTimelineRestore'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'
import { chatMessageToUI } from '@/lib/chatUtils'
import type { ChatMessageUI } from '@/types/messages'
import type { ChatMessage } from '@/types/models'

const SID = 's1'

// Timestamp for seeded window rows: comfortably after every timeline row
// (row() ids map to 2026-01-01T00:00:NN), mirroring reality — the newest
// page postdates the plan declaration it is loaded against. prependHistory
// -Messages splices by created_at, so a zero timestamp would wrongly sort
// the window rows behind the timeline rows.
const WINDOW_TS = Date.parse('2026-01-02T00:00:00Z')

function uiMsg(id: string, type: ChatMessageUI['type'], metadata: Record<string, unknown> = {}): ChatMessageUI {
  return { id, sessionId: SID, type, content: '', metadata, timestamp: WINDOW_TS }
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

describe('restorePlanFromTimeline', () => {
  beforeEach(() => {
    getPlanTimelineMock.mockReset()
    useChatStore.setState({
      messages: {}, messageOrder: {},
      historyCursor: {}, historyHasMore: {}, historyLoading: {},
      prependedHistoryIds: {}, prependCursor: {}, prependHasMore: {},
      workUnitStatus: {}, workUnitEventAt: {},
    })
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
  })

  it('restores the panel and prepends the timeline rows without touching the paging cursor', async () => {
    // Newest page (already loaded by ChatArea): only the live tail, no plan
    // row — the declaration sits thousands of rows back.
    seedNewestPage([
      uiMsg('n1', 'plan_step_start', { step_id: 'step_2', description: 'work' }),
      uiMsg('n2', 'assistant'),
    ])
    useChatStore.setState({ workUnitStatus: { [SID]: { step_1: 'interrupted' } } })
    // The timeline: declaration + step_1 finished long before the window.
    const timeline = [
      planRow(1),
      row(2, 'plan_step_start', { step_id: 'step_1' }),
      row(3, 'plan_step_complete', { step_id: 'step_1', success: true, duration: 5 }),
    ]
    getPlanTimelineMock.mockResolvedValue(timeline)

    const restored = await restorePlanFromTimeline(SID)

    expect(restored).toBe(true)
    expect(getPlanTimelineMock).toHaveBeenCalledTimes(1)
    expect(getPlanTimelineMock).toHaveBeenCalledWith(SID)

    // The panel is rebuilt from the timeline: both steps present, step_1
    // settled to completed by its replayed complete row (NOT left pending
    // just because its rows are behind the window), step_2 running.
    const groups = usePlanStore.getState().planGroups
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items.map(i => i.id)).toEqual(['step_1', 'step_2'])
    expect(groups[0]!.items.find(i => i.id === 'step_1')!.status).toBe('completed')
    expect(groups[0]!.items.find(i => i.id === 'step_2')!.status).toBe('running')

    // The timeline rows are deduped into the store AHEAD of the window (the
    // window's own step_2 start keeps its position via the id dedupe), and
    // the paging cursor/hasMore are untouched — no page was fetched.
    const s = useChatStore.getState()
    const expectedOrder = [
      chatMessageToUI(planRow(1)).id,
      chatMessageToUI(row(2, 'plan_step_start', { step_id: 'step_1' })).id,
      chatMessageToUI(row(3, 'plan_step_complete', { step_id: 'step_1', success: true, duration: 5 })).id,
      'n1', 'n2',
    ]
    expect(s.messageOrder[SID]).toEqual(expectedOrder)
    expect(s.historyCursor[SID]).toBe('c1')
    expect(s.historyHasMore[SID]).toBe(true)
    expect(SID in s.historyLoading).toBe(false)
  })

  it('issues no RPC when the panel already holds a plan', async () => {
    usePlanStore.setState({ planGroups: [{ id: 'existing', items: [] }] })

    const restored = await restorePlanFromTimeline(SID)

    expect(restored).toBe(true)
    expect(getPlanTimelineMock).not.toHaveBeenCalled()
    // The already-built panel is left exactly as it was.
    expect(usePlanStore.getState().planGroups[0]!.id).toBe('existing')
  })

  it('does not clobber a panel that a live plan_generated built while the RPC was in flight', async () => {
    seedNewestPage([uiMsg('n1', 'assistant')])
    let resolveTimeline: (v: unknown) => void = () => {}
    getPlanTimelineMock.mockReturnValue(new Promise(r => { resolveTimeline = r }))

    const promise = restorePlanFromTimeline(SID)
    await flushMicrotasks()
    // A live plan_generated lands while the timeline RPC is in flight.
    usePlanStore.setState({ planGroups: [{ id: 'live', items: [] }] })
    resolveTimeline([planRow(1)])
    const restored = await promise

    expect(restored).toBe(true)
    expect(usePlanStore.getState().planGroups[0]!.id).toBe('live')
    // Nothing was inserted into the chat store either.
    expect(useChatStore.getState().messageOrder[SID]).toEqual(['n1'])
  })

  it('issues no store mutation when the timeline carries no plan row (plan-less session)', async () => {
    seedNewestPage([uiMsg('n1', 'assistant')])
    getPlanTimelineMock.mockResolvedValue([row(1, 'assistant'), row(2, 'tool_call', { tool: 'bash' })])

    const restored = await restorePlanFromTimeline(SID)

    expect(restored).toBe(false)
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
    expect(useChatStore.getState().messageOrder[SID]).toEqual(['n1'])
  })

  it('aborts before any fetch when shouldAbort is already true', async () => {
    const restored = await restorePlanFromTimeline(SID, { shouldAbort: () => true })

    expect(restored).toBe(false)
    expect(getPlanTimelineMock).not.toHaveBeenCalled()
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('discards an in-flight timeline (no prepend, no panel rebuild) when the session switches mid-RPC', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])
    let resolveTimeline: (v: unknown) => void = () => {}
    getPlanTimelineMock.mockReturnValue(new Promise(r => { resolveTimeline = r }))

    let aborted = false
    const promise = restorePlanFromTimeline(SID, { shouldAbort: () => aborted })
    await flushMicrotasks()
    // The RPC is in flight when the user switches sessions.
    aborted = true
    resolveTimeline([planRow(1)])
    const restored = await promise

    // The timeline carried the declaration, but the restore was aborted:
    // nothing is prepended and — crucially — the plan panel of the
    // now-active session is not clobbered with this session's plan.
    expect(restored).toBe(false)
    expect(useChatStore.getState().messageOrder[SID]).toEqual(['n1'])
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('reports false and leaves the panel empty when the timeline RPC fails', async () => {
    seedNewestPage([uiMsg('n1', 'plan_step_start', { step_id: 'step_1' })])
    getPlanTimelineMock.mockRejectedValue(new Error('rpc down'))

    const restored = await restorePlanFromTimeline(SID)

    expect(restored).toBe(false)
    expect(usePlanStore.getState().planGroups).toHaveLength(0)
  })

  it('settles a behind-the-window step via the durable work-unit overlay', async () => {
    seedNewestPage([uiMsg('n1', 'assistant')])
    // The ledger settled step_2 as interrupted (crash mid-step); the
    // timeline holds its start but no terminal row.
    useChatStore.setState({ workUnitStatus: { [SID]: { step_2: 'interrupted' } } })
    getPlanTimelineMock.mockResolvedValue([
      planRow(1),
      row(2, 'plan_step_start', { step_id: 'step_2' }),
    ])

    const restored = await restorePlanFromTimeline(SID)

    expect(restored).toBe(true)
    const groups = usePlanStore.getState().planGroups
    expect(groups[0]!.items.find(i => i.id === 'step_2')!.status).toBe('interrupted')
  })
})
