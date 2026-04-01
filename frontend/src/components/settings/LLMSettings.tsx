import { useState, useEffect, useCallback, useRef } from 'react'
import { Info } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { GetConfig, UpdateLLMSettings } from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'

interface Provider {
  type: string
  api_key: string
  base_url: string
  model: string
}

interface LLMDefaults {
  max_tokens: number
  temperature: number
}

interface LLMConfig {
  default_provider: string
  providers: Record<string, Provider>
  defaults: LLMDefaults
}

export function LLMSettings() {
  const [config, setConfig] = useState<LLMConfig | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const debounceRef = useRef<NodeJS.Timeout | null>(null)

  const loadConfig = useCallback(async () => {
    try {
      const result = await GetConfig()
      const llmConfig = result?.llm as LLMConfig | undefined
      if (llmConfig) {
        setConfig(llmConfig)
      }
    } catch {
      // Keep null if fetch fails
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadConfig()
  }, [loadConfig])

  const saveSettings = useCallback(async (newConfig: LLMConfig) => {
    try {
      const defaultProv = newConfig.providers[newConfig.default_provider]
      const request = new main.LLMSettingsRequest({
        default_provider: newConfig.default_provider,
        model: defaultProv?.model || '',
        defaults: newConfig.defaults,
      })
      await UpdateLLMSettings(request)
    } catch {
      // Handle error silently
    }
  }, [])

  const updateSettings = useCallback(
    (newConfig: LLMConfig) => {
      setConfig(newConfig)

      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
      }
      debounceRef.current = setTimeout(() => {
        saveSettings(newConfig)
      }, 300)
    },
    [saveSettings]
  )

  const handleDefaultProviderChange = (provider: string) => {
    if (!config) return
    const newConfig: LLMConfig = {
      ...config,
      default_provider: provider,
    }
    updateSettings(newConfig)
  }

  const handleModelChange = (model: string) => {
    if (!config) return
    const currentProvider = config.default_provider
    const newConfig: LLMConfig = {
      ...config,
      providers: {
        ...config.providers,
        [currentProvider]: {
          ...config.providers[currentProvider],
          model,
        },
      },
    }
    updateSettings(newConfig)
  }

  const handleDefaultsChange = (key: keyof LLMDefaults, value: number) => {
    if (!config) return
    const newConfig: LLMConfig = {
      ...config,
      defaults: {
        ...config.defaults,
        [key]: value,
      },
    }
    updateSettings(newConfig)
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading LLM settings...</span>
      </div>
    )
  }

  if (!config) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Failed to load LLM settings</span>
      </div>
    )
  }

  const providerNames = Object.keys(config.providers)
  const currentProvider = config.providers[config.default_provider]

  return (
    <div className="flex flex-col gap-6">
      <div className="text-sm text-muted-foreground">
        Configure the LLM provider and model used for all agent tasks. Changes apply immediately.
      </div>

      {/* Provider & Model Section */}
      <div className="flex flex-col gap-4">
        <span className="text-sm font-medium">Provider & Model</span>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <label className="text-xs text-muted-foreground">Default Provider</label>
            <div className="flex items-center gap-3">
              <select
                value={config.default_provider}
                onChange={(e) => handleDefaultProviderChange(e.target.value)}
                className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring min-w-[180px]"
              >
                {providerNames.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
              {currentProvider && (
                <Badge variant="secondary" className="text-xs">
                  {currentProvider.type}
                </Badge>
              )}
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <label className="text-xs text-muted-foreground">Model</label>
            <Input
              placeholder="Model name (e.g., gpt-4o, claude-sonnet-4-20250514)"
              value={currentProvider?.model || ''}
              onChange={(e) => handleModelChange(e.target.value)}
              className="h-9 text-sm"
            />
          </div>
        </div>
      </div>

      {/* Defaults Section */}
      <div className="flex flex-col gap-3">
        <span className="text-sm font-medium">Default Parameters</span>
        <div className="flex gap-4">
          <div className="flex flex-col gap-2 flex-1">
            <label className="text-xs text-muted-foreground">Max Tokens</label>
            <Input
              type="number"
              min={1}
              max={32768}
              value={config.defaults.max_tokens}
              onChange={(e) =>
                handleDefaultsChange('max_tokens', parseInt(e.target.value, 10) || 0)
              }
              className="h-9 text-sm"
            />
          </div>
          <div className="flex flex-col gap-2 flex-1">
            <label className="text-xs text-muted-foreground">Temperature</label>
            <Input
              type="number"
              min={0}
              max={2}
              step={0.1}
              value={config.defaults.temperature}
              onChange={(e) =>
                handleDefaultsChange('temperature', parseFloat(e.target.value) || 0)
              }
              className="h-9 text-sm"
            />
          </div>
        </div>
      </div>

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          A single model is used for all agent tasks (routing, planning, execution, evaluation).
          Temperature controls randomness (0 = deterministic, 2 = very random). Max tokens limits
          response length.
        </p>
      </div>
    </div>
  )
}
