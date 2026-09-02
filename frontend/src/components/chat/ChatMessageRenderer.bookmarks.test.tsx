// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ChatMessageRenderer } from './ChatMessageRenderer'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'

// bookmark store loads via RPC on session switch; not exercised here, and the
// star reads an empty session's bookmarks (stable empty array) so no API call.
vi.mock('@/api/bookmarks', () => ({
  addBookmark: vi.fn().mockResolvedValue({}),
  listBookmarks: vi.fn().mockResolvedValue([]),
  deleteBookmark: vi.fn().mockResolvedValue({}),
  renameBookmark: vi.fn().mockResolvedValue({}),
}))

let container: HTMLElement
let root: Root | null

function message(id: string, type: ChatMessageUI['type'], content: string): ChatMessageUI {
  return {
    id,
    sessionId: 'session-1',
    type,
    content,
    metadata: {},
    timestamp: 0,
  }
}

function fireMouseOver(el: HTMLElement) {
  act(() => {
    el.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
  })
}

function fireMouseLeave(el: HTMLElement) {
  act(() => {
    el.dispatchEvent(new MouseEvent('mouseout', { bubbles: true, relatedTarget: document.body }))
  })
}

describe('ChatMessageRenderer bookmark row hover scoping', () => {
  const planStep: DisplayItem = {
    kind: 'plan_step',
    id: 'step-1',
    stepId: 'step-1',
    stepNum: 1,
    title: 'Implement auth',
    description: 'Implement auth middleware',
    status: 'running',
    children: [
      { kind: 'assistant', message: message('child-1', 'assistant', 'child answer one') },
      { kind: 'assistant', message: message('child-2', 'assistant', 'child answer two') },
    ],
  }

  beforeEach(() => {
    document.body.replaceChildren()
    // Radix Presence schedules enter/exit animations via requestAnimationFrame,
    // which jsdom lacks; run frames synchronously so CollapsibleContent mounts
    // its children. ResizeObserver is likewise absent in jsdom.
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    })
    vi.stubGlobal('cancelAnimationFrame', () => {})
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    act(() => root?.unmount())
    root = null
  })

  const renderStep = () =>
    act(() => {
      root!.render(
        <TooltipProvider>
          <ChatMessageRenderer items={[planStep]} />
        </TooltipProvider>,
      )
    })

  it('renders a star gutter for the parent collapsible row only when bookmarkable', () => {
    renderStep()
    // Parent plan_step row carries a data-bookmark-id anchor + its own star.
    const parentRow = container.querySelector('[data-bookmark-id="step-1"]')
    expect(parentRow).not.toBeNull()
    expect(parentRow!.querySelector('button[aria-label="Add bookmark"]')).not.toBeNull()
  })

  it('makes nested children bookmarkable again (each child gets its own star)', () => {
    renderStep()
    const childRow1 = container.querySelector('[data-bookmark-id="child-1"]')
    const childRow2 = container.querySelector('[data-bookmark-id="child-2"]')
    expect(childRow1).not.toBeNull()
    expect(childRow2).not.toBeNull()
    expect(childRow1!.querySelector('button[aria-label="Add bookmark"]')).not.toBeNull()
    expect(childRow2!.querySelector('button[aria-label="Add bookmark"]')).not.toBeNull()
  })

  it('reveals only the hovered row star, not nested children stars', () => {
    renderStep()

    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const childRow1 = container.querySelector<HTMLElement>('[data-bookmark-id="child-1"]')!

    const parentStar = parentRow.querySelector<HTMLButtonElement>('button[aria-label="Add bookmark"]')!
    const childStar = childRow1.querySelector<HTMLButtonElement>('button[aria-label="Add bookmark"]')!

    // Initially hidden.
    expect(parentStar.className).toContain('opacity-0')
    expect(childStar.className).toContain('opacity-0')

    // Hover the parent row: only the parent star becomes visible.
    fireMouseOver(parentRow)
    expect(parentStar.className).toContain('opacity-100')
    expect(childStar.className).toContain('opacity-0')

    // Leave the parent, hover the child: child star becomes visible.
    fireMouseLeave(parentRow)
    fireMouseOver(childRow1)
    expect(childStar.className).toContain('opacity-100')
  })
})
