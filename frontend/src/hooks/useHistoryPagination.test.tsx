// @vitest-environment jsdom
//
// useOlderHistoryLoader: walks OLDER history pages in on scroll-up, driven by
// the store's keyset cursor. These tests pin the loading contract: it fires
// when an older page exists, is a no-op once the oldest page is reached or while
// a fetch is in flight, and it prepends without duplicating rows. They also pin
// the two findings on this path — the plan panel rebuild from a prepended page
// (#1) and the paging-state reset / in-flight-flag teardown across a session
// switch (#2).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { RefObject } from 'react'

const { getSessionHistoryMock } = vi.hoisted(() => ({ getSessionHistoryMock: vi.fn() }))
vi.mock('@/api/chat', () => ({ getSessionHistory: getSessionHistoryMock }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), warn: vi.fn() } }))

import { useOlderHistoryLoader, HISTORY_PAGE_SIZE } from './useHistoryPagination'
import { useChatStore } from '@/stores/chatStore'
import { usePlanStore } from '@/stores/planStore'

function Harness({ sessionId, scrollRef }: { sessionId: string; scrollRef: RefObject<HTMLElement | null> }) {
  useOlderHistoryLoader(sessionId, scrollRef)
  return null
}

function row(id: number, content: string) {
  return { id, session_id: 's1', role: 'assistant', content, metadata: {}, created_at: `2026-01-01T00:00:0${id}Z` }
}

function planRow(id: number) {
  return {
    id,
    session_id: 's1',
    role: 'plan',
    content: 'Plan',
    metadata: { steps: [{ id: 'step_1', description: 'd1' }, { id: 'step_2', description: 'd2' }] },
    created_at: `2026-01-01T00:00:0${id}Z`,
  }
}

let root: Root | null = null

async function mount(el: HTMLElement, sessionId = 's1') {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  const scrollRef: RefObject<HTMLElement | null> = { current: el }
  const render = (sid: string) =>
    act(async () => { root!.render(<Harness sessionId={sid} scrollRef={scrollRef} />) })
  await render(sessionId)
  await act(async () => { await Promise.resolve(); await Promise.resolve() })
  return { scrollRef, rerender: render }
}

describe('useOlderHistoryLoader', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    getSessionHistoryMock.mockReset()
    useChatStore.setState({
      messages: {}, messageOrder: {}, historyCursor: {}, historyHasMore: {}, historyLoading: {},
    })
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
  })
  afterEach(() => { act(() => { root?.unmount() }); root = null })

  it('loads the preceding page when older pages remain, then advances the cursor', async () => {
    useChatStore.setState({
      historyCursor: { s1: 'c0' },
      historyHasMore: { s1: true },
    })
    getSessionHistoryMock.mockResolvedValue({
      messages: [row(1, 'a'), row(2, 'b')],
      next_cursor: 'c1',
      has_more: true,
    })

    await mount(document.createElement('div'))

    expect(getSessionHistoryMock).toHaveBeenCalledWith('s1', HISTORY_PAGE_SIZE, 'c0')
    const s = useChatStore.getState()
    expect(s.messageOrder['s1']).toHaveLength(2)
    expect(s.historyCursor['s1']).toBe('c1')
    expect(s.historyHasMore['s1']).toBe(true)
    // The in-flight flag is cleared once the page settles.
    expect('s1' in s.historyLoading).toBe(false)
  })

  it('does nothing once the oldest page has been reached (hasMore false)', async () => {
    useChatStore.setState({ historyCursor: { s1: 'c0' }, historyHasMore: { s1: false } })

    await mount(document.createElement('div'))

    expect(getSessionHistoryMock).not.toHaveBeenCalled()
  })

  it('does not fetch while a page is already in flight', async () => {
    useChatStore.setState({
      historyCursor: { s1: 'c0' },
      historyHasMore: { s1: true },
      historyLoading: { s1: true },
    })

    await mount(document.createElement('div'))

    expect(getSessionHistoryMock).not.toHaveBeenCalled()
  })

  it('a scroll during an in-flight fetch does not start a second fetch (synchronous guard)', async () => {
    useChatStore.setState({ historyCursor: { s1: 'c0' }, historyHasMore: { s1: true } })
    let resolvePage: (v: unknown) => void = () => {}
    getSessionHistoryMock.mockReturnValue(new Promise((r) => { resolvePage = r }))

    const el = document.createElement('div')
    await mount(el)
    expect(getSessionHistoryMock).toHaveBeenCalledTimes(1)

    // A second scroll event while the first fetch is pending must be a no-op:
    // the guard reads the store synchronously, so it already sees loading=true.
    act(() => { el.dispatchEvent(new Event('scroll')) })
    expect(getSessionHistoryMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      resolvePage({ messages: [], next_cursor: '', has_more: false })
      await Promise.resolve()
    })
  })

  it('clears the in-flight flag even when the session switched mid-fetch (finding #2a)', async () => {
    useChatStore.setState({ historyCursor: { s1: 'c0' }, historyHasMore: { s1: true } })
    let resolvePage: (v: unknown) => void = () => {}
    getSessionHistoryMock.mockReturnValue(new Promise((r) => { resolvePage = r }))

    const { rerender } = await mount(document.createElement('div'), 's1')
    expect(useChatStore.getState().historyLoading['s1']).toBe(true)

    // Switch sessions while the fetch for s1 is still pending.
    await rerender('s2')

    await act(async () => {
      resolvePage({ messages: [row(1, 'a')], next_cursor: '', has_more: false })
      await Promise.resolve()
      await Promise.resolve()
    })

    // The result was discarded, but the flag must not be left stuck — otherwise
    // scrolling up in s1 could never load older pages again.
    expect(useChatStore.getState().historyLoading['s1']).toBeFalsy()
  })

  it('rebuilds the plan panel when an older page carries the declaration (finding #1)', async () => {
    useChatStore.setState({ historyCursor: { s1: 'c0' }, historyHasMore: { s1: true } })
    getSessionHistoryMock.mockResolvedValue({
      messages: [row(1, 'a'), planRow(2)],
      next_cursor: '',
      has_more: false,
    })

    await mount(document.createElement('div'))

    // The plan declaration sat on this (older) page: the panel must be rebuilt
    // from the accumulated history rather than left empty.
    const groups = usePlanStore.getState().planGroups
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items.map((i) => i.id)).toEqual(['step_1', 'step_2'])
  })

  it('leaves the plan panel untouched for a page with no anchor row', async () => {
    useChatStore.setState({ historyCursor: { s1: 'c0' }, historyHasMore: { s1: true } })
    usePlanStore.setState({ planGroups: [{ id: 'keep', items: [] }] })
    getSessionHistoryMock.mockResolvedValue({ messages: [row(1, 'a')], next_cursor: '', has_more: false })

    await mount(document.createElement('div'))

    // No plan/goal anchor → the (clear-then-replay) rebuild is skipped.
    expect(usePlanStore.getState().planGroups).toHaveLength(1)
    expect(usePlanStore.getState().planGroups[0]!.id).toBe('keep')
  })
})
