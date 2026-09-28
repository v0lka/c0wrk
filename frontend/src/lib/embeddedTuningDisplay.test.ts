// @vitest-environment node
//
// lib/embeddedTuningDisplay — the pure display derivations of the embedded
// local model's tuning surface (React-free, so no renderer is needed).
//
// The two invariants that matter:
//   - a value that could not be committed back (absent, NaN, out of the range
//     the API guard accepts) never reaches a control: it falls back;
//   - an UNSET knob is its own value — `boolModeOf(null)` is 'auto', never a
//     fallback-derived ON that the planner's effective choice may contradict.

import { describe, it, expect } from 'vitest'
import {
  MAX_CONTEXT_TOKENS,
  MAX_TUNING_HOST_RESERVE_GIB,
  MAX_TUNING_LAYERS,
  MAX_TUNING_MIB,
  MAX_TUNING_PARALLEL,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
} from './embeddedTuningLimits'
import type { EmbeddedLLMPlan } from '@/api/embeddedTuning'
import {
  TRI_OFF,
  TRI_ON,
  TUNING_AUTO,
  TUNING_RANGES,
  boolModeOf,
  isExactContext,
  num,
  oneOf,
  plannedBoolArg,
  plannedContext,
  plannedFitArg,
  plannedLayers,
  seedCount,
  storedOffloadMode,
} from './embeddedTuningDisplay'

