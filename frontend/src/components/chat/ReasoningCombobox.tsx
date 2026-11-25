import { useEffect, useState } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { findModelInfo } from '@/lib/modelId'

/**
 * ReasoningCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message reasoning effort. It reads AllModels from GetConfig to find the
 * native reasoning options for the currently selected model's family.
 * "Default" means no reasoning level is sent: the request omits the reasoning
 * parameter entirely and the provider's server-side default applies (which is
 * NOT necessarily the family's maximum option — the client does not translate
 * the empty selection into a wire value).
 * The selection is persisted in inputModeStore and survives restarts.
 * The reset effect below therefore only wipes the stored value on POSITIVE
 * evidence (the effective model resolved to a known entry whose family cannot
 * carry it) — never while the model list is still loading, or the wipe would
 * clobber the just-restored persisted value on every app start before the
 * async config fetch lands.
 *
 * The menu is built on the project's Radix `dropdown-menu` primitives (like
 * ModelPickerMenu). Radix portals the content to <body> and positions it with
 * the popper, so the option list escapes the chat input panel's own overflow:
 * the inline `absolute bottom-full` menu it replaced was clipped whenever the
 * input panel was at its minimum height. The menu is controlled (open state
 * owned here) so it can force-close when the selector locks — a portaled
 * panel is outside the toolbar's `pointer-events-none` lock wrapper and Radix
 * would otherwise leave it open and clickable past the lock.
 */
export function ReasoningCombobox({ disabled = false }: { disabled?: boolean }) {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const selectedReasoning = useInputModeStore((s) => s.selectedReasoning)
  const setSelectedReasoning = useInputModeStore((s) => s.setSelectedReasoning)

  const { allModels, defaultModel, loaded } = useConfigData()

  // When selectedModel changes, reset reasoning if the new model's family doesn't support it.
  // selectedModel may be a composite "provider/name" id (see ModelCombobox) or
  // null (use the global default_model, which may itself be bare or composite),
  // so resolve via findModelInfo which handles both forms.
  useEffect(() => {
    if (!selectedReasoning) return
    const effectiveModel = selectedModel ?? defaultModel
    const info = findModelInfo(allModels, effectiveModel)
    // Wipe only on POSITIVE evidence: the model resolved to a known entry and
    // that entry's family cannot carry the stored selection. While the config
    // is still loading (allModels empty — the store has already rehydrated the
    // persisted value synchronously) or the selector is stale/unresolvable,
    // findModelInfo returns undefined and the stored choice is left untouched:
    // wiping on unknown data would silently reset the user's persisted
    // reasoning level on every app start before the async model list arrives.
    if (info === undefined) return
    const reasoning = info.reasoning
    if (!reasoning || !reasoning.options.includes(selectedReasoning)) {
      setSelectedReasoning(null)
    }
  }, [selectedModel, defaultModel, allModels, selectedReasoning, setSelectedReasoning])

  // The menu is controlled so the lock effect below can own the open state —
  // an uncontrolled Radix DropdownMenu would stay open when its trigger turns
  // disabled. Force-close when the selector locks: the portaled content mounts
  // on <body>, outside the chat toolbar's `pointer-events-none` lock wrapper,
  // so a panel open across a lock transition (pause/compaction/auto-resume
  // flipping the toolbar's selectorsLocked) would otherwise remain fully
  // interactive past the lock. Restores the close-on-disabled-flip invariant
  // the pre-Radix useDropdown hook pinned.
  const [isOpen, setIsOpen] = useState(false)
  useEffect(() => {
    if (disabled) setIsOpen(false)
  }, [disabled])

  // Find reasoning info for the effective model.
  const effectiveModel = selectedModel ?? defaultModel
  const modelInfo = findModelInfo(allModels, effectiveModel)
  const reasoning = modelInfo?.reasoning ?? null

  // If still loading config, render nothing (matches pre-loaded behavior).
  if (!loaded) return null

  // If family doesn't support reasoning, render nothing.
  if (!reasoning) return null

  const options = reasoning.options

  const displayLabel = selectedReasoning ?? 'Default (provider)'

  return (
    <DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button
          type="button"
          disabled={disabled}
          className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[150px] truncate disabled:opacity-50 disabled:cursor-not-allowed"
          title={disabled ? 'Locked while the session is running' : `Reasoning: ${displayLabel}`}
        >
          <span className="truncate">{displayLabel}</span>
          <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M6 9l6 6 6-6" />
          </svg>
        </button>
      </DropdownMenuTrigger>

      <DropdownMenuContent align="start" className="w-48">
        {/* Default option */}
        <DropdownMenuItem
          className={cn(
            'gap-2 px-3 py-1.5 text-xs',
            !selectedReasoning && 'bg-primary/10 font-medium',
          )}
          title="No reasoning level is sent — the provider's server-side default applies"
          onSelect={() => setSelectedReasoning(null)}
        >
          <span className="flex-1 text-left">
            Default (provider)
          </span>
          {!selectedReasoning && (
            <span className="text-xs text-primary">active</span>
          )}
        </DropdownMenuItem>

        {/* Specific options */}
        {options.map((opt) => {
          const isSelected = selectedReasoning === opt
          return (
            <DropdownMenuItem
              key={opt}
              className={cn(
                'gap-2 px-3 py-1.5 text-xs',
                isSelected && 'bg-primary/10 font-medium',
              )}
              title={`Set reasoning effort to ${opt}`}
              onSelect={() => setSelectedReasoning(opt)}
            >
              <span className="flex-1 text-left">{opt}</span>
              {isSelected && (
                <span className="text-xs text-primary">selected</span>
              )}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
