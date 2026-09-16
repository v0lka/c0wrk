import { describe, it, expect, beforeEach } from 'vitest'
import { useChatStore, type ChatMessageUI } from '@/stores/chatStore'

function ui(id: string, content = id): ChatMessageUI {
  return { id, sessionId: 's1', type: 'assistant', content, metadata: {}, timestamp: 0 }
}

describe('chatStore history pagination', () => {
  beforeEach(() => {
    useChatStore.setState({
      messages: {}, messageOrder: {},
      historyCursor: {}, historyHasMore: {}, historyLoading: {},
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
