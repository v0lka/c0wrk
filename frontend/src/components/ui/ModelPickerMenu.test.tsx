// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import { ModelPickerMenu } from './ModelPickerMenu'
import { clipboardSetText } from '@/api/runtime'
import { COPY_FLASH_MS } from '@/hooks/useCopyFlash'
import type { ModelRef } from '@/lib/modelId'

// The row copy action must go through the native Wails runtime, NOT
// navigator.clipboard (mirrors CopyIconButton.test). The boundary module is
// mocked so no real window.runtime binding is touched.
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

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

// The embedded local model is registered by the backend as an
// `openai_compatible` provider whose KEY is an internal identifier
// (`embedded`) and whose single model is `Bonsai 2 27B`
// (specs/domains/embedded-llm.md). Both pickers — the Settings default-model
// picker and the chat toolbar's ModelCombobox — render it through this
// component, so the human-readable reading is asserted here once: provider
// "Embedded", model "Bonsai 2 27B", composite id unchanged.
describe('ModelPickerMenu — embedded local model entry', () => {
  const embeddedModels: ModelRef[] = [
    { name: 'claude-sonnet', provider: 'anthropic' },
    { name: 'Bonsai 2 27B', provider: 'embedded' },
  ]

  it('groups the model under the human-readable provider "Embedded", not the config key', async () => {
    renderMenu({ models: embeddedModels, hideDefaultOption: true })
    await openMenu()

    const groups = Array.from(menu().querySelectorAll('[role="group"]'))
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Embedded', 'Anthropic'])

    const embeddedGroup = groups[0]!
    expect(embeddedGroup.textContent).toContain('Embedded')
    // Case-sensitive: the raw `embedded` config key is never what the user reads.
    expect(embeddedGroup.textContent).not.toContain('embedded')
    expect(embeddedGroup.textContent).toContain('Bonsai 2 27B')
  })

  it('selects the embedded model by its unchanged composite id', async () => {
    renderMenu({ models: embeddedModels, hideDefaultOption: true })
    await openMenu()

    const item = items().find((i) => i.textContent?.includes('Bonsai 2 27B'))
    expect(item).toBeDefined()
    clickItem(item!)

    expect(onSelect).toHaveBeenCalledWith('embedded/Bonsai 2 27B')
  })

  it('shows the whole bare name in the trigger, with the provider in the tooltip', () => {
    renderMenu({
      models: embeddedModels,
      defaultModel: 'embedded/Bonsai 2 27B',
      value: 'embedded/Bonsai 2 27B',
      hideDefaultOption: true,
    })

    // Exact equality: nothing hand-truncates the label (a CSS ellipsis under a
    // narrow trigger would still leave the full string in the DOM). The title
    // is the sanctioned selector-trigger enrich case: it adds the provider to
    // the bare model label instead of repeating it.
    expect(trigger().textContent).toBe('Bonsai 2 27B')
    expect(trigger().getAttribute('title')).toBe('Embedded: Bonsai 2 27B')
  })

  it('resolves the global default to the embedded entry', () => {
    renderMenu({ models: embeddedModels, defaultModel: 'embedded/Bonsai 2 27B', value: null })

    expect(trigger().textContent).toContain('Default: Bonsai 2 27B')
  })

  it('lets the Settings variant lift the 200px trigger cap so the label is never clipped', () => {
    // LLMSettings passes `max-w-none`; tailwind-merge must drop the built-in
    // `max-w-[200px]` rather than keep both (two max-widths = the narrower one
    // wins and the label ellipsizes).
    renderMenu({
      models: embeddedModels,
      defaultModel: 'embedded/Bonsai 2 27B',
      value: 'embedded/Bonsai 2 27B',
      hideDefaultOption: true,
      className: 'w-full h-9 text-sm px-3 max-w-none',
    })

    expect(trigger().className).toContain('max-w-none')
    expect(trigger().className).not.toContain('max-w-[200px]')
    expect(trigger().textContent).toBe('Bonsai 2 27B')
  })
})

