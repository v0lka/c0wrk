import { useCallback, useRef, useEffect } from 'react'
import { updateLLMConfig, MASKED_API_KEY } from '@/api/config'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { logger } from '@/lib/logger'
import { isBackendOwnedProvider, isCompatibleProvider, PROVIDERS_WITH_BASE_URL } from '@/lib/llm-providers'
import type { LLMFullConfigRequest } from '@/types/models'
import type { ProviderConfig } from './useLLMConfig'

interface UseLLMConfigSaveResult {
  /** Immediately persist the full config to the backend. */
  saveFullConfig: (defModel: string, configs: Record<string, ProviderConfig>) => Promise<void>
  /** Debounced wrapper around saveFullConfig (300 ms). */
  debouncedSave: (defModel: string, configs: Record<string, ProviderConfig>) => void
  /** Complete the pending save before an auth flow starts. */
  flushPendingSave: () => Promise<void>
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
  /** ChatGPT/Kimi authentication mode ('api_key' | 'oauth'). Undefined
   *  (dropped by JSON serialization) keeps the persisted mode via the
   *  backend pointer sentinel; a loaded subscription-capable entry carries
   *  a concrete value, so switching the selector persists verbatim. */
  auth_mode?: 'api_key' | 'oauth'
  auth_region?: 'global' | 'mainland_cn'
}

export function useLLMConfigSave(onSettingsSaved?: () => void): UseLLMConfigSaveResult {
  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  // Latest snapshot queued by debouncedSave and not yet persisted. Held so the
  // unmount cleanup can FLUSH it (cancelDebouncedSave discards it instead —
  // the "cancel stale state" semantics its callers rely on).
  const pendingSaveRef = useRef<{ defModel: string; configs: Record<string, ProviderConfig> } | null>(null)
  // Bumped when a newer save is queued or an older one is explicitly cancelled,
  // so a failed flush cannot put its snapshot back over that newer decision.
  const saveEpochRef = useRef(0)
  const inFlightSaveRef = useRef<Promise<void> | null>(null)
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
        // The backend-owned `embedded` record is deliberately absent from the
        // draft. UpdateLLMConfig replaces the whole openai_compatible map and
        // then regenerates that key from the authoritative embedded_llm state
        // (SyncEmbeddedProvider), so sending it back could only carry a stale
        // base_url — omitting it is what makes "the UI draft never owns the
        // embedded key" true on the wire, not just in a comment.
        if (isBackendOwnedProvider(p)) continue
        const entry: ProviderEntryPayload = {
          api_key: cfg.api_key,
          models: cfg.models,
        }
        if (PROVIDERS_WITH_BASE_URL.has(p, cfg.type)) {
          entry.base_url = cfg.base_url
        }
        if (isCompatibleProvider(p, cfg.type)) {
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
          // The auth mode rides along its fixed ChatGPT or Kimi entry: the
          // draft carries a concrete mode, so the selector's flip persists
          // atomically with the rest of the form. Other fixed providers have
          // no such field — undefined drops the key from the payload.
          if ((p === 'chatgpt' || p === 'kimi') && cfg.auth_mode !== undefined) {
            entry.auth_mode = cfg.auth_mode
          }
          if (p === 'kimi' && cfg.auth_region !== undefined) {
            entry.auth_region = cfg.auth_region
          }
          req[p] = entry
        }
      }

      // This is a full-form replacement payload: transmit both compatible
      // provider maps even when empty. On the backend, nil means "leave this
      // map unchanged", while {} means "replace it with no providers"; omitting
      // an empty map would make deleting the last compatible provider impossible.
      req.openai_compatible = openaiCompatible
      req.anthropic_compatible = anthropicCompatible

      const snapshot = { defModel, configs }
      pendingSaveRef.current = snapshot
      const promise = (async () => {
        try {
          await updateLLMConfig(req as LLMFullConfigRequest)
          if (pendingSaveRef.current === snapshot) pendingSaveRef.current = null
          invalidateConfigCache()
          onSavedRef.current?.()
        } catch (error) {
          logger.error('Failed to save LLM config:', error)
          throw error
        }
      })()
      inFlightSaveRef.current = promise
      const clearInFlight = () => {
        if (inFlightSaveRef.current === promise) inFlightSaveRef.current = null
      }
      void promise.then(clearInFlight, clearInFlight)
      return promise
    },
    [], // stable — uses refs for external deps
  )

  // --- debouncedSave -----------------------------------------------------------
  const debouncedSave = useCallback(
    (defModel: string, configs: Record<string, ProviderConfig>) => {
      saveEpochRef.current += 1
      pendingSaveRef.current = { defModel, configs }
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null
        const pending = pendingSaveRef.current
        const epoch = saveEpochRef.current
        pendingSaveRef.current = null
        if (!pending) return
        void saveFullConfig(pending.defModel, pending.configs).catch(() => {
          if (saveEpochRef.current === epoch && pendingSaveRef.current === null) {
            pendingSaveRef.current = pending
          }
        })
      }, 300)
    },
    [saveFullConfig],
  )

  const cancelDebouncedSave = useCallback(() => {
    saveEpochRef.current += 1
    if (debounceRef.current) {
      clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    pendingSaveRef.current = null
  }, [])

  const flushPendingSave = useCallback(async () => {
    for (;;) {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
      const saving = inFlightSaveRef.current
      if (saving) {
        await saving
        continue
      }
      const pending = pendingSaveRef.current
      const epoch = saveEpochRef.current
      pendingSaveRef.current = null
      if (!pending) return
      try {
        await saveFullConfig(pending.defModel, pending.configs)
      } catch (error) {
        // A failed pre-sign-in flush must stay retryable. A region change that
        // never reaches the backend would start OAuth against the previous region.
        if (saveEpochRef.current === epoch && pendingSaveRef.current === null) {
          pendingSaveRef.current = pending
        }
        throw error
      }
    }
  }, [saveFullConfig])

  // Flush a pending debounced save on unmount so field/model/default edits
  // made within the 300 ms window survive closing the settings dialog or
  // switching tabs (cancelDebouncedSave would silently discard them).
  // Safe against half-typed API keys: the provider forms commit a key edit
  // only on blur/Enter (ProviderConfigForm.commitApiKeyDraft), so a pending
  // snapshot can never carry a keystroke-level fragment over the stored key.
  useEffect(() => () => {
    void flushPendingSave().catch(() => undefined)
  }, [flushPendingSave])

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

  return { saveFullConfig, debouncedSave, cancelDebouncedSave, flushPendingSave, buildSafeUpdates }
}
