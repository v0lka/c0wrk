// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { GitGraphCardMeta } from './GitGraphCardMeta'
import { withVirtualTimers } from '@/test/virtualTime'
import type { CommitStats } from '@/types/models'

let container: HTMLDivElement
let root: Root

const STATS: CommitStats = { added: 2, modified: 1, deleted: 3 }

interface RenderOpts {
  author?: string
  date?: string
  refs?: string[]
  stats?: CommitStats | undefined
}

function render(opts: RenderOpts = {}) {
  // NOTE: no destructuring default for stats — an explicit `stats: undefined`
  // must stay undefined (the hidden-when-empty assertion depends on it).
  const { author = 'Jane', date = '2026-07-10T10:00:00Z', refs = [], stats } = opts
  act(() => {
    root.render(<GitGraphCardMeta author={author} date={date} refs={refs} stats={stats} />)
  })
}

/**
 * Stub offsetWidth so the measure pass sees the given geometry: the clip box
 * (the meta line's available width) reports `clip`, every element inside the
 * hidden measurement layer reports `natural`, everything else 0. Returns the
 * restore fn — call it in a finally so the prototype getter never leaks.
 */
function stubWidths(clip: number, natural: number): () => void {
  const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get(this: HTMLElement) {
      if (this.getAttribute('data-testid') === 'meta-clip') return clip
      if (this.closest('[aria-hidden]') !== null) return natural
      return 0
    },
  })
  return () => {
    if (original) Object.defineProperty(HTMLElement.prototype, 'offsetWidth', original)
  }
}

/** The visible (non-measurement-layer) refs area of the meta line. */
function visibleRefsArea(): HTMLElement {
  const area = container.querySelector<HTMLElement>('.pl-7 > .ml-auto')
  expect(area).toBeTruthy()
  return area!
}

/** A stand-in ResizeObserver: jsdom ships none, but Radix's use-size (on
 *  tooltip open) and the component's re-measure wiring both construct one —
 *  record callbacks and fire them to emulate real resizes. */
class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  private callback: ResizeObserverCallback
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    FakeResizeObserver.instances.push(this)
  }
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
  static fireAll(): void {
    for (const instance of FakeResizeObserver.instances) {
      instance.callback([], instance as unknown as ResizeObserver)
    }
  }
}

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  vi.unstubAllGlobals()
  container.remove()
  document.body.innerHTML = ''
})

describe('GitGraphCardMeta — layout', () => {
  it('renders author · time on the line', () => {
    render()
    expect(container.textContent).toContain('Jane ·')
    expect(container.querySelector('.pl-7')).toBeTruthy()
  })

  it('truncates the visible author span but keeps the measurement layer untruncated', () => {
    render()
    // First span child of the visible line = the author · time span.
    const visibleAuthor = container.querySelector<HTMLElement>('.pl-7 > span')
    expect(visibleAuthor?.className).toContain('min-w-0')
    expect(visibleAuthor?.className).toContain('truncate')
    // The hidden natural-width layer must never ellipsize, or the overflow
    // comparison would under-measure and refs would collapse too late.
    const layerAuthor = container.querySelector<HTMLElement>('[aria-hidden] span')
    expect(layerAuthor?.className).not.toContain('truncate')
    expect(layerAuthor?.className).not.toContain('min-w-0')
  })

  it('renders the stats triple and hides it when there is nothing to count', () => {
    render({ stats: STATS })
    expect(container.textContent).toContain('+2')
    expect(container.textContent).toContain('~1')
    expect(container.textContent).toContain('−3')
    render({ stats: undefined })
    expect(container.textContent).not.toContain('+2')
  })

  it('renders every ref badge (tag: prefix stripped) when the line fits', () => {
    const restore = stubWidths(400, 200)
    try {
      render({ refs: ['HEAD -> main', 'feature-x', 'tag: v1.0'] })
    } finally {
      restore()
    }
    const area = visibleRefsArea()
    expect(area.textContent).toContain('main')
    expect(area.textContent).toContain('feature-x')
    expect(area.textContent).toContain('v1.0')
    expect(area.textContent).not.toContain('tag:')
    // No collapsed badge when everything fits.
    expect(container.querySelector('[data-testid="collapsed-refs-badge"]')).toBeNull()
  })
})

