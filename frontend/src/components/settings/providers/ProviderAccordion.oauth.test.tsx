// @vitest-environment jsdom
// OAuth-mode (ChatGPT subscription) rendering of the provider accordion: the
// model checklist comes from the preset/live subscription list instead of a
// provider API fetch, the Fetch models button drives the live catalog fetch
// (enabled with controls wired, disabled without them), and the
// no-disappear invariant keeps enabled-but-not-in-preset rows listed.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const spies = vi.hoisted(() => ({
  listProviderModels: vi.fn(),
}))

vi.mock('@/api/mcp', () => ({
  listProviderModels: spies.listProviderModels,
}))

vi.mock('../ModelConfigDialog', () => ({
  ModelConfigDialog: () => null,
}))

vi.mock('@/hooks/useConfigData', () => ({
  invalidateConfigCache: vi.fn(),
}))

import { ProviderAccordion } from './ProviderAccordion'
import { formatContextWindow } from '@/lib/chatgptFormat'
import type { ProviderConfig } from './ProviderAccordion'

let container: HTMLDivElement
let root: Root
let toggledModels: string[]

const PRESET = [
  { name: 'gpt-5.5', context_window: 272000, output_limit: 128000, reasoning: true },
  { name: 'gpt-5.4-mini', context_window: 272000, output_limit: 128000 },
  { name: 'codex-mini-latest' },
]

beforeEach(() => {
  vi.clearAllMocks()
  toggledModels = []
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

function renderOAuthAccordion(
  models: string[],
  preset: typeof PRESET = PRESET,
  chatGPTFetch?: { loading: boolean; error: string | null; onRefresh: () => void },
  presetSource?: 'preset' | 'subscription',
): void {
  const config: ProviderConfig = {
    api_key: '',
    base_url: '',
    models,
    tls_fingerprint: '',
    auth_mode: 'oauth',
  }
  act(() => {
    root.render(
      <ProviderAccordion
        provider="chatgpt"
        label="ChatGPT"
        config={config}
        isExpanded={true}
        onToggle={() => {}}
        onConfigChange={() => {}}
        onToggleModel={(m) => toggledModels.push(m)}
        defaultModel=""
        providerConfigs={{ chatgpt: config }}
        autoRetryMaxSeconds={3600}
        isOAuth={true}
        presetModels={preset}
        presetSource={presetSource}
        chatGPTFetch={chatGPTFetch}
      />,
    )
  })
}

function modelRows(): Array<{ name: string; checked: boolean; badges: string[] }> {
  return Array.from(container.querySelectorAll('input[type="checkbox"]')).map((input) => {
    const label = input.closest('label')
    const name = label?.querySelector('span.flex-1')?.textContent ?? ''
    const badges = Array.from(label?.querySelectorAll('span[class*="text-[10px]"]') ?? []).map(
      (b) => b.textContent ?? '',
    )
    return { name, checked: (input as HTMLInputElement).checked, badges }
  })
}

function fetchModelsButton(): HTMLButtonElement {
  // While a fetch is in flight the label is replaced by a spinner, so
  // match the title too (it is always rendered).
  const btn = Array.from(container.querySelectorAll('button')).find(
    (b) => b.textContent?.includes('Fetch models') || b.title.includes('Fetch the models'),
  )
  if (!btn) throw new Error('Fetch models button not found')
  return btn as HTMLButtonElement
}

describe('formatContextWindow', () => {
  it('formats token counts compactly', () => {
    expect(formatContextWindow(272000)).toBe('272K')
    expect(formatContextWindow(1000000)).toBe('1M')
    expect(formatContextWindow(400000)).toBe('400K')
  })

  it('renders nothing for zero metadata', () => {
    expect(formatContextWindow(0)).toBe('')
  })
})

describe('ProviderAccordion oauth checklist', () => {
  it('lists the preset in its own order with registry badges', () => {
    renderOAuthAccordion(['gpt-5.4-mini'])

    const rows = modelRows()
    // Preset order (most capable first) is preserved, NOT alphabetized.
    expect(rows.map((r) => r.name)).toEqual(['gpt-5.5', 'gpt-5.4-mini', 'codex-mini-latest'])
    expect(rows.find((r) => r.name === 'gpt-5.4-mini')?.checked).toBe(true)
    expect(rows.find((r) => r.name === 'gpt-5.5')?.checked).toBe(false)

    // Registry metadata: reasoning flag + compact context window; zero
    // metadata (codex-mini-latest) renders no badges.
    expect(rows.find((r) => r.name === 'gpt-5.5')?.badges).toEqual(['reasoning', '272K'])
    expect(rows.find((r) => r.name === 'gpt-5.4-mini')?.badges).toEqual(['272K'])
    expect(rows.find((r) => r.name === 'codex-mini-latest')?.badges).toEqual([])
  })

  it('keeps enabled models that are not in the preset listed (no-disappear invariant)', () => {
    renderOAuthAccordion(['gpt-5.5', 'legacy-model'])

    const rows = modelRows()
    expect(rows.map((r) => r.name)).toEqual(['gpt-5.5', 'gpt-5.4-mini', 'codex-mini-latest', 'legacy-model'])
    expect(rows.find((r) => r.name === 'legacy-model')?.checked).toBe(true)
    // No fetch ever ran, so the staleness badge never appears in oauth mode.
    expect(rows.find((r) => r.name === 'legacy-model')?.badges).toEqual([])
  })

  it('never dials the provider listing API in oauth mode', async () => {
    renderOAuthAccordion([])
    await act(async () => {
      root.render(null) // Complete mount effects and their cleanup before absence assertion.
    })
    expect(spies.listProviderModels).not.toHaveBeenCalled()
  })

  it('drives the live catalog fetch from the Fetch models button', () => {
    const onRefresh = vi.fn()
    renderOAuthAccordion(
      [],
      PRESET,
      { loading: false, error: null, onRefresh },
      'subscription',
    )
    const btn = fetchModelsButton()
    expect(btn.disabled).toBe(false)
    expect(btn.title).toContain('your ChatGPT subscription')
    expect(container.textContent).toContain('Live from your ChatGPT subscription')
    expect(container.textContent).toContain('the API key stays stored but unused')

    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('shows the fetch error and spinner state from the controls', () => {
    renderOAuthAccordion(
      [],
      PRESET,
      { loading: true, error: 'fetching the ChatGPT model list: boom', onRefresh: () => {} },
    )
    const btn = fetchModelsButton()
    expect(btn.disabled).toBe(true)
    expect(container.textContent).toContain('fetching the ChatGPT model list: boom')

    // With an empty list the checklist itself says a fetch is running.
    renderOAuthAccordion(
      [],
      [],
      { loading: true, error: null, onRefresh: () => {} },
    )
    expect(container.textContent).toContain('Fetching subscription models…')
  })

  it('disables the Fetch models button when no controls are wired', () => {
    renderOAuthAccordion([])
    const btn = fetchModelsButton()
    expect(btn.disabled).toBe(true)
    expect(container.textContent).toContain('ChatGPT fallback preset (offline)')
  })

  it('toggles preset models through the ordinary onToggleModel path', () => {
    renderOAuthAccordion([])
    const checkbox = container.querySelectorAll('input[type="checkbox"]')[0]!
    act(() => {
      checkbox.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    // The first row is gpt-5.5 in preset order.
    expect(toggledModels).toEqual(['gpt-5.5'])
  })

  it('shows the sign-in hint when the list is empty and nothing is enabled', () => {
    renderOAuthAccordion([], [])
    expect(container.textContent).toContain(
      'No ChatGPT models loaded yet — sign in above and click "Fetch models".',
    )
  })
})
