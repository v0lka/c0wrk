// Unit tests for api/embeddedTuning.ts — the boundary guards and the local
// range check of a tuning patch.
//
// Two contracts are pinned here:
//
//   1. Every field the UI reads is TYPE-CHECKED before it reaches a component —
//      nothing is cast. A backend schema drift must fail the read loudly
//      instead of painting "undefined ctx / undefined device" out of a typed
//      field.
//   2. The numeric bounds mirror the backend one for one — the floors from
//      config.ToTuning, the CEILINGS from core/embeddedllm/limits.go, the
//      figures themselves pinned in lib/embeddedTuningLimits.test.ts — so an
//      out-of-range commit is refused locally with an actionable message
//      instead of round-tripping into a Go rejection (or, for
//      `host_reserve_gib`, into a float→int conversion whose result is
//      implementation-defined when the value does not fit). The same holds for
//      INTEGRALITY: every knob but `host_reserve_gib` is an `*int` on the wire,
//      so a decimal is a guaranteed Go unmarshal rejection whose raw driver
//      string (`json: cannot unmarshal number 2.5 into Go struct field …`) is
//      not an actionable message for the Settings error line.

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
  MAX_CONTEXT_TOKENS,
  MAX_TUNING_HOST_RESERVE_GIB,
  MAX_TUNING_LAYERS,
  MAX_TUNING_MIB,
  MAX_TUNING_PARALLEL,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
} from '@/lib/embeddedTuningLimits'
import {
  isEmbeddedLLMPlan,
  isEmbeddedLLMStatusExtras,
  isEmbeddedLLMTuning,
  validateEmbeddedLLMTuningPatch,
  type EmbeddedLLMPlan,
  type EmbeddedLLMStatusExtras,
  type EmbeddedLLMTuning,
} from './embeddedTuning'

beforeEach(() => {
  vi.clearAllMocks()
  for (const key of Object.keys(mockApp)) delete mockApp[key]
})

/** A recorded launch shape, every field populated. */
const PLAN: EmbeddedLLMPlan = {
  recorded: true,
  packing: 'PQ2_0',
  kv_type: 'q8_0',
  context_size: 131072,
  fit: false,
  fit_arg: 'off',
  fit_target_mib: 1024,
  fit_min_context: 65536,
  offload_mode: 'layers',
  layers: 30,
  kv_offload: true,
  mmproj_offload: true,
  parallel: 1,
  cache_ram_mib: -1,
  gpu_family: 'metal',
  device_budget_mib: 24576,
  host_budget_mib: 8192,
  expected_device_mib: 12288,
  expected_host_mib: 4096,
  notes: ['--fit was disabled explicitly'],
}

/** The unrecorded shape: sentinels and empties, which stay LEGAL values. */
const EMPTY_PLAN: EmbeddedLLMPlan = {
  ...PLAN,
  recorded: false,
  packing: '',
  kv_type: '',
  context_size: 0,
  fit_arg: '',
  offload_mode: 'auto',
  layers: -1,
  gpu_family: '',
  notes: [],
}

const EXTRAS: EmbeddedLLMStatusExtras = {
  devices: [{ name: 'Apple M4 Max', description: 'Metal', total_mib: 49152, free_mib: 24576 }],
  unified: true,
  host_ram_gib: 128,
  device_budget_mib: 24576,
  host_budget_mib: 8192,
  topology_probed_at: '2026-01-01T00:00:00Z',
  plan: PLAN,
  reload_required: false,
}

const TUNING: EmbeddedLLMTuning = {
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
}

describe('isEmbeddedLLMPlan — every field the install record renders', () => {
  it('accepts a recorded plan and the unrecorded sentinel shape', () => {
    expect(isEmbeddedLLMPlan(PLAN)).toBe(true)
    expect(isEmbeddedLLMPlan(EMPTY_PLAN)).toBe(true)
  })

  it('rejects a non-object', () => {
    expect(isEmbeddedLLMPlan(null)).toBe(false)
    expect(isEmbeddedLLMPlan('PQ2_0')).toBe(false)
    expect(isEmbeddedLLMPlan(undefined)).toBe(false)
  })

  it.each([
    ['recorded', 'yes'],
    ['packing', 2],
    ['kv_type', false],
    ['context_size', '131072'],
    ['fit', 'off'],
    ['fit_arg', 0],
    ['fit_target_mib', null],
    ['fit_min_context', undefined],
    ['offload_mode', 1],
    ['layers', '30'],
    ['kv_offload', 'true'],
    ['mmproj_offload', 1],
    ['parallel', null],
    ['cache_ram_mib', '-1'],
    ['gpu_family', 7],
    ['device_budget_mib', '24576'],
    ['host_budget_mib', {}],
    ['expected_device_mib', []],
    ['expected_host_mib', true],
    ['notes', 'one note'],
  ])('rejects a drifted %s', (key, value) => {
    expect(isEmbeddedLLMPlan({ ...PLAN, [key]: value })).toBe(false)
  })

  it('rejects a notes array that is not all strings', () => {
    expect(isEmbeddedLLMPlan({ ...PLAN, notes: ['ok', 3] })).toBe(false)
  })

  it('rejects a missing field', () => {
    const { expected_host_mib: _dropped, ...rest } = PLAN
    expect(isEmbeddedLLMPlan(rest)).toBe(false)
  })
})

