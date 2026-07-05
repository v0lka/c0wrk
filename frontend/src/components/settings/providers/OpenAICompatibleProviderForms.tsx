import { ProviderAccordion } from './ProviderAccordion'
import type { ProviderConfig } from './ProviderAccordion'

interface OpenAICompatibleProviderFormProps {
  providerNames: Set<string>
  providerConfigs: Record<string, ProviderConfig>
  expandedProviders: Set<string>
  onToggle: (provider: string) => void
  onConfigChange: (provider: string, updates: Partial<{ api_key: string; base_url: string }>) => void
  onToggleModel: (provider: string, model: string) => void
  onDelete: (provider: string) => void
  defaultModel: string
  /** Label prefix shown for each provider accordion. Defaults to "OpenAI Compatible". */
  labelPrefix?: string
}

export function OpenAICompatibleProviderForms({
  providerNames,
  providerConfigs,
  expandedProviders,
  onToggle,
  onConfigChange,
  onToggleModel,
  onDelete,
  defaultModel,
  labelPrefix = 'OpenAI Compatible',
}: OpenAICompatibleProviderFormProps) {
  return (
    <>
      {[...providerNames].sort().map((name) => {
        const config = providerConfigs[name]
        if (!config) return null
        const isExpanded = expandedProviders.has(name)
        const label = `${labelPrefix}: ${name}`

        return (
          <ProviderAccordion
            key={name}
            provider={name}
            label={label}
            config={config}
            isExpanded={isExpanded}
            onToggle={() => onToggle(name)}
            onConfigChange={(updates) => onConfigChange(name, updates)}
            onToggleModel={(model) => onToggleModel(name, model)}
            onDelete={() => onDelete(name)}
            defaultModel={defaultModel}
            providerConfigs={providerConfigs}
          />
        )
      })}
    </>
  )
}
