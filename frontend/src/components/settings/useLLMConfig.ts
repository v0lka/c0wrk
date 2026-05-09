import { useState, useEffect, useCallback, useRef } from 'react'
import { getConfig, updateLLMSettings } from '@/api/config'
import { logger } from '@/lib/logger'

export interface ProviderConfig {
    api_key: string
    base_url: string
    model: string
}

const defaultProviderConfigs: Record<string, ProviderConfig> = {
    anthropic: { api_key: '', model: '', base_url: '' },
    gemini: { api_key: '', model: '', base_url: '' },
    lmstudio: { api_key: '', base_url: '', model: '' },
    openai_compatible: { api_key: '', base_url: '', model: '' },
    chatgpt: { api_key: '', model: '', base_url: '' },
}

interface UseLLMConfigResult {
    activeProvider: string
    providerConfigs: Record<string, ProviderConfig>
    isLoading: boolean
    handleProviderChange: (provider: string) => void
    updateProviderConfig: (updates: Partial<ProviderConfig>) => void
}

export function useLLMConfig(onSettingsSaved?: () => void): UseLLMConfigResult {
    const [activeProvider, setActiveProvider] = useState('')
    const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
    const [isLoading, setIsLoading] = useState(true)
    const debounceRef = useRef<NodeJS.Timeout | null>(null)
    const activeProviderRef = useRef(activeProvider)
    activeProviderRef.current = activeProvider

    const loadConfig = useCallback(async () => {
        try {
            const result = await getConfig()
            const llm = result?.llm
            if (llm) {
                setActiveProvider(llm.active_provider || 'anthropic')
                setProviderConfigs({
                    anthropic: { api_key: llm.anthropic.api_key, model: llm.anthropic.model, base_url: '' },
                    gemini: { api_key: llm.gemini.api_key, model: llm.gemini.model, base_url: '' },
                    lmstudio: llm.lmstudio ?? { api_key: '', base_url: '', model: '' },
                    openai_compatible: llm.openai_compatible ?? { api_key: '', base_url: '', model: '' },
                    chatgpt: { api_key: llm.chatgpt.api_key, model: llm.chatgpt.model, base_url: '' },
                })
            } else {
                setActiveProvider('anthropic')
                setProviderConfigs(defaultProviderConfigs)
            }
        } catch (error) {
            logger.error('Failed to load LLM config:', error)
            setActiveProvider('anthropic')
            setProviderConfigs(defaultProviderConfigs)
        } finally {
            setIsLoading(false)
        }
    }, [])

    useEffect(() => { loadConfig() }, [loadConfig])

    const saveSettings = useCallback(async (provider: string, config: ProviderConfig) => {
        try {
            await updateLLMSettings({ active_provider: provider, api_key: config.api_key, base_url: config.base_url, model: config.model })
            onSettingsSaved?.()
        } catch (error) {
            logger.error('Failed to save LLM settings:', error)
        }
    }, [onSettingsSaved])

    const debouncedSave = useCallback((provider: string, config: ProviderConfig) => {
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => saveSettings(provider, config), 300)
    }, [saveSettings])

    useEffect(() => () => { if (debounceRef.current) clearTimeout(debounceRef.current) }, [])

    const handleProviderChange = useCallback((provider: string) => {
        setActiveProvider(provider)
        setProviderConfigs((prev) => {
            const config = prev[provider]
            if (config) saveSettings(provider, config)
            return prev
        })
    }, [saveSettings])

    const updateProviderConfig = useCallback((updates: Partial<ProviderConfig>) => {
        setProviderConfigs((prev) => {
            const provider = activeProviderRef.current
            const existing = prev[provider]
            if (!existing) return prev
            const updated = { ...existing, ...updates }
            debouncedSave(provider, updated)
            return { ...prev, [provider]: updated }
        })
    }, [debouncedSave])

    return {
        activeProvider,
        providerConfigs,
        isLoading,
        handleProviderChange,
        updateProviderConfig,
    }
}
