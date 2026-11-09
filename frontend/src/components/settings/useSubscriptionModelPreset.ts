import { useCallback, useEffect, useRef, useState } from 'react'
import type { SubscriptionAuthEventData } from '@/types/events'
import type {
  SubscriptionAuthStatusResponse,
  SubscriptionModelPresetEntry,
  SubscriptionModelPresetResponse,
} from '@/types/models'
import { logger } from '@/lib/logger'

/** Where a subscription model checklist's current list came from. */
export type SubscriptionModelListSource =
  | 'preset' // Network-free provider fallback.
  | 'subscription' // Live catalog returned for the signed-in account.

export interface SubscriptionModelListState {
  models: SubscriptionModelPresetEntry[]
  source: SubscriptionModelListSource
  loading: boolean
  error: string | null
  refresh: () => void
}

export interface SubscriptionModelPresetApi {
  displayName: string
  getPreset: () => Promise<SubscriptionModelPresetResponse>
  fetchModels: () => Promise<SubscriptionModelPresetResponse>
  getStatus: () => Promise<SubscriptionAuthStatusResponse>
  onState: (callback: (data: SubscriptionAuthEventData) => void) => () => void
}

/** Live subscription catalog with the offline fallback required by
 * specs/domains/llm-providers.md. */
export function useSubscriptionModelPreset(
  enabled: boolean,
  api: SubscriptionModelPresetApi,
  scope = '',
): SubscriptionModelListState {
  const [preset, setPreset] = useState<SubscriptionModelPresetEntry[]>([])
  const [live, setLive] = useState<SubscriptionModelPresetEntry[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fetchSeq = useRef(0)

  const runLiveFetch = useCallback(async () => {
    const seq = ++fetchSeq.current
    setLoading(true)
    setError(null)
    try {
      const resp = await api.fetchModels()
      if (seq !== fetchSeq.current) return
      const models = resp.models ?? []
      if (models.length === 0) {
        setLive(null)
        setError('Your subscription returned no visible models — try signing in again or updating the app.')
        return
      }
      setLive(models)
    } catch (err) {
      if (seq !== fetchSeq.current) return
      setLive(null)
      setError(err instanceof Error ? err.message : String(err))
      logger.warn(`${api.displayName} subscription model list fetch failed:`, err)
    } finally {
      if (seq === fetchSeq.current) setLoading(false)
    }
  }, [api])

  useEffect(() => {
    const effectSeq = ++fetchSeq.current
    if (!enabled) {
      setPreset((prev) => (prev.length === 0 ? prev : []))
      setLive(null)
      setLoading(false)
      setError(null)
      return
    }
    let cancelled = false
    setLive(null)
    setLoading(false)
    setError(null)

    api.getPreset()
      .then((resp) => {
        if (!cancelled) setPreset(resp.models ?? [])
      })
      .catch((err) => {
        logger.error(`Failed to load the ${api.displayName} model preset:`, err)
      })

    api.getStatus()
      .then((status) => {
        if (cancelled || effectSeq !== fetchSeq.current) return
        if (!status.signed_in) {
          setLive(null)
          return
        }
        void runLiveFetch()
      })
      .catch((err) => {
        logger.warn(`${api.displayName} auth status read failed before the model fetch:`, err)
      })

    return () => {
      cancelled = true
    }
  }, [enabled, api, scope, runLiveFetch])

  useEffect(() => {
    if (!enabled) return
    return api.onState((data) => {
      if (data.state === 'success') {
        void runLiveFetch()
        return
      }
      if (data.state === 'cancelled') {
        fetchSeq.current++
        setLive(null)
        setLoading(false)
        setError(null)
      }
    })
  }, [enabled, api, runLiveFetch])

  const refresh = useCallback(() => {
    void runLiveFetch()
  }, [runLiveFetch])

  const models = live ?? preset
  return { models, source: live ? 'subscription' : 'preset', loading, error, refresh }
}
