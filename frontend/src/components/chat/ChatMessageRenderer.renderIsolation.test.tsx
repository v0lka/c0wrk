// @vitest-environment jsdom
//
// Render isolation: changing ONE message must not re-render the others (and so
// must not re-parse their Markdown). `groupMessages` rebuilds the whole item
// tree on every store change, so each block receives a brand-new `item` object;
// the memoized blocks must therefore compare by the item's stable payload
// (`message` identity) — not by object identity — to bail out.
//
// MarkdownViewer is the expensive leaf of an assistant message: here it is a
// spy that counts renders per content, which is exactly the "did this message
// re-render?" signal we assert on.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'

const { mdRenders } = vi.hoisted(() => ({ mdRenders: new Map<string, number>() }))

vi.mock('@/components/MarkdownViewer', () => ({
  MarkdownViewer: ({ content }: { content: string }) => {
    mdRenders.set(content, (mdRenders.get(content) ?? 0) + 1)
    return <div data-md={content} />
  },
}))

vi.mock('@/components/chat/MessageFooter', () => ({ MessageFooter: () => null }))

import { ChatMessageRenderer } from './ChatMessageRenderer'

type AssistantItem = Extract<DisplayItem, { kind: 'assistant' }>

function assistantItem(id: string, content: string): AssistantItem {
  const message: ChatMessageUI = {
    id,
    sessionId: 's1',
    type: 'assistant',
    content,
    metadata: {},
    timestamp: 0,
  }
  return { kind: 'assistant', message }
}

/** A fresh wrapper around an existing message, as a re-group would produce. */
function wrap(message: ChatMessageUI): AssistantItem {
  return { kind: 'assistant', message }
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

function rerender(node: ReactElement): void {
  act(() => {
    root!.render(node)
  })
}

describe('ChatMessageRenderer render isolation', () => {
  beforeEach(() => {
    mdRenders.clear()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('does not re-render messages whose content did not change', () => {
    const a1 = assistantItem('a1', 'answer one')
    const a2 = assistantItem('a2', 'answer two')
    render(<ChatMessageRenderer items={[a1, a2]} bookmarkable={false} />)
    expect(mdRenders.get('answer one')).toBe(1)
    expect(mdRenders.get('answer two')).toBe(1)

    // A re-grouped rebuild: brand-new wrappers for a1/a2 (same message objects)
    // plus one new message.
    const a3 = assistantItem('a3', 'answer three')
    rerender(
      <ChatMessageRenderer items={[wrap(a1.message), wrap(a2.message), a3]} bookmarkable={false} />,
    )

    expect(mdRenders.get('answer one')).toBe(1) // unchanged → memo bailed out
    expect(mdRenders.get('answer two')).toBe(1)
    expect(mdRenders.get('answer three')).toBe(1) // new → rendered once
  })

  it('re-renders the message whose content actually changed', () => {
    const a1 = assistantItem('a1', 'v1')
    render(<ChatMessageRenderer items={[a1]} bookmarkable={false} />)
    expect(mdRenders.get('v1')).toBe(1)

    rerender(<ChatMessageRenderer items={[assistantItem('a1', 'v2')]} bookmarkable={false} />)
    expect(mdRenders.get('v2')).toBe(1)
    expect(mdRenders.get('v1')).toBe(1)
  })

  it('still isolates unchanged messages through the sticky-turn + bookmark-gutter path', () => {
    // Mirrors ChatArea's non-virtualized render (sticky turns + bookmark rows),
    // where each block sits under an extra BookmarkableRow wrapper.
    const a1 = assistantItem('a1', 'one')
    const a2 = assistantItem('a2', 'two')
    render(<ChatMessageRenderer items={[a1, a2]} stickyUserMessages />)
    expect(mdRenders.get('one')).toBe(1)
    expect(mdRenders.get('two')).toBe(1)

    rerender(
      <ChatMessageRenderer
        items={[wrap(a1.message), wrap(a2.message), assistantItem('a3', 'three')]}
        stickyUserMessages
      />,
    )
    expect(mdRenders.get('one')).toBe(1)
    expect(mdRenders.get('two')).toBe(1)
    expect(mdRenders.get('three')).toBe(1)
  })
})
