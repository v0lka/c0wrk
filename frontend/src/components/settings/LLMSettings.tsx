import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { GetConfig, UpdateLLMSettings, ListProviderModels } from '../../../wailsjs/go/desktop/App'
import { backend } from '../../../wailsjs/go/models'
import { logger } from '@/lib/logger'
import { MASKED_API_KEY } from '@/constants/api'
import { ProviderSelector } from './ProviderSelector'
import { ProviderConfigForm } from './ProviderConfigForm'
import { ModelSelector } from './ModelSelector'

interface ProviderConfig {
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

export function LLMSettings({ onSettingsSaved }: { onSettingsSaved?: () => void }) {
  const [activeProvider, setActiveProvider] = useState<string>('')
  const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
  const [models, setModels] = useState<string[]>([])
  const [modelsLoading, setModelsLoading] = useState(false)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [apiKeyDirty, setApiKeyDirty] = useState(true)

  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  const fetchIdRef = useRef(0)

  // Compute a stable key for the current provider + credentials
  // This naturally deduplicates: model changes don't change the key, but provider/credential changes do
  const credentialKey = (!activeProvider || !providerConfigs[activeProvider])
    ? ''
    : `${activeProvider}|${providerConfigs[activeProvider].api_key}|${providerConfigs[activeProvider].base_url}`

  // Load config on mount
  const loadConfig = useCallback(async () => {
    try {
      const result = await GetConfig()
      const llmConfig = result?.llm
      if (llmConfig) {
        setActiveProvider(llmConfig.active_provider || 'anthropic')
        setProviderConfigs({
          anthropic: { api_key: llmConfig.anthropic.api_key, model: llmConfig.anthropic.model, base_url: '' },
          gemini: { api_key: llmConfig.gemini.api_key, model: llmConfig.gemini.model, base_url: '' },
          lmstudio: llmConfig.lmstudio ?? { api_key: '', base_url: '', model: '' },
          openai_compatible: llmConfig.openai_compatible ?? { api_key: '', base_url: '', model: '' },
          chatgpt: { api_key: llmConfig.chatgpt.api_key, model: llmConfig.chatgpt.model, base_url: '' },
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

  useEffect(() => {
    loadConfig()
  }, [loadConfig])

  // Reset state when provider or credentials change
  useEffect(() => {
    fetchIdRef.current += 1
    setModels([])
    setModelsError(null)
    setApiKeyDirty(true)
    // credentialKey captures activeProvider + api_key + base_url
     
  }, [credentialKey])

  // Determine if required credentials are filled in for the active provider
  const hasRequiredCredentials = useMemo(() => {
    if (!activeProvider || !providerConfigs[activeProvider]) return false
    const config = providerConfigs[activeProvider]
    const needsBaseUrl = activeProvider === 'lmstudio' || activeProvider === 'openai_compatible'
    const needsApiKey = activeProvider !== 'lmstudio'
    if (needsBaseUrl && !config.base_url) return false
    if (needsApiKey && !config.api_key) return false
    return true
  }, [activeProvider, providerConfigs])

  // Handle Apply button click — fetch models
  const handleApply = useCallback(async () => {
    if (!activeProvider) return
    const myFetchId = ++fetchIdRef.current
    setModelsLoading(true)
    setModelsError(null)
    try {
      const modelList = await ListProviderModels(activeProvider)
      if (myFetchId !== fetchIdRef.current) return
      setModels(modelList || [])
      setApiKeyDirty(false)
    } catch (err) {
      if (myFetchId !== fetchIdRef.current) return
      setModelsError(err instanceof Error ? err.message : String(err))
      setModels([])
    } finally {
      if (myFetchId === fetchIdRef.current) {
        setModelsLoading(false)
      }
    }
  }, [activeProvider])

  // Save settings
  const saveSettings = useCallback(
    async (provider: string, config: ProviderConfig) => {
      try {
        const request = new backend.LLMSettingsRequest({
          active_provider: provider,
          api_key: config.api_key,
          base_url: config.base_url,
          model: config.model,
        })
        await UpdateLLMSettings(request)
        onSettingsSaved?.()
      } catch (error) {
        logger.error('Failed to save LLM settings:', error)
      }
    },
    [onSettingsSaved]
  )

  // Debounced save
  const debouncedSave = useCallback(
    (provider: string, config: ProviderConfig) => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
      }
      debounceRef.current = setTimeout(() => {
        saveSettings(provider, config)
      }, 300)
    },
    [saveSettings]
  )

  // Clean up pending debounce on unmount
  useEffect(() => {
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
      }
    }
  }, [])

  const handleProviderChange = (provider: string) => {
    setActiveProvider(provider)
    // Save immediately when changing provider
    if (providerConfigs[provider]) {
      saveSettings(provider, providerConfigs[provider])
    }
  }

  const updateProviderConfig = (updates: Partial<ProviderConfig>) => {
    setProviderConfigs((prev) => {
      const existing = prev[activeProvider]
      if (!existing) return prev
      const updated: ProviderConfig = {
        ...existing,
        ...updates,
      }
      const newConfig = {
        ...prev,
        [activeProvider]: updated,
      }
      debouncedSave(activeProvider, updated)
      return newConfig
    })
  }

  const isModelInputDisabled = (): boolean => {
    const config = providerConfigs[activeProvider]
    if (!config) return true

    switch (activeProvider) {
      case 'anthropic':
      case 'gemini':
      case 'chatgpt':
        return !config.api_key && config.api_key !== MASKED_API_KEY
      case 'lmstudio':
        return !config.base_url
      case 'openai_compatible':
        return !config.base_url || (!config.api_key && config.api_key !== MASKED_API_KEY)
      default:
        return true
    }
  }

  const getModelInputPlaceholder = (): string => {
    if (isModelInputDisabled()) {
      switch (activeProvider) {
        case 'anthropic':
        case 'gemini':
        case 'chatgpt':
          return 'Enter API key first'
        case 'lmstudio':
          return 'Enter base URL first'
        case 'openai_compatible':
          return 'Enter base URL and API key first'
        default:
          return 'Select provider first'
      }
    }
    return modelsLoading ? 'Loading models...' : 'Select or type model name'
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading LLM settings...</span>
      </div>
    )
  }

  const currentConfig = providerConfigs[activeProvider]

  return (
    <div className="flex flex-col gap-6">
      {/* Provider Selection */}
      <ProviderSelector
        activeProvider={activeProvider}
        onProviderChange={handleProviderChange}
      />

      {/* Provider-specific fields + Model selection */}
      {currentConfig && (
        <div className="flex flex-col gap-4">
          <ProviderConfigForm
            activeProvider={activeProvider}
            config={currentConfig}
            apiKeyDirty={apiKeyDirty}
            hasRequiredCredentials={hasRequiredCredentials}
            modelsLoading={modelsLoading}
            onConfigChange={updateProviderConfig}
            onApply={handleApply}
          />

          <ModelSelector
            activeProvider={activeProvider}
            model={currentConfig?.model ?? ''}
            models={models}
            modelsLoading={modelsLoading}
            modelsError={modelsError}
            disabled={isModelInputDisabled()}
            placeholder={getModelInputPlaceholder()}
            onModelChange={(model) => updateProviderConfig({ model })}
          />
        </div>
      )}
    </div>
  )
}
