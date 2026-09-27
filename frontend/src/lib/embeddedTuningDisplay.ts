// Pure display derivations for the embedded local model's tuning surface.
//
// React-free on purpose: `useEmbeddedLLMTuning` owns the state and the commit
// flow, this module owns the "what does the control show" arithmetic, so every
// rule is unit-testable without a renderer (the same split as
// lib/gitGraphRender and lib/embeddedLLMLabels).
//
// The two rules that matter:
//
//   1. A knob the backend reports as absent, NaN or out of the range the
//      boundary would accept must NOT paint a value that could not be
//      committed back — it falls back to the knob's documented default. The
//      ranges here are the same mirrors of config.ToTuning /
//      core/embeddedllm/limits.go that @/api/embeddedTuning validates against.
//   2. An UNSET knob is a distinct value from any explicit one ("null = the
//      planner decides"), so a tri-state knob renders `auto` rather than a
//      fallback-derived boolean that the effective plan may contradict.

import {
  CONTEXT_MODE_EXACT,
  OFFLOAD_MODE_ALL,
  OFFLOAD_MODE_CPU,
  OFFLOAD_MODE_LAYERS,
  type EmbeddedLLMPlan,
} from '@/api/embeddedTuning'
import {
  MAX_CONTEXT_TOKENS,
  MAX_TUNING_HOST_RESERVE_GIB,
  MAX_TUNING_LAYERS,
  MAX_TUNING_MIB,
  MAX_TUNING_PARALLEL,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
} from './embeddedTuningLimits'

/** The Combobox spelling of "no override" on every string-shaped knob. */
export const TUNING_AUTO = 'auto'

/** The explicit spellings of a TRI-STATE boolean knob (Fit, KV-cache offload,
 *  vision-projector offload). They match the recorded plan's `fit_arg`
 *  ("on" | "off"), so a tri-state reads as the launch flag it drives. */
export const TRI_ON = 'on'
export const TRI_OFF = 'off'

/** The boolean launch knobs that render as a tri-state and can be quoted from
 *  the recorded plan. Spelled as the plan DTO's own field names. */
export type TriStateKnob = 'fit' | 'kv_offload' | 'mmproj_offload'

/** A finite number inside [min, max]. NaN and Infinity fail every relational
 *  comparison, so they are rejected explicitly rather than slipping through. */
function paintable(value: number | null | undefined, min: number, max: number): boolean {
  return (
    typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max
  )
}

/** A snapshot value, or the fallback when it is absent or unpaintable. */
export function num(
  value: number | null | undefined,
  min: number,
  max: number,
  fallback: number,
): number {
  return paintable(value, min, max) ? (value as number) : fallback
}

/** A string knob inside its closed set, else Auto. */
export function oneOf(value: string | null | undefined, set: readonly string[]): string {
  return value !== null && value !== undefined && set.includes(value) ? value : TUNING_AUTO
}

/** The count beside a count-bearing mode: the stored override when it is
 *  paintable, else the recorded plan's own figure, else the documented default.
 *
 *  The plan step is what keeps a freshly picked mode from showing a number
 *  nobody chose: `-ngl 0` (all-CPU) and a 0-token context are both legal
 *  values, so a bare 0 fallback would look like a decision instead of an empty
 *  field. */
export function seedCount(
  stored: number | null | undefined,
  planned: number | null | undefined,
  min: number,
  max: number,
  fallback: number,
): number {
  if (paintable(stored, min, max)) return stored as number
  if (paintable(planned, min, max)) return planned as number
  return fallback
}

/** The stored layer-offload mode, normalized to the Combobox vocabulary. */
export function storedOffloadMode(mode: string | null | undefined): string {
  return mode === OFFLOAD_MODE_ALL || mode === OFFLOAD_MODE_CPU || mode === OFFLOAD_MODE_LAYERS
    ? mode
    : TUNING_AUTO
}

/** Whether the stored context override is the explicit-count mode. */
export function isExactContext(mode: string | null | undefined): boolean {
  return mode === CONTEXT_MODE_EXACT
}

/** A boolean knob's tri-state spelling: `auto` when the override is unset, so
 *  an unset knob is never rendered as a fallback-derived ON. The planner can
 *  resolve `null` to OFF — the offload exclusivity rule forces `-fit off`, and
 *  the memory gate spills the KV cache and the projector reserve to host RAM
 *  (`-nkvo`, `--no-mmproj-offload`) when the accelerator cannot hold them — and
 *  a switch showing ON would then let "flipping it to what it already displays"
 *  silently change the launch shape. */
export function boolModeOf(value: boolean | null | undefined): string {
  if (value === true) return TRI_ON
  if (value === false) return TRI_OFF
  return TUNING_AUTO
}

/** How the recorded plan's own boolean for `knob` reached the runtime, as a
 *  clause fragment — the effective outcome of an unset override, quoted beside
 *  the knob so Auto is never a guess.
 *
 *  The OFF spelling names the flag core/embeddedllm/server.go appends; ON is the
 *  OMISSION of it (the server renders no positive `-kvo`/`--mmproj-offload`), so
 *  the quote never claims a flag the runtime was not launched with. Null when no
 *  plan was recorded. */
export function plannedBoolArg(
  plan: EmbeddedLLMPlan | null | undefined,
  knob: Exclude<TriStateKnob, 'fit'>,
): string | null {
  if (!plan?.recorded) return null
  if (plan[knob]) return 'on device'
  return knob === 'kv_offload' ? 'in system RAM (-nkvo)' : 'in system RAM (--no-mmproj-offload)'
}

/** The recorded plan's literal `-fit` value ("on" | "off") — the effective
 *  outcome of an unset knob — or null when no plan was recorded. */
export function plannedFitArg(plan: EmbeddedLLMPlan | null | undefined): string | null {
  if (!plan || !plan.recorded) return null
  return plan.fit_arg === TRI_ON || plan.fit_arg === TRI_OFF ? plan.fit_arg : null
}

/** The recorded plan's context figure, or null when no plan was recorded. */
export function plannedContext(plan: EmbeddedLLMPlan | null | undefined): number | null {
  return plan?.recorded ? plan.context_size : null
}

/** The recorded plan's explicit `-ngl` count, or null when no plan was recorded
 *  or the flag was omitted (the plan's sentinel is -1, which is not a count —
 *  a negative figure is dropped here rather than passed through, so no caller
 *  can paint "-1 layers" for a fit-sized launch). */
export function plannedLayers(plan: EmbeddedLLMPlan | null | undefined): number | null {
  if (!plan?.recorded) return null
  // NaN fails the comparison too, so an unpaintable sentinel yields null.
  return plan.layers >= 0 ? plan.layers : null
}

/** The paintable range of every numeric tuning knob, keyed by the patch/YAML
 *  key. One source for the `max` attributes and the display fallbacks, so a
 *  control can never offer a value the API guard would refuse. */
export const TUNING_RANGES = {
  context_tokens: { min: MIN_CONTEXT_TOKENS, max: MAX_CONTEXT_TOKENS },
  fit_min_context: { min: MIN_CONTEXT_TOKENS, max: MAX_CONTEXT_TOKENS },
  offload_layers: { min: 0, max: MAX_TUNING_LAYERS },
  fit_target_mib: { min: 0, max: MAX_TUNING_MIB },
  cache_ram_mib: { min: 0, max: MAX_TUNING_MIB },
  parallel: { min: MIN_PARALLEL, max: MAX_TUNING_PARALLEL },
  host_reserve_gib: { min: 0, max: MAX_TUNING_HOST_RESERVE_GIB },
} as const
