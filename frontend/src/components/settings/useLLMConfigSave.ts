import { useCallback, useRef, useEffect } from 'react'
import { updateLLMConfig, MASKED_API_KEY } from '@/api/config'
import { logger } from '@/lib/logger'
import { PROVIDERS, PROVIDERS_WITH_BASE_URL } from '@/lib/llm-providers'
import type { LLMFullConfigRequest } from '@/types/models'
import type { ProviderConfig } from './useLLMConfig'

interface UseLLMConfigSaveResult {
  /** Immediately persist the full config to the backend. */
  saveFullConfig: (defModel: string, configs: Record<string, ProviderConfig>) => void
  /** Debounced wrapper around saveFullConfig (300 ms). */
  debouncedSave: (defModel: string, configs: Record<string, ProviderConfig>) => void
  /** Strip masked API key placeholder before merging into existing config. */
  buildSafeUpdates: (existing: ProviderConfig, updates: Partial<ProviderConfig>) => Partial<ProviderConfig>
}

/**
 * Encapsulates the save/persist side of LLM config management:
 *   - Builds the LLMFullConfigRequest dynamically from PROVIDERS (fixes #1, #8)
 *   - Debounces saves (300 ms)
 *   - Filters out the MASKED_API_KEY sentinel so we never overwrite a real key
 */
export function useLLMConfigSave(onSettingsSaved?: () => void): UseLLMConfigSaveResult {
  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  const onSavedRef = useRef(onSettingsSaved)
  onSavedRef.current = onSettingsSaved

  // --- saveFullConfig ----------------------------------------------------------
  const saveFullConfig = useCallback(
    (defModel: string, configs: Record<string, ProviderConfig>) => {
      const req: LLMFullConfigRequest & Record<string, unknown> = { default_model: defModel }

      for (const p of PROVIDERS) {
        const cfg = configs[p]
        if (!cfg) continue
        const entry: { api_key: string; base_url?: string; models: string[] } = {
          api_key: cfg.api_key,
          models: cfg.models,
        }
        if (PROVIDERS_WITH_BASE_URL.has(p)) {
          entry.base_url = cfg.base_url
        }
        req[p] = entry
      }

      updateLLMConfig(req as LLMFullConfigRequest)
        .then(() => onSavedRef.current?.())
        .catch((error) => logger.error('Failed to save LLM config:', error))
    },
    [], // stable — uses refs for external deps
  )

  // --- debouncedSave -----------------------------------------------------------
  const debouncedSave = useCallback(
    (defModel: string, configs: Record<string, ProviderConfig>) => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => saveFullConfig(defModel, configs), 300)
    },
    [saveFullConfig],
  )

  // Cleanup debounce timer on unmount.
  useEffect(() => () => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
  }, [])

  // --- buildSafeUpdates --------------------------------------------------------
  const buildSafeUpdates = useCallback(
    (_existing: ProviderConfig, updates: Partial<ProviderConfig>): Partial<ProviderConfig> => {
      const safe = { ...updates }
      if (safe.api_key === MASKED_API_KEY) {
        delete safe.api_key
      }
      return safe
    },
    [],
  )

  return { saveFullConfig, debouncedSave, buildSafeUpdates }
}
