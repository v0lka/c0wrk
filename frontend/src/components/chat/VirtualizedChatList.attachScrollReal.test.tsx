// @vitest-environment jsdom
//
// Same contract as VirtualizedChatList.attachScroll.test.tsx, but against the
// REAL @tanstack/react-virtual adapter (that file mocks the module, so the
// adapter's own attach behaviour can only be exercised in a file without the
// mock). jsdom has no layout engine, so the viewport geometry and
// `Element.scrollTo` (which the adapter's `elementScroll` writes through, and
// which jsdom does not implement) are supplied here.
//
// Without the late-attach repair the transcript ends at scrollTop 0.

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { DisplayItem } from '@/types/messages'
import { ChatScrollManager } from './ChatScrollManager'
import { ChatHoverRegion } from './ChatHoverRegion'
import { VirtualizedChatList } from './VirtualizedChatList'
import { ScrollProvider } from './ScrollContext'
import { useChatStore } from '@/stores/chatStore'

const VIEW_H = 600
const ROW_H = 40
const PADDING_AND_TRAILING = 72

let root: Root | null = null
let container: HTMLDivElement
let values: WeakMap<HTMLElement, number>
let log: { value: number; tag: string }[]

const saved: Record<string, PropertyDescriptor | undefined> = {}

function findDesc(name: string): PropertyDescriptor | undefined {
  let proto: object | null = HTMLElement.prototype
  while (proto) {
    const d = Object.getOwnPropertyDescriptor(proto, name)
    if (d) return d
    proto = Object.getPrototypeOf(proto) as object | null
  }
  return undefined
}

function isViewport(node: HTMLElement): boolean {
  return typeof node.className === 'string' && node.className.includes('overflow-auto')
}

/** Content height of the viewport: the rows container's inline spacer height. */
function contentHeight(node: HTMLElement): number {
  const wrapper = node.firstElementChild
  if (!wrapper) return 0
  for (const child of Array.from(wrapper.children) as HTMLElement[]) {
    const h = parseFloat(child.style?.height ?? '')
    if (!Number.isNaN(h)) return h + PADDING_AND_TRAILING
  }
  return PADDING_AND_TRAILING
}

function item(id: string): DisplayItem {
  return { kind: 'assistant', message: { id, sessionId: 's1', type: 'assistant', content: `msg ${id}`, metadata: {}, timestamp: 0 } }
}

async function settle(ms = 220) {
  await act(async () => { await new Promise<void>((r) => setTimeout(r, ms)) })
}

describe('virtualizer late attach vs the session-switch pin (real adapter)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    log = []
    values = new WeakMap()
    useChatStore.setState({ scrollPositions: {}, taskActive: {}, activityStatus: {}, streamingText: {}, messages: {} } as never)

    saved.scrollTop = findDesc('scrollTop')
    saved.clientHeight = findDesc('clientHeight')
    saved.scrollHeight = findDesc('scrollHeight')
    saved.offsetHeight = findDesc('offsetHeight')

    Object.defineProperty(HTMLElement.prototype, 'scrollTop', {
      configurable: true,
      get(this: HTMLElement) { return values.get(this) ?? 0 },
      set(this: HTMLElement, v: number) {
        values.set(this, v)
        log.push({ value: v, tag: isViewport(this) ? 'viewport' : `other(${this.tagName})` })
      },
    })
    Object.defineProperty(HTMLElement.prototype, 'clientHeight', {
      configurable: true,
      get(this: HTMLElement) { return isViewport(this) ? VIEW_H : 0 },
    })
    Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
      configurable: true,
      get(this: HTMLElement) { return isViewport(this) ? contentHeight(this) : 0 },
    })
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get(this: HTMLElement) {
        if (isViewport(this)) return VIEW_H
        if (this.dataset?.index !== undefined) return ROW_H
        return 0
      },
    })
    Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
      configurable: true,
      value(this: HTMLElement, opts: ScrollToOptions) {
        if (opts && typeof opts.top === 'number') this.scrollTop = opts.top
      },
    })
  })

  afterEach(() => {
    act(() => { root?.unmount() })
    root = null
    for (const [name, desc] of Object.entries(saved)) {
      if (desc) Object.defineProperty(HTMLElement.prototype, name, desc)
      else delete (HTMLElement.prototype as unknown as Record<string, unknown>)[name]
    }
    delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollTo
    document.body.innerHTML = ''
  })

  it('keeps the pin when the adapter attaches a commit after the mount', async () => {
    // A RUNNING session with a long transcript: ChatScrollManager's mount effect
    // pins the live tail. `messages` stays IDENTICAL across the re-render, so the
    // auto-scroll effect cannot mask the clobber by re-pinning — exactly the
    // "returned after a while and the chat is at the very top" report.
    useChatStore.setState({ taskActive: { s1: true } } as never)
    const items = Array.from({ length: 100 }, (_, i) => item(`m${i}`))
    const messages = items.map((it) => (it.kind === 'assistant' ? it.message : null))
    const scrollRef = { current: null as HTMLDivElement | null }
    const virtualizerRef = { current: null as never }

    const tree = (list: DisplayItem[]) => (
      <ScrollProvider>
        <ChatScrollManager key="s1" sessionId="s1" messages={messages as never} streamingText={undefined} scrollRef={scrollRef as never} virtualizerRef={virtualizerRef as never}>
          <ChatHoverRegion className="p-4 space-y-4 min-w-0">
            <VirtualizedChatList items={list} scrollRef={scrollRef as never} virtualizerRef={virtualizerRef as never} />
          </ChatHoverRegion>
        </ChatScrollManager>
      </ScrollProvider>
    )

    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => { root!.render(tree(items)) })
    await settle()
    await settle()

    const viewport = scrollRef.current!
    const pinned = viewport.scrollTop
    // The app pinned the live tail (at the bottom of the estimated content).
    expect(pinned).toBeGreaterThan(0)

    act(() => { root!.render(tree([...items, item('m100')])) })
    await settle()
    await settle()

    // The adapter attached on that later commit and wrote initialOffset (0)…
    expect(log.some((w) => w.tag === 'viewport' && w.value === 0)).toBe(true)
    // …and the repair restored the pin before paint, so the transcript did not
    // jump to the very top.
    expect(viewport.scrollTop).toBe(pinned)
  })
})
