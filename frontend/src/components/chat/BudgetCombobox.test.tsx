// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { BudgetCombobox } from './BudgetCombobox'
import { useInputModeStore } from '@/stores/inputModeStore'
import { withVirtualTimers } from '@/test/virtualTime'

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

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  useInputModeStore.setState({ goalBudget: '' })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<BudgetCombobox />)
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

/** The trigger is the only button in the component tree (the menu portals out). */
function trigger(): HTMLButtonElement {
  const el = container.querySelector('button')
  expect(el).not.toBeNull()
  return el!
}

function menu(): HTMLElement | null {
  return document.body.querySelector('[role="listbox"]')
}

function presets(): HTMLButtonElement[] {
  return Array.from(
    menu()!.querySelectorAll<HTMLButtonElement>('button[title^="Set the goal budget"]'),
  )
}

function applyButton(): HTMLButtonElement {
  return menu()!.querySelector<HTMLButtonElement>('button[title="Apply the custom turn count"]')!
}

function customInput(): HTMLInputElement {
  return menu()!.querySelector<HTMLInputElement>('input[aria-label="Custom turn count"]')!
}

/** Open (or re-click) the popover via the trigger. Radix installs its
 *  document-level outside-interaction listener via setTimeout(0) after the
 *  content mounts: dispatch inside the virtual-time window so the listener
 *  attaches deterministically before the open menu is interacted with
 *  (timing-guard compliant; see src/test/virtualTime.ts). The trigger's
 *  click is what toggles, in either direction. */
async function openMenu(): Promise<void> {
  const t = trigger()
  await withVirtualTimers(() => {
    t.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }, 10)
}

/** React 19 controlled input: native prototype setter + input event, so the
 *  synthetic onChange fires (a plain `.value =` write is swallowed by React's
 *  value tracker). */
function typeInto(input: HTMLInputElement, value: string): void {
  act(() => {
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )?.set
    setter!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('BudgetCombobox portal', () => {
  it('is closed by default and opens into a document.body portal outside the component subtree', async () => {
    expect(menu()).toBeNull()

    await openMenu()

    const el = menu()
    expect(el).not.toBeNull()
    // The menu lives in a Radix portal under <body>, so the chat input panel's
    // overflow-hidden ancestors can never clip it, even at minimum height.
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)
  })

  it('toggles aria-expanded on the trigger and closes again on the second click', async () => {
    expect(trigger().getAttribute('aria-expanded')).toBe('false')

    await openMenu()
    expect(trigger().getAttribute('aria-expanded')).toBe('true')

    await openMenu()
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
    expect(menu()).toBeNull()
  })

  it('dismisses on an outside pointerdown', async () => {
    await openMenu()
    expect(menu()).not.toBeNull()

    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(menu()).toBeNull()
  })

  it('closes on Escape without committing anything', async () => {
    await openMenu()
    typeInto(customInput(), '4')

    act(() => {
      customInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(menu()).toBeNull()
    expect(useInputModeStore.getState().goalBudget).toBe('')
  })
})

describe('BudgetCombobox presets', () => {
  it('marks the unlimited preset selected by default', async () => {
    await openMenu()

    const items = presets()
    expect(items.length).toBe(4)
    expect(items[0]!.textContent).toContain('∞')
    expect(items[0]!.className).toContain('bg-primary/10')
    expect(items[0]!.textContent).toContain('selected')
    for (const other of items.slice(1)) {
      expect(other.className).not.toContain('bg-primary/10')
      expect(other.textContent).not.toContain('selected')
    }
  })

  it('selecting a preset commits it, closes the menu and updates the trigger label', async () => {
    await openMenu()

    act(() => {
      presets()[2]!.dispatchEvent(new MouseEvent('click', { bubbles: true })) // 5 turns
    })

    expect(useInputModeStore.getState().goalBudget).toBe('{"max_turns":5}')
    expect(menu()).toBeNull()
    expect(trigger().textContent).toContain('5 turns')
  })

  it('marks the stored non-default preset selected on reopen', async () => {
    act(() => {
      useInputModeStore.setState({ goalBudget: '{"max_turns":3}' })
    })
    expect(trigger().textContent).toContain('3 turns')

    await openMenu()

    const items = presets()
    expect(items[1]!.className).toContain('bg-primary/10')
    expect(items[1]!.textContent).toContain('selected')
    expect(items[0]!.textContent).not.toContain('selected')
  })
})

describe('BudgetCombobox custom turn count', () => {
  it('seeds the input from a stored non-preset value', async () => {
    act(() => {
      useInputModeStore.setState({ goalBudget: '{"max_turns":7}' })
    })

    await openMenu()

    expect(customInput().value).toBe('7')
  })

  it('focuses the custom input on open', async () => {
    await openMenu()
    expect(document.activeElement).toBe(customInput())
  })

  it('Enter commits {"max_turns":N}, closes the menu and updates the trigger', async () => {
    await openMenu()
    typeInto(customInput(), '12')

    act(() => {
      customInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })

    expect(useInputModeStore.getState().goalBudget).toBe('{"max_turns":12}')
    expect(menu()).toBeNull()
    expect(trigger().textContent).toContain('12 turns')
  })

  it('the Apply button commits the typed count the same way', async () => {
    await openMenu()
    typeInto(customInput(), '3')

    act(() => {
      applyButton().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(useInputModeStore.getState().goalBudget).toBe('{"max_turns":3}')
    expect(menu()).toBeNull()
    expect(trigger().textContent).toContain('3 turns')
  })

  it('ignores invalid counts (0 / negative) and keeps the menu open', async () => {
    await openMenu()

    typeInto(customInput(), '0')
    act(() => {
      customInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    expect(useInputModeStore.getState().goalBudget).toBe('')
    expect(menu()).not.toBeNull()

    typeInto(customInput(), '-3')
    act(() => {
      applyButton().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().goalBudget).toBe('')
    expect(menu()).not.toBeNull()
  })
})

describe('BudgetCombobox disabled', () => {
  it('renders a disabled trigger that never opens the menu', async () => {
    act(() => {
      root.render(<BudgetCombobox disabled />)
    })
    expect(trigger().disabled).toBe(true)

    await openMenu()

    expect(menu()).toBeNull()
  })

  it('force-closes an open menu when disabled flips while it is open', async () => {
    // The portaled panel mounts on <body>, outside any pointer-events-none
    // lock wrapper, so the pre-Radix useDropdown pinned close-on-disabled-flip:
    // an open menu must not stay interactive past the toolbar's lock
    // (session start / pause / compaction). Radix does not do this itself —
    // the component owns open state and closes it here.
    await openMenu()
    expect(menu()).not.toBeNull()

    act(() => {
      root.render(<BudgetCombobox disabled />)
    })

    expect(menu()).toBeNull()
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
  })
})
