// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ModelMetadataInfo } from '@/types/models'
import type { ProviderAccordionProps } from './ProviderAccordion'

// The hook is mocked at the module boundary: this suite pins the ACCORDION's
// rendering — the detector-chain metadata badges (ListProviderModels) and the
// api-key displayModels union (enabled models stay listed, endpoint
// duplicates collapse) — not the fetch itself (covered in
// useModelFetch.test.tsx).
const fetchResult = vi.hoisted(() => ({
  models: ['deepseek-v4'],
  modelsMetadata: undefined as Record<string, ModelMetadataInfo> | undefined,
  modelsLoading: false,
  modelsError: null as string | null,
  apiKeyDirty: false,
  handleApply: vi.fn(async () => {}),
  hasRequiredCredentials: true,
}))

vi.mock('../useModelFetch', () => ({
  useModelFetch: () => fetchResult,
}))

// The Configure dialog fetches the effective config over the real API when
// opened; mock that boundary (keeping the rest of the module intact) so the
// prefill wiring test stays deterministic.
vi.mock('@/api/config', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/config')>()),
  getModelConfig: vi.fn(async () => ({
    model: 'deepseek-v4',
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
  })),
  setModelConfig: vi.fn(async () => {}),
  // The dialog's "The same as…" effect also loads the registry names on open;
  // stub it (empty list) so the real wrapper never hits the missing Wails
  // bindings and no failure-path diagnostics leak into the run.
  getBuiltinModelNames: vi.fn(async () => []),
}))

import { ProviderAccordion } from './ProviderAccordion'

let container: HTMLDivElement
let root: Root

const baseProps: ProviderAccordionProps = {
  provider: 'PT',
  label: 'PT',
  config: { api_key: 'k', base_url: 'https://api-llm.example.com/v1', models: [], tls_fingerprint: '' },
  isExpanded: true,
  onToggle: () => {},
  onConfigChange: () => {},
  onToggleModel: () => {},
  defaultModel: '',
  providerConfigs: {},
  autoRetryMaxSeconds: 60,
}

