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
}

interface RoleSetting {
  provider: string
  model: string
}

interface LLMDefaults {
  max_tokens: number
  temperature: number
}

interface LLMConfig {
  providers: Record<string, Provider>
  roles: Record<string, RoleSetting>
  defaults: LLMDefaults
}

const roleLabels: Record<string, string> = {
  router: 'Router',
  planner: 'Planner',
  evaluator_judge: 'Evaluator Judge',
  executor: 'Executor',
  summarizer: 'Summarizer',
}

const roleDescriptions: Record<string, string> = {
  router: 'Routes user queries to appropriate handlers',
  planner: 'Creates execution plans for complex tasks',
  evaluator_judge: 'Evaluates tool execution results',
  executor: 'Executes planned tasks and actions',
  summarizer: 'Summarizes conversation context',
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
      const request = new main.LLMSettingsRequest({
        roles: newConfig.roles,
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

  const handleRoleProviderChange = (role: string, provider: string) => {
    if (!config) return
    const newConfig: LLMConfig = {
      ...config,
      roles: {
        ...config.roles,
        [role]: {
          ...config.roles[role],
          provider,
        },
      },
    }
    updateSettings(newConfig)
  }

  const handleRoleModelChange = (role: string, model: string) => {
    if (!config) return
    const newConfig: LLMConfig = {
      ...config,
      roles: {
        ...config.roles,
        [role]: {
          ...config.roles[role],
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
  const roleKeys = Object.keys(roleLabels)

  return (
    <div className="flex flex-col gap-6">
      <div className="text-sm text-muted-foreground">
        Configure LLM providers and role assignments. Changes apply immediately.
      </div>

      {/* Providers Section */}
      <div className="flex flex-col gap-3">
        <span className="text-sm font-medium">Configured Providers</span>
        <div className="flex flex-wrap gap-2">
          {providerNames.map((name) => (
            <div
              key={name}
              className="flex items-center gap-2 px-3 py-1.5 bg-muted rounded-md"
            >
              <span className="text-sm font-medium">{name}</span>
              <Badge variant="secondary" className="text-xs">
                {config.providers[name].type}
              </Badge>
            </div>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">
          Providers are configured in your config.yaml file
        </p>
      </div>

      {/* Roles Section */}
      <div className="flex flex-col gap-4">
        <span className="text-sm font-medium">Role Assignments</span>
        <div className="flex flex-col gap-4">
          {roleKeys.map((role) => (
            <div key={role} className="flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium">{roleLabels[role]}</span>
                <span className="text-xs text-muted-foreground">
                  {roleDescriptions[role]}
                </span>
              </div>
              <div className="flex gap-3">
                <select
                  value={config.roles[role]?.provider || ''}
                  onChange={(e) => handleRoleProviderChange(role, e.target.value)}
                  className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring min-w-[140px]"
                >
                  {providerNames.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
                <Input
                  placeholder="Model name (e.g., gpt-4o)"
                  value={config.roles[role]?.model || ''}
                  onChange={(e) => handleRoleModelChange(role, e.target.value)}
                  className="h-9 text-sm flex-1"
                />
              </div>
            </div>
          ))}
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
          Each role can use a different provider and model. Temperature controls
          randomness (0 = deterministic, 2 = very random). Max tokens limits response
          length.
        </p>
      </div>
    </div>
  )
}
