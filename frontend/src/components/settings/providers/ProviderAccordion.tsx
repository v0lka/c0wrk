import { useState, useMemo } from 'react'
import type { ReactNode } from 'react'
import { ProviderConfigForm } from '../ProviderConfigForm'
import { useModelFetch } from '../useModelFetch'
import { ModelConfigDialog } from '../ModelConfigDialog'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { compositeModelId, bareModel } from '@/lib/modelId'
import { formatContextWindow } from '@/lib/chatgptFormat'
import type { ChatGPTModelPresetEntry } from '@/types/models'
import { ChevronDown, ChevronRight, X, SlidersHorizontal } from 'lucide-react'

export interface ProviderConfig {
  api_key: string
  base_url: string
  models: string[]
  /** Transport for compatible providers: "openai" | "anthropic". */
  type?: 'openai' | 'anthropic'
  /** Per-provider TLS pin (ADR-054): '' = standard CA verification (override
   *  off), non-empty = only the pinned key is accepted. */
  tls_fingerprint: string
  /** Auto-resend interval in seconds (compatible providers only).
   *  Undefined = keep the persisted value (omitted from the payload);
   *  0 = auto-resend off, sent explicitly. */
  auto_retry_seconds?: number
  /** ChatGPT-only authentication mode ('api_key' | 'oauth'). Mirrors the
   *  useLLMConfig draft field; only the chatgpt entry carries it. */
  auth_mode?: 'api_key' | 'oauth'
}

interface ProviderAccordionProps {
  provider: string
  label: string
  config: ProviderConfig
  isExpanded: boolean
  onToggle: () => void
  onConfigChange: (updates: Partial<{ api_key: string; base_url: string; tls_fingerprint: string; auto_retry_seconds?: number; auth_mode?: 'api_key' | 'oauth' }>) => void
  onToggleModel: (model: string) => void
  onDelete?: () => void
  defaultModel: string
  providerConfigs: Record<string, ProviderConfig>
  /** Server-published auto_retry_seconds upper bound (ADR-065), threaded
   *  to ProviderConfigForm's interval input. REQUIRED — LLMSettings gates
   *  the forms until the bound has loaded. */
  autoRetryMaxSeconds: number
  /** Extra section rendered above the config form (the ChatGPT accordion's
   *  authentication panel: mode selector + browser sign-in flow + status). */
  authSection?: ReactNode
  /** ChatGPT subscription mode is active: the API key input renders
   *  disabled with an explanation, the Fetch models button drives the live
   *  subscription catalog instead of a provider API listing, and the model
   *  checklist switches from the fetched+enabled union to the curated
   *  preset (union with enabled models so a checked row never silently
   *  vanishes). */
  isOAuth?: boolean
  /** The ChatGPT model-list entries (oauth mode): the offline preset until
   *  a live fetch answers, the subscription's own catalog after. May be
   *  empty before the preset RPC answers — the checklist then shows the
   *  enabled models plus its empty-state hint. */
  presetModels?: readonly ChatGPTModelPresetEntry[]
  /** Where presetModels currently comes from (oauth mode): 'preset' until
   *  the live fetch answers, 'subscription' after. Drives the checklist's
   *  hint line. */
  presetSource?: 'preset' | 'subscription'
  /** The live-catalog fetch controls (oauth mode): the Fetch models button
   *  re-runs the subscription /models fetch. Undefined outside oauth mode. */
  chatGPTFetch?: ChatGPTFetchControls
}

/** The subset of the hook state ProviderConfigForm's Fetch models button
 *  needs in oauth mode (from useChatGPTModelPreset). */
