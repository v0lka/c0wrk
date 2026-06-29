import { useState, useMemo, useCallback } from 'react'
import { ProviderConfigForm } from './ProviderConfigForm'
import { useLLMConfig } from './useLLMConfig'
import { useModelFetch } from './useModelFetch'
import { ChevronDown, ChevronRight, Plus, X } from 'lucide-react'
import {
  FIXED_PROVIDERS,
  PROVIDER_LABELS,
} from '@/lib/llm-providers'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'

// ---------------------------------------------------------------------------
// Provider name validation
// ---------------------------------------------------------------------------

const RESERVED_NAMES = new Set<string>(FIXED_PROVIDERS)

/** Regex: must start with a letter, then letters / digits / underscores / hyphens. */
const PROVIDER_NAME_RE = /^[a-zA-Z][a-zA-Z0-9_-]*$/

interface AddFormErrors {
  name?: string
  baseUrl?: string
  apiKey?: string
}

function validateProviderName(
  name: string,
  existingNames: Set<string>,
): string | null {
  const trimmed = name.trim()
  if (!trimmed) return 'Name is required.'
  if (RESERVED_NAMES.has(trimmed)) return `"${trimmed}" is a reserved name.`
  if (existingNames.has(trimmed)) return `A provider named "${trimmed}" already exists.`
  if (!PROVIDER_NAME_RE.test(trimmed)) {
    return 'Name must start with a letter and contain only a-z, A-Z, 0-9, _, -.'
  }
  return null
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function LLMSettings({
  onSettingsSaved,
  onDefaultModelChange,
}: {
  onSettingsSaved?: () => void
  onDefaultModelChange?: (model: string) => void
}) {
  const {
    defaultModel,
    providerConfigs,
    openaiCompatibleProviderNames,
    isLoading,
    setDefaultModel,
    updateProviderConfig,
    toggleModel,
    addProvider,
    deleteProvider,
  } = useLLMConfig(onSettingsSaved, onDefaultModelChange)

  const [expandedProviders, setExpandedProviders] = useState<Set<string>>(new Set())

  // --- Add-provider form state -------------------------------------------
  const [showAddForm, setShowAddForm] = useState(false)
  const [addFormName, setAddFormName] = useState('')
  const [addFormBaseUrl, setAddFormBaseUrl] = useState('')
  const [addFormApiKey, setAddFormApiKey] = useState('')
  const [addFormErrors, setAddFormErrors] = useState<AddFormErrors>({})
  const [addFormSubmitting, setAddFormSubmitting] = useState(false)

  const resetAddForm = useCallback(() => {
    setShowAddForm(false)
    setAddFormName('')
    setAddFormBaseUrl('')
    setAddFormApiKey('')
    setAddFormErrors({})
  }, [])

  const handleAddProvider = useCallback(() => {
    const nameErr = validateProviderName(addFormName, openaiCompatibleProviderNames)
    const errors: AddFormErrors = {}
    if (nameErr) errors.name = nameErr
    setAddFormErrors(errors)
    if (Object.keys(errors).length > 0) return

    setAddFormSubmitting(true)
    const name = addFormName.trim()
    addProvider(name, {
      api_key: addFormApiKey,
      base_url: addFormBaseUrl,
      models: [],
    })
    // Expand the new provider immediately.
    setExpandedProviders((prev) => new Set(prev).add(name))
    resetAddForm()
    setAddFormSubmitting(false)
  }, [
    addFormName,
    addFormBaseUrl,
    addFormApiKey,
    openaiCompatibleProviderNames,
    addProvider,
    resetAddForm,
  ])

  // --- Accordion helpers -------------------------------------------------
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
    for (const p of Object.keys(providerConfigs)) {
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
            <option key={`${provider}:${model}`} value={model}>
              {model}
            </option>
          ))}
        </select>
        <p className="text-xs text-muted-foreground">
          The default model is used when no per-message override is set. It must
          be enabled in at least one provider below.
        </p>
      </div>

      {/* Fixed Provider Accordions */}
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
            onToggle={() => toggleExpanded(provider)}
            onConfigChange={(updates) => updateProviderConfig(provider, updates)}
            onToggleModel={(model) => toggleModel(provider, model)}
            defaultModel={defaultModel}
            providerConfigs={providerConfigs}
          />
        )
      })}

      {/* OpenAI-Compatible Provider Accordions */}
      {[...openaiCompatibleProviderNames].sort().map((name) => {
        const config = providerConfigs[name]
        if (!config) return null
        const isExpanded = expandedProviders.has(name)
        const label = `OpenAI Compatible: ${name}`

        return (
          <ProviderAccordion
            key={name}
            provider={name}
            label={label}
            config={config}
            isExpanded={isExpanded}
            onToggle={() => toggleExpanded(name)}
            onConfigChange={(updates) => updateProviderConfig(name, updates)}
            onToggleModel={(model) => toggleModel(name, model)}
            onDelete={() => deleteProvider(name)}
            defaultModel={defaultModel}
            providerConfigs={providerConfigs}
          />
        )
      })}

      {/* Add OpenAI-compatible provider */}
      <div className="flex flex-col gap-3">
        {!showAddForm && (
          <Button
            variant="outline"
            size="sm"
            className="self-start gap-2"
            onClick={() => setShowAddForm(true)}
          >
            <Plus className="h-4 w-4" />
            Add OpenAI-compatible provider
          </Button>
        )}

        {showAddForm && (
          <div className="rounded-lg border p-4">
            <div className="mb-3 flex items-center justify-between">
              <h4 className="text-sm font-medium">New OpenAI-compatible provider</h4>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={resetAddForm}
              >
                <X className="h-4 w-4" />
              </Button>
            </div>

            <div className="flex flex-col gap-3">
              {/* Name */}
              <div className="flex flex-col gap-1">
                <label className="text-xs text-muted-foreground">Name</label>
                <Input
                  placeholder="e.g. deepseek"
                  value={addFormName}
                  onChange={(e) => setAddFormName(e.target.value)}
                  className="h-9 text-sm"
                />
                {addFormErrors.name && (
                  <span className="text-xs text-destructive">{addFormErrors.name}</span>
                )}
              </div>

              {/* Base URL */}
              <div className="flex flex-col gap-1">
                <label className="text-xs text-muted-foreground">Base URL</label>
                <Input
                  placeholder="http://localhost:1234"
                  value={addFormBaseUrl}
                  onChange={(e) => setAddFormBaseUrl(e.target.value)}
                  className="h-9 text-sm"
                />
              </div>

              {/* API Key */}
              <div className="flex flex-col gap-1">
                <label className="text-xs text-muted-foreground">API Key</label>
                <Input
                  type={addFormApiKey.startsWith('${') ? 'text' : 'password'}
                  placeholder="Enter API key"
                  value={addFormApiKey}
                  onChange={(e) => setAddFormApiKey(e.target.value)}
                  className="h-9 text-sm"
                />
              </div>

              {/* Actions */}
              <div className="flex items-center gap-2 pt-1">
                <Button
                  size="sm"
                  onClick={handleAddProvider}
                  disabled={addFormSubmitting}
                >
                  Add Provider
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={resetAddForm}
                >
                  Cancel
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ProviderAccordion — shared by fixed and OpenAI-compatible providers
// ---------------------------------------------------------------------------

function ProviderAccordion({
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
}: {
  provider: string
  label: string
  config: { api_key: string; base_url: string; models: string[] }
  isExpanded: boolean
  onToggle: () => void
  onConfigChange: (updates: Partial<{ api_key: string; base_url: string }>) => void
  onToggleModel: (model: string) => void
  onDelete?: () => void
  defaultModel: string
  providerConfigs: Record<string, { api_key: string; base_url: string; models: string[] }>
}) {
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
  const [confirmDelete, setConfirmDelete] = useState(false)
  const ownsDefaultModel = config.models.includes(defaultModel)

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
          This provider contains the default model &ldquo;{defaultModel}&rdquo;.
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
