import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { FIXED_PROVIDERS } from '@/lib/llm-providers'
import type { ConfigProviderFull } from '@/types/models'
import { useLLMConfigSave } from './useLLMConfigSave'

export interface ProviderConfig {
    api_key: string
    base_url: string
    models: string[]
}

const defaultProviderConfigs: Record<string, ProviderConfig> = Object.fromEntries(
    FIXED_PROVIDERS.map((p) => [p, { api_key: '', base_url: '', models: [] }]),
)

interface UseLLMConfigResult {
    defaultModel: string
    providerConfigs: Record<string, ProviderConfig>
    /** Names of providers loaded from the openai_compatible map (non-fixed providers). */
    openaiCompatibleProviderNames: Set<string>
    isLoading: boolean
    setDefaultModel: (model: string) => void
    updateProviderConfig: (provider: string, updates: Partial<ProviderConfig>) => void
    toggleModel: (provider: string, model: string) => void
    addProvider: (name: string, config: ProviderConfig) => void
    deleteProvider: (name: string) => void
}

function toProviderConfig(p: ConfigProviderFull): ProviderConfig {
    return {
        api_key: p.api_key,
        base_url: p.base_url ?? '',
        models: Array.isArray(p.models) ? [...p.models] : [],
    }
}

/**
 * Manages LLM config state: loading, default model, per-provider config, and model toggling.
 * Persistence is delegated to useLLMConfigSave (fixes #1, #7, #8).
 */
export function useLLMConfig(onSettingsSaved?: () => void, onDefaultModelChange?: (model: string) => void): UseLLMConfigResult {
    const [defaultModel, setDefaultModelState] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [openaiCompatibleProviderNames, setOpenaiCompatibleProviderNames] = useState<Set<string>>(new Set())
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
                setDefaultModelState(llm.default_model || '')
                const configs: Record<string, ProviderConfig> = {}
                const openaiNames = new Set<string>()

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
                        configs[name] = toProviderConfig(cfg)
                        openaiNames.add(name)
                    }
                }

                setProviderConfigs(configs)
                setOpenaiCompatibleProviderNames(openaiNames)
            } else {
                setDefaultModelState('')
                setProviderConfigs({ ...defaultProviderConfigs })
                setOpenaiCompatibleProviderNames(new Set())
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            setDefaultModelState('')
            setProviderConfigs({ ...defaultProviderConfigs })
            setOpenaiCompatibleProviderNames(new Set())
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
        setOpenaiCompatibleProviderNames((prev) => new Set(prev).add(name))
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
    }, [defaultModel, saveFullConfig])

    return {
        defaultModel,
        providerConfigs,
        openaiCompatibleProviderNames,
        isLoading,
        setDefaultModel,
        updateProviderConfig,
        toggleModel,
        addProvider,
        deleteProvider,
    }
}
