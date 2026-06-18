import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { PROVIDERS } from '@/lib/llm-providers'
import type { ConfigProviderFull } from '@/types/models'
import { useLLMConfigSave } from './useLLMConfigSave'

export interface ProviderConfig {
    api_key: string
    base_url: string
    models: string[]
}

const defaultProviderConfigs: Record<string, ProviderConfig> = Object.fromEntries(
    PROVIDERS.map((p) => [p, { api_key: '', base_url: '', models: [] }]),
)

interface UseLLMConfigResult {
    defaultModel: string
    providerConfigs: Record<string, ProviderConfig>
    isLoading: boolean
    setDefaultModel: (model: string) => void
    updateProviderConfig: (provider: string, updates: Partial<ProviderConfig>) => void
    toggleModel: (provider: string, model: string) => void
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
export function useLLMConfig(onSettingsSaved?: () => void): UseLLMConfigResult {
    const [defaultModel, setDefaultModelState] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [isLoading, setIsLoading] = useState(true)

    // Mutable ref for providerConfigs so setDefaultModel stays stable (fix #5).
    const configsRef = useRef(providerConfigs)
    configsRef.current = providerConfigs

    const { debouncedSave, buildSafeUpdates } = useLLMConfigSave(onSettingsSaved)

    const loadConfig = useCallback(async () => {
        try {
            const result = await getConfig()
            const llm = result?.llm
            if (llm) {
                setDefaultModelState(llm.default_model || '')
                const configs: Record<string, ProviderConfig> = {}
                for (const p of PROVIDERS) {
                    configs[p] = toProviderConfig(llm[p])
                }
                setProviderConfigs(configs)
            } else {
                setDefaultModelState('')
                setProviderConfigs({ ...defaultProviderConfigs })
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            setDefaultModelState('')
            setProviderConfigs({ ...defaultProviderConfigs })
        } finally {
            setIsLoading(false)
        }
    }, [])

    useEffect(() => { loadConfig() }, [loadConfig])

    const setDefaultModel = useCallback((model: string) => {
        setDefaultModelState(model)
        debouncedSave(model, configsRef.current)
    }, [debouncedSave])

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

    return {
        defaultModel,
        providerConfigs,
        isLoading,
        setDefaultModel,
        updateProviderConfig,
        toggleModel,
    }
}
