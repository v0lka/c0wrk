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
})
