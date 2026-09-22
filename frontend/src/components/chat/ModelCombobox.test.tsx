// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ModelCombobox } from './ModelCombobox'
import { useInputModeStore } from '@/stores/inputModeStore'

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

// Spies created via vi.hoisted so they exist before vi.mock factories run and
// are also referenceable inside the test bodies.
const spies = vi.hoisted(() => ({
  setDefaultModel: vi.fn<(model: string) => Promise<void>>(),
  invalidateConfigCache: vi.fn(),
  configData: {
    allModels: [
      { name: 'claude-sonnet', provider: 'anthropic', family: 'anthropic', vision: true },
      { name: 'gpt-4o', provider: 'chatgpt', family: 'chatgpt', vision: true },
    ],
    defaultModel: 'claude-sonnet',
    loaded: true,
  },
}))

// Mock the config hook so the combobox renders synchronously with canned
// models, without touching the Wails backend.
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => spies.configData,
  invalidateConfigCache: spies.invalidateConfigCache,
}) as typeof import('@/hooks/useConfigData'))

// Mock the config API so picking a model persists default_model without a real
// Wails round-trip. Partial mock without a type cast — ModelCombobox only calls
// setDefaultModel — matching the project convention (BlackboardPanel.test,
// ReviewPage.test, useProjectSwitchState.test) where @/api/* modules are
// partially mocked without `as typeof import(...)`, which would otherwise
// require covering every export of the mocked module.
vi.mock('@/api/config', () => ({
  setDefaultModel: spies.setDefaultModel,
}))

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  useInputModeStore.setState({ selectedModel: null })
  spies.configData.allModels = [
    { name: 'claude-sonnet', provider: 'anthropic', family: 'anthropic', vision: true },
    { name: 'gpt-4o', provider: 'chatgpt', family: 'chatgpt', vision: true },
  ]
  spies.configData.defaultModel = 'claude-sonnet'
  spies.configData.loaded = true
  spies.setDefaultModel.mockReset()
  // By default the persist succeeds; individual tests override with
  // mockRejectedValue to exercise the failure path.
  spies.setDefaultModel.mockResolvedValue(undefined)
  spies.invalidateConfigCache.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<ModelCombobox />)
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

/** Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`. */
async function openDropdown(): Promise<HTMLButtonElement> {
  const trigger = container.querySelector('button')
  expect(trigger).not.toBeNull()
  await act(async () => {
    trigger!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  return trigger!
}

function menu(): HTMLDivElement | null {
  return document.body.querySelector('[role="menu"]')
}

/** Radix menu items are `div[role="menuitem"]`, not buttons. */
function options(): HTMLElement[] {
  const el = menu()
  return el ? Array.from(el.querySelectorAll<HTMLElement>('[role="menuitem"]')) : []
}

function clickOption(el: HTMLElement): void {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

describe('ModelCombobox portal', () => {
  it('renders the dropdown into document.body (not the component subtree) when open', async () => {
    // Closed initially: no menu anywhere.
    expect(menu()).toBeNull()

    await openDropdown()

    const el = menu()
    expect(el).not.toBeNull()
    // The menu lives in a Radix portal under <body>, separate from the trigger.
    expect(container.contains(el)).toBe(false)
    expect(document.body.contains(el)).toBe(true)
  })

  it('marks the trigger as expanded while the menu is open', async () => {
    const trigger = await openDropdown()
    expect(trigger.getAttribute('aria-expanded')).toBe('true')

    // Radix closes on Escape; the trigger returns to the collapsed state.
    act(() => {
      menu()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  })

  it('keeps the dropdown open when interacting inside the portaled menu', async () => {
    await openDropdown()
    expect(menu()).not.toBeNull()

    // A pointerdown inside the portal menu must NOT be treated as an outside click.
    act(() => {
      menu()!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(menu()).not.toBeNull()
  })

  it('dismisses the dropdown on an outside pointerdown', async () => {
    await openDropdown()
    expect(menu()).not.toBeNull()

    // A pointerdown on <body> (outside both the trigger wrapper and the portal menu).
    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(menu()).toBeNull()
  })

  it('does not display a stale global default that is no longer selectable', async () => {
    spies.configData.allModels = [
      { name: 'deepseek-v4', provider: 'PT', family: 'deepseek', vision: false },
      { name: 'zai-org/GLM-5.3', provider: 'PT', family: 'glm', vision: false },
    ]
    spies.configData.defaultModel = 'PT/zai-org/GLM-5.2-FP8'

    act(() => {
      root.render(<ModelCombobox />)
    })

    const trigger = container.querySelector('button')
    expect(trigger?.textContent).toContain('Select model…')
    expect(trigger?.textContent).not.toContain('GLM-5.2-FP8')

    await openDropdown()
    const defaultOption = options()[0]
    expect(defaultOption?.textContent).toBe('Defaultactive')
    expect(defaultOption?.textContent).not.toContain('GLM-5.2-FP8')
  })

  it('selects a model and closes when a portaled option is clicked', async () => {
    await openDropdown()
    const entries = options()
    // Default option + two models = 3 items.
    expect(entries.length).toBe(3)

    // Last option is the 'gpt-4o' model.
    clickOption(entries[entries.length - 1]!)

    // The selected value is the composite selector "provider/name".
    expect(useInputModeStore.getState().selectedModel).toBe('chatgpt/gpt-4o')
    expect(menu()).toBeNull()
  })
})

describe('ModelCombobox default-model persistence', () => {
  it('persists the picked model as default_model and invalidates the config cache', async () => {
    await openDropdown()
    const entries = options()

    spies.setDefaultModel.mockResolvedValue(undefined)

    // Last option is the 'gpt-4o' model.
    clickOption(entries[entries.length - 1]!)

    // The picked model is written to default_model (LLM section) as the
    // composite selector "provider/name".
    expect(spies.setDefaultModel).toHaveBeenCalledTimes(1)
    expect(spies.setDefaultModel).toHaveBeenCalledWith('chatgpt/gpt-4o')

    // The per-message override is also set, so the next message uses the
    // picked model immediately.
    expect(useInputModeStore.getState().selectedModel).toBe('chatgpt/gpt-4o')

    // After the persist resolves, the config cache is invalidated so every
    // consumer (settings, reasoning combobox) refreshes the new default.
    await vi.waitFor(() => {
      expect(spies.invalidateConfigCache).toHaveBeenCalledTimes(1)
    })
  })

  it('does NOT persist default_model when the "Default" option is chosen', async () => {
    await openDropdown()
    const entries = options()

    spies.setDefaultModel.mockResolvedValue(undefined)

    // First option is the "Default" entry (resets to global default).
    clickOption(entries[0]!)

    // Choosing "Default" clears the per-message override but must NOT rewrite
    // default_model — it already points at the global default.
    expect(useInputModeStore.getState().selectedModel).toBeNull()
    expect(spies.setDefaultModel).not.toHaveBeenCalled()
    expect(spies.invalidateConfigCache).not.toHaveBeenCalled()
  })

  it('does not let an older failed persist roll back a newer model choice', async () => {
    let rejectFirst!: (reason?: unknown) => void
    let resolveSecond!: () => void
    spies.setDefaultModel
      .mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectFirst = reject }))
      .mockImplementationOnce(() => new Promise<void>((resolve) => { resolveSecond = resolve }))

    await openDropdown()
    let entries = options()
    // Pick gpt-4o first; its request remains in flight.
    clickOption(entries[entries.length - 1]!)

    await openDropdown()
    entries = options()
    // Then select claude-sonnet before the first request settles.
    clickOption(entries[1]!)
    expect(useInputModeStore.getState().selectedModel).toBe('anthropic/claude-sonnet')
    expect(spies.setDefaultModel).toHaveBeenCalledTimes(1)

    await act(async () => { rejectFirst(new Error('older request failed')) })
    await vi.waitFor(() => {
      expect(spies.setDefaultModel).toHaveBeenCalledTimes(2)
    })
    await act(async () => { resolveSecond() })

    expect(useInputModeStore.getState().selectedModel).toBe('anthropic/claude-sonnet')
  })

  it('keeps Default as rollback state after an earlier save settles late', async () => {
    let resolveFirst!: () => void
    let rejectSecond!: (reason?: unknown) => void
    spies.setDefaultModel
      .mockImplementationOnce(() => new Promise<void>((resolve) => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectSecond = reject }))

    await openDropdown()
    let entries = options()
    clickOption(entries[entries.length - 1]!)

    await openDropdown()
    entries = options()
    // Cancel the optimistic pick before its persistence succeeds.
    clickOption(entries[0]!)
    await act(async () => { resolveFirst() })

    await openDropdown()
    entries = options()
    clickOption(entries[1]!)
    await vi.waitFor(() => {
      expect(spies.setDefaultModel).toHaveBeenCalledTimes(2)
    })
    await act(async () => { rejectSecond(new Error('second request failed')) })

    await vi.waitFor(() => {
      expect(useInputModeStore.getState().selectedModel).toBeNull()
    })
  })

  it('uses the effective backend default when a later queued selection fails', async () => {
    let resolveFirst!: () => void
    let rejectSecond!: (reason?: unknown) => void
    spies.setDefaultModel
      .mockImplementationOnce(() => new Promise<void>((resolve) => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectSecond = reject }))

    await openDropdown()
    let entries = options()
    clickOption(entries[entries.length - 1]!)

    await openDropdown()
    entries = options()
    clickOption(entries[1]!)

    await act(async () => { resolveFirst() })
    await vi.waitFor(() => {
      expect(spies.setDefaultModel).toHaveBeenCalledTimes(2)
    })
    await act(async () => { rejectSecond(new Error('second request failed')) })

    await vi.waitFor(() => {
      // The first backend save succeeded, so null delegates to its actual
      // default instead of retaining the rejected second override.
      expect(useInputModeStore.getState().selectedModel).toBeNull()
    })
  })

  it('rolls back queued failed selections to the last backend-confirmed value', async () => {
    let rejectFirst!: (reason?: unknown) => void
    let rejectSecond!: (reason?: unknown) => void
    spies.setDefaultModel
      .mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectFirst = reject }))
      .mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectSecond = reject }))

    await openDropdown()
    let entries = options()
    clickOption(entries[entries.length - 1]!)

    await openDropdown()
    entries = options()
    clickOption(entries[1]!)
    expect(useInputModeStore.getState().selectedModel).toBe('anthropic/claude-sonnet')

    await act(async () => { rejectFirst(new Error('first request failed')) })
    await vi.waitFor(() => {
      expect(spies.setDefaultModel).toHaveBeenCalledTimes(2)
    })
    await act(async () => { rejectSecond(new Error('second request failed')) })

    await vi.waitFor(() => {
      // Neither request persisted, so the override must return to the initial
      // confirmed value (null = use the existing backend default), not gpt-4o.
      expect(useInputModeStore.getState().selectedModel).toBeNull()
    })
  })

  it('still invalidates the cache even if persist rejects', async () => {
    await openDropdown()
    const entries = options()

    spies.setDefaultModel.mockRejectedValue(new Error('boom'))

    // Async act so the rejection's rollback microtask runs inside the act
    // scope rather than after it.
    await act(async () => {
      // Last option is the 'gpt-4o' model.
      clickOption(entries[entries.length - 1]!)
    })

    await vi.waitFor(() => {
      expect(spies.invalidateConfigCache).toHaveBeenCalledTimes(1)
    })
  })

  it('rolls back the per-message override when persist fails (no silent divergence)', async () => {
    await openDropdown()
    const entries = options()

    spies.setDefaultModel.mockRejectedValue(new Error('boom'))

    // Async act so the rejection's rollback microtask runs inside the act
    // scope rather than after it. The optimistic value is captured
    // synchronously right after the click, before the rollback flushes.
    let optimistic: string | null = 'unset'
    await act(async () => {
      // Last option is the 'gpt-4o' model.
      clickOption(entries[entries.length - 1]!)
      optimistic = useInputModeStore.getState().selectedModel
    })

    // Optimistically applied synchronously on click …
    expect(optimistic).toBe('chatgpt/gpt-4o')

    // … then rolled back once the persist rejects, so the selector never
    // advertises a default that was not actually saved.
    await vi.waitFor(() => {
      expect(useInputModeStore.getState().selectedModel).toBeNull()
    })
    expect(spies.invalidateConfigCache).toHaveBeenCalledTimes(1)
  })
})

describe('ModelCombobox disabled (session-pinning lock)', () => {
  it('renders a disabled trigger and ignores clicks while the session is running', async () => {
    act(() => {
      root.render(<ModelCombobox disabled />)
    })
    const trigger = container.querySelector('button') as HTMLButtonElement
    expect(trigger).not.toBeNull()
    expect(trigger.disabled).toBe(true)
    expect(trigger.getAttribute('title')).toBe('Locked while the session is running')

    // A pointerdown on a disabled trigger must not open the dropdown.
    await openDropdown()
    expect(menu()).toBeNull()
  })
})
