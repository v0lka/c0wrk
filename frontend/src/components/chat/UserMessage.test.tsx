// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DisplayItem } from '@/types/messages'
import { UserMessage } from './UserMessage'

// The expanded bubble renders a CopyButton which calls `clipboardSetText` →
// the Wails runtime. Mock it so no real window.runtime binding is touched
// (mirrors CopyButton.test / MessageFooter.test).
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

let root: Root | null = null

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

function renderUser(sticky = false): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<UserMessage item={item} sticky={sticky} />)
  })
  return container
}

function findStickyRow(container: HTMLElement): Element | null {
  return container.querySelector('[data-message-id="user-1"]')
}

describe('UserMessage non-sticky mode', () => {
  beforeEach(() => {
    document.body.replaceChildren()
  })

  afterEach(() => {
    act(() => root?.unmount())
    root = null
  })

  it('renders the full bubble without collapse/expand controls', () => {
    const container = renderUser()
    const message = findStickyRow(container)

    expect(message).not.toBeNull()
    expect(message?.classList.contains('sticky')).toBe(false)
    // No interactive role in the non-sticky path.
    expect(message?.querySelector('[role="button"]')).toBeNull()
    expect(message?.querySelector('[aria-expanded]')).toBeNull()
    // Footer is present in full mode.
    expect(message?.textContent).toContain('A long user message')
    // The inline bubble is not a floating bar — no sticky marker.
    expect(message?.hasAttribute('data-sticky-user-message')).toBe(false)
  })
})

