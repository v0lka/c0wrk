// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DisplayItem } from '@/types/messages'
import { UserMessage } from './UserMessage'

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

let measuredHeight = 0
let resizeCallback: ResizeObserverCallback | null = null
let root: Root | null = null

class ResizeObserverMock {
  constructor(callback: ResizeObserverCallback) {
    resizeCallback = callback
  }

  observe() {}
  unobserve() {}
  disconnect() {}
}

const item: Extract<DisplayItem, { kind: 'user' }> = {
  kind: 'user',
  message: {
    id: 'user-1',
    sessionId: 'session-1',
    type: 'user',
    content: 'A long user message',
    metadata: {},
    timestamp: 0,
  },
}

function renderPinned(): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<UserMessage item={item} isPinned maxHeight={100} />)
  })
  return container.firstElementChild as HTMLElement
}

function notifyResize(): void {
  act(() => {
    resizeCallback?.([], {} as ResizeObserver)
  })
}

describe('UserMessage pinned height', () => {
  beforeEach(() => {
    measuredHeight = 0
    resizeCallback = null
    document.body.replaceChildren()
    vi.stubGlobal('ResizeObserver', ResizeObserverMock)
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(() => measuredHeight)
  })

  afterEach(() => {
    act(() => root?.unmount())
    root = null
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('applies the height limit before the first successful measurement', () => {
    const pinned = renderPinned()

    expect(pinned.style.maxHeight).toBe('100px')
    expect(pinned.style.overflow).toBe('hidden')
  })

  it('keeps overflowing content clipped until the user expands it', () => {
    const pinned = renderPinned()
    measuredHeight = 240
    notifyResize()

    expect(pinned.style.maxHeight).toBe('100px')
    expect(pinned.getAttribute('role')).toBe('button')
    expect(pinned.getAttribute('aria-expanded')).toBe('false')

    act(() => pinned.click())

    expect(pinned.style.maxHeight).toBe('')
    expect(pinned.style.overflow).toBe('')
    expect(pinned.getAttribute('aria-expanded')).toBe('true')
  })

  it('removes the fail-safe limit after measuring short content', () => {
    const pinned = renderPinned()
    measuredHeight = 60
    notifyResize()

    expect(pinned.style.maxHeight).toBe('')
    expect(pinned.style.overflow).toBe('')
    expect(pinned.getAttribute('role')).toBeNull()
  })
})
