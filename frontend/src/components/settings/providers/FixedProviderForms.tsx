import { ProviderAccordion } from './ProviderAccordion'
import type { ProviderConfig } from './ProviderAccordion'
import { ChatGPTAuthSection } from './ChatGPTAuthSection'
import { KimiAuthSection } from './KimiAuthSection'
import { useChatGPTModelPreset } from '../useChatGPTModelPreset'
import { useKimiModelPreset } from '../useKimiModelPreset'
import { FIXED_PROVIDERS, PROVIDER_LABELS } from '@/lib/llm-providers'

interface FixedProviderFormProps {
  providerConfigs: Record<string, ProviderConfig>
  expandedProviders: Set<string>
  onToggle: (provider: string) => void
  onConfigChange: (provider: string, updates: Partial<{ api_key: string; base_url: string; tls_fingerprint: string; auto_retry_seconds?: number; auth_mode?: 'api_key' | 'oauth'; auth_region?: 'global' | 'mainland_cn' }>) => void
  onToggleModel: (provider: string, model: string) => void
  defaultModel: string
  autoRetryMaxSeconds: number
  beforeSignIn?: () => Promise<void>
}

export function FixedProviderForms({
  providerConfigs,
  expandedProviders,
  onToggle,
  onConfigChange,
  onToggleModel,
  defaultModel,
  autoRetryMaxSeconds,
  beforeSignIn,
}: FixedProviderFormProps) {
  const chatgptIsOAuth = providerConfigs.chatgpt?.auth_mode === 'oauth'
  const chatgptModels = useChatGPTModelPreset(chatgptIsOAuth)
  const kimiIsOAuth = providerConfigs.kimi?.auth_mode === 'oauth'
  const kimiModels = useKimiModelPreset(kimiIsOAuth, providerConfigs.kimi?.auth_region ?? 'global')

  return (
    <>
      {FIXED_PROVIDERS.filter((provider) => provider !== 'kimi' || providerConfigs.kimi?.type === undefined).map((provider) => {
        const config = providerConfigs[provider]
        if (!config) return null
        const isExpanded = expandedProviders.has(provider)
        const isChatGPT = provider === 'chatgpt'
        const isKimi = provider === 'kimi'
        const isOAuth = (isChatGPT && chatgptIsOAuth) || (isKimi && kimiIsOAuth)
        const subscriptionModels = isKimi ? kimiModels : chatgptModels

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
            autoRetryMaxSeconds={autoRetryMaxSeconds}
            authSection={
              isChatGPT ? (
                <ChatGPTAuthSection
                  authMode={config.auth_mode ?? 'api_key'}
                  onAuthModeChange={(mode) => onConfigChange('chatgpt', { auth_mode: mode })}
                />
              ) : isKimi ? (
                <KimiAuthSection
                  authMode={config.auth_mode ?? 'api_key'}
                  onAuthModeChange={(mode) => onConfigChange('kimi', { auth_mode: mode })}
                  authRegion={config.auth_region ?? 'global'}
                  onAuthRegionChange={(region) => onConfigChange('kimi', { auth_region: region })}
                  beforeSignIn={beforeSignIn}
                />
              ) : undefined
            }
            subscription={
              isOAuth
                ? {
                    models: subscriptionModels.models,
                    source: subscriptionModels.source,
                    fetch: {
                      loading: subscriptionModels.loading,
                      error: subscriptionModels.error,
                      onRefresh: subscriptionModels.refresh,
                    },
                    providerLabel: PROVIDER_LABELS[provider],
                    liveCatalog: true,
                  }
                : undefined
            }
          />
        )
      })}
    </>
  )
}
