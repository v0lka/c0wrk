// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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

const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateLLMConfig: vi.fn(),
  invalidateConfigCache: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  getConfig: mocks.getConfig,
  updateLLMConfig: mocks.updateLLMConfig,
  MASKED_API_KEY: '***configured***',
}))
vi.mock('@/hooks/useConfigData', () => ({ invalidateConfigCache: mocks.invalidateConfigCache }))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { LLMSettings } from './LLMSettings'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useProxyDraftStore } from '@/stores/proxyDraftStore'

let container: HTMLDivElement
let root: Root

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function makeConfig() {
  return {
    loaded: true,
    llm: {
      auto_retry_max_seconds: 3600,
      default_model: 'anthropic/claude-sonnet',
      anthropic: { api_key: 'sk', models: ['claude-sonnet'] },
      openai_compatible: {
        lmstudio: { api_key: '', base_url: 'http://localhost:1234', models: ['glm-5.3'] },
      },
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.updateLLMConfig.mockResolvedValue(undefined)
  mocks.getConfig.mockResolvedValue(makeConfig())
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

/** The default-model picker's trigger button (first button in the panel). */
function defaultModelTrigger(): HTMLButtonElement {
  const btn = container.querySelector('button[aria-label="Default model"]') as HTMLButtonElement | null
  expect(btn).not.toBeNull()
  return btn!
}

/** Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`. */
async function openDefaultPicker(): Promise<void> {
  act(() => {
    defaultModelTrigger().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 10))
  })
}

describe('LLMSettings default-model picker (shared ModelPickerMenu)', () => {
  it('renders the shared picker showing the current default, not the old Combobox', async () => {
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()

    const trigger = defaultModelTrigger()
    // The trigger shows the bare model name of the composite default.
    expect(trigger.textContent).toContain('claude-sonnet')
    // The old "— Select a default model —" Combobox placeholder is gone.
    expect(container.textContent).not.toContain('Select a default model')
  })

  it('lists provider-grouped models and picks one by composite id', async () => {
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()

    await openDefaultPicker()

    const menu = document.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    // Provider group headers are rendered for both providers.
    expect(menu!.textContent).toContain('Anthropic')
    expect(menu!.textContent).toContain('lmstudio')
    // The "Default" option is hidden in the settings context (the picker IS
    // the default — a "use the default" entry would be self-referential).
    expect(menu!.textContent).not.toContain('Defaultactive')

    // Pick the lmstudio model — the menu item whose text is glm-5.3.
    const glmItem = Array.from(
      menu!.querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ).find((b) => b.textContent?.includes('glm-5.3'))
    expect(glmItem).toBeDefined()
    act(() => {
      glmItem!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The trigger now shows the newly picked bare name…
    expect(defaultModelTrigger().textContent).toContain('glm-5.3')
    // …and the config save path fired with the composite selector.
    await vi.waitFor(() => {
      const last = mocks.updateLLMConfig.mock.calls[mocks.updateLLMConfig.mock.calls.length - 1]
      expect(last?.[0]?.default_model).toBe('lmstudio/glm-5.3')
    })
  })

  it('portals the opened menu to <body> through Radix (issue #71: escapes the dialog transform/overflow)', async () => {
    // The previous hand-rolled menu portaled INTO the settings dialog so it
    // stayed interactive under the modal body lock — but DialogContent's
    // centering `transform` then became the containing block for its
    // `position: fixed`, displacing and clipping it (issue #71). Radix's
    // DismissableLayer instead portals to <body> (escaping any transformed or
    // overflow-hidden ancestor) while still counting as "inside" the dialog.
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()

    // First interaction with the freshly-mounted panel: open the picker.
    await openDefaultPicker()

    const menu = document.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    expect(container.contains(menu)).toBe(false)
    expect(document.body.contains(menu)).toBe(true)
  })
})

describe('LLMSettings section order', () => {
  /** Renders the panel and returns the panel container. */
  async function renderPanel(): Promise<void> {
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()
  }

  it('places Add compatible provider directly after the default-model field, above the provider lists', async () => {
    await renderPanel()

    const buttons = Array.from(container.querySelectorAll('button'))
    const addBtn = buttons.find(
      (b) => (b.textContent ?? '').trim() === 'Add compatible provider',
    )
    expect(addBtn).toBeDefined()

    const defaultTrigger = container.querySelector('button[aria-label="Default model"]')
    expect(defaultTrigger).not.toBeNull()

    // Rendered AFTER the default-model picker (the "after the default-model
    // picker" requirement)…
    expect(
      defaultTrigger!.compareDocumentPosition(addBtn!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    // …and BEFORE the first provider accordion (Anthropic is a fixed provider
    // present in the mocked config) — i.e. it no longer trails the lists.
    const anthropicAccordion = buttons.find((b) =>
      /^Anthropic\d+ models? enabled$/.test((b.textContent ?? '').trim()),
    )
    expect(anthropicAccordion).toBeDefined()
    expect(
      addBtn!.compareDocumentPosition(anthropicAccordion!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('opens the add-provider form in place, still above the provider lists', async () => {
    await renderPanel()

    const addBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === 'Add compatible provider',
    )
    expect(addBtn).toBeDefined()

    await act(async () => {
      addBtn!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const form = container.querySelector('h4')
    expect(form?.textContent).toBe('New compatible provider')
  })
})

// --- Per-provider TLS pin gate (ADR-054) ---

describe('LLMSettings TLS pin proxy gate', () => {
  const pin = 'k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g='

  beforeEach(() => {
    useProxyDraftStore.setState({ active: null })
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy: { enabled: false, url: '', bypass_list: [], tls_cert_dir: '' },
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'lmstudio/glm-5.3',
        anthropic: { api_key: 'sk', models: [] },
        openai_compatible: {
          lmstudio: {
            api_key: 'k',
            base_url: 'http://localhost:1234',
            models: ['glm-5.3'],
            tls_fingerprint: pin,
          },
        },
      },
    })
  })

  async function renderSettings() {
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()
  }

  /**
   * Expand the compatible provider's accordion so its form is mounted, then
   * open the pin collapse block so its field is mounted too (the block ships
   * collapsed by default; the fixed provider has no pin section, hence the
   * guard).
   */
  async function expandProvider() {
    const header = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('lmstudio'),
    )
    expect(header).toBeDefined()
    await act(async () => {
      header!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const pinTrigger = container.querySelector<HTMLButtonElement>('[data-slot="collapsible-trigger"]')
    if (pinTrigger) {
      await act(async () => {
        pinTrigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await flush()
    }
  }

  function fingerprintInput(): HTMLInputElement | null {
    return container.querySelector('input[placeholder*="SPKI DER"]')
  }

  function getButton(): HTMLButtonElement | null {
    const buttons = Array.from(container.querySelectorAll('button'))
    return (buttons.find((b) => b.textContent?.trim() === 'Get') as HTMLButtonElement) ?? null
  }

  it('shows an enabled pin section when no proxy is configured', async () => {
    await renderSettings()
    await expandProvider()

    expect(fingerprintInput()).not.toBeNull()
    expect(fingerprintInput()?.value).toBe(pin)
    expect(fingerprintInput()?.disabled).toBe(false)
    expect(getButton()?.disabled).toBe(false)
  })

  // The whole point of the draft store: the General tab toggles the proxy and
  // an ALREADY-MOUNTED LLM tab must react, with no dialog reload and no extra
  // config read (a re-read would be stale behind the 800 ms debounce, and
  // would contend with the proxy rebuild).
  it('disables the pin section when the proxy is toggled on elsewhere, without re-reading the config', async () => {
    await renderSettings()
    await expandProvider()
    expect(fingerprintInput()?.disabled).toBe(false)
    const readsBefore = mocks.getConfig.mock.calls.length

    await act(async () => {
      useProxyDraftStore.getState().setActive(true)
    })
    await flush()

    expect(fingerprintInput()?.disabled).toBe(true)
    expect(getButton()?.disabled).toBe(true)
    expect(container.textContent).toContain('HTTP Proxy')
    expect(mocks.getConfig.mock.calls.length).toBe(readsBefore)
  })

  it('re-enables the pin section when the proxy is turned back off', async () => {
    await renderSettings()
    await expandProvider()

    await act(async () => { useProxyDraftStore.getState().setActive(true) })
    await flush()
    expect(fingerprintInput()?.disabled).toBe(true)

    await act(async () => { useProxyDraftStore.getState().setActive(false) })
    await flush()
    expect(fingerprintInput()?.disabled).toBe(false)
    // The persisted pin was preserved throughout and re-arms.
    expect(fingerprintInput()?.value).toBe(pin)
  })

  it('starts disabled when the loaded config already has an effective proxy', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy: { enabled: true, url: 'http://proxy.lan:3128', bypass_list: [], tls_cert_dir: '' },
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'lmstudio/glm-5.3',
        anthropic: { api_key: 'sk', models: [] },
        openai_compatible: {
          lmstudio: { api_key: 'k', base_url: 'http://localhost:1234', models: ['glm-5.3'], tls_fingerprint: pin },
        },
      },
    })

    await renderSettings()
    await expandProvider()

    expect(fingerprintInput()?.disabled).toBe(true)
    expect(getButton()?.disabled).toBe(true)
  })

  it('shows no pin section for the fixed anthropic provider', async () => {
    await renderSettings()
    const header = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Anthropic') && !b.textContent?.includes('Compatible'),
    )
    expect(header).toBeDefined()
    await act(async () => {
      header!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // Only the compatible provider's section can exist; the fixed one has no
    // base_url and talks to a vendor endpoint with a public certificate.
    const inputs = container.querySelectorAll('input[placeholder*="SPKI DER"]')
    expect(inputs).toHaveLength(0)
  })
})

// --- Auto-retry interval (compatible providers only) ---

describe('LLMSettings auto-retry interval', () => {
  beforeEach(() => {
    useProxyDraftStore.setState({ active: null })
  })

  async function renderSettings() {
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()
  }

  /** Expand the provider accordion whose header mentions `name`. */
  async function expandProvider(name: string) {
    const header = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes(name),
    )
    expect(header).toBeDefined()
    await act(async () => {
      header!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
  }

  it('renders the field with the persisted interval for a compatible provider', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy: { enabled: false, url: '', bypass_list: [], tls_cert_dir: '' },
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'lmstudio/glm-5.3',
        anthropic: { api_key: 'sk', models: [] },
        openai_compatible: {
          lmstudio: {
            api_key: 'k',
            base_url: 'http://localhost:1234',
            models: ['glm-5.3'],
            auto_retry_seconds: 30,
          },
        },
      },
    })

    await renderSettings()
    await expandProvider('lmstudio')

    const input = container.querySelector<HTMLInputElement>('input[aria-label="Auto-retry interval"]')
    expect(input).not.toBeNull()
    // The value from GetConfig is displayed.
    expect(input?.value).toBe('30')
    expect(container.textContent).toContain('Auto-retry interval')
  })

  it('shows no field for the fixed anthropic provider', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy: { enabled: false, url: '', bypass_list: [], tls_cert_dir: '' },
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'lmstudio/glm-5.3',
        anthropic: { api_key: 'sk', models: ['glm-5.3'] },
        openai_compatible: {
          lmstudio: { api_key: 'k', base_url: 'http://localhost:1234', models: [] },
        },
      },
    })

    await renderSettings()
    await expandProvider('Anthropic')

    expect(container.querySelector('input[aria-label="Auto-retry interval"]')).toBeNull()
  })

  // End-to-end through the real form: picking a preset writes the draft and
  // the debounced save carries the interval in the compatible map only.
  it('saves an explicit 0 through the form into the compatible map only', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy: { enabled: false, url: '', bypass_list: [], tls_cert_dir: '' },
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'lmstudio/glm-5.3',
        anthropic: { api_key: 'sk', models: [] },
        openai_compatible: {
          lmstudio: {
            api_key: 'k',
            base_url: 'http://localhost:1234',
            models: ['glm-5.3'],
            auto_retry_seconds: 120,
          },
        },
      },
    })

    await renderSettings()
    await expandProvider('lmstudio')

    const input = container.querySelector<HTMLInputElement>('input[aria-label="Auto-retry interval"]')
    expect(input?.value).toBe('120')

    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
    await act(async () => {
      setter!.call(input!, '0')
      input!.dispatchEvent(new Event('input', { bubbles: true }))
      input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    await vi.waitFor(() => {
      const last = mocks.updateLLMConfig.mock.calls[mocks.updateLLMConfig.mock.calls.length - 1]
      const compatible = last?.[0]?.openai_compatible as Record<string, { auto_retry_seconds?: number }> | undefined
      expect(compatible?.lmstudio?.auto_retry_seconds).toBe(0)
      // The fixed provider entry never carries the field.
      const fixed = last?.[0]?.anthropic
      expect(fixed).not.toHaveProperty('auto_retry_seconds')
    })
  })
})
