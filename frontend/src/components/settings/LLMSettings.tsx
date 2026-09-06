import { useState, useMemo, useCallback } from 'react'
import { useLLMConfig } from './useLLMConfig'
import { Plus, X } from 'lucide-react'
import { FIXED_PROVIDERS } from '@/lib/llm-providers'
import { compositeModelId } from '@/lib/modelId'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { FixedProviderForms } from './providers/FixedProviderForms'
import { OpenAICompatibleProviderForms } from './providers/OpenAICompatibleProviderForms'

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
    anthropicCompatibleProviderNames,
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
  // Transport type for the new compatible provider: 'openai' (default) or 'anthropic'.
  const [addFormType, setAddFormType] = useState<'openai' | 'anthropic'>('openai')
  const [addFormErrors, setAddFormErrors] = useState<AddFormErrors>({})
  const [addFormSubmitting, setAddFormSubmitting] = useState(false)

  const resetAddForm = useCallback(() => {
    setShowAddForm(false)
    setAddFormName('')
    setAddFormBaseUrl('')
    setAddFormApiKey('')
    setAddFormType('openai')
    setAddFormErrors({})
  }, [])

  // All compatible provider names (both transports) for uniqueness validation.
  const allCompatibleNames = useMemo(
    () => new Set([...openaiCompatibleProviderNames, ...anthropicCompatibleProviderNames]),
    [openaiCompatibleProviderNames, anthropicCompatibleProviderNames],
  )

  const handleAddProvider = useCallback(() => {
    const nameErr = validateProviderName(addFormName, allCompatibleNames)
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
      type: addFormType,
    })
    // Expand the new provider immediately.
    setExpandedProviders((prev) => new Set(prev).add(name))
    resetAddForm()
    setAddFormSubmitting(false)
  }, [
    addFormName,
    addFormBaseUrl,
    addFormApiKey,
    addFormType,
    allCompatibleNames,
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

  // Collect all enabled models across all providers for the global default
  // dropdown. Each entry carries its composite "provider/name" selector (the
  // value sent to the backend) plus the bare name shown to the user. When the
  // same bare name is exposed by more than one provider, the option label is
  // disambiguated with the provider key so the user can tell them apart.
  const allEnabledModels = useMemo(() => {
    const result: { id: string; model: string; provider: string }[] = []
    for (const p of Object.keys(providerConfigs)) {
      const cfg = providerConfigs[p]
      if (cfg) {
        for (const m of cfg.models) {
          result.push({ id: compositeModelId(p, m), model: m, provider: p })
        }
      }
    }
    return result
  }, [providerConfigs])

  // Bare names that appear under more than one provider — their dropdown
  // options are suffixed with the provider key for disambiguation.
  const duplicateBareNames = useMemo(() => {
    const counts = new Map<string, number>()
    for (const { model } of allEnabledModels) {
      counts.set(model, (counts.get(model) ?? 0) + 1)
    }
    const dupes = new Set<string>()
    for (const [model, count] of counts) {
      if (count > 1) dupes.add(model)
    }
    return dupes
  }, [allEnabledModels])

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
        <Combobox
          ariaLabel="Default model"
          value={defaultModel}
          onChange={setDefaultModel}
          options={[
            { value: '', label: '— Select a default model —' },
            ...allEnabledModels.map(({ id, model, provider }) => ({
              value: id,
              label: duplicateBareNames.has(model) ? `${model} (${provider})` : model,
            })),
          ]}
        />
        <p className="text-xs text-muted-foreground">
          The default model is used when no per-message override is set. It must
          be enabled in at least one provider below.
        </p>
        {!defaultModel && (
          <p className="text-xs text-destructive" role="alert">
            Choose a default model to save your provider changes.
          </p>
        )}
      </div>

      {/* Fixed Provider Accordions */}
      <FixedProviderForms
        providerConfigs={providerConfigs}
        expandedProviders={expandedProviders}
        onToggle={toggleExpanded}
        onConfigChange={updateProviderConfig}
        onToggleModel={toggleModel}
        defaultModel={defaultModel}
      />

      {/* OpenAI-Compatible Provider Accordions */}
      <OpenAICompatibleProviderForms
        providerNames={openaiCompatibleProviderNames}
        providerConfigs={providerConfigs}
        expandedProviders={expandedProviders}
        onToggle={toggleExpanded}
        onConfigChange={updateProviderConfig}
        onToggleModel={toggleModel}
        onDelete={deleteProvider}
        defaultModel={defaultModel}
        labelPrefix="OpenAI Compatible"
      />

      {/* Anthropic-Compatible Provider Accordions */}
      <OpenAICompatibleProviderForms
        providerNames={anthropicCompatibleProviderNames}
        providerConfigs={providerConfigs}
        expandedProviders={expandedProviders}
        onToggle={toggleExpanded}
        onConfigChange={updateProviderConfig}
        onToggleModel={toggleModel}
        onDelete={deleteProvider}
        defaultModel={defaultModel}
        labelPrefix="Anthropic Compatible"
      />

      {/* Add compatible provider */}
      <div className="flex flex-col gap-3">
        {!showAddForm && (
          <Button
            variant="outline"
            size="sm"
            className="self-start gap-2"
            onClick={() => setShowAddForm(true)}
          >
            <Plus className="h-4 w-4" />
            Add compatible provider
          </Button>
        )}

        {showAddForm && (
          <div className="rounded-lg border p-4">
            <div className="mb-3 flex items-center justify-between">
              <h4 className="text-sm font-medium">New compatible provider</h4>
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
              {/* Transport type */}
              <div className="flex flex-col gap-1">
                <label className="text-xs text-muted-foreground">API type</label>
                <Combobox
                  ariaLabel="API type"
                  value={addFormType}
                  onChange={(v) => setAddFormType(v as 'openai' | 'anthropic')}
                  options={[
                    { value: 'openai', label: 'OpenAI-compatible (Chat Completions)' },
                    { value: 'anthropic', label: 'Anthropic-compatible (Messages API)' },
                  ]}
                />
                <p className="text-xs text-muted-foreground">
                  Choose the API protocol spoken by the custom endpoint.
                </p>
              </div>

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
                  placeholder={addFormType === 'anthropic' ? 'https://my-anthropic-proxy.example.com' : 'http://localhost:1234'}
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
                  placeholder="Enter API key (optional for local servers)"
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
