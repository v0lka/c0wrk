// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
const spies = vi.hoisted(() => ({
  listProviderModels: vi.fn<(provider: string) => Promise<string[]>>(),
}))

// Mock the backend API wrapper so no real Wails round-trip happens.
// Partial mock following the project convention (ModelConfigDialog.test,
// BlackboardPanel.test): only the functions this tree calls are provided.
vi.mock('@/api/mcp', () => ({
  listProviderModels: spies.listProviderModels,
}))

// The accordion mounts ModelConfigDialog permanently (open=false); stub it so
// the test exercises only the model-list rendering under test.
vi.mock('../ModelConfigDialog', () => ({
  ModelConfigDialog: () => null,
}))

// invalidateConfigCache is only called after a dialog save; stub it out to
// avoid pulling the config-data cache module into the jsdom environment.
vi.mock('@/hooks/useConfigData', () => ({
  invalidateConfigCache: vi.fn(),
}))

import { ProviderAccordion } from './ProviderAccordion'

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

function renderAccordion(models: string[], mountKey = 'mount') {
  const config = { api_key: 'key', base_url: 'http://localhost:1234', models }
  act(() => {
    root.render(
      <ProviderAccordion
        // Changing the key forces a full remount (fresh hook state), the
        // same situation as reopening the LLM settings dialog.
        key={mountKey}
        provider="local"
        label="Local"
        config={config}
        isExpanded={true}
        onToggle={() => {}}
        onConfigChange={() => {}}
        onToggleModel={() => {}}
        defaultModel=""
        providerConfigs={{ local: config }}
      />,
    )
  })
}

/** Flush pending microtasks so the fetch promise resolves and state settles. */
function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 50))
}

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

/** Badge texts rendered next to each model name, e.g. "default" or the
 *  "not reported by endpoint" staleness warning. */
function rowBadges(): Array<{ name: string; badges: string[] }> {
  return Array.from(container.querySelectorAll('input[type="checkbox"]')).map((input) => {
    const label = input.closest('label')
    const span = label?.querySelector('span.flex-1')
    const badges = Array.from(label?.querySelectorAll('span[class*="text-[10px]"]') ?? []).map(
      (b) => b.textContent ?? '',
    )
    return { name: span?.textContent ?? '', badges }
  })
}

async function clickFetchModels(): Promise<void> {
  const btn = Array.from(container.querySelectorAll('button')).find(
    (b) => b.textContent?.includes('Fetch models'),
  )
  if (!btn) throw new Error('Fetch models button not found')
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
  })
}

describe('ProviderAccordion model list', () => {
  it('shows enabled models before any fetch', () => {
    renderAccordion(['old-model'])
    const rows = modelRows()
    expect(rows).toHaveLength(1)
    expect(rows[0]).toEqual({ name: 'old-model', checked: true })
    // Before any fetch the endpoint's list is unknown — no staleness badge.
    expect(rowBadges()[0]?.badges).toEqual([])
  })

  it('deduplicates the fetched list and keeps enabled models visible', async () => {
    renderAccordion(['old-model'])
    spies.listProviderModels.mockResolvedValue(['new-a', 'new-b', 'new-a'])

    await clickFetchModels()

    const rows = modelRows()
    expect(rows.map((r) => r.name)).toEqual(['new-a', 'new-b', 'old-model'])
    // Only the configured model is checked; fetched entries start unchecked.
    expect(rows.find((r) => r.name === 'old-model')?.checked).toBe(true)
    expect(rows.find((r) => r.name === 'new-a')?.checked).toBe(false)
  })

  it('keeps an enabled model listed even when a later fetch does not report it', async () => {
    renderAccordion(['old-model'])
    // First fetch still reports the model; a second fetch drops it.
    spies.listProviderModels.mockResolvedValueOnce(['old-model', 'new-a'])
    await clickFetchModels()
    expect(modelRows().map((r) => r.name)).toEqual(['new-a', 'old-model'])
    // Reported by the endpoint → no staleness badge.
    expect(rowBadges().find((r) => r.name === 'old-model')?.badges).toEqual([])

    // The Fetch button disappears while apiKeyDirty is false, so remount the
    // accordion (as happens when the settings dialog is reopened) and fetch
    // again with the provider no longer reporting the enabled model.
    spies.listProviderModels.mockResolvedValue(['new-a'])
    renderAccordion(['old-model'], 'remount')
    await clickFetchModels()

    const rows = modelRows()
    expect(rows.map((r) => r.name)).toEqual(['new-a', 'old-model'])
    expect(rows.find((r) => r.name === 'old-model')?.checked).toBe(true)
    // Now the drift is visible: the enabled model stays listed but is marked
    // as no longer reported by the endpoint.
    expect(rowBadges().find((r) => r.name === 'old-model')?.badges).toEqual([
      'not reported by endpoint',
    ])
    expect(rowBadges().find((r) => r.name === 'new-a')?.badges).toEqual([])
  })

  it('does not badge enabled models after a failed fetch (endpoint list unknown)', async () => {
    renderAccordion(['old-model'])
    spies.listProviderModels.mockRejectedValue(new Error('network down'))

    await clickFetchModels()

    // The fetch failed, so we cannot claim the model is stale — no badge.
    expect(modelRows().map((r) => r.name)).toEqual(['old-model'])
    expect(rowBadges()[0]?.badges).toEqual([])
  })
})
