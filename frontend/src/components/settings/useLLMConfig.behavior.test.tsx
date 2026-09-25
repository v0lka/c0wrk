// @vitest-environment jsdom
import { StrictMode } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateLLMConfig: vi.fn(),
  invalidateConfigCache: vi.fn(),
  loggerError: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  getConfig: mocks.getConfig,
  updateLLMConfig: mocks.updateLLMConfig,
  MASKED_API_KEY: '***configured***',
}))
vi.mock('@/hooks/useConfigData', () => ({ invalidateConfigCache: mocks.invalidateConfigCache }))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: mocks.loggerError },
}))

import { useLLMConfig, type ProviderConfig } from './useLLMConfig'
import { useProxyDraftStore } from '@/stores/proxyDraftStore'

let container: HTMLDivElement
let root: Root
let result!: ReturnType<typeof useLLMConfig>

function HookHarness() {
  result = useLLMConfig()
  return null
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  mocks.updateLLMConfig.mockResolvedValue(undefined)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
})

describe('useLLMConfig default replacement', () => {
  it('holds provider edits locally until a replacement default produces one complete valid save', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/old-model',
        anthropic: { api_key: '', models: ['old-model'] },
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(result.defaultModel).toBe('anthropic/old-model')
    act(() => result.toggleModel('anthropic', 'old-model'))
    expect(result.defaultModel).toBe('')
    expect(result.providerConfigs.anthropic?.models).toEqual([])

    // Enabling a model while default is empty auto-claims it as the default
    // and persists in one shot (first-run / recovery after clearing default).
    act(() => result.toggleModel('anthropic', 'new-model'))
    expect(result.providerConfigs.anthropic?.models).toEqual(['new-model'])
    expect(result.defaultModel).toBe('anthropic/new-model')
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'anthropic/new-model',
      anthropic: { api_key: '', models: ['new-model'] },
      openai_compatible: {},
      anthropic_compatible: {},
    })
  })

  it('auto-sets default_model when enabling the first model with no default', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: '',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()
    expect(result.defaultModel).toBe('')

    act(() => result.addProvider('custom', {
      api_key: 'sk-test',
      base_url: 'https://api.example.com/v1',
      models: [],
      type: 'openai',
      tls_fingerprint: '',
    }))
    act(() => result.toggleModel('custom', 'org/model-a'))

    expect(result.defaultModel).toBe('custom/org/model-a')
    expect(result.providerConfigs.custom?.models).toEqual(['org/model-a'])
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'custom/org/model-a',
      // The fixed provider carries no tls_fingerprint — only compatible
      // providers store a pin (ADR-054).
      anthropic: { api_key: '', models: [] },
      openai_compatible: {
        custom: {
          api_key: 'sk-test',
          base_url: 'https://api.example.com/v1',
          models: ['org/model-a'],
          tls_fingerprint: '',
        },
      },
      anthropic_compatible: {},
    })
  })
})

describe('useLLMConfig compatible provider deletion', () => {
  it('sends an explicit empty compatible map when the last provider is deleted', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/default-model',
        anthropic: { api_key: '', models: ['default-model'] },
        openai_compatible: {
          custom: { api_key: '', base_url: 'http://localhost:1234', models: ['custom-model'], tls_fingerprint: '' },
        },
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.deleteProvider('custom'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'anthropic/default-model',
      anthropic: { api_key: '', models: ['default-model'] },
      openai_compatible: {},
      anthropic_compatible: {},
    })
  })
})

describe('useLLMConfig loading errors', () => {
  it('retains custom providers from a successful load when a repeated load errors', async () => {
    let rejectSecondLoad!: (reason?: unknown) => void
    mocks.getConfig
      .mockResolvedValueOnce({
        loaded: true,
        llm: {
          auto_retry_max_seconds: 3600,
          default_model: 'custom/model-a',
          openai_compatible: {
            custom: { api_key: '', base_url: 'http://localhost:1234', models: ['model-a'], tls_fingerprint: '' },
          },
        },
      })
      .mockImplementationOnce(() => new Promise((_, reject) => { rejectSecondLoad = reject }))

    act(() => root.render(<StrictMode><HookHarness /></StrictMode>))
    await flush()
    expect(result.openaiCompatibleProviderNames.has('custom')).toBe(true)

    await act(async () => { rejectSecondLoad(new Error('temporary config error')) })
    await flush()

    const custom: ProviderConfig | undefined = result.providerConfigs.custom
    expect(custom).toEqual({
      api_key: '',
      base_url: 'http://localhost:1234',
      models: ['model-a'],
      type: 'openai',
      tls_fingerprint: '',
    })
    expect(result.openaiCompatibleProviderNames.has('custom')).toBe(true)
  })
})