function renderAccordion() {
  act(() => {
    root.render(<ProviderAccordion {...baseProps} />)
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  fetchResult.models = ['deepseek-v4']
  fetchResult.modelsMetadata = undefined
  fetchResult.modelsError = null
  fetchResult.apiKeyDirty = false
  baseProps.config.models = []
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

/** All model rows as {name, checked}, in render order. */
function modelRows(): Array<{ name: string; checked: boolean }> {
  return Array.from(container.querySelectorAll('input[type="checkbox"]')).map((input) => {
    const label = input.closest('label')
    const span = label?.querySelector('span.flex-1')
    return {
      name: span?.textContent ?? '',
      checked: (input as HTMLInputElement).checked,
    }
  })
}

/** Badge texts rendered next to each model name, e.g. the "not reported by
 *  endpoint" staleness warning. */
function rowBadges(): Array<{ name: string; badges: string[] }> {
  return Array.from(container.querySelectorAll('input[type="checkbox"]')).map((input) => {
    const label = input.closest('label')
    const span = label?.querySelector('span.flex-1')
    const badges = Array.from(label?.querySelectorAll('span[class*="text-xs"]') ?? []).map(
      (b) => b.textContent ?? '',
    )
    return { name: span?.textContent ?? '', badges }
  })
}

describe('ProviderAccordion displayModels union (api-key)', () => {
  it('shows enabled models before any fetch', () => {
    // The hook's pre-fetch state: nothing listed yet, the key not yet
    // confirmed by a successful fetch.
    fetchResult.models = []
    fetchResult.apiKeyDirty = true
    baseProps.config.models = ['old-model']
    renderAccordion()

    const rows = modelRows()
    expect(rows).toHaveLength(1)
    expect(rows[0]).toEqual({ name: 'old-model', checked: true })
    // Before any fetch the endpoint's list is unknown — no staleness badge.
    expect(rowBadges()[0]?.badges).toEqual([])
  })

  it('deduplicates the fetched list and keeps enabled models visible', () => {
    // The endpoint reports new-a twice (duplicate collapse) while the config
    // enables old-model; the visible list is the sorted union of both.
    fetchResult.models = ['new-a', 'new-b', 'new-a']
    baseProps.config.models = ['old-model']
    renderAccordion()

    const rows = modelRows()
    expect(rows.map((r) => r.name)).toEqual(['new-a', 'new-b', 'old-model'])
    // Only the configured model is checked; fetched entries start unchecked.
    expect(rows.find((r) => r.name === 'old-model')?.checked).toBe(true)
    expect(rows.find((r) => r.name === 'new-a')?.checked).toBe(false)
    expect(rows.find((r) => r.name === 'new-b')?.checked).toBe(false)
  })

  it('keeps an enabled model listed even when a later fetch does not report it', () => {
    baseProps.config.models = ['old-model']
    // A completed fetch that still reports the model.
    fetchResult.models = ['old-model', 'new-a']
    fetchResult.apiKeyDirty = false
    renderAccordion()
    expect(modelRows().map((r) => r.name)).toEqual(['new-a', 'old-model'])
    // Reported by the endpoint → no staleness badge.
    expect(rowBadges().find((r) => r.name === 'old-model')?.badges).toEqual([])

    // A later fetch drops the enabled model: re-render with the hook's new
    // state (the same situation as re-fetching in the reopened settings
    // dialog).
    fetchResult.models = ['new-a']
    renderAccordion()

    const rows = modelRows()
    expect(rows.map((r) => r.name)).toEqual(['new-a', 'old-model'])
    expect(rows.find((r) => r.name === 'old-model')?.checked).toBe(true)
    // The enabled model stays listed but is marked as no longer reported by
    // the endpoint.
    expect(rowBadges().find((r) => r.name === 'old-model')?.badges).toEqual([
      'not reported by endpoint',
    ])
    expect(rowBadges().find((r) => r.name === 'new-a')?.badges).toEqual([])
  })
})

describe('ProviderAccordion endpoint-metadata badges', () => {
  it('renders the context badge from the fetched detector-chain metadata', () => {
    fetchResult.modelsMetadata = {
      'deepseek-v4': {
        context_window: 512000,
        output_limit: 32000,
        capabilities: { attachment: true, reasoning: true, temperature: true, tool_call: true },
      },
    }
    renderAccordion()

    // The compact badge (Intl compact: 512000 → "512K") plus the reasoning
    // chip from the declared capabilities.
    expect(document.body.textContent).toContain('512K')
    expect(document.body.textContent).toContain('reasoning')

    const badge = Array.from(document.body.querySelectorAll('span[title]')).find((s) =>
      s.getAttribute('title')?.startsWith('Context window:'),
    )
    expect(badge?.getAttribute('title')).toBe(
      'Context window: 512,000 tokens · output limit: 32,000',
    )
  })

  it('renders no badge when the listing declared no metadata', () => {
    fetchResult.modelsMetadata = undefined
    renderAccordion()

    expect(document.body.textContent).not.toContain('512K')
    expect(
      Array.from(document.body.querySelectorAll('span[title]')).find((s) =>
        s.getAttribute('title')?.startsWith('Context window:'),
      ),
    ).toBeUndefined()
  })

  it('renders no badge for an entry the detectors could not parse', () => {
    fetchResult.modelsMetadata = {} // model listed, no detector match
    renderAccordion()

    expect(
      Array.from(document.body.querySelectorAll('span[title]')).find((s) =>
        s.getAttribute('title')?.startsWith('Context window:'),
      ),
    ).toBeUndefined()
  })
})

describe('ProviderAccordion configure-dialog metadata prefill', () => {
  /** Open the Configure dialog for the listed model and return its two
   *  number inputs (context window, output limit). */
  async function openConfigureAndReadInputs(): Promise<HTMLInputElement[]> {
    renderAccordion()
    const configureBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.getAttribute('aria-label') === 'Configure deepseek-v4',
    )
    if (!configureBtn) throw new Error('Configure button not found')
    await act(async () => { configureBtn.click() })
    // The dialog's config load is microtask-only here (the API boundary is
    // mocked with synchronous-resolve promises): advance a bounded number of
    // microtask ticks inside act so the load chain settles, then read the DOM
    // after act's flush. No real timers — the timing guard forbids adding
    // delay-promise debt.
    await act(async () => {
      for (let i = 0; i < 50; i++) await Promise.resolve()
    })
    const inputs = Array.from(
      document.body.querySelectorAll<HTMLInputElement>('input[type="number"]'),
    )
    if (inputs.length === 0) throw new Error('dialog inputs never rendered')
    return inputs
  }

  it('shows the fetched listing values in the dialog fields', async () => {
    fetchResult.modelsMetadata = {
      'deepseek-v4': {
        context_window: 512000,
        output_limit: 32000,
        capabilities: { attachment: true, reasoning: true, temperature: true, tool_call: true },
      },
    }
    const inputs = await openConfigureAndReadInputs()
    expect(inputs[0]?.value).toBe('512000')
    expect(inputs[1]?.value).toBe('32000')
  })

  it('falls back to the factory defaults when the fetch declared no metadata', async () => {
    fetchResult.modelsMetadata = undefined
    const inputs = await openConfigureAndReadInputs()
    expect(inputs[0]?.value).toBe('32768')
    expect(inputs[1]?.value).toBe('8192')
  })
})
