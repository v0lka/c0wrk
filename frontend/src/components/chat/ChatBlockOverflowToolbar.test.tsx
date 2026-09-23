// @vitest-environment jsdom
// Tests for the block-overflow toolbar and its wiring into ChatScrollManager's
// single sticky bottom stack.
//
// Part 1 (unit) renders the toolbar against a stub navigation API: native
// button semantics (keyboard operability), aria-label/title pairing, disabled
// states at the ends, and design-token-only styling.
//
// Part 2 (integration) renders the real ChatScrollManager with the real
// useOversizedBlockNav and live-geometry fixtures (the useOversizedBlockNav
// test conventions): the derived disabled states reach the buttons, clicking a
// button navigates the real viewport, and the sticky stack hosts the banner +
// toolbar without per-child stickiness (no overlap by construction).
//
// jsdom does NOT synthesize a click from Enter/Space keydown — activation
// behavior is provided by the real browser's default `<button>` handling, not
// by component code. Keyboard operability is therefore asserted through the
// native-button invariants that guarantee it: native tag, type=button, no
// tabindex="-1", focusable, and no component-level key interception
// (defaultPrevented stays false). The click pathway the UA's Enter/Space
// handling takes is the same onClick the pointer tests exercise.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatBlockOverflowToolbar } from './ChatBlockOverflowToolbar'
import { ChatScrollManager } from './ChatScrollManager'
import { ScrollProvider } from './ScrollContext'
import type { OversizedBlockNavApi } from './useOversizedBlockNav'
import type { ChatMessageUI } from '@/types/messages'
import { useChatStore } from '@/stores/chatStore'

let root: Root | null = null

function stubNav(partial: Partial<OversizedBlockNavApi> = {}): OversizedBlockNavApi {
  return {
    activeRevealId: null,
    hasPrev: false,
    hasNext: false,
    collapse: vi.fn(),
    goPrev: vi.fn(),
    goNext: vi.fn(),
    goFirst: vi.fn(),
    goLast: vi.fn(),
    ...partial,
  }
}

/** The five buttons in visual order with their accessible names. */
const BUTTON_LABELS = [
  'Jump to first expandable block',
  'Previous expandable block',
  'Collapse current oversized block',
  'Next expandable block',
  'Jump to last expandable block',
] as const

function toolbarButtons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="Expandable block navigation"] button'))
}

function mountToolbar(nav: OversizedBlockNavApi): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<ChatBlockOverflowToolbar nav={nav} />)
  })
  return container
}

