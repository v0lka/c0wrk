// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { configMocks, onGlobalEventMock } = vi.hoisted(() => ({
  configMocks: {
    getConfig: vi.fn(),
    getModelProfiles: vi.fn(),
  },
  // Typed to the real onGlobalEvent() signature (event name + handler) so
  // mockImplementation below type-checks under `tsc -b`.
  onGlobalEventMock: vi.fn((_name: string, _handler: () => void) => () => {}),
}))

vi.mock('@/api/config', () => configMocks)
vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { useModelProfilesGate } from './useModelProfilesGate'
import { useModelProfilesGateStore } from '@/stores/modelProfilesGateStore'
import { logger } from '@/lib/logger'
import type { ConfigResponse, ConfigModelProfilesResponse } from '@/types/models'

/**
 * Minimal valid ConfigResponse; the hook only reads loaded/model_profiles. `llm` and
 * `proxy` are intentionally empty objects cast per-field — building full
 * provider/proxy structures would couple the test to unrelated shapes.
 * `model_profiles` omitted entirely models an older payload without the field.
 */
function makeConfig(loaded: boolean, model_profiles?: ConfigModelProfilesResponse): ConfigResponse {
  return {
    loaded,
    log_level: 'info',
    config_errors: [],
    llm: {} as ConfigResponse['llm'],
    search: { provider: '', api_key: '' },
    proxy: {} as ConfigResponse['proxy'],
    ...(model_profiles ? { model_profiles } : {}),
  }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Latest value returned by useModelProfilesGate across re-renders. */
const rendered = { blocked: false }

/** Handlers captured per event name (the hook subscribes to several). */
const capturedHandlers = new Map<string, () => void>()
onGlobalEventMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  return () => {}
})

