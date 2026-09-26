// @vitest-environment node
//
// embeddedLLMStore — the reducer, the backend sync functions and the
// referential-stability invariant. The component tests
// (components/settings/EmbeddedLLMSettings.test.tsx) cover the rendered
// lifecycle; these cover the store contract those surfaces rely on:
//
//   - `status` has ONE writer (setStatus) and the `embedded_llm:state` event is
//     an invalidation trigger (a re-read), never a partial patch.
//   - `installing` is raised by the authoritative snapshot AND by a progress
//     event (which is itself the proof of a live run), and a snapshot that
//     reports no live run retires the bars with it.
//   - `progress` keeps every component's OWN payload — never aggregated.
//   - the event subscription is shared and refcounted, so two mounted surfaces
//     apply one event once.
//   - selectors hand out primitives or stable references (React error #185).

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getEmbeddedLLMStatus: vi.fn(),
  getEmbeddedLLMTuning: vi.fn(),
  onState: vi.fn(),
  onProgress: vi.fn(),
  invalidateConfigCache: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// The store invalidates the shared model cache on an install transition; stub
// the module so the assertion does not pull the real cache into the node env.
vi.mock('@/hooks/useConfigData', () => ({
  invalidateConfigCache: mocks.invalidateConfigCache,
}))

vi.mock('@/api/embedded', () => ({
  DEFAULT_AUTO_UNLOAD_MINUTES: 60,
  MIN_AUTO_UNLOAD_MINUTES: 1,
  getEmbeddedLLMStatus: mocks.getEmbeddedLLMStatus,
  installEmbeddedLLM: vi.fn(),
  removeEmbeddedLLM: vi.fn(),
  loadEmbeddedLLM: vi.fn(),
  unloadEmbeddedLLM: vi.fn(),
  setEmbeddedLLMAutoUnload: vi.fn(),
  onEmbeddedLLMState: mocks.onState,
  onEmbeddedLLMInstallProgress: mocks.onProgress,
}))

vi.mock('@/api/embeddedTuning', () => ({
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
}))

import {
  refreshEmbeddedLLMStatus,
  refreshEmbeddedLLMTuning,
  subscribeEmbeddedLLMEvents,
  useEmbeddedLLMStore,
} from './embeddedLLMStore'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMPlan, EmbeddedLLMTuning } from '@/api/embeddedTuning'
import type { EmbeddedLLMInstallProgressData } from '@/types/events'

/** The all-zero measured-topology / plan block (no probe, no recorded plan). */
const EMPTY_PLAN: EmbeddedLLMPlan = {
  recorded: false,
  packing: '',
  kv_type: '',
  context_size: 0,
  fit: false,
  fit_arg: '',
  fit_target_mib: 0,
  fit_min_context: 0,
  offload_mode: 'auto',
  layers: -1,
  kv_offload: false,
  mmproj_offload: false,
  parallel: 0,
  cache_ram_mib: -1,
  gpu_family: '',
  device_budget_mib: 0,
  host_budget_mib: 0,
  expected_device_mib: 0,
  expected_host_mib: 0,
  notes: [],
}

/** The captured handler of a mocked subscription, so a test can emit an event. */
function captured(subscribeMock: ReturnType<typeof vi.fn>): (data: unknown) => void {
  const calls = subscribeMock.mock.calls as unknown[][]
  const handler = calls[calls.length - 1]?.[0] as (data: unknown) => void | undefined
  if (typeof handler !== 'function') throw new Error('no handler was subscribed')
  return handler
}

function makeStatus(overrides: Partial<EmbeddedLLMStatus> = {}): EmbeddedLLMStatus {
  return {
    state: 'not_installed',
    installed: false,
    installing: false,
    loading: false,
    loaded: false,
    packing: '',
    backend: '',
    port: 0,
    context_size: 0,
    auto_unload_enabled: false,
    auto_unload_minutes: 60,
    idle_remaining_seconds: 0,
    base_url: '',
    model_id: '',
    model_name: 'Bonsai 2 27B',
    runtime_version: '',
    installed_at: '',
    model_file: '',
    devices: [],
    unified: false,
    host_ram_gib: 0,
    device_budget_mib: 0,
    host_budget_mib: 0,
    topology_probed_at: '',
    plan: EMPTY_PLAN,
    reload_required: false,
    pid: 0,
    error: '',
    available: true,
    ...overrides,
  }
}

