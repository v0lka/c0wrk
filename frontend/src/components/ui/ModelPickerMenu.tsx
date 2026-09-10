import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useDropdown } from '@/hooks/useDropdown'
import { computeDropdownPosition, type DropdownPosition } from '@/lib/dropdownPosition'
import { compositeModelId, bareModel, findModelRef, type ModelRef } from '@/lib/modelId'
import { cn } from '@/lib/utils'

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

export interface ModelPickerEntry {
  /** Composite selector "provider/name" — the value sent to the backend. */
  id: string
  /** Bare model name — what is displayed to the user. */
  model: string
  /** Provider config key used for grouping. */
  provider: string
  /** Human-readable provider label for grouping headers. */
  providerLabel: string
}

/**
 * Convert a model list → ModelPickerEntry[] (flat list of models per
 * provider). `id` is the composite selector sent to the backend; `model` is
 * the bare name shown to the user.
 */
function toModelPickerEntries(models: readonly ModelRef[]): ModelPickerEntry[] {
  const entries: ModelPickerEntry[] = []
  for (const info of models) {
    entries.push({
      id: compositeModelId(info.provider, info.name),
      model: info.name,
      provider: info.provider,
      providerLabel: providerLabel(info.provider),
    })
  }
  return entries
}

/**
 * Group model entries by provider, preserving order. Shared by every
 * ModelPickerMenu consumer so all model pickers render the same provider
 * sections in the same order.
 */
function groupByProvider(entries: ModelPickerEntry[]): Map<string, ModelPickerEntry[]> {
  const map = new Map<string, ModelPickerEntry[]>()
  for (const entry of entries) {
    const list = map.get(entry.provider) || []
    list.push(entry)
    map.set(entry.provider, list)
  }
  return map
}

interface ModelPickerMenuProps {
  /** Models to list — any shape carrying a bare name + provider key
   *  (useConfigData's ModelInfo[], the settings dialog's draft configs). */
  models: readonly ModelRef[]
  /** The global default_model from config (composite or legacy bare name). */
  defaultModel: string
  /** Whether the model data has loaded at least once. */
  loaded: boolean
  /** Currently selected composite id ('' or null = the "Default" option). */
  value: string | null
  /** Fire when the user picks an entry (composite id) or the "Default" option
   *  (null). Not fired when hideDefaultOption hides that option. */
  onSelect: (id: string | null) => void
  /** Disable the trigger (e.g. session-pinning lock in the chat toolbar). */
  disabled?: boolean
  /** Accessible name for the trigger button and the portaled menu. */
  ariaLabel: string
  /** Trigger overrides; base styling comes from the chat toolbar combobox
   *  and merges via tailwind-merge (e.g. `w-full h-9 text-sm px-3`). */
  className?: string
  /** Hide the "Default" entry — for pickers whose value IS the global
   *  default (Settings → LLM), where a "use the default" option would be
   *  self-referential. The provider groups remain unchanged. */
  hideDefaultOption?: boolean
  /**
   * Render the portaled menu inside this element instead of document.body.
   * The settings modal sets `pointer-events: none` on <body>, which would
   * make a plain document.body portal inert (clicks on options fall through
   * and close the dialog) — the Radix dialog content keeps
   * `pointer-events: auto`, so portaling into it keeps the menu interactive.
   */
  portalContainer?: HTMLElement | null
  /**
   * Optional heading rendered above the provider groups (e.g. "Default model"
   * in the settings context). Renders nothing when omitted.
   */
  menuHeading?: string
}

/**
 * ModelPickerMenu — the reusable model-selection dropdown.
 *
 * This is the single implementation of the model picker UI: a compact trigger
 * button plus a portaled, provider-grouped menu with an optional "Default"
 * entry and default/selected badges. It is rendered through a React portal
 * with `position: fixed` so it is never clipped by `overflow-hidden`
 * ancestors, opens upward or downward depending on available space, and
 * tracks window resize/scroll while open.
 *
 * The component is purely presentational with respect to persistence: callers
 * own the selected value and decide what a pick means. The chat toolbar's
 * ModelCombobox adds the per-message override semantics (optimistic
 * inputModeStore write + queued default_model persistence); the Settings →
 * LLM section wires it directly to its config form state.
 *
 * Selection display contract: `value` selects an entry by composite id; the
 * trigger shows the bare model name. When `value` is null/'' the trigger
 * shows `Default: <name>` (the global default resolved to a listed entry), or
 * "Select model…" when even that cannot be resolved.
 */
