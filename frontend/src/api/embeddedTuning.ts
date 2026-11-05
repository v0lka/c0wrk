// Embedded local-model TUNING RPC wrappers (the Settings tuning surface).
//
// Thin, validating wrappers over the desktop App bindings of
// backend/frontend_api_embedded_tuning.go: GetEmbeddedLLMTuning /
// SetEmbeddedLLMTuning, plus the TS mirror of the status snapshot's
// measured-topology additions (devices / unified / budgets / plan /
// reload_required) whose DTOs live in backend/frontend_api_embedded_dto.go.
// Sibling of @/api/embedded — split out so each module stays at one concern
// (lifecycle vs tuning); components import from either, never from wailsjs.
//
// The on-demand `ProbeEmbeddedLLMDevices` RPC is wrapped below (see
// probeEmbeddedLLMDevices) beside the live-snapshot type: the Settings block's
// device-memory panel measures THIS instant with it, and the header comment's
// old "deliberately not wrapped" rule expired the moment that surface existed.
//
// Every knob is NULLABLE and null is load-bearing: it means "unset — the
// planner decides", which is NOT the same value as an explicit "auto" spelling
// (the persisted section keeps pointers for exactly that distinction).
// SetEmbeddedLLMTuning is a PARTIAL patch: a field absent from the request
// keeps the stored value, a present field replaces it verbatim, and a knob
// named in `reset` clears back to unset — so a commit from this UI names only
// the knob it changed.
//
// The numeric bounds mirror the backend one for one — the floors come from
// config.ToTuning's own validation, the CEILINGS from core/embeddedllm/limits.go
// — and live in lib/embeddedTuningLimits, ONE copy shared by the local range
// check below, the display fallbacks and the inputs' own `min`/`max`. So the
// wrapper refuses a guaranteed rejection locally instead of paying a round trip,
// the same contract as setEmbeddedLLMAutoUnload.

import { getApp } from './runtime'
import {
  MAX_CONTEXT_TOKENS,
  MAX_TUNING_DEVICES,
  MAX_TUNING_HOST_RESERVE_GIB,
  MAX_TUNING_LAYERS,
  MAX_TUNING_MIB,
  MAX_TUNING_PARALLEL,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
} from '@/lib/embeddedTuningLimits'
import { logger } from '@/lib/logger'

/** The closed KV-precision set (core KVTypes; q5_0 is excluded on a measured
 *  long-context decode regression). "auto" is the planner's adaptive
 *  escalation, not a member of the set. */
export const KV_CACHE_TYPES = ['f16', 'q8_0', 'q4_0'] as const

/** The closed packing-override set (core SupportedPackings). */
export const TUNING_PACKINGS = ['PQ2_0', 'PTQ1_0'] as const

/** The closed `-sm`/`--split-mode` set (core SplitModes). An explicit "auto"
 *  is ALSO a real wire value — backend tuningChoice stores it verbatim and
 *  distinguishes it from unset — but it is not a set member: the patch guard
 *  below accepts it (and any case/whitespace spelling of these four) the way
 *  tuningChoice does, by trimming and case-folding before the match. */
export const SPLIT_MODES = ['none', 'layer', 'row', 'tensor'] as const

/** The mode spellings of the two composite knobs (config's YAML vocabulary). */
export const CONTEXT_MODE_EXACT = 'exact'
export const OFFLOAD_MODE_ALL = 'all'
export const OFFLOAD_MODE_CPU = 'cpu'
export const OFFLOAD_MODE_LAYERS = 'layers'

/** One resettable knob, spelled exactly as the `embedded_llm.tuning` YAML key
 *  the backend's `reset` vocabulary accepts. */
export type EmbeddedLLMTuningKnob =
  | 'context'
  | 'kv_cache_type'
  | 'offload'
  | 'fit'
  | 'fit_target_mib'
  | 'fit_min_context'
  | 'kv_offload'
  | 'mmproj_offload'
  | 'packing'
  | 'parallel'
  | 'cache_ram_mib'
  | 'host_reserve_gib'
  | 'devices'
  | 'split_mode'

// --- Types (mirrors of the backend DTOs; null = unset) ---

/** One measured accelerator of the machine's topology. */
export interface EmbeddedLLMDevice {
  readonly name: string
  readonly description: string
  readonly total_mib: number
  readonly free_mib: number
}

/** The launch shape the planner last recorded (the EFFECTIVE plan, not the
 *  override). Sentinel conventions: `layers` -1 = flag omitted, `cache_ram_mib`
 *  -1 = flag omitted (0 = cache disabled), `notes` and `devices` are ALWAYS
 *  arrays (`split_mode` "" = the flag is omitted). */