describe('isEmbeddedLLMStatusExtras — the measured-topology block', () => {
  it('accepts the full block', () => {
    expect(isEmbeddedLLMStatusExtras(EXTRAS)).toBe(true)
  })

  it.each([
    ['devices', 'none'],
    ['unified', 'yes'],
    ['host_ram_gib', '128'],
    ['device_budget_mib', null],
    ['host_budget_mib', undefined],
    ['topology_probed_at', 0],
    ['reload_required', 'false'],
  ])('rejects a drifted %s', (key, value) => {
    expect(isEmbeddedLLMStatusExtras({ ...EXTRAS, [key]: value })).toBe(false)
  })

  it('rejects a malformed device entry', () => {
    expect(
      isEmbeddedLLMStatusExtras({
        ...EXTRAS,
        devices: [{ name: 'GPU', description: 'x', total_mib: '1', free_mib: 2 }],
      }),
    ).toBe(false)
  })

  it('rejects a plan whose scalars drifted — not only its notes', () => {
    expect(isEmbeddedLLMStatusExtras({ ...EXTRAS, plan: { ...PLAN, parallel: '1' } })).toBe(false)
    expect(isEmbeddedLLMStatusExtras({ ...EXTRAS, plan: { notes: [] } })).toBe(false)
  })
})

describe('isEmbeddedLLMTuning — the override surface', () => {
  it('accepts the all-unset snapshot and a populated one', () => {
    expect(isEmbeddedLLMTuning(TUNING)).toBe(true)
    expect(
      isEmbeddedLLMTuning({
        ...TUNING,
        context: { mode: 'exact', tokens: 49152 },
        offload: { mode: 'layers', layers: 30 },
        fit: false,
        host_reserve_gib: 6.5,
      }),
    ).toBe(true)
  })

  it.each([
    ['fit', 'false'],
    ['parallel', '2'],
    ['host_reserve_gib', '6'],
    ['kv_cache_type', 8],
  ])('rejects a drifted %s', (key, value) => {
    expect(isEmbeddedLLMTuning({ ...TUNING, [key]: value })).toBe(false)
  })

  it('rejects a missing composite knob', () => {
    expect(isEmbeddedLLMTuning({ ...TUNING, offload: undefined })).toBe(false)
  })
})

describe('validateEmbeddedLLMTuningPatch — ceilings', () => {
  it('accepts each knob exactly at its ceiling', () => {
    expect(() => validateEmbeddedLLMTuningPatch({ fit_target_mib: MAX_TUNING_MIB })).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ cache_ram_mib: MAX_TUNING_MIB })).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ parallel: MAX_TUNING_PARALLEL })).not.toThrow()
    expect(() =>
      validateEmbeddedLLMTuningPatch({ host_reserve_gib: MAX_TUNING_HOST_RESERVE_GIB }),
    ).not.toThrow()
    expect(() =>
      validateEmbeddedLLMTuningPatch({
        offload: { mode: 'layers', layers: MAX_TUNING_LAYERS },
      }),
    ).not.toThrow()
    expect(() =>
      validateEmbeddedLLMTuningPatch({
        context: { mode: 'exact', tokens: MAX_CONTEXT_TOKENS },
      }),
    ).not.toThrow()
  })

  it.each([
    ['fit_target_mib', { fit_target_mib: MAX_TUNING_MIB + 1 }],
    ['cache_ram_mib', { cache_ram_mib: MAX_TUNING_MIB + 1 }],
    ['parallel', { parallel: MAX_TUNING_PARALLEL + 1 }],
    ['host_reserve_gib', { host_reserve_gib: MAX_TUNING_HOST_RESERVE_GIB + 1 }],
    ['offload.layers', { offload: { mode: 'layers', layers: MAX_TUNING_LAYERS + 1 } }],
    ['context.tokens', { context: { mode: 'exact', tokens: MAX_CONTEXT_TOKENS + 1 } }],
    ['fit_min_context', { fit_min_context: MAX_CONTEXT_TOKENS + 1 }],
  ])('refuses %s above its ceiling with an actionable message', (key, patch) => {
    expect(() =>
      validateEmbeddedLLMTuningPatch(
        patch as Parameters<typeof validateEmbeddedLLMTuningPatch>[0],
      ),
    ).toThrow(new RegExp(`embedded_llm\\.tuning\\.${key.replace('.', '\\.')}`))
  })

  it('still refuses the floors', () => {
    expect(() => validateEmbeddedLLMTuningPatch({ parallel: MIN_PARALLEL - 1 })).toThrow(/parallel/)
    expect(() => validateEmbeddedLLMTuningPatch({ fit_target_mib: -1 })).toThrow(/fit_target_mib/)
    expect(() => validateEmbeddedLLMTuningPatch({ cache_ram_mib: -1 })).toThrow(/cache_ram_mib/)
    expect(() => validateEmbeddedLLMTuningPatch({ host_reserve_gib: -0.5 })).toThrow(
      /host_reserve_gib/,
    )
    expect(() =>
      validateEmbeddedLLMTuningPatch({ offload: { mode: 'layers', layers: -1 } }),
    ).toThrow(/offload\.layers/)
    expect(() =>
      validateEmbeddedLLMTuningPatch({ context: { mode: 'exact', tokens: MIN_CONTEXT_TOKENS - 1 } }),
    ).toThrow(/context\.tokens/)
  })
})