export function ModelPickerMenu({
  models,
  defaultModel,
  loaded,
  value,
  onSelect,
  disabled = false,
  ariaLabel,
  className,
  hideDefaultOption = false,
  portalContainer,
  menuHeading,
}: ModelPickerMenuProps) {
  const { isOpen, setIsOpen, containerRef, menuRef } = useDropdown(disabled)

  const triggerRef = useRef<HTMLButtonElement>(null)
  const [position, setPosition] = useState<DropdownPosition | null>(null)

  const allModels = useMemo(() => toModelPickerEntries(models), [models])

  // Resolve the global default_model (which may be a composite "provider/name"
  // or a legacy bare name) to the composite id of the entry it actually points
  // at. A config refresh can temporarily contain a default that no longer has
  // an enabled entry; do not display that stale name in the selector.
  const effectiveDefaultId = useMemo(() => {
    if (!defaultModel) return null
    const info = findModelRef(models, defaultModel)
    return info ? compositeModelId(info.provider, info.name) : null
  }, [models, defaultModel])

  // Build display label. value is a composite id (or null/'' = default).
  // Only show the global default's bare name after it resolves to a listed
  // entry. This avoids advertising a model that is no longer selectable.
  const selected = value ? value : null
  const displayLabel = selected
    ? bareModel(selected)
    : effectiveDefaultId
      ? `Default: ${bareModel(defaultModel)}`
      : 'Select model…'

  const effectiveEntry = allModels.find((e) => e.id === (selected ?? ''))

  // Group models by provider for the dropdown.
  const grouped = useMemo(() => groupByProvider(allModels), [allModels])

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

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        ref={triggerRef}
        type="button"
        disabled={isLoading || disabled}
        aria-label={ariaLabel}
        className={cn(
          'flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background',
          'hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors',
          'max-w-[200px] truncate disabled:opacity-50 disabled:cursor-not-allowed',
          className,
        )}
        onClick={() => setIsOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === 'Escape' && isOpen) { e.stopPropagation(); close() } }}
        title={isLoading ? 'Loading models…' : disabled ? 'Locked while the session is running' : effectiveEntry ? `${effectiveEntry.providerLabel}: ${effectiveEntry.model}` : displayLabel}
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
          aria-label={ariaLabel}
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
              {menuHeading && (
                <div className="px-3 pt-2 pb-1 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                  {menuHeading}
                </div>
              )}

              {/* Default option */}
              {!hideDefaultOption && (
                <button
                  type="button"
                  className={cn(
                    'flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted',
                    !selected && 'bg-primary/10 font-medium',
                  )}
                  onClick={() => {
                    // "Default" is an immediately valid local choice (null =
                    // use the persisted global default). The caller decides
                    // whether this needs a backend round-trip.
                    onSelect(null)
                    setIsOpen(false)
                  }}
                >
                  <span className="flex-1 text-left">
                    Default{effectiveDefaultId ? ` (${bareModel(defaultModel)})` : ''}
                  </span>
                  {!selected && (
                    <span className="text-[10px] text-primary">active</span>
                  )}
                </button>
              )}

              {/* Provider groups */}
              {Array.from(grouped.entries()).map(([provider, modelsInGroup]) => (
                <div key={provider}>
                  <div className="px-3 py-1 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider bg-muted/30">
                    {providerLabel(provider)}
                  </div>
                  {modelsInGroup.map((entry) => {
                    const isSelected = selected === entry.id
                    // "default" badge marks the single entry the global
                    // default_model resolves to. default_model may be a
                    // composite "provider/name" or a legacy bare name, so
                    // compare against the resolved composite id — this pins
                    // the badge to exactly one provider even when the same
                    // bare name is exposed by multiple providers.
                    const isDefault = !selected && entry.id === effectiveDefaultId
                    return (
                      <button
                        key={entry.id}
                        type="button"
                        className={cn(
                          'flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted',
                          isSelected && 'bg-primary/10 font-medium',
                        )}
                        onClick={() => { onSelect(entry.id); setIsOpen(false) }}
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
        portalContainer ?? document.body,
      )}
    </div>
  )
}