export interface EmbeddedLLMPlan {
  readonly recorded: boolean
  readonly packing: string
  readonly kv_type: string
  readonly context_size: number
  readonly fit: boolean
  readonly fit_arg: string
  readonly fit_target_mib: number
  readonly fit_min_context: number
  readonly offload_mode: string
  readonly layers: number
  readonly kv_offload: boolean
  readonly mmproj_offload: boolean
  readonly parallel: number
  readonly cache_ram_mib: number
  /** The `-dev` list this plan pins; empty = the runtime chooses the devices.
   *  Non-empty means the device budget prices ONLY the named pool; the host
   *  budget stays machine-wide. */
  readonly devices: readonly string[]
  /** The `-sm` spelling the plan pins; "" = the flag is omitted. */
  readonly split_mode: string
  readonly gpu_family: string
  readonly device_budget_mib: number
  readonly host_budget_mib: number
  readonly expected_device_mib: number
  readonly expected_host_mib: number
  readonly notes: readonly string[]
}

/** The measured-topology / effective-plan fields GetEmbeddedLLMStatus carries
 *  in addition to the legacy snapshot (mirrors the T10 additive block; the
 *  backend emits them with no omitempty, so they are always present). */
export interface EmbeddedLLMStatusExtras {
  /** Measured accelerators; empty when no probe ever answered. */
  readonly devices: readonly EmbeddedLLMDevice[]
  /** True on unified-memory machines (Apple Silicon): the two budgets share
   *  one pool and must not be summed. */
  readonly unified: boolean
  readonly host_ram_gib: number
  readonly device_budget_mib: number
  readonly host_budget_mib: number
  /** RFC 3339 stamp of the last probe that answered; "" = none ever did (the
   *  only way to tell "CPU-only" from "unknown"). */
  readonly topology_probed_at: string
  readonly plan: EmbeddedLLMPlan
  /** The resident model's argv predates the live tuning — Unload + Load is
   *  the explicit apply path (no restart-on-slider, ADR-066 D13). */
  readonly reload_required: boolean
}

/** The persisted override surface (GetEmbeddedLLMTuning), field for field.
 *  `devices` is null-or-array (nil and empty both read as unset — a cleared
 *  list and an authored-empty one are the same statement), `split_mode` is
 *  null-or-spelling. */
export interface EmbeddedLLMTuning {
  readonly context: { readonly mode: string | null; readonly tokens: number | null }
  readonly kv_cache_type: string | null
  readonly offload: { readonly mode: string | null; readonly layers: number | null }
  readonly fit: boolean | null
  readonly fit_target_mib: number | null
  readonly fit_min_context: number | null
  readonly kv_offload: boolean | null
  readonly mmproj_offload: boolean | null
  readonly packing: string | null
  readonly parallel: number | null
  readonly cache_ram_mib: number | null
  readonly host_reserve_gib: number | null
  readonly devices: readonly string[] | null
  readonly split_mode: string | null
}

/** A partial tuning patch (SetEmbeddedLLMTuning). Absent = keep the stored
 *  value; present = replace verbatim (an EMPTY `devices` array clears the
 *  list); a knob in `reset` = clear to unset. */
export interface EmbeddedLLMTuningPatch {
  readonly reset?: readonly EmbeddedLLMTuningKnob[]
  readonly context?: { mode: string | null; tokens: number | null }
  readonly kv_cache_type?: string
  readonly offload?: { mode: string | null; layers: number | null }
  readonly fit?: boolean
  readonly fit_target_mib?: number
  readonly fit_min_context?: number
  readonly kv_offload?: boolean
  readonly mmproj_offload?: boolean
  readonly packing?: string
  readonly parallel?: number
  readonly cache_ram_mib?: number
  readonly host_reserve_gib?: number
  readonly devices?: readonly string[]
  readonly split_mode?: string
}

// --- Guards (presence + type only; null and "" stay legal values) ---

const isNullable = (v: unknown, is: (x: unknown) => boolean) => v === null || is(v)
const isStr = (v: unknown): v is string => typeof v === 'string'
const isNum = (v: unknown): v is number => typeof v === 'number'

/** Inclusive numeric range check. `Number.isFinite` is part of the check on
 *  purpose: NaN and Infinity fail EVERY relational comparison, so a bare
 *  `min <= v <= max` test would wave them through and hand the planner a value
 *  whose float→int conversion is implementation-defined in Go. */
const withinRange = (v: number, min: number, max: number): boolean =>
  Number.isFinite(v) && v >= min && v <= max

