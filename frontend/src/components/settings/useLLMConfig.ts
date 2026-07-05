import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { FIXED_PROVIDERS, type CompatibleType } from '@/lib/llm-providers'
import { compositeModelId, isCompositeModelId } from '@/lib/modelId'
import type { ConfigProviderFull } from '@/types/models'
import { useLLMConfigSave } from './useLLMConfigSave'

export interface ProviderConfig {
    api_key: string
    base_url: string
    models: string[]
    /**
     * Transport type for compatible (named, custom-endpoint) providers.
     * Fixed providers (anthropic, chatgpt) leave this undefined; it is only
     * meaningful for compatible providers and drives which backend map
     * (openai_compatible vs anthropic_compatible) they are saved under.
     */
    type?: CompatibleType
}

const defaultProviderConfigs: Record<string, ProviderConfig> = Object.fromEntries(
    FIXED_PROVIDERS.map((p) => [p, { api_key: '', base_url: '', models: [] }]),
)

interface UseLLMConfigResult {
    defaultModel: string
    providerConfigs: Record<string, ProviderConfig>
    /** Names of providers loaded from the openai_compatible map (non-fixed providers). */
    openaiCompatibleProviderNames: Set<string>
    /** Names of providers loaded from the anthropic_compatible map. */
    anthropicCompatibleProviderNames: Set<string>
    isLoading: boolean
    setDefaultModel: (model: string) => void
    updateProviderConfig: (provider: string, updates: Partial<ProviderConfig>) => void
    toggleModel: (provider: string, model: string) => void
    addProvider: (name: string, config: ProviderConfig) => void
    deleteProvider: (name: string) => void
}

function toProviderConfig(p: ConfigProviderFull, type?: CompatibleType): ProviderConfig {
    return {
        api_key: p.api_key,
        base_url: p.base_url ?? '',
        models: Array.isArray(p.models) ? [...p.models] : [],
        type,
    }
}

/**
 * Normalize a `default_model` value to its composite "provider/name" form so
 * the rest of the settings dialog can compare against composite identifiers
 * unambiguously (the "default" badge, delete-confirmation ownership, and the
 * default-model dropdown all key off composite ids).
 *
 * - Already composite ("provider/name"): returned unchanged.
 * - Bare model name: resolved to the first provider that exposes it. The
 *   `configs` object is built by {@link loadConfig} with fixed providers
 *   (anthropic, chatgpt) inserted first, then compatible providers whose JSON
 *   keys are alphabetically sorted by the backend — so `Object.entries`
 *   iteration order mirrors the backend's `allProviderEntries` order, and the
 *   first match wins just like `config.LLMConfig.ResolveDefaultModelProvider`.
 * - Stale bare name not enabled in any provider: returned unchanged so the
 *   dropdown shows its placeholder and no badge is rendered.
 */
function normalizeDefaultModel(
    def: string,
    configs: Record<string, ProviderConfig>,
): string {
    if (!def || isCompositeModelId(def)) return def
    for (const [provider, cfg] of Object.entries(configs)) {
        if (cfg?.models.includes(def)) {
            return compositeModelId(provider, def)
        }
    }
    return def
}

/**
 * Manages LLM config state: loading, default model, per-provider config, and model toggling.
 * Persistence is delegated to useLLMConfigSave (fixes #1, #7, #8).
 */
