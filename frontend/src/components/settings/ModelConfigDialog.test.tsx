// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run and
// are referenceable inside test bodies.
const spies = vi.hoisted(() => ({
  getModelConfig: vi.fn<(model: string) => Promise<unknown>>(),
  setModelConfig: vi.fn<(model: string, req: unknown) => Promise<void>>(),
}))

// Mock the config API so the dialog loads/saves without a real Wails round-trip.
// Partial mock without a type cast — ModelConfigDialog only calls getModelConfig
// and setModelConfig — matching the project convention (ModelCombobox.test,
// BlackboardPanel.test) where @/api/* modules are partially mocked without
// `as typeof import(...)`.
vi.mock('@/api/config', () => ({
  getModelConfig: spies.getModelConfig,
  setModelConfig: spies.setModelConfig,
}))

import { ModelConfigDialog } from './ModelConfigDialog'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
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

// The Dialog renders via a Radix Portal to document.body, so all queries must
// target document.body rather than the local container.

function numberInputs(): HTMLInputElement[] {
  return Array.from(document.body.querySelectorAll('input[type="number"]'))
}

function selects(): HTMLSelectElement[] {
  return Array.from(document.body.querySelectorAll('select'))
}

function checkboxes(): HTMLInputElement[] {
  return Array.from(document.body.querySelectorAll('input[type="checkbox"]'))
}

function saveButton(): HTMLButtonElement {
  const btn = Array.from(document.body.querySelectorAll('button')).find(
    (b) => b.textContent?.trim() === 'Save',
  )
  if (!btn) throw new Error('Save button not found')
  return btn
}

/** Flush pending microtasks/timers so async effects (getModelConfig) settle. */
function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 50))
}

// Canned effective config for gpt-4o: override context window, default
// output limit (so the effective output limit equals the default), and
// built-in metadata defaults for tokenizer/family/protocol/capabilities.
const cannedResponse = {
  model: 'gpt-4o',
  context_window: 200000,
  output_limit: 16384,
  tokenizer_type: 'tiktoken/o200k_base',
  family: 'openai_flagship',
  protocol: 'chat_completions',
  capabilities: { attachment: true, reasoning: false, temperature: true, tool_call: true },
  default_context_window: 128000,
  default_output_limit: 16384,
  default_tokenizer_type: 'tiktoken/o200k_base',
  default_family: 'openai_flagship',
  default_protocol: 'chat_completions',
  default_capabilities: { attachment: true, reasoning: false, temperature: true, tool_call: true },
  has_override: true,
}

function renderDialog(model: string, open: boolean, onSaved?: () => void) {
  act(() => {
    root.render(
      <ModelConfigDialog
        model={model}
        open={open}
        onOpenChange={() => {}}
        onSaved={onSaved}
      />,
    )
  })
}