export interface ChatGPTFetchControls {
  loading: boolean
  error: string | null
  onRefresh: () => void
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
  autoRetryMaxSeconds,
  authSection,
  isOAuth = false,
  presetModels,
  presetSource = 'preset',
  chatGPTFetch,
}: ProviderAccordionProps) {
  const {
    models,
    modelsLoading,
    modelsError,
    apiKeyDirty,
    handleApply,
    hasRequiredCredentials,
  } = useModelFetch(provider, providerConfigs)

  // The visible model list. In subscription (oauth) mode it is the curated
  // ChatGPT preset — there is no listing endpoint to dial, so fetch state
  // plays no part; enabled models missing from the preset are appended so a
  // checked row never silently vanishes (the accordion's no-disappear
  // invariant). In api_key mode it stays the deduplicated union of the
  // models fetched from the provider API and the models already enabled in
  // the config: enabled models never disappear from the list — even when a
  // later fetch (or a reopened settings dialog) no longer reports them —
  // until the user unchecks them; duplicates (endpoints reporting the same
  // ID twice) are collapsed into a single row. Sorted so the order is stable
  // regardless of fetch state.
  const displayModels = useMemo(() => {
    const seen = new Set<string>()
    const result: string[] = []
    if (isOAuth) {
      // Preset order (most capable first) is the product decision — keep it.
      for (const entry of presetModels ?? []) {
        if (seen.has(entry.name)) continue
        seen.add(entry.name)
        result.push(entry.name)
      }
      const extras = config.models.filter((m) => !seen.has(m)).sort()
      return [...result, ...extras]
    }
    for (const m of [...models, ...config.models]) {
      if (seen.has(m)) continue
      seen.add(m)
      result.push(m)
    }
    result.sort()
    return result
  }, [isOAuth, presetModels, models, config.models])

  // Preset metadata badges (oauth mode): context window and the reasoning
  // flag come from the model registry via GetChatGPTModelPreset.
  const presetByName = useMemo(() => {
    const map = new Map<string, ChatGPTModelPresetEntry>()
    for (const entry of presetModels ?? []) map.set(entry.name, entry)
    return map
  }, [presetModels])

  const isEmpty = displayModels.length === 0

  // A fetch has completed successfully when the key is no longer dirty
  // (useModelFetch clears apiKeyDirty only on success) and no fetch error
  // is pending. Before that we simply don't know what the endpoint reports,
  // so no staleness signal is shown. In oauth mode no fetch ever runs, so
  // the flag stays false and the "not reported by endpoint" badge never
  // renders.
  const fetchCompleted = !isOAuth && !apiKeyDirty && modelsError === null

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
        title={label}
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
          {authSection}
          <ProviderConfigForm
            activeProvider={provider}
            config={config}
            apiKeyDirty={apiKeyDirty}
            hasRequiredCredentials={hasRequiredCredentials}
            modelsLoading={modelsLoading}
            onConfigChange={onConfigChange}
            onApply={handleApply}
            autoRetryMaxSeconds={autoRetryMaxSeconds}
            authMode={isOAuth ? 'oauth' : 'api_key'}
            chatGPTFetch={chatGPTFetch}
          />

          {/* Model Checklist */}
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium">Enabled Models</label>
            {isOAuth && (
              <span className="text-xs text-muted-foreground">
                {presetSource === 'subscription'
                  ? 'Live from your ChatGPT subscription — press Fetch models to refresh the list.'
                  : 'ChatGPT fallback preset (offline) — sign in and press Fetch models to load the models your subscription actually serves.'}
              </span>
            )}
            {isOAuth && chatGPTFetch?.error && (
              <span className="text-xs text-destructive">{chatGPTFetch.error}</span>
            )}
            {!isOAuth && modelsLoading && (
              <span className="text-xs text-muted-foreground">Fetching models...</span>
            )}
            {!isOAuth && modelsError && (
              <span className="text-xs text-destructive">{modelsError}</span>
            )}
            <div className="flex max-h-48 flex-col gap-1 overflow-y-auto custom-scrollbar rounded-md border p-2">
              {isEmpty && !modelsLoading && !(isOAuth && chatGPTFetch?.loading) && (
                <span className="text-xs text-muted-foreground">
                  {isOAuth
                    ? 'No ChatGPT models loaded yet — sign in above and click "Fetch models".'
                    : hasRequiredCredentials
                      ? 'No models available. Click "Fetch Models" to load them.'
                      : 'Configure API key and click "Fetch Models" to load available models.'}
                </span>
              )}
              {isOAuth && chatGPTFetch?.loading && isEmpty && (
                <span className="text-xs text-muted-foreground">Fetching subscription models…</span>
              )}
              {displayModels.map((model) => {
                const isEnabled = config.models.includes(model)
                // `defaultModel` is a composite "provider/name" selector, so
                // badge the entry whose composite id matches — this pins the
                // badge to the single provider that owns the default even when
                // the same bare name is exposed by multiple providers.
                const isDefault = compositeModelId(provider, model) === defaultModel
                // Enabled but absent from the last successful fetch: the
                // endpoint no longer reports this model. Surfaced so the user
                // notices the drift instead of silently keeping a model that
                // may fail at request time. Never in oauth mode (no fetch).
                const notReported =
                  fetchCompleted && isEnabled && !models.includes(model)
                // Preset metadata (oauth mode): registry-sourced context
                // window and reasoning flag, from GetChatGPTModelPreset.
                // Metadata is fail-soft — zeros mean the async registry had
                // nothing to say, and zeros render no badge.
                const preset = isOAuth ? presetByName.get(model) : undefined
                const presetCtx = preset?.context_window ?? 0
                const presetOut = preset?.output_limit ?? 0
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
                    {preset?.reasoning && (
                      <span className="rounded bg-info/10 px-1.5 py-0.5 text-xs font-medium text-info">
                        reasoning
                      </span>
                    )}
                    {preset && presetCtx > 0 && (
                      <span
                        className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground"
                        title={`Context window: ${presetCtx.toLocaleString('en')} tokens${presetOut > 0 ? ` · output limit: ${presetOut.toLocaleString('en')}` : ''}`}
                      >
                        {formatContextWindow(presetCtx)}
                      </span>
                    )}
                    {isDefault && (
                      <span className="rounded bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                        default
                      </span>
                    )}
                    {notReported && (
                      <span
                        className="rounded bg-warning/10 px-1.5 py-0.5 text-xs font-medium text-warning"
                        title="Enabled in the config, but the last successful fetch did not report this model. It may have been removed from the endpoint."
                      >
                        not reported by endpoint
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