describe('validateEmbeddedLLMTuningPatch — non-finite values', () => {
  // NaN and Infinity fail EVERY relational comparison, so a bare
  // `min <= v <= max` test would wave them through. `host_reserve_gib` is the
  // sharp one: it reaches a Go float→int conversion whose result is
  // implementation-defined when the value does not fit.
  it.each([
    ['host_reserve_gib', NaN],
    ['host_reserve_gib', Infinity],
    ['host_reserve_gib', -Infinity],
    ['fit_target_mib', NaN],
    ['cache_ram_mib', Infinity],
    ['parallel', NaN],
    ['fit_min_context', NaN],
  ])('refuses %s = %s', (key, value) => {
    expect(() =>
      validateEmbeddedLLMTuningPatch({ [key]: value } as Parameters<typeof validateEmbeddedLLMTuningPatch>[0]),
    ).toThrow(new RegExp(String(key)))
  })

  it('refuses a non-finite count beside a count-bearing mode', () => {
    expect(() =>
      validateEmbeddedLLMTuningPatch({ offload: { mode: 'layers', layers: NaN } }),
    ).toThrow(/offload\.layers/)
    expect(() =>
      validateEmbeddedLLMTuningPatch({ context: { mode: 'exact', tokens: Infinity } }),
    ).toThrow(/context\.tokens/)
  })
})

describe('validateEmbeddedLLMTuningPatch — integrality of the `int` knobs', () => {
  // Six of the seven numeric knobs are `*int` on the wire; only
  // `host_reserve_gib` is a `*float64`. A decimal passes EVERY relational
  // comparison, so without an explicit integrality check it reaches Go and comes
  // back as the raw driver string — `json: cannot unmarshal number 2.5 into Go
  // struct field EmbeddedLLMTuningRequest.parallel of type int` — painted in the
  // Settings error line. That is exactly the round trip this module's contract
  // exists to avoid, and the message is not actionable for an operator.
  it.each([
    ['parallel', { parallel: 2.5 }],
    ['fit_target_mib', { fit_target_mib: 1024.5 }],
    ['fit_min_context', { fit_min_context: 4096.25 }],
    ['cache_ram_mib', { cache_ram_mib: 512.5 }],
    ['offload.layers', { offload: { mode: 'layers', layers: 24.5 } }],
    ['context.tokens', { context: { mode: 'exact', tokens: 32768.5 } }],
  ])('refuses a decimal %s', (key, patch) => {
    expect(() =>
      validateEmbeddedLLMTuningPatch(
        patch as Parameters<typeof validateEmbeddedLLMTuningPatch>[0],
      ),
    ).toThrow(new RegExp(`embedded_llm\\.tuning\\.${key.replace('.', '\\.')}`))
  })

  it('names the whole-number requirement, not only the range', () => {
    // The value is INSIDE the range — only its fraction is illegal — so the
    // message has to say so or the operator has nothing to act on.
    expect(() => validateEmbeddedLLMTuningPatch({ parallel: 2.5 })).toThrow(
      /embedded_llm\.tuning\.parallel 2\.5 is not valid; must be a whole number within 1-64/,
    )
  })

  it('still accepts a decimal `host_reserve_gib` — the one float knob', () => {
    expect(() => validateEmbeddedLLMTuningPatch({ host_reserve_gib: 6.5 })).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ host_reserve_gib: 0.25 })).not.toThrow()
  })

  it('still accepts a whole value for every `int` knob', () => {
    expect(() => validateEmbeddedLLMTuningPatch({ parallel: MIN_PARALLEL })).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ fit_target_mib: 1024 })).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ cache_ram_mib: 0 })).not.toThrow()
    expect(() =>
      validateEmbeddedLLMTuningPatch({ offload: { mode: 'layers', layers: 24 } }),
    ).not.toThrow()
    expect(() =>
      validateEmbeddedLLMTuningPatch({ context: { mode: 'exact', tokens: 32768 } }),
    ).not.toThrow()
    expect(() => validateEmbeddedLLMTuningPatch({ fit_min_context: MIN_CONTEXT_TOKENS })).not.toThrow()
  })
})
