import { describe, it, expect, beforeEach } from 'vitest'
import { useChatStore, type ChatMessageUI } from '@/stores/chatStore'

function ui(id: string, content = id, timestamp = 0): ChatMessageUI {
  return { id, sessionId: 's1', type: 'assistant', content, metadata: {}, timestamp }
}

describe('chatStore mergeHistoryMessages', () => {
  beforeEach(() => {
    useChatStore.setState({ messages: {}, messageOrder: {} })
  })

  it('loads the history into an empty store in order', () => {
    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 0)

    const s = useChatStore.getState()
    expect(s.messageOrder['s1']).toEqual(['c', 'd'])
    expect(s.messages['s1']!['c']).toBeDefined()
  })

  it('keeps a live row newer than the load and appends it after the history', () => {
    useChatStore.getState().addMessage('s1', ui('live-new', 'live-new', 5000))

    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 4000)

    expect(useChatStore.getState().messageOrder['s1']).toEqual(['c', 'd', 'live-new'])
  })

  it('drops a stale live row older than the load', () => {
    useChatStore.getState().addMessage('s1', ui('stale', 'stale', 100))

    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 1000)

    expect(useChatStore.getState().messageOrder['s1']).toEqual(['c', 'd'])
  })

  it('preserves an unresolved HITL prompt even when it predates the load', () => {
    useChatStore.getState().addMessage('s1', {
      id: 'pending',
      sessionId: 's1',
      type: 'tool_confirm',
      content: 'confirm?',
      metadata: {},
      timestamp: 100,
    })

    useChatStore.getState().mergeHistoryMessages('s1', [ui('c')], 1000)

    expect(useChatStore.getState().messageOrder['s1']).toEqual(['c', 'pending'])
  })

  it('does not duplicate a live row the loaded history already carries', () => {
    useChatStore.getState().addMessage('s1', ui('c', 'c', 5000))

    useChatStore.getState().mergeHistoryMessages('s1', [ui('c'), ui('d')], 4000)

    expect(useChatStore.getState().messageOrder['s1']).toEqual(['c', 'd'])
  })
})
