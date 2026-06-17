import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig, updateLLMConfig, MASKED_API_KEY } from '@/api/config'
import { logger } from '@/lib/logger'
import type { ConfigProviderFull, LLMFullConfigRequest } from '@/types/models'

export interface ProviderConfig {
    api_key: string
    base_url: string
    models: string[]
}

const defaultProviderConfigs: Record<string, ProviderConfig> = {
    anthropic: { api_key: '', base_url: '', models: [] },
    gemini: { api_key: '', base_url: '', models: [] },
    lmstudio: { api_key: '', base_url: '', models: [] },
    openai_compatible: { api_key: '', base_url: '', models: [] },
    chatgpt: { api_key: '', base_url: '', models: [] },
}

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

export function useLLMConfig(onSettingsSaved?: () => void): UseLLMConfigResult {
    const [defaultModel, setDefaultModelState] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [isLoading, setIsLoading] = useState(true)
    const debounceRef = useRef<NodeJS.Timeout | null>(null)

    const loadConfig = useCallback(async () => {
        try {
            const result = await getConfig()
            const llm = result?.llm
            if (llm) {
                setDefaultModelState(llm.default_model || '')
                setProviderConfigs({
                    anthropic: toProviderConfig(llm.anthropic),
                    gemini: toProviderConfig(llm.gemini),
                    lmstudio: toProviderConfig(llm.lmstudio),
                    openai_compatible: toProviderConfig(llm.openai_compatible),
                    chatgpt: toProviderConfig(llm.chatgpt),
                })
            } else {
                setDefaultModelState('')
                setProviderConfigs(defaultProviderConfigs)
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            setDefaultModelState('')
            setProviderConfigs(defaultProviderConfigs)
        } finally {
            setIsLoading(false)
        }
    }, [])

    useEffect(() => { loadConfig() }, [loadConfig])

    const saveFullConfig = useCallback(async (defModel: string, configs: Record<string, ProviderConfig>) => {
        try {
            const req: LLMFullConfigRequest = {
                default_model: defModel,
                anthropic: { api_key: configs.anthropic!.api_key, models: configs.anthropic!.models },
                gemini: { api_key: configs.gemini!.api_key, models: configs.gemini!.models },
                lmstudio: { api_key: configs.lmstudio!.api_key, base_url: configs.lmstudio!.base_url, models: configs.lmstudio!.models },
                openai_compatible: { api_key: configs.openai_compatible!.api_key, base_url: configs.openai_compatible!.base_url, models: configs.openai_compatible!.models },
                chatgpt: { api_key: configs.chatgpt!.api_key, models: configs.chatgpt!.models },
            }
            await updateLLMConfig(req)
            onSettingsSaved?.()
        } catch (error) {
            logger.error('Failed to save LLM config:', error)
        }
    }, [onSettingsSaved])

    const debouncedSave = useCallback((defModel: string, configs: Record<string, ProviderConfig>) => {
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => saveFullConfig(defModel, configs), 300)
    }, [saveFullConfig])

    useEffect(() => () => { if (debounceRef.current) clearTimeout(debounceRef.current) }, [])

    const setDefaultModel = useCallback((model: string) => {
        setDefaultModelState(model)
        debouncedSave(model, providerConfigs)
    }, [providerConfigs, debouncedSave])

    const updateProviderConfig = useCallback((provider: string, updates: Partial<ProviderConfig>) => {
        setProviderConfigs((prev) => {
            const existing = prev[provider]
            if (!existing) return prev
            // If api_key equals the masked placeholder, keep the existing key
            const safeUpdates = { ...updates }
            if (safeUpdates.api_key === MASKED_API_KEY) {
                delete safeUpdates.api_key
            }
            const updated = { ...existing, ...safeUpdates }
            debouncedSave(defaultModel, { ...prev, [provider]: updated })
            return { ...prev, [provider]: updated }
        })
    }, [defaultModel, debouncedSave])

    const toggleModel = useCallback((provider: string, model: string) => {
        setProviderConfigs((prev) => {
            const existing = prev[provider]
            if (!existing) return prev
            const models = existing.models.includes(model)
                ? existing.models.filter(m => m !== model)
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
