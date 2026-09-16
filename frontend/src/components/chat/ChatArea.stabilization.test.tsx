// @vitest-environment jsdom
//
// ChatArea transcript-stability wiring.
//
// Verifies the two halves of the step-2/step-3 contract together:
//   1. Above the item threshold ChatArea renders the virtualized list (step-2
//      behaviour preserved); below it, the plain renderer.
//   2. The `items` handed to the list are STABILIZED: a store update that
//      changes exactly one message keeps every other item's object identity
//      (`stabilizeDisplayItems` reuse) while the changed item is fresh. That
//      identity is what lets the memoized blocks skip re-rendering.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactElement } from 'react'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import type { SessionInfo } from '@/types/models'

const { messagesRef, EMPTY_WORK_UNITS, itemsCaptured, chatStoreFns, chatStoreState } = vi.hoisted(() => ({
  messagesRef: { current: [] as ChatMessageUI[] },
  EMPTY_WORK_UNITS: {} as Record<string, never>,
  itemsCaptured: [] as DisplayItem[][],
  chatStoreFns: {
    addMessage: vi.fn(),
    mergeHistoryMessages: vi.fn(),
    setHistoryPageMeta: vi.fn(),
    setHistoryLoading: vi.fn(),
    setTaskActive: vi.fn(),
  },
  // Paging bookkeeping maps the real hook reads off the store snapshot.
  chatStoreState: { historyCursor: {}, historyHasMore: {}, historyLoading: {}, workUnitStatus: {} },
}))

vi.mock('@/components/MarkdownViewer', () => ({
  MarkdownViewer: ({ content }: { content: string }) => <div data-md={content} />,
}))

vi.mock('./VirtualizedChatList', () => ({
  VirtualizedChatList: ({ items }: { items: DisplayItem[] }) => {
    itemsCaptured.push(items)
    return <div data-testid="vlist" />
  },
}))

vi.mock('@/stores/chatStore', () => ({
  useChatStore: Object.assign(() => undefined, {
    getState: () => ({ ...chatStoreFns, ...chatStoreState }),
  }),
  useSessionMessages: () => messagesRef.current,
  useSessionWorkUnits: () => EMPTY_WORK_UNITS,
  selectSessionMessages: () => messagesRef.current,
}))

vi.mock('@/stores/sessionStore', async () => {
  const { create } = await import('zustand')
  return {
    useSessionStore: create<{
      sessions: SessionInfo[] | null
      activeSessionId: string | null
    }>(() => ({ sessions: null, activeSessionId: 's1' })),
  }
})

vi.mock('@/stores/planStore', () => ({
  usePlanStore: Object.assign(() => null, {
    getState: () => ({
      clearPlan: vi.fn(),
      setSessionStats: vi.fn(),
      applyWorkUnitStatuses: vi.fn(),
    }),
  }),
}))

vi.mock('@/api/chat', () => ({
  getSessionHistory: vi.fn().mockResolvedValue({ messages: [], next_cursor: '', has_more: false }),
  getSessionRuntimeStatus: vi.fn().mockResolvedValue(null),
  getPendingActions: vi.fn().mockResolvedValue(null),
  resolveStalePrompt: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('@/api/sessions', () => ({ archiveSession: vi.fn().mockResolvedValue(undefined) }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), debug: vi.fn() } }))

vi.mock('./ChatInput', () => ({ ChatInput: () => <div data-testid="chat-input-stub" /> }))
vi.mock('./ExecutionPanels', () => ({ ExecutionPanels: () => null }))
vi.mock('./BlackboardPanel', () => ({ BlackboardPanel: () => null }))
vi.mock('./BookmarksPanel', () => ({ BookmarksPanel: () => null }))

import { ChatArea } from './ChatArea'
import { useSessionStore } from '@/stores/sessionStore'

function session(over: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 's1',
    project_id: '',
    name: 'Session',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...over,
  }
}

function makeMessages(n: number): ChatMessageUI[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `m${i}`,
    sessionId: 's1',
    type: 'assistant' as const,
    content: `message ${i}`,
    metadata: {},
    timestamp: i,
  }))
}

const forceRender: { current: () => void } = { current: () => {} }

function Harness(): ReactElement {
  const [, setTick] = useState(0)
  forceRender.current = () => setTick((t) => t + 1)
  return <ChatArea />
}

let root: Root | null = null
let container: HTMLDivElement

function render(node: ReactElement): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(node)
  })
}

async function flushEffects(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('ChatArea transcript stability', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    messagesRef.current = []
    itemsCaptured.length = 0
    for (const fn of Object.values(chatStoreFns)) fn.mockClear()
    useSessionStore.setState({ sessions: [session()], activeSessionId: 's1' })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('virtualizes a long transcript above the threshold', async () => {
    messagesRef.current = makeMessages(61)
    render(<Harness />)
    await flushEffects()

    expect(container.querySelector('[data-testid="vlist"]')).not.toBeNull()
    expect(itemsCaptured.length).toBeGreaterThan(0)
    expect(itemsCaptured[itemsCaptured.length - 1]).toHaveLength(61)
  })

  it('does not virtualize a short transcript (step-2 threshold preserved)', async () => {
    messagesRef.current = makeMessages(3)
    render(<Harness />)
    await flushEffects()

    expect(container.querySelector('[data-testid="vlist"]')).toBeNull()
    // The plain renderer rendered the messages instead.
    expect(container.querySelectorAll('[data-md]')).toHaveLength(3)
  })

  it('stabilizes the item tree: changing one message keeps the others identity-stable', async () => {
    messagesRef.current = makeMessages(61)
    render(<Harness />)
    await flushEffects()

    const first = itemsCaptured[itemsCaptured.length - 1]!
    expect(first).toHaveLength(61)

    // Replace exactly one message (a new object), keeping the rest identical.
    const next = messagesRef.current.slice()
    next[5] = { ...next[5]!, content: 'message 5 edited' }
    messagesRef.current = next
    act(() => {
      forceRender.current()
    })

    const second = itemsCaptured[itemsCaptured.length - 1]!
    expect(second).toHaveLength(61)
    // Unchanged neighbours keep their object identity (memo can bail out)…
    expect(second[3]).toBe(first[3])
    expect(second[6]).toBe(first[6])
    expect(second[60]).toBe(first[60])
    // …while the changed item is a fresh object.
    expect(second[5]).not.toBe(first[5])
  })

  it('resets the session paging bookkeeping before loading the newest page (finding #2b)', async () => {
    messagesRef.current = makeMessages(3)
    render(<Harness />)
    await flushEffects()

    // A cursor/hasMore left over from an earlier visit must not be reused, and
    // an in-flight flag left set must not block older-page loading.
    expect(chatStoreFns.setHistoryPageMeta).toHaveBeenCalledWith('s1', '', false)
    expect(chatStoreFns.setHistoryLoading).toHaveBeenCalledWith('s1', false)
  })
})