// Both real feed orders put the embedded provider in the MIDDLE of the list:
// the settings picker iterates a provider map whose JSON keys the Go marshaller
// alphabetizes, and the chat picker iterates `all_models`, which the backend
// emits as (anthropic, chatgpt, sorted openai_compatible, sorted
// anthropic_compatible). The group order is normalized here — in the shared
// component — so the local model always leads on every surface.
describe('ModelPickerMenu — the Embedded group always leads', () => {
  function groupLabels(): string[] {
    return Array.from(menu().querySelectorAll('[role="group"]')).map(
      (g) => g.getAttribute('aria-label') ?? '',
    )
  }

  it('hoists Embedded to the top when its model arrives last', async () => {
    renderMenu({
      models: [
        { name: 'claude-sonnet', provider: 'anthropic' },
        { name: 'glm-5.3', provider: 'lmstudio' },
        { name: 'Bonsai 2 27B', provider: 'embedded' },
      ],
      hideDefaultOption: true,
    })
    await openMenu()

    expect(groupLabels()).toEqual(['Embedded', 'Anthropic', 'lmstudio'])
    // The hoist moves the group, not its entry: the model is still inside it.
    const groups = Array.from(menu().querySelectorAll('[role="group"]'))
    expect(groups[0]!.textContent).toContain('Bonsai 2 27B')
  })

  it('hoists Embedded even when it arrives in the middle, leaving the rest in order', async () => {
    renderMenu({
      models: [
        { name: 'claude-sonnet', provider: 'anthropic' },
        { name: 'Bonsai 2 27B', provider: 'embedded' },
        { name: 'gpt-4o', provider: 'chatgpt' },
        { name: 'glm-5.3', provider: 'lmstudio' },
      ],
      hideDefaultOption: true,
    })
    await openMenu()

    expect(groupLabels()).toEqual(['Embedded', 'Anthropic', 'ChatGPT', 'lmstudio'])
  })

  it('keeps its models listed in input order within the hoisted group', async () => {
    renderMenu({
      models: [
        { name: 'claude-sonnet', provider: 'anthropic' },
        { name: 'Bonsai 2 27B', provider: 'embedded' },
        { name: 'Bonsai 2 9B', provider: 'embedded' },
      ],
      hideDefaultOption: true,
    })
    await openMenu()

    const embeddedGroup = Array.from(menu().querySelectorAll('[role="group"]'))[0]!
    const names = Array.from(embeddedGroup.querySelectorAll('[role="menuitem"]')).map(
      (i) => i.textContent ?? '',
    )
    expect(names[0]).toContain('Bonsai 2 27B')
    expect(names[1]).toContain('Bonsai 2 9B')
  })

  it('renders no Embedded group while the local model is not installed', async () => {
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    expect(groupLabels()).toEqual(['Anthropic', 'lmstudio'])
  })
})

