import { useState, useMemo } from 'react'
import { ProviderConfigForm } from './ProviderConfigForm'
import { useLLMConfig } from './useLLMConfig'
import { useModelFetch } from './useModelFetch'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { PROVIDERS, PROVIDER_LABELS } from '@/lib/llm-providers'

export function LLMSettings({ onSettingsSaved, onDefaultModelChange }: { onSettingsSaved?: () => void; onDefaultModelChange?: (model: string) => void }) {
  const {
    defaultModel,
    providerConfigs,
    isLoading,
    setDefaultModel,
    updateProviderConfig,
    toggleModel,
  } = useLLMConfig(onSettingsSaved, onDefaultModelChange)

  const [expandedProviders, setExpandedProviders] = useState<Set<string>>(new Set())

  const toggleExpanded = (provider: string) => {
    setExpandedProviders((prev) => {
      const next = new Set(prev)
      if (next.has(provider)) next.delete(provider)
      else next.add(provider)
      return next
    })
  }

  // Collect all enabled models across all providers for the global default dropdown.
  const allEnabledModels = useMemo(() => {
    const result: { model: string; provider: string }[] = []
    for (const p of PROVIDERS) {
      const cfg = providerConfigs[p]
      if (cfg) {
        for (const m of cfg.models) {
          result.push({ model: m, provider: p })
        }
      }
    }
    return result
  }, [providerConfigs])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading LLM settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {/* Global Default Model */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium">Default Model</label>
        <select
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          value={defaultModel}
          onChange={(e) => setDefaultModel(e.target.value)}
        >
          <option value="">— Select a default model —</option>
          {allEnabledModels.map(({ model, provider }) => (
            <option key={`${provider}:${model}`} value={model}>{model}</option>
          ))}
        </select>
        <p className="text-xs text-muted-foreground">
          The default model is used when no per-message override is set. It must be enabled in at least one provider below.
        </p>
      </div>

      {/* Provider Accordions */}
      {PROVIDERS.map((provider) => {
        const config = providerConfigs[provider]
        if (!config) return null
        const isExpanded = expandedProviders.has(provider)

        return (
          <ProviderAccordion
            key={provider}
            provider={provider}
            label={PROVIDER_LABELS[provider] || provider}
            config={config}
            isExpanded={isExpanded}
            onToggle={() => toggleExpanded(provider)}
            onConfigChange={(updates) => updateProviderConfig(provider, updates)}
            onToggleModel={(model) => toggleModel(provider, model)}
            defaultModel={defaultModel}
          />
        )
      })}
    </div>
  )
}

function ProviderAccordion({
  provider,
  label,
  config,
  isExpanded,
  onToggle,
  onConfigChange,
  onToggleModel,
  defaultModel,
}: {
  provider: string
  label: string
  config: { api_key: string; base_url: string; models: string[] }
  isExpanded: boolean
  onToggle: () => void
  onConfigChange: (updates: Partial<{ api_key: string; base_url: string }>) => void
  onToggleModel: (model: string) => void
  defaultModel: string
}) {
  const {
    models,
    modelsLoading,
    modelsError,
    apiKeyDirty,
    handleApply,
    hasRequiredCredentials,
  } = useModelFetch(provider, { [provider]: config })

  return (
    <div className="rounded-lg border">
      <button
        type="button"
        className="flex w-full items-center justify-between px-4 py-3 text-sm font-medium hover:bg-muted/50"
        onClick={onToggle}
      >
        <span className="flex items-center gap-2">
          {isExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          {label}
        </span>
        <span className="text-xs text-muted-foreground">
          {config.models.length} model{config.models.length !== 1 ? 's' : ''} enabled
        </span>
      </button>

      {isExpanded && (
        <div className="flex flex-col gap-4 border-t px-4 py-4">
          <ProviderConfigForm
            activeProvider={provider}
            config={config}
            apiKeyDirty={apiKeyDirty}
            hasRequiredCredentials={hasRequiredCredentials}
            modelsLoading={modelsLoading}
            onConfigChange={onConfigChange}
            onApply={handleApply}
          />

          {/* Model Checklist */}
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium">Enabled Models</label>
            {modelsLoading && (
              <span className="text-xs text-muted-foreground">Fetching models...</span>
            )}
            {modelsError && (
              <span className="text-xs text-destructive">{modelsError}</span>
            )}
            <div className="flex max-h-48 flex-col gap-1 overflow-y-auto rounded-md border p-2">
              {models.length === 0 && !modelsLoading && (
                <span className="text-xs text-muted-foreground">
                  {hasRequiredCredentials
                    ? 'No models available. Click "Fetch Models" to load them.'
                    : 'Configure API key and click "Fetch Models" to load available models.'}
                </span>
              )}
              {models.map((model) => {
                const isEnabled = config.models.includes(model)
                const isDefault = model === defaultModel
                return (
                  <label
                    key={model}
                    className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs hover:bg-muted"
                  >
                    <input
                      type="checkbox"
                      checked={isEnabled}
                      onChange={() => onToggleModel(model)}
                      className="h-3.5 w-3.5"
                    />
                    <span className="flex-1">{model}</span>
                    {isDefault && (
                      <span className="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary">
                        default
                      </span>
                    )}
                  </label>
                )
              })}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
