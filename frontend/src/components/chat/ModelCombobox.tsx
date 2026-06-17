import { useState, useEffect, useRef, useCallback } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'

interface ModelEntry {
  model: string
  provider: string
  providerLabel: string
}

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  lmstudio: 'LM Studio',
  openai_compatible: 'OpenAI Compatible',
  chatgpt: 'ChatGPT',
}

/**
 * ModelCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message model override.  "Default" means the global default_model is used.
 * The selection is persisted in inputModeStore and survives restarts.
 */
export function ModelCombobox() {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const setSelectedModel = useInputModeStore((s) => s.setSelectedModel)

  const [allModels, setAllModels] = useState<ModelEntry[]>([])
  const [defaultModel, setDefaultModel] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  // Load enabled models from config once.
  useEffect(() => {
    let cancelled = false
    getConfig()
      .then((cfg) => {
        if (cancelled) return
        const entries: ModelEntry[] = []
        const providers = ['anthropic', 'gemini', 'lmstudio', 'openai_compatible', 'chatgpt'] as const
        for (const p of providers) {
          const pc = cfg.llm[p]
          if (pc && Array.isArray(pc.models)) {
            for (const m of pc.models) {
              entries.push({ model: m, provider: p, providerLabel: PROVIDER_LABELS[p] || p })
            }
          }
        }
        setAllModels(entries)
        setDefaultModel(cfg.llm.default_model || '')
      })
      .catch((err) => logger.error('ModelCombobox: failed to load config:', err))
    return () => { cancelled = true }
  }, [])

  // Close on outside click.
  const handleClickOutside = useCallback((e: MouseEvent) => {
    if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
      setIsOpen(false)
    }
  }, [])

  useEffect(() => {
    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside)
      return () => document.removeEventListener('mousedown', handleClickOutside)
    }
    return
  }, [isOpen, handleClickOutside])

  // Build display label.
  const effectiveModel = selectedModel ?? defaultModel
  const displayLabel = selectedModel
    ? effectiveModel
    : defaultModel
      ? `Default: ${defaultModel}`
      : 'Select model…'

  const effectiveEntry = allModels.find((e) => e.model === effectiveModel)

  // Group models by provider for the dropdown.
  const grouped = new Map<string, ModelEntry[]>()
  for (const entry of allModels) {
    const list = grouped.get(entry.provider) || []
    list.push(entry)
    grouped.set(entry.provider, list)
  }

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        type="button"
        className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[200px] truncate"
        onClick={() => setIsOpen((v) => !v)}
        title={effectiveEntry ? `${effectiveEntry.providerLabel}: ${effectiveModel}` : displayLabel}
      >
        <span className="truncate">{displayLabel}</span>
        <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && (
        <div className="absolute bottom-full left-0 mb-1 w-72 rounded-md border bg-popover shadow-md z-50 max-h-64 overflow-y-auto">
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
                {PROVIDER_LABELS[provider] || provider}
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

          {allModels.length === 0 && (
            <div className="px-3 py-2 text-xs text-muted-foreground italic">
              No models configured. Enable models in Settings.
            </div>
          )}
        </div>
      )}
    </div>
  )
}
