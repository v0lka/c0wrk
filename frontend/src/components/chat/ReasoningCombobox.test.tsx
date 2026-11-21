// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ReasoningCombobox } from './ReasoningCombobox'
import { useInputModeStore } from '@/stores/inputModeStore'

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

/** The trigger is the only button while the dropdown is closed. */
function trigger(): HTMLButtonElement {
  const el = container.querySelector('button')
  expect(el).not.toBeNull()
  return el as HTMLButtonElement
}

/** The dropdown renders inline (useDropdown, no portal) when open. */
function menuButtons(): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button')).slice(1)
}

function click(el: HTMLElement): void {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

function findButtonByLabel(label: string): HTMLButtonElement {
  const el = menuButtons().find((b) => b.textContent?.includes(label))
  expect(el, `menu button ${label}`).toBeDefined()
  return el as HTMLButtonElement
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
    // not the "Auto" fallback.
    configData.allModels = REASONING_MODELS
    configData.defaultModel = 'anthropic/claude-sonnet'
    configData.loaded = true
    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')
    expect(trigger().textContent).toContain('medium')
    expect(trigger().textContent).not.toContain('Auto (')
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
    // unsupported for the resolved family → reset to the family default.
    useInputModeStore.setState({ selectedModel: 'PT/glm-5', selectedReasoning: 'high' })

    renderCombobox()

    expect(useInputModeStore.getState().selectedReasoning).toBeNull()
    expect(trigger().textContent).toContain('Auto (medium)')
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

describe('ReasoningCombobox selection UI', () => {
  it('selects an option from the dropdown and marks it active', () => {
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: null })

    renderCombobox()
    expect(trigger().textContent).toContain('Auto (high)')

    click(trigger())
    const options = menuButtons()
    // Default + three native options for the anthropic family; "active" marks
    // the Default entry (no explicit selection yet). Each option's textContent
    // concatenates its label with the trailing status span.
    expect(options.map((b) => b.textContent)).toEqual([
      'Default (high)active',
      'low',
      'medium',
      'high',
    ])

    click(findButtonByLabel('medium'))
    expect(useInputModeStore.getState().selectedReasoning).toBe('medium')
    // The dropdown closes after a pick.
    expect(menuButtons()).toHaveLength(0)
    expect(trigger().textContent).toContain('medium')
  })

  it('returns to the family default when the Default option is chosen', () => {
    useInputModeStore.setState({ selectedModel: null, selectedReasoning: 'low' })

    renderCombobox()
    click(trigger())
    click(findButtonByLabel('Default'))

    expect(useInputModeStore.getState().selectedReasoning).toBeNull()
    expect(trigger().textContent).toContain('Auto (high)')
  })

  it('renders nothing while the config has not loaded', () => {
    configData.loaded = false
    renderCombobox()
    expect(container.querySelector('button')).toBeNull()
  })
})
