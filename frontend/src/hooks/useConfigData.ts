import { useState, useEffect } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import type { ModelInfo } from '@/types/models'

/** Module-level cache – fetched once, shared across all consumers. */
let cachedModels: ModelInfo[] | null = null
let cachedDefaultModel: string | null = null

interface ConfigData {
  /** All enabled models with reasoning metadata. */
  allModels: ModelInfo[]
  /** The global default_model from config. */
  defaultModel: string
}

/**
 * Fetch LLM config models once and cache at module level.
 * Multiple components calling this hook share the same single network request.
 */
export function useConfigData(): ConfigData {
  const [allModels, setAllModels] = useState<ModelInfo[]>(cachedModels ?? [])
  const [defaultModel, setDefaultModel] = useState<string>(cachedDefaultModel ?? '')

  useEffect(() => {
    // Already cached — no fetch needed.
    if (cachedModels !== null) return

    let cancelled = false
    getConfig()
      .then((cfg) => {
        if (cancelled) return
        const models: ModelInfo[] = cfg.llm?.all_models ?? []
        const def = cfg.llm?.default_model ?? ''
        cachedModels = models
        cachedDefaultModel = def
        setAllModels(models)
        setDefaultModel(def)
      })
      .catch((err) => {
        if (!cancelled) logger.error('useConfigData: failed to load config:', err)
      })

    return () => {
      cancelled = true
    }
  }, [])

  return { allModels, defaultModel }
}
