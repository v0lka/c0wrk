import { ProviderSelector } from './ProviderSelector'
import { ProviderConfigForm } from './ProviderConfigForm'
import { ModelSelector } from './ModelSelector'
import { useLLMConfig } from './useLLMConfig'
import { useModelFetch } from './useModelFetch'

export function LLMSettings({ onSettingsSaved }: { onSettingsSaved?: () => void }) {
  const {
    activeProvider,
    providerConfigs,
    isLoading,
    handleProviderChange,
    updateProviderConfig,
  } = useLLMConfig(onSettingsSaved)

  const {
    models,
    modelsLoading,
    modelsError,
    apiKeyDirty,
    handleApply,
    hasRequiredCredentials,
    isModelDisabled,
    modelPlaceholder,
  } = useModelFetch(activeProvider, providerConfigs)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading LLM settings...</span>
      </div>
    )
  }

  const currentConfig = providerConfigs[activeProvider]

  return (
    <div className="flex flex-col gap-6">
      <ProviderSelector activeProvider={activeProvider} onProviderChange={handleProviderChange} />
      {currentConfig && (
        <div className="flex flex-col gap-4">
          <ProviderConfigForm
            activeProvider={activeProvider}
            config={currentConfig}
            apiKeyDirty={apiKeyDirty}
            hasRequiredCredentials={hasRequiredCredentials}
            modelsLoading={modelsLoading}
            onConfigChange={updateProviderConfig}
            onApply={handleApply}
          />
          <ModelSelector
            activeProvider={activeProvider}
            model={currentConfig.model}
            models={models}
            modelsLoading={modelsLoading}
            modelsError={modelsError}
            disabled={isModelDisabled}
            placeholder={modelPlaceholder}
            onModelChange={(model) => updateProviderConfig({ model })}
          />
        </div>
      )}
    </div>
  )
}