function progress(
  component: EmbeddedLLMInstallProgressData['component'],
  bytes_done: number,
  bytes_total: number,
  stage: EmbeddedLLMInstallProgressData['stage'] = 'downloading',
): EmbeddedLLMInstallProgressData {
  return { component, stage, bytes_done, bytes_total }
}

const state = () => useEmbeddedLLMStore.getState()

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus())
  // A mocked subscription hands back a noop unsubscribe and records the handler.
  mocks.onState.mockReturnValue(() => {})
  mocks.onProgress.mockReturnValue(() => {})
  state().reset()
})

describe('embeddedLLMStore — the status snapshot', () => {
  it('replaces the snapshot wholesale and mirrors the backend install flag', () => {
    const snapshot = makeStatus({ installed: true, installing: true, packing: 'PQ2_0' })
    state().setStatus(snapshot)

    expect(state().status).toBe(snapshot)
    expect(state().installing).toBe(true)
    expect(state().statusLoading).toBe(false)
  })

  it('retires the progress bars when a snapshot reports no live run', () => {
    state().applyProgress(progress('model', 1, 10))
    expect(Object.keys(state().progress)).toHaveLength(1)

    state().setStatus(makeStatus({ installing: false }))

    expect(state().installing).toBe(false)
    expect(state().progress).toEqual({})
  })

  it('keeps the bars of a run that is still in flight', () => {
    state().applyProgress(progress('model', 1, 10))
    state().setStatus(makeStatus({ installing: true }))

    expect(state().progress.model?.bytes_done).toBe(1)
  })
})

describe('embeddedLLMStore — install progress', () => {
  it('keeps every component separate and never aggregates the bytes', () => {
    state().applyProgress(progress('runtime', 100, 150))
    state().applyProgress(progress('model', 2_000, 7_206_168_928))
    state().applyProgress(progress('mmproj', 3, 600))

    const p = state().progress
    expect(p.runtime?.bytes_done).toBe(100)
    expect(p.model?.bytes_done).toBe(2_000)
    expect(p.mmproj?.bytes_done).toBe(3)
    // A progress event is the proof of a live run.
    expect(state().installing).toBe(true)
  })

  it('updates one component in place, keeping the others', () => {
    state().applyProgress(progress('runtime', 10, 100))
    state().applyProgress(progress('model', 20, 100))
    state().applyProgress(progress('runtime', 50, 100))

    expect(state().progress.runtime?.bytes_done).toBe(50)
    expect(state().progress.model?.bytes_done).toBe(20)
  })

  it('beginInstall drops the previous run and its failure', () => {
    state().applyProgress(progress('runtime', 10, 100))
    state().setError('the previous run failed')

    state().beginInstall()

    expect(state().installing).toBe(true)
    expect(state().progress).toEqual({})
    expect(state().error).toBeNull()
    expect(state().busy).toBeNull()
  })
})

describe('embeddedLLMStore — refreshEmbeddedLLMStatus', () => {
  it('applies the snapshot and clears the read spinner', async () => {
    const snapshot = makeStatus({ installed: true, packing: 'PTQ1_0', backend: 'vulkan' })
    mocks.getEmbeddedLLMStatus.mockResolvedValue(snapshot)

    await expect(refreshEmbeddedLLMStatus()).resolves.toBe(true)

    expect(state().status).toBe(snapshot)
    expect(state().statusLoading).toBe(false)
  })

  it('never throws on a failed read, keeps the previous snapshot and reports false', async () => {
    const previous = makeStatus({ installed: true })
    state().setStatus(previous)
    mocks.getEmbeddedLLMStatus.mockRejectedValue(new Error('Wails App bindings are not available'))

    await expect(refreshEmbeddedLLMStatus()).resolves.toBe(false)

    expect(state().status).toBe(previous)
    expect(state().statusLoading).toBe(false)
    // A failed READ is not a failed action: it must not paint the action error.
    expect(state().error).toBeNull()
  })
})

