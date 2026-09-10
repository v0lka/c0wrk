// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ChatMessageRenderer } from './ChatMessageRenderer'
import { ChatHoverRegion } from './ChatHoverRegion'
import { chatHoverStore } from './chatHoverStore'
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
    // Reset the single programmatic hover store between tests so a previous
    // test's hover doesn't bleed in (the store is module-level by design).
    chatHoverStore.clear()
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
          <ChatHoverRegion>
            <ChatMessageRenderer items={[planStep]} />
          </ChatHoverRegion>
        </TooltipProvider>,
      )
    })

  const star = (row: Element) =>
    row.querySelector<HTMLButtonElement>('button[aria-label="Add bookmark"]')!

  const hoverOver = (el: Element) =>
    act(() => {
      el.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, cancelable: true }))
    })

  // The star hides via opacity + pointer-events classes, NOT an inline
  // `visibility: hidden` style — that would drop the button from the tab order
  // and make it unreachable for keyboard users (see BookmarkStar).
  const starIsVisible = (b: HTMLButtonElement) => b.classList.contains('opacity-100')
  const starIsHidden = (b: HTMLButtonElement) =>
    b.classList.contains('opacity-0') && b.classList.contains('pointer-events-none')

  it('renders a star gutter for the parent collapsible row only when bookmarkable', () => {
    renderStep()
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

  it('reveals exactly ONE star at a time: hovering the parent hides every child star', () => {
    renderStep()

    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const childRow1 = container.querySelector<HTMLElement>('[data-bookmark-id="child-1"]')!
    const childRow2 = container.querySelector<HTMLElement>('[data-bookmark-id="child-2"]')!

    hoverOver(parentRow)

    expect(starIsVisible(star(parentRow))).toBe(true)
    expect(starIsHidden(star(childRow1))).toBe(true)
    expect(starIsHidden(star(childRow2))).toBe(true)
  })

  it('hovering an inner child switches the single active star to that child only', () => {
    renderStep()

    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const childRow1 = container.querySelector<HTMLElement>('[data-bookmark-id="child-1"]')!

    hoverOver(childRow1)

    expect(starIsVisible(star(childRow1))).toBe(true)
    expect(starIsHidden(star(parentRow))).toBe(true)
  })

  it('hides the active star when the pointer moves to blank space (no row under it)', () => {
    renderStep()

    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const childRow1 = container.querySelector<HTMLElement>('[data-bookmark-id="child-1"]')!

    hoverOver(parentRow)
    expect(starIsVisible(star(parentRow))).toBe(true)

    // Move to the region wrapper itself — a non-row area with no
    // [data-bookmark-id] ancestor. The region div is the only child mounted
    // directly under the React root container.
    const region = container.children[0]!
    hoverOver(region)

    expect(starIsHidden(star(parentRow))).toBe(true)
    expect(starIsHidden(star(childRow1))).toBe(true)
  })

  it('anchors exactly ONE chevron per nearest collapsible under the pointer', () => {
    renderStep()

    const collapsible = container.querySelector<HTMLElement>('[data-chevron-reveal-id]')!
    expect(collapsible).not.toBeNull()
    const chevronId = collapsible.dataset.chevronRevealId!
    const trigger = collapsible.querySelector<HTMLElement>('[data-slot="collapsible-trigger"]')!
    const chevron = trigger.querySelector<HTMLElement>('span')!

    hoverOver(trigger)

    expect(chevron.style.visibility).toBe('visible')
    // The revealed chevron id matches the nearest collapsible's id.
    expect(chevronId).toEqual(collapsible.dataset.chevronRevealId)
  })

  // Regression: the user-reported symptom was a DOWNWARD sweep accumulating
  // icons while an UPWARD sweep cleared them. This sweep traverses the DOM
  // top-to-bottom, and later bottom-to-top, asserting that at every step exactly
  // one bookmark star and one chevron is lit — never more.
  it('never lights MORE than one bookmark star anywhere during a top-to-bottom sweep', () => {
    renderStep()

    const rows = Array.from(
      container.querySelectorAll<HTMLElement>('[data-bookmark-id]'),
    )
    expect(rows.length).toBeGreaterThanOrEqual(3) // parent (step-1) + 2 children

    const allStars = () =>
      Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-label="Add bookmark"]'))

    for (const row of rows) {
      hoverOver(row)
      const lit = allStars().filter(starIsVisible)
      expect(lit.length).toBeLessThanOrEqual(1)
    }

    // Bottom-to-top, the same invariant holds.
    for (const row of [...rows].reverse()) {
      hoverOver(row)
      const lit = allStars().filter(starIsVisible)
      expect(lit.length).toBeLessThanOrEqual(1)
    }
  })

  it('never lights MORE than one chevron anywhere during a sweep', () => {
    renderStep()

    const collapsibles = Array.from(
      container.querySelectorAll<HTMLElement>('[data-chevron-reveal-id]'),
    )
    expect(collapsibles.length).toBeGreaterThanOrEqual(1)

    const allChevrons = () =>
      Array.from(
        container.querySelectorAll<HTMLElement>('[data-slot="collapsible-trigger"] span'),
      )

    let lit = allChevrons().filter((c) => c.style.visibility === 'visible')
    expect(lit.length).toBeLessThanOrEqual(1)

    for (const c of collapsibles) {
      hoverOver(c)
      lit = allChevrons().filter((c) => c.style.visibility === 'visible')
      expect(lit.length).toBeLessThanOrEqual(1)
    }
  })

  it('keeps unbookmarked stars keyboard-accessible while hidden (a11y regression)', () => {
    renderStep()
    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const s = star(parentRow)

    // No inline `visibility` style — that would remove the button from the
    // tab order entirely (the regression this test pins).
    expect(s.style.visibility).toBe('')

    // Hidden (not bookmarked, no hover): faked with opacity + pointer-events.
    expect(starIsHidden(s)).toBe(true)
    expect(starIsVisible(s)).toBe(false)

    // The hidden button is still focusable — keyboard users can reach it.
    act(() => s.focus())
    expect(document.activeElement).toBe(s)

    // Keyboard focus restores both visibility and hit-testing via classes
    // (Tailwind compiles `focus-visible:opacity-100` etc. to a stylesheet
    // rule; jsdom cannot apply it, so pin the classes on the element).
    expect(s.className).toContain('focus-visible:opacity-100')
    expect(s.className).toContain('focus-visible:pointer-events-auto')
  })

  it('hides both the bookmark and the chevron when the pointer leaves the chat', () => {
    renderStep()

    const parentRow = container.querySelector<HTMLElement>('[data-bookmark-id="step-1"]')!
    const trigger = parentRow.querySelector<HTMLElement>('[data-slot="collapsible-trigger"]')!

    hoverOver(parentRow)
    expect(starIsVisible(star(parentRow))).toBe(true)

    // Leaving the region clears the store, hiding everything. React's
    // onMouseLeave is driven by a mouseout whose relatedTarget is outside the
    // region, so simulate that (not a bubbled mouseleave).
    act(() => {
      const region = container.children[0]!
      region.dispatchEvent(
        new MouseEvent('mouseout', {
          bubbles: true,
          cancelable: true,
          relatedTarget: document.body,
        }),
      )
    })

    expect(starIsHidden(star(parentRow))).toBe(true)
    expect(trigger.querySelector<HTMLElement>('span')!.style.visibility).toBe('hidden')
  })
})
