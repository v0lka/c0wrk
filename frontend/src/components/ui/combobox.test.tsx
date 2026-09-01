// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Combobox, type ComboboxOption } from './combobox'

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

const OPTIONS: ComboboxOption[] = [
  { value: 'allow', label: 'Allow' },
  { value: 'user_confirm', label: 'User Confirm' },
  { value: 'deny', label: 'Deny' },
]

let container: HTMLDivElement
let root: Root
let onChange: ReturnType<typeof vi.fn<(value: string) => void>>

beforeEach(() => {
  onChange = vi.fn()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function render(props: Partial<Parameters<typeof Combobox>[0]> = {}) {
  act(() => {
    root.render(
      <Combobox
        ariaLabel="Policy"
        value="user_confirm"
        options={OPTIONS}
        onChange={onChange}
        {...props}
      />,
    )
  })
}

function trigger(): HTMLButtonElement {
  const el = container.querySelector('button')
  expect(el).not.toBeNull()
  return el!
}

/**
 * Open the dropdown. Radix's DropdownMenuTrigger toggles on `pointerdown`
 * (left button, no ctrl), not on `click` — mirroring native menu behavior.
 * The async act also flushes the macrotask queue: Radix attaches its
 * document-level outside-pointerdown listener via setTimeout(0) after the
 * content mounts, so outside-interaction tests need it installed first.
 */
async function openDropdown(): Promise<HTMLButtonElement> {
  const btn = trigger()
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  return btn
}

function menu(): HTMLDivElement {
  const el = document.body.querySelector('[role="menu"]')
  expect(el).not.toBeNull()
  return el as HTMLDivElement
}

function menuItem(label: string): HTMLElement {
  const option = Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]')).find((o) =>
    o.textContent?.includes(label),
  )
  if (!option) throw new Error(`Menu item "${label}" not found`)
  return option
}

describe('Combobox trigger', () => {
  it('shows the label of the currently selected option', () => {
    render()
    expect(trigger().textContent).toContain('User Confirm')
  })

  it('falls back to the placeholder when the value matches no option', () => {
    render({ value: 'missing', placeholder: 'Pick…' })
    expect(trigger().textContent).toContain('Pick…')
  })

  it('does not open the menu when disabled', () => {
    render({ disabled: true })
    act(() => {
      trigger().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })
})

describe('Combobox portaled menu', () => {
  it('renders into document.body (never clipped by dialog scroll containers)', async () => {
    render()
    await openDropdown()
    const el = menu()
    // The menu lives in a separate DOM subtree from the trigger so settings
    // dialog scroll containers cannot clip it, and sits in the dialog z-50
    // layer (its own DismissableLayer on top of any open dialog).
    expect(container.contains(el)).toBe(false)
    expect(el.className).toContain('z-50')
  })

  it('uses theme tokens (bg-popover) instead of the native OS palette', async () => {
    render()
    await openDropdown()
    expect(menu().className).toContain('bg-popover')
  })

  it('closes on outside pointerdown', async () => {
    render()
    await openDropdown()
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('opens and closes from the keyboard', () => {
    render()
    const btn = trigger()
    expect(btn.getAttribute('aria-haspopup')).toBe('menu')
    act(() => {
      btn.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    expect(btn.getAttribute('aria-expanded')).toBe('true')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    act(() => {
      menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(btn.getAttribute('aria-expanded')).toBe('false')
  })
})

describe('Combobox selection', () => {
  it('marks the selected option with the primary highlight and a check', async () => {
    render()
    await openDropdown()
    const selected = menu().querySelector<HTMLElement>('[data-selected="true"]')
    expect(selected).not.toBeNull()
    expect(selected!.className).toContain('bg-primary/10')
    expect(selected!.textContent).toContain('User Confirm')
  })

  it('reports the picked value via onChange and closes the menu', async () => {
    render()
    await openDropdown()
    act(() => {
      menuItem('Deny').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('deny')
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('re-picking the already selected value is a no-op (no onChange)', async () => {
    render()
    await openDropdown()
    act(() => {
      menuItem('User Confirm').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).not.toHaveBeenCalled()
    // The menu still closes — the user made a (redundant) choice.
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('scrolls the selected option into view when opened', async () => {
    // Radix itself also calls scrollIntoView for keyboard-highlighted items,
    // so capture `this` per call and assert the *selected* item was scrolled
    // by our ref callback.
    const calls: Array<{ el: HTMLElement; args: unknown[] }> = []
    HTMLElement.prototype.scrollIntoView = function scrollIntoViewSpy(
      this: HTMLElement,
      ...args: unknown[]
    ) {
      calls.push({ el: this, args })
    }
    try {
      render()
      await openDropdown()
      expect(calls.length).toBeGreaterThan(0)
      const selectedCalls = calls.filter((c) => c.el.getAttribute('data-selected') === 'true')
      // Radix's Presence can remount the portaled content a few times in
      // jsdom (animation settle cycles), so the ref callback may fire more
      // than once — each call is idempotent. What matters: the selected
      // option is scrolled with block:'nearest'.
      expect(selectedCalls.length).toBeGreaterThanOrEqual(1)
      for (const c of selectedCalls) {
        expect(c.args[0]).toEqual({ block: 'nearest' })
      }
    } finally {
      delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView
    }
  })
})

describe('Combobox inside a modal Radix dialog', () => {
  // Regression test for the hand-rolled portal version: a modal Radix dialog
  // sets `body { pointer-events: none }`, and a plain portal to document.body
  // inherits `none` — the menu rendered but was inert in real browsers, and
  // clicks on options fell through and dismissed the dialog. Radix's
  // DismissableLayer must re-enable pointer events on the menu content.
  function renderInDialog() {
    act(() => {
      root.render(
        <Dialog open onOpenChange={() => {}}>
          <DialogContent>
            <DialogTitle>Settings</DialogTitle>
            {/* Real usages pair the title with a description (see e.g.
                ExitConfirmDialog); without one Radix warns about the
                missing Description on every render. */}
            <DialogDescription>Test harness dialog</DialogDescription>
            <Combobox
              ariaLabel="Policy"
              value="user_confirm"
              options={OPTIONS}
              onChange={onChange}
            />
          </DialogContent>
        </Dialog>,
      )
    })
    const dialogContent = document.body.querySelector('[data-slot="dialog-content"]')
    expect(dialogContent).not.toBeNull()
    const btn = dialogContent!.querySelector('button')
    expect(btn).not.toBeNull()
    act(() => {
      btn!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    return dialogContent!
  }

  it('gets pointer-events: auto while the modal dialog locks the body', () => {
    renderInDialog()
    // The modal dialog disables body pointer events…
    expect(document.body.style.pointerEvents).toBe('none')
    // …and the menu layer re-enables them for itself (DismissableLayer).
    const el = menu()
    expect(el.style.pointerEvents).toBe('auto')
    // Portaled outside the dialog content subtree, like Radix's own portals.
    const dialogContent = document.body.querySelector('[data-slot="dialog-content"]')!
    expect(dialogContent.contains(el)).toBe(false)
  })

  it('selects an option without dismissing the dialog', () => {
    renderInDialog()
    act(() => {
      menuItem('Deny').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('deny')
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    // The click on the option must not count as "outside" — the dialog stays.
    expect(document.body.querySelector('[data-slot="dialog-content"]')).not.toBeNull()
    expect(document.body.style.pointerEvents).toBe('none')
  })

  it('Escape closes only the menu, not the dialog', () => {
    renderInDialog()
    act(() => {
      menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(document.body.querySelector('[data-slot="dialog-content"]')).not.toBeNull()
  })
})
