import { useState, useMemo } from 'react'
import { ProviderConfigForm } from '../ProviderConfigForm'
import { useModelFetch } from '../useModelFetch'
import { ModelConfigDialog } from '../ModelConfigDialog'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { compositeModelId, bareModel } from '@/lib/modelId'
import { ChevronDown, ChevronRight, X, SlidersHorizontal } from 'lucide-react'

export interface ProviderConfig {
  api_key: string
  base_url: string
  models: string[]
}

interface ProviderAccordionProps {
  provider: string
  label: string
  config: ProviderConfig
  isExpanded: boolean
  onToggle: () => void
  onConfigChange: (updates: Partial<{ api_key: string; base_url: string }>) => void
  onToggleModel: (model: string) => void
  onDelete?: () => void
  defaultModel: string
  providerConfigs: Record<string, ProviderConfig>
}

export function ProviderAccordion({
  provider,
  label,
  config,
  isExpanded,
  onToggle,
  onConfigChange,
  onToggleModel,
  onDelete,
  defaultModel,
  providerConfigs,
}: ProviderAccordionProps) {
  const {
    models,
    modelsLoading,
    modelsError,
    apiKeyDirty,
    handleApply,
    hasRequiredCredentials,
  } = useModelFetch(provider, providerConfigs)

  // Show configured models immediately when the provider API hasn't been
  // fetched yet. Once the user clicks "Fetch Models" / "Apply", the full
  // provider model list replaces the configured subset.
  const displayModels = useMemo(() => {
    if (models.length > 0) return models
    return config.models
  }, [models, config.models])

  const isEmpty = displayModels.length === 0

  // Deletion confirmation: warn when the provider owns the default model.
  // `defaultModel` is a composite "provider/name" selector (normalized by
  // useLLMConfig), so compare against the composite id built from this
  // provider's enabled models rather than the bare name — this avoids both
  // false positives (another provider exposing the same bare name) and false
  // negatives (a composite default never matching a bare-name list).
  const [confirmDelete, setConfirmDelete] = useState(false)
  const ownsDefaultModel = config.models.some(
    (m) => compositeModelId(provider, m) === defaultModel,
  )

  // Per-model Configure dialog: tracks which model's dialog is open (null when
  // closed). Bare model name — ModelConfigDialog addresses the model directly.
  const [configModel, setConfigModel] = useState<string | null>(null)

  const handleDeleteClick = () => {
    if (ownsDefaultModel && !confirmDelete) {
      setConfirmDelete(true)
      return
    }
    onDelete?.()
  }

  return (
    <div className="rounded-lg border">
      <button
        type="button"
        className="flex w-full items-center justify-between px-4 py-3 text-sm font-medium hover:bg-muted/50"
        onClick={onToggle}
      >
        <span className="flex items-center gap-2">
          {isExpanded ? (
            <ChevronDown className="h-4 w-4" />
          ) : (
            <ChevronRight className="h-4 w-4" />
          )}
          {label}
        </span>
        <span className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {config.models.length} model{config.models.length !== 1 ? 's' : ''} enabled
          </span>
          {onDelete && (
            <span
              role="button"
              tabIndex={0}
              className="ml-1 flex h-5 w-5 cursor-pointer items-center justify-center rounded text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              onClick={(e) => {
                e.stopPropagation()
                handleDeleteClick()
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  e.stopPropagation()
                  handleDeleteClick()
                }
              }}
              title={
                confirmDelete
                  ? 'Click again to confirm deletion'
                  : 'Delete provider'
              }
            >
              <X className="h-3.5 w-3.5" />
            </span>
          )}
        </span>
      </button>

      {confirmDelete && (
        <div className="border-t px-4 py-2 text-xs text-destructive">
          This provider contains the default model &ldquo;{bareModel(defaultModel)}&rdquo;.
          Click the X again to confirm deletion.
        </div>
      )}

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
            <div className="flex max-h-48 flex-col gap-1 overflow-y-auto custom-scrollbar rounded-md border p-2">
              {isEmpty && !modelsLoading && (
                <span className="text-xs text-muted-foreground">
                  {hasRequiredCredentials
                    ? 'No models available. Click "Fetch Models" to load them.'
                    : 'Configure API key and click "Fetch Models" to load available models.'}
                </span>
              )}
              {displayModels.map((model) => {
                const isEnabled = config.models.includes(model)
                // `defaultModel` is a composite "provider/name" selector, so
                // badge the entry whose composite id matches — this pins the
                // badge to the single provider that owns the default even when
                // the same bare name is exposed by multiple providers.
                const isDefault = compositeModelId(provider, model) === defaultModel
                return (
                  <label
                    key={model}
                    className="group flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs hover:bg-muted"
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
                    <button
                      type="button"
                      className="flex h-5 w-5 cursor-pointer items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity hover:bg-primary/10 hover:text-primary group-hover:opacity-100 focus:opacity-100"
                      title={`Configure ${model}`}
                      aria-label={`Configure ${model}`}
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        setConfigModel(model)
                      }}
                    >
                      <SlidersHorizontal className="h-3 w-3" />
                    </button>
                  </label>
                )
              })}
            </div>
          </div>
        </div>
      )}

      <ModelConfigDialog
        model={configModel ?? ''}
        open={configModel !== null}
        onOpenChange={(o) => { if (!o) setConfigModel(null) }}
        onSaved={invalidateConfigCache}
      />
    </div>
  )
}
