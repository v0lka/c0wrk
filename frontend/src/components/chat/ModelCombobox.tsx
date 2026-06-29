import { useMemo } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import { useDropdown } from '@/hooks/useDropdown'
import { PROVIDER_LABELS, type ProviderKey } from '@/lib/llm-providers'

interface ModelEntry {
  model: string
  provider: string
  providerLabel: string
}

/**
 * ModelCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message model override.  "Default" means the global default_model is used.
 * The selection is persisted in inputModeStore and survives restarts.
 */
export function ModelCombobox() {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const setSelectedModel = useInputModeStore((s) => s.setSelectedModel)

  const { allModels: modelInfos, defaultModel, loaded } = useConfigData()
  const { isOpen, setIsOpen, containerRef } = useDropdown()

  // Convert ModelInfo[] → ModelEntry[] (flat list of enabled models per provider).
  const allModels: ModelEntry[] = useMemo(() => {
    const entries: ModelEntry[] = []
    for (const info of modelInfos) {
      entries.push({
        model: info.name,
        provider: info.family,
        providerLabel: PROVIDER_LABELS[info.family as ProviderKey] || info.family,
      })
    }
    return entries
  }, [modelInfos])

  // Build display label.
  const effectiveModel = selectedModel ?? defaultModel
  const displayLabel = selectedModel
    ? effectiveModel
    : defaultModel
      ? `Default: ${defaultModel}`
      : 'Select model…'

  const effectiveEntry = allModels.find((e) => e.model === effectiveModel)

  // Group models by provider for the dropdown.
  const grouped = useMemo(() => {
    const map = new Map<string, ModelEntry[]>()
    for (const entry of allModels) {
      const list = map.get(entry.provider) || []
      list.push(entry)
      map.set(entry.provider, list)
    }
    return map
  }, [allModels])

  const isLoading = !loaded

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        type="button"
        disabled={isLoading}
        className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[200px] truncate disabled:opacity-50 disabled:cursor-not-allowed"
        onClick={() => setIsOpen((v) => !v)}
        title={isLoading ? 'Loading models…' : effectiveEntry ? `${effectiveEntry.providerLabel}: ${effectiveModel}` : displayLabel}
      >
        <span className="truncate">{isLoading ? 'Loading models\u2026' : displayLabel}</span>
        <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && (
        <div className="absolute bottom-full left-0 mb-1 w-72 rounded-md border bg-popover shadow-md z-50 max-h-64 overflow-y-auto">
          {isLoading ? (
            <div className="px-3 py-4 text-xs text-muted-foreground text-center">
              Loading models…
            </div>
          ) : (
            <>
              {/* Default option */}
              <button
                type="button"
                className={`flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted ${!selectedModel ? 'bg-primary/10 font-medium' : ''}`}
                onClick={() => { setSelectedModel(null); setIsOpen(false) }}
              >
                <span className="flex-1 text-left">
                  Default{defaultModel ? ` (${defaultModel})` : ''}
                </span>
                {!selectedModel && (
                  <span className="text-[10px] text-primary">active</span>
                )}
              </button>

              {/* Provider groups */}
              {Array.from(grouped.entries()).map(([provider, models]) => (
                <div key={provider}>
                  <div className="px-3 py-1 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider bg-muted/30">
                    {PROVIDER_LABELS[provider as ProviderKey] || provider}
                  </div>
                  {models.map((entry) => {
                    const isSelected = selectedModel === entry.model
                    const isDefault = !selectedModel && entry.model === defaultModel
                    return (
                      <button
                        key={entry.model}
                        type="button"
                        className={`flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted ${isSelected ? 'bg-primary/10 font-medium' : ''}`}
                        onClick={() => { setSelectedModel(entry.model); setIsOpen(false) }}
                      >
                        <span className="flex-1 text-left truncate">{entry.model}</span>
                        {isSelected && (
                          <span className="text-[10px] text-primary">selected</span>
                        )}
                        {isDefault && (
                          <span className="text-[10px] text-muted-foreground">default</span>
                        )}
                      </button>
                    )
                  })}
                </div>
              ))}

              {loaded && allModels.length === 0 && (
                <div className="px-3 py-2 text-xs text-muted-foreground italic">
                  No models configured. Enable models in Settings.
                </div>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}
