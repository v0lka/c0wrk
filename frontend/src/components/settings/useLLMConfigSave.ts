import { useCallback, useRef, useEffect } from 'react'
import { updateLLMConfig, MASKED_API_KEY } from '@/api/config'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { logger } from '@/lib/logger'
import { isCompatibleProvider, PROVIDERS_WITH_BASE_URL } from '@/lib/llm-providers'
import type { LLMFullConfigRequest } from '@/types/models'
import type { ProviderConfig } from './useLLMConfig'

interface UseLLMConfigSaveResult {
  /** Immediately persist the full config to the backend. */
  saveFullConfig: (defModel: string, configs: Record<string, ProviderConfig>) => void
  /** Debounced wrapper around saveFullConfig (300 ms). */
  debouncedSave: (defModel: string, configs: Record<string, ProviderConfig>) => void
  /** Cancel a queued debounced save before it can persist stale state. */
  cancelDebouncedSave: () => void
  /** Strip masked API key placeholder before merging into existing config. */
  buildSafeUpdates: (existing: ProviderConfig, updates: Partial<ProviderConfig>) => Partial<ProviderConfig>
}

/**
 * Encapsulates the save/persist side of LLM config management:
 *   - Builds the LLMFullConfigRequest: fixed providers as flat fields,
 *     OpenAI-compatible providers nested under openai_compatible map (fixes #1, #8)
 *   - Debounces saves (300 ms)
 *   - Filters out the MASKED_API_KEY sentinel so we never overwrite a real key
 */
/** One provider's slice of the full-form save payload. */
interface ProviderEntryPayload {
  api_key: string
  base_url?: string
  models: string[]
  /** Only set for compatible providers (ADR-054). */
  tls_fingerprint?: string
  /** Auto-retry interval in seconds — compatible providers only. Undefined
   *  (dropped by JSON serialization) keeps the persisted value via the
   *  backend pointer sentinel; an explicit number, including 0, is applied
   *  verbatim. */
  auto_retry_seconds?: number
}

export function useLLMConfigSave(onSettingsSaved?: () => void): UseLLMConfigSaveResult {
  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  const onSavedRef = useRef(onSettingsSaved)
  onSavedRef.current = onSettingsSaved

  // --- saveFullConfig ----------------------------------------------------------
  const saveFullConfig = useCallback(
    (defModel: string, configs: Record<string, ProviderConfig>) => {
      const req: LLMFullConfigRequest & Record<string, unknown> = { default_model: defModel }
      const openaiCompatible: Record<string, ProviderEntryPayload> = {}
      const anthropicCompatible: Record<string, ProviderEntryPayload> = {}

      for (const [p, cfg] of Object.entries(configs)) {
        if (!cfg) continue
        const entry: ProviderEntryPayload = {
          api_key: cfg.api_key,
          models: cfg.models,
        }
        if (PROVIDERS_WITH_BASE_URL.has(p)) {
          entry.base_url = cfg.base_url
        }
        if (isCompatibleProvider(p)) {
          // The per-provider TLS pin (ADR-054) is stored only for compatible
          // providers — fixed ones talk to vendor endpoints with public
          // certificates. It always travels along so the backend applies it
          // atomically with the rest of the entry; the pointer sentinel on
          // the Go side means an explicit '' here clears the pin.
          entry.tls_fingerprint = cfg.tls_fingerprint
          // The auto-retry interval travels along the same compatible-only
          // path: undefined omits the key entirely, which the backend
          // pointer sentinel treats as "keep the persisted value"
          // (debounce-safe), while an explicit number — including a
          // disabling 0 — is applied verbatim, atomically with the entry.
          if (cfg.auto_retry_seconds !== undefined) {
            entry.auto_retry_seconds = cfg.auto_retry_seconds
          }
          // Route to the correct backend map by transport type. Default to
          // 'openai' for compatible providers that lack an explicit type
          // (preserves behavior for any pre-existing compatible entries).
          if (cfg.type === 'anthropic') {
            anthropicCompatible[p] = entry
          } else {
            openaiCompatible[p] = entry
          }
        } else {
          req[p] = entry
        }
      }

      // This is a full-form replacement payload: transmit both compatible
      // provider maps even when empty. On the backend, nil means "leave this
      // map unchanged", while {} means "replace it with no providers"; omitting
      // an empty map would make deleting the last compatible provider impossible.
      req.openai_compatible = openaiCompatible
      req.anthropic_compatible = anthropicCompatible

      updateLLMConfig(req as LLMFullConfigRequest)
        .then(() => {
          invalidateConfigCache()
          onSavedRef.current?.()
        })
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

  const cancelDebouncedSave = useCallback(() => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
  }, [])

  // Cleanup debounce timer on unmount.
  useEffect(() => () => {
    cancelDebouncedSave()
  }, [cancelDebouncedSave])

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

  return { saveFullConfig, debouncedSave, cancelDebouncedSave, buildSafeUpdates }
}