/** Inclusive range check for a knob whose wire type is an `int`. Every tuning
 *  knob but `host_reserve_gib` is one. `Number.isInteger` subsumes the finite
 *  check (NaN and ±Infinity are not integers) and adds the part a decimal needs:
 *  `2.5` passes every relational comparison, reaches Go, and comes back as a raw
 *  `json: cannot unmarshal number 2.5 into Go struct field … of type int` painted
 *  in the Settings error line — the round trip this module exists to avoid. */
const wholeInRange = (v: number, min: number, max: number): boolean =>
  Number.isInteger(v) && v >= min && v <= max

function isDevice(d: unknown): d is EmbeddedLLMDevice {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return isStr(o.name) && isStr(o.description) && isNum(o.total_mib) && isNum(o.free_mib)
}

/** Guard for the recorded launch shape. Every field `formatPlan` (the install
 *  record) and the tuning surfaces read is checked, so a backend schema drift
 *  fails the read loudly instead of painting "undefined ctx / undefined device".
 *  Presence and types only — the VALUES (`packing`, `kv_type`, `offload_mode`)
 *  are deliberately not enumerated, and the sentinel spellings (`layers` -1,
 *  `cache_ram_mib` -1) stay legal. */
export function isEmbeddedLLMPlan(d: unknown): d is EmbeddedLLMPlan {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return (
    typeof o.recorded === 'boolean' &&
    isStr(o.packing) &&
    isStr(o.kv_type) &&
    isNum(o.context_size) &&
    typeof o.fit === 'boolean' &&
    isStr(o.fit_arg) &&
    isNum(o.fit_target_mib) &&
    isNum(o.fit_min_context) &&
    isStr(o.offload_mode) &&
    isNum(o.layers) &&
    typeof o.kv_offload === 'boolean' &&
    typeof o.mmproj_offload === 'boolean' &&
    isNum(o.parallel) &&
    isNum(o.cache_ram_mib) &&
    Array.isArray(o.devices) &&
    o.devices.every(isStr) &&
    isStr(o.split_mode) &&
    isStr(o.gpu_family) &&
    isNum(o.device_budget_mib) &&
    isNum(o.host_budget_mib) &&
    isNum(o.expected_device_mib) &&
    isNum(o.expected_host_mib) &&
    Array.isArray(o.notes) &&
    o.notes.every(isStr)
  )
}

/** Guard for the measured-topology block of the status snapshot. */
export function isEmbeddedLLMStatusExtras(d: unknown): d is EmbeddedLLMStatusExtras {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return (
    Array.isArray(o.devices) &&
    o.devices.every(isDevice) &&
    typeof o.unified === 'boolean' &&
    isNum(o.host_ram_gib) &&
    isNum(o.device_budget_mib) &&
    isNum(o.host_budget_mib) &&
    isStr(o.topology_probed_at) &&
    isEmbeddedLLMPlan(o.plan) &&
    typeof o.reload_required === 'boolean'
  )
}

export function isEmbeddedLLMTuning(d: unknown): d is EmbeddedLLMTuning {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  const ctx = o.context as Record<string, unknown> | null | undefined
  const off = o.offload as Record<string, unknown> | null | undefined
  return (
    typeof ctx === 'object' && ctx !== null && isNullable(ctx.mode, isStr) && isNullable(ctx.tokens, isNum) &&
    isNullable(o.kv_cache_type, isStr) &&
    typeof off === 'object' && off !== null && isNullable(off.mode, isStr) && isNullable(off.layers, isNum) &&
    isNullable(o.fit, (x) => typeof x === 'boolean') &&
    isNullable(o.fit_target_mib, isNum) &&
    isNullable(o.fit_min_context, isNum) &&
    isNullable(o.kv_offload, (x) => typeof x === 'boolean') &&
    isNullable(o.mmproj_offload, (x) => typeof x === 'boolean') &&
    isNullable(o.packing, isStr) &&
    isNullable(o.parallel, isNum) &&
    isNullable(o.cache_ram_mib, isNum) &&
    isNullable(o.host_reserve_gib, isNum) &&
    (o.devices === null || (Array.isArray(o.devices) && o.devices.every(isStr))) &&
    isNullable(o.split_mode, isStr)
  )
}

// --- Wrappers ---

/** Read the persisted overrides. Errors only before startup / on drift. */
export async function getEmbeddedLLMTuning(): Promise<EmbeddedLLMTuning> {
  const result = await getApp().GetEmbeddedLLMTuning()
  if (!isEmbeddedLLMTuning(result)) {
    logger.error('getEmbeddedLLMTuning: unexpected response shape', result)
    throw new Error('GetEmbeddedLLMTuning returned an invalid tuning payload')
  }
  return result
}