// The row affordance: every model row exposes its full composite name via the
// native `title` (restoring what the row's truncate clipping hides) and a
// hover-revealed copy action writing that same string through the native
// Wails clipboard. Clicking the copy action must neither select the row nor
// close the menu (the ItemActions stopPropagation shields).
describe('ModelPickerMenu — row full-name affordance (title + copy)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(clipboardSetText).mockResolvedValue(true)
  })

  function copyButtonOf(item: HTMLElement): HTMLButtonElement {
    const btn = item.querySelector<HTMLButtonElement>('button[title="Copy model name"]')
    expect(btn).not.toBeNull()
    return btn!
  }

  it('titles every model row with its full composite id and leaves the Default row bare', async () => {
    renderMenu()
    await openMenu()

    const rows = items()
    expect(rows).toHaveLength(3) // Default + 2 models

    // Match by the title itself: the Default row's text also contains the
    // resolved default's bare name, so text matching would find it first.
    expect(rows.map((r) => r.getAttribute('title'))).toEqual([
      null,
      'anthropic/claude-sonnet',
      'lmstudio/glm-5.3',
    ])

    // The Default entry has no composite id of its own: no title, no copy.
    const defaultRow = rows[0]!
    expect(defaultRow.textContent).toContain('Default')
    expect(defaultRow.getAttribute('title')).toBeNull()
    expect(defaultRow.querySelector('button')).toBeNull()
  })

  it('keeps the raw composite id even for the embedded entry the display humanizes', async () => {
    renderMenu({
      models: [
        { name: 'claude-sonnet', provider: 'anthropic' },
        { name: 'Bonsai 2 27B', provider: 'embedded' },
      ],
      hideDefaultOption: true,
    })
    await openMenu()

    const bonsai = items().find((i) => i.textContent?.includes('Bonsai 2 27B'))!
    expect(bonsai.getAttribute('title')).toBe('embedded/Bonsai 2 27B')
    expect(bonsai.getAttribute('title')).not.toContain('Embedded:')
  })

  it('reveals exactly one copy action per model row, hidden until row hover/focus', async () => {
    renderMenu()
    await openMenu()

    const overlays = Array.from(menu().querySelectorAll('span.opacity-0'))
    // One overlay per model row, none on the Default row.
    expect(overlays).toHaveLength(2)
    for (const overlay of overlays) {
      expect(overlay.className).toContain('group-hover/item:opacity-100')
      expect(overlay.className).toContain('group-focus-within/item:opacity-100')
    }
  })

  it('copies the composite id through the native clipboard without selecting the row or closing the menu', async () => {
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    const glm = items().find((i) => i.textContent?.includes('glm-5.3'))!
    const btn = copyButtonOf(glm)
    // Fake timers + an act-wrapped flush: the copy verdict arrives on a
    // promise continuation (clipboardSetText → useCopyFlash's flash), so the
    // state update must land INSIDE act or React warns about it.
    vi.useFakeTimers()
    try {
      await act(async () => {
        btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
        await vi.advanceTimersByTimeAsync(0)
      })

      expect(clipboardSetText).toHaveBeenCalledTimes(1)
      expect(clipboardSetText).toHaveBeenCalledWith('lmstudio/glm-5.3')
      expect(onSelect).not.toHaveBeenCalled()
      expect(document.querySelector('[role="menu"]')).not.toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('flashes Copied + the success icon for COPY_FLASH_MS, then reverts', async () => {
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    const btn = copyButtonOf(items().find((i) => i.textContent?.includes('claude-sonnet'))!)
    vi.useFakeTimers()
    try {
      await act(async () => {
        btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
        await vi.advanceTimersByTimeAsync(0)
      })
      expect(clipboardSetText).toHaveBeenCalledWith('anthropic/claude-sonnet')
      expect(btn.getAttribute('title')).toBe('Copied')
      expect(btn.querySelector('svg.lucide-check')).not.toBeNull()
      expect(btn.querySelector('svg.lucide-copy')).toBeNull()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(COPY_FLASH_MS)
      })
      expect(btn.getAttribute('title')).toBe('Copy model name')
      expect(btn.querySelector('svg.lucide-copy')).not.toBeNull()
      expect(btn.querySelector('svg.lucide-check')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('flashes Failed + the destructive icon when the clipboard write reports false', async () => {
    vi.mocked(clipboardSetText).mockResolvedValue(false)
    renderMenu({ hideDefaultOption: true })
    await openMenu()

    const btn = copyButtonOf(items().find((i) => i.textContent?.includes('claude-sonnet'))!)
    vi.useFakeTimers()
    try {
      await act(async () => {
        btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
        await vi.advanceTimersByTimeAsync(0)
      })
      expect(btn.getAttribute('title')).toBe('Failed')
      expect(btn.querySelector('svg.lucide-x')).not.toBeNull()
      expect(btn.querySelector('svg.lucide-check')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
