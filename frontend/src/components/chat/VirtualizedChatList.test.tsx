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

const { scrollToIndexMock, measureElementMock, resizeItemMock, optionsCapture } = vi.hoisted(() => ({
  scrollToIndexMock: vi.fn(),
  measureElementMock: vi.fn(),
  resizeItemMock: vi.fn(),
  optionsCapture: {} as {
    measureElement?: (el: HTMLElement, entry?: ResizeObserverEntry) => number
  },
}))

vi.mock('@tanstack/react-virtual', async () => {
  const React = await import('react')
  return {
    // Mirrors the REAL hook contract: the returned instance keeps its identity
    // across re-renders (created once via useState in the library source), so
    // ref callbacks derived from it stay stable — React must not detach and
    // re-attach row refs on a plain re-render.
    useVirtualizer: (options: {
      count: number
      getItemKey: (i: number) => string | number
      measureElement?: (el: HTMLElement, entry?: ResizeObserverEntry) => number
    }) => {
      // Capture the latest options so tests can assert on what the component
      // passed to the virtualizer (e.g. the custom measurement function).
      optionsCapture.measureElement = options.measureElement
      const build = () => {
        const inst = {
          opts: options,
          // Empty by default: the pinned-overlay logic reads
          // measurementsCache[index]?.start for USER rows only, and these
          // tests build assistant-only transcripts. Provide the property so
          // a future user-item fixture does not TypeError on undefined.
          get measurementsCache() {
            return [] as Array<{ index: number; key: string | number; start: number; size: number }>
          },
          getVirtualItems: () => {
            const end = Math.min(WINDOW, inst.opts.count)
            return Array.from({ length: end }, (_, i) => ({
              index: i,
              key: inst.opts.getItemKey(i),
              start: i * 100,
              size: 100,
            }))
          },
          getTotalSize: () => inst.opts.count * 100,
          measureElement: measureElementMock,
          // Public on the real Virtualizer instance (see virtual-core's .d.ts):
          // the component's synchronous re-measure feeds measurements straight
          // here, bypassing measureElement's isScrolling gate.
          resizeItem: resizeItemMock,
          scrollToIndex: scrollToIndexMock,
        }
        return inst
      }
      const [instance] = React.useState(build)
      // Keep the persistent instance wired to the latest render's options.
      instance.opts = options
      return instance
    },
  }
})

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

  it('renders the trailing content outside the virtualized window, after the sized container', () => {
    const c = render(
      <VirtualizedChatList
        items={makeItems(50)}
        scrollRef={{ current: null }}
        trailingContent={<div data-testid="tail">streaming</div>}
      />,
    )
    const tail = c.querySelector('[data-testid="tail"]')
    expect(tail).not.toBeNull()
    // The trailing block must stay in-flow AFTER the total-size container:
    // the container carries the full virtualized height, so an in-flow tail
    // following it can never overlap the last rows.
    expect(c.lastElementChild).toBe(tail)
    // children = [zero-height sticky pin wrapper, sized rows container, tail]
    // (the wrapper is always mounted so pin toggles never shift the rows).
    const sizedContainer = c.children[1] as HTMLElement
    expect(sizedContainer.style.position).toBe('relative')
    expect((c.children[0] as HTMLElement).classList.contains('h-0')).toBe(true)
  })

  it('measures rows with offsetHeight (layout px), ignoring the ResizeObserver borderBoxSize', () => {
    render(<VirtualizedChatList items={makeItems(20)} scrollRef={{ current: null }} />)
    // The component must pass its own measurement function to the virtualizer.
    expect(typeof optionsCapture.measureElement).toBe('function')

    const el = document.createElement('div')
    // jsdom has no layout engine, so offsetHeight is always 0 — stub the
    // LAYOUT-px value the browser would report.
    Object.defineProperty(el, 'offsetHeight', { value: 137 })
    // A ResizeObserver entry whose borderBoxSize carries VISUAL px (the value
    // under CSS zoom ≠ 100%): the library default would prefer it, the custom
    // measurement must ignore it.
    const entry = {
      borderBoxSize: [{ blockSize: 274, inlineSize: 800 }],
    } as unknown as ResizeObserverEntry

    expect(optionsCapture.measureElement!(el, entry)).toBe(137)
    // And without an entry at all (the synchronous ref-callback path).
    expect(optionsCapture.measureElement!(el, undefined)).toBe(137)
  })

  it('re-measures every mounted row synchronously when items change (before paint)', () => {
    measureElementMock.mockClear()
    resizeItemMock.mockClear()
    const items = makeItems(50)
    const c = render(<VirtualizedChatList items={items} scrollRef={{ current: null }} />)

    const mounted = c.querySelectorAll('[data-index]').length
    expect(mounted).toBe(WINDOW)
    // Initial mount: the measureRow ref callback wired each mounted row into
    // the virtualizer's element cache via measureElement, and the mount-time
    // layout effect fed each row's measurement to resizeItem.
    expect(measureElementMock).toHaveBeenCalledTimes(WINDOW)
    expect(resizeItemMock).toHaveBeenCalledTimes(WINDOW)
    for (const [index, size] of resizeItemMock.mock.calls) {
      expect(index).toBeGreaterThanOrEqual(0)
      expect(index).toBeLessThan(50)
      // jsdom reports offsetHeight 0; what matters is the value SOURCE
      // (layout px, not a ResizeObserver entry) — asserted by the dedicated
      // measurement test below.
      expect(size).toBe(0)
    }

    // Items change (new array identity — e.g. a streamed chunk regrouped the
    // tree): the items-change layout effect must re-measure every STILL-MOUNTED
    // row synchronously, without waiting for the ResizeObserver's post-paint
    // callback. Keys are stable, so refs are not detached → measureElement is
    // not called again.
    measureElementMock.mockClear()
    resizeItemMock.mockClear()
    const grown = [
      ...items,
      {
        kind: 'assistant' as const,
        message: { id: 'm-new', sessionId: 's1', type: 'assistant' as const, content: 'new', metadata: {}, timestamp: 50 },
      },
    ]
    act(() => {
      root!.render(<VirtualizedChatList items={grown} scrollRef={{ current: null }} />)
    })
    expect(measureElementMock).not.toHaveBeenCalled()
    expect(resizeItemMock).toHaveBeenCalledTimes(WINDOW)
    for (const [index] of resizeItemMock.mock.calls) {
      expect(index).toBeLessThan(WINDOW)
    }
  })

  it('measures through resizeItem even while the viewport is mid-scroll (isScrolling gate)', () => {
    // Regression guard for the multi-subagent regime: the tail-pinned
    // auto-scroll keeps scroll events streaming, and the REAL library's
    // measureElement no-ops while isScrolling is set. Simulate that gate by
    // making measureElement record nothing (as the gated library path would)
    // and assert the items-change re-measure still lands via resizeItem —
    // the trailing status block must not overlap the last rows mid-scroll.
    measureElementMock.mockClear()
    resizeItemMock.mockClear()
    const items = makeItems(50)
    render(<VirtualizedChatList items={items} scrollRef={{ current: null }} />)

    measureElementMock.mockClear()
    resizeItemMock.mockClear()
    act(() => {
      root!.render(
        <VirtualizedChatList
          items={[...items]}
          scrollRef={{ current: null }}
        />,
      )
    })
    // The gated measureElement path contributed nothing...
    expect(measureElementMock).not.toHaveBeenCalled()
    // ...yet every still-mounted row was re-measured.
    expect(resizeItemMock).toHaveBeenCalledTimes(WINDOW)
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
