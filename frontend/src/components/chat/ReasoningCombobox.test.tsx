// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ReasoningCombobox } from './ReasoningCombobox'
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

// Canned config data shared with the mocked hook, mutated per test. The mock
// returns this object by reference, so flipping its fields and re-rendering
// simulates the async config fetch landing (the real app start sequence:
// loaded=false with an empty model list, then the fetched data).
const configData = {
  allModels: [] as Array<{
    name: string
    provider: string
    family: string
    vision: boolean
    reasoning?: { options: string[]; default: string }
  }>,
  defaultModel: '',
  loaded: false,
}

// Mock the config hook so the combobox renders synchronously with canned
// models, without touching the Wails backend. ReasoningCombobox only imports
// useConfigData — a partial mock without a type cast, matching the project
// convention (BlackboardPanel.test, ReviewPage.test, useProjectSwitchState.test).
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => configData,
}))

const REASONING_MODELS = [
  {
    name: 'claude-sonnet',
    provider: 'anthropic',
    family: 'anthropic',
    vision: true,
    reasoning: { options: ['low', 'medium', 'high'], default: 'high' },
  },
  {
    name: 'glm-5',
    provider: 'PT',
    family: 'glm',
    vision: false,
    reasoning: { options: ['low', 'medium'], default: 'medium' },
  },
]

const NON_REASONING_MODELS = [
  { name: 'local-mini', provider: 'ollama', family: 'other', vision: false },
]

let container: HTMLDivElement
let root: Root

function renderCombobox(): void {
  act(() => {
    root.render(<ReasoningCombobox />)
  })
}

beforeEach(() => {
  useInputModeStore.setState({ selectedModel: null, selectedReasoning: null })
  configData.allModels = REASONING_MODELS
  configData.defaultModel = 'anthropic/claude-sonnet'
  configData.loaded = true
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

/** The trigger is the only button in the component subtree (menu items are
 *  Radix divs in a body portal). */
function trigger(): HTMLButtonElement {
  const el = container.querySelector('button')
  expect(el).not.toBeNull()
  return el as HTMLButtonElement
}

/** The portaled menu content ([role=menu] under <body>), or null when closed. */
function menu(): HTMLElement | null {
  return document.body.querySelector('[role="menu"]')
}

/** Radix menu items are div[role=menuitem], not buttons. */
function menuItems(): HTMLElement[] {
  const el = menu()
  return el ? Array.from(el.querySelectorAll<HTMLElement>('[role="menuitem"]')) : []
}

/**
 * Radix's DropdownMenuTrigger toggles on `pointerdown`, and its post-open
 * document-level work attaches from a setTimeout(0) — dispatch inside the
 * virtual-time window so it fires deterministically with no real time spent
 * (timing-guard compliant; see src/test/virtualTime.ts).
 */
async function openMenu(): Promise<void> {
  const t = trigger()
  await withVirtualTimers(() => {
    t.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  }, 10)
  expect(menu(), 'menu after open').not.toBeNull()
}

function click(el: HTMLElement): void {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

function findItemByLabel(label: string): HTMLElement {
  const el = menuItems().find((b) => b.textContent?.includes(label))
  expect(el, `menu item ${label}`).toBeDefined()
  return el as HTMLElement
}

describe('ReasoningCombobox persistence across restart', () => {
  it('does NOT wipe a persisted selection while the model list is still loading', () => {
    // App-start sequence: inputModeStore rehydrated a persisted selection
    // synchronously, but the async GetConfig fetch has not landed yet —
    // loaded=false, empty model list, no default model. The reset effect must
    // not treat "unknown model" as "unsupported selection": wiping here
    // destroyed the persisted level on every restart (the regression).
    configData.allModels = []
    configData.defaultModel = ''
    configData.loaded = false
    useInputModeStore.setState({ selectedModel: 'anthropic/claude-sonnet', selectedReasoning: 'high' })

    renderCombobox()

    // The combobox renders nothing while unloaded, but the stored choice
    // survives the validation effect.
    expect(container.querySelector('button')).toBeNull()
    expect(useInputModeStore.getState().selectedReasoning).toBe('high')
  })

  it('keeps the persisted selection without a model override during the loading window', () => {
    // selectedModel=null defers to defaultModel, which is also empty until the
    // config lands — equally unresolvable, equally protected.
    configData.allModels = []
    configData.defaultModel = ''
    configData.loaded = false
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: 'medium' })

    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')
  })

  it('shows the persisted selection once the config lands and the family supports it', () => {
    configData.allModels = []
    configData.defaultModel = ''
    configData.loaded = false
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: 'medium' })

    renderCombobox()
    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')

    // The fetch resolves (models_ready): the stored option is valid for the
    // now-resolvable family, so the trigger shows the explicit choice —
    // not the "Default (provider)" fallback.
    configData.allModels = REASONING_MODELS
    configData.defaultModel = 'anthropic/claude-sonnet'
    configData.loaded = true
    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')
    expect(trigger().textContent).toContain('medium')
    expect(trigger().textContent).not.toContain('Default (')
  })

  it('keeps the persisted selection while the selected model is unknown to the loaded list', () => {
    // A stale selector (model removed from config, list loaded) resolves to
    // nothing — still "unknown", not "unsupported", so the choice stays.
    useInputModeStore.setState({ selectedModel: 'gone/model-x', selectedReasoning: 'high' })

    renderCombobox()

    // Family unresolvable → the combobox hides itself, but the stored value
    // is not destroyed behind the user's back.
    expect(container.querySelector('button')).toBeNull()
    expect(useInputModeStore.getState().selectedReasoning).toBe('high')
  })
})

