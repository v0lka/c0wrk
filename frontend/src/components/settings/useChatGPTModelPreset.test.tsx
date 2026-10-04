// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatGPTModelListState } from './useChatGPTModelPreset'

const mocks = vi.hoisted(() => ({
  getChatGPTModelPreset: vi.fn(),
  fetchChatGPTModels: vi.fn(),
  getChatGPTAuthStatus: vi.fn(),
  onChatGPTAuthState: vi.fn(
    (_callback: (data: { state: string }) => void) => () => {},
  ),
  loggerError: vi.fn(),
  loggerWarn: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  getChatGPTModelPreset: mocks.getChatGPTModelPreset,
  fetchChatGPTModels: mocks.fetchChatGPTModels,
  getChatGPTAuthStatus: mocks.getChatGPTAuthStatus,
  onChatGPTAuthState: mocks.onChatGPTAuthState,
}))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: mocks.loggerWarn, error: mocks.loggerError },
}))

import { useChatGPTModelPreset } from './useChatGPTModelPreset'

let container: HTMLDivElement
let root: Root
let current: ChatGPTModelListState

function Harness({ enabled }: { enabled: boolean }) {
  current = useChatGPTModelPreset(enabled)
  return null
}

async function flush(rounds = 4): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await Promise.resolve()
    })
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  // The default status posture: signed out (no live fetch, preset only).
  mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth' })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('useChatGPTModelPreset', () => {
  it('loads the offline preset and stays on it while signed out', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({
      models: [
        { name: 'gpt-5.5', context_window: 272000, reasoning: true },
        { name: 'codex-mini-latest' },
      ],
    })
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    expect(mocks.getChatGPTModelPreset).toHaveBeenCalledTimes(1)
    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.5', 'codex-mini-latest'])
    expect(current.source).toBe('preset')
    expect(mocks.fetchChatGPTModels).not.toHaveBeenCalled()

    // A re-render with the same mode does not re-fetch anything.
    act(() => root.render(<Harness enabled={true} />))
    await flush()
    expect(mocks.getChatGPTModelPreset).toHaveBeenCalledTimes(1)
    expect(mocks.getChatGPTAuthStatus).toHaveBeenCalledTimes(1)
  })

  it('upgrades to the live subscription list when signed in', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.4' }] })
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockResolvedValue({
      models: [
        { name: 'gpt-5.5', context_window: 272000 },
        { name: 'gpt-6-luna', context_window: 272000 },
      ],
    })
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    expect(mocks.fetchChatGPTModels).toHaveBeenCalledTimes(1)
    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.5', 'gpt-6-luna'])
    expect(current.source).toBe('subscription')
    expect(current.loading).toBe(false)
    expect(current.error).toBeNull()
  })

  it('keeps the preset and records the error when the live fetch fails', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.4' }] })
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockRejectedValue(new Error('sign in first'))
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    expect(current.source).toBe('preset')
    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.4'])
    expect(current.error).toBe('sign in first')
    expect(current.loading).toBe(false)
  })

  it('refresh() re-runs the live fetch on demand', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.4' }] })
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockResolvedValue({ models: [{ name: 'gpt-5.5' }] })
    act(() => root.render(<Harness enabled={true} />))
    await flush()
    expect(current.source).toBe('preset')

    await act(async () => {
      current.refresh()
    })
    await flush()
    expect(mocks.fetchChatGPTModels).toHaveBeenCalledTimes(1)
    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.5'])
    expect(current.source).toBe('subscription')
  })

  it('re-fetches when a sign-in completes (chatgpt_auth:state success)', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.4' }] })
    mocks.fetchChatGPTModels.mockResolvedValue({ models: [{ name: 'gpt-5.5' }] })
    act(() => root.render(<Harness enabled={true} />))
    await flush()
    expect(current.source).toBe('preset')

    const handler = mocks.onChatGPTAuthState.mock.calls[0]![0] as (d: { state: string }) => void
    await act(async () => {
      handler({ state: 'success' })
    })
    await flush()
    expect(mocks.fetchChatGPTModels).toHaveBeenCalledTimes(1)
    expect(current.source).toBe('subscription')
  })

  it('never fetches and reports an empty list while disabled', async () => {
    act(() => root.render(<Harness enabled={false} />))
    await flush()
    expect(mocks.getChatGPTModelPreset).not.toHaveBeenCalled()
    expect(mocks.getChatGPTAuthStatus).not.toHaveBeenCalled()
    expect(current.models).toEqual([])
    expect(current.source).toBe('preset')
  })

  it('clears the list when the mode flips back to api_key', async () => {
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.5' }] })
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockResolvedValue({ models: [{ name: 'gpt-5.5' }] })
    act(() => root.render(<Harness enabled={true} />))
    await flush()
    expect(current.models).toHaveLength(1)
    expect(current.source).toBe('subscription')

    act(() => root.render(<Harness enabled={false} />))
    await flush()
    expect(current.models).toEqual([])
    expect(current.source).toBe('preset')
    expect(current.error).toBeNull()
  })

  it('keeps the preset when the live fetch settles before the preset does', async () => {
    // The blank-checklist regression: the auto-fetch (signed in) fails
    // while the preset RPC is still in flight. The old single-slot design
    // invalidated the preset's write, leaving an empty checklist.
    let resolvePreset!: (v: { models: Array<{ name: string }> }) => void
    mocks.getChatGPTModelPreset.mockReturnValue(
      new Promise((resolve) => {
        resolvePreset = resolve
      }),
    )
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockRejectedValue(new Error('backend 503'))
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    // The live failure recorded its error but could not blank anything…
    expect(current.error).toBe('backend 503')
    expect(current.source).toBe('preset')

    // …and the late preset answer still lands.
    await act(async () => {
      resolvePreset({ models: [{ name: 'gpt-5.4' }] })
    })
    await flush()
    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.4'])
    expect(current.source).toBe('preset')
    expect(current.error).toBe('backend 503')
  })

  it('keeps the preset when the live fetch returns an empty list', async () => {
    // The backend answers a below-minimum client_version with a VALID but
    // empty catalog — installing it would blank the checklist.
    mocks.getChatGPTModelPreset.mockResolvedValue({ models: [{ name: 'gpt-5.4' }] })
    mocks.getChatGPTAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth' })
    mocks.fetchChatGPTModels.mockResolvedValue({ models: [] })
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    expect(current.models.map((m) => m.name)).toEqual(['gpt-5.4'])
    expect(current.source).toBe('preset')
    expect(current.error).not.toBeNull()
    expect(current.loading).toBe(false)
  })

  it('keeps the list empty and logs when the preset load fails', async () => {
    mocks.getChatGPTModelPreset.mockRejectedValue(new Error('rpc gone'))
    act(() => root.render(<Harness enabled={true} />))
    await flush()

    expect(current.models).toEqual([])
    expect(mocks.loggerError).toHaveBeenCalled()
  })
})
