import { useState, useEffect, useCallback, useRef } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { getConfig, updateSearchSettings, MASKED_API_KEY } from '@/api/config'
import { logger } from '@/lib/logger'

interface SearchConfig {
  provider: string
  api_key: string
}

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  tavily: 'Tavily',
  brave: 'Brave Search',
  exa: 'Exa AI',
  duckduckgo: 'DuckDuckGo',
}

const PROVIDER_KEYS = ['tavily', 'brave', 'exa', 'duckduckgo']
const NO_API_KEY_PROVIDERS = ['duckduckgo']

export function SearchSettings() {
  const [config, setConfig] = useState<SearchConfig>({ provider: 'tavily', api_key: '' })
  const [isLoading, setIsLoading] = useState(true)
  const [apiKeyInput, setApiKeyInput] = useState('')
  const [isApiKeyFocused, setIsApiKeyFocused] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const saveTimeoutRef = useRef<NodeJS.Timeout | null>(null)

  useEffect(() => {
    const load = async () => {
      try {
        const result = await getConfig()
        if (result?.search) {
          setConfig(result.search)
          setApiKeyInput(result.search.api_key === MASKED_API_KEY ? '' : result.search.api_key)
        }
      } catch (err) {
        logger.error('Failed to load search config:', err)
      } finally {
        setIsLoading(false)
      }
    }
    load()
  }, [])

  const saveSettings = useCallback(async (newConfig: SearchConfig) => {
    try {
      await updateSearchSettings({ provider: newConfig.provider, api_key: newConfig.api_key })
      setSaveError(null)
    } catch (err) {
      logger.error('Failed to save search settings:', err)
      setSaveError('Failed to save search settings')
    }
  }, [])

  const debouncedSave = useCallback((newConfig: SearchConfig) => {
    if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current)
    saveTimeoutRef.current = setTimeout(() => saveSettings(newConfig), 500)
  }, [saveSettings])

  useEffect(() => () => { if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current) }, [])

  const handleProviderChange = (value: string) => {
    const newConfig = { ...config, provider: value }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleApiKeyChange = (value: string) => {
    setApiKeyInput(value)
    const apiKeyToSave = value.trim() === '' ? MASKED_API_KEY : value
    const newConfig = { ...config, api_key: apiKeyToSave }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleApiKeyFocus = () => {
    setIsApiKeyFocused(true)
    if (config.api_key === MASKED_API_KEY) setApiKeyInput('')
  }

  const handleApiKeyBlur = () => setIsApiKeyFocused(false)

  const getPlaceholder = () =>
    config.api_key === MASKED_API_KEY && !isApiKeyFocused ? '••••••••••••••••' : 'Enter API key'

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading search settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Search Provider</label>
        <select
          value={config.provider}
          onChange={(e) => handleProviderChange(e.target.value)}
          className="c0-input h-9 px-3 rounded-md border border-input text-sm focus:outline-none min-w-[180px]"
        >
          {PROVIDER_KEYS.map((key) => (
            <option key={key} value={key}>{PROVIDER_DISPLAY_NAMES[key]}</option>
          ))}
        </select>
      </div>

      {config.api_key !== MASKED_API_KEY && apiKeyInput.trim() === '' && !NO_API_KEY_PROVIDERS.includes(config.provider) && (
        <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
          <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <p>Search provider API key is not configured.</p>
        </div>
      )}

      {!NO_API_KEY_PROVIDERS.includes(config.provider) && (
        <div className="flex flex-col gap-2">
          <label className="text-xs text-muted-foreground">API Key</label>
          <Input
            type={apiKeyInput.startsWith('${') ? 'text' : 'password'}
            placeholder={getPlaceholder()}
            value={apiKeyInput}
            onChange={(e) => handleApiKeyChange(e.target.value)}
            onFocus={handleApiKeyFocus}
            onBlur={handleApiKeyBlur}
            className="h-9 text-sm"
          />
          <p className="text-xs text-muted-foreground">
            {config.api_key === MASKED_API_KEY
              ? 'API key is configured. Enter a new value to change it.'
              : 'Enter your API key for the search provider.'}
          </p>
        </div>
      )}

      {saveError && <p className="text-sm text-destructive mt-2">{saveError}</p>}
    </div>
  )
}
