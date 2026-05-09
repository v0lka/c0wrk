import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { listProviderModels } from '@/api/mcp'
import { MASKED_API_KEY } from '@/api/config'
import type { ProviderConfig } from './useLLMConfig'

interface UseModelFetchResult {
    models: string[]
    modelsLoading: boolean
    modelsError: string | null
    apiKeyDirty: boolean
    handleApply: () => Promise<void>
    hasRequiredCredentials: boolean
    isModelDisabled: boolean
    modelPlaceholder: string
}

export function useModelFetch(activeProvider: string, providerConfigs: Record<string, ProviderConfig>): UseModelFetchResult {
    const [models, setModels] = useState<string[]>([])
    const [modelsLoading, setModelsLoading] = useState(false)
    const [modelsError, setModelsError] = useState<string | null>(null)
    const [apiKeyDirty, setApiKeyDirty] = useState(true)
    const fetchIdRef = useRef(0)

    const credentialKey = (!activeProvider || !providerConfigs[activeProvider])
        ? ''
        : `${activeProvider}|${providerConfigs[activeProvider].api_key}|${providerConfigs[activeProvider].base_url}`

    useEffect(() => {
        fetchIdRef.current += 1
        setModels([])
        setModelsError(null)
        setApiKeyDirty(true)
    }, [credentialKey])

    const hasRequiredCredentials = useMemo(() => {
        if (!activeProvider || !providerConfigs[activeProvider]) return false
        const config = providerConfigs[activeProvider]
        const needsBaseUrl = activeProvider === 'lmstudio' || activeProvider === 'openai_compatible'
        const needsApiKey = activeProvider !== 'lmstudio'
        if (needsBaseUrl && !config.base_url) return false
        if (needsApiKey && !config.api_key) return false
        return true
    }, [activeProvider, providerConfigs])

    const handleApply = useCallback(async () => {
        if (!activeProvider) return
        const myId = ++fetchIdRef.current
        setModelsLoading(true)
        setModelsError(null)
        try {
            const list = await listProviderModels(activeProvider)
            if (myId !== fetchIdRef.current) return
            setModels(list || [])
            setApiKeyDirty(false)
        } catch (err) {
            if (myId !== fetchIdRef.current) return
            setModelsError(err instanceof Error ? err.message : String(err))
            setModels([])
        } finally {
            if (myId === fetchIdRef.current) setModelsLoading(false)
        }
    }, [activeProvider])

    const isModelDisabled = useMemo(() => {
        const config = providerConfigs[activeProvider]
        if (!config) return true
        if (activeProvider === 'lmstudio') return !config.base_url
        if (activeProvider === 'openai_compatible') return !config.base_url || (!config.api_key && config.api_key !== MASKED_API_KEY)
        return !config.api_key && config.api_key !== MASKED_API_KEY
    }, [activeProvider, providerConfigs])

    const modelPlaceholder = useMemo(() => {
        if (isModelDisabled) {
            if (activeProvider === 'lmstudio') return 'Enter base URL first'
            if (activeProvider === 'openai_compatible') return 'Enter base URL and API key first'
            return 'Enter API key first'
        }
        return modelsLoading ? 'Loading models...' : 'Select or type model name'
    }, [isModelDisabled, activeProvider, modelsLoading])

    return {
        models,
        modelsLoading,
        modelsError,
        apiKeyDirty,
        handleApply,
        hasRequiredCredentials,
        isModelDisabled,
        modelPlaceholder,
    }
}
