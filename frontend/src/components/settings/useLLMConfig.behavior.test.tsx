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
      type: 'openai', tls_fingerprint: '' }))
    act(() => result.toggleModel('custom', 'org/model-a'))

    expect(result.defaultModel).toBe('custom/org/model-a')
    expect(result.providerConfigs.custom?.models).toEqual(['org/model-a'])
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      default_model: 'custom/org/model-a',
      anthropic: { api_key: '', models: [] },
      openai_compatible: {
        custom: {
          api_key: 'sk-test',
          base_url: 'https://api.example.com/v1',
          models: ['org/model-a'], tls_fingerprint: '' },
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
      type: 'openai', tls_fingerprint: '' })
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
        type: 'openai', tls_fingerprint: '' })
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
        type: 'openai', tls_fingerprint: '' })
    })

    act(() => localRoot.unmount())
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    expect(mocks.updateLLMConfig).not.toHaveBeenCalled()
    localContainer.remove()
  })
})