beforeEach(() => {
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

afterEach(() => {
  act(() => root?.unmount())
  root = null
})

describe('ChatBlockOverflowToolbar — buttons', () => {
  it('renders five native buttons in order, each with matching aria-label and title', () => {
    const container = mountToolbar(stubNav())

    const buttons = toolbarButtons(container)
    expect(buttons).toHaveLength(5)
    buttons.forEach((btn, i) => {
      expect(btn.tagName).toBe('BUTTON')
      expect(btn.getAttribute('type')).toBe('button')
      expect(btn.getAttribute('aria-label')).toBe(BUTTON_LABELS[i])
      expect(btn.getAttribute('title')).toBe(BUTTON_LABELS[i])
      // Each button carries its chevron/fold icon.
      expect(btn.querySelector('svg')).not.toBeNull()
    })
  })

  it('is keyboard-operable: native focusable buttons, no tab-order opt-out, no key interception', () => {
    const nav = stubNav({ hasPrev: true, hasNext: true, activeRevealId: 'b' })
    const container = mountToolbar(nav)

    for (const btn of toolbarButtons(container)) {
      // Native <button type="button"> without tabindex="-1" is focusable and
      // UA-activated by Enter/Space — no component key handling exists.
      expect(btn.getAttribute('tabindex')).not.toBe('-1')
      expect(btn.onkeydown).toBeNull()
      btn.focus()
      expect(document.activeElement).toBe(btn)
      for (const key of ['Enter', ' ']) {
        const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
        btn.dispatchEvent(ev)
        // No preventDefault: the browser's default activation (click on
        // Enter/Space) is left fully intact.
        expect(ev.defaultPrevented).toBe(false)
      }
    }
  })

  it('activates the matching navigation callback through the native click pathway', () => {
    const nav = stubNav({ hasPrev: true, hasNext: true, activeRevealId: 'b' })
    const container = mountToolbar(nav)

    const [first, prev, collapse, next, last] = toolbarButtons(container)
    act(() => { first!.click() })
    act(() => { prev!.click() })
    act(() => { collapse!.click() })
    act(() => { next!.click() })
    act(() => { last!.click() })

    expect(nav.goFirst).toHaveBeenCalledTimes(1)
    expect(nav.goPrev).toHaveBeenCalledTimes(1)
    expect(nav.collapse).toHaveBeenCalledTimes(1)
    expect(nav.goNext).toHaveBeenCalledTimes(1)
    expect(nav.goLast).toHaveBeenCalledTimes(1)
  })

  it('disables every button when there is nothing to navigate or collapse', () => {
    const container = mountToolbar(stubNav())

    for (const btn of toolbarButtons(container)) {
      expect(btn.disabled).toBe(true)
    }
  })

  it('disables first/prev without a previous target and next/last without a next target', () => {
    // Middle of the transcript with no oversized block on screen.
    const container = mountToolbar(
      stubNav({ hasPrev: false, hasNext: true, activeRevealId: null }),
    )
    const [first, prev, collapse, next, last] = toolbarButtons(container)
    expect(first!.disabled).toBe(true)
    expect(prev!.disabled).toBe(true)
    expect(collapse!.disabled).toBe(true)
    expect(next!.disabled).toBe(false)
    expect(last!.disabled).toBe(false)
  })

  it('enables collapse only while an oversized block is active', () => {
    const withActive = mountToolbar(
      stubNav({ hasPrev: false, hasNext: false, activeRevealId: 'block-7' }),
    )
    expect(toolbarButtons(withActive)[2]!.disabled).toBe(false)
  })

  it('disabled buttons never fire their callbacks', () => {
    const nav = stubNav() // everything disabled
    const container = mountToolbar(nav)

    for (const btn of toolbarButtons(container)) {
      act(() => { btn.click() })
    }
    expect(nav.goFirst).not.toHaveBeenCalled()
    expect(nav.goPrev).not.toHaveBeenCalled()
    expect(nav.collapse).not.toHaveBeenCalled()
    expect(nav.goNext).not.toHaveBeenCalled()
    expect(nav.goLast).not.toHaveBeenCalled()
  })

  it('styles with design tokens only — no raw colors', () => {
    const container = mountToolbar(
      stubNav({ hasPrev: true, hasNext: true, activeRevealId: 'b' }),
    )
    const group = container.querySelector('[role="group"]')!
    // Container tokens: surface, border, elevation from the theme variables.
    for (const token of ['border-border', 'bg-background/90', 'shadow-lg', 'rounded-full']) {
      expect(group.className).toContain(token)
    }
    // No raw hex/rgb color anywhere in the toolbar's class attributes —
    // every color routes through a theme token. (getAttribute('class'), not
    // .className: the icon <svg>'s className is an SVGAnimatedString.)
    for (const el of Array.from(group.querySelectorAll('*'))) {
      const cls = el.getAttribute('class') ?? ''
      expect(cls).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
      expect(cls).not.toMatch(/\brgb\(/)
    }
    for (const btn of toolbarButtons(container)) {
      expect(btn.className).toContain('text-muted-foreground')
      expect(btn.className).toContain('disabled:opacity-40')
    }
  })
})

// --- Integration: the sticky bottom stack inside ChatScrollManager ----------

// Live-geometry rect helper (useOversizedBlockNav.test.tsx conventions).
function rectOf(top: number, height: number): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as unknown as DOMRect
}

// rAF stub: a manually flushed queue so the hook's scroll-throttled scans are
// deterministic and observable.
const frames: FrameRequestCallback[] = []
function flushFrames(): void {
  const pending = frames.splice(0, frames.length)
  for (const cb of pending) cb(0)
}

interface BlockGeom {
  offsetHeight: number
  top: number
  height: number
}

interface ManagerFixture {
  viewport: HTMLDivElement
  rerender: (messages: ChatMessageUI[]) => void
  blocks: Map<string, HTMLElement>
  geom: Record<string, BlockGeom>
}

/** Render the real ChatScrollManager with three expandable blocks and wire
 *  live per-element geometry into them (viewport rect pinned to [0, 600]). */
function renderManager(initialMessages: ChatMessageUI[]): ManagerFixture {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
  root = createRoot(container)

  const rerender = (messages: ChatMessageUI[]) =>
    act(() => {
      root!.render(
        <ScrollProvider>
          <ChatScrollManager sessionId={null} messages={messages} streamingText={undefined} scrollRef={scrollRef}>
            <div data-transcript-content>
              <div data-chevron-reveal-id="a" data-state="open" />
              <div data-chevron-reveal-id="b" data-state="open" />
              <div data-chevron-reveal-id="c" data-state="closed" />
            </div>
          </ChatScrollManager>
        </ScrollProvider>,
      )
    })
  rerender(initialMessages)

  const viewport = scrollRef.current!
  const blocks = new Map<string, HTMLElement>()
  const geom: Record<string, BlockGeom> = {}
  for (const id of ['a', 'b', 'c']) {
    const el = viewport.querySelector<HTMLElement>(`[data-chevron-reveal-id="${id}"]`)!
    blocks.set(id, el)
    geom[id] = { offsetHeight: 200, top: 0, height: 200 }
    Object.defineProperty(el, 'offsetHeight', {
      configurable: true,
      get: () => geom[id]!.offsetHeight,
    })
    vi.spyOn(el, 'getBoundingClientRect').mockImplementation(
      () => rectOf(geom[id]!.top, geom[id]!.height),
    )
  }
  Object.defineProperty(viewport, 'clientHeight', { configurable: true, get: () => 600 })
  vi.spyOn(viewport, 'getBoundingClientRect').mockImplementation(() => rectOf(0, 600))
  viewport.scrollTo = vi.fn() as unknown as typeof viewport.scrollTo
  return { viewport, rerender, blocks, geom }
}

/** Dispatch a viewport scroll and flush the hook's scheduled rAF scan. */
function scrollAndFlush(viewport: HTMLElement): void {
  act(() => {
    viewport.dispatchEvent(new Event('scroll'))
    flushFrames()
  })
}

function stackButtons(viewport: HTMLElement): HTMLButtonElement[] {
  return Array.from(
    viewport.querySelectorAll<HTMLButtonElement>(
      '[aria-label="Expandable block navigation"] button',
    ),
  )
}

function stackButton(viewport: HTMLElement, label: string): HTMLButtonElement {
  const btn = viewport.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)
  expect(btn).not.toBeNull()
  return btn!
}

describe('ChatScrollManager — single sticky bottom stack', () => {
  beforeEach(() => {
    frames.length = 0
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      frames.push(cb)
      return frames.length
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    useChatStore.setState({ scrollPositions: {}, taskActive: {} })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('hosts the toolbar in one sticky wrapper — no other sticky element, banner-free stack has a single child', () => {
    const f = renderManager([])

    const stickyEls = f.viewport.querySelectorAll('[class*="sticky"]')
    expect(stickyEls).toHaveLength(1)
    const stack = stickyEls[0]!
    // The wrapper owns stickiness, centering and the pointer-events regime.
    expect(stack.className).toContain('bottom-2')
    expect(stack.className).toContain('pointer-events-none')
    // Children stack vertically inside it — the only way banner and toolbar
    // can coexist without overlapping.
    expect(stack.className).toContain('flex-col')
    // Without new activity only the toolbar renders.
    expect(f.viewport.querySelector('button[aria-label="Jump to new activity"]')).toBeNull()
    expect(stack.children).toHaveLength(1)
    expect(stack.children[0]!.getAttribute('aria-label')).toBe('Expandable block navigation')
  })

  it('stacks the banner above the toolbar with the banner carrying no stickiness of its own', () => {
    vi.spyOn(window.Element.prototype, 'scrollHeight', 'get').mockReturnValue(10_000)
    vi.spyOn(window.Element.prototype, 'clientHeight', 'get').mockReturnValue(600)
    const f = renderManager([message('m1')])

    // Reader scrolls away from the bottom, then new output lands: the pill
    // must appear in the SAME sticky stack as the toolbar, above it.
    act(() => {
      f.viewport.scrollTop = 3_000
      f.viewport.dispatchEvent(new Event('scroll'))
      flushFrames()
    })
    f.rerender([message('m1'), message('m2')])

    const stack = f.viewport.querySelector('[class*="sticky"]')!
    expect(stack.children).toHaveLength(2)
    const [banner, toolbar] = Array.from(stack.children)
    expect(banner!.getAttribute('aria-label')).toBe('Jump to new activity')
    expect(toolbar!.getAttribute('aria-label')).toBe('Expandable block navigation')
    // Sticky positioning was dropped from the banner: the wrapper is the only
    // sticky element, so the two can never overlap.
    expect(banner!.className).not.toContain('sticky')
    // The interactive children re-enable pointer events over the inert stack.
    expect(banner!.className).toContain('pointer-events-auto')
    expect(toolbar!.className).toContain('pointer-events-auto')
  })

  it('derives the toolbar disabled states from the real navigation hook', () => {
    const f = renderManager([])

    // a above the viewport, b intersecting (anchor, oversized, open),
    // c closed below: prev/next targets exist and b is collapsible.
    f.geom.a = { offsetHeight: 200, top: -300, height: 200 }
    f.geom.b = { offsetHeight: 700, top: 100, height: 400 }
    f.geom.c = { offsetHeight: 900, top: 800, height: 100 }
    scrollAndFlush(f.viewport)

    const buttons = stackButtons(f.viewport)
    expect(buttons.map(b => b.disabled)).toEqual([false, false, false, false, false])

    // Scroll to the top: a is the anchor (index 0) and nothing oversized is
    // on screen — the "at the ends" buttons disable, next/last stay live.
    f.geom.a = { offsetHeight: 200, top: 50, height: 200 }
    f.geom.b = { offsetHeight: 700, top: 900, height: 400 }
    f.geom.c = { offsetHeight: 900, top: 1_400, height: 100 }
    scrollAndFlush(f.viewport)

    expect(stackButtons(f.viewport).map(b => b.disabled)).toEqual([true, true, true, false, false])
  })

  it('clicking a toolbar button navigates the real viewport through the wired hook', () => {
    const f = renderManager([])
    f.geom.a = { offsetHeight: 200, top: 50, height: 200 }
    f.geom.b = { offsetHeight: 700, top: 900, height: 400 }
    f.geom.c = { offsetHeight: 900, top: 1_400, height: 100 }
    scrollAndFlush(f.viewport)

    act(() => { stackButton(f.viewport, 'Next expandable block').click() })

    // Anchor a (index 0) → next target is b at top 900; no sticky user bar in
    // the fixture, so the block lands at the scrollport top.
    expect(f.viewport.scrollTo).toHaveBeenCalledWith({ top: 900, behavior: 'smooth' })
  })
})

function message(id: string): ChatMessageUI {
  return {
    id,
    sessionId: 's1',
    type: 'assistant',
    content: `content ${id}`,
    metadata: {},
    timestamp: 0,
  }
}
