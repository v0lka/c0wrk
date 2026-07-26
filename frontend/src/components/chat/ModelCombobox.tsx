import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData, invalidateConfigCache } from '@/hooks/useConfigData'
import { setDefaultModel } from '@/api/config'
import { useDropdown } from '@/hooks/useDropdown'
import { computeDropdownPosition, type DropdownPosition } from '@/lib/dropdownPosition'
import { compositeModelId, bareModel, findModelInfo } from '@/lib/modelId'

interface ModelEntry {
  /** Composite selector "provider/name" — the value sent to the backend. */
  id: string
  /** Bare model name — what is displayed to the user. */
  model: string
  /** Provider config key used for grouping. */
  provider: string
  /** Human-readable provider label for grouping headers. */
  providerLabel: string
}

/** Human-readable label for a provider config key. Fixed providers have
 *  canonical labels; OpenAI-compatible providers display their config key. */
function providerLabel(provider: string): string {
  switch (provider) {
    case 'anthropic':
      return 'Anthropic'
    case 'chatgpt':
      return 'ChatGPT'
    default:
      return provider
  }
}

/**
 * Portal positioning constants (viewport-space, pixels).
 *  - MAX_DROPDOWN_HEIGHT mirrors the `max-h-64` (256px) cap on the menu so the
 *    up/down decision is correct before the first measurement is available.
 *  - MIN_WIDTH keeps long model names readable (matches the previous `w-72`).
 *  - GAP is the space kept between the trigger and the menu.
 *  - Z_INDEX sits above the message input area (auto), chat area (z-10/z-20)
 *    and pending actions bar (auto), matching the project's popover/dialog
 *    `z-50` layer so the portaled menu is never covered.
 */
const MAX_DROPDOWN_HEIGHT = 256
const MIN_WIDTH = 288
const GAP = 6
const Z_INDEX = 50

/**
 * ModelCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message model override.  "Default" means the global default_model is used.
 * The selection is persisted in inputModeStore and survives restarts.
 *
 * The menu is rendered through a React portal to `document.body` with
 * `position: fixed` so it is never clipped by the message input area's
 * `overflow-hidden` ancestor (see ChatInput). It opens upward or downward
 * depending on available space and tracks window resize/scroll while open.
 */
