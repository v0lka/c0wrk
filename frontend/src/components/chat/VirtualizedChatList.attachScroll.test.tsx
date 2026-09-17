// @vitest-environment jsdom
//
// Regression: the virtualizer's LATE scroll-element attach used to yank the
// transcript to the very top on a session switch.
//
// `useVirtualizer`'s `_willUpdate` runs as a layout effect on every render. On
// the mount commit the parent viewport's ref is not attached yet (React attaches
// host refs later in the layout phase, after this component's layout effects),
// so `getScrollElement()` returns null and the adapter does NOT attach. It
// attaches on the NEXT commit and then unconditionally runs
// `_scrollToOffset(getScrollOffset())`; `scrollOffset` is still null (the
// adapter never observed a scroll event), so it resolves to the library default
// `initialOffset` = 0 and the adapter writes `scrollTo({ top: 0 })` — discarding
// the position ChatScrollManager just restored/pinned. Because the transcript is
// only virtualized above 60 display items, this showed up as "switching to a
// running session after a while opens it at the very beginning".
//
// The first describe pins the contract deterministically (the mocked hook
// controls exactly when the element becomes available); the second locks the
// behaviour against the REAL @tanstack/react-virtual adapter.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { DisplayItem } from '@/types/messages'

const mock = vi.hoisted(() => ({
  /** The scroll element the (mocked) adapter can see; null models the mount
   *  commit, where the parent viewport ref is not attached yet. */
  scrollEl: null as HTMLElement | null,
  /** How many times the adapter performed its attach write. */
  attachWrites: 0,
}))

vi.mock('@tanstack/react-virtual', async () => {
  const React = await import('react')
  return {
    useVirtualizer: (options: { count: number; getItemKey: (i: number) => string | number }) => {
      const [instance] = React.useState(() => ({
        opts: options,
        scrollElement: null as HTMLElement | null,
        measurementsCache: [] as { index: number; key: string | number; start: number; size: number }[],
        getVirtualItems: () => [] as { index: number; key: string | number; start: number; size: number }[],
        getTotalSize: () => options.count * 100,
        measureElement: () => undefined,
        resizeItem: () => undefined,
        scrollToIndex: () => undefined,
      }))
      instance.opts = options
      // Mirrors the real hook: `_willUpdate` is a layout effect registered
      // before the component's own effects; when the scroll element first
      // becomes available it attaches and writes `initialOffset` (0) because
      // `getScrollOffset()` has nothing observed to fall back on.
      React.useLayoutEffect(() => {
        if (mock.scrollEl && instance.scrollElement === null) {
          instance.scrollElement = mock.scrollEl
          mock.attachWrites += 1
          mock.scrollEl.scrollTop = 0
        }
      })
      return instance
    },
  }
})

import { VirtualizedChatList } from './VirtualizedChatList'

function item(id: string): DisplayItem {
  return { kind: 'assistant', message: { id, sessionId: 's1', type: 'assistant', content: `msg ${id}`, metadata: {}, timestamp: 0 } }
}

const baseItems: DisplayItem[] = [item('a0'), item('a1'), item('a2')]
const grownItems: DisplayItem[] = [...baseItems, item('a3')]

let root: Root | null = null
let container: HTMLDivElement
let el: HTMLElement
/** Every write to the fake viewport's scrollTop, in order. */
let writes: number[]
let scrollTopValue: number

/** Fake viewport exposing an observable scrollTop (jsdom has no layout). */
function makeScrollElement(initial: number): HTMLElement {
  const node = document.createElement('div')
  scrollTopValue = initial
  Object.defineProperty(node, 'scrollTop', {
    configurable: true,
    get: () => scrollTopValue,
    set: (v: number) => { scrollTopValue = v; writes.push(v) },
  })
  return node
}

describe('VirtualizedChatList vs the virtualizer late attach (mocked adapter)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    writes = []
    mock.scrollEl = null
    mock.attachWrites = 0
    container = document.createElement('div')
    document.body.appendChild(container)
  })

  afterEach(() => {
    act(() => { root?.unmount() })
    root = null
    document.body.innerHTML = ''
  })

  function mount(scrollEl: HTMLElement, items: DisplayItem[]) {
    const scrollRef = { current: scrollEl }
    root = createRoot(container)
    act(() => {
      root!.render(<VirtualizedChatList items={items} scrollRef={scrollRef as never} />)
    })
    return scrollRef
  }

  function rerender(scrollRef: { current: HTMLElement | null }, items: DisplayItem[]) {
    act(() => {
      root!.render(<VirtualizedChatList items={items} scrollRef={scrollRef as never} />)
    })
  }

  it("restores the app's scroll position over the adapter's attach write to 0", () => {
    // The app (ChatScrollManager) already restored/pinned its position in the
    // mount commit's layout phase — while the adapter was still detached.
    el = makeScrollElement(5_000)
    const scrollRef = mount(el, baseItems)
    expect(mock.attachWrites).toBe(0)
    expect(writes).toEqual([])

    // Next commit: the adapter sees the element, attaches and writes 0.
    mock.scrollEl = el
    rerender(scrollRef, grownItems)

    expect(mock.attachWrites).toBe(1)
    // Clobber first, repair within the same layout phase (before paint).
    expect(writes).toEqual([0, 5_000])
    expect(el.scrollTop).toBe(5_000)
  })

  it('does not write anything when the captured position is the top', () => {
    el = makeScrollElement(0)
    const scrollRef = mount(el, baseItems)
    mock.scrollEl = el
    rerender(scrollRef, grownItems)

    // Only the adapter's own attach write; the repair stays out of the way.
    expect(writes).toEqual([0])
    expect(el.scrollTop).toBe(0)
  })

  it('does not fire when the adapter attached on its very first pass', () => {
    // Element available from the start (no late attach): nothing was ever
    // captured, so the repair must not invent a position.
    el = makeScrollElement(0)
    mock.scrollEl = el
    const scrollRef = mount(el, baseItems)
    rerender(scrollRef, grownItems)

    expect(mock.attachWrites).toBe(1)
    expect(writes).toEqual([0])
    expect(el.scrollTop).toBe(0)
  })
})