describe('useLLMConfig structural mutations persist immediately (no unmount drop)', () => {
  // Closing the settings dialog (or switching its tab) unmounts LLMSettings,
  // which CANCELS the 300 ms debounced save. Structural mutations (add /
  // delete provider) must therefore persist immediately — the debounce is
  // reserved for field edits, where a lost trailing edit is recoverable from
  // the still-open form.
  function renderLocalHarness(): { localRoot: Root; localContainer: HTMLDivElement } {
    const localContainer = document.createElement('div')
    document.body.appendChild(localContainer)
    const localRoot = createRoot(localContainer)
    act(() => localRoot.render(<HookHarness />))
    return { localRoot, localContainer }
  }

  it('addProvider saves the full config immediately — unmounting before the debounce cannot drop it', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/default-model',
        anthropic: { api_key: '', models: ['default-model'] },
      },
    })
    const { localRoot, localContainer } = renderLocalHarness()
    await flush()

    act(() => {
      result.addProvider('custom', {
        api_key: 'k',
        base_url: 'http://localhost:1234',
        models: [],
        type: 'openai',
        tls_fingerprint: '',
      })
    })

    // Close the dialog BEFORE the old 300 ms debounce window elapses.
    act(() => localRoot.unmount())
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'anthropic/default-model',
      anthropic: { api_key: '', models: ['default-model'] },
      openai_compatible: { custom: { api_key: 'k', base_url: 'http://localhost:1234', models: [], tls_fingerprint: '' } },
      anthropic_compatible: {},
    })
    localContainer.remove()
  })

  it('deleteProvider saves immediately as well — unmounting before the debounce cannot drop it', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/default-model',
        anthropic: { api_key: '', models: ['default-model'] },
        openai_compatible: {
          custom: { api_key: '', base_url: 'http://localhost:1234', models: ['custom-model'], tls_fingerprint: '' },
        },
      },
    })
    const { localRoot, localContainer } = renderLocalHarness()
    await flush()

    act(() => result.deleteProvider('custom'))

    act(() => localRoot.unmount())
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'anthropic/default-model',
      anthropic: { api_key: '', models: ['default-model'] },
      openai_compatible: {},
      anthropic_compatible: {},
    })
    localContainer.remove()
  })

  it('cancels a pending debounced save so its stale snapshot cannot revert a delete', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/default-model',
        anthropic: { api_key: '', models: ['default-model'] },
        openai_compatible: {
          custom: { api_key: '', base_url: 'http://localhost:1234', models: ['custom-model'], tls_fingerprint: '' },
        },
      },
    })
    const { localRoot, localContainer } = renderLocalHarness()
    await flush()

    // A field edit queues the 300 ms debounced save (its captured snapshot
    // still contains the custom provider).
    act(() => result.toggleModel('custom', 'model-a'))
    // Delete the provider within the debounce window: the immediate save must
    // supersede the queued timer, or the orphaned timer's PRE-mutation
    // snapshot would land afterwards and resurrect the deleted provider.
    act(() => result.deleteProvider('custom'))

    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'anthropic/default-model',
      anthropic: { api_key: '', models: ['default-model'] },
      openai_compatible: {},
      anthropic_compatible: {},
    })
    act(() => localRoot.unmount())
    localContainer.remove()
  })

  it('addProvider with an invalid default still holds the change locally (no RPC)', async () => {
    // The default does not resolve to an enabled model, so the save is gated
    // on the timing AND the validity: nothing is sent until the user picks a
    // valid replacement default.
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: '',
        anthropic: { api_key: '', models: ['default-model'] },
      },
    })
    const { localRoot, localContainer } = renderLocalHarness()
    await flush()

    act(() => {
      result.addProvider('custom', {
        api_key: 'k',
        base_url: 'http://localhost:1234',
        models: [],
        type: 'openai',
        tls_fingerprint: '',
      })
    })

    act(() => localRoot.unmount())
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).not.toHaveBeenCalled()
    localContainer.remove()
  })
})

// --- Per-provider TLS pin (ADR-054) ---