describe('ReasoningCombobox reset semantics (loaded families)', () => {
  it('wipes the stored selection when the loaded family does not offer it', () => {
    // glm offers low/medium only: 'high' is positively known to be
    // unsupported for the resolved family → reset to the provider default.
    useInputModeStore.setState({ selectedModel: 'PT/glm-5', selectedReasoning: 'high' })

    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBeNull()
    expect(trigger().textContent).toContain('Default (provider)')
  })

  it('wipes the stored selection when the resolved family has no reasoning support', () => {
    configData.allModels = NON_REASONING_MODELS
    configData.defaultModel = 'ollama/local-mini'
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: 'low' })

    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBeNull()
    // No reasoning metadata → the combobox renders nothing at all.
    expect(container.querySelector('button')).toBeNull()
  })
})

describe('ReasoningCombobox portal (minimal-panel-height fix)', () => {
  it('renders the option list into document.body, outside the clipped panel subtree', async () => {
    renderCombobox()

    // Closed initially: no menu anywhere.
    expect(menu()).toBeNull()

    await openMenu()

    const el = menu()
    expect(el).not.toBeNull()
    // The menu lives in a Radix portal under <body>, separate from the
    // component subtree: no ancestor of the chat input panel can clip it,
    // regardless of the panel's height or overflow (the bug scenario).
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)

    // Default + three native options for the anthropic family; "active" marks
    // the Default entry (no explicit selection yet). Each item's textContent
    // concatenates its label with the trailing status span.
    expect(menuItems().map((i) => i.textContent)).toEqual([
      'Default (provider)active',
      'low',
      'medium',
      'high',
    ])
  })

  it('marks the trigger as expanded while the menu is open and closes on Escape', async () => {
    renderCombobox()
    const t = trigger()
    expect(t.getAttribute('aria-expanded')).toBe('false')

    await openMenu()
    expect(t.getAttribute('aria-expanded')).toBe('true')

    // Radix closes on Escape; the trigger returns to the collapsed state.
    await withVirtualTimers(() => {
      menu()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }, 10)
    expect(t.getAttribute('aria-expanded')).toBe('false')
  })

  it('keeps the menu open when interacting inside the portaled menu', async () => {
    renderCombobox()
    await openMenu()
    expect(menu()).not.toBeNull()

    // A pointerdown inside the portal menu must NOT be treated as an outside click.
    act(() => {
      menu()!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(menu()).not.toBeNull()
  })

  it('dismisses the menu on an outside pointerdown', async () => {
    renderCombobox()
    await openMenu()
    expect(menu()).not.toBeNull()

    // A pointerdown on <body> (outside both the trigger and the portal menu).
    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(menu()).toBeNull()
  })
})

describe('ReasoningCombobox selection UI', () => {
  it('selects an option from the portaled dropdown and marks it active', async () => {
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: null })

    renderCombobox()
    expect(trigger().textContent).toContain('Default (provider)')

    await openMenu()
    // Default + three native options for the anthropic family; "active" marks
    // the Default entry (no explicit selection yet). Each option's textContent
    // concatenates its label with the trailing status span.
    expect(menuItems().map((i) => i.textContent)).toEqual([
      'Default (provider)active',
      'low',
      'medium',
      'high',
    ])

    click(findItemByLabel('medium'))
    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')
    // The dropdown closes after a pick (Radix closes on item select).
    expect(menu()).toBeNull()
    expect(trigger().textContent).toContain('medium')
  })

  it('returns to the provider default when the Default option is chosen', async () => {
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: 'low' })

    renderCombobox()
    await openMenu()
    click(findItemByLabel('Default'))

    expect(useInputModeStore.getState().selectedReasoning).toBeNull()
    expect(trigger().textContent).toContain('Default (provider)')
  })

  it('renders nothing while the config has not loaded', () => {
    configData.loaded = false
    renderCombobox()
    expect(container.querySelector('button')).toBeNull()
  })
})

describe('ReasoningCombobox disabled (session-pinning lock)', () => {
  it('renders a disabled trigger and ignores clicks while the session is running', async () => {
    act(() => {
      root.render(<ReasoningCombobox disabled />)
    })
    const t = trigger()
    expect(t.disabled).toBe(true)
    expect(t.getAttribute('title')).toBe('Locked while the session is running')

    // A pointerdown on the disabled trigger must not open the dropdown.
    await withVirtualTimers(() => {
      t.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    }, 10)
    expect(menu()).toBeNull()
  })
})
