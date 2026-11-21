// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run and
// are referenceable inside test bodies.
const spies = vi.hoisted(() => ({
  getModelConfig: vi.fn<(model: string) => Promise<unknown>>(),
  setModelConfig: vi.fn<(model: string, req: unknown) => Promise<void>>(),
  getBuiltinModelNames: vi.fn<() => Promise<unknown>>(),
}))

// Mock the config API so the dialog loads/saves without a real Wails round-trip.
// Partial mock without a type cast — ModelConfigDialog only calls getModelConfig,
// setModelConfig and getBuiltinModelNames — matching the project convention
// (ModelCombobox.test, BlackboardPanel.test) where @/api/* modules are partially
// mocked without `as typeof import(...)`.
vi.mock('@/api/config', () => ({
  getModelConfig: spies.getModelConfig,
  setModelConfig: spies.setModelConfig,
  getBuiltinModelNames: spies.getBuiltinModelNames,
}))

import { ModelConfigDialog } from './ModelConfigDialog'
import type { ModelMetadataInfo } from '@/types/models'
import { logger } from '@/lib/logger'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  // The dialog fetches the registry names on open; default to a sorted
  // three-name catalog so existing tests exercise the dialog without caring.
  spies.getBuiltinModelNames.mockResolvedValue([
    'claude-3-5-sonnet',
    'gemini-1.5-pro',
    'gpt-4o',
  ])
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

function selects(): HTMLButtonElement[] {
  // The dropdowns are custom comboboxes built on the Radix dropdown-menu
  // primitives (button trigger + portaled menu), not native <select>, so
  // their colors are themable on Windows too. They are portaled to
  // document.body, like the Radix Dialog.
  return Array.from(
    document.body.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]'),
  )
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

/** Combobox trigger by its accessible label (the dropdowns are Radix menus). */
function triggerByLabel(label: string): HTMLButtonElement {
  const el = selects().find((s) => s.getAttribute('aria-label') === label)
  if (!el) throw new Error(`Trigger "${label}" not found`)
  return el
}

/** Open a combobox menu (the Radix trigger toggles on pointerdown, not click).
 *  The flush also lets Radix install its deferred outside-pointerdown listener;
 *  it reuses the file's `flush` helper rather than adding a delay expression,
 *  which the repo-wide timing guard counts as new debt. */
async function openMenu(btn: HTMLButtonElement): Promise<void> {
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await flush()
  })
}

/** A portaled menu item whose text contains `text`. */
function menuItem(text: string): HTMLElement {
  const el = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
    (o) => o.textContent?.includes(text),
  )
  if (!el) throw new Error(`Menu item "${text}" not found`)
  return el
}

