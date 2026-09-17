// @vitest-environment jsdom
//
// Pinned user-message overlay contract for the virtualized transcript.
//
// Virtualized rows are absolutely positioned, which kills the native
// `position: sticky` pin of a turn's user message — the regression bc3db2fd
// introduced ("user messages stopped collapsing / pinning" once a session
// crossed the virtualization threshold). VirtualizedChatList re-creates the
// pin: ONE sticky UserMessage overlay rendered BEFORE the rows container,
// while the natural row is merely `visibility: hidden` (slot and measurements
// preserved).
//
// jsdom has no layout engine, so the virtualizer is mocked: row offsets come
// from a test-controlled table exposed via `measurementsCache`, and scrolling
// is simulated by mutating the fake scroll element's `scrollTop` followed by a
// re-render (the real adapter re-renders on every visible-range change and on
// scroll start/end — exactly the moments the pin can engage or release).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { DisplayItem } from '@/types/messages'
import { indexOfUserLeader } from '@/lib/chatVirtualizer'

const mock = vi.hoisted(() => ({
  /** Row-start offsets per index, served through `measurementsCache`. */
  rowStarts: [] as number[],
  /** First index the mocked virtualizer mounts (the window slides on scroll). */
  visibleFrom: 0,
  /** Mounted window size (mirrors overscan at runtime). */
  window: 8,
  measureElementMock: vi.fn(),
  resizeItemMock: vi.fn(),
  scrollToIndexMock: vi.fn(),
}))

vi.mock('@tanstack/react-virtual', async () => {
  const React = await import('react')
  return {
    // Mirrors the real hook contract: the instance is created once and keeps
    // its identity across re-renders. measurementsCache/getVirtualItems read
    // the live test tables so a re-render after a "scroll" sees fresh offsets.
    useVirtualizer: (options: {
      count: number
      getItemKey: (i: number) => string | number
    }) => {
      const build = () => {
        const inst = {
          opts: options,
          get measurementsCache() {
            return mock.rowStarts.map((start, index) => ({
              index,
              key: inst.opts.getItemKey(index),
              start,
              size: 100,
            }))
          },
          getVirtualItems: () =>
            Array.from({ length: Math.min(mock.window, inst.opts.count - mock.visibleFrom) }, (_, i) => {
              const index = mock.visibleFrom + i
              return { index, key: inst.opts.getItemKey(index), start: mock.rowStarts[index] ?? 0, size: 100 }
            }),
          getTotalSize: () => inst.opts.count * 100,
          measureElement: mock.measureElementMock,
          // Public on the real instance; the component's synchronous
          // re-measure feeds it directly (bypasses measureElement's
          // isScrolling gate).
          resizeItem: mock.resizeItemMock,
          scrollToIndex: mock.scrollToIndexMock,
        }
        return inst
      }
      const [instance] = React.useState(build)
      instance.opts = options
      return instance
    },
  }
})

// The real ChatItem/UserMessage must render (not a stub) so the assertions
// cover the actual pinned DOM: data-sticky-user-message, sticky classes, and
// the collapsed one-line preview.
import { VirtualizedChatList } from './VirtualizedChatList'

function userItem(id: string, text: string): DisplayItem {
  return { kind: 'user', message: { id, sessionId: 's1', type: 'user', content: text, metadata: {}, timestamp: 0 } }
}

function assistantItem(id: string): DisplayItem {
  return { kind: 'assistant', message: { id, sessionId: 's1', type: 'assistant', content: `msg ${id}`, metadata: {}, timestamp: 0 } }
}

/** Two turns: user@0 (+9 assistants), user@10 (+9 assistants); rows are 100px. */
function buildTranscript(): DisplayItem[] {
  const items: DisplayItem[] = [userItem('u1', 'turn one question')]
  for (let i = 0; i < 9; i++) items.push(assistantItem(`a1-${i}`))
  items.push(userItem('u2', 'turn two question'))
  for (let i = 0; i < 9; i++) items.push(assistantItem(`a2-${i}`))
  return items
}

let root: Root | null = null
let container: HTMLDivElement
const fakeScroll = { scrollTop: 0 } as HTMLElement

function renderList(items: DisplayItem[]): HTMLElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<VirtualizedChatList items={items} scrollRef={{ current: fakeScroll }} bookmarkable={false} />)
  })
  return container
}

/** Re-render with the same props — how a scroll-driven re-render arrives. */
function rerender(items: DisplayItem[]): void {
  act(() => {
    root!.render(<VirtualizedChatList items={items} scrollRef={{ current: fakeScroll }} bookmarkable={false} />)
  })
}

