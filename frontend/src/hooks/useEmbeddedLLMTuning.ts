// The Embedded LLM memory-plan TUNING commit flow: the THREE PRIMARY controls
// (context mode, KV precision, layer offload) and the one commit seam the whole
// tuning surface writes through. The collapsed Advanced section's derivation
// lives in useEmbeddedLLMAdvancedTuning and the two count-bearing modes' draft
// lifecycle in useTuningModeDrafts, which this hook composes — so the callers
// still get one `{ primary, advanced }` pair.
//
// Control pattern (the auto-unload suite's discipline): the leaves are fully
// controlled; every display value is DERIVED here from the authoritative
// `GetEmbeddedLLMTuning` snapshot by the pure helpers in
// lib/embeddedTuningDisplay — a knob the backend reports as absent, NaN or
// out-of-range (or one that was never read) must not paint an entry that could
// not be committed back, so it falls back to the knob's documented default.
//
// A commit runs through the store's `runEmbeddedLLMAction`, which keeps the
// busy window open across the re-read of BOTH snapshots in success AND failure
// (a refusal must also land the controls back on the authority). The write
// itself never restarts a resident model (ADR-067 D13); the status snapshot's
// `reload_required` flag reports that Unload + Load applies it.

import { useCallback, useEffect, useMemo } from 'react'
import {
  KV_CACHE_TYPES,
  OFFLOAD_MODE_LAYERS,
  setEmbeddedLLMTuning,
  type EmbeddedLLMTuningPatch,
} from '@/api/embeddedTuning'
import { DEFAULT_CONTEXT_TOKENS } from '@/lib/embeddedTuningLimits'
import {
  TUNING_RANGES,
  oneOf,
  plannedContext,
  plannedLayers,
  seedCount,
} from '@/lib/embeddedTuningDisplay'
import {
  refreshEmbeddedLLMStatus,
  refreshEmbeddedLLMTuning,
  runEmbeddedLLMAction,
  useEmbeddedLLMBusy,
  useEmbeddedLLMStatus,
  useEmbeddedLLMTuningLoading,
  useEmbeddedLLMTuningSnapshot,
} from '@/stores/embeddedLLMStore'
import { useEmbeddedLLMAdvancedTuning } from './useEmbeddedLLMAdvancedTuning'
import { useTuningModeDrafts } from './useTuningModeDrafts'

/** The props EmbeddedLLMTuning (the three primary controls) renders. */
export interface EmbeddedLLMTuningPrimaryProps {
  contextMode: string
  contextTokens: number
  /** True while "Exact" is a local draft — picked, but no token count
   *  committed yet, so nothing is persisted. */
  contextDraft: boolean
  kvCacheType: string
  offloadMode: string
  offloadLayers: number
  /** True while "N layers" is a local draft — picked, but no layer count
   *  committed yet, so nothing is persisted. */
  offloadDraft: boolean
  reloadRequired: boolean
  disabled: boolean
  onSet: (patch: EmbeddedLLMTuningPatch) => void
  /** A Context mode picked from the Combobox. "Exact" becomes a local draft and
   *  persists nothing until a token count is committed. */
  onContextModeChange: (mode: string) => void
  /** A Layer-offload mode picked from the Combobox. "N layers" becomes a local
   *  draft and persists nothing until a layer count is committed. */
  onOffloadModeChange: (mode: string) => void
}

export function useEmbeddedLLMTuning(): {
  primary: EmbeddedLLMTuningPrimaryProps
  advanced: ReturnType<typeof useEmbeddedLLMAdvancedTuning>
} {
  const tuning = useEmbeddedLLMTuningSnapshot()
  const tuningLoading = useEmbeddedLLMTuningLoading()
  const status = useEmbeddedLLMStatus()
  const busy = useEmbeddedLLMBusy()

  // The one read on mount — fail-soft (a backend that cannot answer yet leaves
  // the controls on the all-Auto fallbacks, never an error line).
  useEffect(() => {
    void refreshEmbeddedLLMTuning()
  }, [])

  // Re-read BOTH snapshots in EITHER outcome: the write ends with one
  // `embedded_llm:state` event (itself an invalidation trigger), and a refusal
  // must also land the controls back on the authority.
  const readBack = useCallback(async () => {
    await refreshEmbeddedLLMTuning()
    await refreshEmbeddedLLMStatus()
  }, [])

  // One mutating RPC inside the store's busy window: the wrapper validates the
  // patch locally first (an out-of-range value is refused without touching the
  // config), a refusal is painted as the action error, and the window stays
  // open across `readBack` — see runEmbeddedLLMAction.
  const onSet = useCallback(
    (patch: EmbeddedLLMTuningPatch) =>
      runEmbeddedLLMAction('tuning', () => setEmbeddedLLMTuning(patch), readBack),
    [readBack],
  )

  // Destructured, not kept as one object: the sub-hook returns a fresh literal
  // every render, and these are the memo's dependencies (each a primitive or a
  // `useCallback`-stable handler).
  const {
    contextMode,
    contextDraft,
    offloadMode,
    offloadDraft,
    storedContextExact,
    onContextModeChange,
    onOffloadModeChange,
  } = useTuningModeDrafts(tuning, onSet)

  const ctx = tuning?.context
  const off = tuning?.offload
  const plan = status?.plan

  // Disabled while an RPC is in flight AND while the snapshot has not landed:
  // in that window every knob shows its fallback, and a commit would persist a
  // patch derived from values the user never saw rendered.
  const disabled = busy !== null || tuningLoading
  const commit = useMemo(
    () => ({ onSet: (patch: EmbeddedLLMTuningPatch) => void onSet(patch), disabled }),
    [onSet, disabled],
  )
  const advanced = useEmbeddedLLMAdvancedTuning(commit)

  const primary = useMemo<EmbeddedLLMTuningPrimaryProps>(
    () => ({
      contextMode,
      contextDraft,
      contextTokens: seedCount(
        storedContextExact ? ctx?.tokens : null,
        plannedContext(plan),
        TUNING_RANGES.context_tokens.min,
        TUNING_RANGES.context_tokens.max,
        DEFAULT_CONTEXT_TOKENS,
      ),
      kvCacheType: oneOf(tuning?.kv_cache_type, KV_CACHE_TYPES),
      offloadMode,
      offloadDraft,
      offloadLayers: seedCount(
        offloadMode === OFFLOAD_MODE_LAYERS ? off?.layers : null,
        plannedLayers(plan),
        TUNING_RANGES.offload_layers.min,
        TUNING_RANGES.offload_layers.max,
        0,
      ),
      reloadRequired: status?.reload_required ?? false,
      disabled,
      onSet: (patch) => void onSet(patch),
      onContextModeChange,
      onOffloadModeChange,
    }),
    [
      contextMode,
      contextDraft,
      storedContextExact,
      ctx?.tokens,
      plan,
      tuning?.kv_cache_type,
      offloadMode,
      offloadDraft,
      off?.layers,
      status?.reload_required,
      disabled,
      onSet,
      onContextModeChange,
      onOffloadModeChange,
    ],
  )

  return { primary, advanced }
}
