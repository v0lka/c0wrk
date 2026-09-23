// @vitest-environment jsdom
// Unit tests for the oversized-block navigation hook. The hook derives
// everything from viewport geometry, which jsdom does not lay out, so each
// fixture wires LIVE geometry (instance getters reading a mutable `geom`
// object) into the viewport and its blocks — tests then mutate `geom` and
// rescan to observe the derivation.
//
// - requestAnimationFrame is stubbed into a manually-flushed queue (the
//   hook's rAF throttle becomes deterministic and observable).
// - ResizeObserver is stubbed with instances recorded for manual firing.
// - Date is faked so the 500ms navigation-suppression window is advanced
//   deterministically via vi.setSystemTime.
// - scrollBlockStartIntoView is mocked so navigation asserts TARGET
//   IDENTITY; its geometry math is covered by chatScroll.test.ts.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/lib/chatScroll', () => ({
  scrollBlockStartIntoView: vi.fn(),
}))

import { useOversizedBlockNav } from './useOversizedBlockNav'
import type { OversizedBlockNavApi, OversizedBlockNavOptions } from './useOversizedBlockNav'
import { collapsibleRegistry } from './collapsibleRegistry'
import { scrollBlockStartIntoView } from '@/lib/chatScroll'

interface BlockSpec {
  id: string
  open: boolean
}

interface BlockGeom {
  offsetHeight: number
  top: number
  height: number
}

interface Fixture {
  viewport: HTMLElement
  content: HTMLElement
  blocks: Map<string, HTMLElement>
  geom: {
    viewport: { clientHeight: number; top: number }
    blocks: Record<string, BlockGeom>
  }
}

/** Live-geometry rect from the mutable spec. */
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

function buildFixture(specs: BlockSpec[], clientHeight = 600): Fixture {
  const viewport = document.createElement('div')
  const content = document.createElement('div')
  viewport.appendChild(content)
  document.body.appendChild(viewport)

  const blocks = new Map<string, HTMLElement>()
  const blockGeom: Record<string, BlockGeom> = {}
  for (const spec of specs) {
    const el = document.createElement('div')
    el.setAttribute('data-chevron-reveal-id', spec.id)
    el.setAttribute('data-state', spec.open ? 'open' : 'closed')
    content.appendChild(el)
    blocks.set(spec.id, el)
    blockGeom[spec.id] = { offsetHeight: 100, top: 0, height: 100 }
  }

  const geom: Fixture['geom'] = { viewport: { clientHeight, top: 0 }, blocks: blockGeom }

  Object.defineProperty(viewport, 'clientHeight', {
    configurable: true,
    get: () => geom.viewport.clientHeight,
  })
  vi.spyOn(viewport, 'getBoundingClientRect').mockImplementation(
    () => rectOf(geom.viewport.top, geom.viewport.clientHeight),
  )
  for (const [id, el] of blocks) {
    Object.defineProperty(el, 'offsetHeight', {
      configurable: true,
      get: () => geom.blocks[id]!.offsetHeight,
    })
    vi.spyOn(el, 'getBoundingClientRect').mockImplementation(
      () => rectOf(geom.blocks[id]!.top, geom.blocks[id]!.height),
    )
  }
  return { viewport, content, blocks, geom }
}

// --- rAF stub: a manually flushed queue ------------------------------------
const frames: FrameRequestCallback[] = []
const cancelSpy = vi.fn()
function flushFrames(): void {
  const pending = frames.splice(0, frames.length)
  for (const cb of pending) cb(0)
}

// --- ResizeObserver stub ----------------------------------------------------
class ResizeObserverStub {
  static instances: ResizeObserverStub[] = []
  static observed: Element[] = []
  observe = vi.fn((el: Element) => {
    ResizeObserverStub.observed.push(el)
  })
  disconnect = vi.fn()
  private callback: ResizeObserverCallback
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    ResizeObserverStub.instances.push(this)
  }
  fire(): void {
    this.callback([], this as unknown as ResizeObserver)
  }
}

// --- module-under-test harness ----------------------------------------------
let api: OversizedBlockNavApi | undefined

function Harness({ viewportRef, options }: {
  viewportRef: React.RefObject<HTMLElement | null>
  options?: OversizedBlockNavOptions
}) {
  api = useOversizedBlockNav(viewportRef, options)
  return null
}

let root: Root
let container: HTMLDivElement
const viewportRef: React.RefObject<HTMLElement | null> = { current: null }

function mount(options?: OversizedBlockNavOptions): void {
  act(() => {
    root.render(<Harness viewportRef={viewportRef} options={options} />)
  })
}