describe('GitGraphCardMeta — overflow collapse', () => {
  it('keeps only the first ref and collapses the rest into a «+N» badge', () => {
    const restore = stubWidths(100, 400)
    try {
      render({ refs: ['main', 'feature-x', 'release-branch', 'tag: v1.0'] })
    } finally {
      restore()
    }
    const area = visibleRefsArea()
    // First ref stays, the rest are gone from the visible line…
    expect(area.textContent).toContain('main')
    expect(area.textContent).not.toContain('feature-x')
    expect(area.textContent).not.toContain('release-branch')
    expect(area.textContent).not.toContain('v1.0')
    // …and show up in the «+N» badge.
    const badge = container.querySelector('[data-testid="collapsed-refs-badge"]') as HTMLElement
    expect(badge).toBeTruthy()
    expect(badge.textContent).toContain('+3')
  })

  it('badges the collapsed refs with the first hidden ref\'s kind icon', () => {
    const restore = stubWidths(100, 400)
    try {
      render({ refs: ['main', 'feature-x', 'tag: v1.0'] })
    } finally {
      restore()
    }
    // First hidden ref is a branch → GitBranch glyph on the badge.
    const badge = container.querySelector('[data-testid="collapsed-refs-badge"]') as HTMLElement
    expect(badge.querySelector('svg.lucide-git-branch')).not.toBeNull()
    expect(badge.querySelector('svg.lucide-tag')).toBeNull()
  })

  it('uses the Tag glyph when the first hidden ref is a tag', () => {
    const restore = stubWidths(100, 400)
    try {
      render({ refs: ['main', 'tag: v1.0', 'tag: v2.0'] })
    } finally {
      restore()
    }
    const badge = container.querySelector('[data-testid="collapsed-refs-badge"]') as HTMLElement
    expect(badge.querySelector('svg.lucide-tag')).not.toBeNull()
    expect(badge.querySelector('svg.lucide-git-branch')).toBeNull()
  })

  it('a single overflowing ref cannot collapse (there is nothing to fold)', () => {
    const restore = stubWidths(10, 400)
    try {
      render({ refs: ['main'] })
    } finally {
      restore()
    }
    expect(visibleRefsArea().textContent).toContain('main')
    expect(container.querySelector('[data-testid="collapsed-refs-badge"]')).toBeNull()
  })

  it('shows all refs again when the line fits after having collapsed', () => {
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    const restore = stubWidths(100, 400)
    try {
      render({ refs: ['main', 'feature-x', 'release-branch'] })
    } finally {
      restore()
    }
    expect(container.querySelector('[data-testid="collapsed-refs-badge"]')).not.toBeNull()

    // Widths grow: the ResizeObserver fires and the next measure pass
    // re-expands the refs.
    const restoreWide = stubWidths(400, 200)
    try {
      act(() => {
        FakeResizeObserver.fireAll()
      })
    } finally {
      restoreWide()
    }
    expect(container.querySelector('[data-testid="collapsed-refs-badge"]')).toBeNull()
    expect(visibleRefsArea().textContent).toContain('release-branch')
  })
})

describe('GitGraphCardMeta — collapsed-refs tooltip', () => {
  it('lists every collapsed ref (tag prefix stripped) with its kind icon on hover', async () => {
    // Radix's use-size constructs a ResizeObserver when the tooltip opens.
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    const restore = stubWidths(100, 400)
    try {
      render({ refs: ['main', 'feature-x', 'tag: v1.0', 'release-branch'] })
    } finally {
      restore()
    }
    const badge = container.querySelector('[data-testid="collapsed-refs-badge"]') as HTMLElement
    expect(badge).toBeTruthy()
    // Radix opens the tooltip from pointer movement over the trigger
    // (pointerType must not be 'touch'), through its delay timer — advanced
    // on the virtual clock (test/virtualTime; the timing-guard pattern).
    await withVirtualTimers(() => {
      const move = new Event('pointermove', { bubbles: true })
      Object.defineProperty(move, 'pointerType', { value: 'mouse' })
      badge.dispatchEvent(move)
    }, 1100)
    const content = document.querySelector('[data-slot="tooltip-content"]')
    expect(content).toBeTruthy()
    const text = content!.textContent ?? ''
    // Every HIDDEN ref is listed; the visible first ref is not.
    expect(text).toContain('feature-x')
    expect(text).toContain('v1.0')
    expect(text).toContain('release-branch')
    expect(text).not.toContain('main')
    // Per-kind icons in the VISIBLE list (Radix repeats the content in a
    // visually-hidden role="tooltip" description — scope away from it):
    // two branches + one tag.
    const list = content!.querySelector(':scope > div')
    expect(list).toBeTruthy()
    expect(list!.querySelectorAll('svg.lucide-git-branch').length).toBe(2)
    expect(list!.querySelectorAll('svg.lucide-tag').length).toBe(1)
  })
})
