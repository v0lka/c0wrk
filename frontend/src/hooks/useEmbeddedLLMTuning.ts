// The Embedded LLM memory-plan TUNING commit flow (the parent of the tuning
// leaves; EmbeddedLLMSettings stays at its baseline by delegating here).
//
// Control pattern (the auto-unload suite's discipline): the leaves are fully
// controlled with ZERO local state; every display value is DERIVED here from
// the authoritative `GetEmbeddedLLMTuning` snapshot with a fallback — a knob
// the backend reports as 0 / out-of-range (or one that was never read) must
// not paint an entry that could not be committed back, so it falls back to the
// knob's documented default. The reusable NumberField keeps a focus-scoped
// keystroke draft, which is an implementation detail of the sanctioned
// primitive, not leaf state: its blur commit only ever fires for an in-range
// value and reverts locally otherwise (local refusal, no round trip). A commit
// runs RPC → setBusy/setError → re-read of BOTH snapshots in success AND
// failure — a refusal must also leave the controls on the authoritative
// values. The write itself never restarts a resident model (ADR-066 D13); the
// status snapshot's `reload_required` flag reports that Unload + Load applies
// it.

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  CONTEXT_MODE_EXACT,
  DEFAULT_FIT_MIN_CONTEXT,
  DEFAULT_FIT_TARGET_MIB,
  DEFAULT_PARALLEL,
  KV_CACHE_TYPES,
  MAX_CONTEXT_TOKENS,
  MIN_CONTEXT_TOKENS,
  MIN_PARALLEL,
  OFFLOAD_MODE_ALL,
  OFFLOAD_MODE_CPU,
  OFFLOAD_MODE_LAYERS,
  TUNING_PACKINGS,
  setEmbeddedLLMTuning,
  type EmbeddedLLMTuningPatch,
} from '@/api/embeddedTuning'
import { logger } from '@/lib/logger'
import {
  refreshEmbeddedLLMStatus,
  refreshEmbeddedLLMTuning,
  useEmbeddedLLMBusy,
  useEmbeddedLLMStatus,
  useEmbeddedLLMStore,
  useEmbeddedLLMTuningSnapshot,
} from '@/stores/embeddedLLMStore'

/** The Combobox spelling of "no override" on every string-shaped knob. */
export const TUNING_AUTO = 'auto'

/** The props EmbeddedLLMTuning (the three primary controls) renders. */
export interface EmbeddedLLMTuningPrimaryProps {
  contextMode: string
  contextTokens: number
  kvCacheType: string
  offloadMode: string
  offloadLayers: number
  reloadRequired: boolean
  disabled: boolean
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}

/** Which advanced numeric knobs are unset (Auto). */
export type TuningAutoKnobs = 'fitTargetMiB' | 'fitMinContext' | 'parallel' | 'cacheRamMiB' | 'hostReserveGiB'

/** The props EmbeddedLLMAdvancedTuning renders. */
export interface EmbeddedLLMAdvancedTuningProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  disabled: boolean
  packing: string
  fit: boolean
  kvOffload: boolean
  mmprojOffload: boolean
  fitTargetMiB: number
  fitMinContext: number
  parallel: number
  cacheRamMiB: number
  hostReserveGiB: number
  auto: Readonly<Record<TuningAutoKnobs, boolean>>
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}

/** Wails rejects with a Go error string, so the message IS the report. */
function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** A snapshot value, or the fallback when it is absent or out of range. */
function num(
  value: number | null | undefined,
  min: number,
  max: number,
  fallback: number,
): number {
  return value !== null && value !== undefined && value >= min && value <= max ? value : fallback
}

function oneOf(value: string | null | undefined, set: readonly string[]): string {
  return value !== null && value !== undefined && (set as readonly string[]).includes(value)
    ? value
    : TUNING_AUTO
}

