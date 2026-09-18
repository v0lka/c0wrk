import { describe, it, expect, beforeEach } from 'vitest'
import { useChatStore, type ChatMessageUI } from '@/stores/chatStore'

function ui(id: string, content = id, timestamp = 0): ChatMessageUI {
  return { id, sessionId: 's1', type: 'assistant', content, metadata: {}, timestamp }
}

describe('chatStore history pagination', () => {
  beforeEach(() => {
    useChatStore.setState({
      messages: {}, messageOrder: {},
      historyCursor: {}, historyHasMore: {}, historyLoading: {},
      prependedHistoryIds: {}, prependCursor: {}, prependHasMore: {},
    })
  })

  it('prependHistoryMessages places older rows before existing ones and advances cursor/hasMore', () => {
    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 0)
    useChatStore.getState().setHistoryPageMeta('s1', 'c1', true)

    useChatStore.getState().prependHistoryMessages('s1', [ui('a'), ui('b')], 'c0', true)

    const s = useChatStore.getState()
    expect(s.messageOrder['s1']).toEqual(['a', 'b', 'c', 'd'])
    expect(s.messages['s1']!['a']).toBeDefined()
    expect(s.historyCursor['s1']).toBe('c0')
    expect(s.historyHasMore['s1']).toBe(true)
  })

  it('prependHistoryMessages is idempotent: rows already present are not duplicated', () => {
    useChatStore.getState().mergeHistoryMessages('s1', [ui('b'), ui('c')], 0)

    // A page overlapping the existing rows (b already known) must only add the new one.
    useChatStore.getState().prependHistoryMessages('s1', [ui('a'), ui('b')], 'c0', false)

    const s = useChatStore.getState()
    expect(s.messageOrder['s1']).toEqual(['a', 'b', 'c'])
    expect(s.historyHasMore['s1']).toBe(false)
  })

  it('prependHistoryMessages records the inserted ids and the deepest cursor position', () => {
    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 0)
    useChatStore.getState().prependHistoryMessages('s1', [ui('b')], 'c1', true)
    useChatStore.getState().prependHistoryMessages('s1', [ui('a')], 'c0', true)

    const s = useChatStore.getState()
    expect(s.prependedHistoryIds['s1']).toEqual(new Set(['a', 'b']))
    // The latest prepend IS the deepest position (paging moves backwards).
    expect(s.prependCursor['s1']).toBe('c0')
    expect(s.prependHasMore['s1']).toBe(true)
  })

  it('a newest-page re-load preserves prepended rows ahead of the page and keeps younger live rows', () => {
    // Initial visit: the newest page [c, d] loads, then the user scrolls up and
    // prepends two older pages (deepest cursor 'c0', older pages remain).
    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 0)
    useChatStore.getState().prependHistoryMessages('s1', [ui('b')], 'c1', true)
    useChatStore.getState().prependHistoryMessages('s1', [ui('a')], 'c0', true)

    // A live event lands while the re-load RPC is in flight (younger than the
    // new loadStartedAt) and must survive the merge.
    useChatStore.getState().addMessage('s1', ui('live-new', 'live-new', 5000))

    // Re-load the newest page (session switch back / effect re-run).
    const kept = useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 4000)

    // Both prepended rows were kept...
    expect(kept).toBe(2)
    const s = useChatStore.getState()
    // ...ahead of the newest page, with the live row after it, no duplicates.
    expect(s.messageOrder['s1']).toEqual(['a', 'b', 'c', 'd', 'live-new'])

    // The prepend bookkeeping still points at the deepest position; ChatArea
    // restores the paging cursor from it after the merge (mirrored here).
    expect(s.prependCursor['s1']).toBe('c0')
    expect(s.prependHasMore['s1']).toBe(true)
    useChatStore.getState().setHistoryPageMeta(
      's1', s.prependCursor['s1'] ?? '', s.prependHasMore['s1'] ?? false,
    )
    expect(useChatStore.getState().historyCursor['s1']).toBe('c0')
    expect(useChatStore.getState().historyHasMore['s1']).toBe(true)
  })

  it('a prepended row that reappears in the newest page is neither duplicated nor counted as kept', () => {
    useChatStore.getState().mergeHistoryMessages('s1', [ui('c')], 0)
    useChatStore.getState().prependHistoryMessages('s1', [ui('b')], 'c0', false)

    // The re-loaded page carries b itself (e.g. rows shifted into the newest
    // page after new messages arrived): it comes from the page, not from the
    // prepend memory, so nothing is "kept" and no duplicate renders.
    const kept = useChatStore.getState().mergeHistoryMessages('s1', [ui('b'), ui('c')], 4000)

    expect(kept).toBe(0)
    expect(useChatStore.getState().messageOrder['s1']).toEqual(['b', 'c'])
  })

  it('mergeHistoryMessages returns 0 and replaces stale rows when nothing was prepended', () => {
    // A stale live row older than loadStartedAt (and not an unresolved HITL
    // prompt) is dropped — the pre-retention behavior is unchanged.
    useChatStore.getState().addMessage('s1', ui('stale', 'stale', 100))
    const kept = useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 1000)

    expect(kept).toBe(0)
    expect(useChatStore.getState().messageOrder['s1']).toEqual(['c', 'd'])
  })

  // Regression (finding 23): the plan-timeline restore reuses
  // prependHistoryMessages with SESSION-WIDE rows — older than the newest
  // page but newer than pages scroll-up has not yet fetched. The next
  // scroll-up page must land BELOW those rows, keeping messageOrder
  // chronological for the rest of the run.
  it('prependHistoryMessages keeps messageOrder chronological for a plan-timeline restore followed by an older page', () => {
    // ChatArea's load effect merges the newest window (the live tail).
    useChatStore.getState().mergeHistoryMessages('s1', [ui('w1', 'w1', 900)], 0)
    useChatStore.getState().setHistoryPageMeta('s1', 'c9', true)

    // The plan-timeline restore prepends the declaration plus a step row:
    // older than the window, NEWER than the pages behind it.
    useChatStore.getState().prependHistoryMessages(
      's1', [ui('plan', 'plan', 100), ui('plan-step', 'plan-step', 200)], 'c9', true,
    )

    // The next scroll-up page fetches rows between the timeline rows and the
    // window (50 predates the declaration; 300/400 postdate the step row).
    useChatStore.getState().prependHistoryMessages(
      's1', [ui('mid1', 'mid1', 300), ui('old1', 'old1', 50), ui('mid2', 'mid2', 400)], 'c8', true,
    )

    const s = useChatStore.getState()
    expect(s.messageOrder['s1']).toEqual(['old1', 'plan', 'plan-step', 'mid1', 'mid2', 'w1'])
    // Every row's timestamp is non-decreasing along the order (chronological).
    const timestamps = s.messageOrder['s1']!.map(id => s.messages['s1']![id]!.timestamp)
    expect(timestamps).toEqual([...timestamps].sort((a, b) => a - b))
  })

  it('setHistoryPageMeta records the cursor and hasMore flag', () => {
    useChatStore.getState().setHistoryPageMeta('s1', 'cur-9', true)
    const s = useChatStore.getState()
    expect(s.historyCursor['s1']).toBe('cur-9')
    expect(s.historyHasMore['s1']).toBe(true)
  })

  it('setHistoryLoading sets then clears the in-flight flag (absent when idle)', () => {
    useChatStore.getState().setHistoryLoading('s1', true)
    expect(useChatStore.getState().historyLoading['s1']).toBe(true)

    useChatStore.getState().setHistoryLoading('s1', false)
    expect('s1' in useChatStore.getState().historyLoading).toBe(false)

    // Clearing an already-absent flag is a no-op that returns the same state.
    const before = useChatStore.getState().historyLoading
    useChatStore.getState().setHistoryLoading('s1', false)
    expect(useChatStore.getState().historyLoading).toBe(before)
  })
})
