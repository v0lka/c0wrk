// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { configMocks, onGlobalEventMock } = vi.hoisted(() => ({
  configMocks: {
    getConfig: vi.fn(),
  },
  // Typed to the real onGlobalEvent() signature (event name + handler) so
  // mockImplementation below type-checks under `tsc -b`.
  onGlobalEventMock: vi.fn((_name: string, _handler: () => void) => () => {}),
}))

vi.mock('@/api/config', () => ({ getConfig: configMocks.getConfig }))
vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { useSidebarPacks } from './useSidebarPacks'
import { useSidebarPacksStore } from '@/stores/sidebarPacksStore'
import type { ConfigResponse } from '@/types/models'

/**
 * Minimal valid ConfigResponse; the hook only reads loaded/research/specs.
 * `llm` and `proxy` are intentionally empty objects cast per-field — building
 * full provider/proxy structures would couple the test to unrelated shapes.
 */
function makeConfig(
  loaded: boolean,
  researchEnabled: boolean | null,
  specsEnabled: boolean | null,
): ConfigResponse {
  return {
    loaded,
    log_level: 'info',
    config_errors: [],
    llm: {} as ConfigResponse['llm'],
    search: { provider: '', api_key: '', base_url: '' },
    proxy: {} as ConfigResponse['proxy'],
    research:
      researchEnabled === null
        ? undefined // a config that predates the block entirely
        : { enabled: researchEnabled },
    specs:
      specsEnabled === null
        ? undefined // a config that predates the block entirely
        : { enabled: specsEnabled },
  }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Handlers captured per event name (the hook subscribes to several). */
const capturedHandlers = new Map<string, () => void>()
onGlobalEventMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  return () => {}
})

interface Gates {
  research: boolean
  specs: boolean
}

function Harness(): Gates {
  return useSidebarPacks()
}

function renderHook(): Gates {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  let gates: Gates = { research: true, specs: true }
  act(() => {
    r.render(createElement(() => {
      gates = Harness()
      return null
    }))
  })
  return gates
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
  onGlobalEventMock.mockClear()
  capturedHandlers.clear()
  useSidebarPacksStore.setState({ research: true, specs: true, loaded: false })
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

describe('useSidebarPacks', () => {
  it('latches the persisted gates on a normal (loaded) response', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, false, true))

    renderHook()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(false)
    expect(useSidebarPacksStore.getState().specs).toBe(true)
    expect(useSidebarPacksStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
    // The hook subscribes to both retry triggers.
    expect(onGlobalEventMock).toHaveBeenCalledWith('backend:ready', expect.any(Function))
    expect(onGlobalEventMock).toHaveBeenCalledWith('config:updated', expect.any(Function))
  })

  it('reads absent blocks as the resolved defaults (both gates on)', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, null, null))

    renderHook()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(useSidebarPacksStore.getState().specs).toBe(true)
    expect(useSidebarPacksStore.getState().loaded).toBe(true)
  })

  it('latches the two gates independently', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, true, false))

    renderHook()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(useSidebarPacksStore.getState().specs).toBe(false)
  })

  it('does NOT latch when the backend answers loaded=false during startup', async () => {
    // Startup race: GetConfig succeeds with a zeroed config while Startup is
    // still running. Latching here would hide the tabs for the whole session
    // despite config.yaml saying on (or off).
    configMocks.getConfig.mockResolvedValue(makeConfig(false, false, false))

    renderHook()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().loaded).toBe(false)
    // The pre-load defaults are kept (on), not the zeroed answer.
    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(useSidebarPacksStore.getState().specs).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
  })

  it('recovers via the backend:ready retry after a loaded=false first answer', async () => {
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false, false, false)) // startup race
      .mockResolvedValueOnce(makeConfig(true, false, false)) // config live after ready

    renderHook()
    await flushMicrotasks()
    expect(useSidebarPacksStore.getState().loaded).toBe(false)

    fireBackendReady()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(false)
    expect(useSidebarPacksStore.getState().specs).toBe(false)
    expect(useSidebarPacksStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
  })

  it('recovers via config:updated when still not latched', async () => {
    // Residual case: both the mount fetch and the backend:ready retry
    // landed during the startup race, leaving the gates "unknown". The next
    // persisted config mutation emits config:updated, which re-reads the
    // now-live config without an app restart.
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false, true, true)) // startup race
      .mockResolvedValueOnce(makeConfig(false, true, true)) // post-ready still racing
      .mockResolvedValueOnce(makeConfig(true, false, true)) // config live after a settings save

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()
    expect(useSidebarPacksStore.getState().loaded).toBe(false)

    fireConfigUpdated()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(false)
    expect(useSidebarPacksStore.getState().specs).toBe(true)
    expect(useSidebarPacksStore.getState().loaded).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(3)
  })

  it('does not re-fetch after the gates have latched (Settings keeps them in sync)', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(true, true, true))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()
    fireConfigUpdated()
    await flushMicrotasks()

    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
    expect(useSidebarPacksStore.getState().loaded).toBe(true)
  })

  it('keeps the on defaults on a fetch error (tabs stay visible, still retryable)', async () => {
    // The fetch-failure diagnostic is swallowed by the fully mocked logger
    // module — the hook's catch is the deliberate failure path here, and the
    // error spy assertion pins it was the only diagnostic emitted.
    const { logger } = await import('@/lib/logger')
    configMocks.getConfig.mockRejectedValue(new Error('backend gone'))

    renderHook()
    await flushMicrotasks()

    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(useSidebarPacksStore.getState().specs).toBe(true)
    expect(useSidebarPacksStore.getState().loaded).toBe(false)
    expect(logger.error).toHaveBeenCalledExactlyOnceWith(
      'useSidebarPacks: failed to load config:',
      new Error('backend gone'),
    )
  })
})
