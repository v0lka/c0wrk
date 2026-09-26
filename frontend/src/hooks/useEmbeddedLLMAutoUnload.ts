// The Embedded LLM auto-unload commit flow, extracted from
// EmbeddedLLMSettings (which must not grow past its baseline).
//
// Exactly the block's control pattern: the minutes DRAFT lives here — in the
// parent of the leaf control, which stays fully controlled with zero local
// state — an out-of-range entry reverts locally instead of paying a round
// trip for a guaranteed backend refusal, and a commit runs
// RPC → setBusy/setError → re-read in BOTH success and failure. The values
// rendered always come from the authoritative status snapshot; there is no
// optimistic copy.

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  DEFAULT_AUTO_UNLOAD_MINUTES,
  MIN_AUTO_UNLOAD_MINUTES,
  setEmbeddedLLMAutoUnload,
} from '@/api/embedded'
import { logger } from '@/lib/logger'
import {
  refreshEmbeddedLLMStatus,
  useEmbeddedLLMBusy,
  useEmbeddedLLMStatus,
  useEmbeddedLLMStore,
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

/** Wails rejects with a Go error string, so the message IS the report. */
function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function useEmbeddedLLMAutoUnload(): EmbeddedLLMAutoUnloadProps {
  const status = useEmbeddedLLMStatus()
  const busy = useEmbeddedLLMBusy()
  const setBusy = useEmbeddedLLMStore((s) => s.setBusy)
  const setError = useEmbeddedLLMStore((s) => s.setError)
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

  const commit = useCallback(
    async (nextEnabled: boolean, nextMinutes: number) => {
      setBusy('auto-unload')
      setError(null)
      try {
        await setEmbeddedLLMAutoUnload(nextEnabled, nextMinutes)
      } catch (err) {
        logger.warn('[embedded-llm] auto-unload change failed', err)
        setError(errorMessage(err))
      } finally {
        setBusy(null)
        setDraft(null)
      }
      // Read back in BOTH cases: a refusal is also a state the block must
      // show honestly.
      await refreshEmbeddedLLMStatus()
    },
    [setBusy, setError],
  )

  const commitDraft = useCallback(() => {
    if (draft === null) return
    const parsed = Number(draft)
    setDraft(null)
    // Out of range: revert to the authoritative value instead of paying a
    // round trip for a guaranteed refusal.
    if (!Number.isInteger(parsed) || parsed < MIN_AUTO_UNLOAD_MINUTES) return
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