export function useLLMConfig(onSettingsSaved?: () => void, onDefaultModelChange?: (model: string) => void): UseLLMConfigResult {
    const [defaultModel, setDefaultModelState] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [openaiCompatibleProviderNames, setOpenaiCompatibleProviderNames] = useState<Set<string>>(new Set())
    const [anthropicCompatibleProviderNames, setAnthropicCompatibleProviderNames] = useState<Set<string>>(new Set())
    const [isLoading, setIsLoading] = useState(true)

    // Mutable ref for providerConfigs so setDefaultModel stays stable (fix #5).
    const configsRef = useRef(providerConfigs)
    configsRef.current = providerConfigs

    const { debouncedSave, buildSafeUpdates, saveFullConfig } = useLLMConfigSave(onSettingsSaved)

    const loadConfig = useCallback(async () => {
        try {
            const result = await getConfig()
            const llm = result?.llm
            if (llm) {
                const rawDefault = llm.default_model || ''
                const configs: Record<string, ProviderConfig> = {}
                const openaiNames = new Set<string>()
                const anthropicNames = new Set<string>()

                // Load fixed providers (anthropic, chatgpt).
                for (const p of FIXED_PROVIDERS) {
                    const cfg = llm[p]
                    if (cfg) {
                        configs[p] = toProviderConfig(cfg)
                    }
                }

                // Load openai_compatible providers from the map.
                const ocProviders = llm.openai_compatible
                if (ocProviders && typeof ocProviders === 'object') {
                    for (const [name, cfg] of Object.entries(ocProviders)) {
                        configs[name] = toProviderConfig(cfg, 'openai')
                        openaiNames.add(name)
                    }
                }

                // Load anthropic_compatible providers from the map.
                const acProviders = llm.anthropic_compatible
                if (acProviders && typeof acProviders === 'object') {
                    for (const [name, cfg] of Object.entries(acProviders)) {
                        configs[name] = toProviderConfig(cfg, 'anthropic')
                        anthropicNames.add(name)
                    }
                }

                // Normalize the default model to its composite "provider/name"
                // form so every comparison in the dialog (badge, delete
                // confirmation, dropdown) keys off composite ids consistently.
                setDefaultModelState(normalizeDefaultModel(rawDefault, configs))
                setProviderConfigs(configs)
                setOpenaiCompatibleProviderNames(openaiNames)
                setAnthropicCompatibleProviderNames(anthropicNames)
            } else {
                setDefaultModelState('')
                setProviderConfigs({ ...defaultProviderConfigs })
                setOpenaiCompatibleProviderNames(new Set())
                setAnthropicCompatibleProviderNames(new Set())
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            setDefaultModelState('')
            setProviderConfigs({ ...defaultProviderConfigs })
            setOpenaiCompatibleProviderNames(new Set())
            setAnthropicCompatibleProviderNames(new Set())
        } finally {
            setIsLoading(false)
        }
    }, [])

    useEffect(() => { loadConfig() }, [loadConfig])

    const setDefaultModel = useCallback((model: string) => {
        setDefaultModelState(model)
        onDefaultModelChange?.(model)
        debouncedSave(model, configsRef.current)
    }, [debouncedSave, onDefaultModelChange])

    const updateProviderConfig = useCallback((provider: string, updates: Partial<ProviderConfig>) => {
        setProviderConfigs((prev) => {
            const existing = prev[provider]
            if (!existing) return prev
            const safeUpdates = buildSafeUpdates(existing, updates)
            const updated = { ...existing, ...safeUpdates }
            debouncedSave(defaultModel, { ...prev, [provider]: updated })
            return { ...prev, [provider]: updated }
        })
    }, [defaultModel, debouncedSave, buildSafeUpdates])

    const toggleModel = useCallback((provider: string, model: string) => {
        setProviderConfigs((prev) => {
            const existing = prev[provider]
            if (!existing) return prev
            const models = existing.models.includes(model)
                ? existing.models.filter((m) => m !== model)
                : [...existing.models, model]
            const updated = { ...existing, models }
            debouncedSave(defaultModel, { ...prev, [provider]: updated })
            return { ...prev, [provider]: updated }
        })
    }, [defaultModel, debouncedSave])

    const addProvider = useCallback((name: string, config: ProviderConfig) => {
        setProviderConfigs((prev) => {
            const updated = { ...prev, [name]: config }
            // Save immediately so the new provider is persisted.
            saveFullConfig(defaultModel, updated)
            return updated
        })
        // Track the compatible provider under the correct transport set so it
        // is saved to the right backend map (openai_compatible vs anthropic_compatible).
        if (config.type === 'anthropic') {
            setAnthropicCompatibleProviderNames((prev) => new Set(prev).add(name))
        } else {
            setOpenaiCompatibleProviderNames((prev) => new Set(prev).add(name))
        }
    }, [defaultModel, saveFullConfig])

    const deleteProvider = useCallback((name: string) => {
        setProviderConfigs((prev) => {
            const next = { ...prev }
            delete next[name]
            // Save immediately so the deletion is persisted.
            saveFullConfig(defaultModel, next)
            return next
        })
        setOpenaiCompatibleProviderNames((prev) => {
            const next = new Set(prev)
            next.delete(name)
            return next
        })
        setAnthropicCompatibleProviderNames((prev) => {
            const next = new Set(prev)
            next.delete(name)
            return next
        })
    }, [defaultModel, saveFullConfig])

    return {
        defaultModel,
        providerConfigs,
        openaiCompatibleProviderNames,
        anthropicCompatibleProviderNames,
        isLoading,
        setDefaultModel,
        updateProviderConfig,
        toggleModel,
        addProvider,
        deleteProvider,
    }
}