/** Dispatch a viewport scroll event and flush the scheduled rAF scan. */
function scrollAndFlush(viewport: HTMLElement): void {
  act(() => {
    viewport.dispatchEvent(new Event('scroll'))
    flushFrames()
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
    frames.push(cb)
    return frames.length
  })
  vi.stubGlobal('cancelAnimationFrame', cancelSpy)
  ResizeObserverStub.instances = []
  ResizeObserverStub.observed = []
  vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  api = undefined
  viewportRef.current = null

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  // Drop any geometry the test left on body-level fixtures.
  document.body.replaceChildren()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

// Reusable fixture: A small open, B oversized open, C closed below.
// Viewport: clientHeight 600, rect [0, 600].
function standardFixture(): Fixture {
  const f = buildFixture([
    { id: 'a', open: true },
    { id: 'b', open: true },
    { id: 'c', open: false },
  ])
  // A: small, fully on screen.
  f.geom.blocks.a = { offsetHeight: 200, top: 100, height: 200 }
  // B: oversized, straddling the viewport bottom.
  f.geom.blocks.b = { offsetHeight: 800, top: 350, height: 800 }
  // C: oversized, entirely below the fold.
  f.geom.blocks.c = { offsetHeight: 900, top: 700, height: 900 }
  return f
}

describe('useOversizedBlockNav — oversized scan', () => {
  it('identifies the topmost open block that is taller than the viewport AND intersecting it', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    // A is on screen but small; B is oversized and intersects; C is oversized
    // but open? no — closed and below the fold. The current block is B.
    expect(api?.activeRevealId).toBe('b')
  })

  it('ignores an oversized open block that does not intersect the viewport', () => {
    const f = buildFixture([{ id: 'far', open: true }])
    f.geom.blocks.far = { offsetHeight: 2000, top: 800, height: 2000 }
    viewportRef.current = f.viewport
    mount()

    expect(api?.activeRevealId).toBeNull()
  })

  it('ignores an oversized CLOSED block that intersects the viewport', () => {
    const f = buildFixture([{ id: 'shut', open: false }])
    f.geom.blocks.shut = { offsetHeight: 2000, top: 0, height: 2000 }
    viewportRef.current = f.viewport
    mount()

    expect(api?.activeRevealId).toBeNull()
  })

  it('rescans on viewport scroll (rAF-throttled: one frame per burst)', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()
    expect(api?.activeRevealId).toBe('b')

    // Scroll B fully below the fold and C into view (open it) — the next
    // scan must move the active block to C.
    f.geom.blocks.b = { offsetHeight: 800, top: 700, height: 800 }
    const c = f.blocks.get('c')!
    c.setAttribute('data-state', 'open')
    f.geom.blocks.c = { offsetHeight: 900, top: 0, height: 900 }

    act(() => {
      f.viewport.dispatchEvent(new Event('scroll'))
    })
    // Throttle: a second scroll event inside the same frame schedules nothing.
    act(() => {
      f.viewport.dispatchEvent(new Event('scroll'))
    })
    expect(frames).toHaveLength(1)
    // Not yet applied — the frame has not run.
    expect(api?.activeRevealId).toBe('b')

    scrollAndFlush(f.viewport)
    expect(api?.activeRevealId).toBe('c')
  })

  it('rescans when the transcript content resizes (ResizeObserver on the content wrapper)', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    expect(ResizeObserverStub.observed).toContain(f.content)

    // Collapsing B shrinks the content: the resize fires, the scan re-runs.
    f.geom.blocks.b = { offsetHeight: 100, top: 350, height: 100 }
    act(() => {
      ro.fire()
      flushFrames()
    })
    expect(api?.activeRevealId).toBeNull()
  })

  it('rescans on window resize (viewport height feeds the oversized comparison)', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()
    expect(api?.activeRevealId).toBe('b')

    // Shrink the viewport so A (200px) becomes oversized too.
    f.geom.viewport.clientHeight = 150
    act(() => {
      window.dispatchEvent(new Event('resize'))
      flushFrames()
    })
    expect(api?.activeRevealId).toBe('a')
  })
})

describe('useOversizedBlockNav — hasPrev / hasNext', () => {
  it('derives from the document-order position of the topmost intersecting block', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    // Anchor is A (topmost intersecting expandable block, index 0 of [a,b,c]).
    expect(api?.hasPrev).toBe(false)
    expect(api?.hasNext).toBe(true)
  })

  it('falls back to above/below geometry when no block intersects the viewport', () => {
    const f = buildFixture([
      { id: 'above', open: false },
      { id: 'below', open: false },
    ])
    f.geom.blocks.above = { offsetHeight: 100, top: -300, height: 100 }
    f.geom.blocks.below = { offsetHeight: 100, top: 800, height: 100 }
    viewportRef.current = f.viewport
    mount()

    expect(api?.hasPrev).toBe(true)
    expect(api?.hasNext).toBe(true)
  })
})

