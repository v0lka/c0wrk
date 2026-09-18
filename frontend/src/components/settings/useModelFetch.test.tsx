// @vitest-environment jsdom
//
// The draft TLS pin (ADR-052) must reach ListProviderModels so Fetch Models
// can list a self-signed endpoint — but it must NOT be part of credentialKey,
// whose only job is to reset the fetched list when the identity of the
// endpoint changes. Keying it on the pin would wipe an already-fetched list
// on every keystroke in the fingerprint field.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const spies = vi.hoisted(() => ({
  listProviderModels: vi.fn<(req: Record<string, unknown>) => Promise<string[]>>(),
}))

vi.mock('@/api/mcp', () => ({ listProviderModels: spies.listProviderModels }))

import { useModelFetch } from './useModelFetch'
import type { ProviderConfig } from './useLLMConfig'

let container: HTMLDivElement
let root: Root
let result!: ReturnType<typeof useModelFetch>

const pin = 'k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g='

function makeConfig(over: Partial<ProviderConfig> = {}): ProviderConfig {
  return {
    api_key: 'key',
    base_url: 'https://llm.lan:8443/v1',
    models: ['qwen3'],
    type: 'openai',
    tls_fingerprint: '',
    ...over,
  }
}

function Harness({ config }: { config: ProviderConfig }) {
  result = useModelFetch('selfhosted', { selfhosted: config })
  return null
}

async function render(config: ProviderConfig) {
  await act(async () => {
    root.render(<Harness config={config} />)
  })
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  spies.listProviderModels.mockResolvedValue(['qwen3', 'llama'])
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('useModelFetch TLS pin handling', () => {
  it('sends the draft pin verbatim', async () => {
    await render(makeConfig({ tls_fingerprint: pin }))
    await act(async () => { await result.handleApply() })

    expect(spies.listProviderModels).toHaveBeenCalledWith({
      provider: 'selfhosted',
      api_key: 'key',
      base_url: 'https://llm.lan:8443/v1',
      type: 'openai',
      tls_fingerprint: pin,
    })
  })

  // An explicit empty draft must win over the persisted pin on the backend,
  // which treats an omitted field as "keep the persisted value".
  it('sends an explicit empty pin rather than omitting the field', async () => {
    await render(makeConfig({ tls_fingerprint: '' }))
    await act(async () => { await result.handleApply() })

    const req = spies.listProviderModels.mock.calls[0]![0]
    expect(req).toHaveProperty('tls_fingerprint', '')
  })

  it('keeps a fetched model list when only the pin changes', async () => {
    await render(makeConfig({ tls_fingerprint: '' }))
    await act(async () => { await result.handleApply() })
    await flush()
    expect(result.models).toEqual(['qwen3', 'llama'])
    expect(result.apiKeyDirty).toBe(false)

    // Typing into the fingerprint field must not reset the list.
    await render(makeConfig({ tls_fingerprint: 'k' }))
    await flush()
    expect(result.models).toEqual(['qwen3', 'llama'])
    expect(result.apiKeyDirty).toBe(false)

    await render(makeConfig({ tls_fingerprint: pin }))
    await flush()
    expect(result.models).toEqual(['qwen3', 'llama'])
  })

  // The endpoint identity still resets the list: that is what credentialKey
  // is for.
  it('resets the fetched list when the base URL or key changes', async () => {
    await render(makeConfig())
    await act(async () => { await result.handleApply() })
    await flush()
    expect(result.models).toEqual(['qwen3', 'llama'])

    await render(makeConfig({ base_url: 'https://other.lan:8443/v1' }))
    await flush()
    expect(result.models).toEqual([])
    expect(result.apiKeyDirty).toBe(true)
  })

  // A fetch that failed for lack of a pin must stay retryable: apiKeyDirty is
  // only cleared on success, so the Fetch Models button remains available.
  it('stays retryable after a failure so adding a pin can be applied', async () => {
    spies.listProviderModels.mockRejectedValueOnce(new Error('x509: certificate signed by unknown authority'))
    await render(makeConfig({ tls_fingerprint: '' }))
    await act(async () => { await result.handleApply() })
    await flush()

    expect(result.modelsError).toMatch(/unknown authority/)
    expect(result.apiKeyDirty).toBe(true)

    // Add the pin and retry — no remount, no list reset needed.
    spies.listProviderModels.mockResolvedValueOnce(['qwen3'])
    await render(makeConfig({ tls_fingerprint: pin }))
    await act(async () => { await result.handleApply() })
    await flush()

    expect(result.models).toEqual(['qwen3'])
    expect(result.modelsError).toBeNull()
  })
})
