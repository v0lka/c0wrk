// The embedded-LLM tuning surface's MODE DRAFTS: the local, unpersisted state
// of the two count-bearing modes ("Exact" context, "N layers" offload).
//
// A count-bearing mode picked from a Combobox persists NOTHING on the switch
// itself. The only count available at that instant is a fallback, and committing
// it would write an override the user never chose — `-ngl 0` is an all-CPU launch
// shape for a 27B model. So the switch raises a DRAFT flag; the write happens
// when the user commits a count (the minutes-draft pattern of
// useEmbeddedLLMAutoUnload).
//
// Extracted from useEmbeddedLLMTuning so that hook stays at the derivation and
// the one commit seam, and so the draft lifecycle has a single home: the two
// retirement effects, the two mode-change handlers and the two rendered modes
// all live here and nowhere else.

import { useCallback, useEffect, useState } from 'react'
import {
  CONTEXT_MODE_EXACT,
  OFFLOAD_MODE_LAYERS,
  type EmbeddedLLMTuning,
  type EmbeddedLLMTuningPatch,
} from '@/api/embeddedTuning'
import { TUNING_AUTO, isExactContext, storedOffloadMode } from '@/lib/embeddedTuningDisplay'

/** The draft state the three primary controls render and drive. */
export interface TuningModeDrafts {
  /** The Context mode to render: "exact" while drafted or stored, else Auto. */
  contextMode: string
  /** True while "Exact" is a local draft — picked, no token count committed. */
  contextDraft: boolean
  /** The Layer-offload mode to render. */
  offloadMode: string
  /** True while "N layers" is a local draft — picked, no count committed. */
  offloadDraft: boolean
  /** The stored context IS an explicit one (the authority, not a draft). The
   *  caller seeds the token field from it. */
  storedContextExact: boolean
  /** A Context mode picked from the Combobox. */
  onContextModeChange: (mode: string) => void
  /** A Layer-offload mode picked from the Combobox. */
  onOffloadModeChange: (mode: string) => void
}

/** Derive the two mode drafts from the authoritative tuning snapshot.
 *
 *  `onSet` is the caller's commit seam; it is invoked for the mode switches that
 *  DO persist (back to Auto, or an explicit All / CPU offload). */
export function useTuningModeDrafts(
  tuning: EmbeddedLLMTuning | null,
  onSet: (patch: EmbeddedLLMTuningPatch) => void,
): TuningModeDrafts {
  // Pure draft state: the two count-bearing modes (false = showing the
  // authority).
  const [contextDraft, setContextDraft] = useState(false)
  const [offloadDraft, setOffloadDraft] = useState(false)

  const ctx = tuning?.context
  const off = tuning?.offload
  const storedContextExact = isExactContext(ctx?.mode)
  const storedOffload = storedOffloadMode(off?.mode)

  // A persisted override retires the matching draft: from then on the rendered
  // mode IS the stored one, so a later re-read cannot flip the control back.
  useEffect(() => {
    if (storedContextExact) setContextDraft(false)
  }, [storedContextExact, ctx?.tokens])
  useEffect(() => {
    if (storedOffload !== TUNING_AUTO) setOffloadDraft(false)
  }, [storedOffload, off?.layers])

  const contextMode = contextDraft || storedContextExact ? CONTEXT_MODE_EXACT : TUNING_AUTO
  const offloadMode = offloadDraft ? OFFLOAD_MODE_LAYERS : storedOffload

  const onContextModeChange = useCallback(
    (mode: string) => {
      if (mode === CONTEXT_MODE_EXACT) {
        setContextDraft(true)
        return
      }
      setContextDraft(false)
      void onSet({ reset: ['context'] })
    },
    [onSet],
  )

  const onOffloadModeChange = useCallback(
    (mode: string) => {
      if (mode === OFFLOAD_MODE_LAYERS) {
        // A count-bearing mode is a DRAFT: the write waits for a count the user
        // actually committed, so one Combobox click can never persist
        // `-ngl 0` (an all-CPU launch shape) behind the operator's back.
        setOffloadDraft(true)
        return
      }
      setOffloadDraft(false)
      void onSet(mode === TUNING_AUTO ? { reset: ['offload'] } : { offload: { mode, layers: null } })
    },
    [onSet],
  )

  return {
    contextMode,
    contextDraft,
    offloadMode,
    offloadDraft,
    storedContextExact,
    onContextModeChange,
    onOffloadModeChange,
  }
}
