// @vitest-environment jsdom
// ItemAction tests.
//
// The button is the shared action primitive behind every sidebar/GitPanel/
// research/bookmarks row overlay (see ItemAction.tsx header). The row overlay
// reveals it on hover, and the row itself already shows a pointer cursor — so
// the button MUST carry its own hover/active feedback to look alive. These
// tests pin that contract (background tint + active press state) plus the
// click and disabled behavior.
//
// Rendering follows the project convention (ModelCombobox.test.tsx): Radix
// Tooltips need a provider ancestor (the app mounts one at the root), and
// createRoot + act avoids the legacy react-dom/test-utils warning.

import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ItemAction } from './ItemAction'

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  act(() => {
    root.render(<TooltipProvider>{ui}</TooltipProvider>)
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
})