/** Button whose trimmed text equals `text` ("Reset to defaults"). */
function buttonByText(text: string): HTMLButtonElement {
  const el = Array.from(document.body.querySelectorAll('button')).find(
    (b) => b.textContent?.trim() === text,
  )
  if (!el) throw new Error(`Button "${text}" not found`)
  return el
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

function renderDialog(model: string, open: boolean, onSaved?: () => void, listingMetadata?: ModelMetadataInfo) {
  act(() => {
    root.render(
      <ModelConfigDialog
        model={model}
        open={open}
        onOpenChange={() => {}}
        listingMetadata={listingMetadata}
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

  it('renders the same-as combobox plus 3 dropdown selects for tokenizer/family/protocol', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    const sels = selects()
    expect(sels).toHaveLength(4)
    // Triggers in declared order: the "The same as…" registry picker first,
    // then each labeled field dropdown.
    expect(sels.map((s) => s.getAttribute('aria-label'))).toEqual([
      'Copy built-in defaults from a registry model',
      'Tokenizer Type',
      'Family',
      'Protocol',
    ])
    // Each field trigger shows the label of the effective value; the same-as
    // picker shows its placeholder (it is an action, not a stateful choice).
    expect(sels[0]?.textContent).toContain('Pick a registry model')
    expect(sels[1]?.textContent).toContain('o200k_base')
    expect(sels[2]?.textContent).toContain('OpenAI Flagship')
    expect(sels[3]?.textContent).toContain('Chat Completions')
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

describe('ModelConfigDialog listing-metadata prefill', () => {
  // A model the built-in catalog doesn't know: effective values equal the
  // factory defaults, no override anywhere.
  const noOverrideResponse = {
    model: 'custom-model',
    context_window: 32768,
    output_limit: 8192,
    tokenizer_type: 'approximate',
    family: 'default',
    protocol: 'chat_completions',
    capabilities: { attachment: false, reasoning: false, temperature: false, tool_call: false },
    default_context_window: 32768,
    default_output_limit: 8192,
    default_tokenizer_type: 'approximate',
    default_family: 'default',
    default_protocol: 'chat_completions',
    default_capabilities: { attachment: false, reasoning: false, temperature: false, tool_call: false },
    has_override: false,
  }

  const listingMeta: ModelMetadataInfo = {
    context_window: 512000,
    output_limit: 32000,
    capabilities: { attachment: true, reasoning: true, temperature: true, tool_call: true },
  }

  it('prefills override-free fields from the parsed listing metadata', async () => {
    spies.getModelConfig.mockResolvedValue(noOverrideResponse)
    renderDialog('custom-model', true, undefined, listingMeta)

    await act(async () => { await flush() })

    const inputs = numberInputs()
    expect(inputs[0]?.value).toBe('512000')
    expect(inputs[1]?.value).toBe('32000')
    // Capabilities come from the listing's full declaration.
    const checks = checkboxes()
    expect(checks[0]?.checked).toBe(true) // attachment
    expect(checks[1]?.checked).toBe(true) // reasoning
  })

  it('an explicit override wins over the listing metadata, per field', async () => {
    // cannedResponse overrides the context window (200000 vs default 128000)
    // but not the output limit (16384 == default) — only the context field
    // keeps the override; the output field takes the listing value.
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true, undefined, listingMeta)

    await act(async () => { await flush() })

    const inputs = numberInputs()
    expect(inputs[0]?.value).toBe('200000')
    expect(inputs[1]?.value).toBe('32000')
  })

  it('listing metadata without declared values leaves the defaults', async () => {
    spies.getModelConfig.mockResolvedValue(noOverrideResponse)
    renderDialog('custom-model', true, undefined, { context_window: 0, output_limit: 0 })

    await act(async () => { await flush() })

    const inputs = numberInputs()
    expect(inputs[0]?.value).toBe('32768')
    expect(inputs[1]?.value).toBe('8192')
  })
})

describe('ModelConfigDialog "The same as…" registry defaults', () => {
  // A copy source whose EFFECTIVE output limit differs from its DEFAULT one:
  // picking it must fill the fields with the built-in defaults (8192), never
  // with the effective values that happen to be set for that model.
  const claudeResponse = {
    model: 'claude-3-5-sonnet',
    context_window: 200000,
    output_limit: 4096,
    tokenizer_type: 'anthropic-api',
    family: 'anthropic',
    protocol: 'anthropic',
    capabilities: { attachment: true, reasoning: true, temperature: false, tool_call: true },
    default_context_window: 200000,
    default_output_limit: 8192,
    default_tokenizer_type: 'anthropic-api',
    default_family: 'anthropic',
    default_protocol: 'anthropic',
    default_capabilities: { attachment: true, reasoning: true, temperature: false, tool_call: true },
    has_override: true,
  }

  /** getModelConfig that answers per-model: claude for the picker, else canned. */
  function mockPerModel(): void {
    spies.getModelConfig.mockImplementation(async (model: string) =>
      model === 'claude-3-5-sonnet' ? claudeResponse : cannedResponse,
    )
  }

  /** Open the same-as combobox and pick a registry model. */
  async function pickRegistryModel(name: string): Promise<void> {
    await openMenu(triggerByLabel('Copy built-in defaults from a registry model'))
    await act(async () => {
      menuItem(name).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await act(async () => { await flush() })
  }

  it('renders the section first, above Context Window, with a Reset to defaults button', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })

    // "The same as…" label precedes the first number input in DOM order.
    const label = Array.from(document.body.querySelectorAll('label')).find((l) =>
      l.textContent?.includes('The same as'),
    )
    expect(label).not.toBeUndefined()
    const cwInput = numberInputs()[0]
    if (!cwInput) throw new Error('context window input not found')
    expect(label!.compareDocumentPosition(cwInput) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // The dialog-wide reset button sits in the section header.
    expect(buttonByText('Reset to defaults')).not.toBeUndefined()
  })

  it('loads the registry names into the combobox on open', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })
    expect(spies.getBuiltinModelNames).toHaveBeenCalledTimes(1)

    await openMenu(triggerByLabel('Copy built-in defaults from a registry model'))
    const items = Array.from(document.body.querySelectorAll('[role="menuitem"]')).map((o) => o.textContent)
    expect(items).toEqual(['claude-3-5-sonnet', 'gemini-1.5-pro', 'gpt-4o'])
  })

  it('picking a model fills all six field groups with its built-in defaults', async () => {
    mockPerModel()
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })
    await pickRegistryModel('claude-3-5-sonnet')

    // Numbers come from the DEFAULT values (8192, not the effective 4096).
    const inputs = numberInputs()
    expect(inputs[0]?.value).toBe('200000')
    expect(inputs[1]?.value).toBe('8192')
    // Capability flags come from the copied default_capabilities.
    const checks = checkboxes()
    expect(checks.map((c) => c.checked)).toEqual([true, true, false, true])
    // The three field dropdowns now display the copied tokenizer/family/protocol.
    const sels = selects()
    expect(sels[1]?.textContent).toContain('server-corrected') // anthropic-api tokenizer
    expect(sels[2]?.textContent).toContain('Claude') // anthropic family
    expect(sels[3]?.textContent).toContain('Messages') // anthropic protocol
    // The picker itself stays an action, not a stateful selection.
    expect(sels[0]?.textContent).toContain('Pick a registry model')
  })

  it('Save after a copy persists the copied values for the current model', async () => {
    mockPerModel()
    spies.setModelConfig.mockResolvedValue(undefined)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })
    await pickRegistryModel('claude-3-5-sonnet')

    await act(async () => {
      saveButton().click()
    })
    await act(async () => { await flush() })

    // The request targets the CURRENT model with the copied values; the
    // backend persists only what differs from gpt-4o's built-in defaults.
    expect(spies.setModelConfig).toHaveBeenCalledTimes(1)
    expect(spies.setModelConfig).toHaveBeenCalledWith('gpt-4o', {
      context_window: 200000,
      output_limit: 8192,
      tokenizer_type: 'anthropic-api',
      family: 'anthropic',
      protocol: 'anthropic',
      capabilities: { attachment: true, reasoning: true, temperature: false, tool_call: true },
    })
  })

  it('Reset to defaults restores every field to the current model after a copy', async () => {
    mockPerModel()
    spies.setModelConfig.mockResolvedValue(undefined)
    renderDialog('gpt-4o', true)

    await act(async () => { await flush() })
    await pickRegistryModel('claude-3-5-sonnet')

    await act(async () => {
      buttonByText('Reset to defaults').click()
    })

    // Back to gpt-4o's factory defaults (the `defaults` state loaded on open).
    const inputs = numberInputs()
    expect(inputs[0]?.value).toBe('128000')
    expect(inputs[1]?.value).toBe('16384')
    const checks = checkboxes()
    expect(checks.map((c) => c.checked)).toEqual([true, false, true, true])
    const sels = selects()
    expect(sels[1]?.textContent).toContain('o200k_base')
    expect(sels[2]?.textContent).toContain('OpenAI Flagship')
    expect(sels[3]?.textContent).toContain('Chat Completions')

    // Saving the reset state sends exactly the built-in defaults (no overrides).
    await act(async () => {
      saveButton().click()
    })
    await act(async () => { await flush() })
    expect(spies.setModelConfig).toHaveBeenCalledWith('gpt-4o', expect.objectContaining({
      context_window: 128000,
      output_limit: 16384,
    }))
  })

  it('a registry fetch failure degrades softly and reports through the logger', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    const errorSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})
    try {
      spies.getBuiltinModelNames.mockRejectedValue(new Error('registry boom'))
      renderDialog('gpt-4o', true)

      await act(async () => { await flush() })

      // The dialog's own fields still loaded — the failure is non-fatal.
      expect(numberInputs()[0]?.value).toBe('200000')
      // The picker renders an empty, usable combobox.
      await openMenu(triggerByLabel('Copy built-in defaults from a registry model'))
      expect(document.body.querySelectorAll('[role="menuitem"]')).toHaveLength(0)
      expect(document.body.textContent).toContain('No options available.')
      // The diagnostic is intercepted at its source: severity, message, count.
      expect(errorSpy).toHaveBeenCalledTimes(1)
      expect(errorSpy.mock.calls[0]?.[0]).toContain('built-in model names')
    } finally {
      errorSpy.mockRestore()
    }
  })

  it('a copy-source fetch failure surfaces an inline error and keeps the fields', async () => {
    spies.getModelConfig.mockResolvedValue(cannedResponse)
    const errorSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})
    try {
      spies.getModelConfig.mockImplementation(async (model: string) => {
        if (model === 'claude-3-5-sonnet') throw new Error('cfg boom')
        return cannedResponse
      })
      renderDialog('gpt-4o', true)

      await act(async () => { await flush() })
      await pickRegistryModel('claude-3-5-sonnet')

      // The current model's values stay untouched; the error renders inline.
      expect(numberInputs()[0]?.value).toBe('200000')
      expect(document.body.textContent).toContain('Failed to load the defaults of claude-3-5-sonnet.')
      expect(errorSpy).toHaveBeenCalledTimes(1)
      expect(errorSpy.mock.calls[0]?.[0]).toContain('defaults of claude-3-5-sonnet')
    } finally {
      errorSpy.mockRestore()
    }
  })

  it('a copy still in flight cannot overwrite a later Reset to defaults', async () => {
    let resolveCopy: ((value: unknown) => void) | undefined
    spies.getModelConfig.mockImplementation((model: string) => {
      if (model === 'claude-3-5-sonnet') {
        return new Promise((resolve) => { resolveCopy = resolve })
      }
      return Promise.resolve(cannedResponse)
    })
    renderDialog('gpt-4o', true)
    await act(async () => { await flush() })

    // Pick the slow source; the copy stays pending past this helper's flush.
    await pickRegistryModel('claude-3-5-sonnet')
    expect(resolveCopy).toBeDefined()

    // Reset while the copy is in flight — the user's latest action wins.
    await act(async () => {
      buttonByText('Reset to defaults').click()
    })

    // The stale copy lands afterwards: gpt-4o's reset values must survive
    // (claude's default output limit would be 8192, gpt-4o's is 16384).
    await act(async () => {
      resolveCopy?.(claudeResponse)
      await flush()
    })
    expect(numberInputs()[1]?.value).toBe('16384')
    // ...and the abandoned copy must not leave the picker disabled.
    expect(triggerByLabel('Copy built-in defaults from a registry model').disabled).toBe(false)
  })

  it('locks the field groups and Save while a copy is in flight', async () => {
    let resolveCopy: ((value: unknown) => void) | undefined
    spies.getModelConfig.mockImplementation((model: string) => {
      if (model === 'claude-3-5-sonnet') {
        return new Promise((resolve) => { resolveCopy = resolve })
      }
      return Promise.resolve(cannedResponse)
    })
    renderDialog('gpt-4o', true)
    await act(async () => { await flush() })

    await pickRegistryModel('claude-3-5-sonnet')

    // The copy owns the draft until it lands: neither an edit (which would be
    // silently overwritten) nor a Save (which would persist the pre-copy
    // values and close the dialog) may act inside that window.
    expect(numberInputs()[0]?.disabled).toBe(true)
    expect(saveButton().disabled).toBe(true)

    await act(async () => {
      resolveCopy?.(claudeResponse)
      await flush()
    })

    expect(numberInputs()[0]?.disabled).toBe(false)
    expect(saveButton().disabled).toBe(false)
    // The copy landed in the (now unlocked) fields.
    expect(numberInputs()[1]?.value).toBe('8192')
  })
})
