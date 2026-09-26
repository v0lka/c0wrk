// Embedded local-model TUNING RPC wrappers (the Settings tuning surface).
//
// Thin, validating wrappers over the desktop App bindings of
// backend/frontend_api_embedded.go: GetEmbeddedLLMTuning / SetEmbeddedLLMTuning
// / ProbeEmbeddedLLMDevices, plus the TS mirror of the status snapshot's
// measured-topology additions (devices / unified / budgets / plan /
// reload_required). Sibling of @/api/embedded — split out so each module stays
// at one concern (lifecycle vs tuning); components import from either, never
// from wailsjs.
//
// Every knob is NULLABLE and null is load-bearing: it means "unset — the
// planner decides", which is NOT the same value as an explicit "auto" spelling
// (the persisted section keeps pointers for exactly that distinction).
// SetEmbeddedLLMTuning is a PARTIAL patch: a field absent from the request
// keeps the stored value, a present field replaces it verbatim, and a knob
// named in `reset` clears back to unset — so a commit from this UI names only
// the knob it changed.
//
// The numeric bounds mirror config.ToTuning one for one (the backend's own
// validation) so the wrapper refuses a guaranteed rejection locally instead of
// paying a round trip — the same contract as setEmbeddedLLMAutoUnload.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

// --- Validation constants (mirror backend/config ToTuning ranges) ---

/** Smallest legal explicit context / fit floor. There is no zero-token
 *  context window. Mirrors config.EmbeddedLLMMinContextTokens. */
export const MIN_CONTEXT_TOKENS = 1

/** The pinned model's own training context — the ceiling for every
 *  context-shaped knob (core/embeddedllm `maxTrainingContext`, 1<<18). A pin
 *  bump changes the backend figure, not this mirror; the wrapper's refusal is
 *  only a fast path, the backend stays authoritative. */
export const MAX_CONTEXT_TOKENS = 262144

/** The `-np` default and floor (core DefaultParallel: one slot, because -np
 *  also SPLITS the context across slots). */
export const MIN_PARALLEL = 1
export const DEFAULT_PARALLEL = 1

/** The fit-floor default when unset (core DefaultFitMinContext — deliberately
 *  NOT the runtime's own 4096, which truncates answers on this model). */
export const DEFAULT_FIT_MIN_CONTEXT = 65536

/** The per-device fit margin the runtime itself uses when unset. */
export const DEFAULT_FIT_TARGET_MIB = 1024

/** The closed KV-precision set (core KVTypes; q5_0 is excluded on a measured
 *  long-context decode regression). "auto" is the planner's adaptive
 *  escalation, not a member of the set. */
export const KV_CACHE_TYPES = ['f16', 'q8_0', 'q4_0'] as const

/** The closed packing-override set (core SupportedPackings). */
export const TUNING_PACKINGS = ['PQ2_0', 'PTQ1_0'] as const

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
 *  -1 = flag omitted (0 = cache disabled), `notes` is ALWAYS an array. */
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

/** The persisted override surface (GetEmbeddedLLMTuning), field for field. */
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
}

/** A partial tuning patch (SetEmbeddedLLMTuning). Absent = keep the stored
 *  value; present = replace verbatim; a knob in `reset` = clear to unset. */
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
}

/** The on-demand probe answer (ProbeEmbeddedLLMDevices). */
export interface EmbeddedLLMDevices {
  readonly devices: readonly EmbeddedLLMDevice[]
  readonly unified: boolean
  readonly host_ram_gib: number
  readonly device_budget_mib: number
  readonly host_budget_mib: number
  readonly probed_at: string
}

// --- Guards (presence + type only; null and "" stay legal values) ---

const isNullable = (v: unknown, is: (x: unknown) => boolean) => v === null || is(v)
const isStr = (v: unknown): v is string => typeof v === 'string'
const isNum = (v: unknown): v is number => typeof v === 'number'

function isDevice(d: unknown): d is EmbeddedLLMDevice {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return isStr(o.name) && isStr(o.description) && isNum(o.total_mib) && isNum(o.free_mib)
}

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
    typeof o.plan === 'object' &&
    o.plan !== null &&
    Array.isArray((o.plan as Record<string, unknown>).notes) &&
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
    isNullable(o.host_reserve_gib, isNum)
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

/** Local range check of one patch — the mirror of config.ToTuning's numeric
 *  bounds, so an out-of-range commit is refused WITHOUT a round trip. */
export function validateEmbeddedLLMTuningPatch(patch: EmbeddedLLMTuningPatch): void {
  const bad = (key: string, value: number | string, want: string): Error =>
    new Error(`embedded_llm.tuning.${key} ${value} is not valid; must be ${want}`)
  if (patch.context?.mode === CONTEXT_MODE_EXACT) {
    const tokens = patch.context.tokens
    if (tokens === null || tokens === undefined)
      throw bad('context.tokens', 'null', `paired with ${CONTEXT_MODE_EXACT}`)
    if (tokens < MIN_CONTEXT_TOKENS || tokens > MAX_CONTEXT_TOKENS)
      throw bad('context.tokens', tokens, `within ${MIN_CONTEXT_TOKENS}-${MAX_CONTEXT_TOKENS}`)
  }
  if (patch.offload?.mode === OFFLOAD_MODE_LAYERS) {
    const layers = patch.offload.layers
    if (layers === null || layers === undefined || layers < 0)
      throw bad('offload.layers', String(layers), '>= 0 beside mode "layers"')
  }
  if (patch.fit_target_mib !== undefined && patch.fit_target_mib < 0)
    throw bad('fit_target_mib', patch.fit_target_mib, '>= 0')
  if (patch.fit_min_context !== undefined && (patch.fit_min_context < MIN_CONTEXT_TOKENS || patch.fit_min_context > MAX_CONTEXT_TOKENS))
    throw bad('fit_min_context', patch.fit_min_context, `within ${MIN_CONTEXT_TOKENS}-${MAX_CONTEXT_TOKENS}`)
  if (patch.parallel !== undefined && patch.parallel < MIN_PARALLEL)
    throw bad('parallel', patch.parallel, `>= ${MIN_PARALLEL}`)
  if (patch.cache_ram_mib !== undefined && patch.cache_ram_mib < 0)
    throw bad('cache_ram_mib', patch.cache_ram_mib, '>= 0 (0 disables the cache)')
  if (patch.host_reserve_gib !== undefined && patch.host_reserve_gib < 0)
    throw bad('host_reserve_gib', patch.host_reserve_gib, '>= 0')
}

/** Persist a partial tuning patch. Out-of-range values are refused locally
 *  (the backend refuses them too) without touching the config. The write runs
 *  the shared save tail and ends with one `embedded_llm:state` snapshot, so
 *  the caller re-reads BOTH the tuning and the status afterwards. */
export async function setEmbeddedLLMTuning(patch: EmbeddedLLMTuningPatch): Promise<void> {
  validateEmbeddedLLMTuningPatch(patch)
  await getApp().SetEmbeddedLLMTuning(patch)
}

/** On-demand topology re-probe. Refused when not installed / while an install
 *  runs; persists nothing. */
export async function probeEmbeddedLLMDevices(): Promise<EmbeddedLLMDevices> {
  const result = await getApp().ProbeEmbeddedLLMDevices()
  if (typeof result !== 'object' || result === null || !Array.isArray(result.devices) || !result.devices.every(isDevice)) {
    logger.error('probeEmbeddedLLMDevices: unexpected response shape', result)
    throw new Error('ProbeEmbeddedLLMDevices returned an invalid devices payload')
  }
  return result as EmbeddedLLMDevices
}
