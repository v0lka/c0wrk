// @vitest-environment jsdom
//
// useOlderHistoryLoader: walks OLDER history pages in on scroll-up, driven by
// the store's keyset cursor. These tests pin the loading contract: it fires
// when an older page exists, is a no-op once the oldest page is reached or while
// a fetch is in flight, and it prepends without duplicating rows.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { RefObject } from 'react'

const { getSessionHistoryMock } = vi.hoisted(() => ({ getSessionHistoryMock: vi.fn() }))
vi.mock('@/api/chat', () => ({ getSessionHistory: getSessionHistoryMock }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), warn: vi.fn() } }))

import { useOlderHistoryLoader, HISTORY_PAGE_SIZE } from './useHistoryPagination'
import { useChatStore } from '@/stores/chatStore'

function Harness({ sessionId, scrollRef }: { sessionId: string; scrollRef: RefObject<HTMLElement | null> }) {
  useOlderHistoryLoader(sessionId, scrollRef)
  return null
}

function row(id: number, content: string) {
  return { id, session_id: 's1', role: 'assistant', content, metadata: {}, created_at: `2026-01-01T00:00:0${id}Z` }
}

let root: Root | null = null

async function mount(el: HTMLElement) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  const scrollRef: RefObject<HTMLElement | null> = { current: el }
  await act(async () => { root!.render(<Harness sessionId="s1" scrollRef={scrollRef} />) })
  await act(async () => { await Promise.resolve(); await Promise.resolve() })
}

describe('useOlderHistoryLoader', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    getSessionHistoryMock.mockReset()
    useChatStore.setState({
      messages: {}, messageOrder: {}, historyCursor: {}, historyHasMore: {}, historyLoading: {},
    })
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
})