const PLAN: EmbeddedLLMPlan = {
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

describe('num — an unpaintable snapshot value falls back', () => {
  it('keeps an in-range value', () => {
    expect(num(2048, 0, MAX_TUNING_MIB, 1024)).toBe(2048)
    expect(num(0, 0, MAX_TUNING_MIB, 1024)).toBe(0)
    expect(num(MAX_TUNING_MIB, 0, MAX_TUNING_MIB, 1024)).toBe(MAX_TUNING_MIB)
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['below the floor', -1],
    ['above the ceiling', MAX_TUNING_MIB + 1],
    ['NaN', Number.NaN],
    ['Infinity', Number.POSITIVE_INFINITY],
    ['-Infinity', Number.NEGATIVE_INFINITY],
  ])('falls back for %s', (_label, value) => {
    expect(num(value as number | null | undefined, 0, MAX_TUNING_MIB, 1024)).toBe(1024)
  })
})

describe('oneOf — a string knob outside its closed set is Auto', () => {
  const set = ['PQ2_0', 'PTQ1_0']
  it('keeps a member', () => {
    expect(oneOf('PQ2_0', set)).toBe('PQ2_0')
  })
  it.each([
    ['an unknown spelling', 'q5_0'],
    ['null', null],
    ['undefined', undefined],
    ['the empty string', ''],
  ])('falls back to Auto for %s', (_label, value) => {
    expect(oneOf(value as string | null | undefined, set)).toBe(TUNING_AUTO)
  })
})

describe('seedCount — stored, then the recorded plan, then the default', () => {
  it('prefers a paintable stored override', () => {
    expect(seedCount(24, 42, 0, MAX_TUNING_LAYERS, 0)).toBe(24)
  })

  it('takes the recorded plan when the stored value is unpaintable', () => {
    // The -1 sentinel means "the flag was omitted", and null means "unset":
    // neither is a count, so the plan's own figure seeds the field.
    expect(seedCount(null, 42, 0, MAX_TUNING_LAYERS, 0)).toBe(42)
    expect(seedCount(-1, 42, 0, MAX_TUNING_LAYERS, 0)).toBe(42)
    expect(seedCount(Number.NaN, 42, 0, MAX_TUNING_LAYERS, 0)).toBe(42)
  })

  it('falls back when neither is paintable', () => {
    expect(seedCount(null, null, 0, MAX_TUNING_LAYERS, 0)).toBe(0)
    expect(seedCount(-1, -1, 0, MAX_TUNING_LAYERS, 0)).toBe(0)
    expect(seedCount(null, MAX_TUNING_LAYERS + 1, 0, MAX_TUNING_LAYERS, 0)).toBe(0)
  })
})

describe('the mode normalizers', () => {
  it('keeps the three stored offload spellings', () => {
    expect(storedOffloadMode('all')).toBe('all')
    expect(storedOffloadMode('cpu')).toBe('cpu')
    expect(storedOffloadMode('layers')).toBe('layers')
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['an unknown spelling', 'half'],
  ])('normalizes %s to Auto', (_label, value) => {
    expect(storedOffloadMode(value as string | null | undefined)).toBe(TUNING_AUTO)
  })

  it('recognizes the exact-context mode only by its own spelling', () => {
    expect(isExactContext('exact')).toBe(true)
    expect(isExactContext('auto')).toBe(false)
    expect(isExactContext(null)).toBe(false)
    expect(isExactContext(undefined)).toBe(false)
  })
})

describe('boolModeOf — the tri-state, never a fallback boolean', () => {
  it('maps an explicit override to its own spelling', () => {
    expect(boolModeOf(true)).toBe(TRI_ON)
    expect(boolModeOf(false)).toBe(TRI_OFF)
  })

  it('maps an unset knob to Auto', () => {
    expect(boolModeOf(null)).toBe(TUNING_AUTO)
    expect(boolModeOf(undefined)).toBe(TUNING_AUTO)
  })

  it('uses the spellings the recorded plan quotes, so Auto is checkable', () => {
    expect(TRI_ON).toBe('on')
    expect(TRI_OFF).toBe('off')
  })
})

// The rule `boolModeOf` exists for, on the two knobs that used to render as
// switches: `null` genuinely resolves to OFF on a machine whose accelerator
// cannot hold the KV cache / the projector reserve, and the plan reports that
// resolved value — so an unset knob must say Auto and quote the plan, never
// paint a fallback-derived ON the resident server contradicts.
describe('plannedBoolArg — quoting the plan beside an unset boolean knob', () => {
  it('quotes nothing while no plan was recorded', () => {
    expect(plannedBoolArg(PLAN, 'kv_offload')).toBeNull()
    expect(plannedBoolArg(null, 'kv_offload')).toBeNull()
    expect(plannedBoolArg(undefined, 'mmproj_offload')).toBeNull()
  })

  it('names the flag the runtime was launched with when the plan spilled to host', () => {
    const recorded = { ...PLAN, recorded: true, kv_offload: false, mmproj_offload: false }
    expect(plannedBoolArg(recorded, 'kv_offload')).toBe('in system RAM (-nkvo)')
    expect(plannedBoolArg(recorded, 'mmproj_offload')).toBe(
      'in system RAM (--no-mmproj-offload)',
    )
  })

  it('says "on device" — not a positive flag the server never emits — when the plan kept it', () => {
    const recorded = { ...PLAN, recorded: true, kv_offload: true, mmproj_offload: true }
    expect(plannedBoolArg(recorded, 'kv_offload')).toBe('on device')
    expect(plannedBoolArg(recorded, 'mmproj_offload')).toBe('on device')
  })

  it('quotes each knob from its OWN plan field', () => {
    const recorded = { ...PLAN, recorded: true, kv_offload: false, mmproj_offload: true }
    expect(plannedBoolArg(recorded, 'kv_offload')).toBe('in system RAM (-nkvo)')
    expect(plannedBoolArg(recorded, 'mmproj_offload')).toBe('on device')
  })
})

describe('the recorded-plan readers', () => {
  it('quote nothing while no plan was recorded', () => {
    expect(plannedFitArg(PLAN)).toBeNull()
    expect(plannedFitArg(null)).toBeNull()
    expect(plannedFitArg(undefined)).toBeNull()
    expect(plannedContext(PLAN)).toBeNull()
    expect(plannedLayers(PLAN)).toBeNull()
  })

  it('quote the recorded -fit value, context and layer count', () => {
    const recorded = { ...PLAN, recorded: true, fit_arg: 'off', context_size: 131072, layers: 30 }
    expect(plannedFitArg(recorded)).toBe('off')
    expect(plannedContext(recorded)).toBe(131072)
    expect(plannedLayers(recorded)).toBe(30)
    expect(plannedFitArg({ ...recorded, fit_arg: 'on' })).toBe(TRI_ON)
  })

  it('quote no -fit value for a spelling outside on/off', () => {
    expect(plannedFitArg({ ...PLAN, recorded: true, fit_arg: 'maybe' })).toBeNull()
  })

  it('quote no layer count for the omitted-flag sentinel (-1)', () => {
    // -1 is the plan's "flag omitted" sentinel: it is NOT a layer count, so the
    // helper drops it rather than passing it through — a caller must never be
    // able to paint "-1 layers" for a fit-sized launch. seedCount then falls
    // back exactly as it did when the sentinel reached it.
    expect(plannedLayers({ ...PLAN, recorded: true, layers: -1 })).toBeNull()
    expect(seedCount(null, plannedLayers({ ...PLAN, recorded: true, layers: -1 }), 0, MAX_TUNING_LAYERS, 0)).toBe(0)
  })

  it('quote a real recorded layer count, including 0', () => {
    expect(plannedLayers({ ...PLAN, recorded: true, layers: 0 })).toBe(0)
    expect(plannedLayers({ ...PLAN, recorded: true, layers: 42 })).toBe(42)
  })
})

describe('TUNING_RANGES — one source for the input bounds and the fallbacks', () => {
  it('mirrors the API guard bounds exactly', () => {
    expect(TUNING_RANGES.context_tokens).toEqual({ min: MIN_CONTEXT_TOKENS, max: MAX_CONTEXT_TOKENS })
    expect(TUNING_RANGES.fit_min_context).toEqual({ min: MIN_CONTEXT_TOKENS, max: MAX_CONTEXT_TOKENS })
    expect(TUNING_RANGES.offload_layers).toEqual({ min: 0, max: MAX_TUNING_LAYERS })
    expect(TUNING_RANGES.fit_target_mib).toEqual({ min: 0, max: MAX_TUNING_MIB })
    expect(TUNING_RANGES.cache_ram_mib).toEqual({ min: 0, max: MAX_TUNING_MIB })
    expect(TUNING_RANGES.parallel).toEqual({ min: MIN_PARALLEL, max: MAX_TUNING_PARALLEL })
    expect(TUNING_RANGES.host_reserve_gib).toEqual({ min: 0, max: MAX_TUNING_HOST_RESERVE_GIB })
  })

  it('never offers a range whose floor is above its ceiling', () => {
    for (const range of Object.values(TUNING_RANGES)) {
      expect(range.min).toBeLessThanOrEqual(range.max)
    }
  })
})
