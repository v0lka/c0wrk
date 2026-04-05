import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Loader2 } from 'lucide-react'
import { GetConfig, UpdateLLMSettings, ListProviderModels } from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'

interface ProviderConfig {
  api_key: string
  base_url: string
  model: string
}

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  lmstudio: 'LM Studio',
  openai_compatible: 'OpenAI Compatible',
  chatgpt: 'ChatGPT',
}

const PROVIDER_KEYS = ['anthropic', 'gemini', 'lmstudio', 'openai_compatible', 'chatgpt']

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
  const credentialKey = useMemo(() => {
    if (!activeProvider || !providerConfigs[activeProvider]) {
      return ''
    }
    const config = providerConfigs[activeProvider]
    return `${activeProvider}|${config.api_key}|${config.base_url}`
  }, [activeProvider, providerConfigs])

  // Load config on mount
  const loadConfig = useCallback(async () => {
    try {
      const result = await GetConfig()
      const llmConfig = result?.llm
      if (llmConfig) {
        setActiveProvider(llmConfig.active_provider)
        setProviderConfigs({
          anthropic: { api_key: llmConfig.anthropic.api_key, model: llmConfig.anthropic.model, base_url: '' },
          gemini: { api_key: llmConfig.gemini.api_key, model: llmConfig.gemini.model, base_url: '' },
          lmstudio: llmConfig.lmstudio,
          openai_compatible: llmConfig.openai_compatible,
          chatgpt: { api_key: llmConfig.chatgpt.api_key, model: llmConfig.chatgpt.model, base_url: '' },
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
    const myFetchId = fetchIdRef.current
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
        const request = new main.LLMSettingsRequest({
          active_provider: provider,
          api_key: config.api_key,
          base_url: config.base_url,
          model: config.model,
        })
        await UpdateLLMSettings(request)
        onSettingsSaved?.()
      } catch {
        // Handle error silently
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

      {/* Provider Selection */}
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Provider</label>
        <div className="flex items-center gap-3">
          <select
            value={activeProvider}
            onChange={(e) => handleProviderChange(e.target.value)}
            className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none min-w-[180px]"
          >
            {PROVIDER_KEYS.map((key) => (
              <option key={key} value={key}>
                {PROVIDER_DISPLAY_NAMES[key]}
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* Provider-specific fields */}
      {currentConfig && (
        <div className="flex flex-col gap-4">
          {/* Base URL - for LM Studio and OpenAI Compatible */}
          {(activeProvider === 'lmstudio' || activeProvider === 'openai_compatible') && (
            <div className="flex flex-col gap-2">
              <label className="text-xs text-muted-foreground">Base URL</label>
              <div className="flex items-center gap-2">
                <Input
                  placeholder="http://localhost:1234"
                  value={currentConfig.base_url}
                  onChange={(e) => updateProviderConfig(activeProvider, { base_url: e.target.value })}
                  className="h-9 text-sm flex-1"
                />
                {activeProvider === 'lmstudio' && apiKeyDirty && hasRequiredCredentials && (
                  <Button size="sm" onClick={handleApply} disabled={modelsLoading}>
                    {modelsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Apply'}
                  </Button>
                )}
              </div>
            </div>
          )}

          {/* API Key - for all except LM Studio */}
          {activeProvider !== 'lmstudio' && (
            <div className="flex flex-col gap-2">
              <label className="text-xs text-muted-foreground">API Key</label>
              <div className="flex items-center gap-2">
                <Input
                  type={(() => {
                    const val = currentConfig.api_key === '***configured***' ? '' : currentConfig.api_key
                    return val.startsWith('${') ? 'text' : 'password'
                  })()}
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
                {apiKeyDirty && hasRequiredCredentials && (
                  <Button size="sm" onClick={handleApply} disabled={modelsLoading}>
                    {modelsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Apply'}
                  </Button>
                )}
              </div>
            </div>
          )}

          {/* Model selection */}
          <div className="flex flex-col gap-2">
            <label className="text-xs text-muted-foreground">Model</label>
            <div className="flex flex-col gap-2">
              {models.length > 0 && !modelsLoading ? (
                // Non-editable select when models are successfully fetched
                <select
                  value={currentConfig.model}
                  onChange={(e) => updateProviderConfig(activeProvider, { model: e.target.value })}
                  disabled={isModelInputDisabled()}
                  className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none"
                >
                  <option value="">Select a model...</option>
                  {models.map((model) => (
                    <option key={model} value={model}>
                      {model}
                    </option>
                  ))}
                </select>
              ) : (
                // Plain text input when no models available or fetch failed
                <Input
                  placeholder={getModelInputPlaceholder()}
                  value={currentConfig.model}
                  onChange={(e) => updateProviderConfig(activeProvider, { model: e.target.value })}
                  disabled={isModelInputDisabled() || modelsLoading}
                  className="h-9 text-sm"
                />
              )}
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
    </div>
  )
}
