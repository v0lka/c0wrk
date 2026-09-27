// The Embedded LLM auto-unload commit flow, extracted from
// EmbeddedLLMSettings (which must not grow past its baseline).
//
// Exactly the block's control pattern: the minutes DRAFT lives here — in the
// parent of the leaf control, which stays fully controlled with zero local
// state — an out-of-range entry reverts locally instead of paying a round
// trip for a guaranteed backend refusal, and a commit runs through the store's
// `runEmbeddedLLMAction`: RPC → busy window → re-read, with the window kept
// open across the read-back so the controls cannot be re-enabled against a
// stale snapshot. The values rendered always come from the authoritative
// status snapshot; there is no optimistic copy.

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  DEFAULT_AUTO_UNLOAD_MINUTES,
  MAX_AUTO_UNLOAD_MINUTES,
  MIN_AUTO_UNLOAD_MINUTES,
  setEmbeddedLLMAutoUnload,
} from '@/api/embedded'
import {
  refreshEmbeddedLLMStatus,
  runEmbeddedLLMAction,
  useEmbeddedLLMBusy,
  useEmbeddedLLMStatus,
} from '@/stores/embeddedLLMStore'

/** The props EmbeddedLLMAutoUnload renders, exactly (spread onto the leaf). */
export interface EmbeddedLLMAutoUnloadProps {
  enabled: boolean
  minutes: number
  draft: string | null
  disabled: boolean
  onToggle: (enabled: boolean) => void
  onDraftChange: (draft: string) => void
  onCommit: () => void
}

export function useEmbeddedLLMAutoUnload(): EmbeddedLLMAutoUnloadProps {
  const status = useEmbeddedLLMStatus()
  const busy = useEmbeddedLLMBusy()
  // Pure draft state for the minutes field (null = showing the authority).
  const [draft, setDraft] = useState<string | null>(null)

  const enabled = status?.auto_unload_enabled ?? false
  const minutes =
    status && status.auto_unload_minutes >= MIN_AUTO_UNLOAD_MINUTES
      ? status.auto_unload_minutes
      : DEFAULT_AUTO_UNLOAD_MINUTES

  // A refreshed authoritative budget retires a stale draft.
  useEffect(() => {
    setDraft(null)
  }, [minutes])

  const commit = useCallback(async (nextEnabled: boolean, nextMinutes: number) => {
    await runEmbeddedLLMAction(
      'auto-unload',
      () => setEmbeddedLLMAutoUnload(nextEnabled, nextMinutes),
      refreshEmbeddedLLMStatus,
    )
    // The window has closed and the authoritative budget has landed; a draft
    // must not outlive either — in EITHER outcome, since a refusal is also a
    // state the block shows honestly.
    setDraft(null)
  }, [])

  const commitDraft = useCallback(() => {
    if (draft === null) return
    const parsed = Number(draft)
    setDraft(null)
    // Out of range: revert to the authoritative value instead of paying a round
    // trip for a guaranteed refusal. The ceiling matters as much as the floor —
    // above MAX_AUTO_UNLOAD_MINUTES the backend's minutes→nanoseconds multiply
    // overflows, and an "effectively never" budget inverts into "unload
    // immediately" (see the constant's doc in @/api/embedded).
    if (
      !Number.isInteger(parsed) ||
      parsed < MIN_AUTO_UNLOAD_MINUTES ||
      parsed > MAX_AUTO_UNLOAD_MINUTES
    )
      return
    void commit(enabled, parsed)
  }, [draft, enabled, commit])

  return useMemo(
    () => ({
      enabled,
      minutes,
      draft,
      disabled: busy !== null,
      onToggle: (next: boolean) => void commit(next, minutes),
      onDraftChange: setDraft,
      onCommit: commitDraft,
    }),
    [enabled, minutes, draft, busy, commit, commitDraft],
  )
}