describe('useLLMConfig TLS pin round-trip', () => {
  const pin = 'k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g='

  beforeEach(() => {
    useProxyDraftStore.setState({ active: null })
  })

  it('loads the persisted pin and defaults an absent one to an empty string', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'pinned/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          pinned: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], tls_fingerprint: pin },
          plain: { api_key: 'k', base_url: 'http://127.0.0.1:1234/v1', models: ['llama'] },
        },
        anthropic_compatible: {
          gateway: { api_key: 'k', base_url: 'https://claude.lan:8443', models: ['claude'], tls_fingerprint: pin },
        },
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(result.providerConfigs.pinned?.tls_fingerprint).toBe(pin)
    expect(result.providerConfigs.gateway?.tls_fingerprint).toBe(pin)
    expect(result.providerConfigs.plain?.tls_fingerprint).toBe('')
    // Fixed providers have no pin key; the shape still carries the field.
    expect(result.providerConfigs.anthropic?.tls_fingerprint).toBe('')
  })

  it('sends the pin only for compatible providers on save', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'pinned/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          pinned: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], tls_fingerprint: pin },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('pinned', { tls_fingerprint: 'newpin' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<string, { tls_fingerprint?: string }>
    expect(compatible.pinned?.tls_fingerprint).toBe('newpin')
    // The fixed provider entry must not grow a pin field.
    const fixed = req.anthropic as unknown as Record<string, unknown>
    expect(fixed).not.toHaveProperty('tls_fingerprint')
  })

  // Clearing the field is how the override is switched off: an explicit ''
  // reaches the backend, which treats a present value as authoritative.
  it('sends an explicit empty string when the pin is cleared', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'pinned/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          pinned: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], tls_fingerprint: pin },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('pinned', { tls_fingerprint: '' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<string, { tls_fingerprint?: string }>
    expect(compatible.pinned?.tls_fingerprint).toBe('')
  })
})

// --- Auto-retry interval (compatible providers only) ---

describe('useLLMConfig auto-retry interval', () => {
  beforeEach(() => {
    useProxyDraftStore.setState({ active: null })
  })

  it('loads the persisted interval and defaults an absent one to undefined', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'timed/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          timed: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], auto_retry_seconds: 30 },
          plain: { api_key: 'k', base_url: 'http://127.0.0.1:1234/v1', models: ['llama'] },
        },
        anthropic_compatible: {
          gateway: { api_key: 'k', base_url: 'https://claude.lan:8443', models: ['claude'], auto_retry_seconds: 45 },
        },
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(result.providerConfigs.timed?.auto_retry_seconds).toBe(30)
    expect(result.providerConfigs.gateway?.auto_retry_seconds).toBe(45)
    expect(result.providerConfigs.plain?.auto_retry_seconds).toBeUndefined()
    // Fixed providers never carry the field (backend reports 0 → omitted).
    expect(result.providerConfigs.anthropic?.auto_retry_seconds).toBeUndefined()
  })

  it('sends the interval only for compatible providers on save', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'timed/qwen3',
        anthropic: { api_key: 'k', models: ['claude'] },
        openai_compatible: {
          timed: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], auto_retry_seconds: 30 },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('timed', { base_url: 'https://new.lan/v1' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledTimes(1)
    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<
      string,
      { auto_retry_seconds?: number }
    >
    expect(compatible.timed?.auto_retry_seconds).toBe(30)
    // The fixed provider entry must not grow an auto_retry_seconds field.
    const fixed = req.anthropic as unknown as Record<string, unknown>
    expect(fixed).not.toHaveProperty('auto_retry_seconds')
  })

  // The debounce must not silently disable a persisted interval: a save
  // triggered by an unrelated field edit carries the loaded interval along.
  it('keeps a loaded interval through a debounced unrelated edit', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'timed/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          timed: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], auto_retry_seconds: 30 },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('timed', { api_key: 'k2' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<
      string,
      { auto_retry_seconds?: number }
    >
    expect(compatible.timed?.auto_retry_seconds).toBe(30)
  })

  it('sends an explicit 0 when the interval is disabled via the form', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'timed/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          timed: { api_key: 'k', base_url: 'https://llm.lan:8443/v1', models: ['qwen3'], auto_retry_seconds: 30 },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('timed', { auto_retry_seconds: 0 }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<
      string,
      { auto_retry_seconds?: number }
    >
    // An EXPLICIT 0 — never undefined, which the backend would read as
    // "keep the persisted 30".
    expect(compatible.timed?.auto_retry_seconds).toBe(0)
    expect(compatible.timed).toHaveProperty('auto_retry_seconds', 0)
  })

  it('omits the field for a compatible provider that never had an interval', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'plain/qwen3',
        anthropic: { api_key: '', models: [] },
        openai_compatible: {
          plain: { api_key: 'k', base_url: 'http://127.0.0.1:1234/v1', models: ['qwen3'] },
        },
        anthropic_compatible: {},
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    act(() => result.updateProviderConfig('plain', { api_key: 'k2' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    const req = mocks.updateLLMConfig.mock.calls[0]![0] as Record<string, never>
    const compatible = req.openai_compatible as unknown as Record<
      string,
      { auto_retry_seconds?: number }
    >
    expect(compatible.plain).not.toHaveProperty('auto_retry_seconds')
  })
})

