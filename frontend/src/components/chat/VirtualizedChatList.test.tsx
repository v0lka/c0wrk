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
    }
  },
}))

vi.mock('./ChatMessageRenderer', () => ({
  ChatItem: ({ item }: { item: DisplayItem }) => <div data-testid="chat-item">{item.kind}</div>,
}))

import { VirtualizedChatList } from './VirtualizedChatList'

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
})
