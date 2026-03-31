import { useState, useEffect, useCallback, useRef } from 'react'
import { Info, Search, Key } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { GetConfig, UpdateSearchSettings } from '../../../wailsjs/go/main/App'

interface SearchConfig {
  provider: string
  api_key: string
}

export function SearchSettings() {
  const [config, setConfig] = useState<SearchConfig>({
    provider: '',
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
      <div className="text-sm text-muted-foreground">
        Configure web search provider settings. Changes apply automatically.
      </div>

      {/* Provider */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <Search className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">Search Provider</span>
        </div>
        <Input
          placeholder="e.g., tavily"
          value={config.provider}
          onChange={(e) => handleProviderChange(e.target.value)}
          className="h-9 text-sm"
        />
        <p className="text-xs text-muted-foreground">
          The search provider to use for web search functionality.
        </p>
      </div>

      {/* API Key */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <Key className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">API Key</span>
        </div>
        <Input
          type="password"
          placeholder={getApiKeyPlaceholder()}
          value={apiKeyInput}
          onChange={(e) => handleApiKeyChange(e.target.value)}
          onFocus={handleApiKeyFocus}
          onBlur={handleApiKeyBlur}
          className="h-9 text-sm"
        />
        <p className="text-xs text-muted-foreground">
          {config.api_key === '***configured***'
            ? 'API key is configured. Enter a new value to change it.'
            : 'Enter your API key for the search provider.'}
        </p>
      </div>

      {/* Info tip */}
      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>Requires a Tavily API key for web search functionality.</p>
      </div>
    </div>
  )
}