function Harness(): null {
  rendered.blocked = useModelProfilesGate()
  return null
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
  vi.mocked(logger.error).mockClear()
  configMocks.getModelProfiles.mockReset().mockResolvedValue({
    active_id: 'generic', suggested_profile_id: 'bonsai-2-27b',
  })
  onGlobalEventMock.mockClear()
  capturedHandlers.clear()
  rendered.blocked = false
  useModelProfilesGateStore.setState({
    enabled: false, essentialToolsEnabled: false, loaded: false,
    activeProfileId: null, suggestedProfileId: null,
  })
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

describe('useModelProfilesGate', () => {
  it('latches the resolved gate on a normal (loaded) response', async () => {
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: true }),
    )

    renderHook()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: true,
      essentialToolsEnabled: true,
      activeProfileId: 'generic',
      suggestedProfileId: 'bonsai-2-27b',
      loaded: true,
    })
    expect(rendered.blocked).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
    // The hook subscribes to both retry triggers.
    expect(onGlobalEventMock).toHaveBeenCalledWith('backend:ready', expect.any(Function))
    expect(onGlobalEventMock).toHaveBeenCalledWith('config:updated', expect.any(Function))
  })

  it('does not block when the essential-tools variant is off', async () => {
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: false }),
    )

    renderHook()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: true,
      essentialToolsEnabled: false,
      loaded: true,
    })
    expect(rendered.blocked).toBe(false)
  })

  it('treats a missing model_profiles block as "off" and does not block', async () => {
    // Older payload without the optional field.
    configMocks.getConfig.mockResolvedValue(makeConfig(true))

    renderHook()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: false,
      essentialToolsEnabled: false,
      loaded: true,
    })
    expect(rendered.blocked).toBe(false)
  })

  it('does NOT latch when the backend answers loaded=false during startup', async () => {
    // Regression: GetConfig succeeds with a zeroed config while Startup is
    // still running. Latching "off" here would consume the one-shot
    // backend:ready retry and leave the gate unknown for the whole session.
    configMocks.getConfig.mockResolvedValue(makeConfig(false))

    renderHook()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState().loaded).toBe(false)
    expect(rendered.blocked).toBe(false)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
  })

  it('recovers via the backend:ready retry after a loaded=false first answer', async () => {
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false)) // startup race
      .mockResolvedValueOnce(makeConfig(true, { enabled: true, essential_tools_enabled: true }))

    renderHook()
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState().loaded).toBe(false)

    fireBackendReady()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState().loaded).toBe(true)
    expect(rendered.blocked).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
  })

  it('stays retryable (loaded=false) when the post-ready answer is still not loaded', async () => {
    configMocks.getConfig.mockResolvedValue(makeConfig(false))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState().loaded).toBe(false)
    expect(useModelProfilesGateStore.getState().enabled).toBe(false)
    expect(rendered.blocked).toBe(false)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
  })

  it('recovers via config:updated when still not latched', async () => {
    // Residual case: both the mount fetch and the backend:ready retry landed
    // during the startup race, leaving the gate "unknown". The next persisted
    // config mutation emits config:updated, which re-reads the live config.
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(false)) // startup race
      .mockResolvedValueOnce(makeConfig(false)) // post-ready still racing
      .mockResolvedValueOnce(makeConfig(true, { enabled: true, essential_tools_enabled: true }))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState().loaded).toBe(false)

    fireConfigUpdated()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState().loaded).toBe(true)
    expect(rendered.blocked).toBe(true)
    expect(configMocks.getConfig).toHaveBeenCalledTimes(3)
  })

  it('refreshes on both config:updated and backend:ready after latch', async () => {
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: true }),
    )

    renderHook()
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState().loaded).toBe(true)

    // backend:ready refreshes metadata even after a good mount response.
    configMocks.getModelProfiles.mockResolvedValue({
      active_id: 'bonsai-2-27b', suggested_profile_id: 'bonsai-2-27b',
    })
    fireBackendReady()
    await flushMicrotasks()
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
    expect(useModelProfilesGateStore.getState().activeProfileId).toBe('bonsai-2-27b')

    // config:updated is a REFRESH trigger: it re-reads the config even after
    // latching, because this store has no direct writer (unlike the
    // experimental store, which Settings updates directly).
    configMocks.getModelProfiles.mockResolvedValue({
      active_id: 'generic', suggested_profile_id: null,
    })
    fireConfigUpdated()
    await flushMicrotasks()
    expect(configMocks.getConfig).toHaveBeenCalledTimes(3)
    expect(configMocks.getModelProfiles).toHaveBeenCalledTimes(3)
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      activeProfileId: 'generic', suggestedProfileId: null,
    })
    expect(useModelProfilesGateStore.getState().loaded).toBe(true)
    expect(rendered.blocked).toBe(true)
  })

  it('unlatches via config:updated when the user disables the essential-tools variant', async () => {
    // The user followed the block hint into Settings and turned the variant
    // off: the backend's config:updated must refresh the latched gate so the
    // toggle unblocks without an app restart.
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(true, { enabled: true, essential_tools_enabled: true }))
      .mockResolvedValueOnce(makeConfig(true, { enabled: true, essential_tools_enabled: false }))

    renderHook()
    await flushMicrotasks()
    expect(rendered.blocked).toBe(true)
    expect(useModelProfilesGateStore.getState().essentialToolsEnabled).toBe(true)

    fireConfigUpdated()
    await flushMicrotasks()

    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: true,
      essentialToolsEnabled: false,
      loaded: true,
    })
    expect(rendered.blocked).toBe(false)
  })

  it('does not downgrade a good latch when a post-latch refresh answers loaded=false', async () => {
    configMocks.getConfig
      .mockResolvedValueOnce(makeConfig(true, { enabled: true, essential_tools_enabled: true }))
      .mockResolvedValueOnce(makeConfig(false))

    renderHook()
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState().loaded).toBe(true)

    fireConfigUpdated()
    await flushMicrotasks()

    // The mid-startup (loaded=false) refresh answer must leave the good latch
    // untouched rather than resetting it to "unknown".
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: true,
      essentialToolsEnabled: true,
      loaded: true,
    })
    expect(rendered.blocked).toBe(true)
  })

  it('keeps the goal gate and previous profile identity when metadata refresh fails', async () => {
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: true }),
    )
    renderHook()
    await flushMicrotasks()
    const failure = new Error('catalog unavailable')
    configMocks.getModelProfiles.mockRejectedValueOnce(failure)
    fireConfigUpdated()
    await flushMicrotasks()
    expect(rendered.blocked).toBe(true)
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      activeProfileId: 'generic', suggestedProfileId: 'bonsai-2-27b', loaded: true,
    })
    expect(logger.error).toHaveBeenCalledExactlyOnceWith(
      'useModelProfilesGate: failed to load profile metadata:', failure,
    )
    configMocks.getModelProfiles.mockResolvedValueOnce({
      active_id: 'bonsai-2-27b', suggested_profile_id: null,
    })
    fireBackendReady()
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      activeProfileId: 'bonsai-2-27b', suggestedProfileId: null,
    })
  })

  it('replays a config refresh that arrives while profile metadata is in flight', async () => {
    let resolveMetadata!: (value: { active_id: string; suggested_profile_id: string | null }) => void
    const pending = new Promise<{ active_id: string; suggested_profile_id: string | null }>((resolve) => {
      resolveMetadata = resolve
    })
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: true }),
    )
    configMocks.getModelProfiles.mockReturnValueOnce(pending).mockResolvedValueOnce({
      active_id: 'bonsai-2-27b', suggested_profile_id: null,
    })
    renderHook()
    await flushMicrotasks()
    fireConfigUpdated()
    expect(configMocks.getConfig).toHaveBeenCalledTimes(1)
    await act(async () => {
      resolveMetadata({ active_id: 'generic', suggested_profile_id: 'bonsai-2-27b' })
      await pending
    })
    await flushMicrotasks()
    expect(configMocks.getConfig).toHaveBeenCalledTimes(2)
    expect(configMocks.getModelProfiles).toHaveBeenCalledTimes(2)
    expect(useModelProfilesGateStore.getState()).toMatchObject({
      activeProfileId: 'bonsai-2-27b', suggestedProfileId: null,
    })
  })

  it('does not write late profile metadata after unmount', async () => {
    let resolveMetadata!: (value: { active_id: string; suggested_profile_id: string | null }) => void
    const pending = new Promise<{ active_id: string; suggested_profile_id: string | null }>((resolve) => {
      resolveMetadata = resolve
    })
    configMocks.getConfig.mockResolvedValue(makeConfig(true))
    configMocks.getModelProfiles.mockReturnValueOnce(pending)
    renderHook()
    await flushMicrotasks()
    act(() => root?.unmount())
    root = null
    resolveMetadata({ active_id: 'late', suggested_profile_id: null })
    await pending
    await flushMicrotasks()
    expect(useModelProfilesGateStore.getState().activeProfileId).toBeNull()
  })

  it('keeps the fail-safe state (loaded=false, not blocking) on a fetch error', async () => {
    configMocks.getConfig.mockRejectedValue(new Error('backend gone'))

    renderHook()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState()).toMatchObject({
      enabled: false,
      essentialToolsEnabled: false,
      loaded: false,
    })
    expect(rendered.blocked).toBe(false)
    expect(logger.error).toHaveBeenCalledExactlyOnceWith(
      'useModelProfilesGate: failed to load config:', expect.objectContaining({ message: 'backend gone' }),
    )

    // A later retry can still recover from the transient failure.
    configMocks.getConfig.mockResolvedValue(
      makeConfig(true, { enabled: true, essential_tools_enabled: true }),
    )
    fireBackendReady()
    await flushMicrotasks()

    expect(useModelProfilesGateStore.getState().loaded).toBe(true)
    expect(rendered.blocked).toBe(true)
  })
})
