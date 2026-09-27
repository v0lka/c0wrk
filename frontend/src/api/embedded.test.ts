// Unit tests for api/embedded.ts — the lifecycle RPC wrappers' local bounds and
// the status snapshot's boundary validation.
//
// The auto-unload budget is the sharp one: `embedded_llm.auto_unload.minutes`
// is converted to a time.Duration by an unchecked multiply on the Go side, so a
// value above `embeddedllm.MaxAutoUnloadMinutes` wraps — an "effectively never"
// budget can invert into tens of seconds or single-digit microseconds. The
// wrapper (and the input's `max`) refuse it client-side with an actionable
// message instead of persisting the opposite of what the field says.

import { describe, it, expect, vi, beforeEach } from 'vitest'

// --- Mock getApp before importing the module under test ---
const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
  onGlobalEvent: vi.fn(() => () => {}),
  reportDroppedEvent: vi.fn(),
  subscribe: vi.fn(() => () => {}),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import {
  DEFAULT_AUTO_UNLOAD_MINUTES,
  MAX_AUTO_UNLOAD_MINUTES,
  MIN_AUTO_UNLOAD_MINUTES,
  getEmbeddedLLMStatus,
  isEmbeddedLLMStatus,
  setEmbeddedLLMAutoUnload,
} from './embedded'

beforeEach(() => {
  vi.clearAllMocks()
  for (const key of Object.keys(mockApp)) delete mockApp[key]
})

/** A complete, healthy status snapshot (every DTO field is always present). */
const STATUS = {
  state: 'installed',
  installed: true,
  installing: false,
  loading: false,
  loaded: false,
  packing: 'PQ2_0',
  backend: 'metal',
  port: 43211,
  context_size: 131072,
  auto_unload_enabled: true,
  auto_unload_minutes: 60,
  idle_remaining_seconds: 0,
  base_url: 'http://127.0.0.1:43211/v1',
  model_id: 'embedded/Bonsai 2 27B',
  model_name: 'Bonsai 2 27B',
  runtime_version: 'prism-b10735',
  installed_at: '2026-01-01T00:00:00Z',
  model_file: '/x.gguf',
  pid: 4242,
  error: '',
  available: true,
  devices: [],
  unified: true,
  host_ram_gib: 128,
  device_budget_mib: 24576,
  host_budget_mib: 8192,
  topology_probed_at: '',
  plan: {
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
  },
  reload_required: false,
}

describe('the auto-unload bounds mirror the backend', () => {
  it('mirrors embeddedllm.MaxAutoUnloadMinutes — one year of residency', () => {
    expect(MAX_AUTO_UNLOAD_MINUTES).toBe(525600)
    expect(MAX_AUTO_UNLOAD_MINUTES).toBe(365 * 24 * 60)
  })

  it('keeps the floor at one minute and the default at an hour', () => {
    expect(MIN_AUTO_UNLOAD_MINUTES).toBe(1)
    expect(DEFAULT_AUTO_UNLOAD_MINUTES).toBe(60)
  })
})

describe('setEmbeddedLLMAutoUnload — the local range check', () => {
  beforeEach(() => {
    mockApp.SetEmbeddedLLMAutoUnload = vi.fn(async () => undefined)
  })

  it('passes an in-range budget through, including both ends', async () => {
    await expect(setEmbeddedLLMAutoUnload(true, MIN_AUTO_UNLOAD_MINUTES)).resolves.toBeUndefined()
    await expect(setEmbeddedLLMAutoUnload(true, 60)).resolves.toBeUndefined()
    await expect(setEmbeddedLLMAutoUnload(false, MAX_AUTO_UNLOAD_MINUTES)).resolves.toBeUndefined()
    expect(mockApp.SetEmbeddedLLMAutoUnload).toHaveBeenCalledTimes(3)
  })

  const REFUSED: ReadonlyArray<readonly [string, number]> = [
    ['zero', 0],
    ['a negative budget', -30],
    ['a fractional budget', 1.5],
    ['one minute past the ceiling', MAX_AUTO_UNLOAD_MINUTES + 1],
    ['an int32-overflowing budget', 2147483647],
    ['NaN', Number.NaN],
    ['Infinity', Number.POSITIVE_INFINITY],
  ]

  it.each(REFUSED)('refuses %s without an RPC', async (_label, minutes) => {
    await expect(setEmbeddedLLMAutoUnload(true, minutes)).rejects.toThrow(
      /whole number of minutes between 1 and 525600/,
    )
    expect(mockApp.SetEmbeddedLLMAutoUnload).not.toHaveBeenCalled()
  })

  it('names the offending value so the message is actionable', async () => {
    await expect(setEmbeddedLLMAutoUnload(true, 999999999)).rejects.toThrow('got 999999999')
  })
})

describe('isEmbeddedLLMStatus / getEmbeddedLLMStatus — the boundary guard', () => {
  it('accepts a healthy snapshot', () => {
    expect(isEmbeddedLLMStatus(STATUS)).toBe(true)
  })

  it('rejects a snapshot whose measured-topology block drifted', () => {
    expect(isEmbeddedLLMStatus({ ...STATUS, unified: 'yes' })).toBe(false)
    expect(isEmbeddedLLMStatus({ ...STATUS, host_ram_gib: '128' })).toBe(false)
  })

  it('rejects a snapshot whose recorded plan scalars drifted', () => {
    expect(
      isEmbeddedLLMStatus({ ...STATUS, plan: { ...STATUS.plan, expected_device_mib: '12 GiB' } }),
    ).toBe(false)
    expect(isEmbeddedLLMStatus({ ...STATUS, plan: { ...STATUS.plan, notes: 'one' } })).toBe(false)
  })

  it('returns a validated snapshot and refuses a drifted one loudly', async () => {
    mockApp.GetEmbeddedLLMStatus = async () => STATUS
    await expect(getEmbeddedLLMStatus()).resolves.toEqual(STATUS)

    mockApp.GetEmbeddedLLMStatus = async () => ({ ...STATUS, plan: { notes: [] } })
    await expect(getEmbeddedLLMStatus()).rejects.toThrow(/invalid status payload/)
  })
})
