// The ADVANCED memory-plan section's derivation, split from
// useEmbeddedLLMTuning so each half of the tuning surface stays small: this
// hook owns the collapsed-section flag and the value every advanced knob
// renders; the parent owns the drafts of the primary controls and the ONE
// commit seam both halves write through.
//
// `useEmbeddedLLMTuning` still returns `{ primary, advanced }`, so the settings
// block and the leaf tests are unaffected by the split.
//
// Every display value comes from the authoritative `GetEmbeddedLLMTuning`
// snapshot through the pure helpers in lib/embeddedTuningDisplay: a knob the
// backend reports as absent, NaN or out of range falls back to its documented
// default, and an UNSET knob keeps its own spelling (`auto`) instead of a
// fallback-derived boolean — see `boolModeOf` for why that matters for all
// THREE boolean knobs (Fit, the KV cache and the vision projector), each of
// which the planner can resolve to OFF while the override stays unset.

import { useMemo, useState } from 'react'
import { TUNING_PACKINGS, type EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'
import {
  DEFAULT_FIT_MIN_CONTEXT,
  DEFAULT_FIT_TARGET_MIB,
  DEFAULT_PARALLEL,
} from '@/lib/embeddedTuningLimits'
import {
  TUNING_RANGES,
  boolModeOf,
  num,
  oneOf,
  plannedBoolArg,
  plannedFitArg,
} from '@/lib/embeddedTuningDisplay'
import { useEmbeddedLLMStatus, useEmbeddedLLMTuningSnapshot } from '@/stores/embeddedLLMStore'

/** The commit seam the advanced section writes through, owned by the parent
 *  hook: one busy window and one error line for the whole tuning surface. */
export interface EmbeddedLLMTuningCommit {
  onSet: (patch: EmbeddedLLMTuningPatch) => void
  disabled: boolean
}

/** Which advanced numeric knobs are unset (Auto). */
export type TuningAutoKnobs =
  | 'fitTargetMiB'
  | 'fitMinContext'
  | 'parallel'
  | 'cacheRamMiB'
  | 'hostReserveGiB'

/** The props EmbeddedLLMAdvancedTuning renders. */
export interface EmbeddedLLMAdvancedTuningProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  disabled: boolean
  packing: string
  /** The Fit knob's tri-state spelling ('auto' | 'on' | 'off') — an unset knob
   *  is `auto`, never a fallback-derived ON. */
  fitMode: string
  /** The recorded plan's literal `-fit` value, or null when nothing was
   *  recorded: the effective outcome of `auto`, rendered beside the knob. */
  fitPlanned: string | null
  /** The KV-cache-offload knob's tri-state spelling — same rule as `fitMode`. */
  kvMode: string
  /** How the recorded plan resolved an unset `kv_offload` (a clause fragment), or
   *  null when nothing was recorded. */
  kvPlanned: string | null
  /** The vision-projector knob's tri-state spelling — same rule as `fitMode`. */
  mmprojMode: string
  /** How the recorded plan resolved an unset `mmproj_offload`, or null. */
  mmprojPlanned: string | null
  fitTargetMiB: number
  fitMinContext: number
  parallel: number
  cacheRamMiB: number
  hostReserveGiB: number
  auto: Readonly<Record<TuningAutoKnobs, boolean>>
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}

export function useEmbeddedLLMAdvancedTuning(
  commit: EmbeddedLLMTuningCommit,
): EmbeddedLLMAdvancedTuningProps {
  const tuning = useEmbeddedLLMTuningSnapshot()
  const status = useEmbeddedLLMStatus()
  const plan = status?.plan
  // Pure UI state: the collapsed section.
  const [open, setOpen] = useState(false)
  const { onSet, disabled } = commit

  return useMemo<EmbeddedLLMAdvancedTuningProps>(
    () => ({
      open,
      onOpenChange: setOpen,
      disabled,
      onSet,
      packing: oneOf(tuning?.packing, TUNING_PACKINGS),
      fitMode: boolModeOf(tuning?.fit),
      fitPlanned: plannedFitArg(plan),
      kvMode: boolModeOf(tuning?.kv_offload),
      kvPlanned: plannedBoolArg(plan, 'kv_offload'),
      mmprojMode: boolModeOf(tuning?.mmproj_offload),
      mmprojPlanned: plannedBoolArg(plan, 'mmproj_offload'),
      fitTargetMiB: num(tuning?.fit_target_mib, TUNING_RANGES.fit_target_mib.min, TUNING_RANGES.fit_target_mib.max, DEFAULT_FIT_TARGET_MIB),
      fitMinContext: num(tuning?.fit_min_context, TUNING_RANGES.fit_min_context.min, TUNING_RANGES.fit_min_context.max, DEFAULT_FIT_MIN_CONTEXT),
      parallel: num(tuning?.parallel, TUNING_RANGES.parallel.min, TUNING_RANGES.parallel.max, DEFAULT_PARALLEL),
      cacheRamMiB: num(tuning?.cache_ram_mib, TUNING_RANGES.cache_ram_mib.min, TUNING_RANGES.cache_ram_mib.max, 0),
      hostReserveGiB: num(tuning?.host_reserve_gib, TUNING_RANGES.host_reserve_gib.min, TUNING_RANGES.host_reserve_gib.max, 0),
      auto: {
        fitTargetMiB: tuning?.fit_target_mib == null,
        fitMinContext: tuning?.fit_min_context == null,
        parallel: tuning?.parallel == null,
        cacheRamMiB: tuning?.cache_ram_mib == null,
        hostReserveGiB: tuning?.host_reserve_gib == null,
      },
    }),
    [open, disabled, onSet, tuning, plan],
  )
}
