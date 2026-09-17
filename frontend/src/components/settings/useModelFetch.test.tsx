// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  listProviderModels: vi.fn(),
}))

vi.mock('@/api/mcp', () => ({
  listProviderModels: mocks.listProviderModels,
}))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { useModelFetch } from './useModelFetch'
import type { ProviderConfig } from './useLLMConfig'

let container: HTMLDivElement
let root: Root
let result!: ReturnType<typeof useModelFetch>

function HookHarness({ provider, configs }: { provider: string; configs: Record<string, ProviderConfig> }) {
  result = useModelFetch(provider, configs)
  return null
}

function draftConfig(overrides: Partial<ProviderConfig> = {}): ProviderConfig {
  return {
    api_key: 'sk-test',
    base_url: 'https://llm.lan:8443/v1',
    models: [],
    type: 'openai',
    tls_fingerprint: '',
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.listProviderModels.mockResolvedValue(['qwen3'])
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('useModelFetch draft TLS pin override (ADR-052)', () => {
  it('sends an explicit draft tls_fingerprint="" verbatim — not undefined (which would fall back to the saved pin)', async () => {
    act(() => root.render(<HookHarness provider="selfhosted" configs={{ selfhosted: draftConfig({ tls_fingerprint: '' }) }} />))
    await act(async () => { await result.handleApply() })

    expect(mocks.listProviderModels).toHaveBeenCalledTimes(1)
    expect(mocks.listProviderModels).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'selfhosted',
      tls_fingerprint: '',
    }))
  })

  it('sends the draft pin as-is when set', async () => {
    act(() => root.render(
      <HookHarness provider="selfhosted" configs={{ selfhosted: draftConfig({ tls_fingerprint: 'k3J9vQ1Zpin==' }) }} />,
    ))
    await act(async () => { await result.handleApply() })

    expect(mocks.listProviderModels).toHaveBeenCalledWith(expect.objectContaining({
      tls_fingerprint: 'k3J9vQ1Zpin==',
    }))
  })
})