describe('UserMessage sticky mode', () => {
  beforeEach(() => {
    document.body.replaceChildren()
  })

  afterEach(() => {
    act(() => root?.unmount())
    root = null
  })

  it('collapses to a single truncated line by default', () => {
    const container = renderUser(true)
    const message = findStickyRow(container)

    expect(message?.classList.contains('sticky')).toBe(true)
    expect(message?.classList.contains('top-0')).toBe(true)
    expect(message?.classList.contains('z-10')).toBe(true)

    const trigger = message?.querySelector('[role="button"]')
    expect(trigger).not.toBeNull()
    // Collapsed initially.
    expect(trigger?.getAttribute('aria-expanded')).toBe('false')
    // The collapsed bubble is a single truncated line.
    const bubble = trigger?.querySelector('.truncate')
    expect(bubble).not.toBeNull()
    expect(bubble?.textContent).toBe('A long user message')
    // No footer rendered while collapsed.
    expect(message?.textContent).not.toMatch(/\d{2}:\d{2}/)
  })

  it('marks the floating row for sticky-aware chat scrolling in both states', () => {
    const container = renderUser(true)
    const message = findStickyRow(container)
    expect(message?.hasAttribute('data-sticky-user-message')).toBe(true)

    // The marker survives expand/collapse — the scroll offset is measured
    // live from the row, so both heights stay accounted for.
    const trigger = message?.querySelector('[role="button"]') as Element
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(findStickyRow(container)?.hasAttribute('data-sticky-user-message')).toBe(true)
  })

  it('expands to the full message on click, then collapses again on click', () => {
    const container = renderUser(true)
    const message = findStickyRow(container)
    const trigger = message?.querySelector('[role="button"]') as Element

    // Expand.
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    // Full content rendered (no truncation), footer visible.
    expect(message?.querySelector('.truncate')).toBeNull()
    expect(message?.textContent).toContain('A long user message')
    expect(message?.textContent).toMatch(/\d{2}:\d{2}/)

    // Collapse again.
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(message?.querySelector('.truncate')).not.toBeNull()
  })

  it('renders a gradient erasure strip below the floating bubble', () => {
    const container = renderUser(true)
    const message = findStickyRow(container)

    // The gradient fade element is the last child of the sticky wrapper.
    const children = Array.from(message?.children ?? [])
    const fade = children[children.length - 1]
    expect(fade).toBeTruthy()
    expect(fade?.className).toContain('bg-gradient-to-b')
    expect(fade?.className).toContain('from-background')
    expect(fade?.className).toContain('to-transparent')
    expect(fade?.className).toContain('pointer-events-none')
  })

  it('keeps exactly one DOM instance across collapse/expand toggles', () => {
    const container = renderUser(true)
    const before = container.querySelectorAll('[data-message-id="user-1"]')
    expect(before).toHaveLength(1)

    const trigger = container.querySelector('[role="button"]') as Element
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const after = container.querySelectorAll('[data-message-id="user-1"]')
    expect(after).toHaveLength(1)
  })

  it('does not collapse when clicking an interactive descendant while expanded', async () => {
    const container = renderUser(true)
    const message = findStickyRow(container)
    const trigger = message?.querySelector('[role="button"]') as Element

    // Expand first.
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(trigger.getAttribute('aria-expanded')).toBe('true')

    // Simulate a click on the copy button (a <button> descendant). The handler
    // inspects the event target's closest interactive ancestor and bails out.
    // Use the async act() so the CopyButton's clipboard promise (which calls
    // setCopied in a .then()) flushes its state update inside the act scope.
    const button = message?.querySelector('button') ?? null
    if (button) {
      await act(async () => {
        button.dispatchEvent(
          new MouseEvent('click', { bubbles: true }),
        )
      })
      expect(trigger.getAttribute('aria-expanded')).toBe('true')
    }
  })

  it('shows goal + attachment icons in collapsed sticky mode when metadata carries them', () => {
    const metaItem: Extract<DisplayItem, { kind: 'user' }> = {
      kind: 'user',
      message: {
        ...item.message,
        content: 'Implement the OWASP hardening',
        metadata: {
          goal: true,
          attachments: [
            { original_name: 'OWASP.pdf', format: 'pdf', size_bytes: 1000 },
            { original_name: 'notes.md', format: 'md', size_bytes: 500 },
          ],
          images: [
            { id: 'i1', name: 'shot.png', thumbnail: 'data:', path: '/x.png', media_type: 'image/png' },
          ],
        },
      },
    }

    const container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => {
      root!.render(<UserMessage item={metaItem} sticky />)
    })

    const trigger = container.querySelector('[role="button"]') as Element
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    // Goal icon (Target) present.
    const goalIcon = trigger.querySelector('[aria-label="Goal"]')
    expect(goalIcon).not.toBeNull()

    // Document count badge "2".
    expect(trigger.textContent).toContain('2')

    // Image count badge "1".
    const imgs = trigger.querySelectorAll('svg')
    // At least 3 icons: Target, FileText, ImageIcon.
    expect(imgs.length).toBeGreaterThanOrEqual(3)
  })

  it('shows the nudge icon in collapsed sticky mode when metadata carries is_nudge', () => {
    const nudgeItem: Extract<DisplayItem, { kind: 'user' }> = {
      kind: 'user',
      message: {
        ...item.message,
        content: 'keep going',
        metadata: { is_nudge: true },
      },
    }

    const container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => {
      root!.render(<UserMessage item={nudgeItem} sticky />)
    })

    const trigger = container.querySelector('[role="button"]') as Element
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    // Nudge icon (Zap) present in the collapsed indicators row.
    const nudgeIcon = trigger.querySelector('[aria-label="Nudge"]')
    expect(nudgeIcon).not.toBeNull()
    // No other indicators accompany a nudge-only message.
    expect(trigger.querySelector('[aria-label="Goal"]')).toBeNull()
    expect(trigger.textContent).toContain('keep going')
  })

  it('renders no indicators in collapsed mode for a plain-text message', () => {
    const container = renderUser(true)
    const trigger = container.querySelector('[role="button"]') as Element

    // No goal icon, no count badges.
    expect(trigger.querySelector('[aria-label="Goal"]')).toBeNull()
    expect(trigger.textContent).toBe('A long user message')
  })
})