describe('ModelConfigDialog', () => {
  it('fetches config on open and pre-fills the effective values', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    expect(spies.getModelConfig).toHaveBeenCalledWith('gpt-4o')
    const inputs = numberInputs()
    expect(inputs).toHaveLength(2)
    expect(inputs[0]?.value).toBe('200000')
    expect(inputs[1]?.value).toBe('16384')
  })

  it('renders 3 dropdown selects for tokenizer/family/protocol', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    const sels = selects()
    expect(sels).toHaveLength(3)
    // Tokenizer select pre-filled with effective value.
    expect(sels[0]?.value).toBe('tiktoken/o200k_base')
    // Family select.
    expect(sels[1]?.value).toBe('openai_flagship')
    // Protocol select.
    expect(sels[2]?.value).toBe('chat_completions')
  })

  it('renders 4 capability checkboxes with correct initial state', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    const checks = checkboxes()
    expect(checks).toHaveLength(4)
    // Attachment, Reasoning, Temperature, ToolCall (in declared order).
    expect(checks[0]?.checked).toBe(true)  // attachment
    expect(checks[1]?.checked).toBe(false) // reasoning
    expect(checks[2]?.checked).toBe(true)  // temperature
    expect(checks[3]?.checked).toBe(true)  // tool_call
  })

  it('shows the built-in defaults as hints', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    // Default hints are rendered as text. The context window is overridden
    // (200000 vs default 128000) so it shows "(overridden)"; the output limit
    // equals the default so it shows "(using default)".
    const text = document.body.textContent ?? ''
    expect(text).toContain('128,000')
    expect(text).toContain('(overridden)')
    expect(text).toContain('(using default)')
  })

  it('calls setModelConfig with all form values on Save', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    spies.setModelConfig.mockResolvedValue(undefined)
    const onSaved = vi.fn()
    renderDialog('gpt-4o', true, onSaved)

    await act(async () => { await flush() })

    // Mutate the context window input to a new value. React 19 overrides the
    // input value setter, so we must use the native HTMLInputElement prototype
    // setter then dispatch the event for the synthetic onChange to fire.
    const inputs = numberInputs()
    const cwInput = inputs[0]
    if (!cwInput) throw new Error('context window input not found')
    const nativeInputSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )?.set
    if (!nativeInputSetter) throw new Error('native input setter not found')
    await act(async () => {
      nativeInputSetter.call(cwInput, '300000')
      cwInput.dispatchEvent(new Event('input', { bubbles: true }))
    })

    await act(async () => {
      saveButton().click()
    })
    await act(async () => { await flush() })

    expect(spies.setModelConfig).toHaveBeenCalledTimes(1)
    expect(spies.setModelConfig).toHaveBeenCalledWith('gpt-4o', {
      context_window: 300000,
      output_limit: 16384,
      tokenizer_type: 'tiktoken/o200k_base',
      family: 'openai_flagship',
      protocol: 'chat_completions',
      capabilities: { attachment: true, reasoning: false, temperature: true, tool_call: true },
    })
    expect(onSaved).toHaveBeenCalledTimes(1)
  })

  it('sends updated capability toggles on Save', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    spies.setModelConfig.mockResolvedValue(undefined)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    // Toggle the reasoning checkbox from false → true.
    const checks = checkboxes()
    const reasoningCheck = checks[1]
    if (!reasoningCheck) throw new Error('reasoning checkbox not found')
    await act(async () => {
      reasoningCheck.click()
    })

    await act(async () => {
      saveButton().click()
    })
    await act(async () => { await flush() })

    expect(spies.setModelConfig).toHaveBeenCalledWith('gpt-4o', expect.objectContaining({
      capabilities: { attachment: true, reasoning: true, temperature: true, tool_call: true },
    }))
  })

  it('sends the default when a number field is cleared (no 0 snap)', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    spies.setModelConfig.mockResolvedValue(undefined)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    // Clear the context window input entirely — previously Number('') === 0
    // snapped the field to 0 and the backend rejected it; now '' is kept and
    // the default is sent on save.
    const cwInput = numberInputs()[0]
    if (!cwInput) throw new Error('context window input not found')
    const nativeInputSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      'value',
    )?.set
    if (!nativeInputSetter) throw new Error('native input setter not found')
    await act(async () => {
      nativeInputSetter.call(cwInput, '')
      cwInput.dispatchEvent(new Event('input', { bubbles: true }))
    })

    await act(async () => {
      saveButton().click()
    })
    await act(async () => { await flush() })

    expect(spies.setModelConfig).toHaveBeenCalledTimes(1)
    expect(spies.setModelConfig).toHaveBeenCalledWith('gpt-4o', expect.objectContaining({
      // Empty falls back to the built-in default (128000), never 0.
      context_window: 128000,
    }))
  })

  it('renders the model name in the title', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    const title = document.body.querySelector('[data-slot="dialog-title"]')
    expect(title?.textContent).toContain('gpt-4o')
  })
})
