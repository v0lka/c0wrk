import { useState, useEffect } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import type { ModelInfo } from '@/types/models'

/** Module-level cache – fetched once, shared across all consumers. */
let cachedModels: ModelInfo[] | null = null
let cachedDefaultModel: string | null = null

/** Cache version bump forces re-fetch in all mounted components. */
let cacheVersion = 0

/** Listeners notified on cache invalidation. Each mounted useConfigData
 *  hook pushes a callback to bump its local setVersion. */
const listeners: Array<() => void> = []

interface ConfigData {
  /** All enabled models with reasoning metadata. */
  allModels: ModelInfo[]
  /** The global default_model from config. */
  defaultModel: string
}

/**
 * Invalidate the cached model data so the next render of any useConfigData
 * consumer triggers a fresh fetch from the backend. Call this after LLM
 * config changes (e.g. in settings save flow) to keep model selectors and
 * reasoning comboboxes in sync.
 */
export function invalidateConfigCache(): void {
  cachedModels = null
  cachedDefaultModel = null
  cacheVersion++
  for (const fn of listeners) {
    fn()
  }
}

/**
 * Fetch LLM config models and cache at module level.
 * Cache is invalidated when invalidateConfigCache() is called (e.g. after
 * LLM settings save), forcing a re-fetch in all mounted components.
 */
export function useConfigData(): ConfigData {
  const [allModels, setAllModels] = useState<ModelInfo[]>(cachedModels ?? [])
  const [defaultModel, setDefaultModel] = useState<string>(cachedDefaultModel ?? '')
  const [version, setVersion] = useState(cacheVersion)

  // Subscribe to cache invalidation so this hook re-fetches when
  // invalidateConfigCache() is called from elsewhere (e.g. settings save).
  useEffect(() => {
    const onChange = () => setVersion((v) => v + 1)
    listeners.push(onChange)
    return () => {
      const idx = listeners.indexOf(onChange)
      if (idx !== -1) listeners.splice(idx, 1)
    }
  }, [])

  useEffect(() => {
    if (cachedModels !== null && version === cacheVersion) return

    let cancelled = false
    getConfig()
      .then((cfg) => {
        if (cancelled) return
        const models: ModelInfo[] = cfg.llm?.all_models ?? []
        const def = cfg.llm?.default_model ?? ''
        cachedModels = models
        cachedDefaultModel = def
        setVersion(cacheVersion)
        setAllModels(models)
        setDefaultModel(def)
      })
      .catch((err) => {
        if (!cancelled) logger.error('useConfigData: failed to load config:', err)
      })

    return () => {
      cancelled = true
    }
  }, [version])

  return { allModels, defaultModel }
}