describe('useOversizedBlockNav — navigation', () => {
  it('goNext steps through expandable blocks in document order without expanding them', () => {
    const f = standardFixture()
    const spies: Record<string, ReturnType<typeof vi.fn<(open: boolean) => void>>> = {
      a: vi.fn<(open: boolean) => void>(),
      b: vi.fn<(open: boolean) => void>(),
      c: vi.fn<(open: boolean) => void>(),
    }
    for (const [id, spy] of Object.entries(spies)) {
      collapsibleRegistry.register(id, spy)
    }
    viewportRef.current = f.viewport
    mount()

    // Anchor A → next is B.
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenCalledWith(f.viewport, f.blocks.get('b'))

    // Anchor moved to B → next is C.
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('c'))
    expect(api?.hasNext).toBe(false)

    // At the end: no further navigation.
    vi.mocked(scrollBlockStartIntoView).mockClear()
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()

    // Back up: C → B → A (and A → nothing).
    act(() => { api?.goPrev() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('b'))
    act(() => { api?.goPrev() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('a'))
    expect(api?.hasPrev).toBe(false)
    vi.mocked(scrollBlockStartIntoView).mockClear()
    act(() => { api?.goPrev() })
    expect(scrollBlockStartIntoView).not.toHaveBeenCalled()

    // Navigation NEVER touches the registry — targets are never expanded.
    for (const spy of Object.values(spies)) expect(spy).not.toHaveBeenCalled()
    for (const id of Object.keys(spies)) collapsibleRegistry.unregister(id, spies[id]!)
  })

  it('goFirst / goLast target the document-order ends', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    act(() => { api?.goLast() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('c'))
    act(() => { api?.goFirst() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('a'))
  })

  it('with no intersecting anchor, goNext/goPrev jump to the nearest block below/above', () => {
    const f = buildFixture([
      { id: 'above', open: false },
      { id: 'below', open: false },
    ])
    f.geom.blocks.above = { offsetHeight: 100, top: -300, height: 100 }
    f.geom.blocks.below = { offsetHeight: 100, top: 800, height: 100 }
    viewportRef.current = f.viewport
    mount()

    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('below'))
    // goNext pinned the anchor to `below`; goPrev returns to `above`.
    act(() => { api?.goPrev() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('above'))
  })

  it('writes isAtBottomRef=false and opens the auto-scroll suppression window', () => {
    const f = standardFixture()
    const isAtBottomRef = { current: true }
    const suppressAutoScrollUntilRef = { current: 0 }
    viewportRef.current = f.viewport
    const before = Date.now()
    mount({ isAtBottomRef, suppressAutoScrollUntilRef })

    act(() => { api?.goNext() })

    expect(isAtBottomRef.current).toBe(false)
    expect(suppressAutoScrollUntilRef.current).toBe(before + 500)
  })

  it('keeps the anchor pinned to the navigation target for the suppression window', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    // A → B.
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('b'))

    // Mid-flight scroll frames arrive while the smooth scroll settles. The
    // geometry (still showing A as topmost) must NOT re-anchor: goNext from
    // the pinned B must target C, not B.
    scrollAndFlush(f.viewport)
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('c'))

    // After the window expires, scans re-derive the anchor from geometry
    // (A is the topmost intersecting block) — goNext lands on B again.
    vi.setSystemTime(Date.now() + 600)
    scrollAndFlush(f.viewport)
    act(() => { api?.goNext() })
    expect(scrollBlockStartIntoView).toHaveBeenLastCalledWith(f.viewport, f.blocks.get('b'))
  })
})

describe('useOversizedBlockNav — collapse', () => {
  it('collapses the active oversized block via the registry (only ever false)', () => {
    const f = standardFixture()
    const setOpen = vi.fn<(open: boolean) => void>()
    collapsibleRegistry.register('b', setOpen)
    viewportRef.current = f.viewport
    mount()

    act(() => { api?.collapse() })
    expect(setOpen).toHaveBeenCalledTimes(1)
    expect(setOpen).toHaveBeenCalledWith(false)
    collapsibleRegistry.unregister('b', setOpen)
  })

  it('is a no-op while no oversized block is active', () => {
    const f = buildFixture([{ id: 'small', open: true }])
    f.geom.blocks.small = { offsetHeight: 100, top: 0, height: 100 }
    const setOpen = vi.fn<(open: boolean) => void>()
    collapsibleRegistry.register('small', setOpen)
    viewportRef.current = f.viewport
    mount()

    expect(api?.activeRevealId).toBeNull()
    act(() => { api?.collapse() })
    expect(setOpen).not.toHaveBeenCalled()
    collapsibleRegistry.unregister('small', setOpen)
  })
})

describe('useOversizedBlockNav — lifecycle', () => {
  it('cancels a pending scan frame and disconnects the observer on unmount', () => {
    const f = standardFixture()
    viewportRef.current = f.viewport
    mount()

    // Leave one frame pending.
    act(() => {
      f.viewport.dispatchEvent(new Event('scroll'))
    })
    expect(frames).toHaveLength(1)

    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    act(() => {
      root.unmount()
    })
    expect(cancelSpy).toHaveBeenCalled()
    expect(ro.disconnect).toHaveBeenCalled()
  })
})
