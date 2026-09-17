import { ProviderAccordion } from './ProviderAccordion'
import type { ProviderConfig } from './ProviderAccordion'
import { FIXED_PROVIDERS, PROVIDER_LABELS } from '@/lib/llm-providers'

interface FixedProviderFormProps {
  providerConfigs: Record<string, ProviderConfig>
  expandedProviders: Set<string>
  onToggle: (provider: string) => void
  onConfigChange: (provider: string, updates: Partial<{ api_key: string; base_url: string }>) => void
  onToggleModel: (provider: string, model: string) => void
  defaultModel: string
  /** Effective proxy state (ADR-053): disables the per-provider TLS pin UI. */
  proxyActive?: boolean
}

export function FixedProviderForms({
  providerConfigs,
  expandedProviders,
  onToggle,
  onConfigChange,
  onToggleModel,
  defaultModel,
  proxyActive = false,
}: FixedProviderFormProps) {
  return (
    <>
      {FIXED_PROVIDERS.map((provider) => {
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
            onToggle={() => onToggle(provider)}
            onConfigChange={(updates) => onConfigChange(provider, updates)}
            onToggleModel={(model) => onToggleModel(provider, model)}
            defaultModel={defaultModel}
            providerConfigs={providerConfigs}
            proxyActive={proxyActive}
          />
        )
      })}
    </>
  )
}
