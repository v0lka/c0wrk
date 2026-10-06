// @vitest-environment jsdom
// Unit tests for CollapsibleBlock's collapse repositioning. Radix unmounts
// closed CollapsibleContent, so a collapse can remove the content the
// viewport is parked in and leave the collapsed header above the fold; the
// open→closed transition effect must anchor the chat viewport back onto the
// block's start (the single scroll writer for collapses) — and must NOT
// scroll when the header is still visible (the direct-click case), when the
// viewport is pinned to the bottom (the auto-settle of a followed turn —
// stick-to-bottom owns that landing), or when there is no transition at all
// (mounts, expand, idempotent re-renders).
//
// - requestAnimationFrame is stubbed synchronous (Radix Presence needs rAF
//   in jsdom, per the ChatMessageRenderer.test.tsx note).
// - scrollBlockStartIntoView is mocked so the tests assert TARGET IDENTITY
//   (viewport + block root); its geometry math is covered by chatScroll.test.ts.
// - The ScrollContext viewport is published by a stand-in registrar — the same
//   setScrollViewport registration ChatScrollManager performs — against a fake
//   viewport element with live geometry (jsdom does not lay out).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useEffect, type ReactNode } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/lib/chatScroll', () => ({
  scrollBlockStartIntoView: vi.fn(),
}))

import { CollapsibleBlock } from './CollapsibleBlock'
import { ScrollProvider, useScrollContext } from './ScrollContext'
import { scrollBlockStartIntoView } from '@/lib/chatScroll'

/** Live geometry both rect mocks read; tests mutate it between phases. */
const geom = { rootTop: 0, viewportTop: 0 }

function rectOf(top: number): DOMRect {
  return {
    top,
    height: 100,
    bottom: top + 100,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as unknown as DOMRect
}

/**
 * Stand-in for ChatScrollManager's viewport publication: registers the fake
 * viewport element (id `chat-viewport`) into the ScrollContext exactly the
 * way the manager does (getter indirection, unregisters on cleanup).
 */
/** Live at-bottom state the stand-in publishes; tests flip it between phases. */
let atBottom = false

function FakeViewport({ children }: { children: ReactNode }) {
  const { setScrollViewport, setIsAtBottom } = useScrollContext()
  useEffect(() => {
    const resolve = (): HTMLElement | null => document.getElementById('chat-viewport')
    setScrollViewport(resolve)
    setIsAtBottom(() => atBottom)
    return () => {
      setScrollViewport(null)
      setIsAtBottom(null)
    }
  }, [setScrollViewport, setIsAtBottom])
  return <div id="chat-viewport">{children}</div>
}

/** Fully controlled CollapsibleBlock — `open` flips via re-render. */
function Harness({ open }: { open: boolean }) {
  return (
    <FakeViewport>
      <CollapsibleBlock label="block" open={open} onOpenChange={() => {}}>
        <div>content</div>
      </CollapsibleBlock>
    </FakeViewport>
  )
}

/** CollapsibleBlock WITHOUT any provider — the outside-the-transcript shape. */
function Bare({ open }: { open: boolean }) {
  return (
    <CollapsibleBlock label="block" open={open} onOpenChange={() => {}}>
      <div>content</div>
    </CollapsibleBlock>
  )
}

let root: Root
let container: HTMLDivElement
let viewportEl: HTMLElement
let blockEl: HTMLElement

/** Render (or re-render) the block with the given open state. */
function renderWith(open: boolean): void {
  act(() => {
    root.render(
      <ScrollProvider>
        <Harness open={open} />
      </ScrollProvider>,
    )
  })
}

/** First render of a fresh block; wires the live-geometry rect mocks onto it. */
function mountBlock(initialOpen: boolean): void {
  renderWith(initialOpen)
  viewportEl = document.getElementById('chat-viewport')!
  blockEl = container.querySelector('[data-chevron-reveal-id]')!
  vi.spyOn(viewportEl, 'getBoundingClientRect').mockImplementation(() => rectOf(geom.viewportTop))
  vi.spyOn(blockEl, 'getBoundingClientRect').mockImplementation(() => rectOf(geom.rootTop))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
    cb(0)
    return 0
  })
  vi.stubGlobal('cancelAnimationFrame', () => {})
  geom.rootTop = 0
  geom.viewportTop = 0
  atBottom = false
  document.body.replaceChildren()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

describe('CollapsibleBlock collapse repositioning', () => {
  it('anchors the viewport on the block start when the header ends up above the scrollport', () => {
    // The user was reading mid-content of an oversized open block: its header
    // sits 900px above the viewport top.
    geom.rootTop = -900
    geom.viewportTop = 0
    mountBlock(true)
    vi.mocked(scrollBlockStartIntoView).mockClear()

    renderWith(false)

    expect(scrollBlockStartIntoView).toHaveBeenCalledTimes(1)
    expect(scrollBlockStartIntoView).toHaveBeenCalledWith(viewportEl, blockEl)
  })

  it('stays glued to the bottom while the viewport is pinned there (auto-settle of a followed turn)', () => {
    // The user was following a settling turn's live tail: stick-to-bottom is
    // engaged even though the open block's header sits far above the fold.
    geom.rootTop = -900
    geom.viewportTop = 0
    atBottom = true
    mountBlock(true)
    vi.mocked(scrollBlockStartIntoView).mockClear()

    renderWith(false)

    // Skipping the reposition lets the collapse's scrollTop clamp keep the
    // viewport on the committed answer; the smooth header anchor must not
    // yank a following user up to the header.
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()
  })

  it('does not scroll when the header is still visible (direct header click)', () => {
    geom.rootTop = 40
    geom.viewportTop = 0
    mountBlock(true)
    vi.mocked(scrollBlockStartIntoView).mockClear()

    renderWith(false)

    // Realignment would be a gratuitous jump: the user pressed exactly the
    // visible header, so the collapsed block is already on screen.
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()
  })

  it('never scrolls without an open→closed transition (mounts, expand, same-value rerenders)', () => {
    // Initial CLOSED mount: the first effect run records state, acts never.
    mountBlock(false)
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()

    // Closed → open: an expansion, not a collapse.
    renderWith(true)
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()

    // Open → open (parent re-render, same value): no transition.
    renderWith(true)
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()
  })

  it('treats the initial OPEN mount as record-only (no phantom collapse)', () => {
    geom.rootTop = -900
    geom.viewportTop = 0
    mountBlock(true)
    // The header is far above the fold, but nothing collapsed yet.
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()

    // Only the genuine transition to closed acts.
    renderWith(false)
    expect(scrollBlockStartIntoView).toHaveBeenCalledTimes(1)
    expect(scrollBlockStartIntoView).toHaveBeenCalledWith(viewportEl, blockEl)
  })

  it('is a no-op outside the chat transcript (no ScrollProvider viewport)', () => {
    // No ScrollProvider: useChatScrollViewport falls back to a null resolver
    // (CollapsibleBlock must also tolerate rendering outside the transcript).
    geom.rootTop = -900
    act(() => {
      root.render(<Bare open={true} />)
    })
    vi.mocked(scrollBlockStartIntoView).mockClear()

    act(() => {
      root.render(<Bare open={false} />)
    })

    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()
  })
})