describe('embeddedLLMStore — the shared event subscription', () => {
  it('subscribes once for two consumers and applies one progress event once', () => {
    const offA = subscribeEmbeddedLLMEvents()
    const offB = subscribeEmbeddedLLMEvents()
    expect(mocks.onProgress).toHaveBeenCalledTimes(1)
    expect(mocks.onState).toHaveBeenCalledTimes(1)

    captured(mocks.onProgress)(progress('model', 5, 10))
    expect(state().progress.model?.bytes_done).toBe(5)
    expect(state().installing).toBe(true)

    // One consumer leaving keeps the other subscribed.
    offA()
    expect(mocks.onProgress).toHaveBeenCalledTimes(1)
    offB()
  })

  it('tears the shared subscription down when the last consumer leaves', () => {
    const off = subscribeEmbeddedLLMEvents()
    off()
    off() // idempotent: a StrictMode double-cleanup cannot drop a live one

    expect(mocks.onProgress).toHaveBeenCalledTimes(1)
    // A fresh consumer re-subscribes from scratch.
    const off2 = subscribeEmbeddedLLMEvents()
    expect(mocks.onProgress).toHaveBeenCalledTimes(2)
    off2()
  })

  it('re-reads the authoritative snapshot on a state event instead of patching', async () => {
    const fresh = makeStatus({ installed: true, packing: 'PQ2_0' })
    mocks.getEmbeddedLLMStatus.mockResolvedValue(fresh)
    const off = subscribeEmbeddedLLMEvents()

    captured(mocks.onState)({ installed: true, packing: 'PTQ1_0' })
    await Promise.resolve()
    await Promise.resolve()

    expect(mocks.getEmbeddedLLMStatus).toHaveBeenCalledTimes(1)
    // The snapshot the RPC returned wins — the narrower event payload is only a
    // trigger, so no half-truth is merged in.
    expect(state().status).toBe(fresh)
    expect(state().status?.packing).toBe('PQ2_0')
    off()
  })
})

describe('embeddedLLMStore — selector stability (React error #185)', () => {
  it('hands out the same empty-progress reference across an unrelated update', () => {
    const before = state().progress
    state().setStatus(makeStatus({ installed: true }))
    expect(state().progress).toBe(before)
  })

  it('changes the progress reference only when a component actually reports', () => {
    const before = state().progress
    state().setStatus(makeStatus({ installing: true }))
    expect(state().progress).toBe(before)

    state().applyProgress(progress('runtime', 1, 2))
    expect(state().progress).not.toBe(before)
  })
})

