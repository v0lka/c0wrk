import { useState, useEffect, useCallback, useRef } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Combobox } from '@/components/ui/combobox'
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
  const pendingConfigRef = useRef<SearchConfig | null>(null)

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
    pendingConfigRef.current = newConfig
    if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current)
    saveTimeoutRef.current = setTimeout(() => {
      saveTimeoutRef.current = null
      pendingConfigRef.current = null
      void saveSettings(newConfig)
    }, 500)
  }, [saveSettings])

  // Flush a pending debounced save on unmount so a user who edits the search
  // provider / API key and closes the modal (or switches tabs) within the
  // debounce window does not lose the change. Safe against half-typed keys:
  // pendingConfigRef only ever holds COMMITTED values (a key edit enters the
  // save flow on blur/Enter, never per keystroke), so the flush cannot
  // persist an unfinished fragment over the stored key.
  useEffect(() => () => {
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current)
      saveTimeoutRef.current = null
    }
    const pending = pendingConfigRef.current
    pendingConfigRef.current = null
    if (pending) void saveSettings(pending)
  }, [saveSettings])

  // commitApiKeyDraft moves an API-key edit from the local input draft into
  // the saved config. Keystrokes NEVER schedule a save: the stored key is
  // invisible while masked (the input renders empty), so a half-typed
  // fragment autosaved mid-typing would overwrite the real key with a value
  // the user never finished entering. The explicit commit (blur or Enter) is
  // the single point where a key edit becomes saveable — the debounced save
  // and its unmount flush therefore only ever carry committed values.
  const commitApiKeyDraft = () => {
    setIsApiKeyFocused(false)
    // Empty input maps to the masked sentinel: the backend keeps the stored
    // key on it (an empty field must not be able to clear a configured key).
    const committed = apiKeyInput.trim() === '' ? MASKED_API_KEY : apiKeyInput
    if (committed === config.api_key) return
    const newConfig = { ...config, api_key: committed }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleProviderChange = (value: string) => {
    const newConfig = { ...config, provider: value }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleApiKeyChange = (value: string) => {
    setApiKeyInput(value)
  }

  const handleApiKeyFocus = () => {
    setIsApiKeyFocused(true)
    if (config.api_key === MASKED_API_KEY) setApiKeyInput('')
  }

  const handleApiKeyBlur = () => commitApiKeyDraft()

  const handleApiKeyKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      commitApiKeyDraft()
    }
  }

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
        <Combobox
          ariaLabel="Search provider"
          value={config.provider}
          onChange={handleProviderChange}
          className="min-w-[180px]"
          options={PROVIDER_KEYS.map((key) => ({ value: key, label: PROVIDER_DISPLAY_NAMES[key] ?? key }))}
        />
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
            onKeyDown={handleApiKeyKeyDown}
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
