import { useState, useEffect, useCallback, useRef } from 'react'
import { Info } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { GetConfig, UpdateLLMSettings, ListProviderModels } from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'

interface ProviderConfig {
  api_key: string
  base_url: string
  model: string
}

interface LLMConfig {
  active_provider: string
  anthropic: ProviderConfig
  gemini: ProviderConfig
  lmstudio: ProviderConfig
  openai_compatible: ProviderConfig
  chatgpt: ProviderConfig
}

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  lmstudio: 'LM Studio',
  openai_compatible: 'OpenAI Compatible',
  chatgpt: 'ChatGPT',
}

const PROVIDER_KEYS = ['anthropic', 'gemini', 'lmstudio', 'openai_compatible', 'chatgpt']

export function LLMSettings() {
  const [activeProvider, setActiveProvider] = useState<string>('')
  const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
  const [models, setModels] = useState<string[]>([])
  const [modelsLoading, setModelsLoading] = useState(false)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(true)

  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  const modelsDebounceRef = useRef<NodeJS.Timeout | null>(null)

  // Load config on mount
  const loadConfig = useCallback(async () => {
    try {
      const result = await GetConfig()
      const llmConfig = result?.llm as LLMConfig | undefined
      if (llmConfig) {
        setActiveProvider(llmConfig.active_provider)
        setProviderConfigs({
          anthropic: llmConfig.anthropic,
          gemini: llmConfig.gemini,
          lmstudio: llmConfig.lmstudio,
          openai_compatible: llmConfig.openai_compatible,
          chatgpt: llmConfig.chatgpt,
        })
      }
    } catch {
      // Keep defaults if fetch fails
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadConfig()
  }, [loadConfig])

  // Fetch models when provider or credentials change
  const fetchModels = useCallback(async (provider: string, config: ProviderConfig) => {
    // Check if required fields are filled based on provider
    const needsBaseUrl = provider === 'lmstudio' || provider === 'openai_compatible'
    const needsApiKey = provider !== 'lmstudio'

    if (needsBaseUrl && !config.base_url) {
      setModels([])
      return
    }
    if (needsApiKey && !config.api_key && config.api_key !== '***configured***') {
      setModels([])
      return
    }
    if (provider === 'openai_compatible' && (!config.base_url || (!config.api_key && config.api_key !== '***configured***'))) {
      setModels([])
      return
    }

    setModelsLoading(true)
    setModelsError(null)

    try {
      const modelList = await ListProviderModels(provider)
      setModels(modelList || [])
    } catch (err) {
      setModelsError(err instanceof Error ? err.message : 'Failed to fetch models')
      setModels([])
    } finally {
      setModelsLoading(false)
    }
  }, [])

  // Debounced model fetch
  const debouncedFetchModels = useCallback(
    (provider: string, config: ProviderConfig) => {
      if (modelsDebounceRef.current) {
        clearTimeout(modelsDebounceRef.current)
      }
      modelsDebounceRef.current = setTimeout(() => {
        fetchModels(provider, config)
      }, 300)
    },
    [fetchModels]
  )

  // Fetch models when provider or credentials change
  useEffect(() => {
    if (activeProvider && providerConfigs[activeProvider]) {
      debouncedFetchModels(activeProvider, providerConfigs[activeProvider])
    }
  }, [activeProvider, providerConfigs, debouncedFetchModels])

  // Save settings
  const saveSettings = useCallback(
    async (provider: string, config: ProviderConfig) => {
      try {
        const request = new main.LLMSettingsRequest({
          active_provider: provider,
          api_key: config.api_key,
          base_url: config.base_url,
          model: config.model,
        })
        await UpdateLLMSettings(request)
      } catch {
        // Handle error silently
      }
    },
    []
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

  const handleProviderChange = (provider: string) => {
    setActiveProvider(provider)
    // Save immediately when changing provider
    if (providerConfigs[provider]) {
      saveSettings(provider, providerConfigs[provider])
    }
  }

  const updateProviderConfig = (provider: string, updates: Partial<ProviderConfig>) => {
    setProviderConfigs((prev) => {
      const newConfig = {
        ...prev,
        [provider]: {
          ...prev[provider],
          ...updates,
        },
      }
      debouncedSave(provider, newConfig[provider])
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
        return !config.api_key && config.api_key !== '***configured***'
      case 'lmstudio':
        return !config.base_url
      case 'openai_compatible':
        return !config.base_url || (!config.api_key && config.api_key !== '***configured***')
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
      <div className="text-sm text-muted-foreground">
        Configure the LLM provider and model used for all agent tasks. Changes apply immediately.
      </div>

      {/* Provider Selection */}
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Provider</label>
        <div className="flex items-center gap-3">
          <select
            value={activeProvider}
            onChange={(e) => handleProviderChange(e.target.value)}
            className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring min-w-[180px]"
          >
            {PROVIDER_KEYS.map((key) => (
              <option key={key} value={key}>
                {PROVIDER_DISPLAY_NAMES[key]}
              </option>
            ))}
          </select>
          <Badge variant="secondary" className="text-xs">
            {PROVIDER_DISPLAY_NAMES[activeProvider] || activeProvider}
          </Badge>
        </div>
      </div>

      {/* Provider-specific fields */}
      {currentConfig && (
        <div className="flex flex-col gap-4">
          {/* Base URL - for LM Studio and OpenAI Compatible */}
          {(activeProvider === 'lmstudio' || activeProvider === 'openai_compatible') && (
            <div className="flex flex-col gap-2">
              <label className="text-xs text-muted-foreground">Base URL</label>
              <Input
                placeholder="http://localhost:1234"
                value={currentConfig.base_url}
                onChange={(e) => updateProviderConfig(activeProvider, { base_url: e.target.value })}
                className="h-9 text-sm"
              />
            </div>
          )}

          {/* API Key - for all except LM Studio */}
          {activeProvider !== 'lmstudio' && (
            <div className="flex flex-col gap-2">
              <label className="text-xs text-muted-foreground">API Key</label>
              <div className="flex items-center gap-3">
                <Input
                  type="password"
                  placeholder="Enter API key"
                  value={currentConfig.api_key === '***configured***' ? '' : currentConfig.api_key}
                  onChange={(e) => updateProviderConfig(activeProvider, { api_key: e.target.value })}
                  className="h-9 text-sm flex-1"
                />
                {currentConfig.api_key === '***configured***' && (
                  <Badge variant="outline" className="text-xs">
                    Configured
                  </Badge>
                )}
              </div>
            </div>
          )}

          {/* Model selection */}
          <div className="flex flex-col gap-2">
            <label className="text-xs text-muted-foreground">Model</label>
            <div className="flex flex-col gap-2">
              <Input
                list={`models-${activeProvider}`}
                placeholder={getModelInputPlaceholder()}
                value={currentConfig.model}
                onChange={(e) => updateProviderConfig(activeProvider, { model: e.target.value })}
                disabled={isModelInputDisabled()}
                className="h-9 text-sm"
              />
              <datalist id={`models-${activeProvider}`}>
                {(models || []).map((model) => (
                  <option key={model} value={model} />
                ))}
              </datalist>
              {modelsLoading && (
                <span className="text-xs text-muted-foreground">Loading models...</span>
              )}
              {modelsError && (
                <span className="text-xs text-destructive">{modelsError}</span>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Info note */}
      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          Temperature is fixed at 0 (deterministic). Max tokens is calculated automatically based on model context window.
        </p>
      </div>
    </div>
  )
}
