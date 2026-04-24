import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { getConfig, updateLLMSettings, MASKED_API_KEY } from '@/api/config'
import { listProviderModels } from '@/api/mcp'
import { logger } from '@/lib/logger'
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
  const [activeProvider, setActiveProvider] = useState('')
  const [providerConfigs, setProviderConfigs] = useState<Record<string, ProviderConfig>>({})
  const [models, setModels] = useState<string[]>([])
  const [modelsLoading, setModelsLoading] = useState(false)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [apiKeyDirty, setApiKeyDirty] = useState(true)
  const debounceRef = useRef<NodeJS.Timeout | null>(null)
  const fetchIdRef = useRef(0)

  const credentialKey = (!activeProvider || !providerConfigs[activeProvider])
    ? ''
    : `${activeProvider}|${providerConfigs[activeProvider].api_key}|${providerConfigs[activeProvider].base_url}`

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

  const handleProviderChange = (provider: string) => {
    setActiveProvider(provider)
    if (providerConfigs[provider]) saveSettings(provider, providerConfigs[provider])
  }

  const updateProviderConfig = (updates: Partial<ProviderConfig>) => {
    setProviderConfigs((prev) => {
      const existing = prev[activeProvider]
      if (!existing) return prev
      const updated = { ...existing, ...updates }
      debouncedSave(activeProvider, updated)
      return { ...prev, [activeProvider]: updated }
    })
  }

  const isModelDisabled = (): boolean => {
    const config = providerConfigs[activeProvider]
    if (!config) return true
    if (activeProvider === 'lmstudio') return !config.base_url
    if (activeProvider === 'openai_compatible') return !config.base_url || (!config.api_key && config.api_key !== MASKED_API_KEY)
    return !config.api_key && config.api_key !== MASKED_API_KEY
  }

  const getModelPlaceholder = (): string => {
    if (isModelDisabled()) {
      if (activeProvider === 'lmstudio') return 'Enter base URL first'
      if (activeProvider === 'openai_compatible') return 'Enter base URL and API key first'
      return 'Enter API key first'
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
      <ProviderSelector activeProvider={activeProvider} onProviderChange={handleProviderChange} />
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
            model={currentConfig.model}
            models={models}
            modelsLoading={modelsLoading}
            modelsError={modelsError}
            disabled={isModelDisabled()}
            placeholder={getModelPlaceholder()}
            onModelChange={(model) => updateProviderConfig({ model })}
          />
        </div>
      )}
    </div>
  )
}