export function useEmbeddedLLMTuning(): {
  primary: EmbeddedLLMTuningPrimaryProps
  advanced: EmbeddedLLMAdvancedTuningProps
} {
  const tuning = useEmbeddedLLMTuningSnapshot()
  const status = useEmbeddedLLMStatus()
  const busy = useEmbeddedLLMBusy()
  const setBusy = useEmbeddedLLMStore((s) => s.setBusy)
  const setError = useEmbeddedLLMStore((s) => s.setError)
  // Pure UI state: the collapsed Advanced section.
  const [advancedOpen, setAdvancedOpen] = useState(false)

  // The one read on mount — fail-soft (a backend that cannot answer yet
  // leaves the controls on the all-Auto fallbacks, never an error line).
  useEffect(() => {
    void refreshEmbeddedLLMTuning()
  }, [])

  const onSet = useCallback(
    async (patch: EmbeddedLLMTuningPatch) => {
      setBusy('tuning')
      setError(null)
      try {
        // The wrapper validates the patch locally first, so an out-of-range
        // value is refused without touching the config.
        await setEmbeddedLLMTuning(patch)
      } catch (err) {
        logger.warn('[embedded-llm] tuning change failed', err)
        setError(errorMessage(err))
      } finally {
        setBusy(null)
      }
      // Re-read BOTH snapshots in EITHER outcome: the write ends with one
      // `embedded_llm:state` event (itself an invalidation trigger), and a
      // refusal must also land the controls back on the authority.
      await refreshEmbeddedLLMTuning()
      await refreshEmbeddedLLMStatus()
    },
    [setBusy, setError],
  )

  const disabled = busy !== null
  const ctx = tuning?.context
  const contextExact = ctx?.mode === CONTEXT_MODE_EXACT
  const off = tuning?.offload
  const offloadMode =
    off?.mode === OFFLOAD_MODE_ALL || off?.mode === OFFLOAD_MODE_CPU || off?.mode === OFFLOAD_MODE_LAYERS
      ? off.mode
      : TUNING_AUTO

  const primary = useMemo<EmbeddedLLMTuningPrimaryProps>(
    () => ({
      contextMode: contextExact ? CONTEXT_MODE_EXACT : TUNING_AUTO,
      contextTokens: num(
        contextExact ? ctx?.tokens : null,
        MIN_CONTEXT_TOKENS,
        MAX_CONTEXT_TOKENS,
        DEFAULT_FIT_MIN_CONTEXT,
      ),
      kvCacheType: oneOf(tuning?.kv_cache_type, KV_CACHE_TYPES),
      offloadMode,
      offloadLayers: num(offloadMode === OFFLOAD_MODE_LAYERS ? off?.layers : null, 0, Number.MAX_SAFE_INTEGER, 0),
      reloadRequired: status?.reload_required ?? false,
      disabled,
      onSet: (patch) => void onSet(patch),
    }),
    [contextExact, ctx?.tokens, tuning?.kv_cache_type, offloadMode, off?.layers, status?.reload_required, disabled, onSet],
  )

  const advanced = useMemo<EmbeddedLLMAdvancedTuningProps>(
    () => ({
      open: advancedOpen,
      onOpenChange: setAdvancedOpen,
      disabled,
      packing: oneOf(tuning?.packing, TUNING_PACKINGS),
      fit: tuning?.fit ?? true,
      kvOffload: tuning?.kv_offload ?? true,
      mmprojOffload: tuning?.mmproj_offload ?? true,
      fitTargetMiB: num(tuning?.fit_target_mib, 0, Number.MAX_SAFE_INTEGER, DEFAULT_FIT_TARGET_MIB),
      fitMinContext: num(tuning?.fit_min_context, MIN_CONTEXT_TOKENS, MAX_CONTEXT_TOKENS, DEFAULT_FIT_MIN_CONTEXT),
      parallel: num(tuning?.parallel, MIN_PARALLEL, Number.MAX_SAFE_INTEGER, DEFAULT_PARALLEL),
      cacheRamMiB: num(tuning?.cache_ram_mib, 0, Number.MAX_SAFE_INTEGER, 0),
      hostReserveGiB: num(tuning?.host_reserve_gib, 0, Number.MAX_SAFE_INTEGER, 0),
      auto: {
        fitTargetMiB: tuning?.fit_target_mib == null,
        fitMinContext: tuning?.fit_min_context == null,
        parallel: tuning?.parallel == null,
        cacheRamMiB: tuning?.cache_ram_mib == null,
        hostReserveGiB: tuning?.host_reserve_gib == null,
      },
      onSet: (patch) => void onSet(patch),
    }),
    [advancedOpen, disabled, tuning, onSet],
  )

  return { primary, advanced }
}
