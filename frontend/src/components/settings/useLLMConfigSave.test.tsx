// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
const spies = vi.hoisted(() => ({
  updateLLMConfig: vi.fn<(req: Record<string, unknown>) => Promise<void>>(),
}))

vi.mock('@/api/config', () => ({
  updateLLMConfig: spies.updateLLMConfig,
}))

vi.mock('@/hooks/useConfigData', () => ({
  invalidateConfigCache: vi.fn(),
}))

import { useLLMConfigSave } from './useLLMConfigSave'
import type { ProviderConfig } from './useLLMConfig'

/**
 * TLS-pin payload tests (ADR-052): the save hook must carry
 * tls_fingerprint for compatible providers so the backend persists the
 * override, and never for fixed providers. An explicit empty string clears
 * the pin (back to system CA verification).
 */

let container: HTMLDivElement
let root: Root
let save: ((d: string, c: Record<string, ProviderConfig>) => void) | null = null

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
  save = null
})

function captureHook() {
  function Comp() {
    const h = useLLMConfigSave()
    save = h.saveFullConfig
    return null
  }
  act(() => {
    root.render(<Comp />)
  })
}

function makeConfig(over: Partial<ProviderConfig>): ProviderConfig {
  return {
    api_key: 'k',
    base_url: 'https://llm.lan:8443/v1',
    models: ['qwen3'],
    type: 'openai',
    tls_fingerprint: '',
    ...over,
  }
}

describe('useLLMConfigSave TLS fields', () => {
  it('includes tls_fingerprint for compatible providers', async () => {
    spies.updateLLMConfig.mockResolvedValue(undefined)
    captureHook()
    act(() => {
      save?.('selfhosted/qwen3', {
        selfhosted: makeConfig({ tls_fingerprint: 'PIN123' }),
      })
    })

    await vi.waitFor(() => expect(spies.updateLLMConfig).toHaveBeenCalledTimes(1))
    const req = spies.updateLLMConfig.mock.calls[0]![0] as {
      openai_compatible?: Record<string, Record<string, unknown>>
    }
    expect(req.openai_compatible?.selfhosted).toMatchObject({
      tls_fingerprint: 'PIN123',
    })
  })

  it('routes the pin into anthropic_compatible for anthropic transport', async () => {
    spies.updateLLMConfig.mockResolvedValue(undefined)
    captureHook()
    act(() => {
      save?.('selfclaude/claude-sonnet-4-20250514', {
        selfclaude: makeConfig({ type: 'anthropic', tls_fingerprint: 'PIN9' }),
      })
    })

    await vi.waitFor(() => expect(spies.updateLLMConfig).toHaveBeenCalledTimes(1))
    const req = spies.updateLLMConfig.mock.calls[0]![0] as {
      anthropic_compatible?: Record<string, Record<string, unknown>>
    }
    expect(req.anthropic_compatible?.selfclaude).toMatchObject({
      tls_fingerprint: 'PIN9',
    })
  })

  it('sends an explicit empty pin verbatim (clears the override on save)', async () => {
    spies.updateLLMConfig.mockResolvedValue(undefined)
    captureHook()
    act(() => {
      save?.('selfhosted/qwen3', {
        selfhosted: makeConfig({ tls_fingerprint: '' }),
      })
    })

    await vi.waitFor(() => expect(spies.updateLLMConfig).toHaveBeenCalledTimes(1))
    const req = spies.updateLLMConfig.mock.calls[0]![0] as {
      openai_compatible?: Record<string, Record<string, unknown>>
    }
    // '' must travel as an explicit value (not undefined) so the backend
    // applies it verbatim instead of keeping the persisted pin.
    expect(req.openai_compatible?.selfhosted).toMatchObject({
      tls_fingerprint: '',
    })
  })

  it('omits TLS fields for fixed providers', async () => {
    spies.updateLLMConfig.mockResolvedValue(undefined)
    captureHook()
    act(() => {
      save?.('claude-3-opus', {
        anthropic: { api_key: 'k', base_url: '', models: ['claude-3-opus'], tls_fingerprint: '' },
      })
    })

    await vi.waitFor(() => expect(spies.updateLLMConfig).toHaveBeenCalledTimes(1))
    const req = spies.updateLLMConfig.mock.calls[0]![0] as Record<string, unknown> & {
      anthropic?: Record<string, unknown>
    }
    expect(req.anthropic).toBeDefined()
    expect(req.anthropic).not.toHaveProperty('tls_fingerprint')
  })
})
