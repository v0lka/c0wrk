import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchChatGPTModels, getChatGPTAuthStatus, getChatGPTModelPreset, onChatGPTAuthState } from '@/api/auth'
import { logger } from '@/lib/logger'
import type { ChatGPTModelPresetEntry } from '@/types/models'

/** Where the oauth-mode model checklist's current list came from. */
export type ChatGPTModelListSource = 'preset' | 'subscription'

export interface ChatGPTModelListState {
  /** The checklist entries: the live subscription list once a fetch has
   *  answered, the offline preset until then. */
  models: ChatGPTModelPresetEntry[]
  /** 'preset' until a live fetch replaces the list, 'subscription' after. */
  source: ChatGPTModelListSource
  /** A live fetch is in flight (the Fetch models button's spinner). */
  loading: boolean
  /** The last live-fetch failure, for the Fetch models button's error
   *  line. Null while no fetch has failed (and while nothing but the
   *  static preset has been attempted). */
  error: string | null
  /** Re-run the live fetch (the Fetch models button). No-op while a fetch
   *  is already in flight. */
  refresh: () => void
}

/**
 * The oauth-mode ChatGPT model checklist's data source, as a three-stage
 * pipeline:
 *
 * 1. offline preset — GetChatGPTModelPreset answers first so the checklist
 *    is never empty (fail-soft: a failure leaves the list empty);
 * 2. live catalog — when the account is signed in, FetchChatGPTModels
 *    replaces the list with the models THIS subscription actually serves
 *    (the backend's own /models catalog, the same list the Codex CLI
 *    picker shows); a failure keeps the preset and records `error`;
 * 3. refresh — the Fetch models button re-runs stage 2, and a completed
 *    sign-in (chatgpt_auth:state success) re-runs it automatically so the
 *    list appears without reopening the dialog.
 *
 * The two lists are SEPARATE state (`preset` and `live`), never one slot
 * the stages race to overwrite: a live failure can therefore never blank a
 * preset that is still in flight, and a slow preset answer can never
 * clobber an installed live list — the rendered list is simply
 * `live ?? preset`. A live answer of an EMPTY list is also not installed
 * (the backend answers a below-minimum client_version with a valid but
 * empty catalog): the preset stays and `error` explains why, so the
 * checklist can never go blank through this hook.
 *
 * Everything clears when `enabled` flips back to false, so the api_key-mode
 * checklist never renders subscription leftovers.
 */
export function useChatGPTModelPreset(enabled: boolean): ChatGPTModelListState {
  const [preset, setPreset] = useState<ChatGPTModelPresetEntry[]>([])
  const [live, setLive] = useState<ChatGPTModelPresetEntry[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fetchSeq = useRef(0)

  /** One live-fetch attempt. The sequence guard keeps a stale in-flight
   *  fetch from clobbering the state of a newer one (or of the disabled
   *  reset) when it finally settles. Only a NON-EMPTY answer installs the
   *  live list — an empty one keeps the preset with an explanatory error
   *  (see the doc comment). */
  const runLiveFetch = useCallback(async () => {
    const seq = ++fetchSeq.current
    setLoading(true)
    setError(null)
    try {
      const resp = await fetchChatGPTModels()
      if (seq !== fetchSeq.current) return
      const models = resp.models ?? []
      if (models.length === 0) {
        setError('Your subscription returned no visible models — try signing in again or updating the app.')
        return
      }
      setLive(models)
    } catch (err) {
      if (seq !== fetchSeq.current) return
      // The preset (or the previous live list) stays — the checklist is
      // more useful with a stale list than an empty flash.
      setError(err instanceof Error ? err.message : String(err))
      logger.warn('ChatGPT subscription model list fetch failed:', err)
    } finally {
      if (seq === fetchSeq.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!enabled) {
      fetchSeq.current++
      setPreset((prev) => (prev.length === 0 ? prev : []))
      setLive(null)
      setLoading(false)
      setError(null)
      return
    }
    let cancelled = false

    // Stage 1: the offline preset, unconditionally — it is the fallback the
    // live fetch upgrades, and it loads even while signed out. It only ever
    // writes its OWN state, so it cannot race the live list (and a stale
    // answer after a disable flip is dropped by the cancelled flag).
    getChatGPTModelPreset()
      .then((resp) => {
        if (!cancelled) setPreset(resp.models ?? [])
      })
      .catch((err) => {
        logger.error('Failed to load the ChatGPT model preset:', err)
      })

    // Stage 2: the live catalog when the account is signed in. The status
    // snapshot decides — a failed status read simply keeps the preset.
    void getChatGPTAuthStatus()
      .then((status) => {
        if (cancelled || !status.signed_in) return
        void runLiveFetch()
      })
      .catch((err) => {
        logger.warn('ChatGPT auth status read failed before the model fetch:', err)
      })

    return () => {
      cancelled = true
    }
  }, [enabled, runLiveFetch])

  // Stage 3a: a completed sign-in re-runs the live fetch so the freshly
  // signed-in account's list appears without reopening the settings dialog.
  useEffect(() => {
    if (!enabled) return
    return onChatGPTAuthState((data) => {
      if (data.state === 'success') void runLiveFetch()
    })
  }, [enabled, runLiveFetch])

  const refresh = useCallback(() => {
    void runLiveFetch()
  }, [runLiveFetch])

  const models = live ?? preset
  return { models, source: live ? 'subscription' : 'preset', loading, error, refresh }
}
