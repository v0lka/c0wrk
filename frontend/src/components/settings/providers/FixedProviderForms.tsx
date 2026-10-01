import { ProviderAccordion } from './ProviderAccordion'
import type { ProviderConfig } from './ProviderAccordion'
import { ChatGPTAuthSection } from './ChatGPTAuthSection'
import { useChatGPTModelPreset } from '../useChatGPTModelPreset'
import { FIXED_PROVIDERS, PROVIDER_LABELS } from '@/lib/llm-providers'

interface FixedProviderFormProps {
  providerConfigs: Record<string, ProviderConfig>
  expandedProviders: Set<string>
  onToggle: (provider: string) => void
  onConfigChange: (provider: string, updates: Partial<{ api_key: string; base_url: string; tls_fingerprint: string; auto_retry_seconds?: number; auth_mode?: 'api_key' | 'oauth' }>) => void
  onToggleModel: (provider: string, model: string) => void
  defaultModel: string
  /** Server-published auto_retry_seconds upper bound (ADR-065); unused by
   *  fixed providers themselves but threaded for the shared accordion.
   *  REQUIRED — LLMSettings gates the forms until the bound has loaded. */
  autoRetryMaxSeconds: number
}

export function FixedProviderForms({
  providerConfigs,
  expandedProviders,
  onToggle,
  onConfigChange,
  onToggleModel,
  defaultModel,
  autoRetryMaxSeconds,
}: FixedProviderFormProps) {
  // The ChatGPT subscription surface: the model list loads only while the
  // draft selects oauth mode (cheap read-only preset RPC + a live catalog
  // fetch when signed in) and feeds the oauth checklist; the auth section
  // (mode selector + sign-in flow) rides the chatgpt accordion's
  // authSection slot. The Fetch models button re-runs the live fetch
  // (chatGPTFetch), and the hint line tells preset from live list
  // (presetSource).
  const chatgptIsOAuth = providerConfigs.chatgpt?.auth_mode === 'oauth'
  const chatgptModels = useChatGPTModelPreset(chatgptIsOAuth)

  return (
    <>
      {FIXED_PROVIDERS.map((provider) => {
        const config = providerConfigs[provider]
        if (!config) return null
        const isExpanded = expandedProviders.has(provider)
        const isChatGPT = provider === 'chatgpt'

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
              ) : undefined
            }
            isOAuth={isChatGPT && chatgptIsOAuth}
            presetModels={isChatGPT ? chatgptModels.models : undefined}
            presetSource={isChatGPT ? chatgptModels.source : undefined}
            chatGPTFetch={
              isChatGPT
                ? {
                    loading: chatgptModels.loading,
                    error: chatgptModels.error,
                    onRefresh: chatgptModels.refresh,
                  }
                : undefined
            }
          />
        )
      })}
    </>
  )
}
