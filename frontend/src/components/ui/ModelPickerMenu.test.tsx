// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import { ModelPickerMenu } from './ModelPickerMenu'
import type { ModelRef } from '@/lib/modelId'

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

const models: ModelRef[] = [
  { name: 'claude-sonnet', provider: 'anthropic' },
  { name: 'glm-5.3', provider: 'lmstudio' },
]

let container: HTMLDivElement
let root: Root
let onSelect: Mock<(id: string | null) => void>

function renderMenu(props: Partial<Parameters<typeof ModelPickerMenu>[0]> = {}): void {
  act(() => {
    root.render(
      <ModelPickerMenu
        ariaLabel="Pick a model"
        models={models}
        defaultModel="anthropic/claude-sonnet"
        loaded
        value={null}
        onSelect={onSelect}
        {...props}
      />,
    )
  })
}

function trigger(): HTMLButtonElement {
  const btn = container.querySelector('button[aria-label="Pick a model"]') as HTMLButtonElement | null
  expect(btn).not.toBeNull()
  return btn!
}

/** Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`. */
async function openMenu(): Promise<void> {
  await act(async () => {
    trigger().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
}

function menu(): HTMLDivElement {
  const el = document.querySelector('[role="menu"]')
  expect(el).not.toBeNull()
  return el as HTMLDivElement
}

/** Radix menu items are `div[role="menuitem"]`, not buttons. */
function items(): HTMLElement[] {
  return Array.from(menu().querySelectorAll<HTMLElement>('[role="menuitem"]'))
}

function clickItem(el: HTMLElement): void {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

beforeEach(() => {
  onSelect = vi.fn<(id: string | null) => void>()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

describe('ModelPickerMenu (Radix dropdown-menu)', () => {
  it('shows the resolved global default in the trigger when no override is selected', () => {
    renderMenu()
    expect(trigger().textContent).toContain('Default: claude-sonnet')
  })

  it('does not advertise a stale global default that is no longer selectable', () => {
    renderMenu({ models: [{ name: 'other', provider: 'p' }], defaultModel: 'p/gone' })
    expect(trigger().textContent).toContain('Select model…')
    expect(trigger().textContent).not.toContain('gone')
  })

  it('exposes provider-grouped entries plus the Default option as menu items', async () => {
    renderMenu()
    await openMenu()

    // Default entry + one item per model.
    const entries = items()
    expect(entries).toHaveLength(3)

    // Provider group headers are rendered and grouped with aria-labels.
    const groups = Array.from(menu().querySelectorAll('[role="group"]'))
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Anthropic', 'lmstudio'])
    expect(menu().textContent).toContain('Anthropic')
    expect(menu().textContent).toContain('claude-sonnet')
    expect(menu().textContent).toContain('glm-5.3')
  })

  it('marks the Default option as selected while value is null', async () => {
    renderMenu()
    await openMenu()

    const selected = menu().querySelectorAll('[data-selected="true"]')
    expect(selected).toHaveLength(1)
    expect(selected[0]!.textContent).toContain('Default')
  })

  it('marks exactly one entry as selected when a composite value is set', async () => {
    renderMenu({ value: 'lmstudio/glm-5.3', hideDefaultOption: true })
    await openMenu()

    const selected = menu().querySelectorAll('[data-selected="true"]')
    expect(selected).toHaveLength(1)
    expect(selected[0]!.textContent).toContain('glm-5.3')
  })

  it('calls onSelect with the composite id and closes the menu', async () => {
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    const glmItem = items().find((i) => i.textContent?.includes('glm-5.3'))
    expect(glmItem).toBeDefined()
    clickItem(glmItem!)

    expect(onSelect).toHaveBeenCalledWith('lmstudio/glm-5.3')
    expect(document.querySelector('[role="menu"]')).toBeNull()
  })

  it('calls onSelect(null) for the Default option', async () => {
    renderMenu()
    await openMenu()

    clickItem(items()[0]!)

    expect(onSelect).toHaveBeenCalledWith(null)
    expect(document.querySelector('[role="menu"]')).toBeNull()
  })

  it('hides the Default option when hideDefaultOption is set', async () => {
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    expect(items()).toHaveLength(2)
    expect(menu().textContent).not.toContain('Default')
  })

  it('renders the optional menu heading', async () => {
    renderMenu({ menuHeading: 'Default model' })
    await openMenu()

    expect(menu().textContent).toContain('Default model')
  })

  it('renders a disabled trigger with a loading label until models load', () => {
    renderMenu({ loaded: false })
    expect(trigger().disabled).toBe(true)
    expect(trigger().textContent).toContain('Loading models')
  })

  it('does not open while disabled', async () => {
    renderMenu({ disabled: true })
    await openMenu()
    expect(document.querySelector('[role="menu"]')).toBeNull()
  })

  it('portals the menu to document.body, escaping a transformed/overflow-hidden ancestor (issue #71)', async () => {
    // Reproduces the Settings dialog shape: DialogContent carries a CSS
    // `transform` (centering) and `overflow-hidden`. A hand-rolled
    // `position: fixed` menu inside it would be displaced by the dialog's own
    // offset (transformed ancestor = its containing block) and clipped — the
    // reported bug. Radix portals the content to <body> instead.
    act(() => {
      root.render(
        <div data-testid="dialog" style={{ transform: 'translate(-50%, -50%)', overflow: 'hidden' }}>
          <ModelPickerMenu
            ariaLabel="Pick a model"
            models={models}
            defaultModel="anthropic/claude-sonnet"
            loaded
            value={null}
            onSelect={onSelect}
          />
        </div>,
      )
    })
    await openMenu()

    const dialog = container.querySelector('[data-testid="dialog"]')!
    const m = menu()
    expect(dialog.contains(m)).toBe(false)
    expect(document.body.contains(m)).toBe(true)
  })
})