// The install state decides whether `llm.openai_compatible.embedded` exists at
// all, and therefore whether `GetConfig().llm.all_models` lists the local model.
// The chat toolbar's picker reads that list from useConfigData's module-level
// cache, which no other embedded path invalidates (the settings dialog re-reads
// config on every open) — so an install/remove transition must drop the cache,
// or the chat picker keeps offering the pre-install list.
describe('embeddedLLMStore — model-cache invalidation on an install transition', () => {
  it('drops the shared model cache when an install makes the provider appear', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: false }))
    await refreshEmbeddedLLMStatus()
    expect(mocks.invalidateConfigCache).not.toHaveBeenCalled()

    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true }))
    await refreshEmbeddedLLMStatus()

    expect(mocks.invalidateConfigCache).toHaveBeenCalledTimes(1)
  })

  it('drops it again when a removal makes the provider disappear', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true }))
    await refreshEmbeddedLLMStatus()
    expect(mocks.invalidateConfigCache).not.toHaveBeenCalled()

    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: false }))
    await refreshEmbeddedLLMStatus()

    expect(mocks.invalidateConfigCache).toHaveBeenCalledTimes(1)
  })

  it('does not invalidate on load/unload — the selectable model list is unchanged', async () => {
    // An unloaded model stays listed: the first request to it loads it. So a
    // load/unload transition must not cost a config refetch.
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true, loaded: false }))
    await refreshEmbeddedLLMStatus()

    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true, loaded: true }))
    await refreshEmbeddedLLMStatus()
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true, loaded: false }))
    await refreshEmbeddedLLMStatus()

    expect(state().status?.installed).toBe(true)
    expect(mocks.invalidateConfigCache).not.toHaveBeenCalled()
  })

  it('does not invalidate on the FIRST read — there is no stale cache entry yet', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true }))
    await refreshEmbeddedLLMStatus()

    expect(state().status?.installed).toBe(true)
    expect(mocks.invalidateConfigCache).not.toHaveBeenCalled()
  })

  it('does not invalidate when a failed read leaves the previous snapshot in place', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: false }))
    await refreshEmbeddedLLMStatus()
    mocks.getEmbeddedLLMStatus.mockRejectedValue(new Error('Wails App bindings are not available'))

    await refreshEmbeddedLLMStatus()

    expect(state().status?.installed).toBe(false)
    expect(mocks.invalidateConfigCache).not.toHaveBeenCalled()
  })

  it('invalidates when a state EVENT drives the transition (the install-completion path)', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: false }))
    await refreshEmbeddedLLMStatus()
    const off = subscribeEmbeddedLLMEvents()

    // The install finishes in the background: the backend emits
    // `embedded_llm:state`, the store re-reads, and the flip to installed must
    // reach the model pickers through the shared cache.
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installed: true }))
    captured(mocks.onState)({ installed: true })

    await vi.waitFor(() => {
      expect(mocks.invalidateConfigCache).toHaveBeenCalledTimes(1)
    })
    expect(state().status?.installed).toBe(true)
    off()
  })
})

describe('embeddedLLMStore — the tuning slice', () => {
  /** The all-unset tuning snapshot (the all-Auto plan). */
  function makeTuning(overrides: Partial<EmbeddedLLMTuning> = {}): EmbeddedLLMTuning {
    return {
      context: { mode: null, tokens: null },
      kv_cache_type: null,
      offload: { mode: null, layers: null },
      fit: null,
      fit_target_mib: null,
      fit_min_context: null,
      kv_offload: null,
      mmproj_offload: null,
      packing: null,
      parallel: null,
      cache_ram_mib: null,
      host_reserve_gib: null,
      ...overrides,
    }
  }

  it('refreshEmbeddedLLMTuning applies the snapshot wholesale and clears the spinner', async () => {
    const tuning = makeTuning({ kv_cache_type: 'q8_0' })
    mocks.getEmbeddedLLMTuning.mockResolvedValue(tuning)

    await expect(refreshEmbeddedLLMTuning()).resolves.toBe(true)

    expect(state().tuning).toBe(tuning)
    expect(state().tuningLoading).toBe(false)
  })

  it('never throws on a failed read, keeps the previous snapshot and paints no action error', async () => {
    const previous = makeTuning({ parallel: 2 })
    mocks.getEmbeddedLLMTuning.mockResolvedValueOnce(previous)
    await refreshEmbeddedLLMTuning()

    mocks.getEmbeddedLLMTuning.mockRejectedValue(new Error('not ready'))
    await expect(refreshEmbeddedLLMTuning()).resolves.toBe(false)

    expect(state().tuning).toBe(previous)
    expect(state().tuningLoading).toBe(false)
    expect(state().error).toBeNull()
  })

  it('hands out the exact reference it was given (one writer, no clone)', () => {
    const tuning = makeTuning({ fit: false })
    state().setTuning(tuning)

    const selected = useEmbeddedLLMStore.getState().tuning
    expect(selected).toBe(tuning)
  })
})