describe('VirtualizedChatList pinned user-message overlay', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    mock.visibleFrom = 0
    mock.rowStarts = Array.from({ length: 20 }, (_, i) => i * 100)
    fakeScroll.scrollTop = 0
    mock.measureElementMock.mockClear()
    mock.scrollToIndexMock.mockClear()
  })
  afterEach(() => {
    act(() => { root?.unmount() })
    root = null
  })

  it('pins the current turn user message as a collapsed one-line overlay once its row top is above the viewport top', () => {
    const items = buildTranscript()
    fakeScroll.scrollTop = 550
    const c = renderList(items)

    const overlay = c.querySelector('[data-sticky-user-message]')
    expect(overlay).not.toBeNull()
    // Collapsed to a single truncated line by default (expand on click).
    expect(overlay!.querySelector('[aria-expanded="false"]')).not.toBeNull()
    expect(overlay!.querySelector('[aria-expanded="true"]')).toBeNull()
    expect(overlay!.querySelector('.truncate')!.textContent).toBe('turn one question')
    // Native sticky pin classes (zoom-safe positioning, above the rows).
    expect(overlay!.classList.contains('sticky')).toBe(true)
    expect(overlay!.classList.contains('top-0')).toBe(true)
    expect(overlay!.classList.contains('z-10')).toBe(true)
  })

  it('renders the overlay as a sibling BEFORE the rows container and hides the natural row with visibility:hidden', () => {
    const items = buildTranscript()
    fakeScroll.scrollTop = 550
    mock.visibleFrom = 0 // rows 0..7 mounted, including the pinned user row
    const c = renderList(items)

    const overlay = c.querySelector('[data-sticky-user-message]')!
    expect(overlay).not.toBeNull()
    // The overlay rides an ALWAYS-MOUNTED zero-height sticky wrapper that
    // precedes the sized rows container — engaging the pin must not change the
    // flow (a full-height overlay would push the rows down and flip their
    // `space-y-4` first-child margin, jumping the transcript), and
    // chatScroll.ts's sticky-bar compensation still sees the bar first in
    // document order.
    const wrapper = c.children[0] as HTMLElement
    expect(wrapper.contains(overlay)).toBe(true)
    expect(wrapper.classList.contains('sticky')).toBe(true)
    expect(wrapper.classList.contains('h-0')).toBe(true)
    const sizedContainer = c.children[1] as HTMLElement
    expect(sizedContainer.style.position).toBe('relative')

    // The natural row keeps its slot and measurements — hidden, not unmounted.
    const naturalRow = c.querySelector('[data-index="0"]') as HTMLElement
    expect(naturalRow.style.visibility).toBe('hidden')
    // Sibling rows are untouched.
    expect((c.querySelector('[data-index="1"]') as HTMLElement).style.visibility).toBe('')
  })

  it('switches the overlay to the next turn once its user row crosses the viewport top', () => {
    const items = buildTranscript()
    fakeScroll.scrollTop = 1500 // past user@10 (start 1000), inside turn two
    mock.visibleFrom = 8 // rows 8..15 mounted, including user@10
    const c = renderList(items)

    const overlay = c.querySelector('[data-sticky-user-message]')!
    expect(overlay.querySelector('.truncate')!.textContent).toBe('turn two question')
    expect((c.querySelector('[data-index="10"]') as HTMLElement).style.visibility).toBe('hidden')
  })

  it('releases the pin when scrolled back to the turn start — no overlay, natural row visible', () => {
    const items = buildTranscript()
    fakeScroll.scrollTop = 1500
    mock.visibleFrom = 8
    const c = renderList(items)
    expect(c.querySelector('[data-sticky-user-message]')).not.toBeNull()

    // Scroll back so the turn's user row top is exactly AT the viewport top:
    // the natural row is visible at its natural position, overlay unmounted.
    fakeScroll.scrollTop = 1000
    rerender(items)
    expect(c.querySelector('[data-sticky-user-message]')).toBeNull()
    // The zero-height wrapper stays mounted (flow geometry must not change
    // between pin states), only its content unmounts.
    expect((c.children[0] as HTMLElement).classList.contains('h-0')).toBe(true)
    const row = c.querySelector('[data-index="10"]') as HTMLElement
    expect(row).not.toBeNull()
    expect(row.style.visibility).toBe('')

    // Same at the transcript top.
    fakeScroll.scrollTop = 0
    mock.visibleFrom = 0
    rerender(items)
    expect(c.querySelector('[data-sticky-user-message]')).toBeNull()
    expect((c.querySelector('[data-index="0"]') as HTMLElement).style.visibility).toBe('')
  })

  it('mounts no overlay when no user row starts at or above the scroll position', () => {
    const items: DisplayItem[] = [assistantItem('a0'), assistantItem('a1'), userItem('u1', 'later turn'), assistantItem('a3')]
    mock.rowStarts = [0, 100, 200, 300]
    fakeScroll.scrollTop = 150 // above the user row (start 200)
    mock.visibleFrom = 0
    const c = renderList(items)
    expect(c.querySelector('[data-sticky-user-message]')).toBeNull()
    expect((c.querySelector('[data-index="2"]') as HTMLElement).style.visibility).toBe('')
  })
})

describe('indexOfUserLeader', () => {
  const items: DisplayItem[] = [
    userItem('u1', 'one'),
    assistantItem('a1'),
    userItem('u2', 'two'),
    assistantItem('a2'),
    userItem('u3', 'three'),
  ]
  const rowStarts = [0, 100, 200, 300, 400]

  it('returns the LAST user row whose start sits at or above the scroll top', () => {
    // Scroll inside turn one (past user@0, before user@2): leader is user@0.
    expect(indexOfUserLeader(items, 150, (i) => rowStarts[i])).toBe(0)
    // Scroll between user@2 and user@4: leader is user@2.
    expect(indexOfUserLeader(items, 350, (i) => rowStarts[i])).toBe(2)
    // Past the last user row: leader is user@4.
    expect(indexOfUserLeader(items, 999, (i) => rowStarts[i])).toBe(4)
  })

  it('treats the boundary as inclusive — a user row starting exactly at scrollTop leads', () => {
    expect(indexOfUserLeader(items, 200, (i) => rowStarts[i])).toBe(2)
  })

  it('returns -1 when every user row starts below the scroll position', () => {
    expect(indexOfUserLeader(items, 0, () => 100)).toBe(-1)
  })

  it('skips user rows with unknown offsets but still finds an earlier leader', () => {
    // row 4 (user) has no measurement yet; row 2 (user) owns the turn.
    const rowStart = (i: number) => (i === 4 ? undefined : rowStarts[i])
    expect(indexOfUserLeader(items, 450, rowStart)).toBe(2)
  })
})
