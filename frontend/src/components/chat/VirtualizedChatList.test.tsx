// @vitest-environment jsdom
//
// VirtualizedChatList contract: it mounts ONLY the rows the virtualizer reports
// as visible (+overscan), not the whole item list. jsdom has no layout engine,
// so the real virtualizer would mount nothing; it is mocked to return a small
// WINDOW of a large list, which is exactly the DOM-bounding property we assert.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { DisplayItem } from '@/types/messages'

// A fixed-size window the mocked virtualizer always reports, independent of the
// (large) item count — mirrors what overscan gives at runtime.
const WINDOW = 8

const { scrollToIndexMock } = vi.hoisted(() => ({ scrollToIndexMock: vi.fn() }))

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: (options: { count: number; getItemKey: (i: number) => string | number }) => {
    const end = Math.min(WINDOW, options.count)
    const items = Array.from({ length: end }, (_, i) => ({
      index: i,
      key: options.getItemKey(i),
      start: i * 100,
      size: 100,
    }))
    return {
      getVirtualItems: () => items,
      getTotalSize: () => options.count * 100,
      measureElement: () => {},
      scrollToIndex: scrollToIndexMock,
    }
  },
}))

vi.mock('./ChatMessageRenderer', () => ({
  ChatItem: ({ item }: { item: DisplayItem }) => <div data-testid="chat-item">{item.kind}</div>,
}))

import { VirtualizedChatList } from './VirtualizedChatList'
import type { ChatVirtualizerHandle } from '@/lib/chatVirtualizer'

function makeItems(n: number): DisplayItem[] {
  return Array.from({ length: n }, (_, i) => ({
    kind: 'assistant' as const,
    message: { id: `m${i}`, sessionId: 's1', type: 'assistant' as const, content: `msg ${i}`, metadata: {}, timestamp: i },
  }))
}

let root: Root | null = null
let container: HTMLDivElement

function render(node: React.ReactElement): HTMLElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => { root!.render(node) })
  return container
}

describe('VirtualizedChatList', () => {
  beforeEach(() => { document.body.innerHTML = ''; root = null })
  afterEach(() => { act(() => { root?.unmount() }); root = null })

  it('mounts only the windowed rows — DOM size does not scale with history length', () => {
    const items = makeItems(1000)
    const c = render(<VirtualizedChatList items={items} scrollRef={{ current: null }} />)

    expect(c.querySelectorAll('[data-testid="chat-item"]').length).toBe(WINDOW)
  })

  it('renders the trailing content outside the virtualized window', () => {
    const c = render(
      <VirtualizedChatList
        items={makeItems(50)}
        scrollRef={{ current: null }}
        trailingContent={<div data-testid="tail">streaming</div>}
      />,
    )
    expect(c.querySelector('[data-testid="tail"]')).not.toBeNull()
  })

  it('registers a navigation handle that scrolls the virtualizer to the target row', () => {
    scrollToIndexMock.mockClear()
    const items: DisplayItem[] = [
      { kind: 'assistant', message: { id: 'm0', sessionId: 's1', type: 'assistant', content: 'a', metadata: {}, timestamp: 0 } },
      { kind: 'plan_step', id: 'p1', stepId: 'step_1', stepNum: 1, title: 't', status: 'completed', children: [] },
      { kind: 'assistant', message: { id: 'm2', sessionId: 's1', type: 'assistant', content: 'b', metadata: {}, timestamp: 0 } },
    ]
    const ref: { current: ChatVirtualizerHandle | null } = { current: null }
    render(<VirtualizedChatList items={items} scrollRef={{ current: null }} virtualizerRef={ref} />)

    const handle = ref.current!
    expect(handle).not.toBeNull()

    // A plan-step target maps to its own row index.
    expect(handle.scrollToStep('step_1')).toBe(true)
    expect(scrollToIndexMock).toHaveBeenCalledWith(1, { align: 'start' })

    // A bookmark key maps to the row carrying it.
    scrollToIndexMock.mockClear()
    expect(handle.scrollToKey('m2')).toBe(true)
    expect(scrollToIndexMock).toHaveBeenCalledWith(2, { align: 'start' })

    // An unknown target is not found and does not scroll.
    scrollToIndexMock.mockClear()
    expect(handle.scrollToStep('missing')).toBe(false)
    expect(handle.scrollToKey('missing')).toBe(false)
    expect(scrollToIndexMock).not.toHaveBeenCalled()
  })

  it('clears the registered navigation handle on unmount', () => {
    const ref: { current: ChatVirtualizerHandle | null } = { current: null }
    render(<VirtualizedChatList items={makeItems(10)} scrollRef={{ current: null }} virtualizerRef={ref} />)
    expect(ref.current).not.toBeNull()
    act(() => { root?.unmount() })
    root = null
    expect(ref.current).toBeNull()
  })
})