/** Local range check of one patch — the mirror of config.ToTuning's floors and
 *  core/embeddedllm/limits.go's ceilings, so an out-of-range commit is refused
 *  WITHOUT a round trip. Every knob but `host_reserve_gib` is an `int` on the
 *  wire, so those are also checked for INTEGRALITY: a decimal is a guaranteed
 *  Go unmarshal rejection, and the raw driver string is not an actionable
 *  message for the Settings error line. */
export function validateEmbeddedLLMTuningPatch(patch: EmbeddedLLMTuningPatch): void {
  const bad = (key: string, value: number | string, want: string): Error =>
    new Error(`embedded_llm.tuning.${key} ${value} is not valid; must be ${want}`)
  const whole = (min: number, max: number): string => `a whole number within ${min}-${max}`
  if (patch.context?.mode === CONTEXT_MODE_EXACT) {
    const tokens = patch.context.tokens
    if (tokens === null || tokens === undefined)
      throw bad('context.tokens', 'null', `paired with ${CONTEXT_MODE_EXACT}`)
    if (!wholeInRange(tokens, MIN_CONTEXT_TOKENS, MAX_CONTEXT_TOKENS))
      throw bad('context.tokens', tokens, whole(MIN_CONTEXT_TOKENS, MAX_CONTEXT_TOKENS))
  }
  if (patch.offload?.mode === OFFLOAD_MODE_LAYERS) {
    const layers = patch.offload.layers
    if (layers === null || layers === undefined || !wholeInRange(layers, 0, MAX_TUNING_LAYERS))
      throw bad('offload.layers', String(layers), `${whole(0, MAX_TUNING_LAYERS)} beside mode "layers"`)
  }
  if (patch.fit_target_mib !== undefined && !wholeInRange(patch.fit_target_mib, 0, MAX_TUNING_MIB))
    throw bad('fit_target_mib', patch.fit_target_mib, whole(0, MAX_TUNING_MIB))
  if (
    patch.fit_min_context !== undefined &&
    !wholeInRange(patch.fit_min_context, MIN_CONTEXT_TOKENS, MAX_CONTEXT_TOKENS)
  )
    throw bad('fit_min_context', patch.fit_min_context, whole(MIN_CONTEXT_TOKENS, MAX_CONTEXT_TOKENS))
  if (patch.parallel !== undefined && !wholeInRange(patch.parallel, MIN_PARALLEL, MAX_TUNING_PARALLEL))
    throw bad('parallel', patch.parallel, whole(MIN_PARALLEL, MAX_TUNING_PARALLEL))
  if (patch.cache_ram_mib !== undefined && !wholeInRange(patch.cache_ram_mib, 0, MAX_TUNING_MIB))
    throw bad('cache_ram_mib', patch.cache_ram_mib, `${whole(0, MAX_TUNING_MIB)} (0 disables the cache)`)
  if (
    patch.host_reserve_gib !== undefined &&
    !withinRange(patch.host_reserve_gib, 0, MAX_TUNING_HOST_RESERVE_GIB)
  )
    throw bad('host_reserve_gib', patch.host_reserve_gib, `within 0-${MAX_TUNING_HOST_RESERVE_GIB}`)
  // devices: the same fast-path shape rules the backend's ToTuning applies —
  // bounded count, one separator-free non-empty token per entry, no
  // duplicates. Whether a name names a device THIS machine has stays the
  // backend's plan-time inventory check (an unknown name is refused by the
  // LOAD, not here — the UI cannot know the machine's inventory spelling).
  if (patch.devices !== undefined) {
    // A runtime null is out of contract for `devices?` (reset goes through
    // the reset knob list; an empty array clears the list) — refuse it like
    // split_mode refuses a non-string, never a TypeError on `.length`.
    if (patch.devices === null)
      throw bad('devices', 'null', 'a list of device names (an empty array clears the list)')
    if (patch.devices.length > MAX_TUNING_DEVICES)
      throw bad(
        'devices',
        `${patch.devices.length} entries`,
        `at most ${MAX_TUNING_DEVICES} entries`,
      )
    const seen = new Set<string>()
    for (const raw of patch.devices) {
      const name = raw.trim()
      if (name === '')
        throw bad('devices', JSON.stringify(raw), 'a non-empty device name (CUDA0, Vulkan0, ...)')
      if (/[\s,]/.test(name) || name.startsWith('-'))
        throw bad('devices', JSON.stringify(raw), 'a single separator-free device token')
      if (seen.has(name)) throw bad('devices', name, 'listed exactly once')
      seen.add(name)
    }
  }
  // split_mode mirrors the backend's tuningChoice — trim, case-fold, and
  // forward the request verbatim (the backend canonicalizes, it never
  // round-trips a rewritten spelling): "" / "auto" are the explicit-auto
  // statement (a real value, distinct from unset), the members match in any
  // spelling, and any other value is refused here exactly as tuningChoice
  // refuses it there. A non-string reaches the refusal too, never a TypeError.
  const sm =
    typeof patch.split_mode === 'string'
      ? patch.split_mode.trim().toLowerCase()
      : patch.split_mode
  if (
    patch.split_mode !== undefined &&
    sm !== '' &&
    sm !== 'auto' &&
    !SPLIT_MODES.includes(sm as never)
  )
    throw bad(
      'split_mode',
      JSON.stringify(patch.split_mode),
      `one of ${SPLIT_MODES.join(', ')}, or "auto"`,
    )
}

