import { useState, useEffect, useCallback, useRef } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { GetConfig, UpdateSearchSettings } from '../../../wailsjs/go/main/App'

interface SearchConfig {
  provider: string
  api_key: string
}

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  tavily: 'Tavily',
}

const PROVIDER_KEYS = ['tavily']

export function SearchSettings() {
  const [config, setConfig] = useState<SearchConfig>({
    provider: 'tavily',
    api_key: '',
  })
  const [isLoading, setIsLoading] = useState(true)
  const [apiKeyInput, setApiKeyInput] = useState('')
  const [isApiKeyFocused, setIsApiKeyFocused] = useState(false)
  const saveTimeoutRef = useRef<NodeJS.Timeout | null>(null)

  useEffect(() => {
    const loadConfig = async () => {
      try {
        const result = await GetConfig()
        const searchConfig = result.search as SearchConfig
        if (searchConfig) {
          setConfig(searchConfig)
          // Initialize input with masked value if key exists
          setApiKeyInput(searchConfig.api_key === '***configured***' ? '' : searchConfig.api_key)
        }
      } catch {
        // Keep defaults if fetch fails
      } finally {
        setIsLoading(false)
      }
    }
    loadConfig()
  }, [])

  const saveSettings = useCallback(
    async (newConfig: SearchConfig) => {
      try {
        await UpdateSearchSettings({
          provider: newConfig.provider,
          api_key: newConfig.api_key,
        })
      } catch {
        // Handle error silently
      }
    },
    []
  )

  const debouncedSave = useCallback(
    (newConfig: SearchConfig) => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
      saveTimeoutRef.current = setTimeout(() => {
        saveSettings(newConfig)
      }, 500)
    },
    [saveSettings]
  )

  useEffect(() => {
    return () => {
      if (saveTimeoutRef.current) {
        clearTimeout(saveTimeoutRef.current)
      }
    }
  }, [])

  const handleProviderChange = (value: string) => {
    const newConfig = { ...config, provider: value }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleApiKeyChange = (value: string) => {
    setApiKeyInput(value)
    // If user clears the field, send "***configured***" to keep existing
    // Otherwise send the new value
    const apiKeyToSave = value.trim() === '' ? '***configured***' : value
    const newConfig = { ...config, api_key: apiKeyToSave }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleApiKeyFocus = () => {
    setIsApiKeyFocused(true)
    // Clear the input when user starts typing (if it was masked)
    if (config.api_key === '***configured***') {
      setApiKeyInput('')
    }
  }

  const handleApiKeyBlur = () => {
    setIsApiKeyFocused(false)
    // If user left field empty and there was a key, revert to masked state
    if (apiKeyInput.trim() === '' && config.api_key === '***configured***') {
      setApiKeyInput('')
    }
  }

  const getApiKeyPlaceholder = () => {
    if (config.api_key === '***configured***' && !isApiKeyFocused) {
      return '••••••••••••••••'
    }
    return 'Enter API key'
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading search settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {/* Provider */}
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Search Provider</label>
        <div className="flex items-center gap-3">
          <select
            value={config.provider}
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

      {/* Warning if API key not configured */}
      {config.api_key !== '***configured***' && apiKeyInput.trim() === '' && (
        <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
          <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <p>Search provider API key is not configured. Web search will not function without it.</p>
        </div>
      )}

      {/* API Key */}
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">API Key</label>
        <div className="flex items-center gap-3">
          <Input
            type={apiKeyInput.startsWith('${') ? 'text' : 'password'}
            placeholder={getApiKeyPlaceholder()}
            value={apiKeyInput}
            onChange={(e) => handleApiKeyChange(e.target.value)}
            onFocus={handleApiKeyFocus}
            onBlur={handleApiKeyBlur}
            className="h-9 text-sm flex-1"
          />
        </div>
        <p className="text-xs text-muted-foreground">
          {config.api_key === '***configured***'
            ? 'API key is configured. Enter a new value to change it.'
            : 'Enter your API key for the search provider.'}
        </p>
      </div>
    </div>
  )
}
