// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
      default_model: 'anthropic/claude-sonnet',
      anthropic: { api_key: 'sk', models: ['claude-sonnet'] },
      openai_compatible: {
        lmstudio: { api_key: '', base_url: 'http://localhost:1234', models: ['glm-5.3'], tls_fingerprint: '' },
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

    act(() => {
      defaultModelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const listbox = document.querySelector('[role="listbox"]')
    expect(listbox).not.toBeNull()
    // Provider group headers are rendered for both providers.
    expect(listbox!.textContent).toContain('Anthropic')
    expect(listbox!.textContent).toContain('lmstudio')
    // The "Default" option is hidden in the settings context (the picker IS
    // the default — a "use the default" entry would be self-referential).
    expect(listbox!.textContent).not.toContain('Defaultactive')

    // Pick the lmstudio model — the entry button whose text is glm-5.3.
    const glmBtn = Array.from(
      listbox!.querySelectorAll('button'),
    ).find((b) => b.textContent?.includes('glm-5.3'))
    expect(glmBtn).toBeDefined()
    act(() => {
      glmBtn!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
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

  it('portals the FIRST-opened dropdown inside the settings container, not document.body', async () => {
    // Regression (review finding): the container div mounts only on the
    // render after `isLoading` flips to false, so a plain `ref.current` read
    // during that render is still null and the menu would portal to
    // document.body — inert inside the Radix settings dialog (pointer-events:
    // none on <body>). The portal target must be populated by the time the
    // user's first interaction opens the menu.
    await act(async () => {
      root.render(
        <TooltipProvider>
          <LLMSettings />
        </TooltipProvider>,
      )
    })
    await flush()

    // First interaction with the freshly-mounted panel: open the picker.
    act(() => {
      defaultModelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const listbox = document.querySelector('[role="listbox"]')
    expect(listbox).not.toBeNull()
    // The menu lives inside the panel container (the portal target), not as
    // a direct child of <body>.
    expect(container.contains(listbox)).toBe(true)
    expect(listbox!.parentElement).not.toBe(document.body)
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
