// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { configMocks, subscribeMock } = vi.hoisted(() => ({
  configMocks: {
    getConfig: vi.fn(),
  },
  // Typed to the real subscribe() signature (event name + handler) so
  // mockImplementation below type-checks under `tsc -b`.
  subscribeMock: vi.fn((_name: string, _handler: () => void) => () => {}),
}))

vi.mock('@/api/config', () => ({ getConfig: configMocks.getConfig }))
vi.mock('@/api/runtime', () => ({ subscribe: subscribeMock }))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { useExperimentalFeatures } from './useExperimentalFeatures'
import { useExperimentalStore } from '@/stores/experimentalStore'
import type { ConfigResponse } from '@/types/models'

/**
 * Minimal valid ConfigResponse; the hook only reads loaded/experimental.
 * `llm` and `proxy` are intentionally empty objects cast per-field — building
 * full provider/proxy structures would couple the test to unrelated shapes.
 */
function makeConfig(loaded: boolean, experimentalEnabled: boolean): ConfigResponse {
  return {
    loaded,
    log_level: 'info',
    config_errors: [],
    llm: {} as ConfigResponse['llm'],
    search: { provider: '', api_key: '' },
    proxy: {} as ConfigResponse['proxy'],
    experimental: { enabled: experimentalEnabled },
  }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Handlers captured per event name (the hook subscribes to several). */
const capturedHandlers = new Map<string, () => void>()
subscribeMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  return () => {}
})

function Harness(): boolean {
  return useExperimentalFeatures()
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Let the fire-and-forget promise chain settle past the store writes. */
async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

function fireBackendReady(): void {
  act(() => {
    capturedHandlers.get('backend:ready')?.()
  })
}

function fireConfigUpdated(): void {
  act(() => {
    capturedHandlers.get('config:updated')?.()
  })
}

beforeEach(() => {
  configMocks.getConfig.mockReset()
  subscribeMock.mockClear()
  capturedHandlers.clear()
  useExperimentalStore.setState({ enabled: false, loaded: false })
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('useExperimentalFeatures', () => {
  it('latches the persisted switch on a normal (loaded) response', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, true))

    renderHook()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().enabled).toBe(true)
    expect(useExperimentalStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
    // The hook subscribes to both retry triggers.
    expect(subscribeMock).toHaveBeenCalledWith('backend:ready', expect.any(Function))
    expect(subscribeMock).toHaveBeenCalledWith('config:updated', expect.any(Function))
  })

  it('does NOT latch when the backend answers loaded=false during startup', async () => {
    // Regression: GetConfig succeeds with a zeroed config while Startup is
    // still running. Latching here would leave the switch off for the whole
    // session despite config.yaml saying enabled=true.
    configMocks.getConfig.mockResolvedValue(makeConfig(false, false))

    renderHook()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().loaded).toBe(false)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
  })

  it('recovers via the backend:ready retry after a loaded=false first answer', async () => {
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false, false)) // startup race
      .mockResolvedValueOnce(makeConfig(true, true)) // config live after ready

    renderHook()
    await flushMicrotasks()
    expect(useExperimentalStore.getState().loaded).toBe(false)

    fireBackendReady()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().enabled).toBe(true)
    expect(useExperimentalStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
  })

  it('stays retryable (loaded=false) when the post-ready answer is still not loaded', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(false, false))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().loaded).toBe(false)
    expect(useExperimentalStore.getState().enabled).toBe(false)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
  })

  it('recovers via config:updated when still not latched', async () => {
    // Residual case: both the mount fetch and the backend:ready retry failed
    // transiently (or landed during the startup race), leaving the switch
    // "unknown". The next persisted config mutation emits config:updated,
    // which re-reads the now-live config without an app restart.
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false, false)) // startup race
      .mockResolvedValueOnce(makeConfig(false, false)) // post-ready still racing
      .mockResolvedValueOnce(makeConfig(true, true)) // config live after a settings save

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()
    expect(useExperimentalStore.getState().loaded).toBe(false)

    fireConfigUpdated()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().enabled).toBe(true)
    expect(useExperimentalStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(3)
  })

  it('does not re-fetch after the switch has latched', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, true))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()
    fireConfigUpdated()
    await flushMicrotasks()

    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
  })

  it('keeps the fail-closed state on a fetch error', async () => {
    configMocks.getConfig.mockRejectedValue(new Error('backend gone'))

    renderHook()
    await flushMicrotasks()

    expect(useExperimentalStore.getState().enabled).toBe(false)
    expect(useExperimentalStore.getState().loaded).toBe(false)
  })
})
