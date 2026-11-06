// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { SubscriptionModelListState } from './useChatGPTModelPreset'
import type { KimiModelPresetResponse } from '@/types/models'

const mocks = vi.hoisted(() => ({
  getKimiModelPreset: vi.fn(),
  fetchKimiModels: vi.fn(),
  getKimiAuthStatus: vi.fn(),
  onKimiAuthState: vi.fn((_callback: (data: { state: string }) => void) => () => {}),
  loggerError: vi.fn(),
  loggerWarn: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  getKimiModelPreset: mocks.getKimiModelPreset,
  fetchKimiModels: mocks.fetchKimiModels,
  getKimiAuthStatus: mocks.getKimiAuthStatus,
  onKimiAuthState: mocks.onKimiAuthState,
}))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: mocks.loggerWarn, error: mocks.loggerError },
}))

import { useKimiModelPreset } from './useKimiModelPreset'

let container: HTMLDivElement
let root: Root
let current: SubscriptionModelListState

function Harness({ enabled, region = 'global' }: { enabled: boolean; region?: 'global' | 'mainland_cn' }) {
  current = useKimiModelPreset(enabled, region)
  return null
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: Error) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function settle<T>(call: Deferred<T>, value: T): Promise<void> {
  await act(async () => {
    call.resolve(value)
    await call.promise
  })
}

async function settleFailure<T>(call: Deferred<T>, error: Error): Promise<void> {
  await act(async () => {
    call.reject(error)
    await call.promise.catch(() => undefined)
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth', in_flight: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('useKimiModelPreset', () => {
  it('stays on the offline preset while signed out', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3.1-pro', context_window: 1048576, reasoning: true }] })
    expect(current.models.map((m) => m.name)).toEqual(['k3.1-pro'])
    expect(current.source).toBe('preset')
    expect(mocks.fetchKimiModels).not.toHaveBeenCalled()
  })

  it('replaces the preset with the live subscription catalog when signed in', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels.mockReturnValueOnce(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settle(live, {
      models: [
        { name: 'k3.1-pro', context_window: 1048576, reasoning: true },
        { name: 'k3.1-pro-highspeed', context_window: 1048576, reasoning: true },
      ],
    })
    expect(current.models.map((m) => m.name)).toEqual(['k3.1-pro', 'k3.1-pro-highspeed'])
    expect(current.source).toBe('subscription')
    expect(current.error).toBeNull()
  })

  it('keeps the preset and records one warning when the live fetch fails', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    const failure = new Error('sign in first')
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels.mockReturnValueOnce(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settleFailure(live, failure)
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
    expect(current.source).toBe('preset')
    expect(current.error).toBe('sign in first')
    expect(mocks.loggerWarn).toHaveBeenCalledTimes(1)
    expect(mocks.loggerWarn).toHaveBeenCalledWith('Kimi subscription model list fetch failed:', failure)
  })

  it('refresh re-runs the live fetch', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    mocks.fetchKimiModels.mockReturnValueOnce(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    act(() => current.refresh())
    await settle(live, { models: [{ name: 'k3.1-pro' }] })
    expect(mocks.fetchKimiModels).toHaveBeenCalledTimes(1)
    expect(current.source).toBe('subscription')
    expect(current.models.map((m) => m.name)).toEqual(['k3.1-pro'])
  })

  it('re-fetches after a completed sign-in', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    mocks.fetchKimiModels.mockReturnValueOnce(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    const handler = mocks.onKimiAuthState.mock.calls[0]![0] as (data: { state: string }) => void
    act(() => handler({ state: 'success' }))
    await settle(live, { models: [{ name: 'k3.1-pro' }] })
    expect(current.source).toBe('subscription')
  })

  it('clears subscription models when api_key mode resumes', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValueOnce(preset.promise)
    act(() => root.render(<Harness enabled={true} />))
    act(() => root.render(<Harness enabled={false} />))
    expect(current.models).toEqual([])
    expect(current.error).toBeNull()
    await settle(preset, { models: [{ name: 'k3' }] })
    expect(current.models).toEqual([])
  })

  it('restores the preset when a refresh fails after a successful live fetch', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const firstLive = deferred<KimiModelPresetResponse>()
    const failedRefresh = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValue(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels
      .mockReturnValueOnce(firstLive.promise)
      .mockReturnValueOnce(failedRefresh.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settle(firstLive, { models: [{ name: 'k3.1-pro' }] })
    expect(current.source).toBe('subscription')

    act(() => current.refresh())
    await settleFailure(failedRefresh, new Error('catalog unavailable'))
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
    expect(current.source).toBe('preset')
    expect(current.error).toBe('catalog unavailable')
  })

  it('restores the preset when a refresh returns an empty catalog after a success', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const firstLive = deferred<KimiModelPresetResponse>()
    const emptyRefresh = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValue(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels
      .mockReturnValueOnce(firstLive.promise)
      .mockReturnValueOnce(emptyRefresh.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settle(firstLive, { models: [{ name: 'k3.1-pro' }] })

    act(() => current.refresh())
    await settle(emptyRefresh, { models: [] })
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
    expect(current.source).toBe('preset')
  })

  it('restores the preset after sign-out', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValue(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels.mockReturnValue(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settle(live, { models: [{ name: 'k3.1-pro' }] })

    const handler = mocks.onKimiAuthState.mock.calls[0]![0] as (data: { state: string }) => void
    act(() => handler({ state: 'cancelled' }))
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
    expect(current.source).toBe('preset')
  })

  it('restores the preset when the account region changes', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValue(preset.promise)
    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false })
    mocks.fetchKimiModels.mockReturnValue(live.promise)
    act(() => root.render(<Harness enabled={true} region="global" />))
    await settle(preset, { models: [{ name: 'k3' }] })
    await settle(live, { models: [{ name: 'k3.1-pro' }] })

    mocks.getKimiAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth', in_flight: false })
    act(() => root.render(<Harness enabled={true} region="mainland_cn" />))
    const status = mocks.getKimiAuthStatus.mock.results[mocks.getKimiAuthStatus.mock.results.length - 1]
    await act(async () => { await status?.value })
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
    expect(current.source).toBe('preset')
  })

  it('ignores a delayed signed-in status after cancellation', async () => {
    const preset = deferred<KimiModelPresetResponse>()
    const status = deferred<{ signed_in: boolean; mode: string; in_flight: boolean }>()
    const live = deferred<KimiModelPresetResponse>()
    mocks.getKimiModelPreset.mockReturnValue(preset.promise)
    mocks.getKimiAuthStatus.mockReturnValue(status.promise)
    mocks.fetchKimiModels.mockReturnValue(live.promise)
    act(() => root.render(<Harness enabled={true} />))
    await settle(preset, { models: [{ name: 'k3' }] })

    const handler = mocks.onKimiAuthState.mock.calls[0]![0] as (data: { state: string }) => void
    act(() => handler({ state: 'cancelled' }))
    await settle(status, { signed_in: true, mode: 'oauth', in_flight: false })
    expect(mocks.fetchKimiModels).not.toHaveBeenCalled()
    expect(current.error).toBeNull()
    expect(current.models.map((m) => m.name)).toEqual(['k3'])
  })
})