/** Persist a partial tuning patch. Out-of-range values are refused locally
 *  (the backend refuses them too) without touching the config. The write runs
 *  the shared save tail and ends with one `embedded_llm:state` snapshot, so
 *  the caller re-reads BOTH the tuning and the status afterwards. */
export async function setEmbeddedLLMTuning(patch: EmbeddedLLMTuningPatch): Promise<void> {
  validateEmbeddedLLMTuningPatch(patch)
  await getApp().SetEmbeddedLLMTuning(patch)
}

// --- Live device-memory snapshot ---

/** A measurement of THIS instant: the on-demand ProbeEmbeddedLLMDevices
 *  answer. Field for field the topology half of EmbeddedLLMStatusExtras, but a
 *  value in its own right — the recorded snapshot ages (another process takes
 *  VRAM, a driver updates), and this is what "how much is free right now"
 *  means. */
export interface EmbeddedLLMDeviceSnapshot {
  /** The accelerator inventory in the runtime's own print order. An EMPTY
   *  array is a real answer from a machine with no usable accelerator, not a
   *  failed probe — a failed probe rejects the promise instead. */
  readonly devices: readonly EmbeddedLLMDevice[]
  /** True when the device pool aliases host RAM (the budgets share one pool
   *  and must never be summed). */
  readonly unified: boolean
  readonly host_ram_gib: number
  readonly device_budget_mib: number
  readonly host_budget_mib: number
  /** RFC 3339 stamp of THIS probe (never ""). */
  readonly probed_at: string
}

/** Guard for a ProbeEmbeddedLLMDevices answer. Mirrors the backend DTO's
 *  always-present contract: a payload missing a field is schema drift, not a
 *  machine with no memory. */
export function isEmbeddedLLMDeviceSnapshot(d: unknown): d is EmbeddedLLMDeviceSnapshot {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  const isDevice = (v: unknown): v is EmbeddedLLMDevice =>
    typeof v === 'object' &&
    v !== null &&
    typeof (v as Record<string, unknown>).name === 'string' &&
    typeof (v as Record<string, unknown>).description === 'string' &&
    typeof (v as Record<string, unknown>).total_mib === 'number' &&
    typeof (v as Record<string, unknown>).free_mib === 'number'
  return (
    Array.isArray(o.devices) &&
    o.devices.every(isDevice) &&
    typeof o.unified === 'boolean' &&
    typeof o.host_ram_gib === 'number' &&
    typeof o.device_budget_mib === 'number' &&
    typeof o.host_budget_mib === 'number' &&
    typeof o.probed_at === 'string'
  )
}

/** Measure THIS machine's accelerator memory now: the on-demand
 *  ProbeEmbeddedLLMDevices RPC. Unlike every other embedded-LLM read this one
 *  is allowed to be slow (it spawns the provisioned runtime with
 *  --list-devices) and to fail — an unanswered probe rejects with the
 *  operator-facing cause instead of degrading to an empty inventory. Refused
 *  while an install/removal owns the embedded-operation gate, and before any
 *  install exists. */
export async function probeEmbeddedLLMDevices(): Promise<EmbeddedLLMDeviceSnapshot> {
  const result = await getApp().ProbeEmbeddedLLMDevices()
  if (!isEmbeddedLLMDeviceSnapshot(result)) {
    logger.error('probeEmbeddedLLMDevices: unexpected response shape', result)
    throw new Error('ProbeEmbeddedLLMDevices returned an invalid device snapshot')
  }
  return result
}