describe('useLLMConfig proxy gate', () => {
  beforeEach(() => {
    useProxyDraftStore.setState({ active: null })
  })

  function mockConfigWithProxy(proxy: unknown) {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      proxy,
      llm: {
        auto_retry_max_seconds: 3600,
        default_model: 'anthropic/claude',
        anthropic: { api_key: 'k', models: ['claude'] },
        openai_compatible: {},
        anthropic_compatible: {},
      },
    })
  }

  it.each([
    { name: 'enabled with a URL', proxy: { enabled: true, url: 'http://proxy.lan:3128' }, want: true },
    { name: 'enabled without a URL', proxy: { enabled: true, url: '' }, want: false },
    { name: 'disabled', proxy: { enabled: false, url: 'http://proxy.lan:3128' }, want: false },
    { name: 'absent', proxy: undefined, want: false },
  ])('seeds the proxy gate from the config payload: $name', async ({ proxy, want }) => {
    mockConfigWithProxy(proxy)

    act(() => root.render(<HookHarness />))
    await flush()

    expect(useProxyDraftStore.getState().active).toBe(want)
  })

  // The General tab writes the store synchronously on every edit, so its
  // draft is fresher than anything getConfig can report. The LLM tab's seed
  // must not overwrite it.
  it('does not overwrite a draft state already published by the General tab', async () => {
    useProxyDraftStore.getState().setActive(true)
    mockConfigWithProxy({ enabled: false, url: '' })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(useProxyDraftStore.getState().active).toBe(true)
  })

  // A proxy toggled in the General tab must reach an already-mounted LLM tab
  // without any config re-read.
  it('reacts to a later store change without re-reading the config', async () => {
    mockConfigWithProxy({ enabled: false, url: '' })

    act(() => root.render(<HookHarness />))
    await flush()
    expect(useProxyDraftStore.getState().active).toBe(false)
    const readsAfterLoad = mocks.getConfig.mock.calls.length

    act(() => useProxyDraftStore.getState().setActive(true))
    expect(useProxyDraftStore.getState().active).toBe(true)
    expect(mocks.getConfig.mock.calls.length).toBe(readsAfterLoad)
  })
})

// --- Server-published auto-retry bound contract (ADR-065) ---

describe('useLLMConfig auto_retry_max_seconds contract', () => {
  // The bound is REQUIRED (no compiled-in fallback): frontend and backend
  // ship in one binary, so a payload without a positive value means the
  // contract is broken — the load must fail LOUDLY instead of silently
  // clamping the interval input against a stale constant.
  it('fails the load loudly when auto_retry_max_seconds is missing or non-positive', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        default_model: 'anthropic/old-model',
        anthropic: { api_key: '', models: ['old-model'] },
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(mocks.loggerError).toHaveBeenCalledWith(
      'Failed to load LLM config:',
      expect.objectContaining({ message: expect.stringContaining('auto_retry_max_seconds') }),
    )
    // The whole load is rejected: no provider state leaks in from the broken
    // payload, and the bound stays unknown (LLMSettings keeps the forms
    // gated on exactly this).
    expect(result.defaultModel).toBe('')
    expect(Object.keys(result.providerConfigs)).toEqual([])
    expect(result.autoRetryMaxSeconds).toBeUndefined()
  })

  it('publishes the server value verbatim when present', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        auto_retry_max_seconds: 600,
        default_model: '',
      },
    })

    act(() => root.render(<HookHarness />))
    await flush()

    expect(result.autoRetryMaxSeconds).toBe(600)
    expect(mocks.loggerError).not.toHaveBeenCalled()
  })
})
