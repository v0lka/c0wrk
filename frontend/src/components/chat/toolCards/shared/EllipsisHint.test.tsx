// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { EllipsisHint } from './EllipsisHint'

describe('EllipsisHint', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    // jsdom lacks ResizeObserver, which the component measures overflow with.
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  const render = (el: React.ReactElement) =>
    act(() => {
      root.render(el)
    })

  it('renders the visible children', () => {
    render(
      <EllipsisHint fullText="the full long value" className="truncate">
        short
      </EllipsisHint>,
    )
    expect(container.textContent).toContain('short')
  })

  it('applies the truncating classes (min-w-0 + className)', () => {
    render(
      <EllipsisHint fullText="full" className="text-sm truncate">
        short
      </EllipsisHint>,
    )
    const span = container.querySelector('span')
    expect(span).not.toBeNull()
    expect(span!.className).toContain('min-w-0')
    expect(span!.className).toContain('truncate')
    expect(span!.className).toContain('text-sm')
  })

  it('renders a plain span without a title when fullText is empty', () => {
    render(
      <EllipsisHint fullText="" className="truncate">
        short
      </EllipsisHint>,
    )
    expect(container.querySelector('span')!.hasAttribute('title')).toBe(false)
    expect(container.textContent).toContain('short')
  })

  it('carries no title when the content does not overflow', () => {
    render(
      <EllipsisHint fullText="the full long value" className="truncate">
        short
      </EllipsisHint>,
    )
    // jsdom reports scrollWidth === clientWidth === 0 → the overflow gate is
    // off, so the span renders without a title.
    expect(container.querySelector('span')!.hasAttribute('title')).toBe(false)
  })

  it('reveals the full value via title with alwaysShow, regardless of overflow', () => {
    const full = 'very long untruncated value here that should wrap'
    render(
      <EllipsisHint fullText={full} alwaysShow className="truncate">
        short
      </EllipsisHint>,
    )
    expect(container.querySelector('span')!.getAttribute('title')).toBe(full)
  })

  it('sets the title when the measured content overflows', () => {
    // jsdom has no layout — force every element to report overflow so the
    // mount measurement flips the overflow gate on.
    const scrollDesc = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollWidth')!
    const clientDesc = Object.getOwnPropertyDescriptor(Element.prototype, 'clientWidth')!
    Object.defineProperty(Element.prototype, 'scrollWidth', { configurable: true, get: () => 400 })
    Object.defineProperty(Element.prototype, 'clientWidth', { configurable: true, get: () => 100 })
    try {
      const full = 'a value too wide for the truncated span'
      render(
        <EllipsisHint fullText={full} className="truncate">
          short
        </EllipsisHint>,
      )
      expect(container.querySelector('span')!.getAttribute('title')).toBe(full)
    } finally {
      Object.defineProperty(Element.prototype, 'scrollWidth', scrollDesc)
      Object.defineProperty(Element.prototype, 'clientWidth', clientDesc)
    }
  })
})