export function ModelCombobox() {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const setSelectedModel = useInputModeStore((s) => s.setSelectedModel)

  const { allModels: modelInfos, defaultModel, loaded } = useConfigData()
  const { isOpen, setIsOpen, containerRef, menuRef } = useDropdown()

  const triggerRef = useRef<HTMLButtonElement>(null)
  const [position, setPosition] = useState<DropdownPosition | null>(null)

  // Convert ModelInfo[] → ModelEntry[] (flat list of enabled models per provider).
  // `id` is the composite selector sent to the backend; `model` is the bare name
  // shown to the user.
  const allModels: ModelEntry[] = useMemo(() => {
    const entries: ModelEntry[] = []
    for (const info of modelInfos) {
      entries.push({
        id: compositeModelId(info.provider, info.name),
        model: info.name,
        provider: info.provider,
        providerLabel: providerLabel(info.provider),
      })
    }
    return entries
  }, [modelInfos])

  // Build display label. selectedModel is a composite id (or null = default).
  // The global default_model may itself be a composite "provider/name" or a
  // legacy bare name — show only the bare model name to the user.
  const displayLabel = selectedModel
    ? bareModel(selectedModel)
    : defaultModel
      ? `Default: ${bareModel(defaultModel)}`
      : 'Select model…'

  const effectiveEntry = allModels.find((e) => e.id === (selectedModel ?? ''))

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

  // Resolve the global default_model (which may be a composite "provider/name"
  // or a legacy bare name) to the composite id of the entry it actually points
  // at, so the "default" badge lands on exactly one provider's entry — even
  // when the same bare name is exposed by multiple providers. Mirrors the
  // backend's bare-name resolution (first match in deterministic order).
  const effectiveDefaultId = useMemo(() => {
    if (!defaultModel) return null
    const info = findModelInfo(modelInfos, defaultModel)
    return info ? compositeModelId(info.provider, info.name) : null
  }, [modelInfos, defaultModel])

  const isLoading = !loaded

  // Position the portaled menu whenever it is open, and recompute on resize or
  // any scroll (capture phase so nested scroll containers are covered too).
  useLayoutEffect(() => {
    if (!isOpen) {
      setPosition(null)
      return
    }

    const recompute = () => {
      const trigger = triggerRef.current
      if (!trigger) return
      const rect = trigger.getBoundingClientRect()
      const menu = menuRef.current
      // Use the rendered height once available; otherwise fall back to the
      // max-height cap so the direction decision is correct on first paint.
      const dropdownHeight = menu && menu.offsetHeight > 0 ? menu.offsetHeight : MAX_DROPDOWN_HEIGHT
      setPosition(
        computeDropdownPosition({
          triggerRect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width },
          dropdownHeight,
          viewportHeight: window.innerHeight,
          viewportWidth: window.innerWidth,
          gap: GAP,
          minWidth: MIN_WIDTH,
        }),
      )
    }

    recompute()
    window.addEventListener('resize', recompute)
    window.addEventListener('scroll', recompute, true)
    return () => {
      window.removeEventListener('resize', recompute)
      window.removeEventListener('scroll', recompute, true)
    }
  }, [isOpen, menuRef])

  const close = () => {
    setIsOpen(false)
    triggerRef.current?.focus()
  }

  // Persist the picked model as the global default_model (LLM section of the
  // config). UpdateLLMConfig is a partial merge so only default_model is
  // written — provider configs and API keys are untouched. The per-message
  // override (selectedModel) is applied optimistically so the next message
  // uses the picked model immediately, even before the rebuilt router
  // propagates.
  //
  // Race handling: if the persist fails, the override is rolled back to its
  // previous value so the selector never advertises a default that was not
  // actually saved — otherwise the UI would show the picked model while the
  // backend default stayed unchanged, silently reverting on the next "Default"
  // selection or restart. The previous value is read fresh from the store
  // (not the render-scoped `selectedModel`) so it reflects any intervening
  // pick at click time. The config cache is invalidated on settle regardless,
  // so every consumer re-syncs with the actual backend state.
  const handleSelectModel = (id: string) => {
    const prev = useInputModeStore.getState().selectedModel
    setSelectedModel(id)
    setIsOpen(false)
    setDefaultModel(id)
      .catch(() => {
        // Persist failed: reconcile the override. setDefaultModel (api/config)
        // already logs the error, so nothing to surface here.
        setSelectedModel(prev)
      })
      .finally(() => invalidateConfigCache())
  }

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        ref={triggerRef}
        type="button"
        disabled={isLoading}
        className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[200px] truncate disabled:opacity-50 disabled:cursor-not-allowed"
        onClick={() => setIsOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === 'Escape' && isOpen) { e.stopPropagation(); close() } }}
        title={isLoading ? 'Loading models…' : effectiveEntry ? `${effectiveEntry.providerLabel}: ${effectiveEntry.model}` : displayLabel}
      >
        <span className="truncate">{isLoading ? 'Loading models\u2026' : displayLabel}</span>
        <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && createPortal(
        <div
          ref={menuRef}
          role="listbox"
          aria-label="Select model"
          className="rounded-md border bg-popover shadow-md max-h-64 overflow-y-auto custom-scrollbar"
          style={{
            position: 'fixed',
            top: position?.top ?? 0,
            left: position?.left ?? 0,
            width: position?.width ?? MIN_WIDTH,
            visibility: position ? 'visible' : 'hidden',
            zIndex: Z_INDEX,
          }}
          onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }}
        >
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
                  Default{defaultModel ? ` (${bareModel(defaultModel)})` : ''}
                </span>
                {!selectedModel && (
                  <span className="text-[10px] text-primary">active</span>
                )}
              </button>

              {/* Provider groups */}
              {Array.from(grouped.entries()).map(([provider, models]) => (
                <div key={provider}>
                  <div className="px-3 py-1 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider bg-muted/30">
                    {providerLabel(provider)}
                  </div>
                  {models.map((entry) => {
                    const isSelected = selectedModel === entry.id
                    // "default" badge marks the single entry the global
                    // default_model resolves to. default_model may be a
                    // composite "provider/name" or a legacy bare name, so
                    // compare against the resolved composite id — this pins
                    // the badge to exactly one provider even when the same
                    // bare name is exposed by multiple providers.
                    const isDefault = !selectedModel && entry.id === effectiveDefaultId
                    return (
                      <button
                        key={entry.id}
                        type="button"
                        className={`flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted ${isSelected ? 'bg-primary/10 font-medium' : ''}`}
                        onClick={() => { handleSelectModel(entry.id) }}
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
        </div>,
        document.body,
      )}
    </div>
  )
}
