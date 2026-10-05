// @vitest-environment jsdom
// ItemAction tests.
//
// The button is the shared action primitive behind every sidebar/GitPanel/
// research/bookmarks row overlay (see ItemAction.tsx header). The row overlay
// reveals it on hover, and the row itself already shows a pointer cursor — so
// the button MUST carry its own hover/active feedback to look alive. These
// tests pin that contract (background tint + active press state), the click
// and disabled behavior, and the tooltip contract: the native `title` is the
// ONLY tooltip — a disabled action carries its reason as the wrapper span's
// title (the button itself never receives pointer events, so its own title
// would never show).
//
// Rendering follows the project convention (ModelCombobox.test.tsx):
// createRoot + act avoids the legacy react-dom/test-utils warning.

import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { ItemAction } from './ItemAction'

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return container
}

const findButton = (container: HTMLElement): HTMLButtonElement => {
  const button = container.querySelector('button')
  if (!button) throw new Error('ItemAction button not found')
  return button
}

describe('ItemAction', () => {
  it('renders hover and active press feedback on the button', () => {
    const container = render(
      <ItemAction label="Delete" onClick={() => {}}>
        <span>icon</span>
      </ItemAction>,
    )
    const button = findButton(container)
    // Hover tint + active press state — the overlay rows only reveal the
    // button on row hover; the button itself must visibly respond to the
    // pointer (cursor is inherited as pointer from the row).
    expect(button.className).toContain('transition-colors')
    expect(button.className).toContain('enabled:hover:bg-accent/20')
    expect(button.className).toContain('enabled:active:bg-accent/30')
  })

  it('carries the label as the native title (no Radix tooltip)', () => {
    const container = render(
      <ItemAction label="Delete" onClick={() => {}}>
        <span>icon</span>
      </ItemAction>,
    )
    expect(findButton(container).title).toBe('Delete')
  })

  it('invokes onClick on click', () => {
    const onClick = vi.fn()
    const container = render(
      <ItemAction label="Rename" onClick={onClick}>
        <span>icon</span>
      </ItemAction>,
    )
    act(() => {
      findButton(container).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('keeps the disabled look inert (pointer-events-none + opacity)', () => {
    const onClick = vi.fn()
    const container = render(
      <ItemAction label="Fork session" onClick={onClick} disabled disabledReason="busy">
        <span>icon</span>
      </ItemAction>,
    )
    const button = findButton(container)
    expect(button.disabled).toBe(true)
    expect(button.className).toContain('pointer-events-none')
    expect(button.className).toContain('opacity-30')
    // The wrapper span stays focusable so the disabledReason tooltip works.
    const wrapper = container.querySelector('button')?.parentElement
    expect(wrapper?.tagName).toBe('SPAN')
    expect(wrapper?.className).toContain('cursor-not-allowed')
  })

  it('exposes the disabledReason as the wrapper span title for a disabled action', () => {
    const container = render(
      <ItemAction label="Fork session" onClick={() => {}} disabled disabledReason="busy">
        <span>icon</span>
      </ItemAction>,
    )
    // The button itself never receives pointer events when disabled, so the
    // reason must live on the focusable wrapper span (it is mirrored on the
    // button's own title for assistive tech).
    const button = findButton(container)
    expect(button.parentElement?.title).toBe('busy')
    expect(button.title).toBe('busy')
  })

  it('carries "label: reason" as the wrapper span accessible name while disabled', () => {
    const container = render(
      <ItemAction label="Fork session" onClick={() => {}} disabled disabledReason="busy">
        <span>icon</span>
      </ItemAction>,
    )
    // The wrapper span is the tab stop while disabled and its disabled
    // button contents are excluded from accessible-name computation, so the
    // action name would otherwise vanish from the a11y tree.
    const wrapper = findButton(container).parentElement
    expect(wrapper?.getAttribute('aria-label')).toBe('Fork session: busy')
  })

  it('ignores disabledReason for an enabled action (title stays the label)', () => {
    const container = render(
      <ItemAction label="Fork session" onClick={() => {}} disabledReason="busy">
        <span>icon</span>
      </ItemAction>,
    )
    const button = findButton(container)
    expect(button.title).toBe('Fork session')
    expect(button.parentElement?.title).toBe('')
    expect(button.parentElement?.getAttribute('aria-label')).toBeNull()
  })
})
