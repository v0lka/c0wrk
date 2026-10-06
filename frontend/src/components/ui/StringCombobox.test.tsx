// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { StringCombobox } from './StringCombobox'

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

const OPTIONS = ['alpha', 'beta', 'gamma'] as const

let container: HTMLDivElement
let root: Root
let onChange: ReturnType<typeof vi.fn<(v: string) => void>>

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

type Props = Partial<Parameters<typeof StringCombobox>[0]>

function render(props: Props = {}) {
  act(() => {
    root.render(
      <StringCombobox
        ariaLabel="Profile name"
        value="beta"
        options={OPTIONS}
        onChange={onChange}
        {...props}
      />,
    )
  })
}

function input(): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>('input[aria-label="Profile name"]')
  expect(el).not.toBeNull()
  return el!
}

function chevron(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Profile name options"]')
  expect(el).not.toBeNull()
  return el!
}

/** Set the input value the way a real browser does (React 19 controlled input):
 *  native prototype setter + `input` event, so the synthetic onChange fires. */
function type(text: string): void {
  const el = input()
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) throw new Error('native input setter not found')
  act(() => {
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/** React 17+ delegates onBlur via the native `focusout` event. */
async function blur(): Promise<void> {
  await act(async () => {
    input().dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })
}

function press(key: string, alt = false): void {
  act(() => {
    input().dispatchEvent(new KeyboardEvent('keydown', { key, altKey: alt, bubbles: true }))
  })
}

/** Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`. */
async function openDropdown(): Promise<void> {
  const btn = chevron()
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
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

describe('StringCombobox render', () => {
  it('shows the current value in the input', () => {
    render()
    expect(input().value).toBe('beta')
  })

  it('renders every option in the dropdown with the selected one checked', async () => {
    render()
    await openDropdown()
    for (const opt of OPTIONS) {
      expect(menu().textContent).toContain(opt)
    }
    const selected = menu().querySelector<HTMLElement>('[data-selected="true"]')
    expect(selected).not.toBeNull()
    expect(selected!.textContent).toContain('beta')
    // The check mark is the CheckIcon svg inside the selected item.
    expect(selected!.querySelector('svg')).not.toBeNull()
  })

  it('leaves every option unchecked when the value is arbitrary (not in options)', async () => {
    render({ value: 'my-custom-name' })
    await openDropdown()
    expect(menu().querySelector<HTMLElement>('[data-selected="true"]')).toBeNull()
  })

  it('scrolls the selected option into view when opened', async () => {
    // Radix itself also calls scrollIntoView for keyboard-highlighted items,
    // so capture `this` per call and assert the *selected* item was scrolled
    // by our ref callback (same assertion shape as Combobox).
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
      // The open-scoped latch scrolls once per open; `>=1` tolerates Radix's
      // Presence remount cycles in jsdom. What matters: the selected option
      // is scrolled with block:'nearest'.
      expect(selectedCalls.length).toBeGreaterThanOrEqual(1)
      for (const c of selectedCalls) {
        expect(c.args[0]).toEqual({ block: 'nearest' })
      }
    } finally {
      delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView
    }
  })

  it('does not re-scroll the selected option on unrelated re-renders while open', async () => {
    // Regression for the self-scrolling dropdown: Radix's useComposedRefs
    // re-creates the composed content ref on EVERY re-render of the open
    // menu, so React re-invokes the content ref callback after any
    // unrelated update; scroll-under-cursor hover events re-render the open
    // list, and a scroll on every re-attach made it snap back to the
    // selected option in a self-sustaining loop. The open-scoped latch in
    // `useSelectedOptionScroll` must keep the selected-scroll to the opening
    // one, no matter how often the menu re-renders while it stays open.
    let selectedScrolls = 0
    HTMLElement.prototype.scrollIntoView = function scrollIntoViewSpy(this: HTMLElement) {
      if (this.getAttribute('data-selected') === 'true') selectedScrolls += 1
    }
    try {
      render()
      await openDropdown()
      const afterOpen = selectedScrolls
      expect(afterOpen).toBeGreaterThanOrEqual(1)
      // Unrelated updates while the menu stays open — an external re-render
      // and typing in the text field — must not scroll again.
      render()
      type('unrelated-edit')
      expect(selectedScrolls).toBe(afterOpen)
    } finally {
      delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView
    }
  })
})

describe('StringCombobox manual input', () => {
  it('does not commit while typing', () => {
    render()
    type('my-custom-name')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('commits a custom (not-in-options) value on Enter', () => {
    render()
    type('my-custom-name')
    press('Enter')
    expect(onChange).toHaveBeenCalledWith('my-custom-name')
    expect(input().value).toBe('my-custom-name')
  })

  it('commits a custom value on blur', async () => {
    render()
    type('my-custom-name')
    await blur()
    expect(onChange).toHaveBeenCalledWith('my-custom-name')
  })

  it('reverts empty input on Enter without onChange', () => {
    render()
    type('')
    press('Enter')
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('beta')
  })

  it('reverts empty input on blur without onChange', async () => {
    render()
    type('')
    await blur()
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('beta')
  })

  it('applies normalize on commit', () => {
    render({ normalize: (raw) => raw.trim() })
    type('  padded name  ')
    press('Enter')
    expect(onChange).toHaveBeenCalledWith('padded name')
    expect(input().value).toBe('padded name')
  })

  it('reverts when normalize produces an empty string', async () => {
    render({ normalize: (raw) => raw.trim() })
    type('   ')
    await blur()
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('beta')
  })

  it('committing the unchanged value fires no onChange', () => {
    render()
    type('beta')
    press('Enter')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('Escape discards the edit without onChange', () => {
    render()
    type('my-custom-name')
    press('Escape')
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('beta')
  })
})

describe('StringCombobox dropdown', () => {
  it('opens via the chevron and via ArrowDown / Alt+ArrowDown from the input', async () => {
    render()
    await openDropdown()
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    act(() => {
      menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    press('ArrowDown')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
    act(() => {
      menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    press('ArrowDown', true)
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  })

  it('clicking an option commits it via onChange and closes the menu', async () => {
    render()
    await openDropdown()
    act(() => {
      menuItem('gamma').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('gamma')
    expect(input().value).toBe('gamma')
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('re-picking the current option is a no-op (no onChange)', async () => {
    render()
    await openDropdown()
    act(() => {
      menuItem('beta').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).not.toHaveBeenCalled()
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })

  it('closing the menu on Escape does not commit the pending edit', async () => {
    render()
    type('draft name')
    await openDropdown()
    // The input's Escape handler must not treat the menu's dismissal as a
    // commit trigger: text stays pending until Enter/blur.
    act(() => {
      menu().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('draft name')
  })
})

describe('StringCombobox inside a modal Radix dialog', () => {
  function renderInDialog() {
    act(() => {
      root.render(
        <Dialog open onOpenChange={() => {}}>
          <DialogContent>
            <DialogTitle>Settings</DialogTitle>
            <DialogDescription>Test harness dialog</DialogDescription>
            <StringCombobox
              ariaLabel="Profile name"
              value="beta"
              options={OPTIONS}
              onChange={onChange}
            />
          </DialogContent>
        </Dialog>,
      )
    })
    const dialogContent = document.body.querySelector('[data-slot="dialog-content"]')
    expect(dialogContent).not.toBeNull()
  }

  it('commits manual input from inside the dialog', () => {
    renderInDialog()
    const el = document.body.querySelector<HTMLInputElement>('input[aria-label="Profile name"]')
    expect(el).not.toBeNull()
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
    act(() => {
      setter!.call(el!, 'my-custom-name')
      el!.dispatchEvent(new Event('input', { bubbles: true }))
      el!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('my-custom-name')
  })

  it('selects an option without dismissing the dialog', async () => {
    renderInDialog()
    const btn = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="Profile name options"]',
    )
    expect(btn).not.toBeNull()
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    // The modal dialog locks body pointer events; the menu layer must
    // re-enable them for itself (Radix DismissableLayer).
    expect(menu().style.pointerEvents).toBe('auto')
    act(() => {
      menuItem('alpha').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('alpha')
    expect(document.body.querySelector('[data-slot="dialog-content"]')).not.toBeNull()
  })
})

describe('StringCombobox option and input styling hooks', () => {
  it('applies itemStyle to each option label, receiving that option', async () => {
    const seen = new Set<string>()
    render({
      itemStyle: (opt) => {
        seen.add(opt)
        return { fontFamily: `"${opt}", monospace` }
      },
    })
    await openDropdown()
    // Every rendered option reaches the callback; re-renders of the open
    // menu may call it more than once per option — fine, the callback is a
    // pure (opt → style) mapping.
    expect([...seen].sort()).toEqual(['alpha', 'beta', 'gamma'])
    for (const opt of OPTIONS) {
      const label = menuItem(opt).querySelector<HTMLElement>('span')
      expect(label).not.toBeNull()
      expect(label!.style.fontFamily).toBe(`"${opt}", monospace`)
    }
    // Selection chrome is untouched by the label style.
    const selected = menu().querySelector<HTMLElement>('[data-selected="true"]')
    expect(selected!.querySelector('svg')).not.toBeNull()
  })

  it('sets no inline style on option labels without itemStyle', async () => {
    render()
    await openDropdown()
    const label = menuItem('beta').querySelector<HTMLElement>('span')
    expect(label!.getAttribute('style')).toBeNull()
  })

  it('applies inputStyle to the text field and keeps it across commits', () => {
    render({ inputStyle: { fontFamily: '"Current Font", monospace' } })
    expect(input().style.fontFamily).toBe('"Current Font", monospace')
    // The style is the parent's per-value choice: commits change the value,
    // not the inline style (the parent re-renders with a new one).
    type('gamma')
    press('Enter')
    expect(onChange).toHaveBeenCalledWith('gamma')
    expect(input().style.fontFamily).toBe('"Current Font", monospace')
  })
})

describe('StringCombobox select-only mode (editable=false)', () => {
  it('renders a read-only field with the select-only affordance', () => {
    render({ editable: false })
    expect(input().readOnly).toBe(true)
  })

  it('typing never fires onChange and never changes the value', () => {
    // A real browser fires no `input` on a readOnly field; the synthetic
    // event here proves the component ignores one anyway (autofill guard).
    render({ editable: false })
    type('garbage')
    press('Enter')
    expect(onChange).not.toHaveBeenCalled()
    expect(input().value).toBe('beta')
  })

  it('blur does not commit anything', async () => {
    render({ editable: false })
    await blur()
    expect(onChange).not.toHaveBeenCalled()
  })

  it('the dropdown still opens and picking an option commits', async () => {
    render({ editable: false })
    await openDropdown()
    act(() => {
      menuItem('gamma').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onChange).toHaveBeenCalledWith('gamma')
    expect(input().value).toBe('gamma')
  })

  it('ArrowDown from the input still opens the menu (keyboard access)', () => {
    render({ editable: false })
    press('ArrowDown')
    expect(document.body.querySelector('[role="menu"]')).not.toBeNull()
  })
})
