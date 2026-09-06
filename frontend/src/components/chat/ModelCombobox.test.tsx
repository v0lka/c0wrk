// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ModelCombobox } from './ModelCombobox'
import { useInputModeStore } from '@/stores/inputModeStore'

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

function openDropdown(): HTMLButtonElement {
  const trigger = container.querySelector('button')
  expect(trigger).not.toBeNull()
  act(() => {
    trigger!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  return trigger!
}

describe('ModelCombobox portal', () => {
  it('renders the dropdown into document.body (not the component subtree) when open', () => {
    // Closed initially: no listbox anywhere.
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()

    openDropdown()

    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()
    // The menu lives in a portal under <body>, separate from the trigger's DOM.
    expect(container.contains(listbox)).toBe(false)
    expect(document.body.contains(listbox)).toBe(true)
  })

  it('positions the portaled menu fixed with a high z-index', () => {
    openDropdown()
    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()
    expect(listbox!.style.position).toBe('fixed')
    // z-50 exceeds the message input area (auto), chat area (z-10/z-20) and
    // pending actions bar (auto).
    expect(listbox!.style.zIndex).toBe('50')
  })

  it('keeps the dropdown open when clicking inside the portaled menu', () => {
    openDropdown()
    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()

    // A mousedown inside the portal menu must NOT be treated as an outside click.
    act(() => {
      listbox!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()
  })

  it('dismisses the dropdown on an outside click (outside trigger + portal)', () => {
    openDropdown()
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()

    // A mousedown on <body> (outside both the trigger wrapper and the portal menu).
    act(() => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
  })

  it('does not display a stale global default that is no longer selectable', () => {
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

    openDropdown()
    const defaultOption = document.body.querySelector('[role="listbox"] button')
    expect(defaultOption?.textContent).toBe('Defaultactive')
    expect(defaultOption?.textContent).not.toContain('GLM-5.2-FP8')
  })

  it('renders and selects a connected ChatGPT subscription model', () => {
    spies.configData.allModels = [
      { name: 'claude-sonnet', provider: 'anthropic', family: 'anthropic', vision: true },
      { name: 'gpt-5.4', provider: 'chatgpt_subscription', family: 'openai_flagship', vision: true },
    ]
    act(() => {
      root.render(<ModelCombobox />)
    })

    openDropdown()
    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement
    expect(listbox.textContent).toContain('ChatGPT subscription')
    const buttons = listbox.querySelectorAll('button')
    act(() => {
      buttons[buttons.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(useInputModeStore.getState().selectedModel).toBe('chatgpt_subscription/gpt-5.4')
    expect(spies.setDefaultModel).toHaveBeenCalledWith('chatgpt_subscription/gpt-5.4')
  })

  it('selects a model and closes when a portaled option is clicked', () => {
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')
    // Default option + two models = 3 buttons.
    expect(options.length).toBe(3)

    act(() => {
      // Last option button is the 'gpt-4o' model.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // The selected value is the composite selector "provider/name".
    expect(useInputModeStore.getState().selectedModel).toBe('chatgpt/gpt-4o')
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
  })
})

describe('ModelCombobox default-model persistence', () => {
  it('persists the picked model as default_model and invalidates the config cache', async () => {
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')

    spies.setDefaultModel.mockResolvedValue(undefined)

    act(() => {
      // Last option button is the 'gpt-4o' model.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

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
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')

    spies.setDefaultModel.mockResolvedValue(undefined)

    act(() => {
      // First option button is the "Default" entry (resets to global default).
      options[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

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

    openDropdown()
    let options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      // Pick gpt-4o first; its request remains in flight.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    openDropdown()
    options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      // Then select claude-sonnet before the first request settles.
      options[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
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

    openDropdown()
    let options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    openDropdown()
    options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      // Cancel the optimistic pick before its persistence succeeds.
      options[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await act(async () => { resolveFirst() })

    openDropdown()
    options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
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

    openDropdown()
    let options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    openDropdown()
    options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

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

    openDropdown()
    let options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    openDropdown()
    options = document.body.querySelectorAll('[role="listbox"] button')
    act(() => {
      options[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
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
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')

    spies.setDefaultModel.mockRejectedValue(new Error('boom'))

    // Async act so the rejection's rollback microtask runs inside the act
    // scope rather than after it.
    await act(async () => {
      // Last option button is the 'gpt-4o' model.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    await vi.waitFor(() => {
      expect(spies.invalidateConfigCache).toHaveBeenCalledTimes(1)
    })
  })

  it('rolls back the per-message override when persist fails (no silent divergence)', async () => {
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')

    spies.setDefaultModel.mockRejectedValue(new Error('boom'))

    // Async act so the rejection's rollback microtask runs inside the act
    // scope rather than after it. The optimistic value is captured
    // synchronously right after the click, before the rollback flushes.
    let optimistic: string | null = 'unset'
    await act(async () => {
      // Last option button is the 'gpt-4o' model.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
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
  it('renders a disabled trigger and ignores clicks while the session is running', () => {
    act(() => {
      root.render(<ModelCombobox disabled />)
    })
    const trigger = container.querySelector('button') as HTMLButtonElement
    expect(trigger).not.toBeNull()
    expect(trigger.disabled).toBe(true)
    expect(trigger.getAttribute('title')).toBe('Locked while the session is running')

    // A click on a disabled button must not open the dropdown.
    act(() => {
      trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
  })
})
